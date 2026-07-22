package onedrive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/memohai/connect-it/packages/core/connector"
)

// apiBaseURL 指向 Microsoft Graph 根地址；测试用 httptest.Server 覆盖后还原。
var apiBaseURL = "https://graph.microsoft.com"

// Handlers 由 connectors.AllHandlers() 暴露给执行引擎。
var Handlers = connector.HandlerMap{
	"list_drive_items": listDriveItems,
	"upload_file":      uploadFile,
}

// listDriveItems 列出根目录（GET /v1.0/me/drive/root/children）
// 或指定文件夹（GET /v1.0/me/drive/root:/{path}:/children）。
func listDriveItems(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
	u := apiBaseURL + "/v1.0/me/drive/root/children"
	if path, _ := call.Arguments["path"].(string); path != "" {
		u = apiBaseURL + "/v1.0/me/drive/root:/" + escapePath(path) + ":/children"
	}
	return callGraph(ctx, http.MethodGet, u, nil, "", call.AccessToken)
}

// uploadFile 简单上传文本文件（PUT /v1.0/me/drive/root:/{path}:/content，<4MB）。
func uploadFile(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
	path, _ := call.Arguments["path"].(string)
	content, _ := call.Arguments["content"].(string)
	if path == "" || content == "" {
		return connector.ToolResultData{
			Text:       "path 与 content 均为必填参数",
			Structured: json.RawMessage(`{"error":"path 与 content 均为必填参数"}`),
			IsError:    true,
		}, nil
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

// callGraph 发送请求并把结果统一转换为 ToolResultData：
// 传输层失败返回 error；HTTP >= 400 转成 IsError 结果（error 为 nil）。
// 与 gmail 包的 callGmail 结构相同：目录映射测试要求 packages/connectors
// 下只有 provider 目录，因此不抽公共包，接受两份小重复。
func callGraph(ctx context.Context, method, u string, body io.Reader, contentType, token string) (connector.ToolResultData, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	if resp.StatusCode >= 400 {
		detail, _ := json.Marshal(map[string]any{
			"error":  fmt.Sprintf("microsoft graph 返回 %d", resp.StatusCode),
			"detail": string(data),
		})
		return connector.ToolResultData{
			Text:       fmt.Sprintf("microsoft graph 返回 %d", resp.StatusCode),
			Structured: detail,
			IsError:    true,
		}, nil
	}
	return connector.ToolResultData{Structured: data}, nil
}
