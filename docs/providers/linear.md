# Linear Provider

Connector type: `linear`

## Source

- Linear's official hosted Remote MCP server: <https://linear.app/docs/mcp>
- Personal API keys and API access:
  <https://linear.app/docs/api-and-webhooks>
- OAuth 2.0 authorization and token endpoints:
  <https://linear.app/developers/oauth-2-0-authentication>
- Remote tool **names** were cross-checked against two independent clients that
  talk to this very endpoint: `fprochazka/linear-mcp-cli` (a schema-driven CLI
  that generates its commands from the live `tools/list`) and `openclaw/mcporter`
  (a published `mcporter list linear` capture of the real server).
- Behavioral reference for the removed GraphQL implementation (historical):
  `oomol-lab/open-connector` commit
  `dbc50f5b46e978a9b825f92a1f5aed585f8408d4`, Apache-2.0, audited at
  <https://github.com/oomol-lab/open-connector>, exact paths
  `src/providers/linear/definition.ts`, `src/providers/linear/actions.ts`,
  `src/providers/linear/executors.ts`, and `src/providers/linear/scopes.ts`.
- Server contract and source review date: hosted MCP contract reviewed
  2026-07-24 from those two third-party captures; **no live `tools/list` has
  been observed from this repository**. The Linear API itself is unversioned.

## Implementation shape: Remote MCP

This Connector owns **no Managed handler**. It holds the credential, a fixed
endpoint, a reviewed Tool list and the security policy; every call is an
authenticated, guarded forward to Linear's own hosted MCP server. Concretely:

- Tool semantics are defined by Linear, not by this repository. Arguments pass
  through unchanged and results come back as the upstream server produced them.
- No request/response normalization, no pagination wrapper, no envelope. What
  changes upstream changes here without a code change in connect-it.
- What connect-it still owns: which Tools are exposed at all, credential
  storage and injection, the origin/redirect/downgrade/timeout/size guards, and
  error redaction.

Endpoint: fixed to `https://mcp.linear.app/mcp` (`EndpointFixed`,
`ProvenanceOfficial`, allowed hostname `mcp.linear.app`). Runtime derives the
exact `https://mcp.linear.app:443` origin from that reviewed value; neither
Connector configuration nor Tool arguments can replace it or opt it into
private-network or plain-HTTP access. The legacy `/sse` transport is
**deprecated upstream and deliberately unused**.

Linear also publishes a read-only variant of the same server at
`https://mcp.linear.app/mcp/readonly`. It is **not declared in the Definition
today** — a read-scoped credential already cannot reach the write API, so the
variant buys defence in depth rather than a new capability. Declaring it as a
second server key would be the cleanest way to offer a safe-mode Connection to
a credential whose scopes cannot be restricted; that is a Definition change,
not a configuration toggle.

### Schema confidence — read this before trusting the argument lists

Tool **names** are high confidence (two independent captures of the live
server). Tool **input schemas are a reconstruction, not verbatim `tools/list`
output**: property names are reliable, types and required sets are inferred.

The single exception is `create_comment`'s first three properties (`issueId`,
`body`, `parentId`), which come from a real capture.

Consequences, all deliberate:

- every schema is declared `"additionalProperties": true`, so unknown arguments
  pass through and the upstream server stays the authority;
- no `OutputSchema` is declared anywhere — guessing one would make a Tool fail
  validation permanently at runtime;
- before relying on a `required` set or a type, calibrate against the live
  server and reconcile any drift into
  `packages/connectors/linear/definition.go`. The probe takes flags and needs a
  credential:

  ```sh
  mise run mcp-probe -- -endpoint https://mcp.linear.app/mcp -token "$LINEAR_API_KEY"
  ```

## Authentication and onboarding

Linear is the most permissive of this repository's Remote MCP Providers: the
hosted server accepts **either** credential on the same `Authorization: Bearer`
header.

| Auth method | Credential | Config prerequisites |
|---|---|---|
| `oauth` | Linear OAuth access token | optional `client_id` / secret `client_secret` on the Connector |
| `api_key` | Personal API key from `linear.app/settings/api` | none |

**Onboarding barrier: none.** There is no plan or tier gate — any Linear
workspace user can mint a personal API key. Because the API key is presented
directly as a Bearer token, a Connection can be created **fully headless, with
no OAuth round trip and no callback URL registration**, which is the fastest
path for a machine integration.

OAuth requests `read`, `write`, `issues:create` and `comments:create` with the
standard space-separated scope syntax. Linear access tokens are long-lived and
are issued without a refresh token, so no refresh endpoint is declared. When
`client_id`/`client_secret` are absent, OAuth authorization fails at start
while the API-key path — and therefore Connector readiness — is unaffected.

