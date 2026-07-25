// connect-it API 服务入口。
//
//	@title						connect-it API
//	@version					1.0
//	@description				内部 Connector 服务：catalog、配置管理、OAuth 连接与聚合 MCP。
//	@description				管理端 /admin 使用 HttpOnly SameSite=Strict Cookie；CONNECT_IT_BASE_URL 为 HTTPS 时固定设置 Secure。浏览器客户端须启用 credentials=include，且写请求须携带 X-Connect-It-CSRF: 1。Swagger 2 无法形式化表达 Cookie 鉴权。
//	@BasePath					/
//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/memohai/connect-it/packages/api"
	connectors "github.com/memohai/connect-it/packages/connectors"
	"github.com/memohai/connect-it/packages/core/crypto"
	"github.com/memohai/connect-it/packages/core/providerkit"
	"github.com/memohai/connect-it/packages/core/registry"
	"github.com/memohai/connect-it/packages/service/authsvc"
	"github.com/memohai/connect-it/packages/service/catalogsvc"
	"github.com/memohai/connect-it/packages/service/configsvc"
	"github.com/memohai/connect-it/packages/service/connsvc"
	"github.com/memohai/connect-it/packages/service/dbgate"
	"github.com/memohai/connect-it/packages/service/exec"
	"github.com/memohai/connect-it/packages/service/mcpclient"
	"github.com/memohai/connect-it/packages/service/oauthsvc"
	"github.com/memohai/connect-it/packages/service/sessions"
	"github.com/memohai/connect-it/packages/service/store"
	"github.com/memohai/connect-it/packages/service/tokens"
)

func main() {
	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	dbURL := mustEnv("DATABASE_URL")
	keySpec := mustEnv(crypto.EnvSecretKey)
	cookieSecret := mustEnv("COOKIE_SECRET")
	baseURL := mustEnv("CONNECT_IT_BASE_URL")
	expectedDatabaseIdentity, err := loadExpectedDatabaseIdentity(os.Getenv)
	if err != nil {
		log.Fatalf("数据库 identity 配置: %v", err)
	}
	cookieSecure, err := cookieSecureForBaseURL(baseURL)
	if err != nil {
		log.Fatalf("解析 CONNECT_IT_BASE_URL: %v", err)
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	keyring, err := crypto.ParseKeyring(keySpec)
	if err != nil {
		log.Fatalf("解析 %s: %v", crypto.EnvSecretKey, err)
	}

	databaseGate, err := dbgate.NewProductionDatabaseGate(
		expectedDatabaseIdentity,
	)
	if err != nil {
		log.Fatalf("数据库 identity gate 配置: %v", err)
	}
	poolConfig, err := newGatedPoolConfig(dbURL, databaseGate)
	if err != nil {
		log.Fatalf("装配数据库连接池: %v", err)
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		// Pool construction errors can originate in callbacks or drivers.
		// Never copy server-controlled error text into production logs.
		log.Fatal("创建数据库连接池失败")
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		log.Fatalf(
			"数据库连接/identity/schema gate: %s",
			dbgate.SafeStartupFailure(err),
		)
	}
	defer pool.Close()

	providerObserver, err := newProductionProviderObserver(os.Stderr)
	if err != nil {
		log.Fatalf("装配 Provider observer: %v", err)
	}
	providerFactory, err := providerkit.NewFactoryFromEnv(
		providerkit.WithObserver(providerObserver),
	)
	if err != nil {
		log.Fatalf("解析 Provider 网络策略: %v", err)
	}
	runtime, err := connectors.NewRuntime(providerFactory)
	if err != nil {
		log.Fatalf("装配 Provider runtime: %v", err)
	}
	mcpRuntime, err := mcpclient.New(providerFactory)
	if err != nil {
		log.Fatalf("装配 Remote MCP runtime: %v", err)
	}
	reg := registry.New()
	connectors.RegisterAll(reg, runtime)
	definitions := reg.All()
	if err := mcpRuntime.PreflightDefinitions(definitions); err != nil {
		log.Fatalf("校验固定 Remote MCP 网络策略: %v", err)
	}
	if err := oauthsvc.PreflightDefinitions(
		providerFactory,
		definitions,
	); err != nil {
		log.Fatalf("校验固定 OAuth 网络策略: %v", err)
	}

	queries := store.New(pool)
	configSvc := configsvc.New(queries, reg, keyring)
	policyResults, err := configSvc.ReconcilePolicyIdentities(ctx)
	if err != nil {
		log.Fatalf("校准 Connector policy identity: %v", err)
	}
	for _, result := range policyResults {
		if !result.Changed {
			continue
		}
		log.Printf(
			"Connector policy identity 已校准: connector=%s previous_known=%t definition_changed=%t connections_bumped=%d",
			result.ConnectorType,
			result.PreviousKnown,
			result.DefinitionChanged,
			result.ConnectionsBumped,
		)
	}
	authSvc := authsvc.New(queries)
	catalogSvc := catalogsvc.New(queries, reg, configSvc)
	oauthSvc := oauthsvc.New(
		queries,
		reg,
		configSvc,
		keyring,
		providerFactory,
		baseURL,
		runtime.Authorization,
	)
	maintainOAuthAuthorizations(ctx, oauthSvc)
	var maintenanceWorkers sync.WaitGroup
	maintenanceWorkers.Add(1)
	go func() {
		defer maintenanceWorkers.Done()
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				maintainOAuthAuthorizations(ctx, oauthSvc)
			}
		}
	}()
	connSvc := connsvc.New(
		queries,
		reg,
		keyring,
		configSvc,
		runtime.Authorization.CredentialValidators,
		runtime.Authorization.ScopeMatchers,
	)
	refresher := tokens.New(
		queries,
		reg,
		configSvc,
		keyring,
		providerFactory,
		runtime.Authorization.ScopeMatchers,
	)
	sessionSvc := sessions.New(queries, reg)
	engine := exec.New(exec.Deps{
		Store:      queries,
		Registry:   reg,
		Configs:    configSvc,
		Refresher:  refresher,
		Keyring:    keyring,
		Handlers:   runtime.Handlers,
		MCP:        mcpRuntime,
		Matchers:   runtime.Authorization.ScopeMatchers,
		Authorizer: sessionSvc,
	})

	if err := authSvc.EnsureAdminFromEnv(ctx); err != nil {
		log.Fatalf("初始化 admin 账号: %v", err)
	}

	e := api.New(api.Deps{
		Registry:     reg,
		Config:       configSvc,
		Catalog:      catalogSvc,
		Auth:         authSvc,
		OAuth:        oauthSvc,
		Conns:        connSvc,
		Exec:         engine,
		MCPTools:     mcpRuntime,
		Sessions:     sessionSvc,
		CookieSecret: []byte(cookieSecret),
		CookieSecure: cookieSecure,
	})
	log.Printf("connect-it 监听 %s", addr)
	server := api.NewHTTPServer(addr, e)
	serverResult := make(chan error, 1)
	go func() {
		serverResult <- server.ListenAndServe()
	}()

	var serveErr error
	select {
	case serveErr = <-serverResult:
		stop()
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(),
			10*time.Second,
		)
		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Printf("HTTP 服务优雅退出失败: %v", err)
			_ = server.Close()
		}
		cancel()
		serveErr = <-serverResult
	}
	maintenanceWorkers.Wait()
	shutdownProviderObserver(providerObserver)
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		log.Fatalf("HTTP 服务退出: %v", serveErr)
	}
}

