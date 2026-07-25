package providerkit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
)

// Streaming counterpart of
// TestClientBufferedRedirectChainStripsCredentialsAfterCrossOrigin: the same
// redirectChain scenario must come out identically when the caller drives
// net/http directly, and the returned client must additionally carry no cookie
// jar, no second timeout, and no request echo that could leak the real URL.
func TestHTTPClientStreamingRedirectChainStripsCredentialsAfterCrossOrigin(
	t *testing.T,
) {
	t.Parallel()

	chain := newRedirectChain(t)
	client := chain.harness.client(t, chain.policy(), WithClock(realClock{}))
	httpClient, err := client.HTTPClient(chain.authorizer(), validTestLabels())
	if err != nil {
		t.Fatalf("HTTPClient() error = %v", err)
	}
	if httpClient.Jar != nil || httpClient.Timeout != 0 {
		t.Fatalf(
			"guarded client Jar/Timeout = %#v/%s, want nil/0",
			httpClient.Jar,
			httpClient.Timeout,
		)
	}
	if _, ok := httpClient.Transport.(interface{ CloseIdleConnections() }); !ok {
		t.Fatal("guarded streaming transport cannot close idle connections")
	}
	httpClient.CloseIdleConnections()

	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"http://"+chain.harness.primaryHost()+"/start?keep=start",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Mcp-Session-Id", redirectChainSessionID)
	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatalf("Body.Close() error = %v", err)
	}
	if response.StatusCode != http.StatusOK || string(body) != "ok" {
		t.Fatalf("response = status %d body %q", response.StatusCode, body)
	}
	if response.Request == nil || response.Request.URL == nil ||
		response.Request.URL.Path != "/" ||
		response.Request.URL.RawQuery != "" ||
		len(response.Request.Header) != 0 {
		t.Fatalf("response.Request was not sanitized: %#v", response.Request)
	}
	chain.assert(t)
}

func TestHTTPClientRedirectDenialDoesNotExposeLocationOrRequestQuery(
	t *testing.T,
) {
	t.Parallel()

	var targetCalls atomic.Int64
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path == "/target-secret" {
			targetCalls.Add(1)
			_, _ = io.WriteString(writer, "must not arrive")
			return
		}
		writer.Header().Set(
			"Location",
			"/target-secret?location_token=location-secret",
		)
		writer.WriteHeader(http.StatusFound)
	}))
	policy := harness.policy()
	policy.RedirectMode = RedirectDenyAll
	client := harness.client(t, policy, WithClock(realClock{}))
	httpClient, err := client.HTTPClient(NoAuth(), validTestLabels())
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(
		http.MethodGet,
		"http://"+harness.primaryHost()+
			"/request-secret?request_token=request-secret",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	response, err := httpClient.Do(request)
	if response != nil {
		t.Fatalf("response = %#v, want nil", response)
	}
	assertErrorCode(t, err, connector.FailurePolicyDenied)
	var urlError *url.Error
	if !errors.As(err, &urlError) {
		t.Fatalf("error type = %T, want *url.Error chain", err)
	}
	for _, secret := range []string{
		"target-secret",
		"location-secret",
		"request-secret",
		"request_token",
	} {
		if strings.Contains(err.Error(), secret) ||
			strings.Contains(urlError.URL, secret) {
			t.Fatalf("safe redirect error exposed %q: %v", secret, err)
		}
	}
	if request.URL.Path != "/" || request.URL.RawQuery != "" {
		t.Fatalf("error-facing request URL = %q", request.URL.String())
	}
	if targetCalls.Load() != 0 {
		t.Fatal("denied redirect reached target")
	}
}

