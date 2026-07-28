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

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int64  `json:"expires_in"`
}

// TokenEndpointError retains only a status and standard OAuth error code.
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

func IsInvalidGrant(err error) bool {
	var endpointErr *TokenEndpointError
	return errors.As(err, &endpointErr) && endpointErr.OAuthCode == "invalid_grant"
}

func IsInvalidClient(err error) bool {
	var endpointErr *TokenEndpointError
	return errors.As(err, &endpointErr) && endpointErr.OAuthCode == "invalid_client"
}

func ClientCredentials(config map[string]any) (clientID, clientSecret string, err error) {
	clientID, _ = config["client_id"].(string)
	clientSecret, _ = config["client_secret"].(string)
	if clientID == "" || clientSecret == "" {
		return "", "", ErrMissingClient
	}
	return clientID, clientSecret, nil
}

func ExchangeToken(
	ctx context.Context,
	hc *http.Client,
	oc *connector.OAuthConfig,
	clientID, clientSecret string,
	form url.Values,
) (TokenResponse, error) {
	switch oc.TokenEndpointAuth {
	case connector.TokenAuthPost:
		form.Set("client_id", clientID)
		if clientSecret != "" {
			form.Set("client_secret", clientSecret)
		}
	case connector.TokenAuthNone:
		form.Set("client_id", clientID)
	default:
		// Basic credentials are attached below.
	}
	req, err := http.NewRequestWithContext(
		ctx, http.MethodPost, oc.TokenEndpoint, strings.NewReader(form.Encode()),
	)
	if err != nil {
		return TokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if oc.TokenEndpointAuth != connector.TokenAuthPost &&
		oc.TokenEndpointAuth != connector.TokenAuthNone {
		req.SetBasicAuth(url.QueryEscape(clientID), url.QueryEscape(clientSecret))
	}
	resp, err := noRedirectClient(hc).Do(req)
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

// OAuth credentials must never follow a redirect to another host.
func noRedirectClient(hc *http.Client) *http.Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	clone := *hc
	clone.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &clone
}
