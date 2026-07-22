// Package connsvc 管理 connection 的创建（api_key / custom_credential）、列表与删除。
// OAuth connection 的创建走 oauthsvc。
package connsvc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/store"
)

// AliasPattern 是 alias 的合法形式（spec §7）。
var AliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

var (
	ErrInvalidAlias      = errors.New("connsvc: alias 必须匹配 ^[a-z0-9][a-z0-9-]{0,31}$")
	ErrUnknownConnector  = errors.New("connsvc: 未知 connector type")
	ErrUnknownAuthMethod = errors.New("connsvc: 未知 auth method")
	ErrWrongAuthType     = errors.New("connsvc: auth method 不是 api_key / custom_credential")
	ErrInvalidFields     = errors.New("connsvc: credential 字段不合法")
	ErrAliasTaken        = errors.New("connsvc: alias 已被占用")
	ErrNotFound          = errors.New("connsvc: connection 不存在")
)

type Service struct {
	q   *store.Queries
	reg *registry.Registry
	kr  *crypto.Keyring
}

func New(q *store.Queries, reg *registry.Registry, kr *crypto.Keyring) *Service {
	return &Service{q: q, reg: reg, kr: kr}
}

// ConnectionView 是不含 credential 的对外视图。
type ConnectionView struct {
	ID            uuid.UUID `json:"id"`
	ConnectorType string    `json:"connector_type"`
	Alias         string    `json:"alias"`
	AuthMethod    string    `json:"auth_method"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

func (s *Service) CreateAPIKey(ctx context.Context, t connector.Type, authMethodKey, alias string, fields map[string]string) (uuid.UUID, error) {
	if !AliasPattern.MatchString(alias) {
		return uuid.Nil, ErrInvalidAlias
	}
	def, ok := s.reg.Get(t)
	if !ok {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	var method *connector.AuthMethod
	for i := range def.AuthMethods {
		if def.AuthMethods[i].Key == authMethodKey {
			method = &def.AuthMethods[i]
			break
		}
	}
	if method == nil {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrUnknownAuthMethod, authMethodKey)
	}
	if method.Type != connector.AuthAPIKey && method.Type != connector.AuthCustomCredential {
		return uuid.Nil, fmt.Errorf("%w: %s 是 %s", ErrWrongAuthType, authMethodKey, method.Type)
	}
	if err := validateFields(method.CredentialFields, fields); err != nil {
		return uuid.Nil, err
	}

	plain, err := credential.Fields{Fields: fields}.Marshal()
	if err != nil {
		return uuid.Nil, err
	}
	id := uuid.New()
	ct, ver, err := s.kr.Encrypt(plain, []byte(id.String()))
	if err != nil {
		return uuid.Nil, err
	}
	_, err = s.q.CreateConnection(ctx, store.CreateConnectionParams{
		ID:               id,
		ConnectorType:    string(t),
		Alias:            alias,
		AuthMethod:       authMethodKey,
		Credential:       ct,
		SecretKeyVersion: int32(ver),
		Scopes:           []string{},
		Status:           "active",
		// AccessTokenExpiresAt 保持 nil（NULL）：api_key 不过期
	})
	if err != nil {
		if isUniqueViolation(err) {
			return uuid.Nil, fmt.Errorf("%w: %s", ErrAliasTaken, alias)
		}
		return uuid.Nil, err
	}
	return id, nil
}

func (s *Service) List(ctx context.Context) ([]ConnectionView, error) {
	rows, err := s.q.ListConnections(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]ConnectionView, 0, len(rows))
	for _, r := range rows {
		out = append(out, toView(r))
	}
	return out, nil
}

func (s *Service) Get(ctx context.Context, id uuid.UUID) (ConnectionView, error) {
	row, err := s.q.GetConnection(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ConnectionView{}, ErrNotFound
		}
		return ConnectionView{}, err
	}
	return toView(row), nil
}

func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	n, err := s.q.DeleteConnection(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func toView(r store.Connection) ConnectionView {
	return ConnectionView{
		ID:            r.ID,
		ConnectorType: r.ConnectorType,
		Alias:         r.Alias,
		AuthMethod:    r.AuthMethod,
		Status:        r.Status,
		CreatedAt:     r.CreatedAt,
	}
}

func validateFields(defs []connector.ConfigField, got map[string]string) error {
	byKey := map[string]connector.ConfigField{}
	for _, f := range defs {
		byKey[f.Key] = f
	}
	for k := range got {
		if _, ok := byKey[k]; !ok {
			return fmt.Errorf("%w: 未声明的字段 %q", ErrInvalidFields, k)
		}
	}
	for _, f := range defs {
		v, ok := got[f.Key]
		if f.Required && (!ok || v == "") {
			return fmt.Errorf("%w: 缺少必填字段 %q", ErrInvalidFields, f.Key)
		}
		if ok && v != "" && f.Validation.Pattern != "" {
			re, err := regexp.Compile(f.Validation.Pattern)
			if err != nil {
				return fmt.Errorf("%w: 字段 %q 的校验正则非法: %v", ErrInvalidFields, f.Key, err)
			}
			if !re.MatchString(v) {
				return fmt.Errorf("%w: 字段 %q 不符合 %s", ErrInvalidFields, f.Key, f.Validation.Pattern)
			}
		}
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
