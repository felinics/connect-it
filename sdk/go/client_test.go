package connectit_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	connectit "github.com/felinics/connect-it/sdk/go"
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
				"auth_methods": []map[string]any{
					{
						"key": "oauth", "type": "oauth2", "label": "GitHub OAuth",
						"credential_fields": []any{},
					},
					{
						"key": "token", "type": "api_key", "label": "Personal access token",
						"credential_fields": []map[string]any{{
							"key": "token", "label": "Token", "input_type": "text",
							"required": true, "secret": true, "default_value": nil,
							"description": "GitHub personal access token",
							"pattern":     "^github_pat_", "options": []string{},
						}},
					},
				},
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
		len(connectors[0].AuthMethods) != 2 || connectors[0].AuthMethods[0].Key != "oauth" {
		t.Fatalf("unexpected connectors: %+v", connectors)
	}
	tokenMethod := connectors[0].AuthMethods[1]
	if tokenMethod.Key != "token" || len(tokenMethod.CredentialFields) != 1 {
		t.Fatalf("unexpected token auth method: %+v", tokenMethod)
	}
	field := tokenMethod.CredentialFields[0]
	if field.Key != "token" || !field.Required || !field.Secret ||
		field.Pattern != "^github_pat_" || field.Options == nil {
		t.Fatalf("unexpected credential field: %+v", field)
	}

	handler := client.MCPAuthHandler(connectit.MCPSessionConfig{
		Connections: map[string]string{"github": "connection-id"},
		TTL:         time.Hour,
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

	staleRequest := httptest.NewRequest(http.MethodPost, server.URL+"/mcp", nil)
	staleRequest.Header.Set("Authorization", "Bearer "+first.AccessToken)
	if err := handler.Authorize(context.Background(), staleRequest, &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       http.NoBody,
	}); err != nil {
		t.Fatal(err)
	}
	source, err = handler.TokenSource(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	afterStaleResponse, err := source.Token()
	if err != nil {
		t.Fatal(err)
	}
	if afterStaleResponse.AccessToken != refreshed.AccessToken || sessionCalls.Load() != 2 {
		t.Fatalf("stale 401 invalidated current session: token=%q calls=%d",
			afterStaleResponse.AccessToken, sessionCalls.Load())
	}
}

func TestClientPreservesEscapedPathSegments(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.URL.EscapedPath(), "/control/v1/connections/type%20with%20space"; got != want {
			t.Errorf("escaped path = %q, want %q", got, want)
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	client, err := connectit.New(server.URL+"/control?ignored=true", "cit_test")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteConnection(t.Context(), "type with space"); err != nil {
		t.Fatal(err)
	}
}

func TestClientRejectsCrossOriginRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	t.Cleanup(redirectTarget.Close)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Redirect(w, request, redirectTarget.URL+"/credential-target", http.StatusFound)
	}))
	t.Cleanup(server.Close)

	client, err := connectit.New(server.URL, "cit_test")
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListConnectors(t.Context())
	var apiErr *connectit.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusFound {
		t.Fatalf("ListConnectors() error = %v, want HTTP %d API error", err, http.StatusFound)
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("cross-origin redirect requests = %d, want 0", redirectedRequests.Load())
	}
}

func TestMCPAuthHandlerReportsFailedConnections(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/mcp-sessions" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"token":      "session-1",
			"expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
			"failed_connections": []map[string]any{{
				"alias":         "gmail",
				"connection_id": "3f2c0069-c8ec-45db-b6af-42643ae003d5",
				"error":         "tools/list: Forbidden",
			}},
		})
	}))
	t.Cleanup(server.Close)

	client, err := connectit.New(server.URL, "cit_test")
	if err != nil {
		t.Fatal(err)
	}
	session, err := client.CreateMCPSession(t.Context(), connectit.MCPSessionConfig{
		Connections: map[string]string{"github": "a", "gmail": "b"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(session.FailedConnections) != 1 || session.FailedConnections[0].Alias != "gmail" ||
		session.FailedConnections[0].Error != "tools/list: Forbidden" {
		t.Fatalf("failed connections not decoded: %+v", session)
	}

	var observed []connectit.MCPSession
	handler := client.MCPAuthHandler(connectit.MCPSessionConfig{
		Connections:      map[string]string{"github": "a", "gmail": "b"},
		OnSessionCreated: func(session connectit.MCPSession) { observed = append(observed, session) },
	})
	for range 2 {
		source, err := handler.TokenSource(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := source.Token(); err != nil {
			t.Fatal(err)
		}
	}
	if len(observed) != 1 {
		t.Fatalf("observer must see each issued session exactly once, got %d", len(observed))
	}
	if observed[0].Token != "session-1" || len(observed[0].FailedConnections) != 1 ||
		observed[0].FailedConnections[0].Alias != "gmail" {
		t.Fatalf("observed=%+v", observed[0])
	}
	if err := handler.Authorize(context.Background(), nil, &http.Response{
		StatusCode: http.StatusUnauthorized, Body: http.NoBody,
	}); err != nil {
		t.Fatal(err)
	}
	if len(observed) != 2 {
		t.Fatalf("re-issued session must be observed too, got %d", len(observed))
	}
}
