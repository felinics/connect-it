// Package registry 保存全部 ConnectorDefinition 并在注册时校验。
package registry

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"

	"github.com/memohai/connect-it/packages/core/connector"
)

var (
	typePattern   = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	toolIDPattern = regexp.MustCompile(`^[a-z0-9_]+$`)
)

type Registry struct {
	defs        map[connector.Type]connector.Definition
	handlerKeys map[connector.Type]map[string]bool
}

func New() *Registry {
	return &Registry{
		defs:        map[connector.Type]connector.Definition{},
		handlerKeys: map[connector.Type]map[string]bool{},
	}
}

// Register 校验并登记一个 Definition。
// managedHandlerKeys 是该 Connector 在 managed.go 中注册的 handler key 集合，
// 用于校验 ManagedBackend 引用的 handler 确实存在。
func (r *Registry) Register(def connector.Definition, managedHandlerKeys ...string) error {
	if _, exists := r.defs[def.Type]; exists {
		return fmt.Errorf("connector %q: type 重复注册", def.Type)
	}
	keys := map[string]bool{}
	for _, k := range managedHandlerKeys {
		keys[k] = true
	}
	if err := validate(def, keys); err != nil {
		return err
	}
	r.defs[def.Type] = def
	r.handlerKeys[def.Type] = keys
	return nil
}

func (r *Registry) MustRegister(def connector.Definition, managedHandlerKeys ...string) {
	if err := r.Register(def, managedHandlerKeys...); err != nil {
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

func validate(def connector.Definition, handlerKeys map[string]bool) error {
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

	serverKeys := map[string]bool{}
	for _, s := range def.RemoteMCPServers {
		if s.Key == "" {
			return fmt.Errorf("connector %q: MCP server key 不能为空", def.Type)
		}
		if serverKeys[s.Key] {
			return fmt.Errorf("connector %q: MCP server %q 重复", def.Type, s.Key)
		}
		serverKeys[s.Key] = true
		switch s.Endpoint.Source {
		case connector.EndpointFixed:
			u, err := url.Parse(s.Endpoint.URL)
			if err != nil || u.Scheme != "https" || u.Host == "" {
				return fmt.Errorf("connector %q: MCP server %q 的固定 endpoint 必须是 https URL", def.Type, s.Key)
			}
		case connector.EndpointConfigField:
			if s.Provenance.Kind != connector.ProvenanceSelfHosted {
				return fmt.Errorf("connector %q: MCP server %q 从配置取 endpoint 仅允许 self_hosted", def.Type, s.Key)
			}
			if !fieldKeys[s.Endpoint.ConfigFieldKey] {
				return fmt.Errorf("connector %q: MCP server %q 引用的配置字段 %q 不存在", def.Type, s.Key, s.Endpoint.ConfigFieldKey)
			}
		default:
			return fmt.Errorf("connector %q: MCP server %q 的 Endpoint.Source 非法", def.Type, s.Key)
		}
	}

	toolIDs := map[string]bool{}
	for _, tl := range def.Tools {
		if !toolIDPattern.MatchString(tl.ID) {
			return fmt.Errorf("connector %q: tool ID %q 必须匹配 %s", def.Type, tl.ID, toolIDPattern)
		}
		if toolIDs[tl.ID] {
			return fmt.Errorf("connector %q: tool %q 重复", def.Type, tl.ID)
		}
		toolIDs[tl.ID] = true
		switch b := tl.Backend.(type) {
		case connector.RemoteMCPBackend:
			if !serverKeys[b.ServerKey] {
				return fmt.Errorf("connector %q: tool %q 引用的 MCP server %q 不存在", def.Type, tl.ID, b.ServerKey)
			}
			if b.RemoteToolName == "" {
				return fmt.Errorf("connector %q: tool %q 缺少 RemoteToolName", def.Type, tl.ID)
			}
		case connector.ManagedBackend:
			if !handlerKeys[b.HandlerKey] {
				return fmt.Errorf("connector %q: tool %q 引用的 managed handler %q 未注册", def.Type, tl.ID, b.HandlerKey)
			}
		case nil:
			return fmt.Errorf("connector %q: tool %q 缺少 Backend", def.Type, tl.ID)
		default:
			return fmt.Errorf("connector %q: tool %q 的 Backend 类型未知", def.Type, tl.ID)
		}
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
