package configsvc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/store"
)

const (
	policyDigestDomain      = "connect-it/connector-policy-identity/v2"
	policyFieldDigestDomain = "connect-it/connector-policy-field/v1"
)

// PolicyReconcileResult is safe to log: it deliberately contains no policy
// values or digests. PreviousKnown=false identifies the one-time conservative
// cutover from a pre-policy-identity installation.
type PolicyReconcileResult struct {
	ConnectorType     connector.Type
	Changed           bool
	PreviousKnown     bool
	DefinitionChanged bool
	ConnectionsBumped int64
}

type policyProjection struct {
	Format           string                 `json:"format"`
	ConnectorType    connector.Type         `json:"connector_type"`
	DefinitionDigest string                 `json:"definition_digest"`
	Fields           []policyProjectedField `json:"fields"`
	RemoteServers    []policyRemoteServer   `json:"remote_servers"`
	OAuthMethods     []policyOAuthMethod    `json:"oauth_methods"`
}

type policyDefinitionProjection struct {
	Format        string                  `json:"format"`
	ConnectorType connector.Type          `json:"connector_type"`
	Fields        []policyDefinitionField `json:"fields"`
	RemoteServers []policyRemoteServer    `json:"remote_servers"`
	OAuthMethods  []policyOAuthMethod     `json:"oauth_methods"`
}

type policyDefinitionField struct {
	Key    string `json:"key"`
	Secret bool   `json:"secret"`
}

type policyProjectedField struct {
	Key         string `json:"key"`
	Present     bool   `json:"present"`
	ValueDigest string `json:"value_digest,omitempty"`
}

type policyRemoteServer struct {
	Key                string                    `json:"key"`
	EndpointSource     string                    `json:"endpoint_source"`
	EndpointField      string                    `json:"endpoint_field,omitempty"`
	EndpointTemplate   string                    `json:"endpoint_template,omitempty"`
	CanonicalURL       string                    `json:"canonical_url,omitempty"`
	CanonicalOrigin    string                    `json:"canonical_origin,omitempty"`
	NetworkMode        string                    `json:"network_mode"`
	AuthScheme         string                    `json:"auth_scheme,omitempty"`
	CredentialBindings []policyCredentialBinding `json:"credential_bindings,omitempty"`
	AllowedHostnames   []string                  `json:"allowed_hostnames,omitempty"`
}

type policyCredentialBinding struct {
	AuthMethod string `json:"auth_method"`
	Field      string `json:"field"`
}

type policyOAuthMethod struct {
	Key                           string             `json:"key"`
	AuthorizationEndpointTemplate string             `json:"authorization_endpoint_template"`
	AuthorizationEndpointURL      string             `json:"authorization_endpoint_url,omitempty"`
	AuthorizationEndpointOrigin   string             `json:"authorization_endpoint_origin,omitempty"`
	AuthorizationOrigins          []string           `json:"authorization_origins"`
	TokenEndpointTemplate         string             `json:"token_endpoint_template"`
	TokenEndpointURL              string             `json:"token_endpoint_url,omitempty"`
	TokenEndpointOrigin           string             `json:"token_endpoint_origin,omitempty"`
	RefreshTokenEndpointTemplate  string             `json:"refresh_token_endpoint_template,omitempty"`
	RefreshTokenEndpointURL       string             `json:"refresh_token_endpoint_url,omitempty"`
	RefreshTokenEndpointOrigin    string             `json:"refresh_token_endpoint_origin,omitempty"`
	TokenOrigins                  []string           `json:"token_origins"`
	Scopes                        []string           `json:"scopes"`
	AuthorizationScopeSeparator   string             `json:"authorization_scope_separator"`
	TokenScopeSeparator           string             `json:"token_scope_separator"`
	UsePKCE                       bool               `json:"use_pkce"`
	TokenEndpointAuth             string             `json:"token_endpoint_auth"`
	TokenRequestFormat            string             `json:"token_request_format"`
	ExtraAuthParams               []policyStringPair `json:"extra_auth_params"`
	ExtraTokenParams              []policyStringPair `json:"extra_token_params"`
}

