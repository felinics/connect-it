package providerkit

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
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

func TestRedirectPolicySameOriginUsesSchemeHostAndEffectivePort(t *testing.T) {
	t.Parallel()

	redirects := &redirectPolicy{
		policy: Policy{},
		footprint: CredentialFootprint{
			HeaderNames:     []string{"X-Api-Key"},
			QueryParamNames: []string{"api_key"},
		},
	}
	previous := redirectRequest(t, http.MethodGet, "https://api.example.test/start", false)
	next := redirectRequest(
		t,
		http.MethodGet,
		"https://api.example.test:443/next?api_key=still-present",
		false,
	)
	next.Header.Set("Authorization", "Bearer credential")
	next.Header.Set("X-Api-Key", "credential")
	next.Header.Set("Referer", "https://api.example.test/start")

	if err := redirects.Check(next, []*http.Request{previous}); err != nil {
		t.Fatalf("Check() same-origin error = %v", err)
	}
	if got := next.Header.Get("Authorization"); got == "" {
		t.Fatal("same-origin redirect unexpectedly removed Authorization")
	}
	if got := next.Header.Get("X-Api-Key"); got == "" {
		t.Fatal("same-origin redirect unexpectedly removed declared credential header")
	}
	if got := next.URL.Query().Get("api_key"); got == "" {
		t.Fatal("same-origin redirect unexpectedly removed declared credential query")
	}
	if got := next.Header.Get("Referer"); got != "" {
		t.Fatalf("Referer = %q after same-origin redirect, want empty", got)
	}
	if approval, ok := next.Context().Value(
		redirectApprovalContextKey{},
	).(redirectApproval); !ok || approval.origin != "https://api.example.test:443" {
		t.Fatalf("redirect approval = %#v, %t", approval, ok)
	}
}

func TestRedirectPolicyRejectsUnsafeCrossOriginTransitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		previous *http.Request
		next     *http.Request
		policy   Policy
	}{
		{
			name:     "origin absent from allowlist",
			previous: redirectRequest(t, http.MethodGet, "https://api.example.test/start", false),
			next:     redirectRequest(t, http.MethodGet, "https://other.example.test/next", false),
		},
		{
			name:     "non-default port is a different origin",
			previous: redirectRequest(t, http.MethodGet, "https://api.example.test/start", false),
			next:     redirectRequest(t, http.MethodGet, "https://api.example.test:8443/next", false),
		},
		{
			name:     "HTTPS downgrade",
			previous: redirectRequest(t, http.MethodGet, "https://api.example.test/start", false),
			next:     redirectRequest(t, http.MethodGet, "http://redirect.example.test/next", false),
			policy: Policy{
				AllowedRedirectOrigins: []string{"http://redirect.example.test:80"},
			},
		},
		{
			name:     "cross-origin request with body",
			previous: redirectRequest(t, http.MethodPost, "https://api.example.test/start", true),
			next:     redirectRequest(t, http.MethodGet, "https://redirect.example.test/next", false),
			policy: Policy{
				AllowedRedirectOrigins: []string{"https://redirect.example.test:443"},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			redirects := &redirectPolicy{policy: test.policy}
			err := redirects.Check(test.next, []*http.Request{test.previous})
			assertPolicyViolation(t, err, PolicyOutcomeBlockedRedirect)
		})
	}
}

func TestRedirectPolicyCrossOriginTaintIsPermanentAndStripsCredentialFootprint(t *testing.T) {
	t.Parallel()

	redirects := &redirectPolicy{
		policy: Policy{
			AllowedRedirectOrigins: []string{"https://redirect.example.test:443"},
		},
		footprint: CredentialFootprint{
			HeaderNames:     []string{"X-Api-Key", "X-Signature"},
			QueryParamNames: []string{"api_key", "signature"},
		},
	}
	origin := redirectRequest(t, http.MethodGet, "https://api.example.test/start", false)
	crossOrigin := redirectRequest(
		t,
		http.MethodGet,
		"https://redirect.example.test/step?api_key=one&signature=two&keep=yes",
		false,
	)
	setRedirectCredentials(crossOrigin)
	crossOrigin.Header["x-api-key"] = []string{"lowercase-credential"}

	if err := redirects.Check(crossOrigin, []*http.Request{origin}); err != nil {
		t.Fatalf("first cross-origin Check() error = %v", err)
	}
	assertRedirectCredentialsRemoved(t, crossOrigin)
	if _, exists := crossOrigin.Header["x-api-key"]; exists {
		t.Fatal("lowercase credential header survived redirect stripping")
	}
	if got := crossOrigin.URL.Query().Get("keep"); got != "yes" {
		t.Fatalf("unrelated query parameter = %q, want yes", got)
	}
	if got := crossOrigin.Header.Get("X-Unrelated"); got != "" {
		t.Fatalf("unlisted cross-origin header = %q, want removed", got)
	}

	// Even though the next hop is same-origin with the already crossed-to
	// origin, the redirect chain remains tainted.
	sameAfterCross := redirectRequest(
		t,
		http.MethodGet,
		"https://redirect.example.test/final?api_key=again&signature=again",
		false,
	)
	setRedirectCredentials(sameAfterCross)
	if err := redirects.Check(
		sameAfterCross,
		[]*http.Request{origin, crossOrigin},
	); err != nil {
		t.Fatalf("same-origin-after-cross Check() error = %v", err)
	}
	assertRedirectCredentialsRemoved(t, sameAfterCross)
}

func TestRedirectPolicyFailsClosedOnMalformedTaintedQuery(t *testing.T) {
	t.Parallel()

	redirects := &redirectPolicy{
		policy: Policy{
			AllowedRedirectOrigins: []string{"https://redirect.example.test:443"},
		},
		footprint: CredentialFootprint{
			QueryParamNames: []string{"api_key"},
		},
	}
	previous := redirectRequest(t, http.MethodGet, "https://api.example.test/start", false)
	next := redirectRequest(t, http.MethodGet, "https://redirect.example.test/next", false)
	next.URL.RawQuery = "%zz"

	err := redirects.Check(next, []*http.Request{previous})
	assertPolicyViolation(t, err, PolicyOutcomeBlockedRedirect)
}

