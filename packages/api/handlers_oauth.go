package api

import (
	"errors"
	"html"
	"net/http"
	"net/url"

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
//	@Success	302
//	@Router		/v1/oauth/callback [get]
func (h *handlers) oauthCallback(c echo.Context) error {
	state, code := c.QueryParam("state"), c.QueryParam("code")

	// provider 直接报错（用户拒绝等）：state 有效时仍能取到调用方的回跳地址。
	if provErr := c.QueryParam("error"); provErr != "" {
		redirectURL := ""
		if state != "" {
			if result, err := h.deps.OAuth.RejectCallback(c.Request().Context(), state); err == nil {
				redirectURL = result.RedirectURL
			} else if !errors.Is(err, oauthsvc.ErrInvalidState) {
				c.Logger().Errorf("结束 OAuth 授权失败: %v", err)
			}
		}
		return h.finishCallback(c, redirectURL, "", provErr)
	}
	if state == "" || code == "" {
		return h.finishCallback(c, "", "", "invalid_callback")
	}

	result, err := h.deps.OAuth.HandleCallback(c.Request().Context(), state, code)
	if err != nil {
		c.Logger().Errorf("oauth 回调失败: %v", err)
		code := "oauth_failed"
		if err == oauthsvc.ErrInvalidState {
			code = "invalid_state"
		}
		return h.finishCallback(c, result.RedirectURL, "", code)
	}
	return h.finishCallback(c, result.RedirectURL, result.ConnectionID.String(), "")
}

// finishCallback 结束授权流程：调用方登记了 redirect_url 就带参数 302 回去；
// 没登记就渲染 connect-it 自己的极简完成页（终端用户看的）。
func (h *handlers) finishCallback(c echo.Context, redirectURL, connectionID, errCode string) error {
	if redirectURL != "" {
		u, err := url.Parse(redirectURL)
		if err == nil {
			q := u.Query()
			if errCode != "" {
				q.Set("status", "error")
				q.Set("code", errCode)
			} else {
				q.Set("status", "connected")
				q.Set("connection_id", connectionID)
			}
			u.RawQuery = q.Encode()
			return c.Redirect(http.StatusFound, u.String())
		}
	}
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
