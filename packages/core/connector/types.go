// Package connector 定义 Connector 的固定 Definition 类型。
// 本包只有纯数据类型，不做任何 I/O。
package connector

import (
	"encoding/json"
	"time"
)

// Type 是稳定的平台标识（snake_case），如 "github"、"one_drive"。
type Type string

// Definition 是代码内固定的 Connector 模板。
type Definition struct {
	Type                Type
	Name                string
	Description         string
	Categories          []string
	HomepageURL         string
	IconURL             string
	ConfigSchemaVersion int

	ConfigFields     []ConfigField
	AuthMethods      []AuthMethod
	RemoteMCPServers []RemoteMCPServer
	Tools            []Tool

	ConfigUpgraders []ConfigUpgrader

	Deprecated bool
}

type ConfigInputType string

const (
	InputText   ConfigInputType = "text"
	InputURL    ConfigInputType = "url"
	InputSelect ConfigInputType = "select"
)

type FieldValidation struct {
	Pattern string   // 正则，空串表示不校验
	Options []string // InputSelect 的可选值
}

type ConfigField struct {
	Key          string
	Label        string
	InputType    ConfigInputType
	Required     bool
	Secret       bool
	DefaultValue *string // Secret 字段禁止设置
	Description  string
	Validation   FieldValidation
	// PolicyIdentity marks a provider-specific field whose normalized value
	// changes the authorization/security policy of every Connection for this
	// Connector. Registry automatically adds standard fields referenced by a
	// Remote MCP endpoint, OAuth endpoint placeholder, OAuth client identity,
	// or network switch; this flag is for additional provider-specific inputs.
	//
	// Secret fields may opt in. The service persists only a domain-separated
	// digest of the resulting policy identity, never the field value.
	PolicyIdentity bool
}

type AuthMethodType string

const (
	AuthNone             AuthMethodType = "none"
	AuthOAuth2           AuthMethodType = "oauth2"
	AuthAPIKey           AuthMethodType = "api_key"
	AuthCustomCredential AuthMethodType = "custom_credential"
)

type TokenEndpointAuth string

const (
	TokenAuthBasic TokenEndpointAuth = "client_secret_basic"
	TokenAuthPost  TokenEndpointAuth = "client_secret_post"
	// TokenAuthNone is an explicit public-client mode. It must never be
	// inferred from a missing client secret.
	TokenAuthNone TokenEndpointAuth = "none"
)

// OAuthScopeSeparator 描述 provider 在 token response 的 scope 字符串中
// 使用的分隔方式。零值按空格分隔处理。
type OAuthScopeSeparator string

const (
	OAuthScopeSpace OAuthScopeSeparator = "space"
	OAuthScopeComma OAuthScopeSeparator = "comma"
)

type TokenRequestFormat string

const (
	TokenRequestForm TokenRequestFormat = "form"
	TokenRequestJSON TokenRequestFormat = "json"
)

// OAuthEgressConfig is the reviewed, code-defined network boundary for an
// OAuth authorization method. Origins use the canonical
// scheme://lowercase-host:effective-port form. OAuth is PublicOnly in Phase D;
// self-hosted and tenant-derived origins require separate typed contracts.
type OAuthEgressConfig struct {
	AuthorizationOrigins []string
	TokenOrigins         []string
}

type OAuthConfig struct {
	AuthorizationEndpoint       string
	TokenEndpoint               string
	RefreshTokenEndpoint        string
	Egress                      OAuthEgressConfig
	Scopes                      []string
	AuthorizationScopeSeparator OAuthScopeSeparator
	TokenScopeSeparator         OAuthScopeSeparator
	UsePKCE                     bool
	TokenEndpointAuth           TokenEndpointAuth
	TokenRequestFormat          TokenRequestFormat
	ExtraAuthParams             map[string]string
	ExtraTokenParams            map[string]string
}

// EffectiveAuthorizationScopeSeparator returns the separator used when
// building the browser authorization request.
func (c OAuthConfig) EffectiveAuthorizationScopeSeparator() OAuthScopeSeparator {
	if c.AuthorizationScopeSeparator == "" {
		return OAuthScopeSpace
	}
	return c.AuthorizationScopeSeparator
}

// EffectiveTokenScopeSeparator 返回 token response scope 的有效分隔方式。
// Registry 会拒绝未知非零值，因此零值只需规范为兼容 OAuth 默认值的 space。
func (c OAuthConfig) EffectiveTokenScopeSeparator() OAuthScopeSeparator {
	if c.TokenScopeSeparator == "" {
		return OAuthScopeSpace
	}
	return c.TokenScopeSeparator
}