func TestRedirectPolicyDeletesRefererEveryHopAndLimitsChain(t *testing.T) {
	t.Parallel()

	redirects := &redirectPolicy{}
	via := make([]*http.Request, maxRedirects)
	for i := range via {
		via[i] = redirectRequest(
			t,
			http.MethodGet,
			"https://api.example.test/"+strconv.Itoa(i),
			false,
		)
	}
	tenth := redirectRequest(t, http.MethodGet, "https://api.example.test/tenth", false)
	tenth.Header.Set("Referer", "https://api.example.test/previous")
	if err := redirects.Check(tenth, via); err != nil {
		t.Fatalf("Check() rejected the tenth redirect: %v", err)
	}
	if got := tenth.Header.Get("Referer"); got != "" {
		t.Fatalf("Referer = %q on tenth redirect, want empty", got)
	}

	via = append(via, tenth)
	eleventh := redirectRequest(t, http.MethodGet, "https://api.example.test/eleventh", false)
	eleventh.Header.Set("Referer", "https://api.example.test/tenth")
	err := redirects.Check(eleventh, via)
	assertPolicyViolation(t, err, PolicyOutcomeBlockedRedirect)
	if got := eleventh.Header.Get("Referer"); got != "" {
		t.Fatalf("Referer = %q on rejected redirect, want empty", got)
	}
}

func TestClientRedirectDenyAllRejectsEveryHTTPRedirectStatus(t *testing.T) {
	t.Parallel()

	var targetRequests atomic.Int64
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path == "/target" {
			targetRequests.Add(1)
			_, _ = io.WriteString(writer, "must not arrive")
			return
		}
		status := requestedStatus(request)
		if status == 0 {
			http.Error(writer, "missing redirect status", http.StatusBadRequest)
			return
		}
		http.Redirect(writer, request, "/target", status)
	}))
	policy := harness.policy()
	policy.RedirectMode = RedirectDenyAll
	client := harness.client(t, policy)

	for _, status := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			_, err := client.Do(context.Background(), Request{
				Method: http.MethodPost,
				URL:    "/oauth/token",
				Query:  url.Values{"status": {strconv.Itoa(status)}},
				Form:   url.Values{"grant_type": {"authorization_code"}},
				Labels: RequestLabels{
					ConnectorType:   "test",
					Operation:       "oauth_exchange",
					AuthorizationID: "redirect-test-authorization",
				},
			})
			assertErrorCode(t, err, connector.FailurePolicyDenied)
		})
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("denied redirects reached the target %d times", targetRequests.Load())
	}
}

// The buffered Client and the streaming HTTPClient must handle the same
// cross-origin redirect chain identically; the shared scenario lives in
// redirectChain so the two layers cannot drift apart silently.
func TestClientBufferedRedirectChainStripsCredentialsAfterCrossOrigin(
	t *testing.T,
) {
	t.Parallel()

	chain := newRedirectChain(t)
	client := chain.harness.client(t, chain.policy(), WithClock(realClock{}))

	response, err := client.Do(context.Background(), Request{
		Method:     http.MethodGet,
		URL:        "/start?keep=start",
		Authorizer: chain.authorizer(),
		Headers:    http.Header{"Mcp-Session-Id": {redirectChainSessionID}},
		Labels:     validTestLabels(),
	})
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}
	if response.StatusCode != http.StatusOK || string(response.Body) != "ok" {
		t.Fatalf(
			"response = status %d body %q",
			response.StatusCode,
			response.Body,
		)
	}
	chain.assert(t)
}

func TestClientObserverRecordsEveryAllowedRedirectHopAndRetry(
	t *testing.T,
) {
	t.Parallel()

	var finalCalls atomic.Int64
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/start":
			http.Redirect(
				writer,
				request,
				"/same",
				http.StatusFound,
			)
		case "/same":
			http.Redirect(
				writer,
				request,
				"http://"+hostOnPort(request.Host, "redirect.test")+"/final",
				http.StatusFound,
			)
		case "/final":
			if finalCalls.Add(1) == 1 {
				writer.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(writer, "complete")
		default:
			http.Error(writer, "unexpected path", http.StatusNotFound)
		}
	}))

	policy := harness.policy()
	policy.AllowedRedirectOrigins = []string{
		"http://" + harness.redirectHost(),
	}
	policy.Retry = RetryPolicy{
		MaxRetries:     1,
		InitialBackoff: time.Nanosecond,
		MaxBackoff:     time.Nanosecond,
	}
	events := &eventCollector{}
	client := harness.client(t, policy, WithObserver(events))

	response, err := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    "/start",
		Labels: validTestLabels(),
	})
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}
	if response.StatusCode != http.StatusOK ||
		string(response.Body) != "complete" {
		t.Fatalf(
			"response = status %d body %q",
			response.StatusCode,
			response.Body,
		)
	}

	got := events.Events()
	want := []struct {
		host          string
		status        int
		attempt       int
		errorCode     connector.FailureCode
		responseBytes int64
	}{
		{"provider.test", http.StatusFound, 1, "", 0},
		{"provider.test", http.StatusFound, 1, "", 0},
		{
			"redirect.test",
			http.StatusServiceUnavailable,
			1,
			connector.FailureUpstreamUnavailable,
			0,
		},
		{"provider.test", http.StatusFound, 2, "", 0},
		{"provider.test", http.StatusFound, 2, "", 0},
		{"redirect.test", http.StatusOK, 2, "", int64(len("complete"))},
	}
	if len(got) != len(want) {
		t.Fatalf("observer events = %#v, want %d events", got, len(want))
	}
	for index, expected := range want {
		event := got[index]
		if event.ProviderHost != expected.host ||
			event.UpstreamStatus != expected.status ||
			event.Attempt != expected.attempt ||
			event.ErrorCode != expected.errorCode ||
			event.ResponseBytes != expected.responseBytes ||
			event.PolicyOutcome != PolicyOutcomeAllowed {
			t.Errorf("event %d = %#v, want %#v", index, event, expected)
		}
	}
}