The OAuth path is **not yet proven against the hosted MCP server**. That server
additionally runs its own OAuth 2.1 + PKCE flow with dynamic client
registration, which this Definition does not implement; whether a token minted
by an ordinary Linear OAuth application is accepted there must be confirmed
before the method is relied on. The API-key path is the one covered by the
harvest and by the real-account harness.

Credential validation does **not** go to the MCP server. It runs the Linear
GraphQL `viewer` query through a second guarded client pinned to
`https://api.linear.app/` with redirects denied, and uses the stable Linear
user ID as `AccountID`. Note the presentation differs by target: the GraphQL
API takes a personal API key as the complete `Authorization` value with no
`Bearer` prefix, while the MCP server takes the same key as a Bearer token.
Personal API keys expose no authoritative scope snapshot, so validation returns
`ScopesKnown=false` with no granted scopes, and this Provider registers no
scope matcher — validation success does not prove that a given workspace or
Tool operation is permitted. An upstream HTTP 401 produces the internal
credential-invalid marker; HTTP 403 is a state-neutral `permission_denied`.
Credentials never appear in endpoint URLs, errors, health rows or audit output.

## Tools

Twelve of the roughly 25 Tools the hosted server publishes. Tool IDs equal the
upstream remote tool names.

| Tool ID | Risk | Purpose |
|---|---|---|
| `list_issues` | read | List/filter issues by assignee, team, state, project, cycle, label or full text |
| `get_issue` | read | Read one issue by UUID or identifier such as `ENG-42` |
| `create_issue` | write | Create one issue in a team |
| `update_issue` | write | Update one issue (relation arrays replace, not append) |
| `list_comments` | read | List the comments of one issue |
| `create_comment` | write | Comment on an issue, optionally replying to a comment |
| `list_projects` | read | List projects by team, state, initiative, member or name |
| `list_teams` | read | List workspace teams |
| `list_users` | read | List workspace users |
| `list_issue_labels` | read | List workspace- and team-level issue labels |
| `list_issue_statuses` | read | List one team's workflow statuses (team-scoped) |
| `list_cycles` | read | List one team's cycles (sprints) |

Deliberately **not** exposed: `search_documentation` (it searches Linear's help
centre, not the workspace — not a business capability), the document Tools and
project create/update (single-source names, medium confidence), and the
per-entity `get_*` Tools whose `list_*` counterpart already returns the same
records.

Unlike the removed GraphQL Tools, team / state / project / assignee / label
arguments accept human-readable **names** as well as IDs, and the upstream
server resolves them. MCP `IsError` content and protocol/HTTP error bodies are
discarded and replaced with a stable `ToolFailure`; Provider text and structured
error payloads never cross the Tool boundary.

## Capability changes versus the removed Managed implementation

The old implementation was hand-written GraphQL against
`api.linear.app/graphql` with 8 Tools. Compared with it:

Gained — five lookups the writes actually need: `list_comments`, `list_users`,
`list_issue_labels`, `list_issue_statuses`, `list_cycles`. Plus name-or-ID
resolution and whatever the upstream server ships next, for free.

Lost or regressed — stated plainly, not papered over:

- **The authenticated-viewer read is gone as a Tool.** The old `get_viewer` has
  no counterpart — the hosted server publishes no viewer Tool, so an agent can
  no longer ask "who am I". The identity is still resolved during credential
  validation, but it is not callable.
- **Tool IDs changed, so session grants naming the old IDs authorize nothing
  and must be reissued.** `search_issues` became `list_issues`, which is a
  rename *and* a semantic change: full-text search is now one optional `query`
  argument among the filters, not the Tool's purpose.
- **Normalized output is gone.** The previous envelopes, cursor shapes and
  nullability guarantees no longer exist; callers see raw upstream output.
- **Argument names changed** (camelCase, name-or-ID). Prompts and callers
  written against the old snake_case UUID-only schema break.
- **Schemas are approximate** (see above), so argument validation at our edge
  is weaker than it was for the hand-written GraphQL Tools.
- **Coverage is capped by the upstream Tool list.** Anything the hosted server
  does not publish is simply unavailable; we can no longer add a GraphQL query.

## Runtime limits and known constraints

- Total logical call budget 30 seconds, covering MCP handshake, DNS, dial, TLS,
  redirects and bounded response reads.
- Decoded response limit 10 MiB per guarded HTTP response.
- Same-origin redirects are allowed and retain the declared bearer footprint;
  cross-origin redirects are denied, so credentials cannot move to another
  origin. HTTPS downgrade is always denied.
