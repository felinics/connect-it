// Package github 是 GitHub Connector 的固定 Definition。
// 计划 1 阶段为 catalog-only：无 AuthMethods、无 Tools，仅进入 catalog。
package github

import "github.com/memohai/connect-it/packages/core/connector"

var Definition = connector.Definition{
	Type:                "github",
	Name:                "GitHub",
	Description:         "GitHub 代码托管与协作平台",
	Categories:          []string{"developer_tools"},
	HomepageURL:         "https://github.com",
	ConfigSchemaVersion: 1,
}
