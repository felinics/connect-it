// Package tokens provides an authorization-generation-bound OAuth token
// resolver. Refresh uses a database lease and compare-and-swap; Provider
// network I/O never runs while holding a database transaction or row lock.
package tokens

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/singleflight"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/store"
)

const (
	expirySkew            = 60 * time.Second
	refreshLeaseTTL       = 90 * time.Second
	refreshLeaseSeconds   = int32(refreshLeaseTTL / time.Second)
	refreshRequestTimeout = 30 * time.Second
	refreshPollInterval   = 20 * time.Millisecond
	refreshCleanupTimeout = 3 * time.Second

	// refresh_state values written by the lease state machine.
	stateLeased     = "leased"
	stateRequesting = "requesting"
)

var (
	ErrReauthRequired       = errors.New("tokens: connection 需要重新授权")
	ErrNotFound             = errors.New("tokens: connection 不存在")
	ErrNotOAuth             = errors.New("tokens: connection 不是 OAuth credential")
	ErrAuthorizationChanged = errors.New(
		"tokens: authorization generation 已改变，请重建 Session",
	)
)

// OAuthToken is the immutable credential snapshot passed to Engine. TokenType
// is normalized by credential parsing and is never inferred by a handler.
// The versions identify the exact stored credential used for the eventual
// Provider request, including a credential_version advanced by refresh.
type OAuthToken struct {
	AccessToken             string
	TokenType               string
	CredentialVersion       int64
	AuthorizationGeneration int64
}

type Refresher struct {
	q        *store.Queries
	reg      *registry.Registry
	cfg      *configsvc.Service
	kr       *crypto.Keyring
	factory  *providerkit.Factory
	matchers connector.ScopeMatcherMap
	group    singleflight.Group
}

func New(
	q *store.Queries,
	reg *registry.Registry,
	cfg *configsvc.Service,
	kr *crypto.Keyring,
	factory *providerkit.Factory,
	matchers connector.ScopeMatcherMap,
) *Refresher {
	if matchers == nil {
		matchers = connector.ScopeMatcherMap{}
	}
	return &Refresher{
		q:        q,
		reg:      reg,
		cfg:      cfg,
		kr:       kr,
		factory:  factory,
		matchers: matchers,
	}
}

// AccessToken returns an immutable OAuth token snapshot for exactly
// expectedAuthorizationGeneration. A refresh that changes authorization facts
// advances generation and returns ErrAuthorizationChanged instead of letting
// the old Session continue with the new authorization.
func (r *Refresher) AccessToken(
	ctx context.Context,
	connectionID uuid.UUID,
	expectedAuthorizationGeneration int64,
) (OAuthToken, error) {
	if connectionID == uuid.Nil || expectedAuthorizationGeneration < 1 {
		return OAuthToken{}, ErrAuthorizationChanged
	}
	snapshot, err := r.readOAuthCredential(
		ctx,
		connectionID,
		expectedAuthorizationGeneration,
	)
	if err != nil {
		return OAuthToken{}, err
	}
	if credentialFresh(snapshot.credential, time.Now()) {
		return snapshot.token()
	}

	key := fmt.Sprintf("%s/%d", connectionID, expectedAuthorizationGeneration)
	result := r.group.DoChan(key, func() (any, error) {
		refreshCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			refreshRequestTimeout+refreshLeaseTTL,
		)
		defer cancel()
		return r.refreshUntilResolved(
			refreshCtx,
			connectionID,
			expectedAuthorizationGeneration,
		)
	})
	select {
	case <-ctx.Done():
		return OAuthToken{}, ctx.Err()
	case outcome := <-result:
		if outcome.Err != nil {
			return OAuthToken{}, outcome.Err
		}
		token, ok := outcome.Val.(OAuthToken)
		if !ok {
			return OAuthToken{}, errors.New(
				"tokens: refresh returned invalid value",
			)
		}
		return token, nil
	}
}

// oauthSnapshot is the decrypted OAuth credential together with the exact row
// and auth method it was read at.
type oauthSnapshot struct {
	row        store.Connection
	method     connector.AuthMethod
	credential credential.OAuth
}

func (s oauthSnapshot) connectorType() connector.Type {
	return connector.Type(s.row.ConnectorType)
}

