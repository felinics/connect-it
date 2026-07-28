package api

import (
	"errors"
	"html"
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/memohai/connect-it/packages/service/oauthsvc"
)

// oauthCallback godoc
//
//	@Summary	OAuth 回调（provider 跳转回来，无鉴权，靠一次性 state）
//	@ID			oauthCallback
//	@Tags		connections
//	@Param		state	query	string	false	"授权发起时生成的 state"
//	@Param		code	query	string	false	"授权码"
//	@Param		error	query	string	false	"provider 返回的错误码"
//	@Success	200
//	@Router		/v1/oauth/callback [get]
func (h *handlers) oauthCallback(c echo.Context) error {
	state, code := c.QueryParam("state"), c.QueryParam("code")

	// provider 直接报错（用户拒绝等）：state 有效时结束对应授权。
	if provErr := c.QueryParam("error"); provErr != "" {
		if state != "" {
			if err := h.deps.OAuth.RejectCallback(c.Request().Context(), state); err != nil &&
				!errors.Is(err, oauthsvc.ErrInvalidState) {
				c.Logger().Errorf("结束 OAuth 授权失败: %v", err)
			}
		}
		return h.finishCallback(c, provErr)
	}
	if state == "" || code == "" {
		return h.finishCallback(c, "invalid_callback")
	}

	err := h.deps.OAuth.HandleCallback(c.Request().Context(), state, code)
	if err != nil {
		c.Logger().Errorf("oauth 回调失败: %v", err)
		code := "oauth_failed"
		if err == oauthsvc.ErrInvalidState {
			code = "invalid_state"
		}
		return h.finishCallback(c, code)
	}
	return h.finishCallback(c, "")
}

// finishCallback 渲染 connect-it 自己的完成页。可信下游通过 connection
// 状态判断授权结果，不需要第二次跨服务回跳。
func (h *handlers) finishCallback(c echo.Context, errCode string) error {
	if errCode != "" {
		return c.HTML(http.StatusOK, callbackPage("授权失败", "错误代码："+errCode+"，请回到原应用重试。"))
	}
	return c.HTML(http.StatusOK, callbackPage("授权完成", "现在可以关闭本页，回到原应用继续。"))
}

func callbackPage(title, body string) string {
	title = html.EscapeString(title)
	body = html.EscapeString(body)
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>` +
		title + `</title></head><body style="font-family:system-ui;display:flex;min-height:100vh;align-items:center;justify-content:center"><div style="text-align:center"><h1 style="font-size:18px">` +
		title + `</h1><p style="color:#666">` + body + `</p></div></body></html>`
}
