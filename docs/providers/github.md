# GitHub Provider

Connector type: `github`

## Source and protocol

- Official GitHub hosted MCP server:
  <https://github.com/github/github-mcp-server/blob/main/docs/remote-server.md>
- GitHub OAuth App scopes:
  <https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps>
- GitHub REST API versions:
  <https://docs.github.com/en/rest/about-the-rest-api/api-versions>
- Credential validation endpoint:
  <https://docs.github.com/en/rest/users/users#get-the-authenticated-user>
- Reviewed hosted-MCP implementation:
  `github/github-mcp-server` commit
  `eb088dfe9d854dab6453a8d4ae5871a5ced20974`, MIT.
- Audited hosted-MCP repository and exact paths:
  <https://github.com/github/github-mcp-server>,
  `docs/remote-server.md`, `pkg/github/context_tools.go`,
  `pkg/github/issues.go`, `pkg/github/repositories.go`, and
  `pkg/github/search.go`.
- Reviewed behavioral reference:
  `oomol-lab/open-connector` commit
  `dbc50f5b46e978a9b825f92a1f5aed585f8408d4`, Apache-2.0, under
  `src/providers/github/`.
- Audited behavioral-reference repository and exact paths:
  <https://github.com/oomol-lab/open-connector>,
  `src/providers/github/actions.ts`,
  `src/providers/github/definition.ts`,
  `src/providers/github/executors.ts`,
  `src/providers/github/runtime-activity.ts`,
  `src/providers/github/runtime-issue.ts`,
  `src/providers/github/runtime-pull-request.ts`,
  `src/providers/github/runtime-release.ts`,
  `src/providers/github/runtime-repository.ts`,
  `src/providers/github/runtime-search.ts`,
  `src/providers/github/runtime-shared.ts`, and
  `src/providers/github/scopes.ts`.
- REST API version and source review date: `2026-03-10`, 2026-07-23.

The Definition fixes the Remote MCP endpoint to
`https://api.githubcopilot.com/mcp/`. Runtime code derives the exact
`https://api.githubcopilot.com:443` origin from that reviewed value. Neither
Connector configuration nor Tool arguments can replace it or opt it into
private-network or plain-HTTP access.

## Authentication and configuration

GitHub supports either:

- OAuth: optionally configure `client_id` and secret `client_secret`, then
  register the deployment's documented connect-it callback URL exactly; or
- Personal Access Token: create a Connection with the required secret `token`.

OAuth requests `repo` and `read:user`. The authorization request uses the
standard space-separated scope syntax; GitHub token responses are normalized
with GitHub's comma-separated scope syntax. PAT and OAuth access tokens are
presented to the hosted MCP server only as `Authorization: Bearer`.

An explicit upstream HTTP 401 produces the platform's internal
credential-invalid marker; HTTP 403 is a state-neutral `permission_denied`.
Tokens are never placed in endpoint URLs, errors, health rows or audit output.

## Tools

| Tool ID | Risk | Purpose |
|---|---:|---|
| `get_me` | read | Read the authenticated user's profile |
| `search_repositories` | read | Search repositories with GitHub search syntax |
| `get_file_contents` | read | Read one repository file or directory |
| `list_issues` | read | List repository issues |
| `issue_write` | write | Create or update an issue |

The connector preserves the published Tool IDs, schemas and successful
Text/Structured output shape. MCP `IsError` content and protocol/HTTP error
bodies are discarded and replaced with a stable `ToolFailure`; Provider text
or structured error payloads never cross the Tool boundary.

## Runtime limits and known constraints

- Total logical call budget: 30 seconds, including MCP handshake, DNS, dial,
  TLS, redirects and bounded response reads.
- Decoded response limit: 10 MiB for each guarded HTTP response.
- At most 32 Remote MCP sessions run concurrently per process; queued calls
  honor caller cancellation/deadlines.
- Same-origin redirects are allowed and retain the declared bearer footprint.
  Cross-origin redirects are not declared and are denied, so credentials
  cannot move to another origin. HTTPS downgrade is always denied.
- The SDK's implicit default HTTP client, environment proxy, standalone SSE
  connection and reconnect retries are disabled.
- Remote input schemas are intentionally permissive approximations of the
  hosted server's current `tools/list`. Reconciling the five declared mappings
  against the live list is the `mcp:verify` flow (`mise run mcp-probe` for a
  one-off probe); re-run it when GitHub changes that contract.
- No Tool declares an `OutputSchema`, so a change in the hosted server's output
  shape reaches the Agent unvalidated. That is the current state, not an engine
  limit: `packages/service/exec/engine.go` routes Remote and Managed results
  through the same `finish()`, which validates whatever output schema the
  Definition declares. Declaring one requires sampling real responses first.

## Upgrade notes

The providerkit migration does not change connector type, Tool IDs, schemas,
auth fields or `ConfigSchemaVersion` (`1`). Existing GitHub configuration
needs no data migration. The externally observable failure shape is now the
platform `ToolFailure` contract rather than Provider-body-bearing MCP
`IsError` content.

Re-review the fixed endpoint, allowed hostname, requested scopes, API version,
this document's Source section before changing the hosted MCP version
or authentication contract.

## Real-account smoke procedure