type policyStringPair struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

type policyDigests struct {
	identity   [sha256.Size]byte
	definition [sha256.Size]byte
	version    int
}

func projectPolicyIdentity(
	def connector.Definition,
	spec registry.ConnectorPolicyIdentitySpec,
	values map[string]string,
) (policyDigests, error) {
	definitionFields := make([]policyDefinitionField, 0, len(spec.Fields))
	projectedFields := make([]policyProjectedField, 0, len(spec.Fields))
	fieldDefinitions := make(map[string]connector.ConfigField, len(def.ConfigFields))
	for _, field := range def.ConfigFields {
		fieldDefinitions[field.Key] = field
	}
	for _, field := range spec.Fields {
		definitionFields = append(definitionFields, policyDefinitionField{
			Key:    field.Key,
			Secret: field.Secret,
		})
		value, present := values[field.Key]
		projected := policyProjectedField{
			Key:     field.Key,
			Present: present,
		}
		if present {
			if declaration, ok := fieldDefinitions[field.Key]; ok &&
				declaration.InputType == connector.InputURL {
				canonicalURL, _, err := canonicalPolicyURL(value)
				if err != nil {
					return policyDigests{}, &ValidationError{
						Field:  field.Key,
						Reason: "URL 不是可用于 policy identity 的绝对 HTTP(S) URL",
					}
				}
				value = canonicalURL
			}
			projected.ValueDigest = digestPolicyField(field.Key, value)
		}
		projectedFields = append(projectedFields, projected)
	}

	staticRemote, resolvedRemote, err := projectRemoteServers(def, values)
	if err != nil {
		return policyDigests{}, err
	}
	staticOAuth, resolvedOAuth, err := projectOAuthMethods(def, values)
	if err != nil {
		return policyDigests{}, err
	}
	definitionProjection := policyDefinitionProjection{
		Format:        policyDigestDomain,
		ConnectorType: def.Type,
		Fields:        definitionFields,
		RemoteServers: staticRemote,
		OAuthMethods:  staticOAuth,
	}
	definitionJSON, err := json.Marshal(definitionProjection)
	if err != nil {
		return policyDigests{}, fmt.Errorf("configsvc: marshal policy definition: %w", err)
	}
	definitionDigest := sha256.Sum256(definitionJSON)

	projection := policyProjection{
		Format:           policyDigestDomain,
		ConnectorType:    def.Type,
		DefinitionDigest: hex.EncodeToString(definitionDigest[:]),
		Fields:           projectedFields,
		RemoteServers:    resolvedRemote,
		OAuthMethods:     resolvedOAuth,
	}
	identityJSON, err := json.Marshal(projection)
	if err != nil {
		return policyDigests{}, fmt.Errorf("configsvc: marshal policy identity: %w", err)
	}
	return policyDigests{
		identity:   sha256.Sum256(identityJSON),
		definition: definitionDigest,
		version:    spec.Version,
	}, nil
}

