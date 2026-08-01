package api

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/labstack/echo/v4"
)

func newWebServer(web fs.FS) *echo.Echo {
	e := echo.New()
	e.GET("/healthz", func(c echo.Context) error {
		return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
	})
	registerWeb(e, web)
	return e
}

func requestWeb(t *testing.T, e *echo.Echo, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

func TestWebServesAssetsAndSPAFallback(t *testing.T) {
	e := newWebServer(fstest.MapFS{
		"index.html":    {Data: []byte("<html>connect-it</html>")},
		"assets/app.js": {Data: []byte("console.log('connect-it')")},
	})

	asset := requestWeb(t, e, "/assets/app.js")
	if asset.Code != http.StatusOK || asset.Body.String() != "console.log('connect-it')" {
		t.Fatalf("asset: status=%d body=%q", asset.Code, asset.Body.String())
	}
	if got := asset.Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Fatalf("asset cache control = %q", got)
	}

	spa := requestWeb(t, e, "/connections/123")
	if spa.Code != http.StatusOK || spa.Body.String() != "<html>connect-it</html>" {
		t.Fatalf("SPA fallback: status=%d body=%q", spa.Code, spa.Body.String())
	}

	directory := requestWeb(t, e, "/assets")
	if directory.Code != http.StatusOK || directory.Body.String() != "<html>connect-it</html>" {
		t.Fatalf("directory fallback: status=%d body=%q", directory.Code, directory.Body.String())
	}
}

func TestWebDoesNotHandleReservedAPIRoutes(t *testing.T) {
	e := newWebServer(fstest.MapFS{
		"index.html": {Data: []byte("<html>connect-it</html>")},
	})

	health := requestWeb(t, e, "/healthz")
	if health.Code != http.StatusOK || health.Body.String() == "<html>connect-it</html>" {
		t.Fatalf("health route: status=%d body=%q", health.Code, health.Body.String())
	}

	for _, path := range []string{
		"/admin/missing",
		"/mcp/missing",
		"/swagger/missing",
		"/v1/missing",
		"/version/missing",
	} {
		rec := requestWeb(t, e, path)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status=%d body=%q", path, rec.Code, rec.Body.String())
		}
	}
}
