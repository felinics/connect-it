package gmail

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

func managedCall(
	toolID string,
	arguments map[string]any,
) connector.ToolCallContext {
	return connector.ToolCallContext{
		ConnectorType: Definition.Type,
		ToolID:        toolID,
		ConnectionID:  "gmail-managed-test",
		Arguments:     arguments,
		AccessToken:   "at-token",
		TokenType:     "Bearer",
	}
}

// managedServer starts a guarded test client bound to this Provider identity.
func managedServer(t *testing.T, handler http.HandlerFunc) *testkit.Server {
	t.Helper()
	options := testkit.ServerOptions{Provider: string(Definition.Type)}
	return testkit.NewServerWithOptions(t, handler, options)
}

// statusServer replies with one upstream status, optional Retry-After and a
// body carrying Provider secrets that must never reach the Agent.
func statusServer(
	t *testing.T,
	status int,
	retryAfter string,
	body string,
) *testkit.Server {
	t.Helper()
	return managedServer(t, func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		if retryAfter != "" {
			writer.Header().Set("Retry-After", retryAfter)
		}
		writer.WriteHeader(status)
		_, _ = writer.Write([]byte(body))
	})
}

func TestManagedListMessagesPreservesSuccessfulOutput(t *testing.T) {
	const upstream = `{"messages":[{"id":"m1"}],"nextPageToken":"next","resultSizeEstimate":1}`
	var gotMethod, gotPath, gotQuery, gotAuth, gotContentType string
	var gotBody []byte
	server := managedServer(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		gotMethod = request.Method
		gotPath = request.URL.Path
		gotQuery = request.URL.RawQuery
		gotAuth = request.Header.Get("Authorization")
		gotContentType = request.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(request.Body)
		_, _ = writer.Write([]byte(upstream))
	})

	result, err := newHandlers(server.Client)["list_messages"](
		t.Context(),
		managedCall("list_messages", map[string]any{
			"q":           "is:unread",
			"max_results": float64(5),
		}),
	)
	if err != nil || result.Failed() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	query, queryErr := url.ParseQuery(gotQuery)
	if queryErr != nil {
		t.Fatalf("query is invalid: %v", queryErr)
	}
	if gotMethod != http.MethodGet ||
		gotPath != "/gmail/v1/users/me/messages" ||
		len(query) != 2 ||
		query.Get("maxResults") != "5" ||
		query.Get("q") != "is:unread" {
		t.Fatalf(
			"request=%s %q?%s",
			gotMethod,
			gotPath,
			gotQuery,
		)
	}
	if gotAuth != "Bearer at-token" {
		t.Fatalf("Authorization = %q", gotAuth)
	}
	if gotContentType != "" || len(gotBody) != 0 {
		t.Fatalf(
			"GET content-type/body = %q/%q, want empty",
			gotContentType,
			gotBody,
		)
	}
	if string(result.Structured) != upstream {
		t.Fatalf(
			"successful Structured changed:\n got %s\nwant %s",
			result.Structured,
			upstream,
		)
	}
	if result.Text != "" || result.Failure != nil {
		t.Fatalf("success shape changed: %+v", result)
	}
}

