package datadog

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
)

func datadogValidationInput(
	site string,
) connector.CredentialValidationInput {
	return connector.CredentialValidationInput{
		ConnectorType:   Definition.Type,
		AuthMethodKey:   datadogAuthMethod,
		AuthType:        connector.AuthCustomCredential,
		AuthorizationID: "datadog-validator-test",
		Fields: map[string]string{
			datadogAPIKeyField:         "validator-api-secret",
			datadogApplicationKeyField: "validator-app-secret",
			datadogSiteField:           site,
		},
	}
}

func TestDatadogCredentialValidatorUsesV2PairEndpointAndFallbackProfile(
	t *testing.T,
) {
	runtime := newDatadogTestRuntime(
		t,
		http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if request.Method != http.MethodGet ||
				request.URL.EscapedPath() != "/api/v2/validate_keys" {
				t.Errorf("request = %s %s", request.Method, request.URL.EscapedPath())
			}
			if request.Header.Get("DD-API-KEY") != "validator-api-secret" {
				t.Error("API key header did not match")
			}
			if request.Header.Get("DD-APPLICATION-KEY") !=
				"validator-app-secret" {
				t.Error("Application key header did not match")
			}
			if request.Header.Get("Referer") != "" ||
				request.Header.Get("Authorization") != "" {
				t.Error("unexpected Referer/Authorization")
			}
			_, _ = writer.Write([]byte(`{"status":"ok"}`))
		}),
		1024,
	)
	result, err := newCredentialValidators(runtime.source)[datadogAuthMethod](
		t.Context(),
		datadogValidationInput("eu"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile.AccountID != "" ||
		result.Profile.DisplayName != "Datadog EU1" ||
		result.ScopesKnown ||
		result.GrantedScopes != nil {
		t.Fatalf("validation result = %+v", result)
	}
	// Every reviewed site has an explicit label; anything else falls back
	// rather than surfacing the raw site key.
	if len(datadogSiteDisplayNames) != len(datadogSites) ||
		datadogDisplayName("evil.example") != "Datadog" {
		t.Fatal("Datadog site display names are not explicit")
	}
}

func TestDatadogCredentialValidatorRequiresExactFields(t *testing.T) {
	runtime := datadogRejectingRuntime(t, "invalid credential reached Provider")
	tests := []struct {
		name   string
		mutate func(*connector.CredentialValidationInput)
		code   connector.FailureCode
	}{
		{
			name: "missing API key",
			mutate: func(input *connector.CredentialValidationInput) {
				delete(input.Fields, datadogAPIKeyField)
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name: "missing Application key",
			mutate: func(input *connector.CredentialValidationInput) {
				delete(input.Fields, datadogApplicationKeyField)
			},
			code: connector.FailureAuthorizationFailed,
		},
		{
			name: "extra field",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields["base_url"] = "https://evil.example"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "arbitrary site",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields[datadogSiteField] = "evil.example"
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "wrong auth type",
			mutate: func(input *connector.CredentialValidationInput) {
				input.AuthType = connector.AuthAPIKey
			},
			code: connector.FailureConfigurationError,
		},
		{
			name: "unicode secret whitespace",
			mutate: func(input *connector.CredentialValidationInput) {
				input.Fields[datadogAPIKeyField] = "api\u00a0key"
			},
			code: connector.FailureAuthorizationFailed,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := datadogValidationInput("us1")
			test.mutate(&input)
			_, err := newCredentialValidators(runtime.source)[datadogAuthMethod](
				t.Context(),
				input,
			)
			assertDatadogCredentialError(t, err, test.code, 0)
		})
	}
	if len(runtime.requests()) != 0 {
		t.Fatalf("invalid credentials made %d requests", len(runtime.requests()))
	}
}

func TestDatadogCredentialValidatorFailureMappingAndPropagationHint(
	t *testing.T,
) {
	tests := []struct {
		status int
		code   connector.FailureCode
		hint   bool
	}{
		{401, connector.FailureAuthorizationFailed, true},
		{403, connector.FailureAuthorizationFailed, true},
		{429, connector.FailureRateLimited, false},
		{503, connector.FailureUpstreamUnavailable, false},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			runtime := datadogStatusRuntime(
				t,
				test.status,
				"",
				"provider-secret validator-api-secret validator-app-secret",
			)
			_, err := newCredentialValidators(runtime.source)[datadogAuthMethod](
				t.Context(),
				datadogValidationInput("us1"),
			)
			assertDatadogCredentialError(t, err, test.code, test.status)
			if test.hint != strings.Contains(err.Error(), "propagate") {
				t.Fatalf("propagation hint mismatch: %q", err.Error())
			}
			if strings.Contains(err.Error(), "provider-secret") ||
				strings.Contains(err.Error(), "validator-api-secret") ||
				strings.Contains(err.Error(), "validator-app-secret") ||
				errors.Unwrap(err) != nil {
				t.Fatal("validator error leaked body, credential, or cause")
			}
		})
	}
}

func TestDatadogCredentialValidatorRejectsMalformedAndOversizeResponses(
	t *testing.T,
) {
	tests := []struct {
		name     string
		body     string
		maxBytes int64
		code     connector.FailureCode
	}{
		{
			name:     "malformed",
			body:     `{"status":`,
			maxBytes: 1024,
			code:     connector.FailureInvalidResponse,
		},
		{
			name:     "wrong success status",
			body:     `{"status":"invalid"}`,
			maxBytes: 1024,
			code:     connector.FailureInvalidResponse,
		},
		{
			name:     "wrong status type",
			body:     `{"status":true}`,
			maxBytes: 1024,
			code:     connector.FailureInvalidResponse,
		},
		{
			name:     "oversize",
			body:     `{"status":"` + strings.Repeat("x", 128) + `"}`,
			maxBytes: 32,
			code:     connector.FailureResponseTooLarge,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := datadogBodyRuntime(t, test.body, test.maxBytes)
			_, err := newCredentialValidators(runtime.source)[datadogAuthMethod](
				t.Context(),
				datadogValidationInput("us1"),
			)
			assertDatadogCredentialError(t, err, test.code, 0)
		})
	}
}

func TestDatadogToolCredentialShapeIsExact(t *testing.T) {
	runtime := datadogRejectingRuntime(
		t,
		"invalid tool credential reached Provider",
	)
	handlers := newHandlers(runtime.source)
	for _, mutate := range []func(*connector.ToolCallContext){
		func(call *connector.ToolCallContext) {
			call.Credential[datadogSiteField] = "evil.example"
		},
		func(call *connector.ToolCallContext) {
			call.Credential["base_url"] = "https://evil.example"
		},
		func(call *connector.ToolCallContext) {
			call.Credential[datadogApplicationKeyField] = 42
		},
	} {
		call := datadogCall(
			"get_monitor",
			map[string]any{"monitor_id": float64(42)},
		)
		mutate(&call)
		result, err := handlers[call.ToolID](t.Context(), call)
		if err != nil || result.Failure == nil ||
			result.Failure.Code != connector.FailureConfigurationError {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	}
	if len(runtime.requests()) != 0 {
		t.Fatalf("invalid tool credentials made %d requests", len(runtime.requests()))
	}
}

func assertDatadogCredentialError(
	t *testing.T,
	err error,
	code connector.FailureCode,
	status int,
) {
	t.Helper()
	var validationErr *connector.CredentialValidationError
	if !errors.As(err, &validationErr) ||
		validationErr.Code != code ||
		(status != 0 && validationErr.UpstreamStatus != status) {
		t.Fatalf("validation error = %#v (%v)", validationErr, err)
	}
}
