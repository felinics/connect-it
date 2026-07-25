package googleads

import (
	"context"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"testing"
	"time"

	"github.com/memohai/connect-it/packages/connectors/internal/smoketest"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

const (
	googleAdsSmokeAccessTokenEnv = "CONNECT_IT_GOOGLE_ADS_SMOKE_ACCESS_TOKEN"
	googleAdsSmokeDeveloperEnv   = "CONNECT_IT_GOOGLE_ADS_SMOKE_DEVELOPER_TOKEN"
	googleAdsSmokeCustomerEnv    = "CONNECT_IT_GOOGLE_ADS_SMOKE_CUSTOMER_ID"
	googleAdsSmokeMCPURLEnv      = "CONNECT_IT_GOOGLE_ADS_SMOKE_MCP_URL"
	googleAdsSmokeAllowHTTPEnv   = "CONNECT_IT_GOOGLE_ADS_SMOKE_ALLOW_INSECURE_HTTP"

	googleAdsSmokeConnection = "google-ads-real-smoke"
)

var googleAdsSmokeCustomerPattern = regexp.MustCompile(`^[0-9]{10}$`)

// TestGoogleAdsRealSmoke is an opt-in, read-only real-account harness for a
// Google Ads test account and a reviewed self-hosted official MCP deployment.
// It validates both the OAuth access token and the developer token against the
// Google Ads API, then calls both published Tools through the official MCP Go
// SDK over a providerkit-guarded dynamic client.
//
// The developer token is used only by the fixed Google Ads validator and is
// never sent to the MCP endpoint. The harness never logs either credential,
// customer IDs, GAQL results, MCP payloads, endpoint query data, or Provider
// error text.
func TestGoogleAdsRealSmoke(t *testing.T) {
	config := smoketest.Config(t, "Google Ads", parseGoogleAdsSmokeConfig)

	factory, err := providerkit.NewFactoryFromEnv()
	if err != nil {
		t.Fatal("construct Google Ads provider client factory")
	}
	validators, err := NewCredentialValidators(factory)
	if err != nil {
		t.Fatal("construct Google Ads credential validators")
	}
	validator, exists := validators["oauth"]
	if !exists {
		t.Fatal("Google Ads OAuth credential validator is not registered")
	}
	input := func(connectionID string, accessToken string) connector.CredentialValidationInput {
		return connector.CredentialValidationInput{
			ConnectorType: Definition.Type,
			AuthMethodKey: "oauth",
			AuthType:      connector.AuthOAuth2,
			ConnectionID:  connectionID,
			Config: map[string]any{
				"developer_token": config.developerToken,
			},
			AccessToken: accessToken,
			TokenType:   "Bearer",
		}
	}
	validation, err := validator(
		t.Context(),
		input(googleAdsSmokeConnection, config.accessToken),
	)
	if err != nil {
		t.Fatal("validate Google Ads credentials")
	}
	if validation.Profile.AccountID != "" ||
		validation.Profile.DisplayName == "" ||
		validation.ScopesKnown ||
		len(validation.GrantedScopes) != 0 {
		t.Fatal("Google Ads validator returned an invalid profile or scope snapshot")
	}
	smoketest.RequireRejectedCredential(t, validator, input(
		googleAdsSmokeConnection+"-invalid-auth",
		"connect-it-deliberately-invalid-google-ads-token",
	))

	if len(Definition.RemoteMCPServers) != 1 {
		t.Fatal("Google Ads real smoke expects exactly one reviewed MCP server")
	}
	definitionRegistry := smoketest.Registry(t, Definition)
	policyClient, err := factory.NewDynamicClient(
		providerkit.DynamicPolicyInput{
			Provider:          string(Definition.Type),
			BaseURL:           config.mcpURL,
			AllowInsecureHTTP: config.allowInsecureHTTP,
			RedirectMode:      providerkit.RedirectFollowPolicy,
			RequestTimeout:    Definition.RemoteMCPServers[0].RequestTimeout,
			MaxResponseBytes:  providerkit.DefaultMaxResponseBytes,
			Retry:             providerkit.RetryPolicy{Disabled: true},
		},
	)
	if err != nil {
		t.Fatal("construct Google Ads guarded MCP client")
	}
	t.Cleanup(policyClient.CloseIdleConnections)
	authorizer, err := providerkit.Bearer(config.accessToken)
	if err != nil {
		t.Fatal("construct Google Ads smoke authorization")
	}
	httpClient, err := policyClient.HTTPClient(
		authorizer,
		providerkit.RequestLabels{
			ConnectorType:   string(Definition.Type),
			Operation:       "real_smoke",
			AuthorizationID: googleAdsSmokeConnection,
		},
	)
	if err != nil {
		t.Fatal("construct Google Ads guarded MCP transport")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	t.Cleanup(cancel)
	session := smoketest.Connect(
		t,
		ctx,
		"connect-it-google-ads-real-smoke",
		config.mcpURL,
		httpClient,
	)

	smoketest.RunRemote(
		t,
		ctx,
		definitionRegistry,
		Definition,
		session,
		"list_accessible_customers",
		map[string]any{},
	)
	smoketest.RunRemote(
		t,
		ctx,
		definitionRegistry,
		Definition,
		session,
		"search",
		map[string]any{
			"customer_id": config.customerID,
			"query": "SELECT campaign.id, campaign.name " +
				"FROM campaign ORDER BY campaign.id LIMIT 1",
		},
	)
}

type googleAdsSmokeConfiguration struct {
	accessToken       string
	developerToken    string
	customerID        string
	mcpURL            string
	allowInsecureHTTP string
}

func parseGoogleAdsSmokeConfig(
	lookup smoketest.Lookup,
) (googleAdsSmokeConfiguration, bool, error) {
	values, configured, err := smoketest.Values(
		"Google Ads",
		lookup,
		googleAdsSmokeAccessTokenEnv,
		googleAdsSmokeDeveloperEnv,
		googleAdsSmokeCustomerEnv,
		googleAdsSmokeMCPURLEnv,
		googleAdsSmokeAllowHTTPEnv,
	)
	if !configured || err != nil {
		return googleAdsSmokeConfiguration{}, configured, err
	}
	if err := smoketest.Exactly(
		values,
		googleAdsSmokeAllowHTTPEnv,
		"true",
		"false",
	); err != nil {
		return googleAdsSmokeConfiguration{}, true, err
	}
	customerID := values[googleAdsSmokeCustomerEnv]
	if !googleAdsSmokeCustomerPattern.MatchString(customerID) {
		return googleAdsSmokeConfiguration{}, true, fmt.Errorf(
			"%s must be exactly ten digits",
			googleAdsSmokeCustomerEnv,
		)
	}
	allowHTTP := values[googleAdsSmokeAllowHTTPEnv]
	parsed, parseErr := providerkit.ParseAndValidateURL(
		values[googleAdsSmokeMCPURLEnv],
	)
	if parseErr != nil ||
		parsed.RawQuery != "" ||
		parsed.ForceQuery ||
		parsed.Fragment != "" ||
		parsed.RawFragment != "" {
		return googleAdsSmokeConfiguration{}, true, fmt.Errorf(
			"%s must be a canonical MCP endpoint without query or fragment",
			googleAdsSmokeMCPURLEnv,
		)
	}
	if parsed.Scheme != "https" &&
		(parsed.Scheme != "http" || allowHTTP != "true") {
		return googleAdsSmokeConfiguration{}, true, fmt.Errorf(
			"%s requires HTTPS unless the connector HTTP opt-in is exactly true",
			googleAdsSmokeMCPURLEnv,
		)
	}
	return googleAdsSmokeConfiguration{
		accessToken:       values[googleAdsSmokeAccessTokenEnv],
		developerToken:    values[googleAdsSmokeDeveloperEnv],
		customerID:        customerID,
		mcpURL:            parsed.String(),
		allowInsecureHTTP: allowHTTP,
	}, true, nil
}

// The shared harness covers the env gate itself; this covers the endpoint
// rules, because the smoke MCP URL is the one operator-supplied value that
// decides where a credential is sent.
func TestGoogleAdsSmokeConfigBoundsTheMCPEndpoint(t *testing.T) {
	t.Parallel()
	complete := map[string]string{
		googleAdsSmokeAccessTokenEnv: "redacted-access-token",
		googleAdsSmokeDeveloperEnv:   "redacted-developer-token",
		googleAdsSmokeCustomerEnv:    "1234567890",
		googleAdsSmokeMCPURLEnv:      "https://ads-mcp.example.test:443/mcp",
		googleAdsSmokeAllowHTTPEnv:   "false",
	}
	rejected := []struct {
		name     string
		override map[string]string
	}{
		{"non-exact network flag", map[string]string{
			googleAdsSmokeAllowHTTPEnv: "TRUE",
		}},
		{"http without the connector opt-in", map[string]string{
			googleAdsSmokeMCPURLEnv: "http://ads-mcp.example.test/mcp",
		}},
		{"endpoint carrying a query", map[string]string{
			googleAdsSmokeMCPURLEnv: "https://ads-mcp.example.test/mcp?token=forbidden",
		}},
		{"customer ID that is not ten digits", map[string]string{
			googleAdsSmokeCustomerEnv: "12345",
		}},
	}
	for _, test := range rejected {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values := maps.Clone(complete)
			maps.Copy(values, test.override)
			if _, _, err := parseGoogleAdsSmokeConfig(
				smoketest.MapLookup(values),
			); err == nil {
				t.Fatal("Google Ads smoke config accepted an unsafe endpoint")
			}
		})
	}

	t.Run("http with the exact connector opt-in parses", func(t *testing.T) {
		t.Parallel()
		values := maps.Clone(complete)
		values[googleAdsSmokeMCPURLEnv] = "http://ads-mcp.example.test/mcp"
		values[googleAdsSmokeAllowHTTPEnv] = "true"
		if _, _, err := parseGoogleAdsSmokeConfig(
			smoketest.MapLookup(values),
		); err != nil {
			t.Fatalf("reviewed HTTP opt-in = %v", err)
		}
	})

	t.Run("the endpoint is normalized", func(t *testing.T) {
		t.Parallel()
		config, configured, err := parseGoogleAdsSmokeConfig(
			smoketest.MapLookup(complete),
		)
		if !configured || err != nil {
			t.Fatalf("complete Google Ads smoke config err = %v", err)
		}
		parsed, err := url.Parse(config.mcpURL)
		if err != nil ||
			parsed.Scheme != "https" ||
			parsed.Hostname() != "ads-mcp.example.test" ||
			parsed.Path != "/mcp" {
			t.Fatal("Google Ads smoke MCP URL was not normalized safely")
		}
	})
}