`TestGitHubRealSmoke` is skipped when none of these variables is configured:

- `CONNECT_IT_GITHUB_SMOKE_TOKEN`;
- `CONNECT_IT_GITHUB_SMOKE_OWNER`;
- `CONNECT_IT_GITHUB_SMOKE_REPOSITORY`;
- `CONNECT_IT_GITHUB_SMOKE_FILE_PATH`; and
- `CONNECT_IT_GITHUB_SMOKE_ALLOW_WRITES=true`.

Once any variable is present, all five are required and the write gate must
be exactly `true`. Use a short-lived least-privilege PAT, a dedicated GitHub
test user, a disposable repository, and a known non-sensitive file. Export
the variables without placing the token in shell history. After the
implementation is committed, run the fail-closed release gate from the
repository root. The SHA must be the full current `HEAD`, the repository must
be clean, and the receipt must be an absolute path that does not exist:

```sh
bash scripts/run-provider-real-smoke.sh \
  github \
  '<FULL_APPROVED_HEAD_SHA>' \
  '/protected/provider-smoke/github.tsv'
```

The gate runs only `TestGitHubRealSmoke`, rejects the default no-environment
skip, withholds raw Go JSON/stderr, and publishes a mode-`0600` no-clobber
receipt only after the exact test and package explicitly pass. Its
`cleanup_mode` identifies the automatic guarded-close path but does not claim
cleanup completed; the smoke table still requires a separately confirmed
cleanup `PASS`.

The harness validates the PAT identity, proves a known-invalid PAT is rejected,
validates every input against the compiled Definition schema, and invokes all
five reviewed remote mappings through the official MCP Go SDK over the fixed
providerkit policy. It does not call `tools/list`; that reconciliation is
`mcp:verify` / `mise run mcp-probe` and must be run separately. Before `issue_write`, it registers cleanup
for the unique UTC-and-random-tagged title and marks the mutation attempted
immediately before the remote call. The fixed test-only cleanup performs up to
five bounded lookups, accepts only one exact title/body/non-pull-request match,
and closes it through the versioned GitHub REST API. If issue creation was
accepted but its MCP response was lost, lookup retries cover delayed list
visibility. If the close response times out, is temporarily unavailable, or
is malformed, cleanup looks up the exact issue again and succeeds only after
confirming `state=closed`; an exhausted zero-match or uncertain close is an
explicit smoke failure. Cleanup errors contain only stable failure code/status
metadata. The harness never prints the token, account profile, repository/file
content, MCP payload, issue body, endpoint query, or Provider error text.

For the complete release check:

1. Create both an OAuth Connection and, when PAT support is release-critical,
   a least-privilege PAT Connection; run the harness for the PAT path.
2. Verify `get_me`, repository search, file read, issue list and unique issue
   creation results against the dedicated fixtures without copying content
   into evidence.
3. Confirm Session authorization prevents the write Tool unless it was
   explicitly granted.
4. Confirm audit/health output contains stable operation and status metadata,
   but no token, repository content, endpoint query or Provider error body.
5. Confirm the harness closed the disposable issue. If bounded cleanup failed,
   find the issue whose title starts with `connect-it GitHub real smoke ` in
   the dedicated repository, verify its fixed smoke body before closing it
   manually, and record that manual cleanup; then revoke both credentials and
   remove the repository if it was temporary.

Record account class, authentication method, date, tool results and cleanup
outcome below; never record tokens or repository content.

## Smoke records

| Date | Kind | Account/auth class | Environment | API/server version | Coverage | Cleanup status | Result |
|---|---|---|---|---|---|---|---|
| 2026-07-24 | deterministic mock | N/A | `providerkit/testkit` and local `httptest`, no public network | GitHub REST `2026-03-10` | credential-validator success/scopes, pre-request PAT-field rejection, safe Provider failure mapping, malformed-profile rejection; smoke-harness default-skip/config gate; cleanup registered before write; delayed create visibility; exact title/body/non-PR match; malformed or temporary close-response recheck; zero-match exhaustion; generic-error redaction | N/A — no external resources | PASS (`go test -race -count=1 ./github`) |
| 2026-07-23 | deterministic mock | N/A | official Go MCP SDK over guarded `httptest` in `packages/service/mcpclient` | N/A — transport level | exact fixed policy derivation, fixed HTTP/private downgrade rejection, bearer injection, same-origin redirect, cross-origin denial, 10 MiB bound, timeout, 401 marker, `IsError` payload redaction and concurrency queue | N/A — no external resources | PASS (`go test ./mcpclient -count=5`) |
| — | real account | dedicated GitHub test user; OAuth and release-critical PAT | dedicated user/repository required | PENDING — record hosted MCP and REST versions at execution | validator and negative-auth probe, all five Tool calls, negative authorization checks and audit redaction; `tools/list` reconciliation via a separate `mcp:verify` run | PENDING — not run; harness uses bounded exact lookup/close with a documented manual fallback | PENDING; no real-account claim has been made |

The transport row is coverage of `packages/service/mcpclient`, not of the
`github` package: those tests drive a synthetic Definition, so they pin the
Remote MCP transport every Provider shares rather than GitHub's own mappings.

The deterministic mock records are reproducible unit evidence, not a substitute
for the release-gated real-account smoke.
