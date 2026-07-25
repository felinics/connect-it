# Notion Provider

Connector type: `notion`

This Provider is **Remote MCP**, not a Managed REST implementation. connect-it
owns no request shape here: it holds the credential, pins the endpoint and its
egress policy, and forwards `tools/call` to Notion's own hosted MCP server.
Tool arguments pass through untouched, the tool contract is defined by Notion
and can change without a release on our side, and the only thing we guarantee
is authenticated, origin-locked, bounded forwarding plus a stable failure
shape.

## Source

- Official hosted Remote MCP server:
  <https://developers.notion.com/docs/mcp>
- Dynamic client registration and the server's own OAuth metadata:
  <https://mcp.notion.com/register>
- Reviewed behavioral reference (historical, from the removed Managed REST
  implementation): `oomol-lab/open-connector` commit
  `dbc50f5b46e978a9b825f92a1f5aed585f8408d4`, Apache-2.0. The audited
  repository is <https://github.com/oomol-lab/open-connector>; the exact paths
  are `src/providers/notion/definition.ts`, `src/providers/notion/actions.ts`,
  `src/providers/notion/executors.ts`, and `src/providers/notion/scopes.ts`.
- Server contract and source review date: hosted MCP tool list reviewed
  2026-07-24 from Notion's documentation only. Official Notion documentation
  is authoritative; nothing here was read off a live `tools/list`.

## Three generations of "Notion MCP" — read before editing

Three different servers are called "Notion MCP" and their tool namespaces have
**zero overlap**:

| Generation | Tool names | Status |
|---|---|---|
| open-source local v1 (`makenotion/notion-mcp-server` `<2.0.0`, OpenAPI-generated) | `API-post-search`, `API-retrieve-a-page`, … | no longer actively maintained |
| open-source local v2 (same repository, `main`) | `query-data-source`, `retrieve-page-markdown`, … (no prefix) | no longer actively maintained |
| **official hosted remote — the only target of this Connector** | every tool prefixed `notion-`: `notion-search`, `notion-fetch`, … | current |

When changing this Provider, do **not** consult the GitHub repository. Only
`developers.notion.com`'s remote-server documentation applies. Copying a tool
name from either local generation produces a Definition that compiles, passes
every offline test, and fails on the first real call.

**Client-name alias risk.** The remote server changes tool names according to
the connecting client: the documentation states that for OpenAI clients
`notion-fetch` is exposed as a bare `fetch`. connect-it connects as a generic
MCP client (`mcp.Implementation{Name: "connect-it"}`) and *should* therefore
receive the `notion-` prefixed set — but this has **not been verified against
the live server**. `mise run mcp-probe` uses the same mcpclient as production,
so whatever it reports is what production will get. Run it once before
onboarding, and again after any change to the client identity.

## Endpoint and authentication

The Definition fixes the Remote MCP endpoint to `https://mcp.notion.com/mcp`
(`ProvenanceOfficial`). Runtime code derives the exact
`https://mcp.notion.com:443` origin from that reviewed value. Neither
Connector configuration nor Tool arguments can replace it or opt it into
private-network or plain-HTTP access.

The credential is an **OAuth 2.1 access token**, presented as
`Authorization: Bearer <token>`. There is exactly one auth method (`oauth`);
long-lived internal integration tokens (`ntn_…`) are not accepted — see
Upgrade notes for the misreading this regularly causes.

`mcp.notion.com` is both the resource server and the authorization server and
supports RFC 7591 dynamic client registration. The operator registers a
confidential client once at <https://mcp.notion.com/register> and stores the
result in the two required configuration fields `client_id` and secret
`client_secret`; without both, authorization cannot start.

Authorization Code with PKCE `S256`, endpoints `https://mcp.notion.com/authorize`
and `https://mcp.notion.com/token` (RFC 8414 metadata), scope `default`, HTTP
Basic client authentication. Access tokens last roughly eight hours. Refresh
tokens rotate on every use, and the grant itself expires 180 days after
authorization or after 30 consecutive days of inactivity — a Connection left
idle for a month must be re-authorized, not refreshed.

Credential validation uses the server's RFC 7662 introspection endpoint, the
only public way to decide whether one of its access tokens is still valid;
`api.notion.com` neither issues nor accepts these tokens. Introspection
authenticates with the registered client's own credentials (HTTP Basic) and the
token travels in the form body, as RFC 7662 requires. `active` is the sole
verdict; the optional `sub` and `username` are read for identity only, and when
`sub` is absent the profile carries no account ID rather than a fabricated one.
Scopes are not a usable authorization signal, so `ScopesKnown` is false. An explicit upstream HTTP 401 produces the platform's internal
credential-invalid marker; HTTP 403 is a state-neutral `permission_denied`.
Tokens never appear in endpoint URLs, errors, health rows or audit output.

