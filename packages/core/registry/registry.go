// Package registry 保存全部 ConnectorDefinition 并在注册时校验。
package registry

import (
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
)

var (
	typePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	toolIDPattern = regexp.MustCompile(`^[a-z0-9_]+$`)
)

const maxRequiredScopeBytes = 4096

type Registry struct {
	mu          sync.RWMutex
	defs        map[connector.Type]connector.Definition
	toolSchemas map[toolSchemaKey]CompiledToolSchemas
}

type toolSchemaKey struct {
	connectorType connector.Type
	toolID        string
}

func New() *Registry {
	return &Registry{
		defs:        map[connector.Type]connector.Definition{},
		toolSchemas: map[toolSchemaKey]CompiledToolSchemas{},
	}
}

// Register 校验并登记一个 Definition。
// managedHandlerKeys 是该 Connector 在 managed.go 中注册的 handler key 集合，
// 用于校验 ManagedBackend 引用的 handler 确实存在。
func (r *Registry) Register(def connector.Definition, managedHandlerKeys ...string) error {
	r.mu.RLock()
	if _, exists := r.defs[def.Type]; exists {
		r.mu.RUnlock()
		return fmt.Errorf("connector %q: type 重复注册", def.Type)
	}
	r.mu.RUnlock()

	keys := map[string]bool{}
	for _, k := range managedHandlerKeys {
		keys[k] = true
	}
	normalized := normalizeDefinitionRuntimeContracts(def)
	schemas, err := validate(normalized, keys)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.defs[def.Type]; exists {
		return fmt.Errorf("connector %q: type 重复注册", def.Type)
	}
	r.defs[def.Type] = cloneDefinition(normalized)
	for toolID, compiled := range schemas {
		r.toolSchemas[toolSchemaKey{connectorType: def.Type, toolID: toolID}] = compiled
	}
	return nil
}

func (r *Registry) MustRegister(def connector.Definition, managedHandlerKeys ...string) {
	if err := r.Register(def, managedHandlerKeys...); err != nil {
		panic(err)
	}
}

func (r *Registry) Get(t connector.Type) (connector.Definition, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.defs[t]
	if !ok {
		return connector.Definition{}, false
	}
	return cloneDefinition(def), true
}

