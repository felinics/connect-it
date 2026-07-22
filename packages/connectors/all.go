// Package connectors 显式注册全部 provider 的 Definition。
// 新增 Connector：加目录、加 definition.go、在这里加一行。
package connectors

import (
	"github.com/memohai/connect-it/packages/connectors/github"
	"github.com/memohai/connect-it/packages/core/registry"
)

func RegisterAll(r *registry.Registry) {
	r.MustRegister(github.Definition)
}