func (s oauthSnapshot) token() (OAuthToken, error) {
	return tokenValue(
		s.credential,
		s.row.CredentialVersion,
		s.row.AuthorizationGeneration,
	)
}

func (r *Refresher) readOAuthCredential(
	ctx context.Context,
	connectionID uuid.UUID,
	expectedGeneration int64,
) (oauthSnapshot, error) {
	row, err := r.q.GetConnectionAtAuthorizationGeneration(
		ctx,
		store.GetConnectionAtAuthorizationGenerationParams{
			ConnectionID:                    connectionID,
			ExpectedAuthorizationGeneration: expectedGeneration,
		},
	)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return oauthSnapshot{}, err
		}
		if _, lookupErr := r.q.GetConnection(ctx, connectionID); errors.Is(
			lookupErr,
			pgx.ErrNoRows,
		) {
			return oauthSnapshot{}, ErrNotFound
		}
		return oauthSnapshot{}, ErrAuthorizationChanged
	}
	if row.Status != "active" {
		return oauthSnapshot{}, fmt.Errorf(
			"%w（当前状态 %s）",
			ErrReauthRequired,
			row.Status,
		)
	}
	def, ok := r.reg.Get(connector.Type(row.ConnectorType))
	if !ok {
		return oauthSnapshot{}, fmt.Errorf(
			"tokens: 未知 connector type %s",
			row.ConnectorType,
		)
	}
	method, err := findMethod(def, row.AuthMethod)
	if err != nil {
		return oauthSnapshot{}, err
	}
	if method.Type != connector.AuthOAuth2 || method.OAuth == nil {
		return oauthSnapshot{}, ErrNotOAuth
	}
	plaintext, err := r.kr.Decrypt(
		row.Credential,
		int(row.SecretKeyVersion),
		[]byte(row.ID.String()),
	)
	if err != nil {
		return oauthSnapshot{}, err
	}
	oauthCredential, err := credential.UnmarshalOAuth(plaintext)
	if err != nil {
		return oauthSnapshot{}, err
	}
	if oauthCredential.AccessToken == "" {
		return oauthSnapshot{}, errors.New(
			"tokens: OAuth credential 缺少 access token",
		)
	}
	return oauthSnapshot{row: row, method: method, credential: oauthCredential}, nil
}

func findMethod(
	def connector.Definition,
	key string,
) (connector.AuthMethod, error) {
	for _, method := range def.AuthMethods {
		if method.Key == key {
			return method, nil
		}
	}
	return connector.AuthMethod{}, fmt.Errorf(
		"tokens: 未知 auth method %s",
		key,
	)
}

func credentialFresh(value credential.OAuth, now time.Time) bool {
	return value.ExpiresAt.IsZero() ||
		value.ExpiresAt.Sub(now) > expirySkew
}

func tokenValue(
	value credential.OAuth,
	credentialVersion int64,
	authorizationGeneration int64,
) (OAuthToken, error) {
	if value.AccessToken == "" ||
		value.TokenType == "" ||
		credentialVersion < 1 ||
		authorizationGeneration < 1 {
		return OAuthToken{}, errors.New("tokens: OAuth token snapshot 不完整")
	}
	return OAuthToken{
		AccessToken:             value.AccessToken,
		TokenType:               value.TokenType,
		CredentialVersion:       credentialVersion,
		AuthorizationGeneration: authorizationGeneration,
	}, nil
}

