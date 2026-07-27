package connectit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	connectit "github.com/memohai/connect-it/sdk/go"
)

func TestClientAndMCPAuthHandler(t *testing.T) {
	var sessionCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cit_test" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/connectors":
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"type": "github", "name": "GitHub", "status": "ready",
				"auth_methods": []map[string]string{{"key": "oauth", "type": "oauth2", "label": "GitHub OAuth"}},
			}})
		case "/v1/mcp-sessions":
			call := sessionCalls.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"token":      fmt.Sprintf("session-%d", call),
				"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client, err := connectit.New(server.URL, "cit_test")
	if err != nil {
		t.Fatal(err)
	}
	connectors, err := client.ListConnectors(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(connectors) != 1 || connectors[0].Type != "github" ||
		len(connectors[0].AuthMethods) != 1 || connectors[0].AuthMethods[0].Key != "oauth" {
		t.Fatalf("unexpected connectors: %+v", connectors)
	}

	handler := client.MCPAuthHandler(connectit.MCPSessionConfig{
		ConnectionID: "connection-id",
		TTL:          time.Hour,
	})
	source, err := handler.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	first, err := source.Token()
	if err != nil {
		t.Fatal(err)
	}
	source, err = handler.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	second, err := source.Token()
	if err != nil {
		t.Fatal(err)
	}
	if first.AccessToken != "session-1" || second.AccessToken != first.AccessToken || sessionCalls.Load() != 1 {
		t.Fatalf("session was not cached: first=%q second=%q calls=%d",
			first.AccessToken, second.AccessToken, sessionCalls.Load())
	}

	response := &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       http.NoBody,
	}
	if err := handler.Authorize(context.Background(), nil, response); err != nil {
		t.Fatal(err)
	}
	source, err = handler.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := source.Token()
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AccessToken != "session-2" || sessionCalls.Load() != 2 {
		t.Fatalf("session was not refreshed: token=%q calls=%d",
			refreshed.AccessToken, sessionCalls.Load())
	}
}
