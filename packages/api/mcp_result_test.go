package api

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/core/connector"
)

func TestToolResultToMCPProjectsOnlySafeFailureFields(t *testing.T) {
	tests := []struct {
		name    string
		failure connector.ToolFailure
		want    string
	}{
		{
			name: "retry hint",
			failure: connector.ToolFailure{
				Code:              connector.FailureRateLimited,
				Message:           "provider rate limit exceeded",
				UpstreamStatus:    429,
				RetryAfterSeconds: 172800,
			},
			want: `{"error":"rate_limited","message":"provider rate limit exceeded","retry_after_seconds":172800}`,
		},
		{
			name: "internal credential signal",
			failure: connector.ToolFailure{
				Code:              connector.FailureAuthorizationFailed,
				Message:           "provider credential is invalid",
				UpstreamStatus:    401,
				CredentialInvalid: true,
			},
			want: `{"error":"authorization_failed","message":"provider credential is invalid"}`,
		},
		{
			name: "unsafe struct",
			failure: connector.ToolFailure{
				Code:              connector.FailureProviderError,
				Message:           "provider-secret-body\n",
				UpstreamStatus:    999,
				RetryAfterSeconds: -1,
			},
			want: `{"error":"internal_error","message":"internal error"}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := toolResultToMCP(connector.ToolResultData{
				Failure: &tc.failure,
			})
			if !result.IsError || len(result.Content) != 1 {
				t.Fatalf("result = %+v", result)
			}
			content, ok := result.Content[0].(*mcp.TextContent)
			if !ok || content.Text != tc.want {
				t.Fatalf("content = %+v, want %s", result.Content[0], tc.want)
			}
		})
	}
}