func TestManagedSendMessagePreservesRequestAndOutput(t *testing.T) {
	const upstream = `{"id":"sent1","threadId":"t1"}`
	var gotBody []byte
	var gotPath, gotQuery, gotAuth string
	server := managedServer(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if request.Method != http.MethodPost ||
			request.URL.Path != "/gmail/v1/users/me/messages/send" {
			t.Errorf(
				"request = %s %s",
				request.Method,
				request.URL.Path,
			)
		}
		gotPath = request.URL.Path
		gotQuery = request.URL.RawQuery
		gotAuth = request.Header.Get("Authorization")
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		gotBody, _ = io.ReadAll(request.Body)
		_, _ = writer.Write([]byte(upstream))
	})

	result, err := newHandlers(server.Client)["send_message"](
		t.Context(),
		managedCall("send_message", map[string]any{
			"to":      "a@b.c",
			"subject": "hi",
			"body":    "你好",
		}),
	)
	if err != nil || result.Failed() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if gotPath != "/gmail/v1/users/me/messages/send" ||
		gotQuery != "" ||
		gotAuth != "Bearer at-token" {
		t.Fatalf(
			"path/query/auth = %q/%q/%q",
			gotPath,
			gotQuery,
			gotAuth,
		)
	}
	var payload struct {
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal(gotBody, &payload); err != nil {
		t.Fatal(err)
	}
	raw, err := base64.URLEncoding.DecodeString(payload.Raw)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "To: a@b.c") ||
		!strings.Contains(string(raw), "你好") {
		t.Fatalf("RFC 2822 body = %q", raw)
	}
	if string(result.Structured) != upstream {
		t.Fatalf("Structured = %s", result.Structured)
	}
}

