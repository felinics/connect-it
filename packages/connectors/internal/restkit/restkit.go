// Package restkit is the plumbing every Managed REST connector repeats:
// project the Connection credential, lease the reviewed providerkit Client for
// that credential, build the authorizer, issue one guarded request, and map an
// egress error through the connector's own failure table.
//
// A connector declares a Transport once and its handlers become request
// descriptions. Do runs a whole Tool call; Send runs one request for
// already-projected credentials and hands back the audited response with the
// raw egress error, which is what a GraphQL envelope or a credential
// validation needs. Response projection, cursor pagination and multi-step
// calls stay hand-written in the connector; this layer does not absorb them.
package restkit

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"

	"github.com/memohai/connect-it/packages/connectors/internal/toolfail"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

// Lease is one providerkit Client checked out for a single operation. A nil
// Failure from a Transport's Client hook means the lease is usable; Close is
// safe on the zero value and is a no-op for a shared static Client.
type Lease struct {
	Client *providerkit.Client

	release func()
}

// Shared leases a long-lived Client owned by the connector's client source.
func Shared(client *providerkit.Client) Lease {
	return Lease{Client: client}
}

// Owned leases a Client derived from one Connection credential. Close releases
// its connection pool, so one Connection's sockets can never be reused by
// another.
func Owned(client *providerkit.Client) Lease {
	if client == nil {
		return Lease{}
	}
	return Lease{Client: client, release: client.CloseIdleConnections}
}

// Close releases a leased Client.
func (lease Lease) Close() {
	if lease.release != nil {
		lease.release()
	}
}

// Transport declares how one Managed connector turns a Tool call into a
// guarded Provider request. C is the connector's normalized credential type.
// Every hook returns a nil *connector.ToolFailure on success, so no step can
// be mistaken for "succeeded" by forgetting a sentinel comparison.
type Transport[C any] struct {
	// Connector labels every request for observability and policy.
	Connector connector.Type
	// Credentials projects and validates the Connection credential.
	Credentials func(connector.ToolCallContext) (C, *connector.ToolFailure)
	// Authorizer builds the complete outbound credential footprint.
	Authorizer func(C) (providerkit.Authorizer, *connector.ToolFailure)
	// Client leases the reviewed Client for this exact credential.
	Client func(C) (Lease, *connector.ToolFailure)
	// ClientFor replaces Client when the lease depends on the request too.
	// Notion and Stripe lease a retrying Client for a GET and a no-retry
	// Client for a write, so no write can be replayed by transport policy.
	ClientFor func(C, Request) (Lease, *connector.ToolFailure)
	// Headers are sent on every request (Accept, Provider API version, ...).
	Headers http.Header
	// MaxRequestBytes caps the encoded request body. Zero leaves it to the
	// Provider.
	MaxRequestBytes int
	// FailureRemap carries this connector's HTTP-status overrides. 401 is
	// never remappable: it is the credential-invalid signal.
	FailureRemap map[int]connector.FailureCode
	// RetryAfter is this connector's Retry-After contract. It runs whenever
	// the Provider actually answered and receives the audited response plus
	// the delay providerkit already parsed from it, so a connector can keep,
	// refuse or re-parse that delay. A nil hook republishes providerkit's.
	RetryAfter func(*providerkit.Response, int) int
}

// Request is one Provider call. Exactly one of JSON and Form may be set.
type Request struct {
	Method string
	Path   string
	Query  url.Values
	JSON   any
	Form   url.Values
}

// Labels identify one Tool call for observability and policy.
func (transport Transport[C]) Labels(
	call connector.ToolCallContext,
) providerkit.RequestLabels {
	return providerkit.RequestLabels{
		ConnectorType: string(transport.Connector),
		ToolID:        call.ToolID,
		ConnectionID:  call.ConnectionID,
	}
}

