// Managed Tool 的运行时类型。Definition 通过 ManagedBackend.HandlerKey
// 以字符串引用 HandlerMap 中的 handler（保持 Definition 可序列化）。
package connector

import (
	"context"
	"encoding/json"
)

// ToolCallContext 是一次 Tool 调用装配完成后的全部输入（spec §9 运行时装配）。
// Arguments 为 MCP CallTool arguments 的反序列化结果：managed handler 直接按
// map 取参，remote backend 由 exec 重新序列化后透传。
type ToolCallContext struct {
	ConnectorType Type
	ToolID        string
	Arguments     map[string]any
	// Config 是合并默认值后的管理员配置（含 Secret 明文），来自 configsvc.Resolved。
	Config map[string]any
	// Credential 是 api_key / custom_credential 连接解密后的凭证字段；
	// OAuth 与 AuthNone 连接为 nil。
	Credential map[string]any
	// AccessToken 是 OAuth 连接经惰性刷新后的有效 access token；
	// api_key 类连接为其凭证值；其他为空。
	AccessToken string
}

// ToolResultData 是两种 backend 统一的执行结果：Text 是人类可读输出（映射
// MCP TextContent）；Structured 是结构化 JSON 输出（映射 structuredContent），
// 可为空；IsError 表示业务失败（区别于 Go error 的传输/装配失败）。
type ToolResultData struct {
	Text       string
	Structured json.RawMessage
	IsError    bool
}

// ManagedHandler 是 Managed Tool 的执行入口。
type ManagedHandler func(ctx context.Context, call ToolCallContext) (ToolResultData, error)

// HandlerMap 按 HandlerKey 索引一个 Connector 的全部 Managed handler。
type HandlerMap map[string]ManagedHandler
