// Managed Tool 的运行时类型。
package connector

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ManagedCall struct {
	Arguments json.RawMessage
	// Config 是合并默认值后的管理员配置（含 Secret 明文），来自 configsvc.Resolved。
	Config map[string]any
	// Credential 是 api_key / custom_credential 连接解密后的凭证字段；
	// OAuth 与 AuthNone 连接为 nil。
	Credential map[string]any
	// AccessToken 是 OAuth 连接经惰性刷新后的有效 access token；
	// api_key 类连接为其凭证值；其他为空。
	AccessToken string
}

// ManagedHandler 是 Managed Tool 的执行入口。
type ManagedHandler func(ctx context.Context, call ManagedCall) (*mcp.CallToolResult, error)
