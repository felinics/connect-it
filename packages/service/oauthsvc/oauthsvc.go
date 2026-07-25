// Package oauthsvc implements OAuth authorization with a single-use,
// version-bound state machine. Provider network I/O never runs inside a
// database transaction.
package oauthsvc

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/credential"
	"github.com/memohai/connect-it/packages/service/internal/credentialcheck"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/svcerr"
)

// CallbackPath is the fixed callback path below CONNECT_IT_BASE_URL.
const CallbackPath = "/v1/oauth/callback"

const (
	authorizationTTL = 10 * time.Minute
	statusPending    = "pending"
	flowInitial      = "initial"
	flowReauth       = "reauth"

	// A callback may claim just before authorizationTTL expires. The token
	// exchange and credential validator are both bounded to at most 30
	// seconds in production, so the DB-time janitor waits for a wider window
	// before recovering a claimed attempt whose process may have crashed.
	claimedRecoveryGrace        = 2 * time.Minute
	claimedRecoveryGraceSeconds = int32(claimedRecoveryGrace / time.Second)
)

var (
	ErrUnknownConnector      = svcerr.New(svcerr.NotFound, "oauthsvc: 未知 connector type")
	ErrUnknownAuthMethod     = svcerr.New(svcerr.Invalid, "oauthsvc: 未知 auth method")
	ErrNotOAuth              = svcerr.New(svcerr.Invalid, "oauthsvc: auth method 不是 oauth2")
	ErrMissingClient         = svcerr.New(svcerr.Invalid, "oauthsvc: 配置缺少 client_id / client_secret")
	ErrConnectionGone        = svcerr.New(svcerr.NotFound, "oauthsvc: connection 不存在")
	ErrAuthorizationConflict = svcerr.New(svcerr.ConnectionConflict, "oauthsvc: authorization 已被更新的授权取代")
	ErrEgressPolicy          = svcerr.New(svcerr.EgressRejected, "oauthsvc: OAuth endpoint 被出站策略拒绝")
	// ErrInvalidState 不进对外词汇表：回调失败一律走固定的重定向文案。
	ErrInvalidState = errors.New("oauthsvc: state 无效、已过期或已使用")
)

// BeginResult contains the durable pending Connection ID and browser URL.
type BeginResult struct {
	ConnectionID     uuid.UUID
	AuthorizationURL string
}

// CallbackResult always carries the previously registered redirect when the
// non-consuming state lookup succeeded, even if a later claim fails.
type CallbackResult struct {
	ConnectionID uuid.UUID
	RedirectURL  string
}

type BeginInput struct {
	ConnectorType connector.Type
	AuthMethodKey string
	Alias         string
	RedirectURL   string
}

type ReauthInput struct {
	ConnectionID uuid.UUID
	RedirectURL  string
}

type Service struct {
	q             *store.Queries
	reg           *registry.Registry
	cfg           *configsvc.Service
	kr            *crypto.Keyring
	factory       *providerkit.Factory
	baseURL       string
	authorization connector.AuthorizationRuntime
}

func New(
	q *store.Queries,
	reg *registry.Registry,
	cfg *configsvc.Service,
	kr *crypto.Keyring,
	factory *providerkit.Factory,
	baseURL string,
	runtime connector.AuthorizationRuntime,
) *Service {
	return &Service{
		q:             q,
		reg:           reg,
		cfg:           cfg,
		kr:            kr,
		factory:       factory,
		baseURL:       strings.TrimRight(baseURL, "/"),
		authorization: runtime,
	}
}

