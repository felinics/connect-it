package exec

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/felinics/connect-it/packages/service/configsvc"
	"github.com/felinics/connect-it/packages/service/tokens"
)

type statusError int

func (e statusError) Error() string           { return "private upstream body" }
func (e statusError) UpstreamStatusCode() int { return int(e) }

func TestDescribeError(t *testing.T) {
	for _, tc := range []struct {
		name       string
		err        error
		code, kind string
		status     int
	}{
		{"internal", errors.New("private internal details"), "execution_failed", "internal", 0},
		{"timeout", context.DeadlineExceeded, "temporarily_unavailable", "timeout", 0},
		{"cancel", context.Canceled, "temporarily_unavailable", "canceled", 0},
		{"dns", &net.DNSError{Err: "private host"}, "temporarily_unavailable", "transport", 0},
		{"network_timeout", &net.DNSError{IsTimeout: true}, "temporarily_unavailable", "timeout", 0},
		{"reauth", tokens.ErrReauthRequired, "reauth_required", "auth", 0},
		{"disabled", configsvc.ErrConnectorDisabled, "tool_unavailable", "tool_unavailable", 0},
		{"missing", ErrConnectionNotFound, "tool_unavailable", "tool_unavailable", 0},
		{"redirect", statusError(307), "execution_failed", "upstream_redirect", 307},
		{"bad_request", statusError(400), "execution_failed", "upstream_4xx", 400},
		{"unauthorized", statusError(401), "upstream_auth_error", "auth", 401},
		{"forbidden", statusError(403), "upstream_auth_error", "auth", 403},
		{"rate_limit", statusError(429), "temporarily_unavailable", "rate_limited", 429},
		{"unavailable", statusError(503), "temporarily_unavailable", "upstream_5xx", 503},
		{"invalid_status", statusError(999), "execution_failed", "transport", 0},
		{"rpc_params", &jsonrpc.Error{Code: -32602, Message: "private params"}, "execution_failed", "invalid_args", 0},
		{"rpc_internal", &jsonrpc.Error{Code: -32603, Message: "private data"}, "execution_failed", "upstream_rpc", 0},
		{"cancel_with_status", errors.Join(context.Canceled, statusError(500)), "temporarily_unavailable", "canceled", 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := fmt.Errorf("outer: %w", tc.err)
			f := DescribeError(err)
			if f.Code != tc.code || f.Kind != tc.kind || f.UpstreamStatus != tc.status {
				t.Fatalf("failure=%+v", f)
			}
			kind, status := classifyCallError(err)
			if kind != f.Kind || (tc.status != 0 && (status == nil || int(*status) != tc.status)) || (tc.status == 0 && status != nil) {
				t.Fatalf("audit classification=%s %v", kind, status)
			}
		})
	}
}
