// Package connectors 显式注册全部 provider 的 Definition 与 runtime。
// 新增 Connector：加目录与实现，并在 providerRegistrations 增加一个条目。
package connectors

import (
	"fmt"
	"maps"

	"github.com/memohai/connect-it/packages/connectors/datadog"
	"github.com/memohai/connect-it/packages/connectors/github"
	"github.com/memohai/connect-it/packages/connectors/gitlab"
	"github.com/memohai/connect-it/packages/connectors/gmail"
	"github.com/memohai/connect-it/packages/connectors/googleads"
	"github.com/memohai/connect-it/packages/connectors/linear"
	"github.com/memohai/connect-it/packages/connectors/notion"
	"github.com/memohai/connect-it/packages/connectors/onedrive"
	"github.com/memohai/connect-it/packages/connectors/stripe"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/registry"
)

// RuntimeBundle 是 connectors module 暴露给 composition root 的全部运行时实现。
// Definition 仍保持纯数据；service 只消费这些窄契约，不 import 具体 Provider。
type RuntimeBundle struct {
	Handlers      map[connector.Type]connector.HandlerMap
	Authorization connector.AuthorizationRuntime
}

type credentialValidatorConstructor func(
	*providerkit.Factory,
) (map[string]connector.CredentialValidator, error)

type handlerConstructor func(
	*providerkit.Factory,
) (connector.HandlerMap, error)

// providerRegistration 是 provider composition 的唯一清单。Definition 注册、
// runtime 构造与注册顺序都由这张强类型表派生。
type providerRegistration struct {
	def                     connector.Definition
	newCredentialValidators credentialValidatorConstructor
	newHandlers             handlerConstructor
	scopeMatchers           map[string]connector.ScopeMatcher
}

var providerRegistrations = [...]providerRegistration{
	{
		def:                     github.Definition,
		newCredentialValidators: github.NewCredentialValidators,
		scopeMatchers: map[string]connector.ScopeMatcher{
			"oauth": github.ScopeMatcher,
			"pat":   github.ScopeMatcher,
		},
	},
	{
		def:                     gitlab.Definition,
		newCredentialValidators: gitlab.NewCredentialValidators,
	},
	{
		def:                     gmail.Definition,
		newCredentialValidators: gmail.NewCredentialValidators,
		newHandlers:             gmail.NewHandlers,
	},
	{
		def:                     onedrive.Definition,
		newCredentialValidators: onedrive.NewCredentialValidators,
		newHandlers:             onedrive.NewHandlers,
	},
	{
		def:                     googleads.Definition,
		newCredentialValidators: googleads.NewCredentialValidators,
	},
	{
		def:                     datadog.Definition,
		newCredentialValidators: datadog.NewCredentialValidators,
		newHandlers:             datadog.NewHandlers,
	},
	{
		def:                     linear.Definition,
		newCredentialValidators: linear.NewCredentialValidators,
	},
	{
		def:                     notion.Definition,
		newCredentialValidators: notion.NewCredentialValidators,
	},
	{
		def:                     stripe.Definition,
		newCredentialValidators: stripe.NewCredentialValidators,
	},
}

// NewRuntime 使用 composition root 创建的 providerkit Factory 装配 Provider runtime。
func NewRuntime(factory *providerkit.Factory) (RuntimeBundle, error) {
	if factory == nil {
		return RuntimeBundle{}, fmt.Errorf("connectors: providerkit factory 不能为空")
	}
	runtime := RuntimeBundle{
		Handlers: make(map[connector.Type]connector.HandlerMap),
		Authorization: connector.AuthorizationRuntime{
			CredentialValidators: make(connector.CredentialValidatorMap),
			ScopeMatchers:        make(connector.ScopeMatcherMap),
		},
	}
	for _, registration := range providerRegistrations {
		typ := registration.def.Type
		if constructor := registration.newCredentialValidators; constructor != nil {
			validators, err := constructor(factory)
			if err != nil {
				return RuntimeBundle{}, fmt.Errorf(
					"connectors: 装配 %s credential validators: %w",
					typ,
					err,
				)
			}
			runtime.Authorization.CredentialValidators[typ] = validators
		}
		if constructor := registration.newHandlers; constructor != nil {
			handlers, err := constructor(factory)
			if err != nil {
				return RuntimeBundle{}, fmt.Errorf(
					"connectors: 装配 %s managed handlers: %w",
					typ,
					err,
				)
			}
			runtime.Handlers[typ] = handlers
		}
		if len(registration.scopeMatchers) != 0 {
			runtime.Authorization.ScopeMatchers[typ] = maps.Clone(
				registration.scopeMatchers,
			)
		}
	}
	if err := validateRuntimeBundle(runtime); err != nil {
		return RuntimeBundle{}, err
	}
	return runtime, nil
}

