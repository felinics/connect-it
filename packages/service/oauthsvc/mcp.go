package oauthsvc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/service/store"
)

const (
	oauthMetadataLimit = 1 << 20
	mcpOAuthTimeout    = 30 * time.Second
)

var (
	ErrMCPDiscovery     = errors.New("oauthsvc: MCP OAuth discovery failed")
	ErrMCPRegistration  = errors.New("oauthsvc: MCP OAuth client registration failed")
	ErrMCPClientExpired = errors.New("oauthsvc: MCP OAuth client registration has expired")
)

type mcpOAuthDiscovery struct {
	Resource              string
	Issuer                string
	AuthorizationEndpoint string
	TokenEndpoint         string
	RegistrationEndpoint  string
	Scopes                []string
	TokenAuthMethods      []string
}

// ResolvedMCPClient is a persisted OAuth client registration used by callback
// exchange and refresh. ClientSecret is decrypted only in memory.
type ResolvedMCPClient struct {
	ID                uuid.UUID
	ConnectorType     connector.Type
	Resource          string
	ClientID          string
	ClientSecret      string
	TokenEndpoint     string
	TokenEndpointAuth connector.TokenEndpointAuth
}

func (s *Service) resolveMCPClient(
	ctx context.Context,
	t connector.Type,
	endpoint string,
) (mcpOAuthDiscovery, store.OauthClient, error) {
	ctx, cancel := context.WithTimeout(ctx, mcpOAuthTimeout)
	defer cancel()

	discovery, err := discoverMCPOAuth(ctx, noRedirectClient(s.hc), endpoint)
	if err != nil {
		return mcpOAuthDiscovery{}, store.OauthClient{},
			fmt.Errorf("%w: %v", ErrMCPDiscovery, err)
	}

	redirectURI := s.baseURL + CallbackPath
	lockKey := strings.Join([]string{
		string(t), discovery.Resource, discovery.Issuer, redirectURI,
	}, "\n")
	tx, qtx, err := s.q.BeginTx(ctx)
	if err != nil {
		return mcpOAuthDiscovery{}, store.OauthClient{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := qtx.LockOAuthClientRegistration(ctx, lockKey); err != nil {
		return mcpOAuthDiscovery{}, store.OauthClient{}, err
	}

	key := store.GetOAuthClientByRegistrationParams{
		ConnectorType: string(t),
		Resource:      discovery.Resource,
		Issuer:        discovery.Issuer,
		RedirectUri:   redirectURI,
	}
	row, err := qtx.GetOAuthClientByRegistration(ctx, key)
	switch {
	case err == nil && oauthClientUsable(row):
		if row.TokenEndpoint != discovery.TokenEndpoint {
			row, err = qtx.UpdateOAuthClientEndpoint(ctx, store.UpdateOAuthClientEndpointParams{
				ID: row.ID, TokenEndpoint: discovery.TokenEndpoint,
			})
			if err != nil {
				return mcpOAuthDiscovery{}, store.OauthClient{}, err
			}
		}
	case err == nil || errors.Is(err, pgx.ErrNoRows):
		row, err = s.registerMCPClient(ctx, qtx, t, discovery, redirectURI, row)
		if err != nil {
			return mcpOAuthDiscovery{}, store.OauthClient{}, err
		}
	default:
		return mcpOAuthDiscovery{}, store.OauthClient{}, err
	}
	if _, err := LoadMCPClient(ctx, qtx, s.kr, row.ID); err != nil {
		return mcpOAuthDiscovery{}, store.OauthClient{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return mcpOAuthDiscovery{}, store.OauthClient{}, err
	}
	return discovery, row, nil
}

func (s *Service) registerMCPClient(
	ctx context.Context,
	q *store.Queries,
	t connector.Type,
	discovery mcpOAuthDiscovery,
	redirectURI string,
	existing store.OauthClient,
) (store.OauthClient, error) {
	requestedAuth := preferredTokenAuth(discovery.TokenAuthMethods)
	response, err := oauthex.RegisterClient(
		ctx,
		discovery.RegistrationEndpoint,
		&oauthex.ClientRegistrationMetadata{
			RedirectURIs:            []string{redirectURI},
			TokenEndpointAuthMethod: string(requestedAuth),
			GrantTypes:              []string{"authorization_code", "refresh_token"},
			ResponseTypes:           []string{"code"},
			ClientName:              "connect-it",
			Scope:                   strings.Join(discovery.Scopes, " "),
		},
		noRedirectClient(s.hc),
	)
	if err != nil {
		return store.OauthClient{}, fmt.Errorf("%w: %v", ErrMCPRegistration, err)
	}

	tokenAuth := connector.TokenEndpointAuth(response.TokenEndpointAuthMethod)
	if tokenAuth == "" {
		tokenAuth = requestedAuth
	}
	if err := validateTokenAuth(tokenAuth, response.ClientSecret); err != nil {
		return store.OauthClient{}, fmt.Errorf("%w: %v", ErrMCPRegistration, err)
	}

	id := existing.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	ciphertext, keyVersion, err := s.kr.Encrypt(
		[]byte(response.ClientSecret),
		[]byte(id.String()),
	)
	if err != nil {
		return store.OauthClient{}, err
	}
	var secretExpiresAt *time.Time
	if !response.ClientSecretExpiresAt.IsZero() {
		expiresAt := response.ClientSecretExpiresAt
		secretExpiresAt = &expiresAt
	}

	if existing.ID == uuid.Nil {
		return q.CreateOAuthClient(ctx, store.CreateOAuthClientParams{
			ID:                      id,
			ConnectorType:           string(t),
			Resource:                discovery.Resource,
			Issuer:                  discovery.Issuer,
			RedirectUri:             redirectURI,
			ClientID:                response.ClientID,
			ClientSecret:            ciphertext,
			SecretKeyVersion:        int32(keyVersion),
			TokenEndpoint:           discovery.TokenEndpoint,
			TokenEndpointAuthMethod: string(tokenAuth),
			ClientSecretExpiresAt:   secretExpiresAt,
		})
	}
	return q.ReplaceOAuthClientRegistration(ctx, store.ReplaceOAuthClientRegistrationParams{
		ID:                      id,
		ClientID:                response.ClientID,
		ClientSecret:            ciphertext,
		SecretKeyVersion:        int32(keyVersion),
		TokenEndpoint:           discovery.TokenEndpoint,
		TokenEndpointAuthMethod: string(tokenAuth),
		ClientSecretExpiresAt:   secretExpiresAt,
	})
}

func oauthClientUsable(row store.OauthClient) bool {
	if row.ID == uuid.Nil || row.ClientID == "" {
		return false
	}
	if row.ClientSecretExpiresAt != nil &&
		time.Until(*row.ClientSecretExpiresAt) <= time.Minute {
		return false
	}
	tokenAuth := connector.TokenEndpointAuth(row.TokenEndpointAuthMethod)
	if tokenAuth != connector.TokenAuthNone &&
		tokenAuth != connector.TokenAuthPost &&
		tokenAuth != connector.TokenAuthBasic {
		return false
	}
	return true
}

func preferredTokenAuth(supported []string) connector.TokenEndpointAuth {
	for _, method := range []connector.TokenEndpointAuth{
		connector.TokenAuthNone,
		connector.TokenAuthPost,
		connector.TokenAuthBasic,
	} {
		if slices.Contains(supported, string(method)) {
			return method
		}
	}
	// Public clients are the MCP default. A server can reject this during DCR
	// if it requires a confidential client.
	return connector.TokenAuthNone
}

func validateTokenAuth(method connector.TokenEndpointAuth, secret string) error {
	switch method {
	case connector.TokenAuthNone:
		return nil
	case connector.TokenAuthBasic, connector.TokenAuthPost:
		if secret == "" {
			return fmt.Errorf("token auth method %q has no client_secret", method)
		}
		return nil
	default:
		return fmt.Errorf("unsupported token auth method %q", method)
	}
}

// LoadMCPClient loads and decrypts one native MCP OAuth registration.
func LoadMCPClient(
	ctx context.Context,
	q *store.Queries,
	kr *crypto.Keyring,
	id uuid.UUID,
) (ResolvedMCPClient, error) {
	row, err := q.GetOAuthClient(ctx, id)
	if err != nil {
		return ResolvedMCPClient{}, err
	}
	if row.ClientSecretExpiresAt != nil && !time.Now().Before(*row.ClientSecretExpiresAt) {
		return ResolvedMCPClient{}, ErrMCPClientExpired
	}
	secret, err := kr.Decrypt(
		row.ClientSecret,
		int(row.SecretKeyVersion),
		[]byte(row.ID.String()),
	)
	if err != nil {
		return ResolvedMCPClient{}, err
	}
	tokenAuth := connector.TokenEndpointAuth(row.TokenEndpointAuthMethod)
	if err := validateTokenAuth(tokenAuth, string(secret)); err != nil {
		return ResolvedMCPClient{}, err
	}
	return ResolvedMCPClient{
		ID:                row.ID,
		ConnectorType:     connector.Type(row.ConnectorType),
		Resource:          row.Resource,
		ClientID:          row.ClientID,
		ClientSecret:      string(secret),
		TokenEndpoint:     row.TokenEndpoint,
		TokenEndpointAuth: tokenAuth,
	}, nil
}

func discoverMCPOAuth(
	ctx context.Context,
	hc *http.Client,
	endpoint string,
) (mcpOAuthDiscovery, error) {
	metadataURL, challengeScopes, err := probeMCPAuthorization(ctx, hc, endpoint)
	if err != nil {
		return mcpOAuthDiscovery{}, err
	}
	prm, err := discoverProtectedResource(ctx, hc, metadataURL, endpoint)
	if err != nil {
		return mcpOAuthDiscovery{}, err
	}
	if len(prm.AuthorizationServers) == 0 {
		return mcpOAuthDiscovery{}, errors.New("protected resource metadata has no authorization_servers")
	}
	issuer := prm.AuthorizationServers[0]
	metadata, err := mcpauth.GetAuthServerMetadata(ctx, issuer, hc)
	if err != nil {
		return mcpOAuthDiscovery{}, err
	}
	if metadata == nil {
		return mcpOAuthDiscovery{}, errors.New("authorization server metadata not found")
	}
	if metadata.AuthorizationEndpoint == "" || metadata.TokenEndpoint == "" {
		return mcpOAuthDiscovery{}, errors.New("authorization server metadata has no OAuth endpoint")
	}
	if metadata.RegistrationEndpoint == "" {
		return mcpOAuthDiscovery{}, errors.New("authorization server does not support dynamic client registration")
	}
	if !slices.Contains(metadata.CodeChallengeMethodsSupported, "S256") {
		return mcpOAuthDiscovery{}, errors.New("authorization server does not support S256 PKCE")
	}
	for name, rawURL := range map[string]string{
		"authorization_endpoint": metadata.AuthorizationEndpoint,
		"token_endpoint":         metadata.TokenEndpoint,
		"registration_endpoint":  metadata.RegistrationEndpoint,
	} {
		if err := validateSecureURL(rawURL); err != nil {
			return mcpOAuthDiscovery{}, fmt.Errorf("%s: %w", name, err)
		}
	}

	scopes := challengeScopes
	if len(scopes) == 0 {
		scopes = prm.ScopesSupported
	}
	return mcpOAuthDiscovery{
		Resource:              prm.Resource,
		Issuer:                issuer,
		AuthorizationEndpoint: metadata.AuthorizationEndpoint,
		TokenEndpoint:         metadata.TokenEndpoint,
		RegistrationEndpoint:  metadata.RegistrationEndpoint,
		Scopes:                scopes,
		TokenAuthMethods:      metadata.TokenEndpointAuthMethodsSupported,
	}, nil
}

func probeMCPAuthorization(
	ctx context.Context,
	hc *http.Client,
	endpoint string,
) (metadataURL string, scopes []string, err error) {
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"connect-it","version":"0.1.0"}}}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := hc.Do(req)
	if err != nil {
		// Discovery can still proceed through the mandatory well-known paths.
		return "", nil, nil
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, oauthMetadataLimit))

	headers := resp.Header.Values("WWW-Authenticate")
	if len(headers) == 0 {
		return "", nil, nil
	}
	challenges, err := oauthex.ParseWWWAuthenticate(headers)
	if err != nil {
		return "", nil, fmt.Errorf("parse WWW-Authenticate: %w", err)
	}
	for _, challenge := range challenges {
		if challenge.Scheme != "bearer" {
			continue
		}
		if metadataURL == "" {
			metadataURL = challenge.Params["resource_metadata"]
		}
		if scope := challenge.Params["scope"]; scope != "" {
			scopes = strings.Fields(scope)
		}
	}
	return metadataURL, scopes, nil
}