func projectRemoteServers(
	def connector.Definition,
	values map[string]string,
) ([]policyRemoteServer, []policyRemoteServer, error) {
	static := make([]policyRemoteServer, 0, len(def.RemoteMCPServers))
	resolved := make([]policyRemoteServer, 0, len(def.RemoteMCPServers))
	servers := append([]connector.RemoteMCPServer(nil), def.RemoteMCPServers...)
	sort.Slice(servers, func(i, j int) bool { return servers[i].Key < servers[j].Key })
	for _, server := range servers {
		allowed := append([]string(nil), server.Provenance.AllowedHostnames...)
		sort.Strings(allowed)
		bindingMethods := make(
			[]string,
			0,
			len(server.AuthBinding.CredentialFieldByAuthMethod),
		)
		for method := range server.AuthBinding.CredentialFieldByAuthMethod {
			bindingMethods = append(bindingMethods, method)
		}
		sort.Strings(bindingMethods)
		credentialBindings := make(
			[]policyCredentialBinding,
			0,
			len(bindingMethods),
		)
		for _, method := range bindingMethods {
			credentialBindings = append(
				credentialBindings,
				policyCredentialBinding{
					AuthMethod: method,
					Field:      server.AuthBinding.CredentialFieldByAuthMethod[method],
				},
			)
		}
		networkMode := "public_only"
		if server.Provenance.Kind == connector.ProvenanceSelfHosted {
			networkMode = "self_hosted_opt_in"
		}
		base := policyRemoteServer{
			Key:                server.Key,
			EndpointSource:     string(server.Endpoint.Source),
			EndpointField:      server.Endpoint.ConfigFieldKey,
			NetworkMode:        networkMode,
			AuthScheme:         strings.ToLower(server.AuthBinding.Scheme),
			CredentialBindings: credentialBindings,
			AllowedHostnames:   allowed,
		}
		staticEntry := base
		resolvedEntry := base
		switch server.Endpoint.Source {
		case connector.EndpointFixed:
			staticEntry.EndpointTemplate = server.Endpoint.URL
			canonicalURL, origin, err := canonicalPolicyURL(server.Endpoint.URL)
			if err != nil {
				return nil, nil, fmt.Errorf(
					"configsvc: connector %q MCP server %q has invalid fixed endpoint",
					def.Type,
					server.Key,
				)
			}
			resolvedEntry.CanonicalURL = canonicalURL
			resolvedEntry.CanonicalOrigin = origin
		case connector.EndpointConfigField:
			if endpoint, present := values[server.Endpoint.ConfigFieldKey]; present && endpoint != "" {
				canonicalURL, origin, err := canonicalPolicyURL(endpoint)
				if err != nil {
					return nil, nil, &ValidationError{
						Field:  server.Endpoint.ConfigFieldKey,
						Reason: "URL 不是可用于 Remote MCP 的绝对 HTTP(S) URL",
					}
				}
				resolvedEntry.CanonicalURL = canonicalURL
				resolvedEntry.CanonicalOrigin = origin
			}
		}
		static = append(static, staticEntry)
		resolved = append(resolved, resolvedEntry)
	}
	return static, resolved, nil
}

func projectOAuthMethods(
	def connector.Definition,
	values map[string]string,
) ([]policyOAuthMethod, []policyOAuthMethod, error) {
	methods := append([]connector.AuthMethod(nil), def.AuthMethods...)
	sort.Slice(methods, func(i, j int) bool { return methods[i].Key < methods[j].Key })
	static := make([]policyOAuthMethod, 0, len(methods))
	resolved := make([]policyOAuthMethod, 0, len(methods))
	for _, method := range methods {
		if method.Type != connector.AuthOAuth2 || method.OAuth == nil {
			continue
		}
		authorizationOrigins := append(
			[]string(nil),
			method.OAuth.Egress.AuthorizationOrigins...,
		)
		tokenOrigins := append(
			[]string(nil),
			method.OAuth.Egress.TokenOrigins...,
		)
		sort.Strings(authorizationOrigins)
		sort.Strings(tokenOrigins)
		scopes := append([]string(nil), method.OAuth.Scopes...)
		sort.Strings(scopes)
		base := policyOAuthMethod{
			Key:                           method.Key,
			AuthorizationEndpointTemplate: method.OAuth.AuthorizationEndpoint,
			AuthorizationOrigins:          authorizationOrigins,
			TokenEndpointTemplate:         method.OAuth.TokenEndpoint,
			RefreshTokenEndpointTemplate:  method.OAuth.RefreshTokenEndpoint,
			TokenOrigins:                  tokenOrigins,
			Scopes:                        scopes,
			AuthorizationScopeSeparator: string(
				method.OAuth.EffectiveAuthorizationScopeSeparator(),
			),
			TokenScopeSeparator: string(
				method.OAuth.EffectiveTokenScopeSeparator(),
			),
			UsePKCE: method.OAuth.UsePKCE,
			TokenEndpointAuth: string(
				method.OAuth.EffectiveTokenEndpointAuth(),
			),
			TokenRequestFormat: string(
				method.OAuth.EffectiveTokenRequestFormat(),
			),
			ExtraAuthParams:  sortedPolicyPairs(method.OAuth.ExtraAuthParams),
			ExtraTokenParams: sortedPolicyPairs(method.OAuth.ExtraTokenParams),
		}
		static = append(static, base)
		entry := base
		for _, endpoint := range []struct {
			label    string
			template string
			url      *string
			origin   *string
		}{
			{
				"authorization",
				method.OAuth.AuthorizationEndpoint,
				&entry.AuthorizationEndpointURL,
				&entry.AuthorizationEndpointOrigin,
			},
			{
				"token",
				method.OAuth.TokenEndpoint,
				&entry.TokenEndpointURL,
				&entry.TokenEndpointOrigin,
			},
			{
				"refresh",
				method.OAuth.RefreshTokenEndpoint,
				&entry.RefreshTokenEndpointURL,
				&entry.RefreshTokenEndpointOrigin,
			},
		} {
			// 占位符缺值时该 endpoint 尚未成形，不进 resolved 投影。
			expanded, missing := providerkit.ExpandEndpoint(
				endpoint.template,
				func(key string) string { return values[key] },
			)
			if len(missing) > 0 || expanded == "" {
				continue
			}
			canonicalURL, origin, err := canonicalPolicyURL(expanded)
			if err != nil {
				return nil, nil, fmt.Errorf(
					"configsvc: connector %q OAuth method %q has invalid %s endpoint",
					def.Type,
					method.Key,
					endpoint.label,
				)
			}
			*endpoint.url = canonicalURL
			*endpoint.origin = origin
		}
		resolved = append(resolved, entry)
	}
	return static, resolved, nil
}

