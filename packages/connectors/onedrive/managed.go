package onedrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// apiBaseURL 指向 Microsoft Graph 根地址；测试用 httptest.Server 覆盖后还原。
var apiBaseURL = "https://graph.microsoft.com"

const (
	maxAPIResponseBytes int64 = 4 << 20
	maxUploadBytes            = 4 << 20
)

var apiHTTPClient = &http.Client{Timeout: 30 * time.Second}

// listDriveItems 列出根目录（GET /v1.0/me/drive/root/children）
// 或指定文件夹（GET /v1.0/me/drive/root:/{path}:/children）。
func listDriveItems(ctx context.Context, call connector.ManagedCall) (*mcp.CallToolResult, error) {
	args, err := arguments(call.Arguments)
	if err != nil {
		return toolError("arguments 必须是 JSON object"), nil
	}
	u := apiBaseURL + "/v1.0/me/drive/root/children"
	if path, _ := args["path"].(string); path != "" {
		u = apiBaseURL + "/v1.0/me/drive/root:/" + escapePath(path) + ":/children"
	}
	return callGraph(ctx, http.MethodGet, u, nil, "", call.AccessToken)
}

// uploadFile 简单上传文本文件（PUT /v1.0/me/drive/root:/{path}:/content，<4MB）。
func uploadFile(ctx context.Context, call connector.ManagedCall) (*mcp.CallToolResult, error) {
	args, err := arguments(call.Arguments)
	if err != nil {
		return toolError("arguments 必须是 JSON object"), nil
	}
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if path == "" || content == "" {
		return toolError("path 与 content 均为必填参数"), nil
	}
	if len(content) > maxUploadBytes {
		return toolError(fmt.Sprintf("content 超过 %d bytes", maxUploadBytes)), nil
	}
	u := apiBaseURL + "/v1.0/me/drive/root:/" + escapePath(path) + ":/content"
	return callGraph(ctx, http.MethodPut, u,
		strings.NewReader(content), "application/octet-stream", call.AccessToken)
}

// escapePath 对路径逐段 PathEscape，保留段间的 /。
func escapePath(p string) string {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// callGraph 发送请求并把结果转换为原生 MCP result：
// 传输层失败返回 error；HTTP >= 400 转成 IsError 结果（error 为 nil）。
func callGraph(ctx context.Context, method, u string, body io.Reader, contentType, token string) (*mcp.CallToolResult, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := apiHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maxAPIResponseBytes {
		return nil, fmt.Errorf("microsoft graph 响应超过 %d bytes", maxAPIResponseBytes)
	}
	if resp.StatusCode >= 400 {
		return toolError(fmt.Sprintf("microsoft graph 返回 %d", resp.StatusCode)), nil
	}
	var structured map[string]any
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}
	if json.Unmarshal(data, &structured) == nil && structured != nil {
		result.StructuredContent = structured
	}
	return result, nil
}

func arguments(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return map[string]any{}, nil
	}
	var args map[string]any
	if err := json.Unmarshal(raw, &args); err != nil || args == nil {
		return nil, fmt.Errorf("arguments 必须是 JSON object")
	}
	return args, nil
}

func toolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: message}},
		StructuredContent: map[string]any{"error": message},
		IsError:           true,
	}
}