type protectedResource struct {
	Resource             string
	AuthorizationServers []string
	ScopesSupported      []string
}

type protectedResourceURL struct {
	URL      string
	Resource string
}

func discoverProtectedResource(
	ctx context.Context,
	hc *http.Client,
	metadataURL string,
	endpoint string,
) (protectedResource, error) {
	var lastErr error
	for _, candidate := range protectedResourceURLs(metadataURL, endpoint) {
		prm, err := getProtectedResource(ctx, hc, candidate)
		if err == nil {
			return prm, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("no usable protected resource metadata URL")
	}
	return protectedResource{}, lastErr
}

func protectedResourceURLs(metadataURL, endpoint string) []protectedResourceURL {
	var candidates []protectedResourceURL
	seen := map[string]bool{}
	add := func(rawURL, resource string) {
		if rawURL != "" && !seen[rawURL] {
			seen[rawURL] = true
			candidates = append(candidates, protectedResourceURL{
				URL: rawURL, Resource: resource,
			})
		}
	}
	add(metadataURL, endpoint)

	resourceURL, err := url.Parse(endpoint)
	if err != nil {
		return candidates
	}
	metadata := *resourceURL
	metadata.RawQuery = ""
	metadata.Fragment = ""
	metadata.Path = "/.well-known/oauth-protected-resource/" +
		strings.TrimLeft(resourceURL.Path, "/")
	metadata.RawPath = ""
	add(metadata.String(), endpoint)

	metadata.Path = "/.well-known/oauth-protected-resource"
	rootResource := *resourceURL
	rootResource.Path = ""
	rootResource.RawPath = ""
	rootResource.RawQuery = ""
	rootResource.Fragment = ""
	add(metadata.String(), rootResource.String())
	return candidates
}

func getProtectedResource(
	ctx context.Context,
	hc *http.Client,
	candidate protectedResourceURL,
) (protectedResource, error) {
	if err := validateSecureURL(candidate.URL); err != nil {
		return protectedResource{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, candidate.URL, nil)
	if err != nil {
		return protectedResource{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return protectedResource{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return protectedResource{}, fmt.Errorf("protected resource metadata returned %d", resp.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return protectedResource{}, fmt.Errorf(
			"invalid protected resource metadata Content-Type %q",
			resp.Header.Get("Content-Type"),
		)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, oauthMetadataLimit+1))
	if err != nil {
		return protectedResource{}, err
	}
	if len(data) > oauthMetadataLimit {
		return protectedResource{}, errors.New("protected resource metadata is too large")
	}
	var wire struct {
		Resource             json.RawMessage `json:"resource"`
		AuthorizationServers []string        `json:"authorization_servers"`
		ScopesSupported      []string        `json:"scopes_supported"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return protectedResource{}, fmt.Errorf("parse protected resource metadata: %w", err)
	}
	resource, err := selectResource(wire.Resource, candidate.Resource)
	if err != nil {
		return protectedResource{}, err
	}
	for _, issuer := range wire.AuthorizationServers {
		if err := validateSecureURL(issuer); err != nil {
			return protectedResource{}, fmt.Errorf("authorization server %q: %w", issuer, err)
		}
	}
	return protectedResource{
		Resource:             resource,
		AuthorizationServers: wire.AuthorizationServers,
		ScopesSupported:      wire.ScopesSupported,
	}, nil
}

// GitLab currently emits resource as a one-element array. Accepting either the
// RFC 9728 string or an array keeps discovery interoperable without weakening
// the exact resource match.
func selectResource(raw json.RawMessage, expected string) (string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		if single != expected {
			return "", fmt.Errorf("metadata resource %q does not match %q", single, expected)
		}
		return single, nil
	}
	var multiple []string
	if err := json.Unmarshal(raw, &multiple); err != nil {
		return "", errors.New("protected resource metadata has an invalid resource")
	}
	if !slices.Contains(multiple, expected) {
		return "", fmt.Errorf("metadata resources do not contain %q", expected)
	}
	return expected, nil
}

func validateSecureURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("invalid URL %q", raw)
	}
	if u.Scheme == "https" {
		return nil
	}
	return fmt.Errorf("URL %q must use HTTPS", raw)
}
