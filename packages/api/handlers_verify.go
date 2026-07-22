package api

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/connector"
	"github.com/memohai/connect-it/packages/service/mcpclient"
	"github.com/memohai/connect-it/packages/service/store"
)

// MCPToolLister 是 mcp:verify 的上游探测依赖（真实装配为 mcpclient.Client）。
type MCPToolLister interface {
	ListTools(ctx context.Context, endpoint, bearerToken string, timeout time.Duration) ([]string, error)
}

// verifyMCP godoc
//
//	@Summary	实测 Connector 的全部 Remote MCP server 并比对 tool 映射
//	@ID			verifyMcp
//	@Tags		admin
//	@Param		type	path	string	true	"connector_type"
//	@Success	204
//	@Failure	404	{object}	api.ErrorResponse
//	@Failure	422	{object}	api.ErrorResponse
//	@Router		/admin/connectors/{type}/mcp:verify [post]
func (h *handlers) verifyMCP(c echo.Context) error {
	ctx := c.Request().Context()
	t := connector.Type(c.Param("type"))
	def, ok := h.deps.Registry.Get(t)
	if !ok {
		return writeError(c, http.StatusNotFound, "not_found", "资源不存在")
	}
	if len(def.RemoteMCPServers) == 0 {
		return writeError(c, http.StatusUnprocessableEntity, "verify_failed", "该 Connector 没有 Remote MCP server")
	}
	resolved, err := h.deps.Config.Resolved(ctx, t)
	if err != nil {
		return mapServiceError(c, err)
	}

	// 记录 config-field endpoint（写入 verified 字段用）；固定 endpoint 的
	// Connector 验证成功时 verified_endpoint 为空。
	// 错误分两类：配置类（缺 endpoint、URL 形态非法）只报 422 不写 health；
	// 探测类（握手失败、tool 缺失）另外 piggyback 写 health 失败。
	var configEndpoint *string
	var probeErr, configErr error
	for _, server := range def.RemoteMCPServers {
		endpoint := server.Endpoint.URL
		if server.Endpoint.Source == connector.EndpointConfigField {
			v, _ := resolved[server.Endpoint.ConfigFieldKey].(string)
			if v == "" {
				configErr = fmt.Errorf("配置字段 %q 未填写 MCP endpoint", server.Endpoint.ConfigFieldKey)
				break
			}
			allowInsecure, _ := resolved["allow_insecure_http"].(string)
			if err := mcpclient.CheckEndpoint(v, allowInsecure == "true"); err != nil {
				configErr = err
				break
			}
			endpoint = v
			configEndpoint = &v
		}
		timeout := server.RequestTimeout
		if timeout <= 0 {
			timeout = 30 * time.Second
		}
		// verify 发生在配置期、尚无用户凭证，用空 bearer 实测握手。
		names, err := h.deps.MCPTools.ListTools(ctx, endpoint, "", timeout)
		if err != nil {
			probeErr = fmt.Errorf("server %q 握手失败: %v", server.Key, err)
			break
		}
		upstream := map[string]bool{}
		for _, n := range names {
			upstream[n] = true
		}
		for _, tool := range def.Tools {
			b, isRemote := tool.Backend.(connector.RemoteMCPBackend)
			if !isRemote || b.ServerKey != server.Key {
				continue
			}
			if !upstream[b.RemoteToolName] {
				probeErr = fmt.Errorf("server %q 缺少 tool %q（映射自 %s）", server.Key, b.RemoteToolName, tool.ID)
				break
			}
		}
		if probeErr != nil {
			break
		}
	}

	if configErr != nil {
		return writeError(c, http.StatusUnprocessableEntity, "verify_failed", configErr.Error())
	}
	if probeErr != nil {
		msg := probeErr.Error()
		_ = h.deps.Store.UpsertConnectorHealthFailure(ctx, store.UpsertConnectorHealthFailureParams{
			ConnectorType: string(t), LastError: &msg,
		})
		return writeError(c, http.StatusUnprocessableEntity, "verify_failed", msg)
	}

	if err := h.deps.Store.SetConnectorConfigVerified(ctx, store.SetConnectorConfigVerifiedParams{
		ConnectorType: string(t), Endpoint: configEndpoint,
	}); err != nil {
		return mapServiceError(c, err)
	}
	_ = h.deps.Store.UpsertConnectorHealthSuccess(ctx, string(t))
	return c.NoContent(http.StatusNoContent)
}
