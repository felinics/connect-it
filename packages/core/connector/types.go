// Package connector defines the code-fixed Definition types for connectors.
// It holds pure data types only and performs no I/O.
package connector

import (
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Type is the stable platform identifier in snake_case, such as "github" or
// "one_drive".
type Type string

// Definition is the code-fixed template for one connector.
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
	// Exactly one of Endpoint or EndpointSelector is configured.
	Endpoint         string
	EndpointSelector *RemoteMCPEndpointSelector
	// AuthorizationScheme defaults to Bearer. It is only needed when an
	// official server defines a distinct token scheme, such as Sentry-Bearer.
	AuthorizationScheme string
	RequestTimeout      time.Duration
}

// RemoteMCPEndpointSelector chooses among code-defined official endpoints
// using one InputSelect provider config field.
type RemoteMCPEndpointSelector struct {
	ConfigField string
	Endpoints   map[string]string
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
	Pattern string   // regular expression; an empty string disables validation
	Options []string // allowed values for InputSelect
}

type ConfigField struct {
	Key          string
	Label        string
	InputType    ConfigInputType
	Required     bool
	Secret       bool
	DefaultValue *string // must not be set on Secret fields
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
	TokenAuthNone  TokenEndpointAuth = "none"
)

type OAuthMode string

const (
	// OAuthModeMCP discovers the OAuth server from the remote MCP endpoint and
	// registers connect-it as a client dynamically. It does not require an
	// administrator-provided client_id or client_secret.
	OAuthModeMCP OAuthMode = "mcp"
)

type OAuthConfig struct {
	// Mode is empty for a provider-specific, statically configured OAuth app.
	// OAuthModeMCP enables the native MCP OAuth discovery flow.
	Mode                  OAuthMode
	AuthorizationEndpoint string
	TokenEndpoint         string
	Scopes                []string
	// ScopeSeparator defaults to one space. Some providers, notably Slack,
	// require a comma-separated scope parameter.
	ScopeSeparator    string
	UsePKCE           bool
	TokenEndpointAuth TokenEndpointAuth
	ExtraAuthParams   map[string]string
}

type AuthMethod struct {
	Key   string // unique within a connector, such as "oauth" or "pat"
	Type  AuthMethodType
	Label string
	// OAuth is required when Type == AuthOAuth2 and must be nil otherwise.
	OAuth *OAuthConfig
	// CredentialFields are the fields the user fills in for api_key and
	// custom_credential auth methods.
	CredentialFields []ConfigField
}

// ConfigUpgrader migrates administrator config from FromVersion to
// FromVersion+1.
type ConfigUpgrader struct {
	FromVersion int
	Upgrade     func(public, secret map[string]any) (map[string]any, map[string]any, error)
}