// Do issues request for call. ok is false when the call must not or did not
// reach the Provider, and the returned result is then the finished handler
// result. The audited response is returned even then: a Provider that answered
// with a failure status still carries headers and a body its connector may
// need. The leased Client is always released before Do returns.
func (transport Transport[C]) Do(
	ctx context.Context,
	call connector.ToolCallContext,
	request Request,
) (*providerkit.Response, connector.ToolResultData, bool) {
	credentials, failure := transport.Credentials(call)
	if failure != nil {
		return nil, toolfail.Of(failure), false
	}
	response, failure, err := transport.Send(
		ctx,
		transport.Labels(call),
		credentials,
		request,
	)
	switch {
	case failure != nil:
		return response, toolfail.Of(failure), false
	case err != nil:
		return response, toolfail.Of(transport.Failure(response, err)), false
	}
	return response, connector.ToolResultData{}, true
}

// Send issues one guarded request for already-projected credentials. It hands
// back the audited response together with the raw egress error, so a connector
// whose classification needs both — and a credential validation, which has no
// Tool call to project — can map them itself. A non-nil failure means the
// request never left the process. The leased Client is always released.
func (transport Transport[C]) Send(
	ctx context.Context,
	labels providerkit.RequestLabels,
	credentials C,
	request Request,
) (*providerkit.Response, *connector.ToolFailure, error) {
	authorizer, failure := transport.Authorizer(credentials)
	if failure != nil {
		return nil, failure, nil
	}
	if failure := transport.budget(request); failure != nil {
		return nil, failure, nil
	}
	lease, failure := transport.lease(credentials, request)
	if failure != nil {
		return nil, failure, nil
	}
	defer lease.Close()
	response, err := lease.Client.Do(ctx, providerkit.Request{
		Method:     request.Method,
		URL:        request.Path,
		Query:      request.Query,
		JSON:       request.JSON,
		Form:       request.Form,
		Authorizer: authorizer,
		Headers:    transport.Headers,
		Labels:     labels,
	})
	return response, nil, err
}

// budget refuses an oversized request body before any egress happens.
func (transport Transport[C]) budget(request Request) *connector.ToolFailure {
	if transport.MaxRequestBytes <= 0 {
		return nil
	}
	encoded := 0
	switch {
	case request.JSON != nil:
		body, err := json.Marshal(request.JSON)
		if err != nil {
			return toolfail.New(connector.FailureInternalError, 0, 0)
		}
		encoded = len(body)
	case request.Form != nil:
		encoded = len(request.Form.Encode())
	}
	if encoded > transport.MaxRequestBytes {
		return toolfail.New(connector.FailureInvalidInput, 0, 0)
	}
	return nil
}

// lease checks out the Client for one request. A Transport declaring neither
// hook fails closed rather than reaching the Provider unpoliced.
func (transport Transport[C]) lease(
	credentials C,
	request Request,
) (Lease, *connector.ToolFailure) {
	switch {
	case transport.ClientFor != nil:
		return transport.ClientFor(credentials, request)
	case transport.Client != nil:
		return transport.Client(credentials)
	default:
		return Lease{},
			toolfail.New(connector.FailureConfigurationError, 0, 0)
	}
}

// Failure maps one Provider or response-decoding error through this
// connector's failure table. response is the audited response whenever the
// Provider answered, which is what lets RetryAfter decide the republished
// delay; 401 keeps neither an override nor a delay, because it is the
// credential-invalid signal.
func (transport Transport[C]) Failure(
	response *providerkit.Response,
	err error,
) *connector.ToolFailure {
	failure := toolfail.FromProvider(err, transport.FailureRemap).Failure
	if transport.RetryAfter == nil || response == nil || failure == nil ||
		failure.UpstreamStatus == http.StatusUnauthorized {
		return failure
	}
	return toolfail.New(
		failure.Code,
		failure.UpstreamStatus,
		transport.RetryAfter(response, failure.RetryAfterSeconds),
	)
}

// Fail is the finished handler result for the errors Do cannot see, such as a
// rejected response body.
func (transport Transport[C]) Fail(err error) connector.ToolResultData {
	return toolfail.Of(transport.Failure(nil, err))
}
