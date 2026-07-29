// Package registry holds every connector Definition and validates each one at
// registration time.
package registry

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"

	"github.com/memohai/connect-it/packages/core/connector"
)

var (
	typePattern                = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	toolNamePattern            = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	authorizationSchemePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*$`)
)

type Registry struct {
	defs map[connector.Type]connector.Definition
}

func New() *Registry {
	return &Registry{defs: map[connector.Type]connector.Definition{}}
}

// Register validates a Definition and records it.
func (r *Registry) Register(def connector.Definition) error {
	if _, exists := r.defs[def.Type]; exists {
		return fmt.Errorf("connector %q: type registered twice", def.Type)
	}
	if err := validate(def); err != nil {
		return err
	}
	r.defs[def.Type] = def
	return nil
}

func (r *Registry) MustRegister(def connector.Definition) {
	if err := r.Register(def); err != nil {
		panic(err)
	}
}

func (r *Registry) Get(t connector.Type) (connector.Definition, bool) {
	def, ok := r.defs[t]
	return def, ok
}

func (r *Registry) All() []connector.Definition {
	out := make([]connector.Definition, 0, len(r.defs))
	for _, def := range r.defs {
		out = append(out, def)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

func validate(def connector.Definition) error {
	if !typePattern.MatchString(string(def.Type)) {
		return fmt.Errorf("connector %q: type must match %s", def.Type, typePattern)
	}
	if def.Name == "" {
		return fmt.Errorf("connector %q: Name must not be empty", def.Type)
	}
	if def.ConfigSchemaVersion < 1 {
		return fmt.Errorf("connector %q: ConfigSchemaVersion must be >= 1", def.Type)
	}

	if err := validateFields(def.Type, "config", def.ConfigFields); err != nil {
		return err
	}

	authKeys := map[string]bool{}
	for _, a := range def.AuthMethods {
		if a.Key == "" {
			return fmt.Errorf("connector %q: auth method key must not be empty", def.Type)
		}
		if authKeys[a.Key] {
			return fmt.Errorf("connector %q: duplicate auth method %q", def.Type, a.Key)
		}
		authKeys[a.Key] = true
		if a.Type == connector.AuthOAuth2 && a.OAuth == nil {
			return fmt.Errorf("connector %q: auth method %q is oauth2 but has no OAuth config", def.Type, a.Key)
		}
		if a.Type != connector.AuthOAuth2 && a.OAuth != nil {
			return fmt.Errorf("connector %q: auth method %q is not oauth2 and must not carry an OAuth config", def.Type, a.Key)
		}
		if err := validateFields(
			def.Type,
			fmt.Sprintf("auth method %q credential", a.Key),
			a.CredentialFields,
		); err != nil {
			return err
		}
		switch a.Type {
		case connector.AuthAPIKey, connector.AuthCustomCredential:
			if len(a.CredentialFields) == 0 {
				return fmt.Errorf(
					"connector %q: auth method %q must declare CredentialFields",
					def.Type, a.Key,
				)
			}
			if !a.CredentialFields[0].Required {
				return fmt.Errorf(
					"connector %q: auth method %q must mark its first CredentialField as Required",
					def.Type, a.Key,
				)
			}
		case connector.AuthNone, connector.AuthOAuth2:
			if len(a.CredentialFields) != 0 {
				return fmt.Errorf(
					"connector %q: auth method %q must not declare CredentialFields",
					def.Type, a.Key,
				)
			}
		default:
			return fmt.Errorf(
				"connector %q: auth method %q has invalid type %q",
				def.Type, a.Key, a.Type,
			)
		}
		if a.OAuth != nil {
			switch a.OAuth.Mode {
			case "":
			case connector.OAuthModeMCP:
				if a.OAuth.AuthorizationEndpoint != "" || a.OAuth.TokenEndpoint != "" ||
					len(a.OAuth.Scopes) > 0 || a.OAuth.ScopeSeparator != "" ||
					a.OAuth.UsePKCE || a.OAuth.TokenEndpointAuth != "" ||
					len(a.OAuth.ExtraAuthParams) > 0 {
					return fmt.Errorf(
						"connector %q: auth method %q uses MCP OAuth and must not set static OAuth parameters",
						def.Type, a.Key,
					)
				}
			default:
				return fmt.Errorf(
					"connector %q: auth method %q has invalid OAuth mode %q",
					def.Type, a.Key, a.OAuth.Mode,
				)
			}
		}
	}

	switch impl := def.Implementation.(type) {
	case connector.RemoteMCP:
		if err := validateRemoteEndpoints(def, impl); err != nil {
			return err
		}
		if impl.AuthorizationScheme != "" &&
			!authorizationSchemePattern.MatchString(impl.AuthorizationScheme) {
			return fmt.Errorf("connector %q: invalid remote MCP AuthorizationScheme", def.Type)
		}
		if impl.RequestTimeout < 0 {
			return fmt.Errorf("connector %q: remote MCP RequestTimeout must not be negative", def.Type)
		}
		for _, method := range def.AuthMethods {
			if method.OAuth != nil && method.OAuth.Mode == connector.OAuthModeMCP &&
				impl.AuthorizationScheme != "" {
				return fmt.Errorf(
					"connector %q: MCP OAuth only supports the standard Bearer Authorization scheme",
					def.Type,
				)
			}
		}
	case connector.Managed:
		for _, method := range def.AuthMethods {
			if method.OAuth != nil && method.OAuth.Mode == connector.OAuthModeMCP {
				return fmt.Errorf(
					"connector %q: MCP OAuth is only valid for a remote MCP implementation",
					def.Type,
				)
			}
		}
		if len(impl.Tools) == 0 {
			return fmt.Errorf("connector %q: managed implementation needs at least one tool", def.Type)
		}
		toolNames := map[string]bool{}
		for _, managedTool := range impl.Tools {
			tool := managedTool.Tool
			if !toolNamePattern.MatchString(tool.Name) {
				return fmt.Errorf("connector %q: tool name %q must match %s", def.Type, tool.Name, toolNamePattern)
			}
			if toolNames[tool.Name] {
				return fmt.Errorf("connector %q: duplicate tool %q", def.Type, tool.Name)
			}
			toolNames[tool.Name] = true
			if managedTool.Handler == nil {
				return fmt.Errorf("connector %q: tool %q has no Handler", def.Type, tool.Name)
			}
			if err := validateObjectSchema(tool.InputSchema); err != nil {
				return fmt.Errorf("connector %q: tool %q InputSchema: %w", def.Type, tool.Name, err)
			}
			if tool.OutputSchema != nil {
				if err := validateObjectSchema(tool.OutputSchema); err != nil {
					return fmt.Errorf("connector %q: tool %q OutputSchema: %w", def.Type, tool.Name, err)
				}
			}
		}
	case nil:
		return fmt.Errorf("connector %q: Implementation is missing", def.Type)
	default:
		return fmt.Errorf("connector %q: Implementation must be a RemoteMCP or Managed value", def.Type)
	}

	seenFrom := map[int]bool{}
	for _, up := range def.ConfigUpgraders {
		if up.FromVersion < 1 || up.FromVersion >= def.ConfigSchemaVersion {
			return fmt.Errorf("connector %q: upgrader FromVersion %d out of range", def.Type, up.FromVersion)
		}
		if seenFrom[up.FromVersion] {
			return fmt.Errorf("connector %q: duplicate upgrader FromVersion %d", def.Type, up.FromVersion)
		}
		seenFrom[up.FromVersion] = true
	}
	return nil
}

func validateFields(t connector.Type, kind string, fields []connector.ConfigField) error {
	keys := map[string]bool{}
	for _, field := range fields {
		if field.Key == "" {
			return fmt.Errorf("connector %q: %s field key must not be empty", t, kind)
		}
		if keys[field.Key] {
			return fmt.Errorf("connector %q: duplicate %s field %q", t, kind, field.Key)
		}
		keys[field.Key] = true
		if field.Secret && field.DefaultValue != nil {
			return fmt.Errorf("connector %q: secret %s field %q must not have a default value", t, kind, field.Key)
		}
		switch field.InputType {
		case connector.InputText:
		case connector.InputSelect:
			if len(field.Validation.Options) == 0 {
				return fmt.Errorf(
					"connector %q: %s field %q has no select options",
					t, kind, field.Key,
				)
			}
			options := map[string]bool{}
			for _, option := range field.Validation.Options {
				if option == "" || options[option] {
					return fmt.Errorf(
						"connector %q: %s field %q has an invalid or duplicate select option %q",
						t, kind, field.Key, option,
					)
				}
				options[option] = true
			}
		default:
			return fmt.Errorf(
				"connector %q: %s field %q has invalid input type %q",
				t, kind, field.Key, field.InputType,
			)
		}
	}
	return nil
}

func validateRemoteEndpoints(def connector.Definition, remote connector.RemoteMCP) error {
	if remote.EndpointSelector == nil {
		if !validHTTPSURL(remote.Endpoint) {
			return fmt.Errorf("connector %q: remote MCP endpoint must be an https URL", def.Type)
		}
		return nil
	}
	if remote.Endpoint != "" {
		return fmt.Errorf("connector %q: remote MCP must set either Endpoint or EndpointSelector, not both", def.Type)
	}
	selector := remote.EndpointSelector
	if selector.ConfigField == "" || len(selector.Endpoints) == 0 {
		return fmt.Errorf("connector %q: remote MCP EndpointSelector is incomplete", def.Type)
	}
	var field *connector.ConfigField
	for i := range def.ConfigFields {
		if def.ConfigFields[i].Key == selector.ConfigField {
			field = &def.ConfigFields[i]
			break
		}
	}
	if field == nil || field.InputType != connector.InputSelect {
		return fmt.Errorf(
			"connector %q: endpoint selector field %q must be an InputSelect",
			def.Type, selector.ConfigField,
		)
	}
	options := make(map[string]bool, len(field.Validation.Options))
	for _, option := range field.Validation.Options {
		options[option] = true
		if _, ok := selector.Endpoints[option]; !ok {
			return fmt.Errorf(
				"connector %q: endpoint selector is missing option %q",
				def.Type, option,
			)
		}
	}
	for option, endpoint := range selector.Endpoints {
		if !options[option] {
			return fmt.Errorf(
				"connector %q: endpoint selector has undeclared option %q",
				def.Type, option,
			)
		}
		if !validHTTPSURL(endpoint) {
			return fmt.Errorf(
				"connector %q: endpoint selector %q must be an https URL",
				def.Type, option,
			)
		}
	}
	return nil
}

func validHTTPSURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Scheme == "https" && u.Host != "" &&
		u.User == nil && u.Fragment == ""
}

func validateObjectSchema(schema any) error {
	if schema == nil {
		return fmt.Errorf("must not be empty")
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("is not valid JSON: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return fmt.Errorf("must be a JSON object")
	}
	if object["type"] != "object" {
		return fmt.Errorf(`type must be "object"`)
	}
	return nil
}