func TestClientObserverDoesNotInventRejectedRedirectHop(t *testing.T) {
	t.Parallel()

	var targetCalls atomic.Int64
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.URL.Path == "/target" {
			targetCalls.Add(1)
			_, _ = io.WriteString(writer, "must not arrive")
			return
		}
		http.Redirect(
			writer,
			request,
			"http://"+hostOnPort(request.Host, "redirect.test")+"/target",
			http.StatusFound,
		)
	}))
	policy := harness.policy()
	policy.RedirectMode = RedirectDenyAll
	events := &eventCollector{}
	client := harness.client(t, policy, WithObserver(events))

	response, err := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    "/start",
		Labels: validTestLabels(),
	})
	if response != nil {
		t.Fatalf("response = %#v, want nil", response)
	}
	assertErrorCode(t, err, connector.FailurePolicyDenied)
	if targetCalls.Load() != 0 {
		t.Fatal("rejected redirect target received a request")
	}

	got := events.Events()
	if len(got) != 1 {
		t.Fatalf("observer events = %#v, want only the sent initial hop", got)
	}
	event := got[0]
	if event.ProviderHost != "provider.test" ||
		event.UpstreamStatus != http.StatusFound ||
		event.Attempt != 1 ||
		event.ErrorCode != connector.FailurePolicyDenied ||
		event.ResponseBytes != 0 ||
		event.PolicyOutcome != PolicyOutcomeBlockedRedirect {
		t.Fatalf("initial hop event = %#v", event)
	}
}

func TestClientRevalidatesDNSOnEveryRedirectHop(t *testing.T) {
	t.Parallel()

	var providerRequests atomic.Int64
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		providerRequests.Add(1)
		location := "http://" + hostOnPort(request.Host, "redirect.test") +
			"/blocked"
		http.Redirect(writer, request, location, http.StatusFound)
	}))

	var resolverMu sync.Mutex
	var resolvedHosts []string
	resolver := resolverFunc(func(
		_ context.Context,
		network string,
		host string,
	) ([]netip.Addr, error) {
		if network != "ip" {
			return nil, errors.New("unexpected network")
		}
		resolverMu.Lock()
		resolvedHosts = append(resolvedHosts, host)
		resolverMu.Unlock()
		if host == "redirect.test" {
			return testAddresses("169.254.169.254"), nil
		}
		return testAddresses("8.8.8.8"), nil
	})

	policy := harness.policy()
	policy.AllowedRedirectOrigins = []string{
		"http://" + harness.redirectHost(),
	}
	client := harness.client(t, policy, WithResolver(resolver))

	_, err := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    "/start",
		Labels: validTestLabels(),
	})
	assertErrorCode(t, err, connector.FailurePolicyDenied)
	if providerRequests.Load() != 1 {
		t.Fatalf("Provider requests = %d, want only initial hop", providerRequests.Load())
	}
	resolverMu.Lock()
	gotHosts := append([]string(nil), resolvedHosts...)
	resolverMu.Unlock()
	if len(gotHosts) != 2 ||
		gotHosts[0] != "provider.test" ||
		gotHosts[1] != "redirect.test" {
		t.Fatalf("resolved hosts = %v", gotHosts)
	}
}

func TestClientRejectsCrossOriginBodyReplayFor307And308(t *testing.T) {
	t.Parallel()

	var initial atomic.Int64
	var redirected atomic.Int64
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if strings.HasPrefix(request.Host, "redirect.test:") {
			redirected.Add(1)
			_, _ = io.WriteString(writer, "must not arrive")
			return
		}
		initial.Add(1)
		status := requestedStatus(request)
		if status == 0 {
			http.Error(writer, "missing redirect status", http.StatusBadRequest)
			return
		}
		http.Redirect(
			writer,
			request,
			"http://"+hostOnPort(request.Host, "redirect.test")+"/target",
			status,
		)
	}))
	policy := harness.policy()
	policy.AllowedRedirectOrigins = []string{
		"http://" + harness.redirectHost(),
	}
	client := harness.client(t, policy)
	authorizer, err := Bearer("credential")
	if err != nil {
		t.Fatal(err)
	}

	for _, status := range []int{
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			_, err := client.Do(context.Background(), Request{
				Method:     http.MethodPost,
				URL:        "/write",
				Query:      url.Values{"status": {strconv.Itoa(status)}},
				JSON:       map[string]string{"secret": "body"},
				Authorizer: authorizer,
				Labels:     validTestLabels(),
			})
			assertErrorCode(t, err, connector.FailurePolicyDenied)
		})
	}
	if initial.Load() != 2 || redirected.Load() != 0 {
		t.Fatalf(
			"initial=%d redirected=%d, want 2/0",
			initial.Load(),
			redirected.Load(),
		)
	}
}

