package api

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"

	"github.com/labstack/echo/v4"
)

var reservedWebPrefixes = [...]string{
	"/admin",
	"/healthz",
	"/mcp",
	"/swagger",
	"/v1",
	"/version",
}

func registerWeb(e *echo.Echo, web fs.FS) {
	if web == nil {
		return
	}

	e.GET("/*", func(c echo.Context) error {
		requestPath := c.Request().URL.Path
		name := strings.TrimPrefix(requestPath, "/")
		if name == "" {
			name = "index.html"
		}

		if fs.ValidPath(name) {
			info, err := fs.Stat(web, name)
			if err == nil && !info.IsDir() {
				body, err := fs.ReadFile(web, name)
				if err != nil {
					return err
				}
				return serveWebFile(c, name, body)
			}
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}

		if isReservedWebPath(requestPath) {
			return echo.ErrNotFound
		}

		body, err := fs.ReadFile(web, "index.html")
		if err != nil {
			return err
		}
		return serveWebFile(c, "index.html", body)
	})
}

func isReservedWebPath(path string) bool {
	for _, prefix := range reservedWebPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func serveWebFile(c echo.Context, name string, body []byte) error {
	contentType := mime.TypeByExtension(path.Ext(name))
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}
	if strings.HasPrefix(name, "assets/") {
		c.Response().Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		c.Response().Header().Set("Cache-Control", "no-cache")
	}
	return c.Blob(http.StatusOK, contentType, body)
}
