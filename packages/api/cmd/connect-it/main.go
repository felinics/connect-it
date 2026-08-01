// Command connect-it is the API server entry point.
//
//	@title						connect-it API
//	@version					1.0
//	@description				Self-hosted connector gateway: catalog, config management, OAuth connections and aggregated MCP.
//	@BasePath					/
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
package main

import (
	"context"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/api"
	connectors "github.com/memohai/connect-it/packages/connectors"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	service "github.com/memohai/connect-it/packages/service"
	"github.com/memohai/connect-it/packages/service/authsvc"
	"github.com/memohai/connect-it/packages/service/catalogsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/mcpclient"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/tokens"
)

func main() {
	ctx := context.Background()

	dbURL := mustEnv("DATABASE_URL")
	keySpec := mustEnv(crypto.EnvSecretKey)
	cookieSecret := mustEnv("COOKIE_SECRET")
	baseURL := mustEnv("CONNECT_IT_BASE_URL")
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8421"
	}
	web := loadWebFS()

	keyring, err := crypto.ParseKeyring(keySpec)
	if err != nil {
		log.Fatalf("parse %s: %v", crypto.EnvSecretKey, err)
	}

	if err := service.MigrateUp(dbURL); err != nil {
		log.Fatalf("database migration: %v", err)
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("connect to database: %v", err)
	}
	defer pool.Close()

	reg := registry.New()
	connectors.RegisterAll(reg)

	queries := store.New(pool)
	configSvc := configsvc.New(queries, reg, keyring)
	authSvc := authsvc.New(queries)
	catalogSvc := catalogsvc.New(reg, configSvc)
	httpClient := &http.Client{Timeout: 30 * time.Second}
	oauthSvc := oauthsvc.New(queries, reg, configSvc, keyring, httpClient, baseURL)
	connSvc := connsvc.New(queries, reg, keyring)
	refresher := tokens.New(queries, reg, configSvc, keyring, httpClient)
	engine := exec.New(queries, reg, configSvc, refresher, keyring, mcpclient.Client{})

	if err := authSvc.EnsureAdminFromEnv(ctx); err != nil {
		log.Fatalf("initialize admin account: %v", err)
	}
	if err := authSvc.EnsureBootstrapTokenFromEnv(ctx); err != nil {
		log.Fatalf("initialize bootstrap API token: %v", err)
	}

	e := api.New(api.Deps{
		Registry:     reg,
		Store:        queries,
		Config:       configSvc,
		Catalog:      catalogSvc,
		Auth:         authSvc,
		OAuth:        oauthSvc,
		Conns:        connSvc,
		Exec:         engine,
		Sessions:     sessions.New(queries, engine),
		CookieSecret: []byte(cookieSecret),
		Web:          web,
	})
	log.Printf("connect-it listening on %s", addr)
	log.Fatal(e.Start(addr))
}

func loadWebFS() fs.FS {
	dir := os.Getenv("CONNECT_IT_WEB_DIR")
	if dir == "" {
		return nil
	}
	web := os.DirFS(dir)
	if _, err := fs.Stat(web, "index.html"); err != nil {
		log.Fatalf("load admin UI from %s: %v", dir, err)
	}
	return web
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("missing required environment variable %s", name)
	}
	return v
}
