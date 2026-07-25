package providerkit

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

const maxRedirects = 10

var errProviderRequestTimeout = errors.New("providerkit: total request timeout")

// Client is a policy-bound Provider HTTP client. Its Transport and connection
// pool are never shared with a client constructed from a different Policy.
type Client struct {
	policy    Policy
	transport *guardedTransport
	observer  Observer
	clock     Clock
}

func newClient(
	policy Policy,
	transport *guardedTransport,
	observer Observer,
	clock Clock,
) *Client {
	return &Client{
		policy:    policy,
		transport: transport,
		observer:  observer,
		clock:     clock,
	}
}

// Policy returns a defensive copy of the normalized, non-secret client policy.
func (client *Client) Policy() Policy {
	if client == nil {
		return Policy{}
	}
	out := client.policy
	out.AllowedOrigins = append([]string(nil), client.policy.AllowedOrigins...)
	out.AllowedRedirectOrigins = append(
		[]string(nil),
		client.policy.AllowedRedirectOrigins...,
	)
	return out
}

// Do executes one logical Provider request. The configured timeout covers all
// DNS, dial, TLS, redirect, retry, backoff and bounded response-read work.
//
// HTTP error statuses return both the bounded Response and a safe typed error,
// so Provider adapters may inspect a documented error envelope without ever
// accidentally treating a 4xx/5xx response as success.
func (client *Client) Do(
	ctx context.Context,
	input Request,
) (*Response, error) {
	if client == nil || client.transport == nil || client.clock == nil {
		return nil, knownError(
			connector.FailureConfigurationError,
			defaultSafeMessage(connector.FailureConfigurationError),
		)
	}
	if ctx == nil {
		return nil, knownError(
			connector.FailureInvalidInput,
			defaultSafeMessage(connector.FailureInvalidInput),
		)
	}
	if err := validateRequestLabels(
		input.Labels,
		client.policy.Provider,
	); err != nil {
		return nil, err
	}
	if input.Authorizer == nil {
		input.Authorizer = NoAuth()
	}

	requestContext, cancel := context.WithTimeoutCause(
		ctx,
		client.policy.RequestTimeout,
		errProviderRequestTimeout,
	)
	defer cancel()
	frozenInput, freezeErr := freezeRequestBody(input)
	if freezeErr != nil {
		return nil, normalizeClientError(
			ctx,
			requestContext,
			freezeErr,
		)
	}
	input = frozenInput

	for attempt := 1; ; attempt++ {
		built, buildErr := buildRequest(
			requestContext,
			client.policy.BaseURL,
			input,
		)
		if buildErr != nil {
			return nil, normalizeClientError(ctx, requestContext, buildErr)
		}

		result := client.doAttempt(
			requestContext,
			input.Labels,
			built,
			attempt,
		)
		// Transport/read failures are retried by error classification. Completed
		// HTTP responses are retried by status, even when StatusError is non-nil.
		var (
			response      *Response
			attemptErr    error
			retryErr      error
			responseBytes int64
			status        = result.status
			header        http.Header
		)
		if result.err != nil {
			attemptErr = normalizeClientError(
				ctx,
				requestContext,
				result.err,
			)
			retryErr = attemptErr
		} else {
			status = result.response.StatusCode
			response, attemptErr = ReadResponse(
				result.response,
				client.policy.MaxResponseBytes,
			)
			if attemptErr != nil {
				header = result.response.Header
				attemptErr = normalizeClientError(
					ctx,
					requestContext,
					attemptErr,
				)
				retryErr = attemptErr
			} else {
				status = response.StatusCode
				header = response.Header
				attemptErr = response.StatusError(client.clock.Now())
				responseBytes = int64(len(response.Body))
			}
		}

		client.observe(
			requestContext,
			input.Labels,
			result.request,
			result.startedAt,
			attempt,
			status,
			attemptErr,
			responseBytes,
		)

		decision := decideRetry(
			client.policy.Retry,
			built.request.Method,
			built.retryWrite,
			attempt-1,
			status,
			header,
			retryErr,
			client.clock.Now(),
		)
		if !decision.retry {
			return response, attemptErr
		}
		if sleepErr := client.clock.Sleep(
			requestContext,
			decision.delay,
		); sleepErr != nil {
			return nil, normalizeClientError(
				ctx,
				requestContext,
				sleepErr,
			)
		}
	}
}

type clientAttemptResult struct {
	response  *http.Response
	request   *http.Request
	startedAt time.Time
	status    int
	err       error
}

