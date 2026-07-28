// Package registry 保存全部 ConnectorDefinition 并在注册时校验。
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

// Register 校验并登记一个 Definition。
func (r *Registry) Register(def connector.Definition) error {
	if _, exists := r.defs[def.Type]; exists {
		return fmt.Errorf("connector %q: type 重复注册", def.Type)
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
		return fmt.Errorf("connector %q: type 必须匹配 %s", def.Type, typePattern)
	}
	if def.Name == "" {
		return fmt.Errorf("connector %q: Name 不能为空", def.Type)
	}
	if def.ConfigSchemaVersion < 1 {
		return fmt.Errorf("connector %q: ConfigSchemaVersion 必须 >= 1", def.Type)
	}

	if err := validateFields(def.Type, "配置", def.ConfigFields); err != nil {
		return err
	}

	authKeys := map[string]bool{}
	for _, a := range def.AuthMethods {
		if a.Key == "" {
			return fmt.Errorf("connector %q: auth method key 不能为空", def.Type)
		}
		if authKeys[a.Key] {
			return fmt.Errorf("connector %q: auth method %q 重复", def.Type, a.Key)
		}
		authKeys[a.Key] = true
		if a.Type == connector.AuthOAuth2 && a.OAuth == nil {
			return fmt.Errorf("connector %q: auth method %q 是 oauth2 但缺少 OAuth 配置", def.Type, a.Key)
		}
		if a.Type != connector.AuthOAuth2 && a.OAuth != nil {
			return fmt.Errorf("connector %q: auth method %q 不是 oauth2 不允许携带 OAuth 配置", def.Type, a.Key)
		}
		if err := validateFields(
			def.Type,
			fmt.Sprintf("auth method %q 凭证", a.Key),
			a.CredentialFields,
		); err != nil {
			return err
		}
		switch a.Type {
		case connector.AuthAPIKey, connector.AuthCustomCredential:
			if len(a.CredentialFields) == 0 {
				return fmt.Errorf(
					"connector %q: auth method %q 必须声明 CredentialFields",
					def.Type, a.Key,
				)
			}
			if !a.CredentialFields[0].Required {
				return fmt.Errorf(
					"connector %q: auth method %q 的首个 CredentialField 必须 Required",
					def.Type, a.Key,
				)
			}
		case connector.AuthNone, connector.AuthOAuth2:
			if len(a.CredentialFields) != 0 {
				return fmt.Errorf(
					"connector %q: auth method %q 不允许声明 CredentialFields",
					def.Type, a.Key,
				)
			}
		default:
			return fmt.Errorf(
				"connector %q: auth method %q 的 type %q 非法",
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
						"connector %q: auth method %q 使用 MCP OAuth 时不能配置静态 OAuth 参数",
						def.Type, a.Key,
					)
				}
			default:
				return fmt.Errorf(
					"connector %q: auth method %q 的 OAuth mode %q 非法",
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
			return fmt.Errorf("connector %q: remote MCP AuthorizationScheme 非法", def.Type)
		}
		if impl.RequestTimeout < 0 {
			return fmt.Errorf("connector %q: remote MCP RequestTimeout 不能为负数", def.Type)
		}
		for _, method := range def.AuthMethods {
			if method.OAuth != nil && method.OAuth.Mode == connector.OAuthModeMCP &&
				impl.AuthorizationScheme != "" {
				return fmt.Errorf(
					"connector %q: MCP OAuth 只支持标准 Bearer Authorization scheme",
					def.Type,
				)
			}
		}
	case connector.Managed:
		for _, method := range def.AuthMethods {
			if method.OAuth != nil && method.OAuth.Mode == connector.OAuthModeMCP {
				return fmt.Errorf(
					"connector %q: MCP OAuth 只能用于 remote MCP implementation",
					def.Type,
				)
			}
		}
		if len(impl.Tools) == 0 {
			return fmt.Errorf("connector %q: managed implementation 至少需要一个 tool", def.Type)
		}
		toolNames := map[string]bool{}
		for _, managedTool := range impl.Tools {
			tool := managedTool.Tool
			if !toolNamePattern.MatchString(tool.Name) {
				return fmt.Errorf("connector %q: tool name %q 必须匹配 %s", def.Type, tool.Name, toolNamePattern)
			}
			if toolNames[tool.Name] {
				return fmt.Errorf("connector %q: tool %q 重复", def.Type, tool.Name)
			}
			toolNames[tool.Name] = true
			if managedTool.Handler == nil {
				return fmt.Errorf("connector %q: tool %q 缺少 Handler", def.Type, tool.Name)
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
		return fmt.Errorf("connector %q: 缺少 Implementation", def.Type)
	default:
		return fmt.Errorf("connector %q: Implementation 必须是 RemoteMCP 或 Managed 值", def.Type)
	}

	seenFrom := map[int]bool{}
	for _, up := range def.ConfigUpgraders {
		if up.FromVersion < 1 || up.FromVersion >= def.ConfigSchemaVersion {
			return fmt.Errorf("connector %q: upgrader FromVersion %d 越界", def.Type, up.FromVersion)
		}
		if seenFrom[up.FromVersion] {
			return fmt.Errorf("connector %q: upgrader FromVersion %d 重复", def.Type, up.FromVersion)
		}
		seenFrom[up.FromVersion] = true
	}
	return nil
}

func validateFields(t connector.Type, kind string, fields []connector.ConfigField) error {
	keys := map[string]bool{}
	for _, field := range fields {
		if field.Key == "" {
			return fmt.Errorf("connector %q: %s字段 key 不能为空", t, kind)
		}
		if keys[field.Key] {
			return fmt.Errorf("connector %q: %s字段 %q 重复", t, kind, field.Key)
		}
		keys[field.Key] = true
		if field.Secret && field.DefaultValue != nil {
			return fmt.Errorf("connector %q: %s Secret 字段 %q 不允许默认值", t, kind, field.Key)
		}
		switch field.InputType {
		case connector.InputText:
		case connector.InputSelect:
			if len(field.Validation.Options) == 0 {
				return fmt.Errorf(
					"connector %q: %s字段 %q 的 select options 不能为空",
					t, kind, field.Key,
				)
			}
			options := map[string]bool{}
			for _, option := range field.Validation.Options {
				if option == "" || options[option] {
					return fmt.Errorf(
						"connector %q: %s字段 %q 的 select option %q 非法或重复",
						t, kind, field.Key, option,
					)
				}
				options[option] = true
			}
		default:
			return fmt.Errorf(
				"connector %q: %s字段 %q 的 input type %q 非法",
				t, kind, field.Key, field.InputType,
			)
		}
	}
	return nil
}

func validateRemoteEndpoints(def connector.Definition, remote connector.RemoteMCP) error {
	if remote.EndpointSelector == nil {
		if !validHTTPSURL(remote.Endpoint) {
			return fmt.Errorf("connector %q: remote MCP endpoint 必须是 https URL", def.Type)
		}
		return nil
	}
	if remote.Endpoint != "" {
		return fmt.Errorf("connector %q: remote MCP 只能配置 Endpoint 或 EndpointSelector", def.Type)
	}
	selector := remote.EndpointSelector
	if selector.ConfigField == "" || len(selector.Endpoints) == 0 {
		return fmt.Errorf("connector %q: remote MCP EndpointSelector 不完整", def.Type)
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
			"connector %q: endpoint selector 字段 %q 必须是 InputSelect",
			def.Type, selector.ConfigField,
		)
	}
	options := make(map[string]bool, len(field.Validation.Options))
	for _, option := range field.Validation.Options {
		options[option] = true
		if _, ok := selector.Endpoints[option]; !ok {
			return fmt.Errorf(
				"connector %q: endpoint selector 缺少 option %q",
				def.Type, option,
			)
		}
	}
	for option, endpoint := range selector.Endpoints {
		if !options[option] {
			return fmt.Errorf(
				"connector %q: endpoint selector 包含未声明 option %q",
				def.Type, option,
			)
		}
		if !validHTTPSURL(endpoint) {
			return fmt.Errorf(
				"connector %q: endpoint selector %q 必须是 https URL",
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
		return fmt.Errorf("不能为空")
	}
	data, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("不是合法 JSON: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(data, &object); err != nil || object == nil {
		return fmt.Errorf("必须是 JSON object")
	}
	if object["type"] != "object" {
		return fmt.Errorf(`type 必须是 "object"`)
	}
	return nil
}
