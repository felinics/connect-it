package oauthsvc

import (
	"strings"
	"testing"
)

func TestExpandEndpoint(t *testing.T) {
	for _, test := range []struct {
		name     string
		endpoint string
		config   map[string]any
		want     string
		wantErr  string
	}{
		{
			name:     "replaces placeholder",
			endpoint: "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/authorize",
			config:   map[string]any{"tenant": "common"},
			want:     "https://login.microsoftonline.com/common/oauth2/v2.0/authorize",
		},
		{
			name:     "no placeholder is identity",
			endpoint: "https://github.com/login/oauth/authorize",
			config:   map[string]any{},
			want:     "https://github.com/login/oauth/authorize",
		},
		{
			name:     "escapes path and query values",
			endpoint: "https://login.example.com/{tenant}/token?audience={audience}",
			config: map[string]any{
				"tenant":   "tenant name",
				"audience": "https://api.example.com/a?b=c",
			},
			want: "https://login.example.com/tenant%20name/token?" +
				"audience=https%3A%2F%2Fapi.example.com%2Fa%3Fb%3Dc",
		},
		{
			name:     "missing value names the field",
			endpoint: "https://login.microsoftonline.com/{tenant}/oauth2/v2.0/token",
			config:   map[string]any{},
			wantErr:  "tenant",
		},
		{
			name:     "non-string value is rejected",
			endpoint: "https://x.example.com/{tenant}/y",
			config:   map[string]any{"tenant": 42},
			wantErr:  "缺少对应配置值",
		},
		{
			name:     "placeholder cannot change origin",
			endpoint: "https://{tenant}.example.com/oauth/token",
			config:   map[string]any{"tenant": "attacker"},
			wantErr:  "hostname",
		},
		{
			name:     "encoded path traversal is rejected",
			endpoint: "https://login.example.com/{tenant}/token",
			config:   map[string]any{"tenant": "safe/../escape"},
			wantErr:  "展开后非法",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := expandEndpoint(test.endpoint, test.config)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, test.wantErr)
				}
				return
			}
			if err != nil || got != test.want {
				t.Fatalf("got %q err=%v, want %q", got, err, test.want)
			}
		})
	}
}