// doAttempt executes one retry attempt and owns its redirect chain. Intermediate
// redirect responses are observed immediately; the final response remains
// unread so Do can apply the ordinary bounded-response and retry contracts.
func (client *Client) doAttempt(
	ctx context.Context,
	labels RequestLabels,
	built builtRequest,
	attempt int,
) clientAttemptResult {
	redirects := &redirectPolicy{
		policy:    client.policy,
		footprint: built.footprint,
		credentials: captureRedirectCredentials(
			built.request,
			built.footprint,
		),
	}
	via := make([]*http.Request, 0, maxRedirects+1)
	current := built.request

	for {
		via = append(via, current)
		startedAt := client.clock.Now()
		response, err := client.transport.RoundTrip(current)
		if err != nil {
			return clientAttemptResult{
				request:   current,
				startedAt: startedAt,
				err:       err,
			}
		}
		if responseErr := validateResponseEnvelope(response); responseErr != nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			return clientAttemptResult{
				request:   current,
				startedAt: startedAt,
				err:       responseErr,
			}
		}

		next, follow, redirectErr := buildRedirectRequest(response, current)
		if redirectErr != nil {
			_ = response.Body.Close()
			return clientAttemptResult{
				request:   current,
				startedAt: startedAt,
				status:    response.StatusCode,
				err:       redirectErr,
			}
		}
		if !follow {
			return clientAttemptResult{
				response:  response,
				request:   current,
				startedAt: startedAt,
			}
		}
		if err := redirects.Check(next, via); err != nil {
			closeRequestBody(next)
			_ = response.Body.Close()
			return clientAttemptResult{
				request:   current,
				startedAt: startedAt,
				status:    response.StatusCode,
				err:       err,
			}
		}

		// Redirect bodies are discarded rather than exposed to Provider
		// adapters. No body bytes were consumed, so this hop records zero.
		_ = response.Body.Close()
		client.observe(
			ctx,
			labels,
			current,
			startedAt,
			attempt,
			response.StatusCode,
			nil,
			0,
		)
		current = next
	}
}

// CloseIdleConnections releases this policy-bound client's idle pool.
func (client *Client) CloseIdleConnections() {
	if client != nil && client.transport != nil {
		client.transport.CloseIdleConnections()
	}
}

type redirectPolicy struct {
	policy      Policy
	footprint   CredentialFootprint
	credentials *redirectCredentialSnapshot
	tainted     bool
}

type redirectCredentialSnapshot struct {
	headers map[string][]string
	query   map[string][]string
}

var crossOriginSafeRequestHeaders = map[string]struct{}{
	"Accept":          {},
	"Accept-Language": {},
	"Cache-Control":   {},
	"User-Agent":      {},
}

func (policy *redirectPolicy) Check(
	request *http.Request,
	via []*http.Request,
) error {
	if request == nil || request.URL == nil || len(via) == 0 {
		return policyViolation(
			PolicyOutcomeBlockedRedirect,
			"provider redirect was denied by policy",
		)
	}

	// net/http prepares Referer before invoking CheckRedirect. Delete it for
	// every hop, including same-origin redirects.
	request.Header.Del("Referer")
	if policy.policy.RedirectMode == RedirectDenyAll {
		return policyViolation(
			PolicyOutcomeBlockedRedirect,
			"provider redirect was denied by policy",
		)
	}
	if len(via) > maxRedirects {
		return policyViolation(
			PolicyOutcomeBlockedRedirect,
			"provider redirect was denied by policy",
		)
	}

	target, err := ParseAndValidateURL(request.URL.String())
	if err != nil {
		return policyViolation(
			PolicyOutcomeBlockedRedirect,
			"provider redirect was denied by policy",
		)
	}
	previous := via[len(via)-1]
	if previous.URL == nil {
		return policyViolation(
			PolicyOutcomeBlockedRedirect,
			"provider redirect was denied by policy",
		)
	}
	previousURL, err := ParseAndValidateURL(previous.URL.String())
	if err != nil {
		return policyViolation(
			PolicyOutcomeBlockedRedirect,
			"provider redirect was denied by policy",
		)
	}
	if previousURL.Scheme == "https" && target.Scheme == "http" {
		return policyViolation(
			PolicyOutcomeBlockedRedirect,
			"provider redirect was denied by policy",
		)
	}

	sameOrigin, err := IsSameOrigin(previousURL, target)
	if err != nil {
		return policyViolation(
			PolicyOutcomeBlockedRedirect,
			"provider redirect was denied by policy",
		)
	}
	targetOrigin, err := CanonicalOrigin(target)
	if err != nil {
		return policyViolation(
			PolicyOutcomeBlockedRedirect,
			"provider redirect was denied by policy",
		)
	}
	if !sameOrigin {
		if !redirectableWithoutBody(previous) ||
			!slices.Contains(
				policy.policy.AllowedRedirectOrigins,
				targetOrigin,
			) {
			return policyViolation(
				PolicyOutcomeBlockedRedirect,
				"provider redirect was denied by policy",
			)
		}
		policy.tainted = true
	}
	request.URL = target
	if policy.tainted {
		if err := stripCredentialFootprint(
			request,
			policy.footprint,
		); err != nil {
			return policyViolation(
				PolicyOutcomeBlockedRedirect,
				"provider redirect was denied by policy",
			)
		}
	} else if policy.credentials != nil {
		if err := restoreRedirectCredentials(
			request,
			policy.credentials,
		); err != nil {
			return policyViolation(
				PolicyOutcomeBlockedRedirect,
				"provider redirect was denied by policy",
			)
		}
	}

	approveRedirect(request, targetOrigin)
	return nil
}

