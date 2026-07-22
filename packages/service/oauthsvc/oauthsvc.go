// Package oauthsvc 实现 OAuth 授权发起（state＋PKCE）与回调处理（授权码换 token）。
//
// SaaS 模型：每次 Begin 立即创建一条 pending 连接并返回其持久 ID（连接 ID 即
// 句柄，调用方自己维护「谁拥有这个 ID」）；终端用户完成授权后回调把连接置
// active。BeginReauth 复用既有 ID 重新授权。
package oauthsvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/store"
)

// CallbackPath 是固定回调路径，回调完整地址 = CONNECT_IT_BASE_URL + CallbackPath。
const CallbackPath = "/v1/oauth/callback"

const (
	authorizationTTL = 10 * time.Minute
	statusPending    = "pending"
	statusCompleted  = "completed"
)

var (
	ErrUnknownConnector  = errors.New("oauthsvc: 未知 connector type")
	ErrUnknownAuthMethod = errors.New("oauthsvc: 未知 auth method")
	ErrNotOAuth          = errors.New("oauthsvc: auth method 不是 oauth2")
	ErrInvalidState      = errors.New("oauthsvc: state 无效或已过期")
	ErrMissingClient     = errors.New("oauthsvc: 配置缺少 client_id / client_secret")
	ErrConnectionGone    = errors.New("oauthsvc: connection 不存在")
)

// BeginResult 是授权发起的结果：连接 ID 当场返回（pending），
// AuthorizationURL 交给终端用户跳转。
type BeginResult struct {
	ConnectionID     uuid.UUID
	AuthorizationURL string
}

// CallbackResult 是回调处理的结果；RedirectURL 为发起时调用方登记的回跳地址
// （可为空，表示落在 connect-it 的默认完成页）。
type CallbackResult struct {
	ConnectionID uuid.UUID
	RedirectURL  string
}

type Service struct {
	q       *store.Queries
	reg     *registry.Registry
	cfg     *configsvc.Service
	kr      *crypto.Keyring
	hc      *http.Client
	baseURL string
}

func New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service, kr *crypto.Keyring, hc *http.Client, baseURL string) *Service {
	return &Service{q: q, reg: reg, cfg: cfg, kr: kr, hc: hc, baseURL: strings.TrimRight(baseURL, "/")}
}

// Begin 创建一条 pending 连接并生成授权 URL。alias 是可选展示标签；
// redirectURL 是授权完成后回跳给调用方的地址（可为空）。
func (s *Service) Begin(ctx context.Context, t connector.Type, authMethodKey, alias, redirectURL string) (BeginResult, error) {
	if alias != "" && !connsvc.AliasPattern.MatchString(alias) {
		return BeginResult{}, connsvc.ErrInvalidAlias
	}
	def, ok := s.reg.Get(t)
	if !ok {
		return BeginResult{}, fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	method, err := findOAuthMethod(def, authMethodKey)
	if err != nil {
		return BeginResult{}, err
	}

	// pending 连接：ID 当场生成并返回，凭证在回调时写入。
	connID := uuid.New()
	emptyCred, keyVersion, err := s.kr.Encrypt(nil, []byte(connID.String()))
	if err != nil {
		return BeginResult{}, err
	}
	scopes := method.OAuth.Scopes
	if scopes == nil {
		scopes = []string{}
	}
	var aliasPtr *string
	if alias != "" {
		aliasPtr = &alias
	}
	if _, err := s.q.CreateConnection(ctx, store.CreateConnectionParams{
		ID:               connID,
		ConnectorType:    string(t),
		Alias:            aliasPtr,
		AuthMethod:       authMethodKey,
		Credential:       emptyCred,
		SecretKeyVersion: int32(keyVersion),
		Scopes:           scopes,
		Status:           statusPending,
	}); err != nil {
		return BeginResult{}, err
	}

	authURL, err := s.createAuthorization(ctx, t, method, alias, connID, redirectURL)
	if err != nil {
		return BeginResult{}, err
	}
	return BeginResult{ConnectionID: connID, AuthorizationURL: authURL}, nil
}

// BeginReauth 对既有连接重新发起授权：ID 不变，回调后覆盖凭证并置 active。
func (s *Service) BeginReauth(ctx context.Context, connectionID uuid.UUID, redirectURL string) (BeginResult, error) {
	row, err := s.q.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BeginResult{}, ErrConnectionGone
		}
		return BeginResult{}, err
	}
	t := connector.Type(row.ConnectorType)
	def, ok := s.reg.Get(t)
	if !ok {
		return BeginResult{}, fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	method, err := findOAuthMethod(def, row.AuthMethod)
	if err != nil {
		return BeginResult{}, err
	}
	alias := ""
	if row.Alias != nil {
		alias = *row.Alias
	}
	authURL, err := s.createAuthorization(ctx, t, method, alias, row.ID, redirectURL)
	if err != nil {
		return BeginResult{}, err
	}
	return BeginResult{ConnectionID: row.ID, AuthorizationURL: authURL}, nil
}

