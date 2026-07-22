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
)

type OAuthConfig struct {
	AuthorizationEndpoint string
	TokenEndpoint         string
	Scopes                []string
	UsePKCE               bool
	TokenEndpointAuth     TokenEndpointAuth
	ExtraAuthParams       map[string]string
	ProfileResolverKey    string
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
}

type ProvenanceKind string

const (
	ProvenanceOfficial   ProvenanceKind = "official"
	ProvenanceThirdParty ProvenanceKind = "third_party"
	ProvenanceSelfHosted ProvenanceKind = "self_hosted"
)

type Stability string

const (
	StabilityStable       Stability = "stable"
	StabilityPreview      Stability = "preview"
	StabilityExperimental Stability = "experimental"
)

type Provenance struct {
	Kind             ProvenanceKind
	Publisher        string
	DocsURL          string
	SourceURL        string
	ReviewedAt       string // YYYY-MM-DD
	AllowedHostnames []string
	Stability        Stability
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
	ID             string // ^[a-z0-9_]+$
	Name           string
	Description    string
	InputSchema    json.RawMessage
	OutputSchema   json.RawMessage
	RequiredScopes []string
	Risk           ToolRisk
	Backend        ToolBackend
}

// ConfigUpgrader 把管理员配置从 FromVersion 升级到 FromVersion+1。
type ConfigUpgrader struct {
	FromVersion int
	Upgrade     func(public, secret map[string]any) (map[string]any, map[string]any, error)
}
