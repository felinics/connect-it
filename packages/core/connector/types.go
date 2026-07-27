// Package connector 定义 Connector 的固定 Definition 类型。
// 本包只有纯数据类型，不做任何 I/O。
package connector

import (
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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

	ConfigFields   []ConfigField
	AuthMethods    []AuthMethod
	Implementation Implementation

	ConfigUpgraders []ConfigUpgrader

	Deprecated bool
}

type Mode string

const (
	ModeRemoteMCP Mode = "remote_mcp"
	ModeManaged   Mode = "managed"
)

func (d Definition) Mode() Mode {
	if d.Implementation == nil {
		return ""
	}
	return d.Implementation.Mode()
}

// Implementation is exactly one of RemoteMCP or Managed.
type Implementation interface {
	Mode() Mode
	isImplementation()
}

// RemoteMCP exposes the tools discovered from one upstream MCP server.
type RemoteMCP struct {
	Endpoint       string
	RequestTimeout time.Duration
}

func (RemoteMCP) Mode() Mode        { return ModeRemoteMCP }
func (RemoteMCP) isImplementation() {}

// Managed exposes locally implemented tools backed by an HTTP API or SDK.
type Managed struct {
	Tools []ManagedTool
}

func (Managed) Mode() Mode        { return ModeManaged }
func (Managed) isImplementation() {}

type ManagedTool struct {
	Tool    mcp.Tool
	Handler ManagedHandler
}

type ConfigInputType string

const (
	InputText   ConfigInputType = "text"
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

// ConfigUpgrader 把管理员配置从 FromVersion 升级到 FromVersion+1。
type ConfigUpgrader struct {
	FromVersion int
	Upgrade     func(public, secret map[string]any) (map[string]any, map[string]any, error)
}
