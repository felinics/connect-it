package gmail

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"

	"github.com/memohai/connect-it/packages/core/connector"
)

// apiBaseURL 指向 Gmail REST API 根地址；测试用 httptest.Server 覆盖后还原。
var apiBaseURL = "https://gmail.googleapis.com"

// Handlers 由 connectors.AllHandlers() 暴露给执行引擎。
var Handlers = connector.HandlerMap{
	"list_messages": listMessages,
	"send_message":  sendMessage,
}

// listMessages 调 GET /gmail/v1/users/me/messages。
// 参数：q（Gmail 搜索语法，可选）、max_results（1–100，默认 20）。
func listMessages(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
	max := 20
	if v, ok := call.Arguments["max_results"].(float64); ok && v >= 1 && v <= 100 {
		max = int(v)
	}
	u := apiBaseURL + "/gmail/v1/users/me/messages?maxResults=" + strconv.Itoa(max)
	if q, _ := call.Arguments["q"].(string); q != "" {
		u += "&q=" + url.QueryEscape(q)
	}
	return callGmail(ctx, http.MethodGet, u, nil, call.AccessToken)
}

// sendMessage 组装 RFC 2822 纯文本邮件，base64url 编码后
// 调 POST /gmail/v1/users/me/messages/send。
func sendMessage(ctx context.Context, call connector.ToolCallContext) (connector.ToolResultData, error) {
	to, _ := call.Arguments["to"].(string)
	subject, _ := call.Arguments["subject"].(string)
	body, _ := call.Arguments["body"].(string)
	if to == "" || subject == "" || body == "" {
		return connector.ToolResultData{
			Text:       "to、subject、body 均为必填参数",
			Structured: json.RawMessage(`{"error":"to、subject、body 均为必填参数"}`),
			IsError:    true,
		}, nil
	}
	rfc822 := fmt.Sprintf(
		"To: %s\r\nSubject: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
		to, subject, body)
	payload, err := json.Marshal(map[string]string{
		"raw": base64.URLEncoding.EncodeToString([]byte(rfc822)),
	})
	if err != nil {
		return connector.ToolResultData{}, err
	}
	return callGmail(ctx, http.MethodPost,
		apiBaseURL+"/gmail/v1/users/me/messages/send",
		bytes.NewReader(payload), call.AccessToken)
}

// callGmail 发送请求并把结果统一转换为 ToolResultData：
// 传输层失败返回 error；HTTP >= 400 转成 IsError 结果（error 为 nil）。
// 与 onedrive 包的 callGraph 结构相同：目录映射测试要求 packages/connectors
// 下只有 provider 目录，因此不抽公共包，接受两份小重复。
func callGmail(ctx context.Context, method, u string, body io.Reader, token string) (connector.ToolResultData, error) {
	req, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return connector.ToolResultData{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
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
			"error":  fmt.Sprintf("gmail api 返回 %d", resp.StatusCode),
			"detail": string(data),
		})
		return connector.ToolResultData{
			Text:       fmt.Sprintf("gmail api 返回 %d", resp.StatusCode),
			Structured: detail,
			IsError:    true,
		}, nil
	}
	return connector.ToolResultData{Structured: data}, nil
}
