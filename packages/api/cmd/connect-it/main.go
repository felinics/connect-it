// connect-it API 服务入口。
//
//	@title						connect-it API
//	@version					1.0
//	@description				内部 Connector 服务：catalog、配置管理、OAuth 连接与聚合 MCP。
//	@BasePath					/
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
package main

import (
	"context"
	"log"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/api"
	connectors "github.com/memohai/connect-it/packages/connectors"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/registry"
	service "github.com/memohai/connect-it/packages/service"
	"github.com/memohai/connect-it/packages/service/authsvc"
	"github.com/memohai/connect-it/packages/service/catalogsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/store"
)

func main() {
	ctx := context.Background()

	dbURL := mustEnv("DATABASE_URL")
	keySpec := mustEnv(crypto.EnvSecretKey)
	cookieSecret := mustEnv("COOKIE_SECRET")
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	keyring, err := crypto.ParseKeyring(keySpec)
	if err != nil {
		log.Fatalf("解析 %s: %v", crypto.EnvSecretKey, err)
	}

	if err := service.MigrateUp(dbURL); err != nil {
		log.Fatalf("数据库 migration: %v", err)
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		log.Fatalf("连接数据库: %v", err)
	}
	defer pool.Close()

	reg := registry.New()
	connectors.RegisterAll(reg)

	queries := store.New(pool)
	configSvc := configsvc.New(queries, reg, keyring)
	authSvc := authsvc.New(queries)
	catalogSvc := catalogsvc.New(queries, reg, configSvc)

	if err := authSvc.EnsureAdminFromEnv(ctx); err != nil {
		log.Fatalf("初始化 admin 账号: %v", err)
	}

	e := api.New(api.Deps{
		Registry:     reg,
		Store:        queries,
		Config:       configSvc,
		Catalog:      catalogSvc,
		Auth:         authSvc,
		CookieSecret: []byte(cookieSecret),
	})
	log.Printf("connect-it 监听 %s", addr)
	log.Fatal(e.Start(addr))
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("缺少必填环境变量 %s", name)
	}
	return v
}
