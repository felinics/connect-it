# GitLab Provider

Connector type: `gitlab`

GitLab is a **Remote MCP** Provider. This repository no longer implements
GitLab REST calls; it forwards `tools/call` to the instance's own MCP server
with an authenticated, policy-guarded transport. Concretely that means:

- the tool set is defined by GitLab, not by us — we only pin which subset we
  expose and under what risk class;
- arguments pass through unchanged (`additionalProperties: true` on every
  Tool), so the instance is the authority on validation; and
- what we still own is credential handling, endpoint policy, egress gating and
  failure redaction.

## Source

- Official MCP server guide (upstream `doc/user/model_context_protocol/mcp_server.md`):
  <https://docs.gitlab.com/user/gitlab_duo/model_context_protocol/mcp_server/>
- Official published tool list (upstream
  `doc/user/model_context_protocol/mcp_server_tools.md`):
  <https://docs.gitlab.com/user/model_context_protocol/mcp_server_tools/>
- Official OAuth token scopes, including `mcp`:
  <https://docs.gitlab.com/security/tokens/access_token_scopes/>
- Official Doorkeeper OAuth/introspection reference used for credential
  validation: <https://docs.gitlab.com/api/oauth2/>
- Reviewed upstream implementation: GitLab `lib/api/mcp/base.rb` (the
  `AccessTokenValidationService` scope guard) and
  `app/services/mcp/tools/manager.rb` (the registered tool set), cross-checked
  against `doc/user/model_context_protocol/mcp_server_tools.md`.
- Reviewed behavioral reference (historical, from the deleted Managed REST
  implementation): `oomol-lab/open-connector` commit
  `dbc50f5b46e978a9b825f92a1f5aed585f8408d4`, Apache-2.0, audited at
  <https://github.com/oomol-lab/open-connector>, exact paths
  `src/providers/gitlab/definition.ts`, `src/providers/gitlab/actions.ts` and
  `src/providers/gitlab/executors.ts`.
- Server contract and source review date: 2026-07-24; REST namespace `v4`.

## Enablement prerequisites

These are external to connect-it and are the most common reason a correctly
configured Connector still fails. Confirm all three before onboarding:

1. **GitLab Duo availability must be `Always on` or `On by default`** for the
   group/project in question. An instance admin sets this; the MCP server is
   part of Duo and is unreachable when Duo is off.
2. **An OAuth application with the `mcp` scope must exist on that instance.**
   The token comes from that application (see below).
3. **Tier**: the MCP server moved from Premium to Free in **GitLab 19.2**.
   Below 19.2 a Free instance does not have it.

## Endpoint and configuration

GitLab's MCP server is not a separate hosted service — it is a REST namespace
of the instance itself, `POST {instance}/api/v4/mcp`. There is therefore no
fixed endpoint we can bake into the Definition: SaaS uses
`https://gitlab.com/api/v4/mcp`, self-managed and Dedicated use their own
domain. The Definition uses `EndpointConfigField` + `ProvenanceSelfHosted`,
the same shape as `google_ads`.

Two configuration fields are stored per Connector:

- required `mcp_url` (`InputURL`, not secret) with **deliberately no default**
  — pointing it at the wrong instance means sending someone else's server your
  access token. The runtime derives the policy origin from this one normalized
  value and requires the exact `/api/v4/mcp` suffix, which is also what stops a
  mistyped endpoint from being probed with the operator's token. Tool arguments
  can never replace it.
- `allow_insecure_http`, an explicit `false`/`true` selection defaulting to
  `false`.

## Authentication

One auth method, `access_token`, holding one secret `token` field: a GitLab
**OAuth 2.0 access token whose scopes include `mcp`**.

- **The `api` scope is not enough, and `api` does not imply `mcp`.** The guard
  in `lib/api/mcp/base.rb` runs `AccessTokenValidationService` against
  `MCP_SCOPE` / `GRANULAR_SCOPE` only.
