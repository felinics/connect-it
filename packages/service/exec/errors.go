package exec

import (
	"context"
	"errors"
	"net"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/felinics/connect-it/packages/service/configsvc"
	"github.com/felinics/connect-it/packages/service/tokens"
)

// Failure contains only safe metadata, never the underlying error text or data.
type Failure struct {
	Code           string `json:"error"`
	Message        string `json:"message"`
	Kind           string `json:"kind"`
	Stage          string `json:"stage,omitempty"`
	UpstreamStatus int    `json:"upstream_status,omitempty"`
	RPCCode        *int64 `json:"rpc_code,omitempty"`
}

func DescribeError(err error) Failure {
	f := Failure{Code: "execution_failed", Message: "tool execution failed", Kind: errorKindInternal}
	var stage interface{ UpstreamStage() string }
	if errors.As(err, &stage) {
		f.Stage = stage.UpstreamStage()
	}
	var upstream interface{ UpstreamStatusCode() int }
	if errors.As(err, &upstream) {
		if status := upstream.UpstreamStatusCode(); status >= 100 && status <= 599 {
			f.UpstreamStatus = status
		}
	}
	var transport interface{ UpstreamTransportFailure() bool }
	transportFailed := errors.As(err, &transport) && transport.UpstreamTransportFailure()
	var rpc *jsonrpc.Error
	if !transportFailed && f.UpstreamStatus == 0 && errors.As(err, &rpc) {
		code := rpc.Code
		f.RPCCode = &code
	}
	var network net.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		f.Code, f.Message, f.Kind = "temporarily_unavailable", "tool call timed out", errorKindTimeout
	case errors.Is(err, context.Canceled):
		f.Code, f.Message, f.Kind = "temporarily_unavailable", "tool call was canceled", errorKindCanceled
	case errors.Is(err, tokens.ErrReauthRequired):
		f.Code, f.Message, f.Kind = "reauth_required", "connection needs reauthorization", errorKindAuth
	case errors.Is(err, ErrToolUnavailable), errors.Is(err, ErrConnectionNotFound),
		errors.Is(err, ErrConnectionInactive), errors.Is(err, configsvc.ErrConnectorDisabled), errors.Is(err, tokens.ErrNotFound):
		f.Code, f.Message, f.Kind = "tool_unavailable", "tool is unavailable", errorKindToolUnavailable
	case f.UpstreamStatus == http.StatusUnauthorized || f.UpstreamStatus == http.StatusForbidden:
		f.Code, f.Message, f.Kind = "upstream_auth_error", "upstream denied access to the tool", errorKindAuth
	case f.UpstreamStatus == http.StatusTooManyRequests:
		f.Code, f.Message, f.Kind = "temporarily_unavailable", "upstream rate limit exceeded", "rate_limited"
	case f.UpstreamStatus >= 500:
		f.Code, f.Message, f.Kind = "temporarily_unavailable", "upstream is temporarily unavailable", errorKindUpstream5xx
	case f.UpstreamStatus >= 400:
		f.Message, f.Kind = "upstream rejected the request", errorKindUpstream4xx
	case f.UpstreamStatus >= 300:
		f.Message, f.Kind = "upstream redirect was refused", "upstream_redirect"
	case rpc != nil:
		f.Message, f.Kind = "upstream returned a protocol error", "upstream_rpc"
		if rpc.Code == jsonrpc.CodeInvalidParams {
			f.Message, f.Kind = "upstream rejected the tool arguments", errorKindInvalidArgs
		}
	case errors.As(err, &network) || transportFailed:
		f.Code, f.Message, f.Kind = "temporarily_unavailable", "upstream transport failed", errorKindTransport
		if network != nil && network.Timeout() {
			f.Message, f.Kind = "tool call timed out", errorKindTimeout
		}
	case stage != nil:
		f.Message, f.Kind = "upstream MCP exchange failed", "upstream_protocol"
	case upstream != nil:
		f.Kind = errorKindTransport
	}
	return f
}

func classifyCallError(err error) (string, *int32) {
	f := DescribeError(err)
	if f.UpstreamStatus == 0 {
		return f.Kind, nil
	}
	status := int32(f.UpstreamStatus)
	return f.Kind, &status
}
