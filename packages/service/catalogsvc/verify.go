package catalogsvc

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/mcpclient"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/svcerr"
)

// MCPToolLister 是 mcp:verify 的上游探测依赖（真实装配为 mcpclient.Client）。
type MCPToolLister interface {
	ListTools(context.Context, mcpclient.ListRequest) ([]string, error)
}

// VerifyMCP 实测 Connector 的全部 Remote MCP server 并比对 tool 映射，成功时
// 记录 verified endpoint（固定 endpoint 的 Connector 记空）并清零 health。
// 失败分两类：配置类（缺 endpoint、URL 形态非法）只报错；探测类（握手失败、
// tool 映射缺失）另外 piggyback 写一次 health 失败。
func (s *Service) VerifyMCP(ctx context.Context, t connector.Type, tools MCPToolLister) error {
	def, ok := s.reg.Get(t)
	if !ok {
		return configsvc.ErrUnknownConnector
	}
	if len(def.RemoteMCPServers) == 0 {
		return svcerr.New(svcerr.VerifyFailed, "该 Connector 没有 Remote MCP server")
	}
	resolved, err := s.cfg.Resolved(ctx, t)
	if err != nil {
		return err
	}

	var configEndpoint *string
	authorizationID := uuid.New().String()
	for _, server := range def.RemoteMCPServers {
		endpoint := server.Endpoint.URL
		allowInsecure := "false"
		if server.Endpoint.Source == connector.EndpointConfigField {
			configured, _ := resolved[server.Endpoint.ConfigFieldKey].(string)
			if configured == "" {
				return svcerr.New(svcerr.VerifyFailed, fmt.Sprintf(
					"配置字段 %q 未填写 MCP endpoint", server.Endpoint.ConfigFieldKey))
			}
			if flag, ok := resolved["allow_insecure_http"].(string); ok && flag != "" {
				allowInsecure = flag
			}
			if err := mcpclient.CheckEndpoint(configured, allowInsecure == "true"); err != nil {
				return svcerr.New(svcerr.VerifyFailed, "MCP endpoint 配置无效")
			}
			endpoint = configured
			configEndpoint = &configured
		}
		// verify 发生在配置期、尚无用户凭证，用空 bearer 实测握手。
		names, err := tools.ListTools(ctx, mcpclient.ListRequest{
			ConnectorType:     t,
			Operation:         mcpclient.OperationVerify,
			AuthorizationID:   authorizationID,
			Server:            server,
			Endpoint:          endpoint,
			AllowInsecureHTTP: allowInsecure,
		})
		if err != nil {
			return s.probeFailure(ctx, t, "mcp_probe_failed", "MCP server 握手失败")
		}
		upstream := make(map[string]bool, len(names))
		for _, name := range names {
			upstream[name] = true
		}
		for _, tool := range def.Tools {
			backend, isRemote := tool.Backend.(connector.RemoteMCPBackend)
			if !isRemote || backend.ServerKey != server.Key {
				continue
			}
			if !upstream[backend.RemoteToolName] {
				return s.probeFailure(ctx, t, "mcp_tool_mapping_missing", "Remote MCP tool 映射缺失")
			}
		}
	}

	if err := s.q.SetConnectorConfigVerified(ctx, store.SetConnectorConfigVerifiedParams{
		ConnectorType: string(t), Endpoint: configEndpoint,
	}); err != nil {
		return err
	}
	_ = s.q.UpsertConnectorHealthSuccess(ctx, string(t))
	return nil
}

// probeFailure 记一次 health 失败后返回安全文案；health 里只留稳定错误码，
// Provider 原始错误既不入库也不出响应。
func (s *Service) probeFailure(ctx context.Context, t connector.Type, code, message string) error {
	storedError := fmt.Sprintf(`{"code":%q}`, code)
	_ = s.q.UpsertConnectorHealthFailure(ctx, store.UpsertConnectorHealthFailureParams{
		ConnectorType: string(t), LastError: &storedError,
	})
	return svcerr.New(svcerr.VerifyFailed, message)
}
