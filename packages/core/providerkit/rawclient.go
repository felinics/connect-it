package providerkit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

// HTTPClient returns a guarded streaming client for protocols such as Remote
// MCP that require *http.Client. The Authorizer and labels are fixed at
// construction. The returned client applies one total timeout across DNS,
// dial, redirects, response headers, and streamed response reads.
//
// Redirects are followed inside the guarded RoundTripper instead of by
// net/http.Client. This is intentional: net/http otherwise places the raw
// Location or request URL in *url.Error. Internal redirect handling also keeps
// injected credentials on same-origin hops and permanently strips them after
// the first cross-origin hop.
func (client *Client) HTTPClient(
	authorizer Authorizer,
	labels RequestLabels,
) (*http.Client, error) {
	if client == nil || client.transport == nil || client.clock == nil {
		return nil, knownError(
			connector.FailureConfigurationError,
			defaultSafeMessage(connector.FailureConfigurationError),
		)
	}
	if err := validateRequestLabels(labels, client.policy.Provider); err != nil {
		return nil, err
	}
	if authorizer == nil {
		authorizer = NoAuth()
	}
	footprint, err := authorizerFootprint(authorizer)
	if err != nil {
		return nil, err
	}
	if footprint.Body {
		return nil, knownError(
			connector.FailureConfigurationError,
			defaultSafeMessage(connector.FailureConfigurationError),
		)
	}

	adapter := &streamingRoundTripper{
		client:     client,
		authorizer: authorizer,
		footprint:  footprint,
		labels:     labels,
	}
	return &http.Client{
		Transport: adapter,
		Jar:       nil,
		// The adapter owns the total timeout so streamed body errors can be
		// normalized as provider timeout versus caller cancellation.
		Timeout: 0,
		// Supported redirects are consumed by adapter.RoundTrip. This safe
		// fallback never returns a CheckRedirect error, because net/http
		// would include the untrusted Location value in *url.Error.
		CheckRedirect: func(
			*http.Request,
			[]*http.Request,
		) error {
			return http.ErrUseLastResponse
		},
	}, nil
}

type streamingRoundTripper struct {
	client     *Client
	authorizer Authorizer
	footprint  CredentialFootprint
	labels     RequestLabels
}

var _ interface{ CloseIdleConnections() } = (*streamingRoundTripper)(nil)

// CloseIdleConnections lets the standard http.Client lifecycle API release
// the guarded transport's policy-bound idle pool.
func (adapter *streamingRoundTripper) CloseIdleConnections() {
	if adapter != nil && adapter.client != nil {
		adapter.client.CloseIdleConnections()
	}
}

func (adapter *streamingRoundTripper) RoundTrip(
	request *http.Request,
) (*http.Response, error) {
	if adapter == nil || adapter.client == nil ||
		request == nil || request.URL == nil {
		return nil, knownError(
			connector.FailureConfigurationError,
			defaultSafeMessage(connector.FailureConfigurationError),
		)
	}

	callerContext := request.Context()
	requestContext, cancel := context.WithTimeoutCause(
		callerContext,
		adapter.client.policy.RequestTimeout,
		errProviderRequestTimeout,
	)
	outbound := request.Clone(requestContext)
	if err := validateStreamingRequest(outbound, adapter.footprint); err != nil {
		cancel()
		sanitizeErrorFacingRequest(request)
		return nil, err
	}
	if _, err := applyAuthorizer(
		outbound,
		adapter.authorizer,
		adapter.footprint,
		nil,
	); err != nil {
		cancel()
		sanitizeErrorFacingRequest(request)
		return nil, err
	}

	redirects := &redirectPolicy{
		policy:    adapter.client.policy,
		footprint: adapter.footprint,
		credentials: captureRedirectCredentials(
			outbound,
			adapter.footprint,
		),
	}
	via := make([]*http.Request, 0, maxRedirects+1)
	current := outbound

	for {
		via = append(via, current)
		startedAt := adapter.client.clock.Now()
		response, err := adapter.client.transport.RoundTrip(current)
		if err != nil {
			return adapter.fail(
				request,
				callerContext,
				requestContext,
				cancel,
				current,
				startedAt,
				0,
				err,
			)
		}
		if responseErr := validateResponseEnvelope(response); responseErr != nil {
			if response != nil && response.Body != nil {
				_ = response.Body.Close()
			}
			return adapter.fail(
				request,
				callerContext,
				requestContext,
				cancel,
				current,
				startedAt,
				0,
				responseErr,
			)
		}

		next, follow, redirectErr := buildRedirectRequest(
			response,
			current,
		)
		if redirectErr != nil {
			_ = response.Body.Close()
			return adapter.fail(
				request,
				callerContext,
				requestContext,
				cancel,
				current,
				startedAt,
				response.StatusCode,
				redirectErr,
			)
		}
		if follow {
			if err := redirects.Check(next, via); err != nil {
				closeRequestBody(next)
				_ = response.Body.Close()
				return adapter.fail(
					request,
					callerContext,
					requestContext,
					cancel,
					current,
					startedAt,
					response.StatusCode,
					err,
				)
			}
			_ = response.Body.Close()
			adapter.client.observe(
				requestContext,
				adapter.labels,
				current,
				startedAt,
				1,
				response.StatusCode,
				nil,
				0,
			)
			current = next
			continue
		}

		return adapter.finishResponse(
			request,
			callerContext,
			requestContext,
			cancel,
			current,
			startedAt,
			response,
		)
	}
}