func TestClientRetries429WithoutRealSleepAndReappliesAuthorizer(t *testing.T) {
	t.Parallel()

	var requestCount atomic.Int64
	var missingAuthorization atomic.Bool
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Header.Get("Authorization") != "Bearer credential" {
			missingAuthorization.Store(true)
		}
		attempt := requestCount.Add(1)
		if attempt == 1 {
			writer.Header().Set("Retry-After", "3")
			http.Error(writer, "rate limited secret body", http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(writer, "ok")
	}))

	clock := newFakeClock()
	observer := &eventCollector{}
	var applyCalls atomic.Int64
	auth := authorizerFunc{
		apply: func(request *http.Request) error {
			applyCalls.Add(1)
			request.Header.Set("Authorization", "Bearer credential")
			return nil
		},
		footprint: CredentialFootprint{HeaderNames: []string{"Authorization"}},
	}
	policy := harness.policy()
	policy.Retry = RetryPolicy{
		MaxRetries:     1,
		InitialBackoff: time.Second,
		MaxBackoff:     5 * time.Second,
	}
	client := harness.client(t, policy, WithClock(clock), WithObserver(observer))

	response, err := client.Do(context.Background(), Request{
		Method:     http.MethodGet,
		URL:        "/items",
		Authorizer: auth,
		Labels:     validTestLabels(),
	})
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}
	if response.StatusCode != http.StatusOK || string(response.Body) != "ok" {
		t.Fatalf("response = status %d body %q", response.StatusCode, response.Body)
	}
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("request attempts = %d, want 2", got)
	}
	if got := applyCalls.Load(); got != 2 {
		t.Fatalf("Authorizer.Apply calls = %d, want 2", got)
	}
	if missingAuthorization.Load() {
		t.Fatal("a retry was sent without reapplying Authorization")
	}
	if got := clock.SleepDurations(); len(got) != 1 || got[0] != 3*time.Second {
		t.Fatalf("fake clock sleeps = %v, want [3s]", got)
	}

	events := observer.Events()
	if len(events) != 2 {
		t.Fatalf("observer events = %d, want 2", len(events))
	}
	if events[0].Attempt != 1 ||
		events[0].UpstreamStatus != http.StatusTooManyRequests ||
		events[0].ErrorCode != connector.FailureRateLimited ||
		events[0].PolicyOutcome != PolicyOutcomeAllowed {
		t.Fatalf("first event = %#v", events[0])
	}
	if events[1].Attempt != 2 ||
		events[1].UpstreamStatus != http.StatusOK ||
		events[1].ErrorCode != "" ||
		events[1].ResponseBytes != 2 {
		t.Fatalf("second event = %#v", events[1])
	}
}

func TestClientReturnsBoundedResponseAndTypedStatusError(t *testing.T) {
	t.Parallel()

	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		http.Error(writer, "raw secret body", http.StatusUnauthorized)
	}))
	client := harness.client(t, harness.policy())
	response, err := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    "/unauthorized",
		Labels: validTestLabels(),
	})
	if response == nil || response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("bounded response = %#v", response)
	}
	assertErrorCode(t, err, connector.FailureAuthorizationFailed)
	if strings.Contains(err.Error(), "raw secret body") {
		t.Fatal("typed status error exposed response body")
	}
}

func TestClientRetriesRetryableNetworkFailureWithoutRealSleep(t *testing.T) {
	t.Parallel()

	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = io.WriteString(writer, "recovered")
	}))
	policy := harness.policy()
	policy.Retry = RetryPolicy{
		MaxRetries:     1,
		InitialBackoff: 250 * time.Millisecond,
		MaxBackoff:     time.Second,
	}
	clock := newFakeClock()
	var dialAttempts atomic.Int64
	client := harness.client(
		t,
		policy,
		WithClock(clock),
		WithDialContext(func(
			ctx context.Context,
			network string,
			_ string,
		) (net.Conn, error) {
			if dialAttempts.Add(1) == 1 {
				return nil, &net.OpError{
					Op:  "dial",
					Net: network,
					Err: errors.New("synthetic transient dial failure"),
				}
			}
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, harness.listener.Addr().String())
		}),
	)

	response, err := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    "/network-retry",
		Labels: validTestLabels(),
	})
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}
	if string(response.Body) != "recovered" {
		t.Fatalf("response body = %q, want recovered", response.Body)
	}
	if got := dialAttempts.Load(); got != 2 {
		t.Fatalf("dial attempts = %d, want 2", got)
	}
	if got := clock.SleepDurations(); len(got) != 1 ||
		got[0] != 250*time.Millisecond {
		t.Fatalf("fake clock sleeps = %v, want [250ms]", got)
	}
}

func TestClientDoesNotRetryWriteWithBodyMutatingAuthorizer(t *testing.T) {
	t.Parallel()

	var (
		applyCalls   atomic.Int64
		handlerCalls atomic.Int64
	)
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		handlerCalls.Add(1)
		http.Error(
			writer,
			"retryable provider response",
			http.StatusServiceUnavailable,
		)
	}))
	policy := harness.policy()
	policy.Retry = RetryPolicy{
		MaxRetries:     1,
		InitialBackoff: time.Nanosecond,
		MaxBackoff:     time.Nanosecond,
	}
	client := harness.client(t, policy)
	authorizer := authorizerFunc{
		apply: func(request *http.Request) error {
			call := applyCalls.Add(1)
			body := []byte(fmt.Sprintf("signed-body-%d", call))
			request.Body = io.NopCloser(bytes.NewReader(body))
			request.ContentLength = int64(len(body))
			return nil
		},
		footprint: CredentialFootprint{Body: true},
	}

	response, err := client.Do(context.Background(), Request{
		Method:               http.MethodPost,
		URL:                  "/write",
		JSON:                 map[string]string{"value": "original"},
		IdempotencyKeyHeader: "Idempotency-Key",
		IdempotencyKey:       "stable-key",
		Authorizer:           authorizer,
		Labels:               validTestLabels(),
	})
	if response == nil ||
		response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("response = %#v", response)
	}
	assertErrorCode(t, err, connector.FailureUpstreamUnavailable)
	if applyCalls.Load() != 1 || handlerCalls.Load() != 1 {
		t.Fatalf(
			"Apply/handler calls = %d/%d, want 1/1",
			applyCalls.Load(),
			handlerCalls.Load(),
		)
	}
}

