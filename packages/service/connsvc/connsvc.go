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

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/internal/credentialcheck"
	"github.com/memohai/connect-it/packages/service/internal/fieldnorm"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/svcerr"
)

// AliasPattern 是 alias 的唯一合法形式（spec §7）；connection 与 MCP session
// 共用它，Go 侧不再有第二份拷贝。
var AliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

var (
	ErrInvalidAlias      = svcerr.New(svcerr.Invalid, fmt.Sprintf("connsvc: alias 必须匹配 %s", AliasPattern))
	ErrUnknownConnector  = svcerr.New(svcerr.NotFound, "connsvc: 未知 connector type")
	ErrUnknownAuthMethod = svcerr.New(svcerr.Invalid, "connsvc: 未知 auth method")
	ErrWrongAuthType     = svcerr.New(svcerr.Invalid, "connsvc: auth method 不是 api_key / custom_credential")
	ErrInvalidFields     = svcerr.New(svcerr.Invalid, "connsvc: credential 字段不合法")
	ErrNotFound          = svcerr.New(svcerr.NotFound, "connsvc: connection 不存在")
	ErrConflict          = svcerr.New(svcerr.ConnectionConflict, "connsvc: connection credential 已被并发修改")
)

type configResolver interface {
	ResolvedWithPolicy(
		context.Context,
		connector.Type,
	) (map[string]any, configsvc.PolicySnapshot, error)
}

type Service struct {
	q          *store.Queries
	reg        *registry.Registry
	kr         *crypto.Keyring
	configs    configResolver
	validators connector.CredentialValidatorMap
	matchers   connector.ScopeMatcherMap
}