## Plan entitlement — the usual onboarding blocker

Notion gates tools by workspace plan, and it does **not** gate them by
visibility: every tool appears in `tools/list` on every plan. Calling one the
workspace is not entitled to returns an **upgrade prompt as a successful tool
result**, not an error. connect-it does not detect or rewrite that; the caller
sees text instead of data. `search` is the common casualty — full cross-source
search requires Notion AI, and without it search degrades to workspace-only.

The official way to check entitlement is to call `fetch` with `id` = `self`
and read `self.current_tool_access`. Its keys are the remote tool name with the
`notion-` prefix removed and hyphens turned into underscores (`notion-query-data-sources`
→ `query_data_sources`); values are one of `available`, `limited_free_trial`,
`upgrade_required`, `not_enabled`. Do this first when onboarding a workspace.

## Tools

| Tool ID | Risk | Remote tool | Purpose |
|---|---:|---|---|
| `search` | read | `notion-search` | Search the workspace and connected third-party sources |
| `fetch` | read | `notion-fetch` | Read a page/database/data source by URL or ID; `id` = `self` returns workspace and user identity |
| `query_data_sources` | read | `notion-query-data-sources` | Query a data source with the SQL-ish DSL, or run an existing view |
| `get_users` | read | `notion-get-users` | List workspace members and guests, or fetch one |
| `get_teams` | read | `notion-get-teams` | List teamspaces |
| `get_comments` | read | `notion-get-comments` | Read comments and discussions on a page |
| `create_pages` | write | `notion-create-pages` | Create one or more pages (batch) with properties, content, icon, cover |
| `update_page` | write | `notion-update-page` | Update one page's properties, content, icon or cover |
| `create_database` | write | `notion-create-database` | Create a database with its initial data source and view |
| `create_comment` | write | `notion-create-comment` | Comment on a page or reply to a discussion |

Tool IDs drop the vendor prefix; the mapping to the upstream name is pinned in
the Definition. The remote server publishes 18 tools; the ten above are the
reviewed long-term contract. Deliberately not exposed:
`notion-get-async-task` (internal polling), `notion-duplicate-page`
(asynchronous, needs the task tool to converge), and
`notion-create-view` / `notion-update-view` / `notion-update-data-source` /
`notion-query-database-view` / `notion-query-meeting-notes` /
`notion-move-pages` (configuration DSLs or narrow cases that, without a
parameter contract, only give an agent something to fail at repeatedly).

MCP `IsError` content and protocol/HTTP error bodies are discarded and
replaced with a stable `ToolFailure`; Provider text or structured error
payloads never cross the Tool boundary.

## Input schemas are approximations — probe before relying on them

**Notion publishes no parameter documentation for the remote server**: no JSON
Schema, no parameter tables, only prose and example prompts. Every Tool here
therefore carries a permissive approximation — `additionalProperties: true`,
nothing `required`, and `properties` listing only parameters the documentation
spells out verbatim. Seven of the ten declare no `properties` at all.

Consequences, stated plainly: argument validation is effectively absent, so a
malformed call fails at Notion rather than locally, and an agent gets no useful
schema hints. The complex tools are the ones this hurts:

- `create_pages` takes an array of page objects with `properties`, `content`,
  `icon` and `cover`; its `allow_async` flag returns a task ID instead of a
  result, and this Connector exposes no polling tool, so leave it unset;
- `query_data_sources` takes a SQL-ish DSL.

Neither is genuinely usable until sampled. **Calibrating these schemas with
`mise run mcp-probe` (then `mcp:verify` against the live `tools/list`) is the
largest outstanding item on this Provider.**

## Runtime limits and known constraints

- Total logical call budget: 60 seconds. Search and data-source queries run
  through Notion AI and are slower than an ordinary read, which is why the
  budget is longer than the 30 seconds used for most Providers.
- Decoded response limit: 10 MiB for each guarded HTTP response.
- Same-origin redirects retain the declared bearer footprint; cross-origin
  redirects are denied, so credentials cannot move to another origin. HTTPS
  downgrade is always denied.
- The SDK's implicit default HTTP client, environment proxy, standalone SSE
  connection and reconnect retries are disabled.
- Pagination, filtering, truncation and output shape are owned by the remote
  server; this Connector normalizes none of them.

## Upgrade notes

`ConfigSchemaVersion` stays `1`, but the backend moved from this repository's
own guarded Notion Public API handlers to the official hosted MCP server. That
is an externally visible break; existing Connections must be re-authorized.

