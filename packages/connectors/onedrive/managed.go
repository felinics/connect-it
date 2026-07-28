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

	"github.com/memohai/connect-it/packages/connectors/internal/managedapi"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var apiBaseURL = "https://graph.microsoft.com"

const (
	maxAPIResponseBytes int64 = 4 << 20
	maxUploadBytes            = 4 << 20
)

var apiHTTPClient = managedapi.NewHTTPClient(30 * time.Second)

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

func escapePath(p string) string {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

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
	return jsonResult(data), nil
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

func jsonResult(data json.RawMessage) *mcp.CallToolResult {
	// Keep the exact JSON text so large numeric IDs are not rounded through
	// map[string]any. Managed callers can parse it with UseNumber when needed.
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(data)}},
	}
}

func toolError(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: message}},
		StructuredContent: map[string]any{"error": message},
		IsError:           true,
	}
}