func (s *Service) BeginWithInput(
	ctx context.Context,
	input BeginInput,
) (BeginResult, error) {
	if input.Alias != "" && !connsvc.AliasPattern.MatchString(input.Alias) {
		return BeginResult{}, connsvc.ErrInvalidAlias
	}
	def, method, err := s.authorizationMethod(
		input.ConnectorType,
		input.AuthMethodKey,
	)
	if err != nil {
		return BeginResult{}, err
	}
	material, err := s.prepareAuthorization(ctx, def, method, 1, 1)
	if err != nil {
		return BeginResult{}, err
	}

	connectionID := uuid.New()
	emptyCredential, connectionKeyVersion, err := s.kr.Encrypt(
		nil,
		[]byte(connectionID.String()),
	)
	if err != nil {
		return BeginResult{}, err
	}
	var aliasPtr *string
	if input.Alias != "" {
		aliasPtr = &input.Alias
	}
	requestedScopes, err := connector.NormalizeScopes(method.OAuth.Scopes)
	if err != nil {
		return BeginResult{}, err
	}
	row, err := s.q.BeginInitialOAuthAuthorizationAtPolicyIdentity(
		ctx,
		store.BeginInitialOAuthAuthorizationAtPolicyIdentityParams{
			ExpectedIdentityVersion:       material.policy.IdentityVersion,
			ExpectedIdentityDigest:        material.policy.IdentityDigest,
			ExpectedDefinitionDigest:      material.policy.DefinitionDigest,
			ConnectionID:                  connectionID,
			ConnectorType:                 string(input.ConnectorType),
			ConnectionAlias:               aliasPtr,
			AuthMethod:                    method.Key,
			EmptyCredential:               emptyCredential,
			ConnectionSecretKeyVersion:    int32(connectionKeyVersion),
			AuthorizationID:               material.id,
			StateHash:                     hashToken(material.state),
			ContextCiphertext:             material.encryptedContext,
			ExpiresAt:                     material.expiresAt,
			AuthorizationSecretKeyVersion: int32(material.keyVersion),
			RedirectUrl:                   input.RedirectURL,
			RequestedScopes:               requestedScopes,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return BeginResult{}, ErrAuthorizationConflict
	}
	if err != nil {
		return BeginResult{}, err
	}
	if row.ConnectionID != connectionID ||
		row.AuthorizationID != material.id ||
		row.AttemptVersion != 1 ||
		row.ExpectedAuthorizationGeneration != 1 {
		return BeginResult{}, errors.New(
			"oauthsvc: initial authorization returned an invalid version binding",
		)
	}
	return BeginResult{
		ConnectionID:     connectionID,
		AuthorizationURL: material.authorizationURL,
	}, nil
}

func (s *Service) BeginReauthWithInput(
	ctx context.Context,
	input ReauthInput,
) (BeginResult, error) {
	row, err := s.q.GetConnection(ctx, input.ConnectionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return BeginResult{}, ErrConnectionGone
		}
		return BeginResult{}, err
	}
	def, method, err := s.authorizationMethod(
		connector.Type(row.ConnectorType),
		row.AuthMethod,
	)
	if err != nil {
		return BeginResult{}, err
	}
	nextAttempt := row.AuthorizationAttemptVersion + 1
	nextGeneration := row.AuthorizationGeneration + 1
	requestedScopes, err := connector.NormalizeScopes(method.OAuth.Scopes)
	if err != nil {
		return BeginResult{}, err
	}
	material, err := s.prepareAuthorization(
		ctx,
		def,
		method,
		nextAttempt,
		nextGeneration,
	)
	if err != nil {
		return BeginResult{}, err
	}
	bound, err := s.q.BeginOAuthReauthorization(
		ctx,
		store.BeginOAuthReauthorizationParams{
			ConnectionID:                           input.ConnectionID,
			ExpectedCurrentAttemptVersion:          row.AuthorizationAttemptVersion,
			ExpectedCurrentAuthorizationGeneration: row.AuthorizationGeneration,
			AuthorizationID:                        material.id,
			StateHash:                              hashToken(material.state),
			ContextCiphertext:                      material.encryptedContext,
			ExpiresAt:                              material.expiresAt,
			AuthorizationSecretKeyVersion:          int32(material.keyVersion),
			RedirectUrl:                            input.RedirectURL,
			RequestedScopes:                        requestedScopes,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return BeginResult{}, ErrAuthorizationConflict
	}
	if err != nil {
		return BeginResult{}, err
	}
	if bound.ConnectionID != input.ConnectionID ||
		bound.AuthorizationID != material.id ||
		bound.AttemptVersion != nextAttempt ||
		bound.ExpectedAuthorizationGeneration != nextGeneration {
		return BeginResult{}, errors.New(
			"oauthsvc: reauthorization returned an invalid version binding",
		)
	}
	return BeginResult{
		ConnectionID:     input.ConnectionID,
		AuthorizationURL: material.authorizationURL,
	}, nil
}

type authorizationMaterial struct {
	id               uuid.UUID
	state            string
	authorizationURL string
	encryptedContext []byte
	keyVersion       int
	expiresAt        time.Time
	policy           configsvc.PolicySnapshot
}

func (s *Service) prepareAuthorization(
	ctx context.Context,
	def connector.Definition,
	method connector.AuthMethod,
	attemptVersion int64,
	expectedGeneration int64,
) (authorizationMaterial, error) {
	resolved, policy, err := s.cfg.ResolvedWithPolicy(ctx, def.Type)
	if err != nil {
		return authorizationMaterial{}, err
	}
	clientID, _ := resolved["client_id"].(string)
	if clientID == "" {
		return authorizationMaterial{}, ErrMissingClient
	}
	oauthConfig := *method.OAuth
	if err := ExpandEndpoints(&oauthConfig, resolved); err != nil {
		return authorizationMaterial{}, err
	}
	authorizationURL, err := validateOAuthEndpoint(
		oauthConfig.AuthorizationEndpoint,
		oauthConfig.Egress.AuthorizationOrigins,
	)
	if err != nil {
		return authorizationMaterial{}, ErrEgressPolicy
	}
	state, err := randomToken()
	if err != nil {
		return authorizationMaterial{}, err
	}
	verifier := ""
	if method.OAuth.UsePKCE {
		verifier, err = randomToken()
		if err != nil {
			return authorizationMaterial{}, err
		}
	}
	authorizationID := uuid.New()
	contextCiphertext, keyVersion, err := encryptAuthorizationContext(
		s.kr,
		authorizationID,
		authorizationContext{
			Version:                         authorizationContextVersion,
			ConnectorType:                   def.Type,
			AuthMethodKey:                   method.Key,
			StateHash:                       hashToken(state),
			AttemptVersion:                  attemptVersion,
			ExpectedAuthorizationGeneration: expectedGeneration,
			PKCEVerifier:                    verifier,
		},
	)
	if err != nil {
		return authorizationMaterial{}, err
	}

	params := authorizationURL.Query()
	for key, value := range oauthConfig.ExtraAuthParams {
		params.Set(key, value)
	}
	// Core protocol parameters are written last so ExtraAuthParams can never
	// replace state, callback identity, PKCE or the requested scope.
	params.Set("response_type", "code")
	params.Set("client_id", clientID)
	params.Set("redirect_uri", s.baseURL+CallbackPath)
	params.Set("state", state)
	if len(oauthConfig.Scopes) > 0 {
		params.Set(
			"scope",
			joinScopes(
				oauthConfig.Scopes,
				oauthConfig.EffectiveAuthorizationScopeSeparator(),
			),
		)
	}
	if oauthConfig.UsePKCE {
		params.Set("code_challenge", s256Challenge(verifier))
		params.Set("code_challenge_method", "S256")
	}
	authorizationURL.RawQuery = params.Encode()
	return authorizationMaterial{
		id:               authorizationID,
		state:            state,
		authorizationURL: authorizationURL.String(),
		encryptedContext: contextCiphertext,
		keyVersion:       keyVersion,
		expiresAt:        time.Now().Add(authorizationTTL),
		policy:           policy,
	}, nil
}

func joinScopes(
	scopes []string,
	separator connector.OAuthScopeSeparator,
) string {
	if separator == connector.OAuthScopeComma {
		return strings.Join(scopes, ",")
	}
	return strings.Join(scopes, " ")
}

// HandleCallback performs a non-consuming context read and local preparation,
// then atomically claims the exact current attempt. Only the claim winner may
// contact the token endpoint or validator.
type CallbackInput struct {
	State string
	Code  string
}

func (s *Service) HandleCallbackWithInput(
	ctx context.Context,
	input CallbackInput,
) (CallbackResult, error) {
	state, code := input.State, input.Code
	if state == "" || code == "" {
		return CallbackResult{}, ErrInvalidState
	}
	prepared, result, err := s.prepareCallback(ctx, state)
	if err != nil {
		return result, err
	}
	claimed, err := s.claimAttempt(ctx, state, prepared)
	if err != nil {
		return result, err
	}

	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.baseURL+CallbackPath)
	if prepared.method.OAuth.UsePKCE {
		form.Set("code_verifier", prepared.verifier)
	}
	token, err := ExchangeToken(
		ctx,
		s.factory,
		prepared.definition.Type,
		&prepared.oauthConfig,
		prepared.clientID,
		prepared.clientSecret,
		form,
		providerkit.RequestLabels{
			AuthorizationID: claimed.ID.String(),
		},
	)
	if err != nil {
		return result, s.failClaimedAttemptAfter(ctx, claimed, err)
	}

	grantedScopes, err := initialGrantedScopes(token, claimed.RequestedScopes)
	if err != nil {
		return result, s.failClaimedAttemptAfter(ctx, claimed, err)
	}
	snapshot, err := credentialcheck.Validate(
		ctx,
		s.authorization.CredentialValidators,
		s.authorization.ScopeMatchers,
		prepared.definition,
		prepared.method,
		connector.CredentialValidationInput{
			ConnectorType:   prepared.definition.Type,
			AuthMethodKey:   prepared.method.Key,
			AuthType:        prepared.method.Type,
			AuthorizationID: claimed.ID.String(),
			Config:          prepared.resolved,
			AccessToken:     token.AccessToken,
			TokenType:       token.TokenType,
		},
		grantedScopes,
		// The standard grant always establishes a known protocol scope set.
		true,
	)
	if err != nil {
		return result, s.failClaimedAttemptAfter(ctx, claimed, err)
	}

	now := time.Now()
	refreshToken := ""
	if token.RefreshToken != nil {
		refreshToken = *token.RefreshToken
	}
	oauthCredential := credential.OAuth{
		AccessToken:  token.AccessToken,
		TokenType:    token.TokenType,
		RefreshToken: refreshToken,
	}
	if token.ExpiresInSeconds != nil {
		oauthCredential.ExpiresAt = now.Add(
			time.Duration(*token.ExpiresInSeconds) * time.Second,
		)
	}
	plaintext, err := oauthCredential.Marshal()
	if err != nil {
		return result, s.failClaimedAttemptAfter(ctx, claimed, err)
	}
	ciphertext, keyVersion, err := s.kr.Encrypt(
		plaintext,
		[]byte(claimed.ConnectionID.String()),
	)
	if err != nil {
		return result, s.failClaimedAttemptAfter(ctx, claimed, err)
	}
	var expiresAt *time.Time
	if !oauthCredential.ExpiresAt.IsZero() {
		expiresAt = &oauthCredential.ExpiresAt
	}
	completed, err := s.q.CompleteOAuthAuthorizationCAS(
		ctx,
		store.CompleteOAuthAuthorizationCASParams{
			AuthorizationID:      claimed.ID,
			Credential:           ciphertext,
			SecretKeyVersion:     int32(keyVersion),
			Profile:              snapshot.Profile,
			Scopes:               snapshot.Scopes,
			ScopesKnown:          snapshot.ScopesKnown,
			AccessTokenExpiresAt: expiresAt,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if maintenanceErr := s.MaintainAuthorizations(ctx); maintenanceErr != nil {
			return result, errors.Join(
				ErrAuthorizationConflict,
				fmt.Errorf(
					"oauthsvc: maintain conflicted authorization: %w",
					maintenanceErr,
				),
			)
		}
		return result, ErrAuthorizationConflict
	}
	if err != nil {
		return result, s.failClaimedAttemptAfter(ctx, claimed, err)
	}
	if completed.ConnectionID != claimed.ConnectionID {
		return result, errors.New(
			"oauthsvc: completion returned a different connection",
		)
	}
	return result, nil
}

// HandleProviderError consumes a valid current attempt without contacting the
// token endpoint. It is used for provider-side denial/error callbacks.
type ProviderErrorInput struct {
	State string
}

func (s *Service) HandleProviderErrorWithInput(
	ctx context.Context,
	input ProviderErrorInput,
) (CallbackResult, error) {
	state := input.State
	if state == "" {
		return CallbackResult{}, ErrInvalidState
	}
	prepared, result, err := s.prepareCallback(ctx, state)
	if err != nil {
		return result, err
	}
	claimed, err := s.claimAttempt(ctx, state, prepared)
	if err != nil {
		return result, err
	}
	if err := s.failClaimedAttempt(ctx, claimed); err != nil {
		return result, err
	}
	return result, nil
}

// MaintainAuthorizations recovers expired, abandoned, and superseded OAuth
// attempts. Every decision uses database time and exact attempt/generation
// predicates. A claimed callback receives claimedRecoveryGrace beyond its
// authorization expiry so maintenance cannot invalidate a legitimate
// callback which claimed just before the deadline.
//
// All statements are attempted independently. The joined error lets callers
// observe partial maintenance failures while a later run safely retries them.
func (s *Service) MaintainAuthorizations(ctx context.Context) error {
	var maintenanceErrors []error
	run := func(operation string, fn func() error) {
		if err := fn(); err != nil {
			maintenanceErrors = append(
				maintenanceErrors,
				fmt.Errorf("oauthsvc: %s: %w", operation, err),
			)
		}
	}
	run("cleanup expired initial authorizations", func() error {
		_, err := s.q.CleanupExpiredInitialOAuthAuthorizations(ctx)
		return err
	})
	run("recover stale claimed initial authorizations", func() error {
		_, err := s.q.CleanupStaleClaimedInitialOAuthAuthorizations(
			ctx,
			claimedRecoveryGraceSeconds,
		)
		return err
	})
	run("expire reauthorization attempts", func() error {
		_, err := s.q.ExpireReauthOAuthAuthorizations(ctx)
		return err
	})
	run("recover stale claimed reauthorization attempts", func() error {
		_, err := s.q.ExpireStaleClaimedReauthOAuthAuthorizations(
			ctx,
			claimedRecoveryGraceSeconds,
		)
		return err
	})
	run("mark superseded authorizations", func() error {
		_, err := s.q.MarkSupersededOAuthAuthorizations(ctx)
		return err
	})
	run("cleanup superseded initial authorizations", func() error {
		_, err := s.q.CleanupSupersededInitialOAuthAuthorizations(ctx)
		return err
	})
	return errors.Join(maintenanceErrors...)
}

type preparedCallback struct {
	authorization store.OauthAuthorization
	definition    connector.Definition
	method        connector.AuthMethod
	resolved      map[string]any
	clientID      string
	clientSecret  string
	verifier      string
	oauthConfig   connector.OAuthConfig
}

func (s *Service) prepareCallback(
	ctx context.Context,
	state string,
) (preparedCallback, CallbackResult, error) {
	authz, result, err := s.authorizationRow(ctx, state)
	if err != nil {
		return preparedCallback{}, result, err
	}
	boundContext, err := decryptAuthorizationContext(s.kr, authz)
	if err != nil {
		return preparedCallback{}, result, ErrInvalidState
	}
	def, method, err := s.authorizationMethod(
		connector.Type(authz.ConnectorType),
		authz.AuthMethod,
	)
	if err != nil {
		return preparedCallback{}, result, err
	}
	resolved, err := s.cfg.Resolved(ctx, def.Type)
	if err != nil {
		return preparedCallback{}, result, err
	}
	clientID, _ := resolved["client_id"].(string)
	clientSecret, _ := resolved["client_secret"].(string)
	if clientID == "" {
		return preparedCallback{}, result, ErrMissingClient
	}
	oauthConfig := *method.OAuth
	if err := ExpandEndpoints(&oauthConfig, resolved); err != nil {
		return preparedCallback{}, result, err
	}
	if s.authorization.CredentialValidators[def.Type][method.Key] == nil {
		return preparedCallback{}, result, fmt.Errorf(
			"%w for connector %q auth method %q",
			credentialcheck.ErrValidatorMissing,
			def.Type,
			method.Key,
		)
	}
	return preparedCallback{
		authorization: authz,
		definition:    def,
		method:        method,
		resolved:      resolved,
		clientID:      clientID,
		clientSecret:  clientSecret,
		verifier:      boundContext.PKCEVerifier,
		oauthConfig:   oauthConfig,
	}, result, nil
}

// claimAttempt atomically consumes the exact attempt that prepareCallback read.
// Only the claim winner may contact the token endpoint or the validator; a
// claim that no longer matches the prepared attempt is failed immediately.
func (s *Service) claimAttempt(
	ctx context.Context,
	state string,
	prepared preparedCallback,
) (store.OauthAuthorization, error) {
	claimed, err := s.q.ClaimOAuthAuthorization(ctx, hashToken(state))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.OauthAuthorization{}, ErrInvalidState
	}
	if err != nil {
		return store.OauthAuthorization{}, err
	}
	if claimed.ID != prepared.authorization.ID ||
		claimed.ConnectionID != prepared.authorization.ConnectionID ||
		claimed.AttemptVersion != prepared.authorization.AttemptVersion ||
		claimed.ExpectedAuthorizationGeneration !=
			prepared.authorization.ExpectedAuthorizationGeneration {
		return store.OauthAuthorization{}, s.failClaimedAttemptAfter(
			ctx,
			claimed,
			ErrAuthorizationConflict,
		)
	}
	return claimed, nil
}