func TestHTTPClientRejectsCallerSuppliedCredentialLocations(t *testing.T) {
	t.Parallel()

	var handlerCalls atomic.Int64
	harness := newProviderHarness(t, http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		handlerCalls.Add(1)
	}))
	client := harness.client(t, harness.policy(), WithClock(realClock{}))
	headerAuthorizer, err := HeaderAPIKey("X-Api-Key", "fixed-secret")
	if err != nil {
		t.Fatal(err)
	}
	queryAuthorizer, err := QueryAPIKey("api_key", "fixed-secret")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		authorizer Authorizer
		mutate     func(*http.Request)
		wantCode   connector.FailureCode
	}{
		{
			name:       "Authorization",
			authorizer: NoAuth(),
			mutate: func(request *http.Request) {
				request.Header.Set("Authorization", "Bearer caller-secret")
			},
			wantCode: connector.FailureConfigurationError,
		},
		{
			name:       "lowercase Cookie",
			authorizer: NoAuth(),
			mutate: func(request *http.Request) {
				request.Header["cookie"] = []string{"session=caller-secret"}
			},
			wantCode: connector.FailureConfigurationError,
		},
		{
			name:       "declared header footprint",
			authorizer: headerAuthorizer,
			mutate: func(request *http.Request) {
				request.Header.Set("X-Api-Key", "caller-secret")
			},
			wantCode: connector.FailureConfigurationError,
		},
		{
			name:       "declared query footprint",
			authorizer: queryAuthorizer,
			mutate: func(request *http.Request) {
				query := request.URL.Query()
				query.Set("api_key", "caller-secret")
				request.URL.RawQuery = query.Encode()
			},
			wantCode: connector.FailureConfigurationError,
		},
		{
			name:       "GET body",
			authorizer: NoAuth(),
			mutate: func(request *http.Request) {
				request.Body = io.NopCloser(
					strings.NewReader("caller-secret"),
				)
				request.ContentLength = int64(len("caller-secret"))
			},
			wantCode: connector.FailureInvalidInput,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			httpClient, err := client.HTTPClient(
				test.authorizer,
				validTestLabels(),
			)
			if err != nil {
				t.Fatal(err)
			}
			request, err := http.NewRequest(
				http.MethodGet,
				"http://"+harness.primaryHost()+
					"/secret-path?signed=caller-secret",
				nil,
			)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(request)

			response, err := httpClient.Do(request)
			if response != nil {
				t.Fatalf("response = %#v, want nil", response)
			}
			assertErrorCode(
				t,
				err,
				test.wantCode,
			)
			for _, secret := range []string{
				"caller-secret",
				"secret-path",
				"signed=",
			} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("error exposed %q: %v", secret, err)
				}
			}
		})
	}
	if handlerCalls.Load() != 0 {
		t.Fatalf("handler calls = %d, want zero", handlerCalls.Load())
	}
}

func TestHTTPClientTransportErrorIsTypedAndURLSanitized(t *testing.T) {
	t.Parallel()

	harness := newProviderHarness(t, http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		t.Error("handler must not be reached")
	}))
	client := harness.client(
		t,
		harness.policy(),
		WithResolver(resolverFunc(func(
			context.Context,
			string,
			string,
		) ([]netip.Addr, error) {
			return nil, errors.New("resolver unavailable")
		})),
	)
	authorizer, err := QueryAPIKey("api_key", "authorizer-secret")
	if err != nil {
		t.Fatal(err)
	}
	httpClient, err := client.HTTPClient(authorizer, validTestLabels())
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(
		http.MethodGet,
		"http://"+harness.primaryHost()+
			"/private/path?caller_token=caller-secret",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	response, err := httpClient.Do(request)
	if response != nil {
		t.Fatalf("response = %#v, want nil", response)
	}
	assertErrorCode(t, err, connector.FailureUpstreamUnavailable)
	var urlError *url.Error
	if !errors.As(err, &urlError) {
		t.Fatalf("error type = %T, want *url.Error chain", err)
	}
	for _, secret := range []string{
		"authorizer-secret",
		"caller-secret",
		"private/path",
		"caller_token",
	} {
		if strings.Contains(err.Error(), secret) ||
			strings.Contains(urlError.URL, secret) {
			t.Fatalf("transport error exposed %q: %v", secret, err)
		}
	}
	if request.URL.Path != "/" || request.URL.RawQuery != "" {
		t.Fatalf("error-facing request URL = %q", request.URL.String())
	}
}

