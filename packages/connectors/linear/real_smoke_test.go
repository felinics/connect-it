package linear

import (
	"context"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/smoketest"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	linearSmokeAPIKeyEnv = "CONNECT_IT_LINEAR_SMOKE_API_KEY"
	linearSmokeTeamIDEnv = "CONNECT_IT_LINEAR_SMOKE_TEAM_ID"

	linearSmokeConnection = "linear-real-smoke"
)

// TestLinearRealSmoke is an opt-in, read-only real-account harness. It validates
// a personal API key against the Linear GraphQL API, then calls every published
// read Tool through the official MCP Go SDK over a providerkit-guarded client
// bound to https://mcp.linear.app/mcp.
//
// The write Tools (create_issue, update_issue, create_comment) are deliberately
// not exercised: the remote server owns their semantics, and a smoke run must
// not leave objects behind in a real workspace. There is therefore no write gate
// and no cleanup path.
//
// Nothing here logs the API key, the profile, MCP payloads or Provider error
// text.
func TestLinearRealSmoke(t *testing.T) {
	config := smoketest.Config(t, "Linear", parseLinearSmokeConfig)

	factory, err := providerkit.NewFactoryFromEnv()
	if err != nil {
		t.Fatal("construct Linear provider client factory")
	}
	validators, err := NewCredentialValidators(factory)
	if err != nil {
		t.Fatal("construct Linear credential validators")
	}
	validator, exists := validators[linearAuthMethod]
	if !exists {
		t.Fatal("Linear API-key credential validator is not registered")
	}
	validation, err := validator(
		t.Context(),
		connector.CredentialValidationInput{
			ConnectorType: Definition.Type,
			AuthMethodKey: linearAuthMethod,
			AuthType:      connector.AuthAPIKey,
			ConnectionID:  linearSmokeConnection,
			Fields:        map[string]string{linearAPIKeyField: config.apiKey},
		},
	)
	if err != nil {
		t.Fatal("validate Linear credential")
	}
	if validation.Profile.AccountID == "" ||
		validation.Profile.DisplayName == "" ||
		validation.ScopesKnown ||
		len(validation.GrantedScopes) != 0 {
		t.Fatal("Linear validator returned an invalid profile or scope snapshot")
	}

	if len(Definition.RemoteMCPServers) != 1 {
		t.Fatal("Linear real smoke expects exactly one reviewed MCP server")
	}
	server := Definition.RemoteMCPServers[0]
	definitionRegistry := smoketest.Registry(t, Definition)
	policyClient, err := factory.NewStaticClient(providerkit.Policy{
		Provider:         string(Definition.Type),
		BaseURL:          server.Endpoint.URL,
		AllowedOrigins:   []string{"https://mcp.linear.app:443"},
		RedirectMode:     providerkit.RedirectFollowPolicy,
		NetworkMode:      providerkit.PublicOnly,
		RequestTimeout:   server.RequestTimeout,
		MaxResponseBytes: providerkit.DefaultMaxResponseBytes,
		Retry:            providerkit.RetryPolicy{Disabled: true},
	})
	if err != nil {
		t.Fatal("construct Linear guarded MCP client")
	}
	t.Cleanup(policyClient.CloseIdleConnections)
	// The personal API key is presented to the MCP server as a Bearer token,
	// exactly as the Definition's auth binding declares.
	authorizer, err := providerkit.Bearer(config.apiKey)
	if err != nil {
		t.Fatal("construct Linear smoke authorization")
	}
	httpClient, err := policyClient.HTTPClient(
		authorizer,
		providerkit.RequestLabels{
			ConnectorType:   string(Definition.Type),
			Operation:       "real_smoke",
			AuthorizationID: linearSmokeConnection,
		},
	)
	if err != nil {
		t.Fatal("construct Linear guarded MCP transport")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	session := smoketest.Connect(
		t,
		ctx,
		"connect-it-linear-real-smoke",
		server.Endpoint.URL,
		httpClient,
	)
	run := func(toolID string, arguments map[string]any) {
		t.Helper()
		smoketest.RunRemote(
			t,
			ctx,
			definitionRegistry,
			Definition,
			session,
			toolID,
			arguments,
		)
	}

	run("list_teams", map[string]any{"limit": float64(1)})
	run("list_users", map[string]any{})
	run("list_projects", map[string]any{"limit": float64(1)})
	run("list_issues", map[string]any{"limit": float64(1)})
	run("list_issue_labels", map[string]any{"limit": float64(1)})
	run("list_issue_statuses", map[string]any{"team": config.teamID})
	run("list_cycles", map[string]any{"teamId": config.teamID})
}

type linearSmokeConfig struct {
	apiKey string
	teamID string
}

func parseLinearSmokeConfig(
	lookup smoketest.Lookup,
) (linearSmokeConfig, bool, error) {
	values, configured, err := smoketest.Values(
		"Linear",
		lookup,
		linearSmokeAPIKeyEnv,
		linearSmokeTeamIDEnv,
	)
	if !configured || err != nil {
		return linearSmokeConfig{}, configured, err
	}
	return linearSmokeConfig{
		apiKey: values[linearSmokeAPIKeyEnv],
		teamID: values[linearSmokeTeamIDEnv],
	}, true, nil
}