func TestClientWriteRetryFreezesJSONBodyOnce(t *testing.T) {
	t.Parallel()

	var (
		handlerCalls atomic.Int64
		bodiesMu     sync.Mutex
		bodies       [][]byte
	)
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(writer, "body read failed", http.StatusBadRequest)
			return
		}
		bodiesMu.Lock()
		bodies = append(bodies, append([]byte(nil), body...))
		bodiesMu.Unlock()
		if handlerCalls.Add(1) == 1 {
			http.Error(
				writer,
				"retryable provider response",
				http.StatusServiceUnavailable,
			)
			return
		}
		_, _ = io.WriteString(writer, "ok")
	}))
	policy := harness.policy()
	policy.Retry = RetryPolicy{
		MaxRetries:     1,
		InitialBackoff: time.Nanosecond,
		MaxBackoff:     time.Nanosecond,
	}
	client := harness.client(t, policy)
	payload := &statefulJSONMarshaler{}

	response, err := client.Do(context.Background(), Request{
		Method:               http.MethodPost,
		URL:                  "/write",
		JSON:                 payload,
		IdempotencyKeyHeader: "Idempotency-Key",
		IdempotencyKey:       "stable-key",
		Labels:               validTestLabels(),
	})
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}
	if response == nil || string(response.Body) != "ok" {
		t.Fatalf("response = %#v", response)
	}
	if payload.calls.Load() != 1 || handlerCalls.Load() != 2 {
		t.Fatalf(
			"MarshalJSON/handler calls = %d/%d, want 1/2",
			payload.calls.Load(),
			handlerCalls.Load(),
		)
	}
	bodiesMu.Lock()
	gotBodies := append([][]byte(nil), bodies...)
	bodiesMu.Unlock()
	if len(gotBodies) != 2 ||
		!bytes.Equal(gotBodies[0], gotBodies[1]) ||
		string(gotBodies[0]) != `{"attempt":1}` {
		t.Fatalf("retried bodies = %q", gotBodies)
	}
}

func TestClientRejectsAuthorizerOwningIdempotencyHeader(t *testing.T) {
	t.Parallel()

	var handlerCalls atomic.Int64
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		handlerCalls.Add(1)
		http.Error(
			writer,
			"retryable provider response",
			http.StatusServiceUnavailable,
		)
	}))
	client := harness.client(t, harness.policy())
	var applyCalls atomic.Int64
	authorizer := authorizerFunc{
		apply: func(request *http.Request) error {
			value := fmt.Sprintf("unstable-%d", applyCalls.Add(1))
			request.Header.Set("Idempotency-Key", value)
			return nil
		},
		footprint: CredentialFootprint{
			HeaderNames: []string{"Idempotency-Key"},
		},
	}

	response, err := client.Do(context.Background(), Request{
		Method:               http.MethodPost,
		URL:                  "/write",
		JSON:                 map[string]string{"value": "stable"},
		IdempotencyKeyHeader: "Idempotency-Key",
		IdempotencyKey:       "stable-key",
		Authorizer:           authorizer,
		Labels:               validTestLabels(),
	})
	if response != nil {
		t.Fatalf("response = %#v, want nil", response)
	}
	assertErrorCode(t, err, connector.FailureConfigurationError)
	if applyCalls.Load() != 0 || handlerCalls.Load() != 0 {
		t.Fatalf(
			"Apply/handler calls = %d/%d, want 0/0",
			applyCalls.Load(),
			handlerCalls.Load(),
		)
	}
}

func TestClientObserverPanicCannotChangeProviderResult(t *testing.T) {
	t.Parallel()

	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = io.WriteString(writer, "ok")
	}))
	client := harness.client(
		t,
		harness.policy(),
		WithObserver(ObserverFunc(func(context.Context, RequestEvent) {
			panic("observer failure")
		})),
	)

	response, err := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    "/",
		Labels: validTestLabels(),
	})
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}
	if string(response.Body) != "ok" {
		t.Fatalf("response body = %q, want ok", response.Body)
	}
}

func TestClientDoesNotPersistProviderCookies(t *testing.T) {
	t.Parallel()

	var calls atomic.Int64
	var persisted atomic.Bool
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if calls.Add(1) == 1 {
			http.SetCookie(writer, &http.Cookie{
				Name:  "provider_session",
				Value: "credential",
			})
		} else if request.Header.Get("Cookie") != "" {
			persisted.Store(true)
		}
		_, _ = io.WriteString(writer, "ok")
	}))
	client := harness.client(t, harness.policy())
	for range 2 {
		response, err := client.Do(context.Background(), Request{
			Method: http.MethodGet,
			URL:    "/cookie",
			Labels: validTestLabels(),
		})
		if err != nil {
			t.Fatal(err)
		}
		if string(response.Body) != "ok" {
			t.Fatalf("response body = %q", response.Body)
		}
	}
	if persisted.Load() {
		t.Fatal("Provider Set-Cookie persisted into a later request")
	}
}

// Audit labels are validated before anything is sent, so every rejected label
// shape must fail the same way and never reach the Provider.
func TestClientRejectsInvalidAuditLabels(t *testing.T) {
	t.Parallel()

	harness := newProviderHarness(t, http.HandlerFunc(func(
		http.ResponseWriter,
		*http.Request,
	) {
		t.Error("invalid audit label reached Provider")
	}))
	client := harness.client(t, harness.policy())
	for name, labels := range map[string]RequestLabels{
		"connector type does not match the policy Provider": {
			ConnectorType: "different_provider",
			ToolID:        "read",
		},
		"neither connection nor authorization": {
			ConnectorType: "test",
			ToolID:        "read",
		},
		"both connection and authorization": {
			ConnectorType:   "test",
			ToolID:          "read",
			ConnectionID:    "connection-id",
			AuthorizationID: "authorization-id",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := client.Do(context.Background(), Request{
				Method: http.MethodGet,
				URL:    "/",
				Labels: labels,
			})
			assertErrorCode(t, err, connector.FailureConfigurationError)
		})
	}
}