func TestHTTPClientTotalTimeoutCoversStreamedBody(t *testing.T) {
	t.Parallel()

	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		writer.WriteHeader(http.StatusOK)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		<-request.Context().Done()
	}))
	policy := harness.policy()
	policy.RequestTimeout = 100 * time.Millisecond
	events := &eventCollector{}
	client := harness.client(t, policy, WithClock(realClock{}), WithObserver(events))
	httpClient, err := client.HTTPClient(NoAuth(), validTestLabels())
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(
		http.MethodGet,
		"http://"+harness.primaryHost()+"/stream",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatalf("Do() returned before headers with error = %v", err)
	}
	_, err = io.ReadAll(response.Body)
	assertErrorCode(t, err, connector.FailureTimeout)
	_ = response.Body.Close()

	gotEvents := events.Events()
	if len(gotEvents) != 1 ||
		gotEvents[0].ErrorCode != connector.FailureTimeout {
		t.Fatalf("events = %#v, want one timeout", gotEvents)
	}
}

func TestHTTPClientStreamedResponseEnforcesActualMaxPlusOne(t *testing.T) {
	t.Parallel()

	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.WriteHeader(http.StatusOK)
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		_, _ = io.WriteString(writer, "123456789")
	}))
	policy := harness.policy()
	policy.MaxResponseBytes = 8
	client := harness.client(t, policy, WithClock(realClock{}))
	httpClient, err := client.HTTPClient(NoAuth(), validTestLabels())
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(
		http.MethodGet,
		"http://"+harness.primaryHost()+"/stream",
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	response, err := httpClient.Do(request)
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	body, err := io.ReadAll(response.Body)
	assertErrorCode(t, err, connector.FailureResponseTooLarge)
	if len(body) > int(policy.MaxResponseBytes) {
		t.Fatalf("body length = %d, want at most %d", len(body), policy.MaxResponseBytes)
	}
	_ = response.Body.Close()
}

func TestHTTPClientSameOrigin307And308ReplayBodyWithCredentials(t *testing.T) {
	t.Parallel()

	var failure atomic.Value
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			failure.Store("body read failed")
		}
		if string(body) != "payload" {
			failure.Store("body was not replayed")
		}
		if request.Header.Get("Authorization") != "Bearer bearer-secret" {
			failure.Store("Authorization was not retained")
		}
		if request.URL.Path == "/start" {
			status := requestedStatus(request)
			if status == 0 {
				http.Error(
					writer,
					"missing redirect status",
					http.StatusBadRequest,
				)
				return
			}
			http.Redirect(writer, request, "/target", status)
			return
		}
		_, _ = io.WriteString(writer, "ok")
	}))
	client := harness.client(t, harness.policy(), WithClock(realClock{}))
	authorizer, err := Bearer("bearer-secret")
	if err != nil {
		t.Fatal(err)
	}
	httpClient, err := client.HTTPClient(authorizer, validTestLabels())
	if err != nil {
		t.Fatal(err)
	}

	for _, status := range []int{
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			request, err := http.NewRequest(
				http.MethodPost,
				"http://"+harness.primaryHost()+
					"/start?status="+strconv.Itoa(status),
				bytes.NewBufferString("payload"),
			)
			if err != nil {
				t.Fatal(err)
			}
			response, err := httpClient.Do(request)
			if err != nil {
				t.Fatalf("Do() error = %v", err)
			}
			body, err := io.ReadAll(response.Body)
			if err != nil {
				t.Fatal(err)
			}
			_ = response.Body.Close()
			if string(body) != "ok" {
				t.Fatalf("response body = %q", body)
			}
			if got := failure.Load(); got != nil {
				t.Fatal(got.(string))
			}
		})
	}
}