func captureRedirectCredentials(
	request *http.Request,
	footprint CredentialFootprint,
) *redirectCredentialSnapshot {
	snapshot := &redirectCredentialSnapshot{
		headers: make(map[string][]string, len(footprint.HeaderNames)),
		query:   make(map[string][]string, len(footprint.QueryParamNames)),
	}
	if request == nil || request.URL == nil {
		return snapshot
	}
	for _, rawName := range footprint.HeaderNames {
		name := http.CanonicalHeaderKey(rawName)
		values, exists := request.Header[name]
		if !exists {
			snapshot.headers[name] = nil
			continue
		}
		snapshot.headers[name] = append([]string(nil), values...)
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		for _, name := range footprint.QueryParamNames {
			snapshot.query[name] = nil
		}
		return snapshot
	}
	for _, name := range footprint.QueryParamNames {
		values, exists := query[name]
		if !exists {
			snapshot.query[name] = nil
			continue
		}
		snapshot.query[name] = append([]string(nil), values...)
	}
	return snapshot
}

func restoreRedirectCredentials(
	request *http.Request,
	snapshot *redirectCredentialSnapshot,
) error {
	if request == nil || request.URL == nil || snapshot == nil {
		return errors.New("providerkit: redirect credential state is invalid")
	}
	if request.Header == nil {
		request.Header = make(http.Header)
	}
	for name, values := range snapshot.headers {
		deleteHeaderFold(request.Header, name)
		if values != nil {
			request.Header[name] = append([]string(nil), values...)
		}
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return errors.New("providerkit: redirect query is invalid")
	}
	for name, values := range snapshot.query {
		query.Del(name)
		for _, value := range values {
			query.Add(name, value)
		}
	}
	request.URL.RawQuery = query.Encode()
	return nil
}

func redirectableWithoutBody(request *http.Request) bool {
	if request == nil {
		return false
	}
	switch request.Method {
	case http.MethodGet, http.MethodHead:
	default:
		return false
	}
	return request.Body == nil || request.Body == http.NoBody
}

func stripCredentialFootprint(
	request *http.Request,
	footprint CredentialFootprint,
) error {
	if request == nil || request.URL == nil {
		return errors.New("providerkit: redirect request is invalid")
	}
	// A header outside the Authorizer footprint can still carry protocol
	// credentials (for example Mcp-Session-Id). Once a chain crosses an
	// origin, retain only a deliberately narrow set of representation/cache
	// negotiation headers. Unknown and Provider-specific headers fail closed.
	for rawName := range request.Header {
		name := http.CanonicalHeaderKey(rawName)
		if _, safe := crossOriginSafeRequestHeaders[name]; !safe {
			delete(request.Header, rawName)
		}
	}
	for _, header := range []string{
		"Authorization",
		"Cookie",
		"Proxy-Authorization",
	} {
		deleteHeaderFold(request.Header, header)
	}
	for _, header := range footprint.HeaderNames {
		deleteHeaderFold(request.Header, header)
	}

	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return errors.New("providerkit: redirect query is invalid")
	}
	for _, name := range footprint.QueryParamNames {
		query.Del(name)
	}
	request.URL.RawQuery = query.Encode()
	return nil
}

