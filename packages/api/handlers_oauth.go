package api

import (
	"errors"
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
	redirect := func(query string) error {
		return c.Redirect(http.StatusFound, "/connections?"+query)
	}
	if provErr := c.QueryParam("error"); provErr != "" {
		return redirect("error=" + url.QueryEscape(provErr))
	}
	state, code := c.QueryParam("state"), c.QueryParam("code")
	if state == "" || code == "" {
		return redirect("error=invalid_callback")
	}
	connID, err := h.deps.OAuth.HandleCallback(c.Request().Context(), state, code)
	if err != nil {
		c.Logger().Errorf("oauth 回调失败: %v", err)
		if errors.Is(err, oauthsvc.ErrInvalidState) {
			return redirect("error=invalid_state")
		}
		return redirect("error=oauth_failed")
	}
	view, err := h.deps.Conns.Get(c.Request().Context(), connID)
	if err != nil {
		return redirect("error=oauth_failed")
	}
	return redirect("connected=" + url.QueryEscape(view.Alias))
}