func (adapter *streamingRoundTripper) fail(
	errorFacingRequest *http.Request,
	callerContext context.Context,
	requestContext context.Context,
	cancel context.CancelFunc,
	observedRequest *http.Request,
	startedAt time.Time,
	status int,
	err error,
) (*http.Response, error) {
	normalized := normalizeClientError(
		callerContext,
		requestContext,
		err,
	)
	adapter.client.observe(
		requestContext,
		adapter.labels,
		observedRequest,
		startedAt,
		1,
		status,
		normalized,
		0,
	)
	cancel()
	sanitizeErrorFacingRequest(errorFacingRequest)
	return nil, normalized
}

func (adapter *streamingRoundTripper) finishResponse(
	errorFacingRequest *http.Request,
	callerContext context.Context,
	requestContext context.Context,
	cancel context.CancelFunc,
	observedRequest *http.Request,
	startedAt time.Time,
	response *http.Response,
) (*http.Response, error) {
	if responseErr := validateResponseBody(
		response,
		adapter.client.policy.MaxResponseBytes,
	); responseErr != nil {
		_ = response.Body.Close()
		return adapter.fail(
			errorFacingRequest,
			callerContext,
			requestContext,
			cancel,
			observedRequest,
			startedAt,
			response.StatusCode,
			responseErr,
		)
	}

	statusErr := streamingStatusError(response.StatusCode)
	response.Body = &boundedStreamingBody{
		body:           response.Body,
		max:            adapter.client.policy.MaxResponseBytes,
		status:         response.StatusCode,
		callerContext:  callerContext,
		requestContext: requestContext,
		cancel:         cancel,
		done: func(bytesRead int64, bodyErr error) {
			eventErr := statusErr
			if bodyErr != nil {
				eventErr = bodyErr
			}
			adapter.client.observe(
				requestContext,
				adapter.labels,
				observedRequest,
				startedAt,
				1,
				response.StatusCode,
				eventErr,
				bytesRead,
			)
		},
	}
	response.Request = sanitizedResponseRequest(errorFacingRequest)
	return response, nil
}

func validateStreamingRequest(
	request *http.Request,
	footprint CredentialFootprint,
) error {
	if request == nil || request.URL == nil {
		return knownError(
			connector.FailureInvalidInput,
			defaultSafeMessage(connector.FailureInvalidInput),
		)
	}
	if request.URL.User != nil || len(request.Trailer) != 0 {
		return knownError(
			connector.FailureInvalidInput,
			defaultSafeMessage(connector.FailureInvalidInput),
		)
	}
	if (request.Method == http.MethodGet ||
		request.Method == http.MethodHead) &&
		request.Body != nil && request.Body != http.NoBody {
		return knownError(
			connector.FailureInvalidInput,
			defaultSafeMessage(connector.FailureInvalidInput),
		)
	}
	for _, name := range []string{
		"Authorization",
		"Cookie",
		"Cookie2",
		"Host",
		"Proxy-Authorization",
		"Referer",
	} {
		if hasHeaderFold(request.Header, name) {
			return knownError(
				connector.FailureConfigurationError,
				defaultSafeMessage(connector.FailureConfigurationError),
			)
		}
	}
	for _, name := range footprint.HeaderNames {
		if hasHeaderFold(request.Header, name) {
			return knownError(
				connector.FailureConfigurationError,
				defaultSafeMessage(connector.FailureConfigurationError),
			)
		}
	}
	query, err := url.ParseQuery(request.URL.RawQuery)
	if err != nil {
		return knownError(
			connector.FailureInvalidInput,
			defaultSafeMessage(connector.FailureInvalidInput),
			WithCause(err),
		)
	}
	for _, name := range footprint.QueryParamNames {
		if _, exists := query[name]; exists {
			return knownError(
				connector.FailureConfigurationError,
				defaultSafeMessage(connector.FailureConfigurationError),
			)
		}
	}
	return nil
}

