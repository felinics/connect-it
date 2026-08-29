// Package connsvc creates (for api_key and custom_credential), lists and
// deletes connections. OAuth connections are created through oauthsvc.
package connsvc

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/felinics/connect-it/packages/core/crypto"
	"github.com/felinics/connect-it/packages/core/registry"
	"github.com/felinics/connect-it/packages/service/configsvc"
	"github.com/felinics/connect-it/packages/service/credential"
	"github.com/felinics/connect-it/packages/service/store"
)

// AliasPattern is the accepted form of the optional connection display label.
var AliasPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

var (
	ErrInvalidAlias      = errors.New("connsvc: alias must match ^[a-z0-9][a-z0-9-]{0,31}$")
	ErrUnknownConnector  = errors.New("connsvc: unknown connector type")
	ErrUnknownAuthMethod = errors.New("connsvc: unknown auth method")
	ErrWrongAuthType     = errors.New("connsvc: auth method is not api_key or custom_credential")
	ErrInvalidFields     = errors.New("connsvc: invalid credential fields")
	ErrNotFound          = errors.New("connsvc: connection does not exist")
)

type Service struct {
	q   *store.Queries
	reg *registry.Registry
	cfg *configsvc.Service
	kr  *crypto.Keyring
}

func New(q *store.Queries, reg *registry.Registry, cfg *configsvc.Service, kr *crypto.Keyring) *Service {
	return &Service{q: q, reg: reg, cfg: cfg, kr: kr}
}

// ConnectionView is the outward-facing view, without the credential.
type ConnectionView struct {
	ID            uuid.UUID `json:"id"`
	ConnectorType string    `json:"connector_type"`
	Alias         string    `json:"alias"`
	AuthMethod    string    `json:"auth_method"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
}

// CreateAPIKey creates one api_key or custom_credential connection and
// returns its durable ID. alias is an optional display label; an empty
// string means none.
func (s *Service) CreateAPIKey(ctx context.Context, t connector.Type, authMethodKey, alias string, fields map[string]string) (uuid.UUID, error) {
	if alias != "" && !AliasPattern.MatchString(alias) {
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
		return uuid.Nil, fmt.Errorf("%w: %s is %s", ErrWrongAuthType, authMethodKey, method.Type)
	}
	if err := validateFields(method.CredentialFields, fields); err != nil {
		return uuid.Nil, err
	}
	if err := s.cfg.RequireEnabled(ctx, t); err != nil {
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
	var aliasPtr *string
	if alias != "" {
		aliasPtr = &alias
	}
	_, err = s.q.CreateConnection(ctx, store.CreateConnectionParams{
		ID:               id,
		ConnectorType:    string(t),
		Alias:            aliasPtr,
		AuthMethod:       authMethodKey,
		Credential:       ct,
		SecretKeyVersion: int32(ver),
		Scopes:           []string{},
		Status:           "active",
		// AccessTokenExpiresAt stays nil (NULL): api_key credentials do not expire.
	})
	if err != nil {
		return uuid.Nil, err
	}
	return id, nil
}

func (s *Service) List(ctx context.Context) ([]ConnectionView, error) {
	if err := s.q.ExpireOAuthAuthorizations(ctx); err != nil {
		return nil, err
	}
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
	if err := s.q.ExpireOAuthAuthorizations(ctx); err != nil {
		return ConnectionView{}, err
	}
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

func validateFields(defs []connector.ConfigField, got map[string]string) error {
	byKey := map[string]connector.ConfigField{}
	for _, f := range defs {
		byKey[f.Key] = f
	}
	for k := range got {
		if _, ok := byKey[k]; !ok {
			return fmt.Errorf("%w: undeclared field %q", ErrInvalidFields, k)
		}
	}
	for _, f := range defs {
		v, ok := got[f.Key]
		if f.Required && (!ok || v == "") {
			return fmt.Errorf("%w: missing required field %q", ErrInvalidFields, f.Key)
		}
		if ok && v != "" && f.Validation.Pattern != "" {
			re, err := regexp.Compile(f.Validation.Pattern)
			if err != nil {
				return fmt.Errorf("%w: field %q has an invalid validation pattern: %v", ErrInvalidFields, f.Key, err)
			}
			if !re.MatchString(v) {
				return fmt.Errorf("%w: field %q does not match %s", ErrInvalidFields, f.Key, f.Validation.Pattern)
			}
		}
		if ok && v != "" && len(f.Validation.Options) > 0 {
			valid := false
			for _, option := range f.Validation.Options {
				if v == option {
					valid = true
					break
				}
			}
			if !valid {
				return fmt.Errorf("%w: field %q is not an allowed option", ErrInvalidFields, f.Key)
			}
		}
	}
	return nil
}
