# Development

## Layout

```text
packages/
├── core/        Connector definitions, registry, crypto, shared types (no I/O)
├── connectors/  Remote MCP and managed providers
├── service/     Config, connections, OAuth, tokens, execution, MCP sessions
├── api/         Echo HTTP layer, program entry point, generated OpenAPI
├── sdk/         TypeScript SDK generated from OpenAPI (admin UI only)
├── ui/          git submodule → github.com/felinics/ui
└── web/         Vue 3 admin UI
sdk/go/          Hand-written Go SDK for trusted downstream services
Dockerfile       Combined Go API and admin UI container image
```

Every Go directory under `packages/` is its own module. There is no `go.work`;
they reference each other through relative `replace` directives. `packages/ui`
is a submodule, so clone with `--recursive`.

## Setup

```bash
mise install        # go, node, pnpm, sqlc
pnpm install
mise run db-up      # local postgres:17 on host port 5433
```

Run the API and the admin UI in two terminals:

```bash
mise run dev
pnpm --dir packages/web run dev
```

## Tasks

| Command | What it does |
|---|---|
| `mise run test` | Test every Go module, including `sdk/go` |
| `mise run vet` | `go vet` every Go module |
| `mise run test-release` | Test version parsing and release helpers |
| `mise run release` | Interactively bump, commit, tag, and push a release |
| `mise run dev` | Run the API against the `db-up` database |
| `mise run build-web` | Build the admin UI into `packages/web/dist` |
| `mise run docker-up` | Build and start the compose stack, wait until healthy |
| `mise run docker-down` | Stop the compose stack, keeping the postgres volume |
| `pnpm --dir packages/web run test` | Vitest unit tests for the admin UI |

Go integration tests need a database and skip themselves when
`TEST_DATABASE_URL` is unset. Each test runs in its own random schema and drops
it afterwards, so they are safe to run in parallel against one database:

```bash
TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5433/connect_it?sslmode=disable' mise run test
```

## Generated artifacts

Three directories are generated. Editing them by hand is always wrong — the
next generation run overwrites the change.

```text
packages/service/store/queries/*.sql  --(mise run sqlc)-->     packages/service/store/*.sql.go
packages/api/**/*.go swag annotations --(mise run swagger)-->  packages/api/docs/{docs.go,swagger.json,swagger.yaml}
packages/api/docs/swagger.json        --(mise run sdk)-->      packages/sdk/src/*.gen.ts
```

Change the source, then re-run the generator. Editing a handler's `@Summary`
means running `mise run swagger` and then `mise run sdk`.

## Adding a connector

1. Create a directory under `packages/connectors/`.
2. Add `definition.go`, plus `managed.go` if it is a managed connector.
3. Register it in `packages/connectors/all.go`.

A Definition must pick exactly one implementation, `remote_mcp` or `managed`,
never both. [Architecture](architecture.md) explains why.

The registry validates every Definition at registration time, so a malformed
connector fails at startup rather than at first use.

## Conventions

- **Language.** All code, comments, log lines, error messages and documentation
  are in English. The admin UI ships translations in `packages/web/src/i18n/`;
  that is the only place user-facing Chinese belongs.
- **Comments.** Standard godoc form (`// Package x …`, `// FuncName …`).
  Explain why, not what.
- **Connector descriptions.** The `Description` fields in a Definition reach
  downstream LLMs through the MCP tool schema. Write clear, imperative English
  that says what a tool does and what constrains its arguments.
- **Frontend API access.** Pages go through `packages/web/src/api/endpoints.ts`,
  which delegates to the generated SDK. Hand-written `fetch` calls are not
  allowed.
- **Secrets.** Never log credentials, tool arguments or tool results.
  `tool_runs` records metadata only, by design.

## Before committing

```bash
mise run vet
mise run test
pnpm --dir packages/web run test
mise run build-web
```

CI runs the same set plus a combined Docker image build smoke test. See
`.github/workflows/ci.yml`.

## Releasing

`version.json` is the application version source of truth. Go module and npm
package versions remain independent. Start from a clean `main` that exactly
matches `origin/main`, then run:

```bash
mise run release
```

The command shows the current version, suggests the next patch version, and
asks for confirmation. It then updates `version.json`, creates a Conventional
Commit (`chore(release): vX.Y.Z`), creates an annotated `vX.Y.Z` tag, and pushes
the commit and tag atomically. Pass an explicit version when needed:

```bash
mise run release -- 0.2.0
```

The tag workflow verifies that the tag matches `version.json`, runs CI,
publishes `ghcr.io/felinics/connect-it`, and finally creates a GitHub Release
with generated notes. A failed CI or image publish therefore cannot create a
GitHub Release.

## Status

The project is pre-1.0. Migrations only maintain the schema of a fresh
database; in-place upgrades of older development databases are not promised.
