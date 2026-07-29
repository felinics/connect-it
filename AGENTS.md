# AGENTS.md

Read [docs/development.md](docs/development.md) first — layout, setup, tasks
and conventions live there. This file only collects what a coding agent is most
likely to get wrong.

## Do not edit generated files

```text
packages/service/store/*.sql.go        ← mise run sqlc    (source: store/queries/*.sql)
packages/api/docs/*                    ← mise run swagger (source: swag annotations in packages/api)
packages/sdk/src/*.gen.ts              ← mise run sdk     (source: packages/api/docs/swagger.json)
```

Change the source and re-run the generator. Editing a handler's `@Summary`
means `mise run swagger` followed by `mise run sdk`.

## Everything is in English

Code, comments, log lines, error messages and documentation. The only place
user-facing Chinese belongs is `packages/web/src/i18n/zh.ts`.

## Connector Definitions are one kind or the other

`remote_mcp` proxies an upstream MCP server; `managed` implements tools
locally. Never both in one Definition — the registry rejects it, and
[docs/architecture.md](docs/architecture.md) explains why the hybrid was
deliberately excluded.

`Description` fields reach downstream LLMs through the MCP tool schema. They
are product copy, not comments: write clear, imperative English.

## Never log secrets

No credentials, tool arguments or tool results. `tool_runs` records metadata
only, by design — do not "improve" it by adding payloads.

## Verify before claiming done

```bash
mise run vet
mise run test          # add TEST_DATABASE_URL to include integration tests
pnpm --dir packages/web run test
mise run build-web
```