func (s *Service) createAuthorization(ctx context.Context, t connector.Type, method connector.AuthMethod, alias string, connectionID uuid.UUID, redirectURL string) (string, error) {
	resolved, err := s.cfg.Resolved(ctx, t)
	if err != nil {
		return "", err
	}
	clientID, _ := resolved["client_id"].(string)
	if clientID == "" {
		return "", ErrMissingClient
	}
	authEndpoint, err := ExpandEndpoint(method.OAuth.AuthorizationEndpoint, resolved)
	if err != nil {
		return "", err
	}

	state, err := randomToken()
	if err != nil {
		return "", err
	}
	verifier := ""
	if method.OAuth.UsePKCE {
		if verifier, err = randomToken(); err != nil {
			return "", err
		}
	}

	authzID := uuid.New()
	encVerifier, keyVersion, err := s.kr.Encrypt([]byte(verifier), []byte(authzID.String()))
	if err != nil {
		return "", err
	}
	connID := connectionID
	if _, err := s.q.CreateOAuthAuthorization(ctx, store.CreateOAuthAuthorizationParams{
		ID:               authzID,
		ConnectorType:    string(t),
		StateHash:        hashToken(state),
		PkceVerifier:     encVerifier,
		SecretKeyVersion: int32(keyVersion),
		AuthMethod:       method.Key,
		Alias:            alias,
		ConnectionID:     &connID,
		RedirectUrl:      redirectURL,
		Status:           statusPending,
		ExpiresAt:        time.Now().Add(authorizationTTL),
	}); err != nil {
		return "", err
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", s.baseURL+CallbackPath)
	params.Set("state", state)
	if len(method.OAuth.Scopes) > 0 {
		params.Set("scope", strings.Join(method.OAuth.Scopes, " "))
	}
	if method.OAuth.UsePKCE {
		params.Set("code_challenge", s256Challenge(verifier))
		params.Set("code_challenge_method", "S256")
	}
	for k, v := range method.OAuth.ExtraAuthParams {
		params.Set(k, v)
	}
	sep := "?"
	if strings.Contains(authEndpoint, "?") {
		sep = "&"
	}
	return authEndpoint + sep + params.Encode(), nil
}

// HandleCallback 核对 state、用授权码换 token，把绑定的连接置 active。
func (s *Service) HandleCallback(ctx context.Context, state, code string) (CallbackResult, error) {
	authz, err := s.q.GetOAuthAuthorizationByStateHash(ctx, hashToken(state))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CallbackResult{}, ErrInvalidState
		}
		return CallbackResult{}, err
	}
	result := CallbackResult{RedirectURL: authz.RedirectUrl}
	if authz.Status != statusPending || time.Now().After(authz.ExpiresAt) {
		return result, ErrInvalidState
	}
	if authz.ConnectionID == nil {
		return result, ErrInvalidState
	}
	connID := *authz.ConnectionID
	result.ConnectionID = connID

	t := connector.Type(authz.ConnectorType)
	def, ok := s.reg.Get(t)
	if !ok {
		return result, fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	method, err := findOAuthMethod(def, authz.AuthMethod)
	if err != nil {
		return result, err
	}
	resolved, err := s.cfg.Resolved(ctx, t)
	if err != nil {
		return result, err
	}
	clientID, _ := resolved["client_id"].(string)
	clientSecret, _ := resolved["client_secret"].(string)
	if clientID == "" {
		return result, ErrMissingClient
	}
	verifier, err := s.kr.Decrypt(authz.PkceVerifier, int(authz.SecretKeyVersion), []byte(authz.ID.String()))
	if err != nil {
		return result, err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.baseURL+CallbackPath)
	if method.OAuth.UsePKCE {
		form.Set("code_verifier", string(verifier))
	}
	// {tenant} 类占位符在调用前展开；OAuthConfig 本身保持纯数据。
	oc := *method.OAuth
	if oc.TokenEndpoint, err = ExpandEndpoint(oc.TokenEndpoint, resolved); err != nil {
		return result, err
	}
	tok, err := ExchangeToken(ctx, s.hc, &oc, clientID, clientSecret, form)
	if err != nil {
		return result, err
	}

	now := time.Now()
	cred := credential.OAuth{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken}
	if tok.ExpiresIn > 0 {
		cred.ExpiresAt = now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	plain, err := cred.Marshal()
	if err != nil {
		return result, err
	}
	var expiresAt *time.Time
	if !cred.ExpiresAt.IsZero() {
		expiresAt = &cred.ExpiresAt
	}
	ciphertext, keyVersion, err := s.kr.Encrypt(plain, []byte(connID.String()))
	if err != nil {
		return result, err
	}
	if err := s.q.UpdateConnectionCredential(ctx, store.UpdateConnectionCredentialParams{
		ID:                   connID,
		Credential:           ciphertext,
		SecretKeyVersion:     int32(keyVersion),
		Status:               "active",
		AccessTokenExpiresAt: expiresAt,
	}); err != nil {
		return result, err
	}
	if err := s.q.CompleteOAuthAuthorization(ctx, store.CompleteOAuthAuthorizationParams{
		ID:           authz.ID,
		Status:       statusCompleted,
		ConnectionID: &connID,
	}); err != nil {
		return result, err
	}
	return result, nil
}

func findOAuthMethod(def connector.Definition, key string) (connector.AuthMethod, error) {
	for _, m := range def.AuthMethods {
		if m.Key == key {
			if m.Type != connector.AuthOAuth2 || m.OAuth == nil {
				return connector.AuthMethod{}, fmt.Errorf("%w: %s", ErrNotOAuth, key)
			}
			return m, nil
		}
	}
	return connector.AuthMethod{}, fmt.Errorf("%w: %s", ErrUnknownAuthMethod, key)
}

// randomToken 返回 256bit 随机数的 base64url（43 字符，可直接用作 PKCE verifier）。
func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func s256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