func authorizerFootprint(authorizer Authorizer) (
	footprint CredentialFootprint,
	err error,
) {
	if authorizer == nil {
		return CredentialFootprint{}, knownError(
			connector.FailureConfigurationError,
			defaultSafeMessage(connector.FailureConfigurationError),
		)
	}
	defer func() {
		if recover() != nil {
			footprint = CredentialFootprint{}
			err = knownError(
				connector.FailureConfigurationError,
				defaultSafeMessage(connector.FailureConfigurationError),
			)
		}
	}()

	footprint = authorizer.Footprint().Clone()
	seenHeaders := make(map[string]struct{}, len(footprint.HeaderNames))
	headers := make([]string, 0, len(footprint.HeaderNames))
	for _, raw := range footprint.HeaderNames {
		trimmed := strings.TrimSpace(raw)
		header := http.CanonicalHeaderKey(trimmed)
		if raw != trimmed || !validHeaderName(header) ||
			isReservedAuthorizerHeader(header) {
			return CredentialFootprint{}, knownError(
				connector.FailureConfigurationError,
				defaultSafeMessage(connector.FailureConfigurationError),
			)
		}
		if _, exists := seenHeaders[header]; exists {
			continue
		}
		seenHeaders[header] = struct{}{}
		headers = append(headers, header)
	}

	seenQuery := make(map[string]struct{}, len(footprint.QueryParamNames))
	queryNames := make([]string, 0, len(footprint.QueryParamNames))
	for _, name := range footprint.QueryParamNames {
		if !validQueryName(name) {
			return CredentialFootprint{}, knownError(
				connector.FailureConfigurationError,
				defaultSafeMessage(connector.FailureConfigurationError),
			)
		}
		if _, exists := seenQuery[name]; exists {
			continue
		}
		seenQuery[name] = struct{}{}
		queryNames = append(queryNames, name)
	}
	footprint.HeaderNames = headers
	footprint.QueryParamNames = queryNames
	return footprint, nil
}

func validateRequestLabels(labels RequestLabels, provider string) error {
	if labels.ConnectorType == "" ||
		labels.ConnectorType != provider ||
		(labels.ToolID == "") == (labels.Operation == "") ||
		(labels.ConnectionID == "") == (labels.AuthorizationID == "") {
		return knownError(
			connector.FailureConfigurationError,
			defaultSafeMessage(connector.FailureConfigurationError),
		)
	}
	for _, value := range []string{
		labels.ConnectorType,
		labels.ToolID,
		labels.Operation,
		labels.ConnectionID,
		labels.AuthorizationID,
	} {
		if len(value) > 256 || containsControl(value) ||
			strings.TrimSpace(value) != value {
			return knownError(
				connector.FailureConfigurationError,
				defaultSafeMessage(connector.FailureConfigurationError),
			)
		}
	}
	return nil
}

func normalizeClientError(
	callerContext context.Context,
	requestContext context.Context,
	err error,
) error {
	if err == nil {
		return nil
	}
	if requestContext != nil &&
		errors.Is(context.Cause(requestContext), errProviderRequestTimeout) {
		return knownError(
			connector.FailureTimeout,
			defaultSafeMessage(connector.FailureTimeout),
			WithCause(err),
		)
	}
	if callerContext != nil && callerContext.Err() != nil {
		return knownError(
			connector.FailureCanceled,
			defaultSafeMessage(connector.FailureCanceled),
			WithCause(err),
		)
	}
	var violation *policyViolationError
	if errors.As(err, &violation) && violation != nil {
		return violation
	}
	var providerError *Error
	if errors.As(err, &providerError) && providerError != nil {
		return providerError
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) {
		return knownError(
			connector.FailureCanceled,
			defaultSafeMessage(connector.FailureCanceled),
			WithCause(err),
		)
	}
	return knownError(
		connector.FailureUpstreamUnavailable,
		defaultSafeMessage(connector.FailureUpstreamUnavailable),
		WithCause(err),
	)
}

func (client *Client) observe(
	ctx context.Context,
	labels RequestLabels,
	request *http.Request,
	startedAt time.Time,
	attempt int,
	status int,
	err error,
	responseBytes int64,
) {
	if client == nil || request == nil || request.URL == nil {
		return
	}
	event := RequestEvent{
		Labels:         labels,
		ProviderHost:   request.URL.Hostname(),
		Method:         request.Method,
		Duration:       client.clock.Now().Sub(startedAt),
		Attempt:        attempt,
		UpstreamStatus: status,
		ResponseBytes:  responseBytes,
		PolicyOutcome:  policyOutcomeForError(err),
	}
	var providerError *Error
	if errors.As(err, &providerError) && providerError != nil {
		event.ErrorCode = providerError.Code()
	}
	ObserveSafely(ctx, client.observer, event)
}

func statusFailureCode(status int) connector.FailureCode {
	switch status {
	case http.StatusUnauthorized:
		return connector.FailureAuthorizationFailed
	case http.StatusForbidden:
		return connector.FailurePermissionDenied
	case http.StatusNotFound:
		return connector.FailureNotFound
	case http.StatusConflict:
		return connector.FailureConflict
	case http.StatusTooManyRequests:
		return connector.FailureRateLimited
	default:
		if status >= 500 && status <= 599 {
			return connector.FailureUpstreamUnavailable
		}
		if status >= 400 && status <= 499 {
			return connector.FailureProviderError
		}
		return ""
	}
}
