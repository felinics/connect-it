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
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/store"
)

// CallbackPath 是固定回调路径，回调完整地址 = CONNECT_IT_BASE_URL + CallbackPath。
const CallbackPath = "/v1/oauth/callback"

const (
	authorizationTTL = 10 * time.Minute
	statusPending    = "pending"
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

type Service struct {
	q       *store.Queries
	reg     *registry.Registry
	cfg     *configsvc.Service
	kr      *crypto.Keyring
	hc      *http.Client
	baseURL string
}

type authorizationDraft struct {
	url    string
	scopes []string
	params store.CreateOAuthAuthorizationParams
}

func New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service, kr *crypto.Keyring, hc *http.Client, baseURL string) *Service {
	return &Service{q: q, reg: reg, cfg: cfg, kr: kr, hc: hc, baseURL: strings.TrimRight(baseURL, "/")}
}

// Begin 创建一条 pending 连接并生成授权 URL。alias 是可选展示标签。
func (s *Service) Begin(ctx context.Context, t connector.Type, authMethodKey, alias string) (BeginResult, error) {
	if err := s.q.ExpireOAuthAuthorizations(ctx); err != nil {
		return BeginResult{}, err
	}
	def, ok := s.reg.Get(t)
	if !ok {
		return BeginResult{}, fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	method, err := findOAuthMethod(def, authMethodKey)
	if err != nil {
		return BeginResult{}, err
	}

	connID := uuid.New()
	emptyCred, keyVersion, err := s.kr.Encrypt(nil, []byte(connID.String()))
	if err != nil {
		return BeginResult{}, err
	}
	var aliasPtr *string
	if alias != "" {
		aliasPtr = &alias
	}
	draft, err := s.prepareAuthorization(ctx, t, method, connID)
	if err != nil {
		return BeginResult{}, err
	}
	scopes := draft.scopes
	if scopes == nil {
		scopes = []string{}
	}

	// pending connection 与 authorization 必须一起落库；任何一步失败都不留下孤儿。
	tx, qtx, err := s.q.BeginTx(ctx)
	if err != nil {
		return BeginResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := qtx.CreateConnection(ctx, store.CreateConnectionParams{
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
	if _, err := qtx.CreateOAuthAuthorization(ctx, draft.params); err != nil {
		return BeginResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BeginResult{}, err
	}
	return BeginResult{ConnectionID: connID, AuthorizationURL: draft.url}, nil
}

// BeginReauth 对既有连接重新发起授权：ID 不变，回调后覆盖凭证并置 active。
func (s *Service) BeginReauth(ctx context.Context, connectionID uuid.UUID) (BeginResult, error) {
	if err := s.q.ExpireOAuthAuthorizations(ctx); err != nil {
		return BeginResult{}, err
	}
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
	draft, err := s.prepareAuthorization(ctx, t, method, row.ID)
	if err != nil {
		return BeginResult{}, err
	}

	// 同一 connection 同时只保留最新一次 reauth；较早的 state 立即失效。
	tx, qtx, err := s.q.BeginTx(ctx)
	if err != nil {
		return BeginResult{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	locked, err := qtx.GetConnectionForUpdate(ctx, row.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BeginResult{}, ErrConnectionGone
		}
		return BeginResult{}, err
	}
	if locked.Status == "authorization_failed" || locked.Status == "reauth_required" {
		if err := qtx.UpdateConnectionStatus(ctx, store.UpdateConnectionStatusParams{
			ID: locked.ID, Status: statusPending,
		}); err != nil {
			return BeginResult{}, err
		}
	}
	if err := qtx.SupersedeOpenOAuthAuthorizations(ctx, row.ID); err != nil {
		return BeginResult{}, err
	}
	if _, err := qtx.CreateOAuthAuthorization(ctx, draft.params); err != nil {
		return BeginResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return BeginResult{}, err
	}
	return BeginResult{ConnectionID: row.ID, AuthorizationURL: draft.url}, nil
}

func (s *Service) prepareAuthorization(ctx context.Context, t connector.Type, method connector.AuthMethod, connectionID uuid.UUID) (authorizationDraft, error) {
	authEndpoint, clientID, resource, scopes, oauthClientID, usePKCE, err :=
		s.authorizationParameters(ctx, t, method)
	if err != nil {
		return authorizationDraft{}, err
	}

	state, err := randomToken()
	if err != nil {
		return authorizationDraft{}, err
	}
	verifier := ""
	if usePKCE {
		if verifier, err = randomToken(); err != nil {
			return authorizationDraft{}, err
		}
	}

	authzID := uuid.New()
	encVerifier, keyVersion, err := s.kr.Encrypt([]byte(verifier), []byte(authzID.String()))
	if err != nil {
		return authorizationDraft{}, err
	}
	authzParams := store.CreateOAuthAuthorizationParams{
		ID:               authzID,
		ConnectorType:    string(t),
		StateHash:        hashToken(state),
		PkceVerifier:     encVerifier,
		SecretKeyVersion: int32(keyVersion),
		AuthMethod:       method.Key,
		ConnectionID:     connectionID,
		OauthClientID:    oauthClientID,
		ExpiresAt:        time.Now().Add(authorizationTTL),
	}

	params := url.Values{}
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", s.baseURL+CallbackPath)
	params.Set("state", state)
	if len(scopes) > 0 {
		separator := method.OAuth.ScopeSeparator
		if separator == "" {
			separator = " "
		}
		params.Set("scope", strings.Join(scopes, separator))
	}
	if usePKCE {
		params.Set("code_challenge", s256Challenge(verifier))
		params.Set("code_challenge_method", "S256")
	}
	if resource != "" {
		params.Set("resource", resource)
	}
	for k, v := range method.OAuth.ExtraAuthParams {
		params.Set(k, v)
	}
	sep := "?"
	if strings.Contains(authEndpoint, "?") {
		sep = "&"
	}
	return authorizationDraft{
		url:    authEndpoint + sep + params.Encode(),
		scopes: scopes,
		params: authzParams,
	}, nil
}

func (s *Service) authorizationParameters(
	ctx context.Context,
	t connector.Type,
	method connector.AuthMethod,
) (
	authEndpoint string,
	clientID string,
	resource string,
	scopes []string,
	oauthClientID *uuid.UUID,
	usePKCE bool,
	err error,
) {
	resolved, err := s.cfg.Resolved(ctx, t)
	if err != nil {
		return "", "", "", nil, nil, false, err
	}
	if method.OAuth.Mode != connector.OAuthModeMCP {
		clientID, _, err = ClientCredentials(resolved)
		if err != nil {
			return "", "", "", nil, nil, false, err
		}
		authEndpoint, err = ExpandEndpoint(method.OAuth.AuthorizationEndpoint, resolved)
		return authEndpoint, clientID, "", method.OAuth.Scopes, nil,
			method.OAuth.UsePKCE, err
	}

	def, ok := s.reg.Get(t)
	if !ok {
		return "", "", "", nil, nil, false,
			fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	remote, ok := def.Implementation.(connector.RemoteMCP)
	if !ok {
		return "", "", "", nil, nil, false,
			fmt.Errorf("%w: connector 不是 remote MCP", ErrMCPDiscovery)
	}
	endpoint, err := resolveRemoteEndpoint(remote, resolved)
	if err != nil {
		return "", "", "", nil, nil, false,
			fmt.Errorf("%w: %v", ErrMCPDiscovery, err)
	}
	discovery, client, err := s.resolveMCPClient(ctx, t, endpoint)
	if err != nil {
		return "", "", "", nil, nil, false, err
	}
	clientRef := client.ID
	return discovery.AuthorizationEndpoint, client.ClientID, discovery.Resource,
		discovery.Scopes, &clientRef, true, nil
}

// HandleCallback 核对 state、用授权码换 token，把绑定的连接置 active。
func (s *Service) HandleCallback(ctx context.Context, state, code string) error {
	authz, err := s.claimAuthorization(ctx, state)
	if err != nil {
		return err
	}

	finished := false
	defer func() {
		if finished {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_ = s.failAuthorization(cleanupCtx, authz)
	}()

	connID := authz.ConnectionID

	t := connector.Type(authz.ConnectorType)
	def, ok := s.reg.Get(t)
	if !ok {
		return fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	method, err := findOAuthMethod(def, authz.AuthMethod)
	if err != nil {
		return err
	}
	verifier, err := s.kr.Decrypt(authz.PkceVerifier, int(authz.SecretKeyVersion), []byte(authz.ID.String()))
	if err != nil {
		return err
	}

	var (
		clientID, clientSecret, resource string
		oc                               connector.OAuthConfig
		usePKCE                          bool
	)
	if method.OAuth.Mode == connector.OAuthModeMCP {
		if authz.OauthClientID == nil {
			return errors.New("oauthsvc: MCP OAuth authorization 缺少 client registration")
		}
		client, err := LoadMCPClient(ctx, s.q, s.kr, *authz.OauthClientID)
		if err != nil {
			return err
		}
		if client.ConnectorType != t {
			return errors.New("oauthsvc: MCP OAuth client registration 不属于当前 connector")
		}
		clientID, clientSecret, resource = client.ClientID, client.ClientSecret, client.Resource
		oc = connector.OAuthConfig{
			TokenEndpoint:     client.TokenEndpoint,
			TokenEndpointAuth: client.TokenEndpointAuth,
		}
		usePKCE = true
	} else {
		resolved, err := s.cfg.Resolved(ctx, t)
		if err != nil {
			return err
		}
		clientID, clientSecret, err = ClientCredentials(resolved)
		if err != nil {
			return err
		}
		oc = *method.OAuth
		if oc.TokenEndpoint, err = ExpandEndpoint(oc.TokenEndpoint, resolved); err != nil {
			return err
		}
		usePKCE = method.OAuth.UsePKCE
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.baseURL+CallbackPath)
	if usePKCE {
		form.Set("code_verifier", string(verifier))
	}
	if resource != "" {
		form.Set("resource", resource)
	}
	tok, err := ExchangeToken(ctx, s.hc, &oc, clientID, clientSecret, form)
	if err != nil {
		return err
	}

	now := time.Now()
	cred := credential.OAuth{AccessToken: tok.AccessToken, RefreshToken: tok.RefreshToken}
	if tok.ExpiresIn > 0 {
		cred.ExpiresAt = now.Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	plain, err := cred.Marshal()
	if err != nil {
		return err
	}
	var expiresAt *time.Time
	if !cred.ExpiresAt.IsZero() {
		expiresAt = &cred.ExpiresAt
	}
	ciphertext, keyVersion, err := s.kr.Encrypt(plain, []byte(connID.String()))
	if err != nil {
		return err
	}

	// connection 更新与 authorization 完成必须原子提交。
	tx, qtx, err := s.q.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := qtx.GetConnectionForUpdate(ctx, connID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConnectionGone
		}
		return err
	}
	if err := qtx.ActivateOAuthConnection(ctx, store.ActivateOAuthConnectionParams{
		ID:                   connID,
		Credential:           ciphertext,
		SecretKeyVersion:     int32(keyVersion),
		AccessTokenExpiresAt: expiresAt,
		OauthClientID:        authz.OauthClientID,
	}); err != nil {
		return err
	}
	n, err := qtx.DeleteClaimedOAuthAuthorization(ctx, authz.ID)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalidState
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	finished = true
	return nil
}

// RejectCallback 消费 provider 拒绝或取消返回的 state，并结束本次授权。
func (s *Service) RejectCallback(ctx context.Context, state string) error {
	authz, err := s.claimAuthorization(ctx, state)
	if err != nil {
		return err
	}
	if err := s.failAuthorization(ctx, authz); err != nil {
		return err
	}
	return nil
}

func (s *Service) claimAuthorization(ctx context.Context, state string) (store.OauthAuthorization, error) {
	if state == "" {
		return store.OauthAuthorization{}, ErrInvalidState
	}
	authz, err := s.q.ClaimOAuthAuthorization(ctx, hashToken(state))
	if errors.Is(err, pgx.ErrNoRows) {
		// 顺手收敛过期或进程中断的授权；不影响当前 invalid_state 结果。
		_ = s.q.ExpireOAuthAuthorizations(ctx)
		return store.OauthAuthorization{}, ErrInvalidState
	}
	if err != nil {
		return store.OauthAuthorization{}, err
	}
	return authz, nil
}

func (s *Service) failAuthorization(ctx context.Context, authz store.OauthAuthorization) error {
	tx, qtx, err := s.q.BeginTx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	n, err := qtx.DeleteClaimedOAuthAuthorization(ctx, authz.ID)
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalidState
	}
	if err := qtx.MarkPendingConnectionAuthorizationFailed(ctx, authz.ConnectionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
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

func resolveRemoteEndpoint(remote connector.RemoteMCP, config map[string]any) (string, error) {
	if remote.EndpointSelector == nil {
		return remote.Endpoint, nil
	}
	field := remote.EndpointSelector.ConfigField
	option, _ := config[field].(string)
	endpoint, ok := remote.EndpointSelector.Endpoints[option]
	if !ok {
		return "", fmt.Errorf("remote MCP endpoint option %q 无效", option)
	}
	return endpoint, nil
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