// newGatedPoolConfig 解析 DATABASE_URL 并把 identity gate 装到 AfterConnect：
// 这是 pgxpool 唯一能在每条物理连接上运行门禁的钩子，因此 gate 必填，
// 之后也不得被替换。
func newGatedPoolConfig(
	databaseURL string,
	gate func(context.Context, *pgx.Conn) error,
) (*pgxpool.Config, error) {
	if gate == nil {
		return nil, errors.New("缺少数据库 identity gate")
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// pgx parse errors may quote the input. DATABASE_URL can contain a
		// password, so production logs intentionally omit the raw error.
		return nil, errors.New("解析 DATABASE_URL 失败")
	}
	config.AfterConnect = gate
	return config, nil
}

func cookieSecureForBaseURL(raw string) (bool, error) {
	parsed, err := providerkit.ParseAndValidateURL(raw)
	if err != nil {
		return false, err
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return false, errors.New("must not contain a query")
	}
	return parsed.Scheme == "https", nil
}

func maintainOAuthAuthorizations(
	processCtx context.Context,
	oauthSvc *oauthsvc.Service,
) {
	maintenanceCtx, cancel := context.WithTimeout(processCtx, 10*time.Second)
	defer cancel()
	if err := oauthSvc.MaintainAuthorizations(maintenanceCtx); err != nil {
		// Maintenance never includes state, credentials, or Provider payloads.
		log.Printf("OAuth authorization 维护失败: %v", err)
	}
}

func mustEnv(name string) string {
	v := os.Getenv(name)
	if v == "" {
		log.Fatalf("缺少必填环境变量 %s", name)
	}
	return v
}

func loadExpectedDatabaseIdentity(
	getenv func(string) string,
) (dbgate.ExpectedDatabaseIdentity, error) {
	if getenv == nil {
		return dbgate.ExpectedDatabaseIdentity{}, errors.New(
			"environment reader is required",
		)
	}
	expected := dbgate.ExpectedDatabaseIdentity{
		Database:         getenv(dbgate.EnvExpectedDatabaseName),
		Schema:           getenv(dbgate.EnvExpectedDatabaseSchema),
		SystemIdentifier: getenv(dbgate.EnvExpectedDatabaseSystemIdentifier),
		ApplicationRole:  getenv(dbgate.EnvExpectedDatabaseApplicationRole),
		OwnerRole:        getenv(dbgate.EnvExpectedDatabaseOwnerRole),
	}
	switch {
	case expected.Database == "":
		return dbgate.ExpectedDatabaseIdentity{}, fmt.Errorf(
			"缺少必填环境变量 %s",
			dbgate.EnvExpectedDatabaseName,
		)
	case expected.Schema == "":
		return dbgate.ExpectedDatabaseIdentity{}, fmt.Errorf(
			"缺少必填环境变量 %s",
			dbgate.EnvExpectedDatabaseSchema,
		)
	case expected.SystemIdentifier == "":
		return dbgate.ExpectedDatabaseIdentity{}, fmt.Errorf(
			"缺少必填环境变量 %s",
			dbgate.EnvExpectedDatabaseSystemIdentifier,
		)
	case expected.ApplicationRole == "":
		return dbgate.ExpectedDatabaseIdentity{}, fmt.Errorf(
			"缺少必填环境变量 %s",
			dbgate.EnvExpectedDatabaseApplicationRole,
		)
	case expected.OwnerRole == "":
		return dbgate.ExpectedDatabaseIdentity{}, fmt.Errorf(
			"缺少必填环境变量 %s",
			dbgate.EnvExpectedDatabaseOwnerRole,
		)
	default:
		return expected, nil
	}
}
