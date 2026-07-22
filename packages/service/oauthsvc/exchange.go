package oauthsvc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/configsvc"
)

// TokenResponse 是 token endpoint 标准响应的子集。
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// ClientCredentials 从 Resolved 配置中取约定字段 client_id / client_secret。
// 约定：需要 OAuth 的 Connector 必须声明这两个 ConfigField（client_secret 为 Secret）。
func ClientCredentials(ctx context.Context, cfg *configsvc.Service, t connector.Type) (clientID, clientSecret string, err error) {
	resolved, err := cfg.Resolved(ctx, t)
	if err != nil {
		return "", "", err
	}
	clientID, _ = resolved["client_id"].(string)
	clientSecret, _ = resolved["client_secret"].(string)
	if clientID == "" {
		return "", "", ErrMissingClient
	}
	return clientID, clientSecret, nil
}

// ExchangeToken 请求 token endpoint。form 由调用方填好 grant_type 等业务参数，
// 本函数按 TokenEndpointAuth 补齐客户端认证：
// client_secret_post→写入 form；否则（含零值）→client_secret_basic。
func ExchangeToken(ctx context.Context, hc *http.Client, oc *connector.OAuthConfig, clientID, clientSecret string, form url.Values) (TokenResponse, error) {
	if oc.TokenEndpointAuth == connector.TokenAuthPost {
		form.Set("client_id", clientID)
		if clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, oc.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if oc.TokenEndpointAuth != connector.TokenAuthPost {
		// RFC 6749 §2.3.1：Basic 认证的用户名密码须先做 form 编码。
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}
	resp, err := hc.Do(req)
	if err != nil {
		return TokenResponse{}, fmt.Errorf("oauthsvc: 请求 token endpoint: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return TokenResponse{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return TokenResponse{}, fmt.Errorf("oauthsvc: token endpoint 返回 %d: %s", resp.StatusCode, truncate(body, 256))
	}
	var tok TokenResponse
	if err := json.Unmarshal(body, &tok); err != nil {
		return TokenResponse{}, fmt.Errorf("oauthsvc: token 响应不是 JSON: %w", err)
	}
	if tok.AccessToken == "" {
		return TokenResponse{}, errors.New("oauthsvc: token 响应缺少 access_token")
	}
	return tok, nil
}

func truncate(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return string(b)
}
