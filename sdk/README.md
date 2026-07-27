# Connect-It SDK

`sdk/` 放面向 Connect-It 下游服务的 SDK。当前只提供 Go SDK，因为首个下游
Memoh Cloud 的服务端使用 Go。

仓库中的两个 SDK 职责不同：

| 目录 | 用途 |
|---|---|
| `packages/sdk` | 从 OpenAPI 自动生成的私有 TypeScript Client，供 Connect-It 自己的管理界面使用 |
| `sdk/go` | 手写的下游 Go SDK，供 Memoh Cloud 等可信服务调用 Connect-It |

## Go SDK

发布后安装：

```bash
go get github.com/memohai/connect-it/sdk/go
```

仓库联调阶段先在下游 `go.mod` 使用本地替换：

```go
require github.com/memohai/connect-it/sdk/go v0.0.0

replace github.com/memohai/connect-it/sdk/go => ../connect-it/sdk/go
```

正式发布子模块时使用 `sdk/go/vX.Y.Z` 形式的 Git tag。

创建 Client。API Token 是下游服务的部署级密钥，不能传给浏览器：

```go
client, err := connectit.New(
    os.Getenv("MEMOH_CONNECT_IT_BASE_URL"),
    os.Getenv("MEMOH_CONNECT_IT_API_TOKEN"),
)
if err != nil {
    return err
}
```

创建 OAuth Connection，并把返回的 `ConnectionID` 持久化到下游自己的业务记录：

```go
authorization, err := client.BeginOAuth(ctx, connectit.BeginOAuthRequest{
    ConnectorType: "github",
    AuthMethod:    "oauth",
    Alias:         "github",
    RedirectURL:   "https://memoh.example/oauth/connect-it/callback",
})
if err != nil {
    return err
}

// 让终端用户打开 authorization.AuthorizationURL。
// 下游只需长期保存 authorization.ConnectionID。
```

连接 MCP 时使用官方 Go MCP SDK。`MCPAuthHandler` 会用 API Token 和
Connection ID 按需签发短期 MCP Session Token，在到期前自动更新，并在服务端返回
`401` 或 `403` 后清除缓存、重新签发一次：

```go
authHandler := client.MCPAuthHandler(connectit.MCPSessionConfig{
    ConnectionID: connectionID,
    TTL:          time.Hour,
})

mcpClient := mcp.NewClient(&mcp.Implementation{
    Name:    "memoh-cloud",
    Version: "1.0.0",
}, nil)

session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{
    Endpoint:     client.MCPEndpoint(),
    OAuthHandler: authHandler,
}, nil)
if err != nil {
    return err
}
defer session.Close()
```

Session 签发时会发现并固化当前工具集。`ToolAllowlist` 为空表示允许本次签发时
发现到的全部工具；非空时每个名字都必须存在，否则签发失败。撤销 Client 使用的
Connect-It API Token 会立即使它签发的 Session 失效。

凭证边界：

- Connect-It API Token：保存在下游服务端的 Secret/环境变量中。
- Connection ID：保存在下游业务数据库中。
- MCP Session Token：由 `MCPAuthHandler` 缓存在进程内，不落库。
- 第三方 access/refresh token：只保存在 Connect-It。

`sdk/go` 不封装 `tools/list` 和 `tools/call`，这些协议能力直接使用官方 MCP SDK。