func (s *Service) authorizationRow(
	ctx context.Context,
	state string,
) (store.OauthAuthorization, CallbackResult, error) {
	authz, err := s.q.GetOAuthAuthorizationByStateHash(
		ctx,
		hashToken(state),
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return store.OauthAuthorization{}, CallbackResult{}, ErrInvalidState
	}
	if err != nil {
		return store.OauthAuthorization{}, CallbackResult{}, err
	}
	result := CallbackResult{
		ConnectionID: authz.ConnectionID,
		RedirectURL:  authz.RedirectUrl,
	}
	if authz.Status != statusPending {
		return authz, result, ErrInvalidState
	}
	return authz, result, nil
}

func (s *Service) failClaimedAttempt(
	ctx context.Context,
	authz store.OauthAuthorization,
) error {
	var (
		rows int64
		err  error
	)
	switch authz.FlowKind {
	case flowInitial:
		rows, err = s.q.CleanupInitialOAuthAuthorization(ctx, authz.ID)
	case flowReauth:
		rows, err = s.q.FailReauthOAuthAuthorization(ctx, authz.ID)
	default:
		return errors.New("oauthsvc: authorization has invalid flow kind")
	}
	if err != nil {
		return err
	}
	if rows == 0 {
		// The attempt is already gone or superseded. Let the janitor decide
		// what the current state is instead of guessing here.
		return errors.Join(
			ErrAuthorizationConflict,
			s.MaintainAuthorizations(ctx),
		)
	}
	return nil
}

