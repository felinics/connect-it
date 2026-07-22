// Package connectors 显式注册全部 provider 的 Definition 与 Managed handler。
// 新增 Connector：加目录、加 definition.go（及可选 managed.go）、在这里加注册行。
package connectors

import (
	"github.com/memohai/connect-it/packages/connectors/github"
	"github.com/memohai/connect-it/packages/connectors/gmail"
	"github.com/memohai/connect-it/packages/connectors/googleads"
	"github.com/memohai/connect-it/packages/connectors/onedrive"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

// AllHandlers 返回各 connector type 的 Managed handler 映射（执行引擎消费）。
func AllHandlers() map[connector.Type]connector.HandlerMap {
	return map[connector.Type]connector.HandlerMap{
		gmail.Definition.Type:    gmail.Handlers,
		onedrive.Definition.Type: onedrive.Handlers,
	}
}

// RegisterAll 注册全部 Definition，并把各自的 handler key 传给 MustRegister 做引用校验。
func RegisterAll(r *registry.Registry) {
	handlers := AllHandlers()
	register := func(def connector.Definition) {
		keys := make([]string, 0, len(handlers[def.Type]))
		for k := range handlers[def.Type] {
			keys = append(keys, k)
		}
		r.MustRegister(def, keys...)
	}
	register(github.Definition)
	register(gmail.Definition)
	register(onedrive.Definition)
	register(googleads.Definition)
}
