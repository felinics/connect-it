package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/connect-it/packages/core/buildinfo"
)

func TestGetVersion(t *testing.T) {
	previous := buildinfo.Version
	buildinfo.Version = "1.2.3"
	t.Cleanup(func() { buildinfo.Version = previous })

	e := echo.New()
	e.GET("/version", (&handlers{}).getVersion)
	req := httptest.NewRequest(http.MethodGet, "/version", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got, want := rec.Body.String(), "{\"version\":\"1.2.3\"}\n"; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}