func TestClientObserverReceivesBlockedIPOutcome(t *testing.T) {
	t.Parallel()

	observer := &eventCollector{}
	resolver := &resolverStub{addresses: testAddresses("169.254.169.254")}
	policy := Policy{
		Provider:         "test",
		BaseURL:          "https://provider.test/",
		AllowedOrigins:   []string{"https://provider.test"},
		RequestTimeout:   time.Second,
		MaxResponseBytes: 1024,
		Retry: RetryPolicy{
			MaxRetries:     0,
			InitialBackoff: time.Nanosecond,
			MaxBackoff:     time.Nanosecond,
		},
	}
	client, err := NewFactory(
		WithResolver(resolver),
		WithDialContext(func(context.Context, string, string) (net.Conn, error) {
			return nil, errors.New("must not dial metadata")
		}),
		WithObserver(observer),
		WithClock(newFakeClock()),
	).NewStaticClient(policy)
	if err != nil {
		t.Fatalf("NewStaticClient() error = %v", err)
	}

	_, err = client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    "/latest/meta-data",
		Labels: validTestLabels(),
	})
	assertErrorCode(t, err, connector.FailurePolicyDenied)
	events := observer.Events()
	if len(events) != 1 {
		t.Fatalf("observer events = %d, want 1", len(events))
	}
	if events[0].PolicyOutcome != PolicyOutcomeBlockedIP {
		t.Fatalf(
			"PolicyOutcome = %q, want %q",
			events[0].PolicyOutcome,
			PolicyOutcomeBlockedIP,
		)
	}
}

func TestClientDistinguishesTotalTimeoutFromCallerCancellation(t *testing.T) {
	t.Parallel()

	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		_, _ = io.WriteString(writer, "unexpected")
	}))

	t.Run("total timeout", func(t *testing.T) {
		policy := harness.policy()
		policy.RequestTimeout = time.Nanosecond
		client := harness.client(t, policy)
		_, err := client.Do(context.Background(), Request{
			Method: http.MethodGet,
			URL:    "/",
			Labels: validTestLabels(),
		})
		assertErrorCode(t, err, connector.FailureTimeout)
	})

	t.Run("caller cancellation", func(t *testing.T) {
		client := harness.client(t, harness.policy())
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.Do(ctx, Request{
			Method: http.MethodGet,
			URL:    "/",
			Labels: validTestLabels(),
		})
		assertErrorCode(t, err, connector.FailureCanceled)
	})
}

func TestClientOneTimeoutBudgetSpansRedirectReadAndRetryBackoff(
	t *testing.T,
) {
	t.Parallel()

	var (
		startCalls atomic.Int64
		finalCalls atomic.Int64
	)
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		switch request.URL.Path {
		case "/start":
			startCalls.Add(1)
			http.Redirect(writer, request, "/retry", http.StatusFound)
		case "/retry":
			finalCalls.Add(1)
			http.Error(
				writer,
				"bounded retry response",
				http.StatusServiceUnavailable,
			)
		default:
			http.NotFound(writer, request)
		}
	}))
	policy := harness.policy()
	policy.RequestTimeout = 100 * time.Millisecond
	policy.Retry = RetryPolicy{
		MaxRetries:     1,
		InitialBackoff: time.Second,
		MaxBackoff:     time.Second,
	}
	clock := &contextBlockingClock{}
	client := harness.client(t, policy, WithClock(clock))

	_, err := client.Do(context.Background(), Request{
		Method: http.MethodGet,
		URL:    "/start",
		Labels: validTestLabels(),
	})
	assertErrorCode(t, err, connector.FailureTimeout)
	if got := clock.sleepCalls.Load(); got != 1 {
		t.Fatalf("Sleep calls = %d, want 1", got)
	}
	if got := clock.lastDuration.Load(); got != int64(time.Second) {
		t.Fatalf("Sleep duration = %s, want 1s", time.Duration(got))
	}
	if startCalls.Load() != 1 || finalCalls.Load() != 1 {
		t.Fatalf(
			"handler calls start/retry = %d/%d, want 1/1",
			startCalls.Load(),
			finalCalls.Load(),
		)
	}
}

func TestClientIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	const requestTotal = 32
	var handled atomic.Int64
	harness := newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		handled.Add(1)
		_, _ = io.WriteString(writer, request.URL.Query().Get("id"))
	}))
	client := harness.client(t, harness.policy())

	start := make(chan struct{})
	errs := make(chan error, requestTotal)
	var wait sync.WaitGroup
	for i := 0; i < requestTotal; i++ {
		i := i
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			want := strconv.Itoa(i)
			response, err := client.Do(context.Background(), Request{
				Method: http.MethodGet,
				URL:    "/concurrent",
				Query:  url.Values{"id": {want}},
				Labels: validTestLabels(),
			})
			if err != nil {
				errs <- err
				return
			}
			if string(response.Body) != want {
				errs <- fmt.Errorf("response body = %q, want %q", response.Body, want)
			}
		}()
	}
	close(start)
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	if got := handled.Load(); got != requestTotal {
		t.Fatalf("handled requests = %d, want %d", got, requestTotal)
	}
}

type authorizerFunc struct {
	apply     func(*http.Request) error
	footprint CredentialFootprint
}

type statefulJSONMarshaler struct {
	calls atomic.Int64
}

func (marshaler *statefulJSONMarshaler) MarshalJSON() ([]byte, error) {
	attempt := marshaler.calls.Add(1)
	return []byte(fmt.Sprintf(`{"attempt":%d}`, attempt)), nil
}

type resolverFunc func(
	context.Context,
	string,
	string,
) ([]netip.Addr, error)

func (resolver resolverFunc) LookupNetIP(
	ctx context.Context,
	network string,
	host string,
) ([]netip.Addr, error) {
	return resolver(ctx, network, host)
}

func (authorizer authorizerFunc) Apply(request *http.Request) error {
	return authorizer.apply(request)
}

func (authorizer authorizerFunc) Footprint() CredentialFootprint {
	return authorizer.footprint.Clone()
}

