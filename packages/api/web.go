package api

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"
)

// webdist 是 packages/web 的构建产物（mise run build-web 拷入；仓库只提交占位页）。
//
//go:embed all:webdist
var webFS embed.FS

// registerWeb 挂载内嵌管理界面：静态文件直出，未命中的 GET 回退 index.html
// （SPA 路由）。已注册的 API 路由优先级高于本 catch-all。
func registerWeb(e *echo.Echo) {
	sub, err := fs.Sub(webFS, "webdist")
	if err != nil {
		panic(err)
	}
	fileServer := http.FileServer(http.FS(sub))
	e.GET("/*", func(c echo.Context) error {
		p := strings.TrimPrefix(c.Request().URL.Path, "/")
		if p != "" {
			if _, err := fs.Stat(sub, p); err != nil {
				c.Request().URL.Path = "/" // SPA 回退
			}
		}
		fileServer.ServeHTTP(c.Response(), c.Request())
		return nil
	})
}
