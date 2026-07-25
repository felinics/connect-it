package stripe

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/smoketest"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	stripeSmokeAPIKeyEnv = "CONNECT_IT_STRIPE_SMOKE_API_KEY"

	stripeSmokeConnection = "stripe-real-smoke"
)

// TestStripeRealSmoke is an opt-in real-account harness. It validates the key
// against the Stripe API, then calls the read-only Tools through the official
// MCP Go SDK over a providerkit-guarded client.
//
// It has no write gate because it performs no writes: create_refund moves real
// money and is irreversible, so it is deliberately never exercised here. The
// key must still be test-mode. Nothing here logs the key, the account profile,
// MCP payloads, or Provider error text.
func TestStripeRealSmoke(t *testing.T) {
	apiKey := smoketest.Config(t, "Stripe", parseStripeSmokeConfig)

	factory, err := providerkit.NewFactoryFromEnv()
	if err != nil {
		t.Fatal("construct Stripe provider client factory")
	}
	validators, err := NewCredentialValidators(factory)
	if err != nil {
		t.Fatal("construct Stripe credential validators")
	}
	validator := validators[stripeAuthMethod]
	if validator == nil {
		t.Fatal("Stripe credential validator is not registered")
	}
	input := func(connectionID string, key string) connector.CredentialValidationInput {
		return connector.CredentialValidationInput{
			ConnectorType: Definition.Type,
			AuthMethodKey: stripeAuthMethod,
			AuthType:      connector.AuthAPIKey,
			ConnectionID:  connectionID,
			Fields:        map[string]string{stripeAPIKeyField: key},
		}
	}
	validation, err := validator(
		t.Context(),
		input(stripeSmokeConnection, apiKey),
	)
	if err != nil {
		t.Fatal("validate Stripe credential")
	}
	if validation.Profile.AccountID == "" ||
		validation.Profile.DisplayName == "" ||
		validation.ScopesKnown ||
		len(validation.GrantedScopes) != 0 {
		t.Fatal("Stripe validator returned an invalid profile or scope snapshot")
	}
	smoketest.RequireRejectedCredential(t, validator, input(
		stripeSmokeConnection+"-invalid-auth",
		"connect-it-deliberately-invalid-stripe-key",
	))

	if len(Definition.RemoteMCPServers) != 1 {
		t.Fatal("Stripe real smoke expects exactly one reviewed MCP server")
	}
	server := Definition.RemoteMCPServers[0]
	definitionRegistry := smoketest.Registry(t, Definition)
	policyClient, err := factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          server.Endpoint.URL,
		AllowedOrigins:   []string{"https://" + stripeMCPHostname + ":443"},
		RedirectMode:     providerkit.RedirectFollowPolicy,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   server.RequestTimeout,
		MaxResponseBytes: providerkit.DefaultMaxResponseBytes,
		Retry:            providerkit.RetryPolicy{Disabled: true},
	})
	if err != nil {
		t.Fatal("construct Stripe guarded MCP client")
	}
	t.Cleanup(policyClient.CloseIdleConnections)
	authorizer, err := providerkit.Bearer(apiKey)
	if err != nil {
		t.Fatal("construct Stripe smoke authorization")
	}
	httpClient, err := policyClient.HTTPClient(
		authorizer,
		providerkit.RequestLabels{
			ConnectorType:   string(Definition.Type),
			Operation:       "real_smoke",
			AuthorizationID: stripeSmokeConnection,
		},
	)
	if err != nil {
		t.Fatal("construct Stripe guarded MCP transport")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	session := smoketest.Connect(
		t,
		ctx,
		"connect-it-stripe-real-smoke",
		server.Endpoint.URL,
		httpClient,
	)
	smoketest.RunRemote(
		t,
		ctx,
		definitionRegistry,
		Definition,
		session,
		"get_stripe_account_info",
		map[string]any{},
	)
}

func parseStripeSmokeConfig(lookup smoketest.Lookup) (string, bool, error) {
	values, configured, err := smoketest.Values(
		"Stripe",
		lookup,
		stripeSmokeAPIKeyEnv,
	)
	if !configured || err != nil {
		return "", configured, err
	}
	// This restriction belongs only to the smoke harness. Normal connector
	// authentication deliberately treats Stripe keys as opaque.
	apiKey := values[stripeSmokeAPIKeyEnv]
	if !strings.HasPrefix(apiKey, "sk_test_") &&
		!strings.HasPrefix(apiKey, "rk_test_") {
		return "", true, fmt.Errorf(
			"%s must be a Stripe test-mode key",
			stripeSmokeAPIKeyEnv,
		)
	}
	if !validStripeAPIKey(apiKey) {
		return "", true, fmt.Errorf("%s is invalid", stripeSmokeAPIKeyEnv)
	}
	return apiKey, true, nil
}

// The shared harness covers the env gate itself; this covers the one rule that
// keeps a real smoke run away from live money.
func TestStripeSmokeConfigRequiresTestModeKey(t *testing.T) {
	t.Parallel()
	for _, unsafe := range []string{"sk_live_never_use", "rk_live_never_use"} {
		if _, _, err := parseStripeSmokeConfig(smoketest.MapLookup(
			map[string]string{stripeSmokeAPIKeyEnv: unsafe},
		)); err == nil {
			t.Errorf("%s accepted a live-mode key", stripeSmokeAPIKeyEnv)
		}
	}
	apiKey, configured, err := parseStripeSmokeConfig(smoketest.MapLookup(
		map[string]string{stripeSmokeAPIKeyEnv: "sk_test_smoke"},
	))
	if !configured || err != nil || apiKey != "sk_test_smoke" {
		t.Fatalf("complete Stripe smoke config = configured %t err %v", configured, err)
	}
}