func TestManagedInputFailureUsesToolFailure(t *testing.T) {
	result, err := newHandlers(nil)["send_message"](
		t.Context(),
		managedCall("send_message", map[string]any{"to": "a@b.c"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Failed() || result.Failure == nil ||
		result.Failure.Code != connector.FailureInvalidInput {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Structured) != 0 {
		t.Fatal("failure must not carry Structured output")
	}
}

func TestManagedSendMessageRejectsHeaderInjectionBeforeHTTP(
	t *testing.T,
) {
	var hits atomic.Int32
	server := managedServer(t, func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		hits.Add(1)
		_, _ = writer.Write([]byte(`{"id":"sent"}`))
	})
	send := newHandlers(server.Client)["send_message"]
	tests := []struct {
		name string
		args map[string]any
	}{
		{
			name: "recipient CRLF",
			args: map[string]any{
				"to":      "a@example.com\r\nBcc: hidden@example.com",
				"subject": "hello",
				"body":    "safe",
			},
		},
		{
			name: "subject LF",
			args: map[string]any{
				"to":      "a@example.com",
				"subject": "hello\nBcc: hidden@example.com",
				"body":    "safe",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := hits.Load()
			result, err := send(
				t.Context(),
				managedCall("send_message", test.args),
			)
			if err != nil || result.Failure == nil ||
				result.Failure.Code != connector.FailureInvalidInput {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if hits.Load() != before {
				t.Fatal("header injection reached HTTP")
			}
		})
	}

	result, err := send(
		t.Context(),
		managedCall("send_message", map[string]any{
			"to":      "a@example.com",
			"subject": "hello",
			"body":    "line one\nline two",
		}),
	)
	if err != nil || result.Failed() {
		t.Fatalf("multiline body result=%+v err=%v", result, err)
	}
	if hits.Load() != 1 {
		t.Fatalf("multiline body HTTP hits = %d, want 1", hits.Load())
	}
}

// Both Gmail Tools reach the Provider through the one managedHandler.call /
// restkit.Transport path, so the status contract is pinned once, on the read
// Tool.
func TestManagedHTTPFailureMappingAndRedaction(t *testing.T) {
	const providerSecret = "provider-body-secret"
	arguments := map[string]any{
		"q":           "is:unread",
		"max_results": float64(5),
	}
	tests := []struct {
		name              string
		status            int
		retryAfter        string
		wantCode          connector.FailureCode
		wantRetry         int
		credentialInvalid bool
	}{
		{"unauthorized", 401, "", connector.FailureAuthorizationFailed, 0, true},
		{"forbidden", 403, "", connector.FailurePermissionDenied, 0, false},
		{"bad request", 400, "", connector.FailureProviderError, 0, false},
		{"not found", 404, "", connector.FailureNotFound, 0, false},
		{"conflict", 409, "", connector.FailureConflict, 0, false},
		{"rate limited", 429, "7", connector.FailureRateLimited, 7, false},
		{"server unavailable", 503, "", connector.FailureUpstreamUnavailable, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := statusServer(
				t,
				test.status,
				test.retryAfter,
				`{"error":"`+providerSecret+`:at-token"}`,
			)
			result, err := newHandlers(server.Client)["list_messages"](
				t.Context(),
				managedCall("list_messages", arguments),
			)
			if err != nil {
				t.Fatalf("HTTP failure returned Go error: %v", err)
			}
			if !result.Failed() || result.Failure == nil ||
				result.Failure.Code != test.wantCode ||
				result.Failure.UpstreamStatus != test.status ||
				result.Failure.RetryAfterSeconds != test.wantRetry ||
				result.Failure.IndicatesCredentialInvalid() !=
					test.credentialInvalid {
				t.Fatalf("result = %+v", result)
			}
			public := result.Text +
				result.Failure.Message +
				string(result.Structured)
			if strings.Contains(public, providerSecret) ||
				strings.Contains(public, "at-token") {
				t.Fatalf("failure leaked Provider data: %q", public)
			}
			if len(result.Structured) != 0 {
				t.Fatal("failure must not carry upstream body")
			}
		})
	}
}

func TestManagedResponseIsBoundedAndInvalidJSONIsNormalized(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		maxBytes int64
		wantCode connector.FailureCode
	}{
		{
			name:     "bounded body",
			body:     `{"value":"` + strings.Repeat("x", 128) + `"}`,
			maxBytes: 32,
			wantCode: connector.FailureResponseTooLarge,
		},
		{
			name:     "invalid JSON",
			body:     `{"provider_secret":`,
			maxBytes: 64,
			wantCode: connector.FailureInvalidResponse,
		},
		{
			name:     "non-object JSON",
			body:     `["provider_secret"]`,
			maxBytes: 64,
			wantCode: connector.FailureInvalidResponse,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := testkit.NewServerWithOptions(
				t,
				http.HandlerFunc(func(
					writer http.ResponseWriter,
					_ *http.Request,
				) {
					_, _ = writer.Write([]byte(test.body))
				}),
				testkit.ServerOptions{
					Provider:         string(Definition.Type),
					MaxResponseBytes: test.maxBytes,
				},
			)
			result, err := newHandlers(server.Client)["list_messages"](
				t.Context(),
				managedCall("list_messages", nil),
			)
			if err != nil {
				t.Fatal(err)
			}
			if result.Failure == nil ||
				result.Failure.Code != test.wantCode {
				t.Fatalf("result = %+v", result)
			}
			if strings.Contains(
				result.Failure.Message+string(result.Structured),
				"provider_secret",
			) {
				t.Fatal("invalid response leaked body")
			}
		})
	}
}

func TestManagedInvalidTokenFailsBeforeHTTP(t *testing.T) {
	var hits atomic.Int32
	server := managedServer(t, func(http.ResponseWriter, *http.Request) {
		hits.Add(1)
	})
	tests := []struct {
		name       string
		mutateCall func(*connector.ToolCallContext)
	}{
		{
			name: "empty token",
			mutateCall: func(call *connector.ToolCallContext) {
				call.AccessToken = ""
			},
		},
		{
			name: "wrong token type",
			mutateCall: func(call *connector.ToolCallContext) {
				call.TokenType = "Basic"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			call := managedCall("list_messages", nil)
			test.mutateCall(&call)
			result, err := newHandlers(server.Client)["list_messages"](
				t.Context(),
				call,
			)
			if err != nil || result.Failure == nil ||
				result.Failure.Code !=
					connector.FailureAuthorizationFailed {
				t.Fatalf("result=%+v err=%v", result, err)
			}
			if result.Failure.IndicatesCredentialInvalid() {
				t.Fatal("local token shape must not impersonate an upstream 401")
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("HTTP hits = %d", hits.Load())
	}
}
