package testkit_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/providerkit/testkit"
)

func TestServerExercisesGuardedClientWithoutPublicNetwork(t *testing.T) {
	t.Parallel()

	server := testkit.NewServer(t, http.HandlerFunc(
		func(writer http.ResponseWriter, request *http.Request) {
			testkit.AssertHeader(t, request, "X-Test-Key", "secret")
			testkit.AssertQuery(t, request, "page", "2")
			testkit.AssertFormBody(t, request, url.Values{"name": {"example"}})
			writer.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(writer).Encode(map[string]bool{"ok": true})
		},
	))
	authorizer, err := providerkit.HeaderAPIKey("X-Test-Key", "secret")
	if err != nil {
		t.Fatal(err)
	}
	response, err := server.Client.Do(context.Background(), providerkit.Request{
		Method:     http.MethodPost,
		URL:        server.URL("/items"),
		Query:      url.Values{"page": {"2"}},
		Form:       url.Values{"name": {"example"}},
		Authorizer: authorizer,
		Labels: providerkit.RequestLabels{
			ConnectorType: "test_provider",
			ToolID:        "create_item",
			ConnectionID:  "testkit-connection",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var body struct {
		OK bool `json:"ok"`
	}
	if err := response.DecodeJSON(&body); err != nil {
		t.Fatal(err)
	}
	if !body.OK {
		t.Fatal("response did not contain ok=true")
	}
}

func TestServerOptionsSetProviderIdentityAndTimeout(t *testing.T) {
	t.Parallel()

	t.Run("provider identity", func(t *testing.T) {
		server := testkit.NewServerWithOptions(
			t,
			http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusNoContent)
			}),
			testkit.ServerOptions{Provider: "github"},
		)
		_, err := server.Client.Do(t.Context(), providerkit.Request{
			Method:     http.MethodGet,
			URL:        server.URL("/user"),
			Authorizer: providerkit.NoAuth(),
			Labels: providerkit.RequestLabels{
				ConnectorType:   "github",
				Operation:       "credential_validate",
				AuthorizationID: "testkit-authorization",
			},
		})
		if err != nil {
			t.Fatalf("custom Provider label rejected: %v", err)
		}

		_, err = server.Client.Do(t.Context(), providerkit.Request{
			Method:     http.MethodGet,
			URL:        server.URL("/user"),
			Authorizer: providerkit.NoAuth(),
			Labels: providerkit.RequestLabels{
				ConnectorType:   "test_provider",
				Operation:       "credential_validate",
				AuthorizationID: "testkit-authorization",
			},
		})
		failure := providerkit.AsToolFailure(err)
		if failure == nil ||
			failure.Code != connector.FailureConfigurationError {
			t.Fatalf("mismatched Provider label failure = %#v", failure)
		}
	})

	t.Run("request timeout", func(t *testing.T) {
		server := testkit.NewServerWithOptions(
			t,
			http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				t.Error("expired request reached handler")
			}),
			testkit.ServerOptions{
				Provider:       "github",
				RequestTimeout: time.Nanosecond,
			},
		)
		_, err := server.Client.Do(t.Context(), providerkit.Request{
			Method:     http.MethodGet,
			URL:        server.URL("/user"),
			Authorizer: providerkit.NoAuth(),
			Labels: providerkit.RequestLabels{
				ConnectorType:   "github",
				Operation:       "credential_validate",
				AuthorizationID: "testkit-authorization",
			},
		})
		failure := providerkit.AsToolFailure(err)
		if failure == nil || failure.Code != connector.FailureTimeout {
			t.Fatalf("timeout failure = %#v", failure)
		}
	})
}
