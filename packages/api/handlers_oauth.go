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
//	@Summary	OAuth callback where the provider redirects back; unauthenticated and secured by a single-use state
//	@ID			oauthCallback
//	@Tags		connections
//	@Param		state	query	string	false	"The state generated when the authorization started"
//	@Param		code	query	string	false	"Authorization code"
//	@Param		error	query	string	false	"Error code returned by the provider"
//	@Success	200
//	@Router		/v1/oauth/callback [get]
func (h *handlers) oauthCallback(c echo.Context) error {
	state, code := c.QueryParam("state"), c.QueryParam("code")

	// The provider reported an error, for example the user declined. When the
	// state is valid, end the matching authorization.
	if provErr := c.QueryParam("error"); provErr != "" {
		if state != "" {
			if err := h.deps.OAuth.RejectCallback(c.Request().Context(), state); err != nil &&
				!errors.Is(err, oauthsvc.ErrInvalidState) {
				c.Logger().Errorf("ending the OAuth authorization failed: %v", err)
			}
		}
		return h.finishCallback(c, provErr)
	}
	if state == "" || code == "" {
		return h.finishCallback(c, "invalid_callback")
	}

	err := h.deps.OAuth.HandleCallback(c.Request().Context(), state, code)
	if err != nil {
		c.Logger().Errorf("oauth callback failed: %v", err)
		code := "oauth_failed"
		if err == oauthsvc.ErrInvalidState {
			code = "invalid_state"
		}
		return h.finishCallback(c, code)
	}
	return h.finishCallback(c, "")
}

// finishCallback renders the completion page owned by connect-it. Trusted
// downstream services read the connection status to learn the outcome, so no
// second cross-service redirect is needed.
func (h *handlers) finishCallback(c echo.Context, errCode string) error {
	if errCode != "" {
		return c.HTML(http.StatusOK, callbackPage("Authorization failed", "Error code: "+errCode+". Please return to the original application and try again."))
	}
	return c.HTML(http.StatusOK, callbackPage("Authorization complete", "You can close this page and return to the original application."))
}

func callbackPage(title, body string) string {
	title = html.EscapeString(title)
	body = html.EscapeString(body)
	return `<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><title>` +
		title + `</title></head><body style="font-family:system-ui;display:flex;min-height:100vh;align-items:center;justify-content:center"><div style="text-align:center"><h1 style="font-size:18px">` +
		title + `</h1><p style="color:#666">` + body + `</p></div></body></html>`
}