// EffectiveTokenEndpointAuth preserves the pre-Phase-G default while keeping
// public clients explicit through TokenAuthNone.
func (c OAuthConfig) EffectiveTokenEndpointAuth() TokenEndpointAuth {
	if c.TokenEndpointAuth == "" {
		return TokenAuthBasic
	}
	return c.TokenEndpointAuth
}

func (c OAuthConfig) EffectiveTokenRequestFormat() TokenRequestFormat {
	if c.TokenRequestFormat == "" {
		return TokenRequestForm
	}
	return c.TokenRequestFormat
}

type AuthMethod struct {
	Key   string // Connector 内唯一，如 "oauth"、"pat"
	Type  AuthMethodType
	Label string
	// OAuth 仅 Type == AuthOAuth2 时必填，其他类型必须为 nil。
	OAuth *OAuthConfig
	// CredentialFields 是 api_key / custom_credential 需要用户填写的字段。
	CredentialFields []ConfigField
}

type EndpointSource string

const (
	EndpointFixed       EndpointSource = "fixed"
	EndpointConfigField EndpointSource = "config_field"
)

type Endpoint struct {
	Source         EndpointSource
	URL            string // Source == EndpointFixed 时使用，必须为 https
	ConfigFieldKey string // Source == EndpointConfigField 时引用的 ConfigField
}

type MCPAuthBinding struct {
	Scheme string // 目前仅 "bearer"
	// CredentialFieldByAuthMethod explicitly names the API-key/custom field
	// presented by a Remote MCP server. OAuth uses the typed access token and
	// therefore has no entry. Declaration order is never credential binding.
	CredentialFieldByAuthMethod map[string]string
}

type ProvenanceKind string

const (
	ProvenanceOfficial   ProvenanceKind = "official"
	ProvenanceThirdParty ProvenanceKind = "third_party"
	ProvenanceSelfHosted ProvenanceKind = "self_hosted"
)

type Provenance struct {
	Kind             ProvenanceKind
	AllowedHostnames []string
}

type RemoteMCPServer struct {
	Key            string
	Endpoint       Endpoint
	AuthBinding    MCPAuthBinding
	Provenance     Provenance
	RequestTimeout time.Duration
}

type ToolRisk string

const (
	RiskRead        ToolRisk = "read"
	RiskWrite       ToolRisk = "write"
	RiskDestructive ToolRisk = "destructive"
)

// ToolBackend 是 tagged union：RemoteMCPBackend 或 ManagedBackend。
type ToolBackend interface{ isToolBackend() }

type RemoteMCPBackend struct {
	ServerKey       string
	RemoteToolName  string
	InputMapperKey  string
	OutputMapperKey string
}

func (RemoteMCPBackend) isToolBackend() {}

type ManagedBackend struct {
	HandlerKey string
}

func (ManagedBackend) isToolBackend() {}

type Tool struct {
	ID           string // ^[a-z0-9_]+$
	Name         string
	Description  string
	InputSchema  json.RawMessage
	OutputSchema json.RawMessage
	// MaxInputBytes limits the raw JSON arguments before decoding. Zero selects
	// DefaultMaxInputBytes; declarations above AbsoluteMaxInputBytes are invalid.
	MaxInputBytes  int64
	RequiredScopes []string
	Risk           ToolRisk
	Backend        ToolBackend
}

const (
	// DefaultMaxInputBytes is the per-tool default for a zero MaxInputBytes.
	DefaultMaxInputBytes int64 = 5 << 20
	// AbsoluteMaxInputBytes is the protocol-wide arguments ceiling.
	AbsoluteMaxInputBytes int64 = 32 << 20
)

// EffectiveMaxInputBytes returns the normalized raw arguments limit. Registry
// validation guarantees that registered tools return a positive value no larger
// than AbsoluteMaxInputBytes.
func (t Tool) EffectiveMaxInputBytes() int64 {
	if t.MaxInputBytes == 0 {
		return DefaultMaxInputBytes
	}
	return t.MaxInputBytes
}

// ConfigUpgrader 把管理员配置从 FromVersion 升级到 FromVersion+1。
type ConfigUpgrader struct {
	FromVersion int
	Upgrade     func(public, secret map[string]any) (map[string]any, map[string]any, error)
}