func (r *Refresher) refreshUntilResolved(
	ctx context.Context,
	connectionID uuid.UUID,
	expectedGeneration int64,
) (OAuthToken, error) {
	for {
		if err := ctx.Err(); err != nil {
			return OAuthToken{}, err
		}
		// DB time, not an application clock, decides when an uncertain
		// requesting lease expires and enters the default no-replay state.
		if _, err := r.q.ResolveExpiredRequestingRefreshLeases(ctx); err != nil {
			return OAuthToken{}, err
		}
		snapshot, err := r.readOAuthCredential(
			ctx,
			connectionID,
			expectedGeneration,
		)
		if err != nil {
			return OAuthToken{}, err
		}
		if credentialFresh(snapshot.credential, time.Now()) {
			return snapshot.token()
		}

		owner := uuid.New()
		lease, err := r.q.AcquireRefreshLease(
			ctx,
			store.AcquireRefreshLeaseParams{
				Owner:                           &owner,
				LeaseSeconds:                    refreshLeaseSeconds,
				ConnectionID:                    connectionID,
				ExpectedAuthorizationGeneration: expectedGeneration,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			if err := waitForRefreshStateChange(ctx); err != nil {
				return OAuthToken{}, err
			}
			continue
		}
		if err != nil {
			return OAuthToken{}, err
		}
		return r.refreshAsLeaseOwner(ctx, snapshot, lease, owner)
	}
}

func waitForRefreshStateChange(ctx context.Context) error {
	timer := time.NewTimer(refreshPollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (r *Refresher) refreshAsLeaseOwner(
	ctx context.Context,
	snapshot oauthSnapshot,
	lease store.AcquireRefreshLeaseRow,
	owner uuid.UUID,
) (OAuthToken, error) {
	if lease.RefreshOwner == nil ||
		*lease.RefreshOwner != owner ||
		lease.AuthorizationGeneration != snapshot.row.AuthorizationGeneration {
		return OAuthToken{}, ErrAuthorizationChanged
	}
	leasedRow, err := r.q.GetRefreshLeaseCredential(
		ctx,
		store.GetRefreshLeaseCredentialParams{
			ConnectionID:                    snapshot.row.ID,
			Owner:                           &owner,
			ExpectedCredentialVersion:       lease.CredentialVersion,
			ExpectedAuthorizationGeneration: lease.AuthorizationGeneration,
		},
	)
	if err != nil {
		return OAuthToken{}, err
	}
	plaintext, err := r.kr.Decrypt(
		leasedRow.Credential,
		int(leasedRow.SecretKeyVersion),
		[]byte(leasedRow.ID.String()),
	)
	if err != nil {
		return OAuthToken{}, r.abortLeased(ctx, leasedRow, owner, err)
	}
	currentCredential, err := credential.UnmarshalOAuth(plaintext)
	if err != nil {
		return OAuthToken{}, r.abortLeased(ctx, leasedRow, owner, err)
	}
	if credentialFresh(currentCredential, time.Now()) {
		// The success path must observe the release: a CAS miss here means the
		// authorization moved on and this token snapshot may not be handed out.
		if err := r.clearLease(ctx, leasedRow, owner, stateLeased); err != nil {
			return OAuthToken{}, err
		}
		return tokenValue(
			currentCredential,
			leasedRow.CredentialVersion,
			leasedRow.AuthorizationGeneration,
		)
	}
	if currentCredential.RefreshToken == "" {
		leased := stateLeased
		affected, err := r.q.MarkRefreshLeaseReauthRequiredCAS(
			ctx,
			store.MarkRefreshLeaseReauthRequiredCASParams{
				ConnectionID:                    leasedRow.ID,
				Owner:                           &owner,
				ExpectedRefreshState:            &leased,
				ExpectedCredentialVersion:       leasedRow.CredentialVersion,
				ExpectedAuthorizationGeneration: leasedRow.AuthorizationGeneration,
			},
		)
		if err != nil {
			return OAuthToken{}, err
		}
		if affected != 1 {
			return OAuthToken{}, ErrAuthorizationChanged
		}
		return OAuthToken{}, ErrReauthRequired
	}

	connectorType := snapshot.connectorType()
	resolved, err := r.cfg.Resolved(ctx, connectorType)
	if err != nil {
		return OAuthToken{}, r.abortLeased(ctx, leasedRow, owner, err)
	}
	clientID, _ := resolved["client_id"].(string)
	clientSecret, _ := resolved["client_secret"].(string)
	if clientID == "" {
		return OAuthToken{}, r.abortLeased(
			ctx,
			leasedRow,
			owner,
			oauthsvc.ErrMissingClient,
		)
	}
	oauthConfig := *snapshot.method.OAuth
	if err := oauthsvc.ExpandEndpoints(&oauthConfig, resolved); err != nil {
		return OAuthToken{}, r.abortLeased(ctx, leasedRow, owner, err)
	}
	if _, err := r.q.MarkRefreshLeaseRequesting(
		ctx,
		store.MarkRefreshLeaseRequestingParams{
			ConnectionID:                    leasedRow.ID,
			Owner:                           &owner,
			ExpectedCredentialVersion:       leasedRow.CredentialVersion,
			ExpectedAuthorizationGeneration: leasedRow.AuthorizationGeneration,
		},
	); err != nil {
		return OAuthToken{}, err
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", currentCredential.RefreshToken)
	tokenResponse, err := oauthsvc.ExchangeToken(
		ctx,
		r.factory,
		connectorType,
		&oauthConfig,
		clientID,
		clientSecret,
		form,
		providerkit.RequestLabels{
			ConnectionID: leasedRow.ID.String(),
		},
	)
	if err != nil {
		return OAuthToken{}, r.handleRefreshFailure(
			ctx,
			leasedRow,
			owner,
			err,
		)
	}

	newCredential := refreshedCredential(
		currentCredential,
		tokenResponse,
		time.Now(),
	)
	newScopes, scopesKnown := refreshedScopes(
		tokenResponse,
		leasedRow.Scopes,
		leasedRow.ScopesKnown,
		r.scopeMatcher(connectorType, snapshot.method.Key),
	)
	authorizationFactsChanged :=
		scopesKnown != leasedRow.ScopesKnown ||
			!reflect.DeepEqual(newScopes, leasedRow.Scopes) ||
			newCredential.TokenType != currentCredential.TokenType

	newPlaintext, err := newCredential.Marshal()
	if err != nil {
		// A malformed successful response may have consumed a rotating token.
		// Keep the requesting lease for no-replay expiry.
		return OAuthToken{}, err
	}
	ciphertext, keyVersion, err := r.kr.Encrypt(
		newPlaintext,
		[]byte(leasedRow.ID.String()),
	)
	if err != nil {
		return OAuthToken{}, err
	}
	var expiresAt *time.Time
	if !newCredential.ExpiresAt.IsZero() {
		expiresAt = &newCredential.ExpiresAt
	}
	completed, err := r.q.CompleteRefreshLeaseCAS(
		ctx,
		store.CompleteRefreshLeaseCASParams{
			Credential:                      ciphertext,
			SecretKeyVersion:                int32(keyVersion),
			Scopes:                          newScopes,
			ScopesKnown:                     scopesKnown,
			AccessTokenExpiresAt:            expiresAt,
			AuthorizationFactsChanged:       authorizationFactsChanged,
			ConnectionID:                    leasedRow.ID,
			Owner:                           &owner,
			ExpectedCredentialVersion:       leasedRow.CredentialVersion,
			ExpectedAuthorizationGeneration: leasedRow.AuthorizationGeneration,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return OAuthToken{}, ErrAuthorizationChanged
	}
	if err != nil {
		return OAuthToken{}, err
	}
	if authorizationFactsChanged ||
		completed.AuthorizationGeneration != leasedRow.AuthorizationGeneration {
		return OAuthToken{}, ErrAuthorizationChanged
	}
	return tokenValue(
		newCredential,
		completed.CredentialVersion,
		completed.AuthorizationGeneration,
	)
}

func refreshedCredential(
	current credential.OAuth,
	response oauthsvc.TokenValue,
	now time.Time,
) credential.OAuth {
	next := credential.OAuth{
		AccessToken:  response.AccessToken,
		TokenType:    response.TokenType,
		RefreshToken: current.RefreshToken,
	}
	if response.RefreshToken != nil {
		next.RefreshToken = *response.RefreshToken
	}
	if response.ExpiresInSeconds != nil {
		next.ExpiresAt = now.Add(
			time.Duration(*response.ExpiresInSeconds) * time.Second,
		)
	}
	return next
}

// abortLeased releases a lease that never reached the Provider and returns the
// original cause. A failed release is deliberately not reported over that
// cause: the lease is still fenced by owner/version/generation and DB time
// expires it, so at worst refresh is delayed — it is never replayed.
func (r *Refresher) abortLeased(
	ctx context.Context,
	row store.Connection,
	owner uuid.UUID,
	cause error,
) error {
	_ = r.clearLease(ctx, row, owner, stateLeased)
	return cause
}

func (r *Refresher) clearLease(
	ctx context.Context,
	row store.Connection,
	owner uuid.UUID,
	state string,
) error {
	affected, err := r.q.ClearRefreshLeaseCAS(
		ctx,
		store.ClearRefreshLeaseCASParams{
			ConnectionID:                    row.ID,
			Owner:                           &owner,
			ExpectedRefreshState:            &state,
			ExpectedCredentialVersion:       row.CredentialVersion,
			ExpectedAuthorizationGeneration: row.AuthorizationGeneration,
		},
	)
	if err != nil {
		return err
	}
	if affected != 1 {
		return ErrAuthorizationChanged
	}
	return nil
}

// clearLeaseAfterDefinitiveFailure is intentionally detached from request
// cancellation and tightly bounded. Callers may use it only when no Provider
// request was sent or an explicitly reviewed Provider contract proves that the
// refresh token was not consumed. Merely receiving a complete HTTP error is
// not such proof. The owner/version/generation CAS still prevents stale cleanup
// from touching a newer authorization.
func (r *Refresher) clearLeaseAfterDefinitiveFailure(
	ctx context.Context,
	row store.Connection,
	owner uuid.UUID,
	state string,
) error {
	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		refreshCleanupTimeout,
	)
	defer cancel()
	return r.clearLease(cleanupCtx, row, owner, state)
}

func (r *Refresher) handleRefreshFailure(
	ctx context.Context,
	row store.Connection,
	owner uuid.UUID,
	refreshErr error,
) error {
	var endpointErr *oauthsvc.TokenEndpointError
	if !errors.As(refreshErr, &endpointErr) {
		// Construction failed after state=requesting but before an HTTP
		// request could be issued.
		if err := r.clearLeaseAfterDefinitiveFailure(
			ctx,
			row,
			owner,
			stateRequesting,
		); err != nil {
			return err
		}
		return refreshErr
	}
	if endpointErr.InvalidGrant {
		cleanupCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			refreshCleanupTimeout,
		)
		defer cancel()
		requesting := stateRequesting
		affected, err := r.q.MarkRefreshLeaseReauthRequiredCAS(
			cleanupCtx,
			store.MarkRefreshLeaseReauthRequiredCASParams{
				ConnectionID:                    row.ID,
				Owner:                           &owner,
				ExpectedRefreshState:            &requesting,
				ExpectedCredentialVersion:       row.CredentialVersion,
				ExpectedAuthorizationGeneration: row.AuthorizationGeneration,
			},
		)
		if err != nil {
			return err
		}
		if affected != 1 {
			return ErrAuthorizationChanged
		}
		return fmt.Errorf("%w: %s", ErrReauthRequired, endpointErr.Error())
	}
	if !endpointErr.RequestUncertain {
		if err := r.clearLeaseAfterDefinitiveFailure(
			ctx,
			row,
			owner,
			stateRequesting,
		); err != nil {
			return err
		}
	}
	// Uncertain errors intentionally leave refresh_state=requesting. DB time
	// will expire the lease into reauth_required; no caller replays the
	// rotating token.
	return endpointErr
}

func refreshedScopes(
	token oauthsvc.TokenValue,
	previous []string,
	previousKnown bool,
	matcher connector.ScopeMatcher,
) ([]string, bool) {
	if !token.ScopesKnown {
		return cloneNonNil(previous), previousKnown
	}
	protocol := cloneNonNil(token.Scopes)
	if !previousKnown {
		return protocol, true
	}
	// The old set is already the safe token/validator intersection. A refresh
	// may narrow it but cannot expand beyond that prior validator proof.
	out := make([]string, 0, len(protocol))
	for _, scope := range protocol {
		if matcher(previous, scope) {
			out = append(out, scope)
		}
	}
	return out, true
}

func (r *Refresher) scopeMatcher(
	t connector.Type,
	authMethod string,
) connector.ScopeMatcher {
	if byMethod := r.matchers[t]; byMethod != nil {
		if matcher := byMethod[authMethod]; matcher != nil {
			return matcher
		}
	}
	return connector.ExactScopeMatcher
}

func cloneNonNil(values []string) []string {
	if len(values) == 0 {
		return []string{}
	}
	return append([]string(nil), values...)
}
