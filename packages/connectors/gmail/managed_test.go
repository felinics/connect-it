package gmail

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

// withTestServer 用 httptest.Server 覆盖包级 apiBaseURL，测试结束还原。
func withTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	old := apiBaseURL
	apiBaseURL = srv.URL
	t.Cleanup(func() {
		apiBaseURL = old
		srv.Close()
	})
}

func call(args map[string]any) connector.ToolCallContext {
	return connector.ToolCallContext{
		ConnectorType: "gmail", Arguments: args, AccessToken: "at-token",
	}
}

func TestListMessages(t *testing.T) {
	var gotPath, gotAuth string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.String()
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"messages":[{"id":"m1"}],"resultSizeEstimate":1}`))
	})

	res, err := listMessages(context.Background(), call(map[string]any{
		"q": "is:unread", "max_results": float64(5),
	}))
	if err != nil || res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if !strings.HasPrefix(gotPath, "/gmail/v1/users/me/messages") ||
		!strings.Contains(gotPath, "maxResults=5") ||
		!strings.Contains(gotPath, "q=is%3Aunread") {
		t.Fatalf("path: %s", gotPath)
	}
	if gotAuth != "Bearer at-token" {
		t.Fatalf("auth: %s", gotAuth)
	}
	if !strings.Contains(string(res.Structured), "m1") {
		t.Fatalf("structured: %s", res.Structured)
	}
}

func TestSendMessage(t *testing.T) {
	var gotBody []byte
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"sent1","threadId":"t1"}`))
	})

	res, err := sendMessage(context.Background(), call(map[string]any{
		"to": "a@b.c", "subject": "hi", "body": "你好",
	}))
	if err != nil || res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
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
	if !strings.Contains(string(raw), "To: a@b.c") || !strings.Contains(string(raw), "你好") {
		t.Fatalf("rfc822: %s", raw)
	}
}

func TestSendMessageMissingParams(t *testing.T) {
	res, err := sendMessage(context.Background(), call(map[string]any{"to": "a@b.c"}))
	if err != nil || !res.IsError {
		t.Fatalf("缺参数应 IsError: %+v err=%v", res, err)
	}
}

func TestAPIErrorBecomesIsError(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":403}}`, http.StatusForbidden)
	})
	res, err := listMessages(context.Background(), call(nil))
	if err != nil {
		t.Fatalf("HTTP 错误不应返回 Go error: %v", err)
	}
	if !res.IsError || !strings.Contains(string(res.Structured), "403") {
		t.Fatalf("res: %+v", res)
	}
}
