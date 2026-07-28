package api

import (
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/core/connector"
)

type configFieldDTO struct {
	Key          string   `json:"key"`
	Label        string   `json:"label"`
	InputType    string   `json:"input_type"`
	Required     bool     `json:"required"`
	Secret       bool     `json:"secret"`
	DefaultValue *string  `json:"default_value"`
	Description  string   `json:"description"`
	Pattern      string   `json:"pattern"`
	Options      []string `json:"options"`
}

type configResponse struct {
	ConnectorType string         `json:"connector_type"`
	SchemaVersion int            `json:"schema_version"`
	Public        map[string]any `json:"public"`
	SecretKeysSet []string       `json:"secret_keys_set"`
	UpdatedAt     time.Time      `json:"updated_at"`
}

type putConfigRequest struct {
	Public  map[string]any    `json:"public"`
	Secrets map[string]string `json:"secrets"`
	IfMatch string            `json:"if_match"`
}

type authMethodDTO struct {
	Key              string           `json:"key"`
	Label            string           `json:"label"`
	Type             string           `json:"type"`
	CredentialFields []configFieldDTO `json:"credential_fields"`
}

// listAuthMethods godoc
//
//	@Summary	Connector 的认证方式与凭证字段（供创建 connection 的表单）
//	@ID			listAuthMethods
//	@Tags		admin
//	@Produce	json
//	@Param		type	path		string	true	"connector_type"
//	@Success	200		{array}		api.authMethodDTO
//	@Failure	404		{object}	api.ErrorResponse
//	@Router		/admin/connectors/{type}/auth-methods [get]
func (h *handlers) listAuthMethods(c echo.Context) error {
	def, ok := h.deps.Registry.Get(connector.Type(c.Param("type")))
	if !ok {
		return writeError(c, http.StatusNotFound, "not_found", "资源不存在")
	}
	out := make([]authMethodDTO, 0, len(def.AuthMethods))
	for _, m := range def.AuthMethods {
		fields := make([]configFieldDTO, 0, len(m.CredentialFields))
		for _, f := range m.CredentialFields {
			options := f.Validation.Options
			if options == nil {
				options = []string{}
			}
			fields = append(fields, configFieldDTO{
				Key: f.Key, Label: f.Label, InputType: string(f.InputType),
				Required: f.Required, Secret: f.Secret, DefaultValue: f.DefaultValue,
				Description: f.Description, Pattern: f.Validation.Pattern, Options: options,
			})
		}
		out = append(out, authMethodDTO{
			Key: m.Key, Label: m.Label, Type: string(m.Type), CredentialFields: fields,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// getConfigSchema godoc
//
//	@Summary	Connector 配置表单元数据（由 Definition 的 ConfigFields 生成）
//	@ID			getConfigSchema
//	@Tags		admin
//	@Produce	json
//	@Param		type	path		string	true	"connector_type"
//	@Success	200		{array}		api.configFieldDTO
//	@Failure	404		{object}	api.ErrorResponse
//	@Router		/admin/connectors/{type}/config-schema [get]
func (h *handlers) getConfigSchema(c echo.Context) error {
	def, ok := h.deps.Registry.Get(connector.Type(c.Param("type")))
	if !ok {
		return writeError(c, http.StatusNotFound, "not_found", "资源不存在")
	}
	out := make([]configFieldDTO, 0, len(def.ConfigFields))
	for _, f := range def.ConfigFields {
		options := f.Validation.Options
		if options == nil {
			options = []string{}
		}
		out = append(out, configFieldDTO{
			Key:          f.Key,
			Label:        f.Label,
			InputType:    string(f.InputType),
			Required:     f.Required,
			Secret:       f.Secret,
			DefaultValue: f.DefaultValue,
			Description:  f.Description,
			Pattern:      f.Validation.Pattern,
			Options:      options,
		})
	}
	return c.JSON(http.StatusOK, out)
}

// getConfig godoc
//
//	@Summary	读取 Connector 配置（Secret 只回显已设置的 key）
//	@ID			getConfig
//	@Tags		admin
//	@Produce	json
//	@Param		type	path		string	true	"connector_type"
//	@Success	200		{object}	api.configResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Router		/admin/connectors/{type}/config [get]
func (h *handlers) getConfig(c echo.Context) error {
	view, err := h.deps.Config.Get(c.Request().Context(), connector.Type(c.Param("type")))
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, configResponse(view))
}

// putConfig godoc
//
//	@Summary	写入 Connector 配置（public 全量替换、secrets 增量合并，空串删除）
//	@ID			putConfig
//	@Tags		admin
//	@Accept		json
//	@Produce	json
//	@Param		type	path		string					true	"connector_type"
//	@Param		body	body		api.putConfigRequest	true	"配置内容；if_match 传上次读到的 updated_at（RFC3339），首次创建留空"
//	@Success	200		{object}	api.configResponse
//	@Failure	404		{object}	api.ErrorResponse
//	@Failure	409		{object}	api.ErrorResponse
//	@Failure	422		{object}	api.ErrorResponse
//	@Router		/admin/connectors/{type}/config [put]
func (h *handlers) putConfig(c echo.Context) error {
	var req putConfigRequest
	if err := c.Bind(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON")
	}
	var ifMatch time.Time
	if req.IfMatch != "" {
		ts, err := time.Parse(time.RFC3339Nano, req.IfMatch)
		if err != nil {
			return writeError(c, http.StatusUnprocessableEntity, "validation_failed", "if_match 不是 RFC3339 时间")
		}
		ifMatch = ts
	}
	view, err := h.deps.Config.Put(c.Request().Context(),
		connector.Type(c.Param("type")), req.Public, req.Secrets, ifMatch)
	if err != nil {
		return mapServiceError(c, err)
	}
	return c.JSON(http.StatusOK, configResponse(view))
}

// deleteConfig godoc
//
//	@Summary	删除 Connector 配置
//	@ID			deleteConfig
//	@Tags		admin
//	@Param		type	path	string	true	"connector_type"
//	@Success	204
//	@Failure	404	{object}	api.ErrorResponse
//	@Router		/admin/connectors/{type}/config [delete]
func (h *handlers) deleteConfig(c echo.Context) error {
	if err := h.deps.Config.Delete(c.Request().Context(), connector.Type(c.Param("type"))); err != nil {
		return mapServiceError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
