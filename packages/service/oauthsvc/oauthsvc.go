// Package oauthsvc 实现 OAuth 授权发起（state＋PKCE）与回调处理（授权码换 token）。
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
	ErrAliasTaken        = errors.New("oauthsvc: alias 已被其他 connector 或 auth method 占用")
	ErrInvalidState      = errors.New("oauthsvc: state 无效或已过期")
	ErrMissingClient     = errors.New("oauthsvc: 配置缺少 client_id / client_secret")
)

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

// Begin 生成授权 URL：随机 state（库中只存 sha256）、PKCE S256，
// 写 oauth_authorizations（10 分钟过期）。alias 已有同 connector＋同 auth method
// 的 connection 时进入重授权路径（绑定 connection_id）。
func (s *Service) Begin(ctx context.Context, t connector.Type, authMethodKey, alias string) (string, error) {
	if !connsvc.AliasPattern.MatchString(alias) {
		return "", connsvc.ErrInvalidAlias
	}
	def, ok := s.reg.Get(t)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	method, err := findOAuthMethod(def, authMethodKey)
	if err != nil {
		return "", err
	}

	var connectionID *uuid.UUID
	existing, err := s.q.GetConnectionByAlias(ctx, alias)
	switch {
	case err == nil:
		if existing.ConnectorType != string(t) || existing.AuthMethod != authMethodKey {
			return "", fmt.Errorf("%w: %s", ErrAliasTaken, alias)
		}
		id := existing.ID
		connectionID = &id // 重授权
	case errors.Is(err, pgx.ErrNoRows):
		// 全新连接
	default:
		return "", err
	}

	return s.begin(ctx, t, method, alias, connectionID)
}

// BeginReauth 对既有 connection 重新发起授权：复用其 connector_type／auth_method／alias。
func (s *Service) BeginReauth(ctx context.Context, connectionID uuid.UUID) (string, error) {
	row, err := s.q.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", connsvc.ErrNotFound
		}
		return "", err
	}
	t := connector.Type(row.ConnectorType)
	def, ok := s.reg.Get(t)
	if !ok {
		return "", fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	method, err := findOAuthMethod(def, row.AuthMethod)
	if err != nil {
		return "", err
	}
	id := row.ID
	return s.begin(ctx, t, method, row.Alias, &id)
}

func (s *Service) begin(ctx context.Context, t connector.Type, method connector.AuthMethod, alias string, connectionID *uuid.UUID) (string, error) {
	clientID, _, err := ClientCredentials(ctx, s.cfg, t)
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
	if _, err := s.q.CreateOAuthAuthorization(ctx, store.CreateOAuthAuthorizationParams{
		ID:               authzID,
		ConnectorType:    string(t),
		StateHash:        hashToken(state),
		PkceVerifier:     encVerifier,
		SecretKeyVersion: int32(keyVersion),
		AuthMethod:       method.Key,
		Alias:            alias,
		ConnectionID:     connectionID,
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
	if strings.Contains(method.OAuth.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	return method.OAuth.AuthorizationEndpoint + sep + params.Encode(), nil
}

// HandleCallback 核对 state hash、用授权码换 token，创建或更新 connection 并置 active。
func (s *Service) HandleCallback(ctx context.Context, state, code string) (uuid.UUID, error) {
	authz, err := s.q.GetOAuthAuthorizationByStateHash(ctx, hashToken(state))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, ErrInvalidState
		}
		return uuid.Nil, err
	}
	if authz.Status != statusPending || time.Now().After(authz.ExpiresAt) {
		return uuid.Nil, ErrInvalidState
	}

	t := connector.Type(authz.ConnectorType)
	def, ok := s.reg.Get(t)
	if !ok {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	method, err := findOAuthMethod(def, authz.AuthMethod)
	if err != nil {
		return uuid.Nil, err
	}
	clientID, clientSecret, err := ClientCredentials(ctx, s.cfg, t)
	if err != nil {
		return uuid.Nil, err
	}
	verifier, err := s.kr.Decrypt(authz.PkceVerifier, int(authz.SecretKeyVersion), []byte(authz.ID.String()))
	if err != nil {
		return uuid.Nil, err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.baseURL+CallbackPath)
	if method.OAuth.UsePKCE {
		form.Set("code_verifier", string(verifier))
	}
	tok, err := ExchangeToken(ctx, s.hc, method.OAuth, clientID, clientSecret, form)
	if err != nil {
		return uuid.Nil, err
	}

	now := time.Now()
	cred := credential.OAuth{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken}
	if tok.ExpiresIn > 0 {
		cred.ExpiresAt = now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	plain, err := cred.Marshal()
	if err != nil {
		return uuid.Nil, err
	}
	var expiresAt *time.Time
	if !cred.ExpiresAt.IsZero() {
		expiresAt = &cred.ExpiresAt
	}

	var connID uuid.UUID
	if authz.ConnectionID != nil {
		// 重授权：更新既有 connection，AAD 用既有 id。
		connID = *authz.ConnectionID
		ct, ver, err := s.kr.Encrypt(plain, []byte(connID.String()))
		if err != nil {
			return uuid.Nil, err
		}
		if err := s.q.UpdateConnectionCredential(ctx, store.UpdateConnectionCredentialParams{
			ID:                   connID,
			Credential:           ct,
			SecretKeyVersion:     int32(ver),
			Status:               "active",
			AccessTokenExpiresAt: expiresAt,
		}); err != nil {
			return uuid.Nil, err
		}
	} else {
		connID = uuid.New()
		ct, ver, err := s.kr.Encrypt(plain, []byte(connID.String()))
		if err != nil {
			return uuid.Nil, err
		}
		scopes := method.OAuth.Scopes
		if scopes == nil {
			scopes = []string{}
		}
		if _, err := s.q.CreateConnection(ctx, store.CreateConnectionParams{
			ID:                   connID,
			ConnectorType:        authz.ConnectorType,
			Alias:                authz.Alias,
			AuthMethod:           authz.AuthMethod,
			Credential:           ct,
			SecretKeyVersion:     int32(ver),
			Scopes:               scopes,
			Status:               "active",
			AccessTokenExpiresAt: expiresAt,
		}); err != nil {
			return uuid.Nil, err
		}
	}

	if err := s.q.CompleteOAuthAuthorization(ctx, store.CompleteOAuthAuthorizationParams{
		ID:           authz.ID,
		Status:       statusCompleted,
		ConnectionID: &connID,
	}); err != nil {
		return uuid.Nil, err
	}
	return connID, nil
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