- The user additionally needs the `execute_mcp_tool` permission.
- **Personal, project and group access tokens are not supported**: the endpoint
  validates through Doorkeeper OAuth, which PATs do not go through.
- A self-managed instance's OAuth endpoints vary with its domain and cannot be
  written into the Definition, so the token is brought in by the operator
  rather than obtained through an authorization-code flow here.

Credential validation calls the instance's Doorkeeper introspection endpoint,
`GET {instance}/oauth/token/info` — not `/api/v4/user`. Introspection requires
no scope, reports the token subject and the token's real scope set, and rejects
PATs exactly as the MCP endpoint does. Because the real scope set comes back,
`ScopesKnown` is `true` and the declared `RequiredScopes: ["mcp"]` is genuinely
enforced by the engine. When an older instance's response omits scopes we fall
back to `ScopesKnown=false` rather than treating an empty set as "no scopes."

The token is presented to the MCP server only as `Authorization: Bearer` and
never appears in a URL, policy identity, error, health row, observer event or
audit record. The account ID is the upstream user ID namespaced by a hash of
the canonical instance base, so instance topology does not leak through it.

## Egress gates

The server is always constructed as self-hosted, and access is fail-closed:

- the normalized URL must have one exact origin and contain no userinfo,
  query, fragment or encoded traversal;
- public HTTPS works under the normal guarded transport;
- private-network HTTPS additionally requires
  `CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK=true`;
- plain HTTP additionally requires the URL to use `http`, `allow_insecure_http=true`
  on the Connector, **and** `CONNECT_IT_ALLOW_INSECURE_PROVIDER_HTTP=true` on
  the deployment; and
- loopback, link-local, cloud metadata, multicast, unspecified and reserved
  destinations stay permanently blocked even with the private-network gate on.

These gates exist for deliberately configured self-managed instances, not as a
general outbound proxy. Production should keep both deployment switches off.

## Tools

Fourteen of the instance's twenty-three tools are exposed. Tool IDs are
byte-identical to the upstream remote tool names, including upstream's own
inconsistent spelling (`create_workitem_note` and `get_workitem_notes` have no
underscore in "workitem"; `link_work_items` does) — do not "correct" them.

| Tool ID | Risk | Purpose |
|---|---:|---|
| `search` | read | Search issues / merge requests / projects / blobs at instance, group or project scope |
| `search_labels` | read | Search labels by title in a project or group |
| `get_issue` | read | Read one issue |
| `create_issue` | write | Create an issue in a project |
| `get_merge_request` | read | Read one merge request |
| `create_merge_request` | write | Create a merge request |
| `get_merge_request_diffs` | read | Page through a merge request's diffs |
| `get_merge_request_notes` | read | Cursor-page a merge request's notes |
| `create_merge_request_note` | write | Comment on a merge request or reply to a discussion |
| `get_workitem_notes` | read | Cursor-page a work item's notes |
| `create_workitem_note` | write | Comment on a work item |
| `link_work_items` | write | Link a work item to other work items |
| `get_pipeline_jobs` | read | List a CI/CD pipeline's jobs |
| `get_job_log` | read | Read one CI/CD job's trace |

