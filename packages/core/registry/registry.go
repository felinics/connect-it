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
	typePattern     = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
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

	fieldKeys := map[string]bool{}
	for _, f := range def.ConfigFields {
		if f.Key == "" {
			return fmt.Errorf("connector %q: 配置字段 key 不能为空", def.Type)
		}
		if fieldKeys[f.Key] {
			return fmt.Errorf("connector %q: 配置字段 %q 重复", def.Type, f.Key)
		}
		fieldKeys[f.Key] = true
		if f.Secret && f.DefaultValue != nil {
			return fmt.Errorf("connector %q: Secret 字段 %q 不允许默认值", def.Type, f.Key)
		}
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
	}

	switch impl := def.Implementation.(type) {
	case connector.RemoteMCP:
		u, err := url.Parse(impl.Endpoint)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return fmt.Errorf("connector %q: remote MCP endpoint 必须是 https URL", def.Type)
		}
		if impl.RequestTimeout < 0 {
			return fmt.Errorf("connector %q: remote MCP RequestTimeout 不能为负数", def.Type)
		}
	case connector.Managed:
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
