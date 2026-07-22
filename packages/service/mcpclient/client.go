// Package mcpclient 用官方 go-sdk 以 Streamable HTTP 调用上游 MCP server。
// 每次调用新建并关闭 session（spec §11：正确性优先，会话缓存留作后续优化）。
package mcpclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/memohai/connect-it/packages/core/connector"
)

// Client 是无状态适配器，把包级函数暴露为方法，
// 便于作为 exec.MCPCaller 与 api.MCPToolLister 注入。
type Client struct{}

func (Client) CallTool(ctx context.Context, endpoint, bearerToken string, timeout time.Duration, remoteToolName string, args json.RawMessage) (connector.ToolResultData, error) {
	return CallTool(ctx, endpoint, bearerToken, timeout, remoteToolName, args)
}

func (Client) ListTools(ctx context.Context, endpoint, bearerToken string, timeout time.Duration) ([]string, error) {
	return ListTools(ctx, endpoint, bearerToken, timeout)
}

// CallTool 对 endpoint 完成一次 MCP 握手、调用 remoteToolName、关闭 session。
// 上游全部 TextContent 以换行拼进 Text，StructuredContent 序列化进 Structured。
func CallTool(ctx context.Context, endpoint, bearerToken string, timeout time.Duration, remoteToolName string, args json.RawMessage) (connector.ToolResultData, error) {
	ctx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	session, err := connect(ctx, endpoint, bearerToken)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	defer session.Close()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: remoteToolName, Arguments: args})
	if err != nil {
		return connector.ToolResultData{}, fmt.Errorf("mcpclient: 调用 tool %q 失败: %w", remoteToolName, err)
	}
	var texts []string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			texts = append(texts, tc.Text)
		}
	}
	out := connector.ToolResultData{Text: strings.Join(texts, "\n"), IsError: res.IsError}
	if res.StructuredContent != nil {
		b, err := json.Marshal(res.StructuredContent)
		if err != nil {
			return connector.ToolResultData{}, fmt.Errorf("mcpclient: 序列化 structuredContent 失败: %w", err)
		}
		out.Structured = b
	}
	return out, nil
}

// ListTools 对 endpoint 完成一次 MCP 握手并列出全部工具名（Tools 迭代器自动翻页）。
func ListTools(ctx context.Context, endpoint, bearerToken string, timeout time.Duration) ([]string, error) {
	ctx, cancel := withTimeout(ctx, timeout)
	defer cancel()

	session, err := connect(ctx, endpoint, bearerToken)
	if err != nil {
		return nil, err
	}
	defer session.Close()

	var names []string
	for tool, err := range session.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("mcpclient: tools/list 失败: %w", err)
		}
		names = append(names, tool.Name)
	}
	return names, nil
}

// CheckEndpoint 校验 self_hosted endpoint 的 URL 形态：
// 必须是 https；allowInsecure 为 true 时才放行 http（spec §13 规则 2）。
func CheckEndpoint(endpoint string, allowInsecure bool) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return fmt.Errorf("mcpclient: endpoint %q 不是合法 URL", endpoint)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if allowInsecure {
			return nil
		}
		return fmt.Errorf("mcpclient: endpoint %q 使用 http，需显式开启 allow_insecure_http", endpoint)
	default:
		return fmt.Errorf("mcpclient: endpoint %q 的 scheme 必须是 https", endpoint)
	}
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, timeout)
}

func connect(ctx context.Context, endpoint, bearerToken string) (*mcp.ClientSession, error) {
	client := mcp.NewClient(&mcp.Implementation{Name: "connect-it", Version: "0.1.0"}, nil)
	transport := &mcp.StreamableClientTransport{
		Endpoint:   endpoint,
		HTTPClient: &http.Client{Transport: bearerRoundTripper{token: bearerToken, base: http.DefaultTransport}},
	}
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, fmt.Errorf("mcpclient: 连接 %s 失败: %w", endpoint, err)
	}
	return session, nil
}

// bearerRoundTripper 给每个请求附加 Authorization: Bearer 头
// （go-sdk 的 transport 无 header 选项，只能经 http.Client 注入）。
type bearerRoundTripper struct {
	token string
	base  http.RoundTripper
}

func (rt bearerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if rt.token != "" {
		req = req.Clone(req.Context())
		req.Header.Set("Authorization", "Bearer "+rt.token)
	}
	return rt.base.RoundTrip(req)
}