**Correct a standing misreading first.** Notion's sentence "does not support
bearer token authentication" has been read as "you cannot use a bearer token".
It means the opposite of what that implies: the server does not accept
*long-lived internal integration tokens* (`ntn_…`), only OAuth-issued access
tokens. The transport really is `Authorization: Bearer`. Long-lived token auth
existed only on the deprecated open-source local server.

Capability mapping from the six removed Managed REST tools — not 1:1:

| Removed Managed tool | Now |
|---|---|
| `search` | `search` → `notion-search` |
| `retrieve_page` | `fetch` → `notion-fetch` |
| `retrieve_data_source` | `fetch`, with `id` as `collection://<uuid>` |
| `retrieve_block_children` | `fetch` — the remote server has no standalone block-children tool; block-level reads are folded into fetch |
| `create_page` | `create_pages` → `notion-create-pages` (plural, batch) |
| `update_page` | `update_page` → `notion-update-page` |

Six collapse to four, and six read/write tools are gained (`query_data_sources`,
`get_users`, `get_teams`, `get_comments`, `create_database`, `create_comment`).
Take `definition.go` as authoritative, not this table.

Capability regressions, not glossed:

- **Block-level reads lost their own tool.** There is no paginated walk over a
  page's block children; you get whatever `fetch` decides to return, truncated
  on the server's terms.
- **No `Notion-Version` pinning.** The old implementation pinned an API
  version; the remote server's contract can shift under a live Connection.
- **No normalized envelopes or pagination guarantees.** Output is whatever the
  remote server returns.
- **No argument validation** — see the schema section above.
- **Plan gating is invisible to us.** An unentitled call succeeds and returns
  an upgrade prompt.
- **Search quality is now entitlement-dependent** (Notion AI).

Re-review the fixed endpoint, allowed hostname, the server's OAuth metadata,
this document's Source section and the live `tools/list` before changing the
hosted MCP or authentication contract.

## Real-account smoke procedure

The Definition owns no Managed handler, so there is no provider-specific
real-smoke harness in this package. Verification is the `mcp:verify` path
against a real workspace:

1. Register a confidential MCP OAuth client and store `client_id` and
   `client_secret`; complete the PKCE authorization against a dedicated,
   non-production workspace.
2. Confirm introspection accepts the fresh access token and rejects a revoked
   one with the HTTP 401 credential-invalid marker.
3. Run the probe (flags, not a positional URL; it needs the access token from
   step 1) and reconcile the live `tools/list` against the ten declared remote
   names — **confirm the `notion-` prefix is what we actually receive** — then
   record the real argument shapes:

   ```sh
   mise run mcp-probe -- -endpoint https://mcp.notion.com/mcp -token "$NOTION_ACCESS_TOKEN"
   ```
4. Call `fetch` with `id` = `self` and record `self.current_tool_access` for
   the workspace's plan.
5. Call each read Tool against dedicated fixtures.
6. Call each write Tool only in a disposable teamspace and delete what it
   creates; nothing in this repository will clean up for you.
7. Confirm refresh-token rotation is honored across an access-token expiry.
8. Confirm audit and health output contains stable operation and status
   metadata but no token, MCP payload, page content or Provider error body.
9. Revoke the credential and record workspace class, date, results and cleanup
   outcome.

## Smoke records

| Date | Kind | Account/auth class | Environment | API/server version | Coverage | Cleanup status | Result |
|---|---|---|---|---|---|---|---|
| 2026-07-24 | deterministic mock | N/A | `providerkit/testkit` and local `httptest`, no public network | hosted MCP contract reviewed 2026-07-24 | single `oauth` auth method and its endpoints, fixed endpoint/hostname/provenance and 60 s budget, the `notion-` remote namespace mapping, schema compilation and pass-through, introspection validator (inactive/malformed/missing-identity handling, failure matrix, redaction) | N/A — no external resources | PASS (`go test -count=1 ./notion`) |
| — | real account | dedicated workspace; MCP OAuth client and access token | dedicated Notion workspace and registered MCP OAuth client required | PENDING — record the hosted MCP contract review date at execution | `mcp-probe` tool-name and schema reconciliation plus the procedure above | PENDING — not run | **PENDING**; no real-account claim has been made |

No live `tools/list` has ever been observed for this Provider: the tool names,
the `notion-` prefix assumption and every input schema come from documentation
review alone. The transport guards — MCP handshake, cross-origin redirect
denial, response cap, timeout, session concurrency, `IsError` redaction — are
not exercised by `./notion`; they are covered once, for every Remote MCP
Provider, by `go test ./packages/service/mcpclient`. An earlier deterministic
record covered the removed Notion Public API implementation; that evidence
pertains to code that no longer exists and is not carried forward as coverage
here.
