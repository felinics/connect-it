package googleads

import (
	"reflect"
	"testing"
)

func TestAllowInsecureHTTPV1ConfigUpgrade(t *testing.T) {
	if Definition.ConfigSchemaVersion != 2 ||
		len(Definition.ConfigUpgraders) != 1 {
		t.Fatalf(
			"Google Ads config upgrade chain = version %d, upgraders %+v",
			Definition.ConfigSchemaVersion,
			Definition.ConfigUpgraders,
		)
	}
	upgrader := Definition.ConfigUpgraders[0]
	if upgrader.FromVersion != 1 || upgrader.Upgrade == nil {
		t.Fatalf("Google Ads v1 upgrader = %+v", upgrader)
	}

	tests := []struct {
		name string
		seed map[string]any
		want map[string]any
	}{
		{
			name: "missing network flag defaults to string false",
			seed: map[string]any{
				"mcp_url": "https://mcp.example.test/mcp",
			},
			want: map[string]any{
				"mcp_url":             "https://mcp.example.test/mcp",
				"allow_insecure_http": "false",
			},
		},
		{
			name: "explicit true is preserved",
			seed: map[string]any{
				"mcp_url":             "http://127.0.0.1:8080/mcp",
				"allow_insecure_http": "true",
			},
			want: map[string]any{
				"mcp_url":             "http://127.0.0.1:8080/mcp",
				"allow_insecure_http": "true",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// ConfigUpgrader receives invocation-owned mutable maps. Clone the
			// table seed so an in-place upgrader cannot pollute another case.
			seedBefore := cloneConfigMap(test.seed)
			public := cloneConfigMap(test.seed)
			secret := map[string]any{"client_secret": "redacted"}

			gotPublic, gotSecret, err := upgrader.Upgrade(public, secret)
			if err != nil {
				t.Fatalf("Upgrade() error = %v", err)
			}
			if !reflect.DeepEqual(gotPublic, test.want) {
				t.Fatalf("upgraded public config = %#v, want %#v", gotPublic, test.want)
			}
			if !reflect.DeepEqual(
				gotSecret,
				map[string]any{"client_secret": "redacted"},
			) {
				t.Fatalf("upgraded secret config = %#v", gotSecret)
			}
			if !reflect.DeepEqual(test.seed, seedBefore) {
				t.Fatalf("test seed was unexpectedly mutated: %#v", test.seed)
			}
		})
	}
}

func cloneConfigMap(input map[string]any) map[string]any {
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}
