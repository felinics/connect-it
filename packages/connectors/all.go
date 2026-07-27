// Package connectors 显式注册全部 provider 的 Definition。
// 新增 Connector：加目录、加 definition.go（及可选 managed.go）、在这里加注册行。
package connectors

import (
	"github.com/memohai/connect-it/packages/connectors/github"
	"github.com/memohai/connect-it/packages/connectors/gmail"
	"github.com/memohai/connect-it/packages/connectors/googleads"
	"github.com/memohai/connect-it/packages/connectors/onedrive"
	"github.com/memohai/connect-it/packages/connectors/youtube"
	"github.com/memohai/connect-it/packages/core/registry"
)

// RegisterAll 注册全部 Definition。
func RegisterAll(r *registry.Registry) {
	r.MustRegister(github.Definition)
	r.MustRegister(gmail.Definition)
	r.MustRegister(onedrive.Definition)
	r.MustRegister(googleads.Definition)
	r.MustRegister(youtube.Definition)
}
