package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/memohai/connect-it/packages/connectors/internal/restkit"
	"github.com/memohai/connect-it/packages/connectors/internal/toolargs"
	"github.com/memohai/connect-it/packages/connectors/internal/toolfail"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const managedBaseURL = "https://gmail.googleapis.com/"

type managedHandler struct {
	transport restkit.Transport[string]
}

// NewHandlers constructs Gmail's Managed handlers from the process-wide
// providerkit Factory. The client policy is fixed in reviewed code; access
// tokens remain request-local and never become part of the client identity.
func NewHandlers(
	factory *providerkit.Factory,
) (connector.HandlerMap, error) {
	client, err := factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          managedBaseURL,
		AllowedOrigins:   []string{managedBaseURL},
		RedirectMode:     providerkit.RedirectDenyAll,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   providerkit.DefaultRequestTimeout,
		MaxResponseBytes: providerkit.DefaultMaxResponseBytes,
		Retry:            providerkit.DefaultRetryPolicy(),
	})
	if err != nil {
		return nil, err
	}
	return newHandlers(client), nil
}

func newHandlers(client *providerkit.Client) connector.HandlerMap {
	handler := &managedHandler{transport: restkit.Transport[string]{
		Connector: Definition.Type,
		Credentials: func(
			call connector.ToolCallContext,
		) (string, *connector.ToolFailure) {
			// The OAuth boundary normalizes the scheme; anything else is a
			// credential this handler must not present upstream.
			if call.TokenType != "Bearer" {
				return "", toolfail.New(
					connector.FailureAuthorizationFailed,
					0,
					0,
				)
			}
			return call.AccessToken, nil
		},
		Authorizer: func(
			token string,
		) (providerkit.Authorizer, *connector.ToolFailure) {
			authorizer, err := providerkit.Bearer(token)
			if err != nil {
				return nil, toolfail.New(
					connector.FailureAuthorizationFailed,
					0,
					0,
				)
			}
			return authorizer, nil
		},
		Client: func(string) (restkit.Lease, *connector.ToolFailure) {
			return restkit.Shared(client), nil
		},
	}}
	return connector.HandlerMap{
		"list_messages": handler.listMessages,
		"send_message":  handler.sendMessage,
	}
}

// invalidInput is the finished result for every argument rejected before any
// egress happens.
func invalidInput() (connector.ToolResultData, error) {
	return toolfail.Result(connector.FailureInvalidInput, 0, 0), nil
}

// listMessages calls GET /gmail/v1/users/me/messages.
func (handler *managedHandler) listMessages(
	ctx context.Context,
	call connector.ToolCallContext,
) (connector.ToolResultData, error) {
	maxResults, ok := toolargs.OptionalInteger(
		call.Arguments,
		"max_results",
		20,
		1,
		100,
	)
	if !ok {
		return invalidInput()
	}
	query := url.Values{
		"maxResults": {strconv.FormatInt(maxResults, 10)},
	}
	if value, present := call.Arguments["q"]; present {
		search, valid := toolargs.String(value, 1024, false)
		if !valid {
			return invalidInput()
		}
		if search != "" {
			query.Set("q", search)
		}
	}
	return handler.call(ctx, call, restkit.Request{
		Method: http.MethodGet,
		Path:   "/gmail/v1/users/me/messages",
		Query:  query,
	})
}

// sendMessage assembles an RFC 2822 text message and calls Gmail's send API.
func (handler *managedHandler) sendMessage(
	ctx context.Context,
	call connector.ToolCallContext,
) (connector.ToolResultData, error) {
	// to and subject become header fields, so they are read as single-line
	// text: rejecting every control character also rejects the CR/LF header
	// injection that would smuggle extra recipients into the envelope.
	to, okTo := toolargs.String(call.Arguments["to"], 512, false)
	subject, okSubject := toolargs.String(call.Arguments["subject"], 998, false)
	body, okBody := toolargs.String(call.Arguments["body"], 1<<20, true)
	if !okTo || !okSubject || !okBody ||
		to == "" || subject == "" || body == "" {
		return invalidInput()
	}
	rfc822 := fmt.Sprintf(
		"To: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		to,
		subject,
		body,
	)
	return handler.call(ctx, call, restkit.Request{
		Method: http.MethodPost,
		Path:   "/gmail/v1/users/me/messages/send",
		JSON: map[string]string{
			"raw": base64.URLEncoding.EncodeToString([]byte(rfc822)),
		},
	})
}

// call issues one guarded request and passes a successful JSON object through
// unchanged; the Tool output schema for these two Tools is the Provider's own.
func (handler *managedHandler) call(
	ctx context.Context,
	call connector.ToolCallContext,
	request restkit.Request,
) (connector.ToolResultData, error) {
	response, result, ok := handler.transport.Do(ctx, call, request)
	if !ok {
		return result, nil
	}
	var object map[string]json.RawMessage
	if err := response.DecodeJSON(&object); err != nil {
		return handler.transport.Fail(err), nil
	}
	if object == nil {
		return toolfail.Result(
			connector.FailureInvalidResponse,
			response.StatusCode,
			0,
		), nil
	}
	return connector.ToolResultData{
		Structured: append(json.RawMessage(nil), response.Body...),
	}, nil
}
