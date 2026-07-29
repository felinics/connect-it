package onedrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

// withTestServer overrides the package-level apiBaseURL with an
// httptest.Server and restores it when the test ends.
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

func call(args map[string]any) connector.ManagedCall {
	if args == nil {
		args = map[string]any{}
	}
	raw, _ := json.Marshal(args)
	return connector.ManagedCall{Arguments: raw, AccessToken: "graph-token"}
}

func TestListDriveItemsRoot(t *testing.T) {
	var gotPath, gotAuth string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"value":[{"id":"i1","name":"docs"}]}`))
	})
	res, err := listDriveItems(context.Background(), call(nil))
	if err != nil || res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if gotPath != "/v1.0/me/drive/root/children" || gotAuth != "Bearer graph-token" {
		t.Fatalf("path=%s auth=%s", gotPath, gotAuth)
	}
}

func TestListDriveItemsSubfolderEscaped(t *testing.T) {
	var gotPath string
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		_, _ = w.Write([]byte(`{"value":[]}`))
	})
	if _, err := listDriveItems(context.Background(), call(map[string]any{
		"path": "Reports/2026 Annual Report",
	})); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(gotPath, "/root:/") || !strings.Contains(gotPath, ":/children") {
		t.Fatalf("path: %s", gotPath)
	}
	if strings.Contains(gotPath, " ") {
		t.Fatalf("path was not escaped: %s", gotPath)
	}
}

func TestUploadFile(t *testing.T) {
	var gotMethod, gotCT string
	var gotBody []byte
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotCT = r.Header.Get("Content-Type")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"id":"f1","name":"a.txt","size":5}`))
	})
	res, err := uploadFile(context.Background(), call(map[string]any{
		"path": "notes/a.txt", "content": "hello",
	}))
	if err != nil || res.IsError {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if gotMethod != http.MethodPut || gotCT != "application/octet-stream" || string(gotBody) != "hello" {
		t.Fatalf("method=%s ct=%s body=%s", gotMethod, gotCT, gotBody)
	}
}

func TestUploadFileMissingParams(t *testing.T) {
	res, err := uploadFile(context.Background(), call(map[string]any{"path": "x.txt"}))
	if err != nil || !res.IsError {
		t.Fatalf("missing arguments should set IsError: %+v err=%v", res, err)
	}
}

func TestGraphErrorBecomesIsError(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"code":"itemNotFound"}}`, http.StatusNotFound)
	})
	res, err := listDriveItems(context.Background(), call(nil))
	if err != nil {
		t.Fatalf("an HTTP error must not surface as a Go error: %v", err)
	}
	structured := fmt.Sprint(res.StructuredContent)
	if !res.IsError || !strings.Contains(structured, "404") ||
		strings.Contains(structured, "itemNotFound") {
		t.Fatalf("res: %+v", res)
	}
}
