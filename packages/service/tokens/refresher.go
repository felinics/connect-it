// Package tokens hands out an access token guaranteed to be usable: api_key
// credentials pass straight through, while OAuth tokens are refreshed lazily
// with a 60 second skew, an in-process single-flight, and a re-check under a
// database row lock, so a rotating refresh token is never lost to a
// concurrent overwrite.
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

	"github.com/felinics/connect-it/packages/core/connector"
	"github.com/felinics/connect-it/packages/core/crypto"
	"github.com/felinics/connect-it/packages/core/registry"
	"github.com/felinics/connect-it/packages/service/configsvc"
	"github.com/felinics/connect-it/packages/service/credential"
	"github.com/felinics/connect-it/packages/service/oauthsvc"
	"github.com/felinics/connect-it/packages/service/store"
)

const (
	expirySkew     = 60 * time.Second
	refreshTimeout = 30 * time.Second
)

var (
	ErrReauthRequired = errors.New("tokens: connection requires re-authorization")
	ErrNotFound       = errors.New("tokens: connection does not exist")
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

// AccessToken returns the credential currently usable for a connection: for
// api_key and custom_credential it is the value of the first declared field,
// and for OAuth it is a valid access token.
func (r *Refresher) AccessToken(ctx context.Context, connectionID uuid.UUID) (string, error) {
	row, err := r.q.GetConnection(ctx, connectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if row.Status != "active" {
		return "", fmt.Errorf("%w (current status %s)", ErrReauthRequired, row.Status)
	}
	def, ok := r.reg.Get(connector.Type(row.ConnectorType))
	if !ok {
		return "", fmt.Errorf("tokens: unknown connector type %s", row.ConnectorType)
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
			return "", fmt.Errorf("tokens: auth method %s declares no CredentialFields", method.Key)
		}
		// Convention: a single-field credential. When several are declared,
		// the first one in the Definition wins.
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
			return cred.AccessToken, nil // still valid, use as is
		}
		// Expired or about to expire: single-flight ensures only one
		// goroutine in this process actually refreshes.
		// The shared refresh is not tied to the first caller but still needs
		// its own deadline; every waiter honours its own context so a refresh
		// cannot blow through the overall tools/list budget.
		resultCh := r.group.DoChan(connectionID.String(), func() (any, error) {
			refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
			defer cancel()
			return r.refresh(refreshCtx, connectionID, method, connector.Type(row.ConnectorType))
		})
		select {
		case result := <-resultCh:
			if result.Err != nil {
				return "", result.Err
			}
			return result.Val.(string), nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	default:
		return "", fmt.Errorf("tokens: auth method %s of type %s is not supported", method.Key, method.Type)
	}
}

func findMethod(def connector.Definition, key string) (connector.AuthMethod, error) {
	for _, m := range def.AuthMethods {
		if m.Key == key {
			return m, nil
		}
	}
	return connector.AuthMethod{}, fmt.Errorf("tokens: unknown auth method %s", key)
}

// refresh re-checks and refreshes inside a row-locked transaction;
// single-flight guarantees only one execution per process.
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
	requireReauth := func(cause error) (string, error) {
		if err := qtx.UpdateConnectionStatus(ctx, store.UpdateConnectionStatusParams{
			ID: connectionID, Status: "reauth_required",
		}); err != nil {
			return "", err
		}
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		if cause == nil {
			return "", ErrReauthRequired
		}
		return "", fmt.Errorf("%w: %v", ErrReauthRequired, cause)
	}
	plain, err := r.kr.Decrypt(row.Credential, int(row.SecretKeyVersion), []byte(row.ID.String()))
	if err != nil {
		return "", err
	}
	cred, err := credential.UnmarshalOAuth(plain)
	if err != nil {
		return "", err
	}
	// Re-check: another instance may have refreshed while we waited for the lock.
	if cred.ExpiresAt.IsZero() || time.Until(cred.ExpiresAt) > expirySkew {
		if err := tx.Commit(ctx); err != nil {
			return "", err
		}
		return cred.AccessToken, nil
	}
	if cred.RefreshToken == "" {
		return requireReauth(nil)
	}

	var (
		clientID, clientSecret, resource string
		oc                               connector.OAuthConfig
	)
	if method.OAuth.Mode == connector.OAuthModeMCP {
		if row.OauthClientID == nil {
			return "", errors.New("tokens: MCP OAuth connection has no client registration")
		}
		client, err := oauthsvc.LoadMCPClient(ctx, qtx, r.kr, *row.OauthClientID)
		if err != nil {
			if errors.Is(err, oauthsvc.ErrMCPClientExpired) {
				return requireReauth(err)
			}
			return "", err
		}
		if client.ConnectorType != t {
			return "", errors.New("tokens: MCP OAuth client registration does not belong to this connector")
		}
		clientID, clientSecret, resource = client.ClientID, client.ClientSecret, client.Resource
		oc = connector.OAuthConfig{
			TokenEndpoint:     client.TokenEndpoint,
			TokenEndpointAuth: client.TokenEndpointAuth,
		}
	} else {
		resolved, err := r.cfg.Resolved(ctx, t)
		if err != nil {
			return "", err
		}
		clientID, clientSecret, err = oauthsvc.ClientCredentials(resolved)
		if err != nil {
			return "", err
		}
		// The refresh path must expand {tenant}-style placeholders too, as
		// the OneDrive token endpoint requires.
		oc = *method.OAuth
		if oc.TokenEndpoint, err = oauthsvc.ExpandEndpoint(oc.TokenEndpoint, resolved); err != nil {
			return "", err
		}
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", cred.RefreshToken)
	if resource != "" {
		form.Set("resource", resource)
	}
	tok, err := oauthsvc.ExchangeToken(ctx, r.hc, &oc, clientID, clientSecret, form)
	if err != nil {
		// Only require re-authorization when the provider explicitly rules
		// the refresh token invalid. Network errors, rate limits and 5xx
		// keep the connection active so later calls retry naturally.
		invalidClient := method.OAuth.Mode == connector.OAuthModeMCP &&
			oauthsvc.IsInvalidClient(err)
		if !oauthsvc.IsInvalidGrant(err) && !invalidClient {
			return "", fmt.Errorf("tokens: refresh access token: %w", err)
		}
		if invalidClient && row.OauthClientID != nil {
			if expireErr := qtx.ExpireOAuthClientRegistration(ctx, *row.OauthClientID); expireErr != nil {
				return "", expireErr
			}
		}
		return requireReauth(err)
	}

	newCred := credential.OAuth{
		AccessToken:  tok.AccessToken,
		RefreshToken: cred.RefreshToken,
	}
	if tok.RefreshToken != "" {
		newCred.RefreshToken = tok.RefreshToken // rotation: replace only when the response carries a new value
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
