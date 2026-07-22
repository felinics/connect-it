// Package tokens 对外提供「保证可用的 access token」：api_key 直通，
// OAuth 惰性刷新——60 秒 skew、进程内 single-flight、数据库行锁二次判断，
// 防止轮转 refresh token 在并发下被覆盖丢失（spec §10）。
package tokens

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/singleflight"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/store"
)

const expirySkew = 60 * time.Second

var (
	ErrReauthRequired = errors.New("tokens: connection 需要重新授权")
	ErrNotFound       = errors.New("tokens: connection 不存在")
)

type Refresher struct {
	q     *store.Queries
	reg   *registry.Registry
	cfg   *configsvc.Service
	kr    *crypto.Keyring
	hc    *http.Client
	group singleflight.Group
}

func New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service, kr *crypto.Keyring, hc *http.Client) *Refresher {
	return &Refresher{q: q, reg: reg, cfg: cfg, kr: kr, hc: hc}
}

// AccessToken 返回该 connection 当前可用的凭证：
// api_key / custom_credential 返回其首个声明字段的值；OAuth 返回有效 access token。
func (r *Refresher) AccessToken(ctx context.Context, connectionID uuid.UUID) (string, error) {
	row, err := r.q.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if row.Status != "active" {
		return "", fmt.Errorf("%w（当前状态 %s）", ErrReauthRequired, row.Status)
	}
	def, ok := r.reg.Get(connector.Type(row.ConnectorType))
	if !ok {
		return "", fmt.Errorf("tokens: 未知 connector type %s", row.ConnectorType)
	}
	method, err := findMethod(def, row.AuthMethod)
	if err != nil {
		return "", err
	}

	switch method.Type {
	case connector.AuthAPIKey, connector.AuthCustomCredential:
		plain, err := r.kr.Decrypt(row.Credential, int(row.SecretKeyVersion), []byte(row.ID.String()))
		if err != nil {
			return "", err
		}
		fields, err := credential.UnmarshalFields(plain)
		if err != nil {
			return "", err
		}
		if len(method.CredentialFields) == 0 {
			return "", fmt.Errorf("tokens: auth method %s 未声明 CredentialFields", method.Key)
		}
		// 约定：单字段凭证；多字段取 Definition 中首个声明字段。
		return fields.Fields[method.CredentialFields[0].Key], nil
	case connector.AuthOAuth2:
		plain, err := r.kr.Decrypt(row.Credential, int(row.SecretKeyVersion), []byte(row.ID.String()))
		if err != nil {
			return "", err
		}
		cred, err := credential.UnmarshalOAuth(plain)
		if err != nil {
			return "", err
		}
		if cred.ExpiresAt.IsZero() || time.Until(cred.ExpiresAt) > expirySkew {
			return cred.AccessToken, nil // 未过期，直接用
		}
		// 过期或即将过期：single-flight，进程内只有一个 goroutine 真正刷新。
		// WithoutCancel：首个调用方取消不应连累等待结果的其他调用方。
		v, err, _ := r.group.Do(connectionID.String(), func() (any, error) {
			return r.refresh(context.WithoutCancel(ctx), connectionID, method, connector.Type(row.ConnectorType))
		})
		if err != nil {
			return "", err
		}
		return v.(string), nil
	default:
		return "", fmt.Errorf("tokens: auth method %s 类型 %s 不支持", method.Key, method.Type)
	}
}

func findMethod(def connector.Definition, key string) (connector.AuthMethod, error) {
	for _, m := range def.AuthMethods {
		if m.Key == key {
			return m, nil
		}
	}
	return connector.AuthMethod{}, fmt.Errorf("tokens: 未知 auth method %s", key)
}

// refresh 在行锁事务内二次判断并刷新；single-flight 保证进程内只有一个真正执行。
func (r *Refresher) refresh(ctx context.Context, connectionID uuid.UUID, method connector.AuthMethod, t connector.Type) (string, error) {
	tx, qtx, err := r.q.BeginTx(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, err := qtx.GetConnectionForUpdate(ctx, connectionID)
	if err != nil {
		return "", err
	}
	plain, err := r.kr.Decrypt(row.Credential, int(row.SecretKeyVersion), []byte(row.ID.String()))
	if err != nil {
		return "", err
	}
	cred, err := credential.UnmarshalOAuth(plain)
	if err != nil {
		return "", err
	}
	// 二次判断：拿到锁时可能别的实例刚刷完。
	if cred.ExpiresAt.IsZero() || time.Until(cred.ExpiresAt) > expirySkew {
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return cred.AccessToken, nil
	}

	resolved, err := r.cfg.Resolved(ctx, t)
	if err != nil {
		return "", err
	}
	clientID, _ := resolved["client_id"].(string)
	clientSecret, _ := resolved["client_secret"].(string)
	if clientID == "" {
		return "", oauthsvc.ErrMissingClient
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", cred.RefreshToken)
	// 刷新路径同样要展开 {tenant} 类占位符（OneDrive token endpoint）。
	oc := *method.OAuth
	if oc.TokenEndpoint, err = oauthsvc.ExpandEndpoint(oc.TokenEndpoint, resolved); err != nil {
		return "", err
	}
	tok, err := oauthsvc.ExchangeToken(ctx, r.hc, &oc, clientID, clientSecret, form)
	if err != nil {
		// 网络瞬断也会标记 reauth_required——第一期接受的粗粒度行为。
		if uerr := qtx.UpdateConnectionStatus(ctx, store.UpdateConnectionStatusParams{
			ID: connectionID, Status: "reauth_required",
		}); uerr == nil {
			_ = tx.Commit(ctx)
		}
		return "", fmt.Errorf("%w: %v", ErrReauthRequired, err)
	}

	newCred := credential.OAuth{
		AccessToken:  tok.AccessToken,
		RefreshToken: cred.RefreshToken,
	}
	if tok.RefreshToken != "" {
		newCred.RefreshToken = tok.RefreshToken // 轮转：响应带新值才替换
	}
	if tok.ExpiresIn > 0 {
		newCred.ExpiresAt = time.Now().Add(time.Duration(tok.ExpiresIn) * time.Second)
	}
	newPlain, err := newCred.Marshal()
	if err != nil {
		return "", err
	}
	ct, ver, err := r.kr.Encrypt(newPlain, []byte(connectionID.String()))
	if err != nil {
		return "", err
	}
	var expiresAt *time.Time
	if !newCred.ExpiresAt.IsZero() {
		expiresAt = &newCred.ExpiresAt
	}
	if err := qtx.UpdateConnectionCredential(ctx, store.UpdateConnectionCredentialParams{
		ID:                   connectionID,
		Credential:           ct,
		SecretKeyVersion:     int32(ver),
		Status:               "active",
		AccessTokenExpiresAt: expiresAt,
	}); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return newCred.AccessToken, nil
}