func buildRedirectRequest(
	response *http.Response,
	current *http.Request,
) (*http.Request, bool, error) {
	if response == nil || current == nil || current.URL == nil {
		return nil, false, knownError(
			connector.FailureInvalidResponse,
			defaultSafeMessage(connector.FailureInvalidResponse),
		)
	}
	switch response.StatusCode {
	case http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect:
	default:
		return nil, false, nil
	}

	location := response.Header.Get("Location")
	if location == "" {
		return nil, false, nil
	}
	target, err := current.URL.Parse(location)
	if err != nil {
		return nil, false, knownError(
			connector.FailureInvalidResponse,
			defaultSafeMessage(connector.FailureInvalidResponse),
			WithCause(err),
			statusOption(response.StatusCode),
		)
	}

	next := current.Clone(current.Context())
	next.URL = target
	next.Response = response
	next.RequestURI = ""
	next.Host = ""
	next.Header = current.Header.Clone()
	deleteHeaderFold(next.Header, "Referer")

	switch response.StatusCode {
	case http.StatusMovedPermanently,
		http.StatusFound:
		if current.Method != http.MethodGet &&
			current.Method != http.MethodHead {
			clearStreamingRedirectBody(next)
			next.Method = http.MethodGet
		}
	case http.StatusSeeOther:
		clearStreamingRedirectBody(next)
		if current.Method == http.MethodHead {
			next.Method = http.MethodHead
		} else {
			next.Method = http.MethodGet
		}
	case http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		if current.Body == nil || current.Body == http.NoBody {
			clearStreamingRedirectBody(next)
			break
		}
		if current.GetBody == nil {
			return nil, false, knownError(
				connector.FailureInvalidResponse,
				defaultSafeMessage(connector.FailureInvalidResponse),
				statusOption(response.StatusCode),
			)
		}
		replay, replayErr := current.GetBody()
		if replayErr != nil {
			return nil, false, knownError(
				connector.FailureInvalidResponse,
				defaultSafeMessage(connector.FailureInvalidResponse),
				WithCause(replayErr),
				statusOption(response.StatusCode),
			)
		}
		next.Body = replay
	}
	return next, true, nil
}

func clearStreamingRedirectBody(request *http.Request) {
	request.Body = nil
	request.GetBody = nil
	request.ContentLength = 0
	request.TransferEncoding = nil
	request.Trailer = nil
}

func closeRequestBody(request *http.Request) {
	if request != nil && request.Body != nil {
		_ = request.Body.Close()
	}
}

func sanitizeErrorFacingRequest(request *http.Request) {
	if request == nil {
		return
	}
	request.URL = sanitizedURL(request.URL)
}

func sanitizedResponseRequest(request *http.Request) *http.Request {
	if request == nil {
		return nil
	}
	safe := request.Clone(request.Context())
	safe.URL = sanitizedURL(request.URL)
	safe.Header = make(http.Header)
	safe.Trailer = nil
	safe.Body = nil
	safe.GetBody = nil
	safe.ContentLength = 0
	safe.TransferEncoding = nil
	safe.Host = ""
	safe.Response = nil
	return safe
}

func sanitizedURL(input *url.URL) *url.URL {
	safe := &url.URL{
		Scheme: "https",
		Host:   "redacted.invalid",
		Path:   "/",
	}
	if input == nil {
		return safe
	}
	candidate := &url.URL{
		Scheme: input.Scheme,
		Host:   input.Host,
		Path:   "/",
	}
	if _, err := CanonicalOrigin(candidate); err == nil {
		return candidate
	}
	return safe
}

func streamingStatusError(status int) error {
	code := statusFailureCode(status)
	if code == "" {
		if status >= 200 && status <= 299 {
			return nil
		}
		code = connector.FailureProviderError
	}
	return knownError(
		code,
		defaultSafeMessage(code),
		statusOption(status),
	)
}

