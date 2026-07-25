package onedrive

import (
	"encoding/json"
	"io"
	"net/http"
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
		ConnectionID:  "onedrive-managed-test",
		Arguments:     arguments,
		AccessToken:   "graph-token",
		TokenType:     "Bearer",
	}
}

// managedServer starts a guarded test client bound to this Provider identity.
func managedServer(t *testing.T, handler http.HandlerFunc) *testkit.Server {
	t.Helper()
	options := testkit.ServerOptions{Provider: string(Definition.Type)}
	return testkit.NewServerWithOptions(t, handler, options)
}

func TestManagedListDriveItemsPreservesSuccessfulOutput(t *testing.T) {
	const upstream = `{"value":[{"id":"i1","name":"docs"}],"@odata.nextLink":"https://graph.microsoft.com/v1.0/me/drive/root/children?$skiptoken=next"}`
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
	result, err := newHandlers(server.Client)["list_drive_items"](
		t.Context(),
		managedCall("list_drive_items", nil),
	)
	if err != nil || result.Failed() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if gotMethod != http.MethodGet ||
		gotPath != "/v1.0/me/drive/root/children" ||
		gotQuery != "" ||
		gotAuth != "Bearer graph-token" ||
		gotContentType != "" ||
		len(gotBody) != 0 {
		t.Fatalf(
			"request=%s %q?%s auth=%q content-type=%q body=%q",
			gotMethod,
			gotPath,
			gotQuery,
			gotAuth,
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

func TestManagedListDriveItemsEscapesEachPathSegment(t *testing.T) {
	var gotEscapedPath string
	server := managedServer(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		gotEscapedPath = request.URL.EscapedPath()
		_, _ = writer.Write([]byte(`{"value":[]}`))
	})
	result, err := newHandlers(server.Client)["list_drive_items"](
		t.Context(),
		managedCall("list_drive_items", map[string]any{
			"path": "文档/2026 报告",
		}),
	)
	if err != nil || result.Failed() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if !strings.Contains(gotEscapedPath, "/root:/") ||
		!strings.Contains(gotEscapedPath, ":/children") ||
		strings.Contains(gotEscapedPath, " ") {
		t.Fatalf("escaped path = %q", gotEscapedPath)
	}
}

func TestManagedUploadFilePreservesRequestAndOutput(t *testing.T) {
	const upstream = `{"id":"f1","name":"a.txt","size":5}`
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
	result, err := newHandlers(server.Client)["upload_file"](
		t.Context(),
		managedCall("upload_file", map[string]any{
			"path": "notes/a.txt", "content": "hello",
		}),
	)
	if err != nil || result.Failed() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if gotMethod != http.MethodPut ||
		gotPath != "/v1.0/me/drive/root:/notes/a.txt:/content" ||
		gotQuery != "" ||
		gotAuth != "Bearer graph-token" ||
		gotContentType != uploadContentMediaType ||
		string(gotBody) != "hello" {
		t.Fatalf(
			"request=%s %q?%s auth=%q content-type=%q body=%q",
			gotMethod,
			gotPath,
			gotQuery,
			gotAuth,
			gotContentType,
			gotBody,
		)
	}
	if string(result.Structured) != upstream {
		t.Fatalf("Structured = %s", result.Structured)
	}
}

func TestManagedUploadFileDecodedUTF8ByteLimit(t *testing.T) {
	var hits atomic.Int32
	var receivedBytes atomic.Int64
	server := managedServer(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		hits.Add(1)
		count, _ := io.Copy(io.Discard, request.Body)
		receivedBytes.Store(count)
		_, _ = writer.Write([]byte(`{"id":"f1","name":"limit.txt"}`))
	})
	upload := newHandlers(server.Client)["upload_file"]

	tests := []struct {
		name              string
		content           string
		wantHTTP          bool
		wantFailure       connector.FailureCode
		wantReceivedBytes int64
	}{
		{
			name:              "exactly 4 MiB ASCII passes",
			content:           strings.Repeat("a", maxUploadContentBytes),
			wantHTTP:          true,
			wantReceivedBytes: maxUploadContentBytes,
		},
		{
			name: "exactly 4 MiB multibyte passes",
			content: strings.Repeat(
				"界",
				(maxUploadContentBytes-1)/len("界"),
			) + "x",
			wantHTTP:          true,
			wantReceivedBytes: maxUploadContentBytes,
		},
		{
			name:        "4 MiB plus one byte rejected",
			content:     strings.Repeat("a", maxUploadContentBytes+1),
			wantFailure: connector.FailureInputTooLarge,
		},
		{
			name:        "invalid UTF-8 rejected",
			content:     string([]byte{0xff}),
			wantFailure: connector.FailureInvalidInput,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			before := hits.Load()
			result, err := upload(
				t.Context(),
				managedCall("upload_file", map[string]any{
					"path": "limit.txt", "content": test.content,
				}),
			)
			if err != nil {
				t.Fatal(err)
			}
			if test.wantHTTP {
				if result.Failed() {
					t.Fatalf("result = %+v", result)
				}
				if hits.Load() != before+1 {
					t.Fatalf("HTTP hits = %d, want %d", hits.Load(), before+1)
				}
				if receivedBytes.Load() != test.wantReceivedBytes {
					t.Fatalf(
						"received bytes = %d, want %d",
						receivedBytes.Load(),
						test.wantReceivedBytes,
					)
				}
				return
			}
			if result.Failure == nil ||
				result.Failure.Code != test.wantFailure {
				t.Fatalf("result = %+v", result)
			}
			if hits.Load() != before {
				t.Fatalf("oversized/invalid content reached HTTP")
			}
		})
	}
}

