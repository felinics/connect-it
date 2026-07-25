// Package providerkit 是 Connector 共用的 Provider 运行时边界。
//
// 本包位于 core 之上、connectors 和 service 之下，只能依赖 Go 标准库与
// packages/core。Factory 由 api/cmd composition root 创建并注入调用方；具体
// Provider、数据库、HTTP 框架和 MCP server 不得进入本包。
//
// 本包提供由 Policy 绑定的受控 HTTP client、canonical origin 与 DNS/IP egress
// policy、认证、重定向防护、重试、响应限制和无敏感数据的观测能力。现有 Connector、
// OAuth 和 Remote MCP 调用路径由后续迁移阶段接入该边界。
package providerkit
