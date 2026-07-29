# connect-it

**A self-hosted connector gateway that puts 112 SaaS integrations behind a single MCP endpoint.**

connect-it holds third-party credentials, manages connections over a REST API,
and exposes the resulting tools to trusted downstream services through one
aggregated MCP Streamable HTTP endpoint.

## Features

- **112 connectors out of the box.** GitHub, GitLab, Slack, Notion, Linear,
  Asana, Stripe, HubSpot, Sentry, Datadog, Cloudflare, Google Workspace,
  Microsoft OneDrive and more — 27 backed by official remote MCP servers, 85
  implemented against official REST APIs.
- **Multi-tenant friendly.** A connection ID is an opaque handle. connect-it
  never records who owns it, so your product keeps its own user model and only
  stores the IDs it cares about. One deployment serves every tenant.
- **One session, many connections.** Aggregate any set of connections into a
  single MCP session. Tools are namespaced as `namespace__tool_name` and frozen
  at issue time, so `tools/list` and `tools/call` always agree.
- **Credentials never leave the gateway.** Access and refresh tokens are sealed
  with a versioned AES-256-GCM keyring that supports rotation without downtime.
  Downstream services only ever see connection IDs.
- **Built for concurrency.** Written in Go and shipped as a single static
  binary. Tool discovery fans out across connections in parallel, and OAuth
  refreshes are collapsed by an in-process single-flight plus a database row
  lock, so a rotating refresh token is never lost to a concurrent write.
- **Admin UI included.** A Vue 3 console for configuring connectors, watching
  connection health and minting API tokens, in English and 简体中文.

## Deploy

Requires Docker and Docker Compose.

```bash
git clone --recursive https://github.com/memohai/connect-it.git
cd connect-it
cp .env.example .env
```

Generate the secrets and put them in `.env`:

```bash
echo "1:$(openssl rand -hex 32)"   # CONNECT_IT_SECRET_KEY
openssl rand -hex 32               # COOKIE_SECRET
openssl rand -hex 16               # POSTGRES_PASSWORD
```

Then start the stack:

```bash
docker compose up -d --build
```

Open <http://localhost:8080> and sign in as `admin` with the
`CONNECT_IT_ADMIN_PASSWORD` from your `.env`. Postgres data lives in a named
volume, so `docker compose down` keeps it.

To stop:

```bash
docker compose down
```

## Documentation

- [Architecture](docs/architecture.md) — connector model, MCP sessions,
  environment variables, security boundaries
- [Go SDK](sdk/README.md) — for trusted downstream services
- [AGENTS.md](AGENTS.md) — repository layout and development workflow
- REST API reference — `/swagger/index.html` on a running instance

## License

[MIT](LICENSE)