func TestManagedUploadEscapedEnvelopeUsesDecodedLimit(t *testing.T) {
	content := strings.Repeat(`"`, 3<<20)
	envelope, err := json.Marshal(map[string]string{
		"path": "escaped.txt", "content": content,
	})
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(envelope)) <= connector.DefaultMaxInputBytes {
		t.Fatalf(
			"regression envelope = %d, want above default %d",
			len(envelope),
			connector.DefaultMaxInputBytes,
		)
	}
	if int64(len(envelope)) > connector.AbsoluteMaxInputBytes {
		t.Fatalf(
			"regression envelope = %d, want within override %d",
			len(envelope),
			connector.AbsoluteMaxInputBytes,
		)
	}
	var decoded map[string]any
	if err := json.Unmarshal(envelope, &decoded); err != nil {
		t.Fatal(err)
	}

	var received atomic.Int64
	server := managedServer(t, func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		count, _ := io.Copy(io.Discard, request.Body)
		received.Store(count)
		_, _ = writer.Write([]byte(`{"id":"escaped"}`))
	})
	result, err := newHandlers(server.Client)["upload_file"](
		t.Context(),
		managedCall("upload_file", decoded),
	)
	if err != nil || result.Failed() {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if received.Load() != int64(len([]byte(content))) {
		t.Fatalf(
			"received bytes = %d, want decoded %d",
			received.Load(),
			len([]byte(content)),
		)
	}
}

func TestManagedInputFailureUsesToolFailure(t *testing.T) {
	result, err := newHandlers(nil)["upload_file"](
		t.Context(),
		managedCall("upload_file", map[string]any{"path": "x.txt"}),
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

// Both OneDrive Tools reach Graph through the one managedHandler.call path, so
// the status contract is pinned once, on the read Tool.
func TestManagedHTTPFailureMappingAndRedaction(t *testing.T) {
	const providerSecret = "graph-provider-body-secret"
	arguments := map[string]any{"path": "docs"}
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
		{"rate limited", 429, "11", connector.FailureRateLimited, 11, false},
		{"server unavailable", 502, "", connector.FailureUpstreamUnavailable, 0, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := managedServer(t, func(
				writer http.ResponseWriter,
				_ *http.Request,
			) {
				if test.retryAfter != "" {
					writer.Header().Set("Retry-After", test.retryAfter)
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(
					`{"error":"` + providerSecret + `:graph-token"}`,
				))
			})
			result, err := newHandlers(server.Client)["list_drive_items"](
				t.Context(),
				managedCall("list_drive_items", arguments),
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
				strings.Contains(public, "graph-token") {
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
			result, err := newHandlers(server.Client)["list_drive_items"](
				t.Context(),
				managedCall("list_drive_items", nil),
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
			call := managedCall("list_drive_items", nil)
			test.mutateCall(&call)
			result, err := newHandlers(server.Client)["list_drive_items"](
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
