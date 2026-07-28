package managedapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

func TestImplementationBuildsConstrainedRequest(t *testing.T) {
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/items" ||
			r.URL.Query().Get("limit") != "10" {
			t.Errorf("request=%s %s", r.Method, r.URL.String())
			return
		}
		if r.Header.Get("Authorization") != "Bearer secret" ||
			r.Header.Get("X-API-Version") != "1" {
			t.Errorf("headers=%v", r.Header)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		if string(body) != `{"name":"example"}` {
			t.Errorf("body=%s", body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"item_1"}`))
	}))
	defer upstream.Close()

	previousClient := httpClient
	httpClient = upstream.Client()
	t.Cleanup(func() { httpClient = previousClient })

	managed := Implementation(Spec{
		ProviderName: "Example",
		BaseURL:      Fixed(upstream.URL + "/v1"),
		Auth:         Bearer(),
		Headers:      map[string]string{"X-API-Version": "1"},
	})
	result, err := managed.Tools[0].Handler(t.Context(), connector.ManagedCall{
		Arguments:   []byte(`{"method":"POST","path":"/items","query":{"limit":10},"body":{"name":"example"},"content_type":"application/json"}`),
		AccessToken: "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError {
		t.Fatalf("result=%+v", result)
	}
}

func TestImplementationRejectsPathTraversal(t *testing.T) {
	managed := Implementation(Spec{
		ProviderName: "Example",
		BaseURL:      Fixed("https://api.example.com"),
	})
	result, err := managed.Tools[0].Handler(t.Context(), connector.ManagedCall{
		Arguments: []byte(`{"method":"GET","path":"/../secret"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError {
		t.Fatalf("result=%+v", result)
	}
}