type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

type contextBlockingClock struct {
	sleepCalls   atomic.Int64
	lastDuration atomic.Int64
}

func (*contextBlockingClock) Now() time.Time {
	return time.Now()
}

func (clock *contextBlockingClock) Sleep(
	ctx context.Context,
	duration time.Duration,
) error {
	clock.sleepCalls.Add(1)
	clock.lastDuration.Store(int64(duration))
	<-ctx.Done()
	return context.Cause(ctx)
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 7, 23, 0, 0, 0, 0, time.UTC)}
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeClock) Sleep(ctx context.Context, duration time.Duration) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.sleeps = append(clock.sleeps, duration)
	clock.now = clock.now.Add(duration)
	return nil
}

func (clock *fakeClock) SleepDurations() []time.Duration {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return append([]time.Duration(nil), clock.sleeps...)
}

type eventCollector struct {
	mu     sync.Mutex
	events []RequestEvent
}

func (collector *eventCollector) Observe(_ context.Context, event RequestEvent) {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	collector.events = append(collector.events, event)
}

func (collector *eventCollector) Events() []RequestEvent {
	collector.mu.Lock()
	defer collector.mu.Unlock()
	return append([]RequestEvent(nil), collector.events...)
}

type providerHarness struct {
	server       *http.Server
	listener     net.Listener
	port         string
	resolver     *resolverStub
	serveStopped chan struct{}
}

func newProviderHarness(t *testing.T, handler http.Handler) *providerHarness {
	t.Helper()

	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen() error = %v", err)
	}
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		_ = listener.Close()
		t.Fatalf("net.SplitHostPort() error = %v", err)
	}
	harness := &providerHarness{
		server:       &http.Server{Handler: handler},
		listener:     listener,
		port:         port,
		resolver:     &resolverStub{addresses: testAddresses("8.8.8.8")},
		serveStopped: make(chan struct{}),
	}
	go func() {
		defer close(harness.serveStopped)
		_ = harness.server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = harness.server.Close()
		<-harness.serveStopped
	})
	return harness
}

func (harness *providerHarness) primaryHost() string {
	return net.JoinHostPort("provider.test", harness.port)
}

func (harness *providerHarness) redirectHost() string {
	return net.JoinHostPort("redirect.test", harness.port)
}

func (harness *providerHarness) policy() Policy {
	baseURL := "http://" + harness.primaryHost() + "/"
	return Policy{
		Provider:         "test",
		BaseURL:          baseURL,
		AllowedOrigins:   []string{"http://" + harness.primaryHost()},
		NetworkMode:      SelfHostedOptIn,
		AllowPlainHTTP:   true,
		RequestTimeout:   2 * time.Second,
		MaxResponseBytes: 1024,
		Retry: RetryPolicy{
			MaxRetries:     0,
			InitialBackoff: time.Nanosecond,
			MaxBackoff:     time.Nanosecond,
		},
	}
}

// client builds a guarded Client whose validated literal address is dialed into
// the harness listener. The default clock never sleeps for real; options are
// applied last, so a test can substitute its own resolver, dialer, clock, or
// observer.
func (harness *providerHarness) client(
	t *testing.T,
	policy Policy,
	options ...FactoryOption,
) *Client {
	t.Helper()

	factory := NewFactory(append([]FactoryOption{
		WithInsecureProviderHTTP(true),
		WithResolver(harness.resolver),
		WithDialContext(func(
			ctx context.Context,
			network string,
			_ string,
		) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, harness.listener.Addr().String())
		}),
		WithClock(newFakeClock()),
	}, options...)...)
	client, err := factory.newClient(policy)
	if err != nil {
		t.Fatalf("newClient() error = %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)
	return client
}

// hostOnPort rewrites the hostname of host while keeping the harness listener
// port, so a handler can address the other test origin using only the Host it
// was reached on.
func hostOnPort(host, name string) string {
	_, port, err := net.SplitHostPort(host)
	if err != nil {
		return "invalid.test:1"
	}
	return net.JoinHostPort(name, port)
}

// requestedStatus reads the redirect status a test asked the harness to emit,
// so one harness can serve every redirect status instead of one server per case.
// It returns 0 when the caller did not ask for a valid redirect.
func requestedStatus(request *http.Request) int {
	status, err := strconv.Atoi(request.URL.Query().Get("status"))
	if err != nil || status < 300 || status > 399 {
		return 0
	}
	return status
}

const (
	redirectChainAuthorization = "Bearer header-secret"
	redirectChainHeaderKey     = "header-key-secret"
	redirectChainQueryKey      = "query-secret"
	redirectChainSessionID     = "session-secret"
)

// redirectHop is one request the Provider actually received.
type redirectHop struct {
	host          string
	path          string
	authorization string
	headerKey     string
	queryKey      string
	sessionID     string
	referer       string
	keep          string
}

// redirectChain is the shared four-hop scenario that the buffered Client and
// the streaming HTTPClient must handle identically:
//
//	/start -> /same (same origin) -> /cross (cross origin) -> /back (back to the
//	original origin, still tainted)
//
// The same-origin Location omits api_key, so the pre-cross hops prove the client
// restores the declared query credential from its own initial snapshot instead
// of re-running the authorizer. The /cross and /back Locations reflect api_key
// back, so the tainted hops prove reflected credentials are stripped rather than
// forwarded — and that returning to the original origin does not lift the taint.
type redirectChain struct {
	harness    *providerHarness
	applyCalls atomic.Int64
	mu         sync.Mutex
	hops       []redirectHop
}

func newRedirectChain(t *testing.T) *redirectChain {
	t.Helper()

	chain := &redirectChain{}
	chain.harness = newProviderHarness(t, http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		chain.mu.Lock()
		chain.hops = append(chain.hops, redirectHop{
			host:          request.Host,
			path:          request.URL.Path,
			authorization: request.Header.Get("Authorization"),
			headerKey:     request.Header.Get("X-Api-Key"),
			queryKey:      request.URL.Query().Get("api_key"),
			sessionID:     request.Header.Get("Mcp-Session-Id"),
			referer:       request.Header.Get("Referer"),
			keep:          request.URL.Query().Get("keep"),
		})
		chain.mu.Unlock()

		switch request.URL.Path {
		case "/start":
			http.Redirect(writer, request, "/same?keep=same", http.StatusFound)
		case "/same":
			http.Redirect(
				writer,
				request,
				"http://"+hostOnPort(request.Host, "redirect.test")+
					"/cross?api_key=reflected&keep=cross",
				http.StatusFound,
			)
		case "/cross":
			http.Redirect(
				writer,
				request,
				"http://"+hostOnPort(request.Host, "provider.test")+
					"/back?api_key=reflected&keep=back",
				http.StatusFound,
			)
		case "/back":
			_, _ = io.WriteString(writer, "ok")
		default:
			http.Error(writer, "unexpected path", http.StatusNotFound)
		}
	}))
	return chain
}