func (s *Service) failClaimedAttemptAfter(
	ctx context.Context,
	authz store.OauthAuthorization,
	cause error,
) error {
	if cleanupErr := s.failClaimedAttempt(ctx, authz); cleanupErr != nil {
		return errors.Join(
			cause,
			fmt.Errorf(
				"oauthsvc: cleanup claimed authorization: %w",
				cleanupErr,
			),
		)
	}
	return cause
}

// authorizationMethod resolves an auth method that is guaranteed to be OAuth2
// with a non-nil OAuth config; every other shape is rejected here.
func (s *Service) authorizationMethod(
	t connector.Type,
	key string,
) (connector.Definition, connector.AuthMethod, error) {
	def, ok := s.reg.Get(t)
	if !ok {
		return connector.Definition{}, connector.AuthMethod{},
			fmt.Errorf("%w: %s", ErrUnknownConnector, t)
	}
	for _, method := range def.AuthMethods {
		if method.Key != key {
			continue
		}
		if method.Type == connector.AuthOAuth2 && method.OAuth != nil {
			return def, method, nil
		}
		return connector.Definition{}, connector.AuthMethod{},
			fmt.Errorf("%w: %s", ErrNotOAuth, key)
	}
	return connector.Definition{}, connector.AuthMethod{},
		fmt.Errorf("%w: %s", ErrUnknownAuthMethod, key)
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func s256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
