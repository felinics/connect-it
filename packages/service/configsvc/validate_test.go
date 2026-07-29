package configsvc_test

import (
	"errors"
	"testing"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
)

// newValidateService wires only the registry; store and keyring are nil
// because Validate does not use them.
func newValidateService(t *testing.T) *configsvc.Service {
	t.Helper()
	r := registry.New()
	r.MustRegister(connector.Definition{
		Type:                "example_app",
		Name:                "Example",
		ConfigSchemaVersion: 1,
		ConfigFields: []connector.ConfigField{
			{Key: "client_id", Label: "Client ID", InputType: connector.InputText, Required: true},
			{Key: "client_secret", Label: "Client Secret", InputType: connector.InputText, Required: true, Secret: true},
			{Key: "region", Label: "Region", InputType: connector.InputSelect,
				Validation: connector.FieldValidation{Options: []string{"us", "eu"}}},
			{Key: "project_id", Label: "Project ID", InputType: connector.InputText,
				Validation: connector.FieldValidation{Pattern: `^[0-9]+$`}},
			{Key: "api_key", Label: "API Key", InputType: connector.InputText, Secret: true},
		},
		Implementation: connector.RemoteMCP{Endpoint: "https://mcp.example.com"},
	})
	return configsvc.New(nil, r, nil)
}

func TestValidateOK(t *testing.T) {
	s := newValidateService(t)
	err := s.Validate("example_app",
		map[string]any{"client_id": "abc", "region": "eu", "project_id": "123"},
		map[string]string{"client_secret": "shh", "api_key": "k"})
	if err != nil {
		t.Fatalf("a valid config must not error: %v", err)
	}
}

func TestValidateUnknownConnector(t *testing.T) {
	s := newValidateService(t)
	err := s.Validate("nope", nil, nil)
	if !errors.Is(err, configsvc.ErrUnknownConnector) {
		t.Fatalf("want ErrUnknownConnector, got %v", err)
	}
}

func TestValidateFailures(t *testing.T) {
	cases := []struct {
		name      string
		public    map[string]any
		secrets   map[string]string
		wantField string
	}{
		{"unknown public field", map[string]any{"client_id": "a", "bogus": "x"},
			map[string]string{"client_secret": "s"}, "bogus"},
		{"secret field placed in public", map[string]any{"client_id": "a", "client_secret": "leak"},
			map[string]string{"client_secret": "s"}, "client_secret"},
		{"unknown secret field", map[string]any{"client_id": "a"},
			map[string]string{"client_secret": "s", "bogus": "x"}, "bogus"},
		{"public field value is not a string", map[string]any{"client_id": 42},
			map[string]string{"client_secret": "s"}, "client_id"},
		{"missing required public field", map[string]any{},
			map[string]string{"client_secret": "s"}, "client_id"},
		{"missing required secret field", map[string]any{"client_id": "a"},
			map[string]string{}, "client_secret"},
		{"required secret set to empty string counts as missing", map[string]any{"client_id": "a"},
			map[string]string{"client_secret": ""}, "client_secret"},
		{"pattern mismatch", map[string]any{"client_id": "a", "project_id": "abc"},
			map[string]string{"client_secret": "s"}, "project_id"},
		{"value not in options", map[string]any{"client_id": "a", "region": "cn"},
			map[string]string{"client_secret": "s"}, "region"},
	}
	s := newValidateService(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := s.Validate("example_app", tc.public, tc.secrets)
			var ve *configsvc.ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("want ValidationError, got %v", err)
			}
			if ve.Field != tc.wantField {
				t.Fatalf("error field %q, want %q (reason=%s)", ve.Field, tc.wantField, ve.Reason)
			}
		})
	}
}

func TestValidateOptionalSecretMayBeEmpty(t *testing.T) {
	// An empty string on an optional secret deletes it and must pass validation.
	s := newValidateService(t)
	err := s.Validate("example_app",
		map[string]any{"client_id": "a"},
		map[string]string{"client_secret": "s", "api_key": ""})
	if err != nil {
		t.Fatalf("an empty optional secret should pass: %v", err)
	}
}