- The SDK's implicit default HTTP client, environment proxy, standalone SSE
  connection and reconnect retries are disabled.
- Pagination, filtering, output shape and rate limiting are owned by the
  upstream server; this Connector normalizes none of them.

## Upgrade notes

`ConfigSchemaVersion` stays `1` and the stored credential field `api_key` is
unchanged, so existing API-key Connections keep working. The backend move from
this repository's GraphQL handlers to Linear's hosted Remote MCP server is
nevertheless an externally visible break — see the capability section above for
the exact losses.

Re-review the fixed endpoint, allowed hostname, requested scopes, this
document's Source section and the live `tools/list` before changing the hosted
MCP contract or the authentication contract. Confirming the OAuth method
against the hosted server is a prerequisite for advertising it; if OAuth-app
tokens are rejected there, the method must be dropped again.

## Real-account smoke procedure

`TestLinearRealSmoke` (`packages/connectors/linear/real_smoke_test.go`) still
exists and was kept through the rewrite; it now drives the MCP server instead
of GraphQL. It is an opt-in, read-only harness, skipped when neither variable
is configured:

- `CONNECT_IT_LINEAR_SMOKE_API_KEY`
- `CONNECT_IT_LINEAR_SMOKE_TEAM_ID`

Once either is present both are required and partial configuration fails
closed. Use a short-lived personal API key and a dedicated, non-production
workspace and team. The harness validates the credential, then calls the seven
read Tools it can drive without fixtures (`list_teams`, `list_users`,
`list_projects`, `list_issues`, `list_issue_labels`, `list_issue_statuses`,
`list_cycles`) through the official MCP Go SDK over a providerkit-guarded
client bound to the fixed endpoint. `get_issue` needs a known issue and is not
driven automatically.

The write Tools (`create_issue`, `update_issue`, `create_comment`) are
deliberately not exercised: the remote server owns their semantics and a smoke
run must not leave objects behind in a real workspace. There is therefore no
write gate and no cleanup path. The harness never prints the API key, the
viewer profile, MCP payloads or Provider error text.

After the implementation is committed, run the fail-closed release gate from
the repository root. The SHA must be the full current `HEAD`, the repository
must be clean, and the receipt must be an absolute path that does not exist:

```sh
bash scripts/run-provider-real-smoke.sh \
  linear \
  '<FULL_APPROVED_HEAD_SHA>' \
  '/protected/provider-smoke/linear.tsv'
```

The gate runs only `TestLinearRealSmoke`, rejects the default no-environment
skip, withholds raw Go JSON/stderr, and publishes a mode-`0600` no-clobber
receipt only after the exact test and package explicitly pass.

For a complete release check: confirm the validator returns the real viewer ID
with `ScopesKnown=false`; confirm the live `tools/list` still contains all
twelve remote names and reconcile schema drift with `mise run mcp-probe`; run
the read Tools through the harness; exercise the write Tools manually in a
disposable workspace and delete what they create; confirm a revoked key yields
the 401 credential-invalid marker and an unauthorized resource a state-neutral
`permission_denied`; confirm audit/health/observer output carries only stable
operation and status metadata; then revoke the key and record workspace class,
credential type (never its value), date, result and cleanup outcome.

## Smoke records

| Date | Kind | Account/auth class | Environment | API/server version | Coverage | Cleanup status | Result |
|---|---|---|---|---|---|---|---|
| 2026-07-24 | deterministic mock | N/A | `providerkit/testkit` and local `httptest`, no public network | hosted MCP contract reviewed 2026-07-24 | fixed endpoint/hostname/provenance assertions, the declared api-key bearer binding, exposed tool set with scopes and Risk, schema compilation and pass-through, both credential validators (raw-key and Bearer presentation, failure matrix, redaction) | N/A — no external resources | PASS (`go test -count=1 ./linear`) |
| — | real account | dedicated test workspace/team; short-lived personal API key | dedicated Linear workspace, team and personal API key required | PENDING — record the hosted MCP contract review date at execution | validator, live `tools/list` reconciliation and the seven driveable read Tools; the write Tools, `get_issue`, `list_comments` (both need a known issue) and the OAuth method are excluded by design | N/A — read-only harness creates nothing | **PENDING** — no Linear credential was available and no real-account claim has been made |

The deterministic mock record is reproducible engineering evidence. It does not
replace the real-account release gate, and no record here covers the OAuth
method against the hosted server. The transport guards themselves — MCP
handshake, cross-origin redirect denial, response cap, timeout, session
concurrency, `IsError` redaction — are not exercised by `./linear` at all; they
are covered once, for every Remote MCP Provider, by
`go test ./packages/service/mcpclient`.
