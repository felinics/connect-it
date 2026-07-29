# connect-it SDK

`sdk/` holds the SDKs aimed at services downstream of connect-it. Only a Go SDK
exists today, because the first downstream service is written in Go.

The two SDKs in this repository have different jobs:

| Directory | Purpose |
|---|---|
| `packages/sdk` | Private TypeScript client generated from the OpenAPI spec, used by the connect-it admin UI |
| `sdk/go` | Hand-written Go SDK for trusted downstream services calling connect-it |

## Go SDK

Install:

```bash
go get github.com/memohai/connect-it/sdk/go
```

While developing against a local checkout, use a replace directive in the
downstream `go.mod`:

```go
require github.com/memohai/connect-it/sdk/go v0.0.0

replace github.com/memohai/connect-it/sdk/go => ../connect-it/sdk/go
```

Releases of the submodule are tagged as `sdk/go/vX.Y.Z`.

Create a client. The API token is a deployment-level secret of the downstream
service and must never reach a browser:

```go
client, err := connectit.New(
    os.Getenv("CONNECT_IT_BASE_URL"),
    os.Getenv("CONNECT_IT_API_TOKEN"),
)
if err != nil {
    return err
}
```

Create an OAuth connection and persist the returned `ConnectionID` in your own
records:

```go
authorization, err := client.BeginOAuth(ctx, connectit.BeginOAuthRequest{
    ConnectorType: "github",
    AuthMethod:    "oauth",
    Alias:         "github",
})
if err != nil {
    return err
}

// Send the end user to authorization.AuthorizationURL.
// Downstream only needs to store authorization.ConnectionID long term.
```

Connect to MCP with the official Go MCP SDK. Downstream builds a
`namespace → connection_id` map of the connections enabled for the current bot.
`MCPAuthHandler` uses the API token and that map to issue one aggregated,
short-lived MCP session token on demand, refreshes it before expiry, and clears
the cache and re-issues once after the server returns `401` or `403`:

```go
authHandler := client.MCPAuthHandler(connectit.MCPSessionConfig{
    Connections: map[string]string{
        "github": githubConnectionID,
        "notion": notionConnectionID,
    },
    TTL: time.Hour,
})

mcpClient := mcp.NewClient(&mcp.Implementation{
    Name:    "my-service",
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

Issuing a session discovers the current tool set and freezes it. An empty
`ToolAllowlist` allows every tool discovered at issue time; a non-empty one
requires every listed name to exist, or issuing fails. Revoking the connect-it
API token used by the client invalidates every session it issued, immediately.

Credential boundaries:

- connect-it API token: kept in the downstream server's secret store or environment.
- Connection ID: kept in the downstream service's own connector records.
- MCP session token: cached in process by `MCPAuthHandler`, never persisted.
- Third-party access and refresh tokens: kept only inside connect-it.

`sdk/go` does not wrap `tools/list` or `tools/call`. Use the official MCP SDK
directly for those protocol calls.