func New(
	q *store.Queries,
	reg *registry.Registry,
	kr *crypto.Keyring,
	configs configResolver,
	validators connector.CredentialValidatorMap,
	matchers connector.ScopeMatcherMap,
) *Service {
	return &Service{
		q:          q,
		reg:        reg,
		kr:         kr,
		configs:    configs,
		validators: validators,
		matchers:   matchers,
	}
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

// CreateAPIKey 创建一条 api_key / custom_credential 连接并返回其持久 ID。
// alias 是可选展示标签（空串表示不设）。
func (s *Service) CreateAPIKey(ctx context.Context, t connector.Type, authMethodKey, alias string, fields map[string]string) (uuid.UUID, error) {
	if alias != "" && !AliasPattern.MatchString(alias) {
		return uuid.Nil, ErrInvalidAlias
	}
	def, ok := s.reg.Get(t)
	if !ok {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	method := authMethod(def, authMethodKey)
	if method == nil {
		return uuid.Nil, fmt.Errorf("%w: %s", ErrUnknownAuthMethod, authMethodKey)
	}
	if method.Type != connector.AuthAPIKey && method.Type != connector.AuthCustomCredential {
		return uuid.Nil, fmt.Errorf("%w: %s 是 %s", ErrWrongAuthType, authMethodKey, method.Type)
	}
	normalizedFields, err := normalizeCredentialFields(
		method.CredentialFields,
		fields,
	)
	if err != nil {
		return uuid.Nil, err
	}
	authorizationID := uuid.New().String()
	snapshot, policy, err := s.validateFieldsCredential(
		ctx,
		def,
		*method,
		normalizedFields,
		"",
		authorizationID,
	)
	if err != nil {
		return uuid.Nil, err
	}

	plain, err := credential.Fields{Fields: normalizedFields}.Marshal()
	if err != nil {
		return uuid.Nil, err
	}
	id := uuid.New()
	ct, ver, err := s.kr.Encrypt(plain, []byte(id.String()))
	if err != nil {
		return uuid.Nil, err
	}
	var aliasPtr *string
	if alias != "" {
		aliasPtr = &alias
	}
	_, err = s.q.CreateConnectionAtPolicyIdentity(
		ctx,
		store.CreateConnectionAtPolicyIdentityParams{
			ID:                       id,
			ConnectorType:            string(t),
			Alias:                    aliasPtr,
			AuthMethod:               authMethodKey,
			Credential:               ct,
			SecretKeyVersion:         int32(ver),
			Profile:                  snapshot.Profile,
			Scopes:                   snapshot.Scopes,
			ScopesKnown:              snapshot.ScopesKnown,
			Status:                   "active",
			ExpectedIdentityVersion:  policy.IdentityVersion,
			ExpectedIdentityDigest:   policy.IdentityDigest,
			ExpectedDefinitionDigest: policy.DefinitionDigest,
			// AccessTokenExpiresAt 保持 nil（NULL）：api_key 不过期
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrConflict
	}
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

// RecredentialAPIKey validates and atomically replaces the complete
// API-key/custom-credential group. The remote validation runs after taking a
// version snapshot; the final dual-version CAS prevents a slower request from
// overwriting a newer credential or authorization policy.
func (s *Service) RecredentialAPIKey(
	ctx context.Context,
	connectionID uuid.UUID,
	fields map[string]string,
) (ConnectionView, error) {
	if s.q == nil {
		return ConnectionView{}, errors.New("connsvc: store 未配置")
	}
	row, err := s.q.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ConnectionView{}, ErrNotFound
		}
		return ConnectionView{}, err
	}
	def, ok := s.reg.Get(connector.Type(row.ConnectorType))
	if !ok {
		return ConnectionView{}, fmt.Errorf(
			"%w: %s",
			ErrUnknownConnector,
			row.ConnectorType,
		)
	}
	method := authMethod(def, row.AuthMethod)
	if method == nil {
		return ConnectionView{}, fmt.Errorf(
			"%w: %s",
			ErrUnknownAuthMethod,
			row.AuthMethod,
		)
	}
	if method.Type != connector.AuthAPIKey &&
		method.Type != connector.AuthCustomCredential {
		return ConnectionView{}, fmt.Errorf(
			"%w: %s 是 %s",
			ErrWrongAuthType,
			method.Key,
			method.Type,
		)
	}
	if row.Status == "pending" {
		return ConnectionView{}, ErrConflict
	}
	normalizedFields, err := normalizeCredentialFields(
		method.CredentialFields,
		fields,
	)
	if err != nil {
		return ConnectionView{}, err
	}
	snapshot, _, err := s.validateFieldsCredential(
		ctx,
		def,
		*method,
		normalizedFields,
		connectionID.String(),
		"",
	)
	if err != nil {
		return ConnectionView{}, err
	}
	plain, err := credential.Fields{Fields: normalizedFields}.Marshal()
	if err != nil {
		return ConnectionView{}, err
	}
	ciphertext, keyVersion, err := s.kr.Encrypt(
		plain,
		[]byte(connectionID.String()),
	)
	if err != nil {
		return ConnectionView{}, err
	}
	_, err = s.q.RecredentialConnection(
		ctx,
		store.RecredentialConnectionParams{
			Credential:                      ciphertext,
			SecretKeyVersion:                int32(keyVersion),
			Profile:                         snapshot.Profile,
			Scopes:                          snapshot.Scopes,
			ScopesKnown:                     snapshot.ScopesKnown,
			ConnectionID:                    connectionID,
			ExpectedCredentialVersion:       row.CredentialVersion,
			ExpectedAuthorizationGeneration: row.AuthorizationGeneration,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ConnectionView{}, ErrConflict
	}
	if err != nil {
		return ConnectionView{}, err
	}
	updated, err := s.q.GetConnection(ctx, connectionID)
	if err != nil {
		return ConnectionView{}, err
	}
	return toView(updated), nil
}

func (s *Service) validateFieldsCredential(
	ctx context.Context,
	def connector.Definition,
	method connector.AuthMethod,
	fields map[string]string,
	connectionID string,
	authorizationID string,
) (credentialcheck.Snapshot, configsvc.PolicySnapshot, error) {
	if s.configs == nil {
		return credentialcheck.Snapshot{}, configsvc.PolicySnapshot{}, errors.New(
			"connsvc: config resolver 未配置",
		)
	}
	resolved, policy, err := s.configs.ResolvedWithPolicy(ctx, def.Type)
	if err != nil {
		return credentialcheck.Snapshot{}, configsvc.PolicySnapshot{}, err
	}
	snapshot, err := credentialcheck.Validate(
		ctx,
		s.validators,
		s.matchers,
		def,
		method,
		connector.CredentialValidationInput{
			ConnectorType:   def.Type,
			AuthMethodKey:   method.Key,
			AuthType:        method.Type,
			ConnectionID:    connectionID,
			AuthorizationID: authorizationID,
			Config:          resolved,
			Fields:          fields,
		},
		nil,
		false,
	)
	if err != nil {
		return credentialcheck.Snapshot{}, configsvc.PolicySnapshot{}, err
	}
	return snapshot, policy, nil
}

func authMethod(
	def connector.Definition,
	key string,
) *connector.AuthMethod {
	for i := range def.AuthMethods {
		if def.AuthMethods[i].Key == key {
			return &def.AuthMethods[i]
		}
	}
	return nil
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
	alias := ""
	if r.Alias != nil {
		alias = *r.Alias
	}
	return ConnectionView{
		ID:            r.ID,
		ConnectorType: r.ConnectorType,
		Alias:         alias,
		AuthMethod:    r.AuthMethod,
		Status:        r.Status,
		CreatedAt:     r.CreatedAt,
	}
}

func normalizeCredentialFields(
	definitions []connector.ConfigField,
	input map[string]string,
) (map[string]string, error) {
	normalized, err := fieldnorm.Normalize(definitions, input)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidFields, err)
	}
	return normalized, nil
}