func TestBoundedStreamingBodyConcurrentCloseUnblocksRead(t *testing.T) {
	t.Parallel()

	underlying := newBlockingReadCloser()
	requestContext, cancel := context.WithCancel(context.Background())
	body := &boundedStreamingBody{
		body:           underlying,
		max:            8,
		status:         http.StatusOK,
		callerContext:  context.Background(),
		requestContext: requestContext,
		cancel:         cancel,
	}
	doneResult := make(chan error, 1)
	body.done = func(_ int64, err error) {
		doneResult <- err
	}
	readResult := make(chan error, 1)
	go func() {
		buffer := make([]byte, 1)
		_, err := body.Read(buffer)
		readResult <- err
	}()

	select {
	case <-underlying.started:
	case <-time.After(time.Second):
		t.Fatal("Read did not start")
	}
	if err := body.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	select {
	case err := <-readResult:
		if err == nil {
			t.Fatal("Read error = nil after concurrent Close")
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Close did not unblock Read")
	}
	select {
	case err := <-doneResult:
		if err != nil {
			t.Fatalf("observer completion error = %v, want nil", err)
		}
	case <-time.After(time.Second):
		t.Fatal("concurrent Close did not finish observation")
	}
}

func TestBoundedStreamingBodyClosePreservesExternalContextCause(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		newContext func() (context.Context, context.Context, func())
		wantCode   connector.FailureCode
	}{
		{
			name: "caller cancellation",
			newContext: func() (context.Context, context.Context, func()) {
				callerContext, cancel := context.WithCancel(context.Background())
				requestContext, requestCancel := context.WithCancel(callerContext)
				return callerContext, requestContext, func() {
					cancel()
					requestCancel()
				}
			},
			wantCode: connector.FailureCanceled,
		},
		{
			name: "provider timeout",
			newContext: func() (context.Context, context.Context, func()) {
				requestContext, cancel := context.WithCancelCause(
					context.Background(),
				)
				return context.Background(), requestContext, func() {
					cancel(errProviderRequestTimeout)
				}
			},
			wantCode: connector.FailureTimeout,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			callerContext, requestContext, trigger := test.newContext()
			trigger()
			body := &boundedStreamingBody{
				body:           io.NopCloser(strings.NewReader("")),
				max:            8,
				status:         http.StatusOK,
				callerContext:  callerContext,
				requestContext: requestContext,
			}
			err := body.Close()
			assertErrorCode(t, err, test.wantCode)
		})
	}
}

func TestBoundedStreamingBodyRepeatedReadPreservesTerminalResult(t *testing.T) {
	t.Parallel()

	t.Run("successful EOF remains EOF", func(t *testing.T) {
		t.Parallel()

		requestContext, cancel := context.WithCancel(context.Background())
		body := &boundedStreamingBody{
			body:           io.NopCloser(strings.NewReader("")),
			max:            8,
			status:         http.StatusOK,
			callerContext:  context.Background(),
			requestContext: requestContext,
			cancel:         cancel,
		}
		buffer := make([]byte, 1)
		for attempt := 0; attempt < 2; attempt++ {
			count, err := body.Read(buffer)
			if count != 0 || !errors.Is(err, io.EOF) {
				t.Fatalf(
					"Read #%d = (%d, %v), want (0, EOF)",
					attempt+1,
					count,
					err,
				)
			}
		}
	})

	t.Run("terminal failure remains stable", func(t *testing.T) {
		t.Parallel()

		wantErr := knownError(
			connector.FailureUpstreamUnavailable,
			defaultSafeMessage(connector.FailureUpstreamUnavailable),
		)
		body := &boundedStreamingBody{
			body:           streamingTerminalErrorReadCloser{err: wantErr},
			max:            8,
			status:         http.StatusOK,
			callerContext:  context.Background(),
			requestContext: context.Background(),
		}
		buffer := make([]byte, 1)
		_, firstErr := body.Read(buffer)
		_, secondErr := body.Read(buffer)
		assertErrorCode(t, firstErr, connector.FailureUpstreamUnavailable)
		if firstErr != secondErr {
			t.Fatalf(
				"terminal errors differ: first=%v second=%v",
				firstErr,
				secondErr,
			)
		}
	})
}

type streamingTerminalErrorReadCloser struct {
	err error
}

func (body streamingTerminalErrorReadCloser) Read([]byte) (int, error) {
	return 0, body.err
}

func (streamingTerminalErrorReadCloser) Close() error { return nil }

type blockingReadCloser struct {
	started   chan struct{}
	closed    chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
}

func newBlockingReadCloser() *blockingReadCloser {
	return &blockingReadCloser{
		started: make(chan struct{}),
		closed:  make(chan struct{}),
	}
}

func (body *blockingReadCloser) Read([]byte) (int, error) {
	body.startOnce.Do(func() {
		close(body.started)
	})
	<-body.closed
	return 0, errors.New("stream closed")
}

func (body *blockingReadCloser) Close() error {
	body.closeOnce.Do(func() {
		close(body.closed)
	})
	return nil
}