func sortedPolicyPairs(input map[string]string) []policyStringPair {
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]policyStringPair, 0, len(keys))
	for _, key := range keys {
		out = append(out, policyStringPair{Key: key, Value: input[key]})
	}
	return out
}

// canonicalPolicyURL projects both the exact endpoint and canonical origin.
// The representation always includes the effective port so changes in scheme,
// host, or port cannot collapse to the same origin.
func canonicalPolicyURL(raw string) (string, string, error) {
	parsed, err := url.Parse(raw)
	if err != nil ||
		parsed.Scheme == "" ||
		parsed.Host == "" ||
		parsed.User != nil ||
		parsed.Fragment != "" ||
		parsed.Opaque != "" {
		return "", "", errors.New("invalid absolute URL")
	}
	origin, err := providerkit.CanonicalOrigin(parsed)
	if err != nil {
		return "", "", err
	}
	escapedPath := parsed.EscapedPath()
	if escapedPath == "" {
		escapedPath = "/"
	}
	queryValues, err := url.ParseQuery(parsed.RawQuery)
	if err != nil {
		return "", "", errors.New("invalid query")
	}
	query := queryValues.Encode()
	canonicalURL := origin + escapedPath
	if query != "" {
		canonicalURL += "?" + query
	}
	return canonicalURL, origin, nil
}

func digestPolicyField(key, value string) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(policyFieldDigestDomain))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(key))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(value))
	return hex.EncodeToString(hash.Sum(nil))
}

// ReconcilePolicyIdentities is the startup cutover/release hook. It is safe to
// run repeatedly and returns audit-safe counts. A missing legacy identity, a
// static Definition drift, or an out-of-band config drift conservatively bumps
// every Connection for that connector in the same transaction.
func (s *Service) ReconcilePolicyIdentities(
	ctx context.Context,
) ([]PolicyReconcileResult, error) {
	definitions := s.reg.All()
	results := make([]PolicyReconcileResult, 0, len(definitions))
	for _, def := range definitions {
		result, err := s.reconcilePolicyIdentity(ctx, def.Type)
		if err != nil {
			return nil, err
		}
		results = append(results, result)
	}
	return results, nil
}