type boundedStreamingBody struct {
	body           io.ReadCloser
	max            int64
	read           atomic.Int64
	status         int
	callerContext  context.Context
	requestContext context.Context
	cancel         context.CancelFunc
	once           sync.Once
	finished       atomic.Bool
	closing        atomic.Bool
	terminalErr    error
	done           func(int64, error)
}

func (body *boundedStreamingBody) Read(buffer []byte) (int, error) {
	if body == nil || body.body == nil {
		return 0, io.ErrClosedPipe
	}
	if len(buffer) == 0 {
		return 0, nil
	}
	if body.closing.Load() {
		return 0, io.ErrClosedPipe
	}
	if body.finished.Load() {
		if body.terminalErr != nil {
			return 0, body.terminalErr
		}
		return 0, io.EOF
	}
	if cause := context.Cause(body.requestContext); cause != nil {
		normalized := normalizeClientError(
			body.callerContext,
			body.requestContext,
			cause,
		)
		_ = body.body.Close()
		body.finish(normalized)
		return 0, normalized
	}

	remaining := body.max - body.read.Load()
	if remaining <= 0 {
		var probe [1]byte
		count, err := body.body.Read(probe[:])
		if count > 0 {
			limitErr := knownError(
				connector.FailureResponseTooLarge,
				defaultSafeMessage(connector.FailureResponseTooLarge),
				statusOption(body.status),
			)
			_ = body.body.Close()
			body.finish(limitErr)
			return 0, limitErr
		}
		if errors.Is(err, io.EOF) {
			if !body.closing.Load() {
				body.finish(nil)
			}
			return 0, io.EOF
		}
		if err != nil {
			if body.closing.Load() {
				return 0, io.ErrClosedPipe
			}
			normalized := normalizeClientError(
				body.callerContext,
				body.requestContext,
				err,
			)
			body.finish(normalized)
			return 0, normalized
		}
		return 0, nil
	}
	if int64(len(buffer)) > remaining {
		buffer = buffer[:int(remaining)]
	}
	count, err := body.body.Read(buffer)
	body.read.Add(int64(count))
	if errors.Is(err, io.EOF) {
		if !body.closing.Load() {
			body.finish(nil)
		}
		return count, io.EOF
	}
	if err != nil {
		if body.closing.Load() {
			return count, io.ErrClosedPipe
		}
		normalized := normalizeClientError(
			body.callerContext,
			body.requestContext,
			err,
		)
		body.finish(normalized)
		return count, normalized
	}
	return count, nil
}

func (body *boundedStreamingBody) Close() error {
	if body == nil || body.body == nil {
		return nil
	}
	body.closing.Store(true)
	wasFinished := body.finished.Load()
	err := body.body.Close()
	if err != nil {
		err = normalizeClientError(
			body.callerContext,
			body.requestContext,
			err,
		)
	} else if !wasFinished {
		if cause := streamingExternalContextCause(
			body.callerContext,
			body.requestContext,
		); cause != nil {
			err = normalizeClientError(
				body.callerContext,
				body.requestContext,
				cause,
			)
		}
	}
	body.finish(err)
	return err
}

// streamingExternalContextCause distinguishes caller/provider deadlines from
// the internal context.Canceled emitted by finish. Close itself may unblock a
// concurrent Read, whose finish call cancels requestContext; that lifecycle
// signal must not turn an otherwise successful Close into a canceled failure.
func streamingExternalContextCause(
	callerContext context.Context,
	requestContext context.Context,
) error {
	if callerContext != nil {
		if cause := context.Cause(callerContext); cause != nil {
			return cause
		}
	}
	if requestContext == nil {
		return nil
	}
	cause := context.Cause(requestContext)
	if errors.Is(cause, errProviderRequestTimeout) {
		return cause
	}
	if cause != nil && !errors.Is(cause, context.Canceled) {
		return cause
	}
	return nil
}

func (body *boundedStreamingBody) finish(err error) {
	body.once.Do(func() {
		body.terminalErr = err
		body.finished.Store(true)
		if body.done != nil {
			body.done(body.read.Load(), err)
		}
		if body.cancel != nil {
			body.cancel()
		}
	})
}

var _ http.RoundTripper = (*streamingRoundTripper)(nil)
var _ io.ReadCloser = (*boundedStreamingBody)(nil)