// RegisterAll 注册全部 Definition，并把各自的 handler key 传给 MustRegister 做引用校验。
func RegisterAll(r *registry.Registry, runtime RuntimeBundle) {
	if err := validateRuntimeBundle(runtime); err != nil {
		panic(err)
	}
	register := func(def connector.Definition) {
		keys := make([]string, 0, len(runtime.Handlers[def.Type]))
		for k := range runtime.Handlers[def.Type] {
			keys = append(keys, k)
		}
		r.MustRegister(def, keys...)
	}
	for _, registration := range providerRegistrations {
		register(registration.def)
	}
}

func definitions() []connector.Definition {
	defs := make([]connector.Definition, len(providerRegistrations))
	for i, registration := range providerRegistrations {
		defs[i] = registration.def
	}
	return defs
}

func validateRuntimeBundle(runtime RuntimeBundle) error {
	return validateRuntimeBundleForDefinitions(runtime, definitions())
}

func validateRuntimeBundleForDefinitions(
	runtime RuntimeBundle,
	defs []connector.Definition,
) error {
	if runtime.Handlers == nil {
		return fmt.Errorf("connectors: runtime handlers 不能为空")
	}
	authorization := runtime.Authorization

	definitionsByType := make(map[connector.Type]connector.Definition)
	for _, def := range defs {
		definitionsByType[def.Type] = def

		referencedHandlers := make(map[string]bool)
		for _, tool := range def.Tools {
			backend, managed := tool.Backend.(connector.ManagedBackend)
			if !managed {
				continue
			}
			referencedHandlers[backend.HandlerKey] = true
			handler := runtime.Handlers[def.Type][backend.HandlerKey]
			if handler == nil {
				return fmt.Errorf(
					"connector %q: managed handler %q 未装配",
					def.Type,
					backend.HandlerKey,
				)
			}
		}
		for key, handler := range runtime.Handlers[def.Type] {
			if handler == nil {
				return fmt.Errorf(
					"connector %q: managed handler %q 不能为空",
					def.Type,
					key,
				)
			}
			if !referencedHandlers[key] {
				return fmt.Errorf(
					"connector %q: managed handler %q 没有 Definition Tool 引用",
					def.Type,
					key,
				)
			}
		}

		authMethods := make(map[string]connector.AuthMethod)
		for _, method := range def.AuthMethods {
			authMethods[method.Key] = method
			validator := authorization.CredentialValidators[def.Type][method.Key]
			switch method.Type {
			case connector.AuthNone:
				if validator != nil {
					return fmt.Errorf(
						"connector %q: auth method %q 不得注册 credential validator",
						def.Type,
						method.Key,
					)
				}
			default:
				if validator == nil {
					return fmt.Errorf(
						"connector %q: auth method %q 缺少 credential validator",
						def.Type,
						method.Key,
					)
				}
			}
		}
		for key, validator := range authorization.CredentialValidators[def.Type] {
			method, exists := authMethods[key]
			if !exists ||
				method.Type == connector.AuthNone {
				return fmt.Errorf(
					"connector %q: credential validator %q 没有可验证的 AuthMethod",
					def.Type,
					key,
				)
			}
			if validator == nil {
				return fmt.Errorf(
					"connector %q: credential validator %q 不能为空",
					def.Type,
					key,
				)
			}
		}
		for key, matcher := range authorization.ScopeMatchers[def.Type] {
			method, exists := authMethods[key]
			if !exists {
				return fmt.Errorf(
					"connector %q: scope matcher %q 没有对应 AuthMethod",
					def.Type,
					key,
				)
			}
			switch method.Type {
			case connector.AuthNone:
				return fmt.Errorf(
					"connector %q: AuthNone method %q 不得注册 scope matcher",
					def.Type,
					key,
				)
			case connector.AuthAPIKey,
				connector.AuthCustomCredential,
				connector.AuthOAuth2:
				if authorization.CredentialValidators[def.Type][key] == nil {
					return fmt.Errorf(
						"connector %q: scope matcher %q 没有 credential validator",
						def.Type,
						key,
					)
				}
			default:
				return fmt.Errorf(
					"connector %q: scope matcher %q 对应不支持的 AuthMethod 类型 %q",
					def.Type,
					key,
					method.Type,
				)
			}
			if matcher == nil {
				return fmt.Errorf(
					"connector %q: scope matcher %q 不能为空",
					def.Type,
					key,
				)
			}
		}
	}

	for typ := range runtime.Handlers {
		if _, exists := definitionsByType[typ]; !exists {
			return fmt.Errorf(
				"connector %q: runtime handlers 没有对应 Definition",
				typ,
			)
		}
	}
	for typ := range authorization.CredentialValidators {
		if _, exists := definitionsByType[typ]; !exists {
			return fmt.Errorf(
				"connector %q: credential validators 没有对应 Definition",
				typ,
			)
		}
	}
	for typ := range authorization.ScopeMatchers {
		if _, exists := definitionsByType[typ]; !exists {
			return fmt.Errorf(
				"connector %q: scope matchers 没有对应 Definition",
				typ,
			)
		}
	}
	return nil
}