func (s *Service) reconcilePolicyIdentity(
	ctx context.Context,
	t connector.Type,
) (PolicyReconcileResult, error) {
	def, spec, err := s.policyDefinition(t)
	if err != nil {
		return PolicyReconcileResult{}, err
	}
	tx, qtx, err := s.q.BeginTx(ctx)
	if err != nil {
		return PolicyReconcileResult{}, err
	}
	defer tx.Rollback(ctx) // no-op after Commit

	state, err := lockPolicyIdentity(ctx, qtx, t, spec.Version)
	if err != nil {
		return PolicyReconcileResult{}, err
	}
	row, exists, err := getConfigForUpdate(ctx, qtx, t)
	if err != nil {
		return PolicyReconcileResult{}, err
	}
	values, err := s.policyValuesFromRow(def, row, exists)
	if err != nil {
		return PolicyReconcileResult{}, err
	}
	digests, err := projectPolicyIdentity(def, spec, values)
	if err != nil {
		return PolicyReconcileResult{}, err
	}

	// PreviousKnown/DefinitionChanged 只用于审计日志，不参与失效判定。
	previousKnown := state.Initialized &&
		state.IdentityVersion == int32(digests.version) &&
		len(state.IdentityDigest) == sha256.Size &&
		len(state.DefinitionDigest) == sha256.Size
	result := PolicyReconcileResult{
		ConnectorType: t,
		Changed:       !matchesPolicyIdentity(state, digests),
		PreviousKnown: previousKnown,
		DefinitionChanged: previousKnown &&
			!bytes.Equal(state.DefinitionDigest, digests.definition[:]),
	}
	if result.Changed {
		if _, err := qtx.SetConnectorPolicyIdentity(
			ctx,
			store.SetConnectorPolicyIdentityParams{
				ConnectorType:    string(t),
				IdentityVersion:  int32(digests.version),
				IdentityDigest:   digests.identity[:],
				DefinitionDigest: digests.definition[:],
			},
		); err != nil {
			return PolicyReconcileResult{}, err
		}
		result.ConnectionsBumped, err =
			qtx.BumpConnectorAuthorizationGenerations(ctx, string(t))
		if err != nil {
			return PolicyReconcileResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return PolicyReconcileResult{}, err
	}
	return result, nil
}

func (s *Service) policyDefinition(
	t connector.Type,
) (connector.Definition, registry.ConnectorPolicyIdentitySpec, error) {
	def, ok := s.reg.Get(t)
	if !ok {
		return connector.Definition{}, registry.ConnectorPolicyIdentitySpec{}, ErrUnknownConnector
	}
	spec, ok := s.reg.PolicyIdentitySpec(t)
	if !ok || spec.Version < 1 {
		return connector.Definition{}, registry.ConnectorPolicyIdentitySpec{},
			fmt.Errorf("configsvc: connector %q 缺少 policy identity spec", t)
	}
	return def, spec, nil
}

func lockPolicyIdentity(
	ctx context.Context,
	q *store.Queries,
	t connector.Type,
	version int,
) (store.ConnectorPolicyIdentity, error) {
	if err := q.EnsureConnectorPolicyIdentity(
		ctx,
		store.EnsureConnectorPolicyIdentityParams{
			ConnectorType:   string(t),
			IdentityVersion: int32(version),
		},
	); err != nil {
		return store.ConnectorPolicyIdentity{}, err
	}
	return q.GetConnectorPolicyIdentityForUpdate(ctx, string(t))
}

func getConfigForUpdate(
	ctx context.Context,
	q *store.Queries,
	t connector.Type,
) (store.ConnectorConfig, bool, error) {
	row, err := q.GetConnectorConfigForUpdate(ctx, string(t))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.ConnectorConfig{}, false, nil
	}
	if err != nil {
		return store.ConnectorConfig{}, false, err
	}
	return row, true, nil
}

func matchesPolicyIdentity(
	state store.ConnectorPolicyIdentity,
	digests policyDigests,
) bool {
	return state.Initialized &&
		state.IdentityVersion == int32(digests.version) &&
		bytes.Equal(state.IdentityDigest, digests.identity[:]) &&
		bytes.Equal(state.DefinitionDigest, digests.definition[:])
}
