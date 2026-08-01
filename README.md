<div align="center">

# Connect IT

**Self-hosted connector gateway — hold your SaaS credentials once, and hand your agents a single MCP endpoint.**

[![CI](https://github.com/memohai/connect-it/actions/workflows/ci.yml/badge.svg)](https://github.com/memohai/connect-it/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](https://go.dev)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

</div>

## Features

- **Two kinds of connector.** Proxy an official remote MCP server, or run a
  managed integration implemented against a provider's REST API. The major SaaS
  platforms are covered out of the box.
- **Multi-tenant friendly.** A connection ID is an opaque handle, and
  connect-it never records who owns it. Your product keeps its own user model,
  and one deployment serves every tenant.
- **One session, many connections.** Aggregate any set of connections into a
  single MCP session. Tools are namespaced and frozen at issue time, so
  `tools/list` and `tools/call` always agree.
- **Credentials stay put.** Access and refresh tokens are sealed with a
  rotatable AES-256-GCM keyring. Downstream services only ever see connection
  IDs.
- **Built for concurrency.** Written in Go and shipped as one container image
  that serves both the API and admin UI. Tool discovery fans out across
  connections in parallel, and OAuth refreshes are collapsed by an in-process
  single-flight plus a database row lock.

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

The same multi-architecture image is published as
`ghcr.io/memohai/connect-it:latest` from the default branch and with semver tags
from version tags.

Open <http://localhost:8421> and sign in as `admin` with the
`CONNECT_IT_ADMIN_PASSWORD` from your `.env`.

Maintainers create releases with `mise run release`. The command updates the
canonical `version.json`, commits it, creates an annotated tag, and pushes both;
CI then publishes the image and creates the matching GitHub Release.

## Documentation

See [`docs/`](docs/) for deployment, day-to-day usage, the Go SDK, the
architecture and the development workflow.

## License

[MIT](LICENSE)
