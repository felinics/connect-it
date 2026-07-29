# AGENTS.md

Working notes for humans and coding agents in this repository.

## What this is

connect-it is a self-hosted connector gateway. It stores third-party
credentials, manages connections through a REST API, and exposes tools to
trusted downstream services through one aggregated MCP Streamable HTTP
endpoint. See [docs/architecture.md](docs/architecture.md) for the model.

## Layout

```text
packages/
├── core/        Connector definitions, registry, crypto, shared types (no I/O)
├── connectors/  Remote MCP and managed providers
├── service/     Config, connections, OAuth, tokens, execution, MCP sessions
├── api/         Echo HTTP layer, program entry point, generated OpenAPI
├── sdk/         TypeScript SDK generated from OpenAPI (admin UI only)
├── ui/          git submodule → github.com/memohai/ui
└── web/         Vue 3 admin UI
sdk/go/          Hand-written Go SDK for trusted downstream services
docker/          Dockerfiles and nginx config
```

Each `packages/*` Go directory is its own module. There is no `go.work`; they
reference each other through relative `replace` directives. `packages/ui` is a
submodule, so clone with `--recursive`.

## Setup

```bash
mise install        # go, node, pnpm, sqlc
pnpm install
mise run db-up      # local postgres:17 on host port 5433
```

## Common tasks

| Command | What it does |
|---|---|
| `mise run test` | Test every Go module, including `sdk/go` |
| `mise run vet` | `go vet` every Go module |
| `mise run dev` | Run the API locally against the `db-up` database |
| `mise run build-web` | Build the admin UI into `packages/web/dist` |
| `mise run docker-up` | Build and start the compose stack, wait until healthy |
| `mise run docker-down` | Stop the compose stack, keeping the postgres volume |
| `pnpm --dir packages/web run dev` | Run the admin UI dev server |
| `pnpm --dir packages/web run test` | Vitest unit tests for the admin UI |

Go integration tests need a database and skip themselves when
`TEST_DATABASE_URL` is unset:

```bash
TEST_DATABASE_URL='postgres://postgres:postgres@localhost:5433/connect_it?sslmode=disable' mise run test
```

## Generated artifacts — do not edit by hand

Three directories are generated. Editing them directly is always wrong; the
next generation run overwrites the change.

```text
packages/service/store/queries/*.sql  --(mise run sqlc)-->     packages/service/store/*.sql.go
packages/api/**/*.go swag annotations --(mise run swagger)-->  packages/api/docs/{docs.go,swagger.json,swagger.yaml}
packages/api/docs/swagger.json        --(mise run sdk)-->      packages/sdk/src/*.gen.ts
```

Change the source, then re-run the generator. Editing an API handler's
`@Summary` means running `mise run swagger` and then `mise run sdk`.

## Conventions

- **Language.** All code, comments, log lines, error messages and documentation
  are in English. The admin UI ships translations in `packages/web/src/i18n/`;
  that is the only place user-facing Chinese belongs.
- **Comments.** Standard godoc form (`// Package x …`, `// FuncName …`).
  Explain why, not what.
- **Connector descriptions.** The `Description` fields in a connector
  Definition reach downstream LLMs through the MCP tool schema. Write them as
  clear, imperative English that states what the tool does and what constrains
  its arguments.
- **Adding a connector.** Create a directory under `packages/connectors/`, add
  `definition.go` (and `managed.go` if it is a managed connector), then add the
  registration line in `packages/connectors/all.go`. A Definition must pick
  exactly one implementation: `remote_mcp` or `managed`, never both.
- **Frontend API access.** Pages go through `packages/web/src/api/endpoints.ts`,
  which delegates to the generated SDK. Hand-written `fetch` calls are not
  allowed.
- **Secrets.** Never log credentials, tool arguments or tool results.
  `tool_runs` deliberately records metadata only.

## Before committing

```bash
mise run vet
mise run test
pnpm --dir packages/web run test
mise run build-web
```

CI runs the same set plus a Docker build smoke test. See
`.github/workflows/ci.yml`.