Deliberately not exposed: `get_mcp_server_version` (an operations tool an Agent
should not have); `manage_pipeline` (list/create/delete/retry/cancel dispatched
by argument combination, with a destructive branch — too vague a contract);
`semantic_code_search`, `attach_scan_profile`, `get_saved_view_work_items` (EE
only, absent from a CE instance's `tools/list`); `get_merge_request_conflicts`
and `get_work_item_types` (registered only on upstream `master`, not in the
published tool list, so older instances lack them). That accounts for
twenty-one of the twenty-three; the two remaining upstream names were not
enumerated in this review and must be reconciled from a live `tools/list`.

MCP `IsError` content and protocol/HTTP error bodies are discarded and replaced
with a stable `ToolFailure`; Provider text or structured error payloads never
cross the Tool boundary.

## Schema confidence and calibration

**Tool names are high confidence** — they are corroborated by both
`app/services/mcp/tools/manager.rb` and the published tool list.

**Input schemas are approximations and the instance's `tools/list` is the
authority.** Several were reverse-engineered from Grape parameter whitelists
and are medium confidence — `create_issue` and `get_issue` especially. No Tool
declares an `OutputSchema`, because guessing one would make the Tool return an
invalid-response failure forever.

Calibrate against a real instance at least once, and again after a GitLab
upgrade. The probe takes flags, not a positional URL:

```sh
mise run mcp-probe -- \
  -endpoint https://gitlab.example.com/api/v4/mcp \
  -token "$GITLAB_MCP_ACCESS_TOKEN"
```

Two schema decisions are deliberate and should not be "tightened":

- `link_work_items.link_type` declares **no enum**. CE source allows only
  `relates_to`; the EE documentation lists `relates_to|blocks|blocked_by`. The
  valid set varies by instance tier, so hardcoding either version breaks the
  other half of the fleet. Upstream decides.
- `get_merge_request_notes`, `create_merge_request_note`, `get_workitem_notes`,
  `create_workitem_note` and `link_work_items` accept either a `url` or the
  id/iid pair; upstream does not put that either/or in `required`, and neither
  do we.

## Runtime traps

- **Never send the `X-Gitlab-Mcp-Server-Tool-Name-Prefix` header.** It prefixes
  every upstream tool name, and every hardcoded `RemoteToolName` in the
  Definition would stop matching.
- `X-Gitlab-Enabled-Mcp-Server-Tools` is not set; leaving it unset means the
  full tool set is offered, which is what the Definition assumes.

## Runtime limits and known constraints

- Total logical call budget: 30 seconds, covering MCP handshake, DNS, dial,
  TLS, redirects and bounded response reads.
- Decoded response limit: 10 MiB per guarded MCP response; the credential
  validator caps introspection at 64 KiB.
- At most 32 Remote MCP sessions run concurrently per process; queued calls
  honor caller cancellation and deadlines.
- Same-origin redirects retain the declared bearer footprint; cross-origin
  redirects are denied, so credentials cannot move to another origin. HTTPS
  downgrade is denied unless every insecure-HTTP gate above is on.
- The SDK's implicit default HTTP client, environment proxy, standalone SSE
  connection and reconnect retries are disabled.
- HTTP `401` returns the platform credential-invalid marker; `403` is a
  state-neutral `permission_denied`.
- Pagination, filtering and output shape belong to the instance's MCP server;
  this Connector normalizes none of them.
- An instance older than the reviewed tool list may expose fewer tools.

## Upgrade notes

`ConfigSchemaVersion` stays `1`, but the backend moved from this repository's
own guarded REST handlers to the instance's official MCP server. That is an
externally visible break; existing Connections must be reconfigured with
`mcp_url` and an `mcp`-scoped OAuth token.

- **Credential type changed.** Personal, project and group access tokens no
  longer work; only an OAuth access token carrying `mcp` scope does.
- **`base_url` was replaced by the required `mcp_url`**, which must end in
  `/api/v4/mcp`. The old scope matcher that treated `api` as implying
  `read_api` is gone with the REST path that needed it — and, importantly,
  `api` does **not** imply `mcp`.
- **The credential probe changed from `GET /api/v4/user` to
  `GET {instance}/oauth/token/info`, and this fixed a real bug**: a legitimate
  token holding only the `mcp` scope is `403`ed by `/api/v4/user`, so the old
  probe marked a correct credential as dead. The new probe also returns the
  token's real scope list, so `ScopesKnown` moved from `false` to `true` and
  `RequiredScopes: ["mcp"]` is now actually checked instead of being inert.
- **Output shape is whatever the MCP server returns.** The previous normalized
  envelopes and `X-Page` pagination guarantees are gone.

### Capability regressions (not sugar-coated)

- `get_current_user` — **no equivalent upstream tool. Capability lost.**
- `get_project` — **no equivalent upstream tool. Capability lost.**
- `list_projects`, `list_issues`, `list_merge_requests` — **approximated, not
  replaced**, by `search` with `scope=projects|issues|merge_requests`. The
  filtering vocabulary is Search API syntax rather than the old list
  parameters, and the returned shape is whatever the instance emits, not the
  old pagination envelope. Callers relying on the previous field layout or page
  metadata must be rewritten.

Only `create_issue` and `get_merge_request` map one-to-one from the old Managed
implementation. In exchange, the MCP set adds merge-request review (diffs,
notes, comments), CI triage (`get_pipeline_jobs`, `get_job_log`) and work-item
collaboration, which the Managed implementation never had.

Re-review the official tool list, the token-scope semantics, the egress gates
and this document's Source section before changing the endpoint contract.

## Real-account smoke procedure

**There is currently no real smoke test for GitLab.** The previous
`real_smoke_test.go` (980 lines exercising the Managed REST path) was deleted
in full along with the implementation it covered; nothing replaced it. Adding
one requires an instance with GitLab Duo enabled and an OAuth application
carrying the `mcp` scope, which is why it has not been done yet.

Until then, verification is the `mcp:verify` / `mise run mcp-probe` path
against a real instance:

1. Configure `mcp_url` for a disposable non-production instance and store an
   OAuth access token with the `mcp` scope for a user holding
   `execute_mcp_tool`.
2. Confirm credential validation reports the expected subject and scope set,
   and that a PAT is rejected.
3. Reconcile the live `tools/list` with the fourteen declared tool names and
   their argument shapes; correct the Definition's schemas from what you see.
4. Call each read Tool against dedicated fixtures; call each write Tool only in
   a disposable project and remove what it creates.
5. Verify another host, loopback, metadata, link-local, redirect, userinfo,
   query, fragment and traversal variants remain blocked, and that private or
   HTTP addressing fails until every documented gate is on.
6. Confirm audit and health output contains stable operation and status
   metadata but no token, MCP payload, project content or Provider error body.
7. Revoke the token and destroy or sanitize the test instance.

Record instance class, version, token type and scope (never its value), date,
results and cleanup outcome below.

## Smoke records

| Date | Kind | Account/auth class | Environment | API/server version | Coverage | Cleanup status | Result |
|---|---|---|---|---|---|---|---|
| 2026-07-24 | deterministic mock | N/A | `httptest` behind the guarded providerkit transport, no public network | official MCP tool list reviewed 2026-07-24 | instance-root derivation from `mcp_url`, Doorkeeper introspection, unknown-scope fallback, pre-request config rejection, insecure-HTTP gate, failure mapping without leaks, account-ID scoping | N/A — no external resources | PASS (`go test -count=1 ./gitlab`) |
| 2026-07-24 | deterministic mock | N/A | official Go MCP SDK over guarded `httptest` in `packages/service/mcpclient` | N/A — transport level | bearer binding, cross-origin redirect denial, response cap, timeout, session concurrency, `IsError` payload redaction | N/A — no external resources | PASS (`go test ./mcpclient`) |
| — | real account | disposable instance; OAuth access token with `mcp` scope | instance with Duo enabled, MCP server on, and an OAuth application carrying `mcp` scope | PENDING — record the instance version at execution | none — no harness exists | PENDING — not run | **PENDING**; no real-account claim has been made for the MCP backend |

An earlier real-account PASS was recorded on 2026-07-23 against the deleted
guarded-REST implementation (a disposable GitLab CE `19.2.0` instance,
`api`-scoped PAT, seven Tool happy paths). That evidence pertains to code that
no longer exists and is not carried forward as coverage of this Provider.