func (r *Registry) All() []connector.Definition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]connector.Definition, 0, len(r.defs))
	for _, def := range r.defs {
		out = append(out, cloneDefinition(def))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// cloneDefinition prevents caller-owned mutable data from changing a
// registered catalog, authorization, schema, backend, provenance, or egress
// contract after validation. Register input and Get/All output must never
// share mutable backing storage with Registry.
func cloneDefinition(def connector.Definition) connector.Definition {
	out := def
	out.Categories = append([]string(nil), def.Categories...)
	out.ConfigFields = cloneConfigFields(def.ConfigFields)
	out.AuthMethods = append([]connector.AuthMethod(nil), def.AuthMethods...)
	for index := range out.AuthMethods {
		method := out.AuthMethods[index]
		out.AuthMethods[index].CredentialFields = cloneConfigFields(
			method.CredentialFields,
		)
		if method.OAuth != nil {
			oauth := *method.OAuth
			oauth.Scopes = append([]string(nil), method.OAuth.Scopes...)
			oauth.Egress.AuthorizationOrigins = append(
				[]string(nil),
				method.OAuth.Egress.AuthorizationOrigins...,
			)
			oauth.Egress.TokenOrigins = append(
				[]string(nil),
				method.OAuth.Egress.TokenOrigins...,
			)
			oauth.ExtraAuthParams = cloneStringMap(method.OAuth.ExtraAuthParams)
			oauth.ExtraTokenParams = cloneStringMap(method.OAuth.ExtraTokenParams)
			out.AuthMethods[index].OAuth = &oauth
		}
	}
	out.RemoteMCPServers = append(
		[]connector.RemoteMCPServer(nil),
		def.RemoteMCPServers...,
	)
	for index := range out.RemoteMCPServers {
		server := def.RemoteMCPServers[index]
		out.RemoteMCPServers[index].AuthBinding.
			CredentialFieldByAuthMethod = cloneStringMap(
			server.AuthBinding.CredentialFieldByAuthMethod,
		)
		out.RemoteMCPServers[index].Provenance.AllowedHostnames = append(
			[]string(nil),
			server.Provenance.AllowedHostnames...,
		)
	}
	out.Tools = append([]connector.Tool(nil), def.Tools...)
	for index := range out.Tools {
		out.Tools[index].InputSchema = append(
			[]byte(nil),
			def.Tools[index].InputSchema...,
		)
		out.Tools[index].OutputSchema = append(
			[]byte(nil),
			def.Tools[index].OutputSchema...,
		)
		out.Tools[index].RequiredScopes = append(
			[]string(nil),
			def.Tools[index].RequiredScopes...,
		)
	}
	out.ConfigUpgraders = append(
		[]connector.ConfigUpgrader(nil),
		def.ConfigUpgraders...,
	)
	return out
}

func normalizeDefinitionRuntimeContracts(
	def connector.Definition,
) connector.Definition {
	out := cloneDefinition(def)
	for index := range out.AuthMethods {
		method := &out.AuthMethods[index]
		if method.OAuth != nil {
			method.OAuth.AuthorizationScopeSeparator =
				method.OAuth.EffectiveAuthorizationScopeSeparator()
			method.OAuth.TokenScopeSeparator =
				method.OAuth.EffectiveTokenScopeSeparator()
			method.OAuth.TokenEndpointAuth =
				method.OAuth.EffectiveTokenEndpointAuth()
			method.OAuth.TokenRequestFormat =
				method.OAuth.EffectiveTokenRequestFormat()
		}
	}
	return out
}

func cloneStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func cloneConfigFields(input []connector.ConfigField) []connector.ConfigField {
	if input == nil {
		return nil
	}
	out := append([]connector.ConfigField(nil), input...)
	for index := range out {
		if input[index].DefaultValue != nil {
			value := *input[index].DefaultValue
			out[index].DefaultValue = &value
		}
		out[index].Validation.Options = append(
			[]string(nil),
			input[index].Validation.Options...,
		)
	}
	return out
}

// ToolSchemas returns an immutable snapshot of the resolved schemas and
// effective input limit for a registered tool.
func (r *Registry) ToolSchemas(
	connectorType connector.Type,
	toolID string,
) (CompiledToolSchemas, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	schemas, ok := r.toolSchemas[toolSchemaKey{connectorType: connectorType, toolID: toolID}]
	return schemas, ok
}

func validate(
	def connector.Definition,
	handlerKeys map[string]bool,
) (map[string]CompiledToolSchemas, error) {
	if !typePattern.MatchString(string(def.Type)) {
		return nil, fmt.Errorf("connector %q: type 必须匹配 %s", def.Type, typePattern)
	}
	if def.Name == "" {
		return nil, fmt.Errorf("connector %q: Name 不能为空", def.Type)
	}
	if def.ConfigSchemaVersion < 1 {
		return nil, fmt.Errorf("connector %q: ConfigSchemaVersion 必须 >= 1", def.Type)
	}
	if err := validateFieldDeclarations(def.Type, "配置字段", def.ConfigFields); err != nil {
		return nil, err
	}
	if err := validatePolicyIdentityDefinition(def); err != nil {
		return nil, err
	}
	fieldKeys := make(map[string]bool, len(def.ConfigFields))
	for _, field := range def.ConfigFields {
		fieldKeys[field.Key] = true
	}

	authKeys := map[string]bool{}
	authMethods := map[string]connector.AuthMethod{}
	for _, a := range def.AuthMethods {
		if a.Key == "" {
			return nil, fmt.Errorf("connector %q: auth method key 不能为空", def.Type)
		}
		if authKeys[a.Key] {
			return nil, fmt.Errorf("connector %q: auth method %q 重复", def.Type, a.Key)
		}
		authKeys[a.Key] = true
		authMethods[a.Key] = a
		switch a.Type {
		case connector.AuthOAuth2:
			if a.OAuth == nil {
				return nil, fmt.Errorf(
					"connector %q: auth method %q 是 oauth2 但缺少 OAuth 配置",
					def.Type,
					a.Key,
				)
			}
			if len(a.CredentialFields) != 0 {
				return nil, fmt.Errorf(
					"connector %q: oauth2 auth method %q 不允许声明 CredentialFields",
					def.Type,
					a.Key,
				)
			}
			if err := validateOAuthConfig(def.Type, a); err != nil {
				return nil, err
			}
		case connector.AuthNone:
			if a.OAuth != nil {
				return nil, fmt.Errorf(
					"connector %q: auth method %q 不是 oauth2 不允许携带 OAuth 配置",
					def.Type,
					a.Key,
				)
			}
			if len(a.CredentialFields) != 0 {
				return nil, fmt.Errorf(
					"connector %q: none auth method %q 不允许声明 CredentialFields",
					def.Type,
					a.Key,
				)
			}
		case connector.AuthAPIKey, connector.AuthCustomCredential:
			if a.OAuth != nil {
				return nil, fmt.Errorf(
					"connector %q: auth method %q 不是 oauth2 不允许携带 OAuth 配置",
					def.Type,
					a.Key,
				)
			}
			if len(a.CredentialFields) == 0 {
				return nil, fmt.Errorf(
					"connector %q: auth method %q 必须至少声明一个 CredentialFields 字段",
					def.Type,
					a.Key,
				)
			}
			owner := fmt.Sprintf("auth method %q CredentialFields", a.Key)
			if err := validateFieldDeclarations(
				def.Type,
				owner,
				a.CredentialFields,
			); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf(
				"connector %q: auth method %q 的 Type %q 非法",
				def.Type,
				a.Key,
				a.Type,
			)
		}
	}

	serverKeys := map[string]bool{}
	for _, s := range def.RemoteMCPServers {
		if s.Key == "" {
			return nil, fmt.Errorf("connector %q: MCP server key 不能为空", def.Type)
		}
		if serverKeys[s.Key] {
			return nil, fmt.Errorf("connector %q: MCP server %q 重复", def.Type, s.Key)
		}
		serverKeys[s.Key] = true
		switch s.Endpoint.Source {
		case connector.EndpointFixed:
			u, err := url.Parse(s.Endpoint.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return nil, fmt.Errorf("connector %q: MCP server %q 的固定 endpoint 必须是 https URL", def.Type, s.Key)
			}
		case connector.EndpointConfigField:
			if s.Provenance.Kind != connector.ProvenanceSelfHosted {
				return nil, fmt.Errorf("connector %q: MCP server %q 从配置取 endpoint 仅允许 self_hosted", def.Type, s.Key)
			}
			if !fieldKeys[s.Endpoint.ConfigFieldKey] {
				return nil, fmt.Errorf("connector %q: MCP server %q 引用的配置字段 %q 不存在", def.Type, s.Key, s.Endpoint.ConfigFieldKey)
			}
		default:
			return nil, fmt.Errorf("connector %q: MCP server %q 的 Endpoint.Source 非法", def.Type, s.Key)
		}
		for methodKey, fieldKey := range s.AuthBinding.CredentialFieldByAuthMethod {
			method, exists := authMethods[methodKey]
			if !exists {
				return nil, fmt.Errorf(
					"connector %q: MCP server %q credential binding 引用不存在 auth method %q",
					def.Type,
					s.Key,
					methodKey,
				)
			}
			if method.Type != connector.AuthAPIKey &&
				method.Type != connector.AuthCustomCredential {
				return nil, fmt.Errorf(
					"connector %q: MCP server %q credential binding 的 auth method %q "+
						"必须是 api_key/custom_credential",
					def.Type,
					s.Key,
					methodKey,
				)
			}
			fieldFound := false
			for _, field := range method.CredentialFields {
				if field.Key != fieldKey {
					continue
				}
				fieldFound = true
				if !field.Secret {
					return nil, fmt.Errorf(
						"connector %q: MCP server %q credential binding %q 必须引用 Secret 字段",
						def.Type,
						s.Key,
						fieldKey,
					)
				}
				break
			}
			if !fieldFound {
				return nil, fmt.Errorf(
					"connector %q: MCP server %q credential binding 引用不存在字段 %q",
					def.Type,
					s.Key,
					fieldKey,
				)
			}
		}
		for methodKey, method := range authMethods {
			if method.Type != connector.AuthAPIKey &&
				method.Type != connector.AuthCustomCredential {
				continue
			}
			if s.AuthBinding.CredentialFieldByAuthMethod[methodKey] == "" {
				return nil, fmt.Errorf(
					"connector %q: MCP server %q 的 auth method %q "+
						"缺少显式 credential binding",
					def.Type,
					s.Key,
					methodKey,
				)
			}
		}
	}

	toolIDs := map[string]bool{}
	compiledTools := make(map[string]CompiledToolSchemas, len(def.Tools))
	for _, tl := range def.Tools {
		if !toolIDPattern.MatchString(tl.ID) {
			return nil, fmt.Errorf("connector %q: tool ID %q 必须匹配 %s", def.Type, tl.ID, toolIDPattern)
		}
		if toolIDs[tl.ID] {
			return nil, fmt.Errorf("connector %q: tool %q 重复", def.Type, tl.ID)
		}
		toolIDs[tl.ID] = true
		if err := validateToolPolicy(def.Type, tl); err != nil {
			return nil, err
		}
		if tl.MaxInputBytes < 0 {
			return nil, fmt.Errorf("connector %q: tool %q MaxInputBytes 不能为负数", def.Type, tl.ID)
		}
		if tl.MaxInputBytes > connector.AbsoluteMaxInputBytes {
			return nil, fmt.Errorf(
				"connector %q: tool %q MaxInputBytes 不能超过 %d bytes",
				def.Type,
				tl.ID,
				connector.AbsoluteMaxInputBytes,
			)
		}
		input, err := compileSchema(
			tl.InputSchema,
			fmt.Sprintf("connector %q tool %q InputSchema", def.Type, tl.ID),
		)
		if err != nil {
			return nil, err
		}
		var output *CompiledSchema
		if len(tl.OutputSchema) > 0 {
			output, err = compileSchema(
				tl.OutputSchema,
				fmt.Sprintf("connector %q tool %q OutputSchema", def.Type, tl.ID),
			)
			if err != nil {
				return nil, err
			}
		}
		compiledTools[tl.ID] = CompiledToolSchemas{
			Input:         input,
			Output:        output,
			MaxInputBytes: tl.EffectiveMaxInputBytes(),
		}
		switch b := tl.Backend.(type) {
		case connector.RemoteMCPBackend:
			if !serverKeys[b.ServerKey] {
				return nil, fmt.Errorf("connector %q: tool %q 引用的 MCP server %q 不存在", def.Type, tl.ID, b.ServerKey)
			}
			if b.RemoteToolName == "" {
				return nil, fmt.Errorf("connector %q: tool %q 缺少 RemoteToolName", def.Type, tl.ID)
			}
		case connector.ManagedBackend:
			if !handlerKeys[b.HandlerKey] {
				return nil, fmt.Errorf("connector %q: tool %q 引用的 managed handler %q 未注册", def.Type, tl.ID, b.HandlerKey)
			}
		case nil:
			return nil, fmt.Errorf("connector %q: tool %q 缺少 Backend", def.Type, tl.ID)
		default:
			return nil, fmt.Errorf("connector %q: tool %q 的 Backend 类型未知", def.Type, tl.ID)
		}
	}

	seenFrom := map[int]bool{}
	for _, up := range def.ConfigUpgraders {
		if up.FromVersion < 1 || up.FromVersion >= def.ConfigSchemaVersion {
			return nil, fmt.Errorf("connector %q: upgrader FromVersion %d 越界", def.Type, up.FromVersion)
		}
		if seenFrom[up.FromVersion] {
			return nil, fmt.Errorf("connector %q: upgrader FromVersion %d 重复", def.Type, up.FromVersion)
		}
		seenFrom[up.FromVersion] = true
	}
	return compiledTools, nil
}

func validateToolPolicy(
	connectorType connector.Type,
	tool connector.Tool,
) error {
	switch tool.Risk {
	case connector.RiskRead,
		connector.RiskWrite,
		connector.RiskDestructive:
	default:
		return fmt.Errorf(
			"connector %q: tool %q 的 Risk %q 非法",
			connectorType,
			tool.ID,
			tool.Risk,
		)
	}

	seenScopes := make(map[string]struct{}, len(tool.RequiredScopes))
	for _, scope := range tool.RequiredScopes {
		if scope == "" ||
			strings.TrimSpace(scope) != scope ||
			!utf8.ValidString(scope) ||
			len(scope) > maxRequiredScopeBytes ||
			strings.ContainsAny(scope, "\x00\r\n") {
			return fmt.Errorf(
				"connector %q: tool %q 的 RequiredScope 非法",
				connectorType,
				tool.ID,
			)
		}
		if _, duplicate := seenScopes[scope]; duplicate {
			return fmt.Errorf(
				"connector %q: tool %q 的 RequiredScope %q 重复",
				connectorType,
				tool.ID,
				scope,
			)
		}
		seenScopes[scope] = struct{}{}
	}
	return nil
}

func validateOAuthConfig(
	connectorType connector.Type,
	method connector.AuthMethod,
) error {
	oauth := method.OAuth
	if oauth == nil {
		return nil
	}
	for label, separator := range map[string]connector.OAuthScopeSeparator{
		"AuthorizationScopeSeparator": oauth.AuthorizationScopeSeparator,
		"TokenScopeSeparator":         oauth.TokenScopeSeparator,
	} {
		switch separator {
		case connector.OAuthScopeSpace, connector.OAuthScopeComma:
		default:
			return fmt.Errorf(
				"connector %q: auth method %q 的 %s %q 非法",
				connectorType,
				method.Key,
				label,
				separator,
			)
		}
	}
	switch oauth.TokenEndpointAuth {
	case connector.TokenAuthBasic,
		connector.TokenAuthPost,
		connector.TokenAuthNone:
	default:
		return fmt.Errorf(
			"connector %q: auth method %q 的 TokenEndpointAuth %q 非法",
			connectorType,
			method.Key,
			oauth.TokenEndpointAuth,
		)
	}
	switch oauth.TokenRequestFormat {
	case connector.TokenRequestForm, connector.TokenRequestJSON:
	default:
		return fmt.Errorf(
			"connector %q: auth method %q 的 TokenRequestFormat %q 非法",
			connectorType,
			method.Key,
			oauth.TokenRequestFormat,
		)
	}
	if err := validateExtraParams(
		connectorType,
		method.Key,
		"ExtraAuthParams",
		oauth.ExtraAuthParams,
		reservedAuthorizationParams,
	); err != nil {
		return err
	}
	if err := validateExtraParams(
		connectorType,
		method.Key,
		"ExtraTokenParams",
		oauth.ExtraTokenParams,
		reservedTokenParams,
	); err != nil {
		return err
	}
	return validateStaticOAuthEgress(connectorType, method.Key, oauth)
}

func validateStaticOAuthEgress(
	connectorType connector.Type,
	methodKey string,
	oauth *connector.OAuthConfig,
) error {
	for label, origins := range map[string][]string{
		"AuthorizationOrigins": oauth.Egress.AuthorizationOrigins,
		"TokenOrigins":         oauth.Egress.TokenOrigins,
	} {
		if len(origins) == 0 {
			return fmt.Errorf(
				"connector %q: auth method %q 的 %s 不能为空",
				connectorType,
				methodKey,
				label,
			)
		}
		seen := make(map[string]struct{}, len(origins))
		for _, origin := range origins {
			canonical, err := providerkit.CanonicalizeOrigin(origin)
			if err != nil || canonical != origin ||
				!strings.HasPrefix(origin, "https://") {
				return fmt.Errorf(
					"connector %q: auth method %q 的 %s 必须只包含 canonical HTTPS origin",
					connectorType,
					methodKey,
					label,
				)
			}
			if _, duplicate := seen[origin]; duplicate {
				return fmt.Errorf(
					"connector %q: auth method %q 的 %s 含重复 origin",
					connectorType,
					methodKey,
					label,
				)
			}
			seen[origin] = struct{}{}
		}
	}
	if err := validateAuthorizationEndpoint(
		connectorType,
		methodKey,
		"AuthorizationEndpoint",
		oauth.AuthorizationEndpoint,
		oauth.Egress.AuthorizationOrigins,
		true,
	); err != nil {
		return err
	}
	if err := validateAuthorizationEndpoint(
		connectorType,
		methodKey,
		"TokenEndpoint",
		oauth.TokenEndpoint,
		oauth.Egress.TokenOrigins,
		true,
	); err != nil {
		return err
	}
	if err := validateAuthorizationEndpoint(
		connectorType,
		methodKey,
		"RefreshTokenEndpoint",
		oauth.RefreshTokenEndpoint,
		oauth.Egress.TokenOrigins,
		false,
	); err != nil {
		return err
	}
	return nil
}

func validateAuthorizationEndpoint(
	connectorType connector.Type,
	methodKey string,
	label string,
	endpoint string,
	allowedOrigins []string,
	required bool,
) error {
	if endpoint == "" {
		if !required {
			return nil
		}
		return fmt.Errorf(
			"connector %q: auth method %q 的 %s 不能为空",
			connectorType,
			methodKey,
			label,
		)
	}
	validationEndpoint := endpoint
	if strings.Contains(endpoint, "{") || strings.Contains(endpoint, "}") {
		authorityEnd := len(endpoint)
		if scheme := strings.Index(endpoint, "://"); scheme >= 0 {
			if relative := strings.IndexAny(
				endpoint[scheme+3:],
				"/?#",
			); relative >= 0 {
				authorityEnd = scheme + 3 + relative
			}
		}
		if strings.ContainsAny(endpoint[:authorityEnd], "{}") {
			return fmt.Errorf(
				"connector %q: auth method %q 的 %s placeholder 不得改变 origin",
				connectorType,
				methodKey,
				label,
			)
		}
		validationEndpoint, _ = providerkit.ExpandEndpoint(
			endpoint,
			func(string) string { return "placeholder" },
		)
	}
	parsed, err := providerkit.ParseAndValidateURL(validationEndpoint)
	if err != nil {
		return fmt.Errorf(
			"connector %q: auth method %q 的 %s 非法",
			connectorType,
			methodKey,
			label,
		)
	}
	origin, err := providerkit.CanonicalOrigin(parsed)
	if err != nil || !strings.HasPrefix(origin, "https://") {
		return fmt.Errorf(
			"connector %q: auth method %q 的 %s 非法",
			connectorType,
			methodKey,
			label,
		)
	}
	if !slices.Contains(allowedOrigins, origin) {
		return fmt.Errorf(
			"connector %q: auth method %q 的 %s origin 未列入 egress policy",
			connectorType,
			methodKey,
			label,
		)
	}
	return nil
}

var (
	reservedAuthorizationParams = map[string]struct{}{
		"client_id":             {},
		"redirect_uri":          {},
		"response_type":         {},
		"state":                 {},
		"scope":                 {},
		"code_challenge":        {},
		"code_challenge_method": {},
	}
	reservedTokenParams = map[string]struct{}{
		"grant_type":    {},
		"code":          {},
		"refresh_token": {},
		"redirect_uri":  {},
		"client_id":     {},
		"client_secret": {},
		"code_verifier": {},
	}
)

func validateExtraParams(
	connectorType connector.Type,
	methodKey string,
	label string,
	params map[string]string,
	reserved map[string]struct{},
) error {
	seen := make(map[string]struct{}, len(params))
	for key := range params {
		if key == "" || strings.IndexFunc(key, func(r rune) bool {
			return r <= 0x20 || r == 0x7f || r == '&' || r == '='
		}) >= 0 {
			return fmt.Errorf(
				"connector %q: auth method %q 的 %s 参数名 %q 非法",
				connectorType,
				methodKey,
				label,
				key,
			)
		}
		folded := asciiLower(key)
		if _, duplicate := seen[folded]; duplicate {
			return fmt.Errorf(
				"connector %q: auth method %q 的 %s 含 ASCII 大小写重复参数 %q",
				connectorType,
				methodKey,
				label,
				key,
			)
		}
		seen[folded] = struct{}{}
		if _, blocked := reserved[folded]; blocked {
			return fmt.Errorf(
				"connector %q: auth method %q 的 %s 不允许覆盖 reserved 参数 %q",
				connectorType,
				methodKey,
				label,
				key,
			)
		}
	}
	return nil
}

func asciiLower(value string) string {
	bytes := []byte(value)
	for index, b := range bytes {
		if b >= 'A' && b <= 'Z' {
			bytes[index] = b + ('a' - 'A')
		}
	}
	return string(bytes)
}

func validateFieldDeclarations(
	connectorType connector.Type,
	owner string,
	fields []connector.ConfigField,
) error {
	keys := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if field.Key == "" {
			return fmt.Errorf(
				"connector %q: %s key 不能为空",
				connectorType,
				owner,
			)
		}
		if _, duplicate := keys[field.Key]; duplicate {
			return fmt.Errorf(
				"connector %q: %s %q 重复",
				connectorType,
				owner,
				field.Key,
			)
		}
		keys[field.Key] = struct{}{}

		switch field.InputType {
		case connector.InputText, connector.InputURL, connector.InputSelect:
		default:
			return fmt.Errorf(
				"connector %q: %s %q 的 InputType %q 非法",
				connectorType,
				owner,
				field.Key,
				field.InputType,
			)
		}

		var pattern *regexp.Regexp
		if field.Validation.Pattern != "" {
			compiled, err := regexp.Compile(field.Validation.Pattern)
			if err != nil {
				return fmt.Errorf(
					"connector %q: %s %q 的 Pattern 非法",
					connectorType,
					owner,
					field.Key,
				)
			}
			pattern = compiled
		}

		options := field.Validation.Options
		switch field.InputType {
		case connector.InputSelect:
			if len(options) == 0 {
				return fmt.Errorf(
					"connector %q: select %s %q 的 Options 不能为空",
					connectorType,
					owner,
					field.Key,
				)
			}
		default:
			if len(options) != 0 {
				return fmt.Errorf(
					"connector %q: 非 select %s %q 不允许声明 Options",
					connectorType,
					owner,
					field.Key,
				)
			}
		}

		seenOptions := make(map[string]struct{}, len(options))
		for _, option := range options {
			if option == "" {
				return fmt.Errorf(
					"connector %q: %s %q 的 Options 不允许空值",
					connectorType,
					owner,
					field.Key,
				)
			}
			if _, duplicate := seenOptions[option]; duplicate {
				return fmt.Errorf(
					"connector %q: %s %q 的 Options 存在重复值",
					connectorType,
					owner,
					field.Key,
				)
			}
			seenOptions[option] = struct{}{}
		}

		if field.Secret && field.DefaultValue != nil {
			return fmt.Errorf(
				"connector %q: Secret %s %q 不允许默认值（DefaultValue）",
				connectorType,
				owner,
				field.Key,
			)
		}
		if field.DefaultValue == nil {
			continue
		}
		defaultValue := *field.DefaultValue
		if field.Required && defaultValue == "" {
			return fmt.Errorf(
				"connector %q: required %s %q 的 DefaultValue 不能为空",
				connectorType,
				owner,
				field.Key,
			)
		}
		if pattern != nil && !pattern.MatchString(defaultValue) {
			return fmt.Errorf(
				"connector %q: %s %q 的 DefaultValue 不匹配 Pattern",
				connectorType,
				owner,
				field.Key,
			)
		}
		if len(options) > 0 {
			if _, valid := seenOptions[defaultValue]; !valid {
				return fmt.Errorf(
					"connector %q: %s %q 的 DefaultValue 不在 Options 中",
					connectorType,
					owner,
					field.Key,
				)
			}
		}
	}
	return nil
}
