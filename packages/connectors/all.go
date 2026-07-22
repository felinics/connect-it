// Package connectors 显式注册全部 provider 的 Definition 与 managed handler。
// 新增 Connector：加目录、加 definition.go（可选 managed.go）、在这里加一行。
package connectors

import (
	"github.com/memohai/connect-it/packages/connectors/github"
	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/core/registry"
)

// handlerSets 按 connector type 汇总各 provider 的 HandlerMap；
// 没有 managed tool 的 provider 不在此列。
var handlerSets = map[connector.Type]connector.HandlerMap{}

func RegisterAll(r *registry.Registry) {
	r.MustRegister(github.Definition, handlerKeys(handlerSets[github.Definition.Type])...)
}

// AllHandlers 返回全部 provider 的 managed handler，供 exec.Engine 装配。
func AllHandlers() map[connector.Type]connector.HandlerMap {
	out := make(map[connector.Type]connector.HandlerMap, len(handlerSets))
	for k, v := range handlerSets {
		out[k] = v
	}
	return out
}

func handlerKeys(m connector.HandlerMap) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
