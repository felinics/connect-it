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
)

// TokenResponse 是 token endpoint 标准响应的子集。
type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// TokenEndpointError 是 provider 明确返回的 OAuth 错误。只保留状态码和
// 标准 error code，避免把 provider 响应中的敏感细节写入日志或审计表。
type TokenEndpointError struct {
	StatusCode int
	OAuthCode  string
}

func (e *TokenEndpointError) Error() string {
	if e.OAuthCode != "" {
		return fmt.Sprintf("oauthsvc: token endpoint 返回 %d (%s)", e.StatusCode, e.OAuthCode)
	}
	return fmt.Sprintf("oauthsvc: token endpoint 返回 %d", e.StatusCode)
}

func (e *TokenEndpointError) UpstreamStatusCode() int { return e.StatusCode }

// IsInvalidGrant 表示 refresh token 已失效，只有这种错误需要连接重新授权。
func IsInvalidGrant(err error) bool {
	var endpointErr *TokenEndpointError
	return errors.As(err, &endpointErr) && endpointErr.OAuthCode == "invalid_grant"
}

// ClientCredentials 从 Connector 的 resolved config 读取 OAuth 客户端凭证。
func ClientCredentials(config map[string]any) (clientID, clientSecret string, err error) {
	clientID, _ = config["client_id"].(string)
	clientSecret, _ = config["client_secret"].(string)
	if clientID == "" || clientSecret == "" {
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
	var payload struct {
		TokenResponse
		Error string `json:"error"`
	}
	jsonErr := json.Unmarshal(body, &payload)
	if resp.StatusCode != http.StatusOK {
		return TokenResponse{}, &TokenEndpointError{
			StatusCode: resp.StatusCode,
			OAuthCode:  payload.Error,
		}
	}
	if jsonErr != nil {
		return TokenResponse{}, fmt.Errorf("oauthsvc: token 响应不是 JSON: %w", jsonErr)
	}
	if payload.Error != "" {
		return TokenResponse{}, &TokenEndpointError{
			StatusCode: resp.StatusCode,
			OAuthCode:  payload.Error,
		}
	}
	if payload.AccessToken == "" {
		return TokenResponse{}, errors.New("oauthsvc: token 响应缺少 access_token")
	}
	return payload.TokenResponse, nil
}