// policy allows redirects to both test origins so the chain is rejected only by
// the credential taint rules, never by the origin allowlist.
func (chain *redirectChain) policy() Policy {
	policy := chain.harness.policy()
	policy.AllowedRedirectOrigins = []string{
		"http://" + chain.harness.redirectHost(),
		"http://" + chain.harness.primaryHost(),
	}
	return policy
}

// authorizer declares one header credential and one query credential; redirects
// must never re-run it.
func (chain *redirectChain) authorizer() Authorizer {
	return authorizerFunc{
		apply: func(request *http.Request) error {
			chain.applyCalls.Add(1)
			request.Header.Set("Authorization", redirectChainAuthorization)
			request.Header.Set("X-Api-Key", redirectChainHeaderKey)
			query := request.URL.Query()
			query.Set("api_key", redirectChainQueryKey)
			request.URL.RawQuery = query.Encode()
			return nil
		},
		footprint: CredentialFootprint{
			HeaderNames:     []string{"Authorization", "X-Api-Key"},
			QueryParamNames: []string{"api_key"},
		},
	}
}

// assert pins the whole chain at once: exactly one Apply, four hops in order,
// declared credentials plus the caller's session header retained before the
// cross-origin hop and permanently gone after it, Referer deleted on every hop,
// and non-credential query parameters untouched throughout.
func (chain *redirectChain) assert(t *testing.T) {
	t.Helper()

	if got := chain.applyCalls.Load(); got != 1 {
		t.Fatalf(
			"Authorizer.Apply calls = %d, want 1 (redirects must not reapply auth)",
			got,
		)
	}
	chain.mu.Lock()
	got := append([]redirectHop(nil), chain.hops...)
	chain.mu.Unlock()

	credentialed := func(host, path, keep string) redirectHop {
		return redirectHop{
			host:          host,
			path:          path,
			keep:          keep,
			authorization: redirectChainAuthorization,
			headerKey:     redirectChainHeaderKey,
			queryKey:      redirectChainQueryKey,
			sessionID:     redirectChainSessionID,
		}
	}
	primary := chain.harness.primaryHost()
	want := []redirectHop{
		credentialed(primary, "/start", "start"),
		credentialed(primary, "/same", "same"),
		{host: chain.harness.redirectHost(), path: "/cross", keep: "cross"},
		{host: primary, path: "/back", keep: "back"},
	}
	if len(got) != len(want) {
		t.Fatalf("redirect hops = %#v, want %d hops", got, len(want))
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("hop %d = %#v, want %#v", index, got[index], want[index])
		}
	}
}

func redirectRequest(
	t *testing.T,
	method string,
	rawURL string,
	withBody bool,
) *http.Request {
	t.Helper()

	var body io.Reader
	if withBody {
		body = strings.NewReader("body")
	}
	request, err := http.NewRequest(method, rawURL, body)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	return request
}

func setRedirectCredentials(request *http.Request) {
	request.Header.Set("Authorization", "Bearer credential")
	request.Header.Set("Cookie", "session=credential")
	request.Header.Set("Proxy-Authorization", "Basic credential")
	request.Header.Set("X-Api-Key", "credential")
	request.Header.Set("X-Signature", "credential")
	request.Header.Set("X-Unrelated", "preserved")
}

func assertRedirectCredentialsRemoved(t *testing.T, request *http.Request) {
	t.Helper()

	for _, header := range []string{
		"Authorization",
		"Cookie",
		"Proxy-Authorization",
		"X-Api-Key",
		"X-Signature",
	} {
		if got := request.Header.Get(header); got != "" {
			t.Errorf("%s = %q, want removed", header, got)
		}
	}
	for _, name := range []string{"api_key", "signature"} {
		if got := request.URL.Query().Get(name); got != "" {
			t.Errorf("query %s = %q, want removed", name, got)
		}
	}
}

func assertPolicyViolation(t *testing.T, err error, want PolicyOutcome) {
	t.Helper()

	if err == nil {
		t.Fatal("error = nil, want policy violation")
	}
	if got := policyOutcomeForError(err); got != want {
		t.Fatalf("policy outcome = %q, want %q", got, want)
	}
	assertErrorCode(t, err, connector.FailurePolicyDenied)
}

func assertErrorCode(t *testing.T, err error, want connector.FailureCode) {
	t.Helper()

	if err == nil {
		t.Fatalf("error = nil, want code %q", want)
	}
	var providerError *Error
	if !errors.As(err, &providerError) {
		t.Fatalf("error type = %T, want chain containing *Error", err)
	}
	if got := providerError.Code(); got != want {
		t.Fatalf("error code = %q, want %q (error %v)", got, want, err)
	}
}

func validTestLabels() RequestLabels {
	return RequestLabels{
		ConnectorType: "test",
		ToolID:        "test.request",
		ConnectionID:  "connection-id",
	}
}
