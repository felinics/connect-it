# Google Ads Provider

Connector type: `google_ads`

## Source and protocol

- Official Google Ads MCP server guide:
  <https://developers.google.com/google-ads/api/docs/developer-toolkit/mcp-server>
- Google Ads API v24:
  <https://developers.google.com/google-ads/api/reference/rpc/v24/overview>
- Google Ads authentication:
  <https://developers.google.com/google-ads/api/rest/auth>
- List accessible customers:
  <https://developers.google.com/google-ads/api/samples/list-accessible-customers>
- Reviewed official implementation:
  `googleads/google-ads-mcp` commit
  `f48a6b85e1f43ebd44a72531c9611e2b7265ca28`, Apache-2.0.
- Audited official repository and exact paths:
  <https://github.com/googleads/google-ads-mcp>,
  `ads_mcp/server.py`, `ads_mcp/tools/core.py`,
  `ads_mcp/tools/search.py`, and `pyproject.toml`.
- Reviewed behavioral reference:
  `oomol-lab/open-connector` commit
  `dbc50f5b46e978a9b825f92a1f5aed585f8408d4`, Apache-2.0, under
  `src/providers/googleads/`.
- Audited behavioral-reference repository and exact paths:
  <https://github.com/oomol-lab/open-connector>,
  `src/providers/googleads/actions.ts`,
  `src/providers/googleads/definition.ts`,
  `src/providers/googleads/executors.ts`, and
  `src/providers/googleads/scopes.ts`.
- API version and source review date: `v24`, 2026-07-23.

Google Ads uses the official MCP implementation as a self-hosted server. The
administrator supplies one `mcp_url`; the runtime derives exactly that URL's
canonical origin and does not accept a separate allowlist from Tool arguments
or another configuration field.

## Authentication and configuration

Configure:

- `client_id`
- secret `client_secret`
- `project_id`
- secret `developer_token`
- `customer_id` as exactly 10 digits without hyphens
- `mcp_url` for the deployed `googleads/google-ads-mcp` Streamable HTTP server
- `allow_insecure_http`, normally the default `false`

Register the deployment's documented connect-it OAuth callback URL exactly.
OAuth requests offline access and the
`https://www.googleapis.com/auth/adwords` scope. The self-hosted server must be
configured independently with the corresponding Google Ads developer token;
connect-it does not send the developer token to the MCP endpoint.

After changing `mcp_url` or a network-policy field, run the administrative
`mcp:verify` flow before Tool execution. Verification records the exact
endpoint identity but never stores response bodies or credentials.

## Egress gates

The server is always constructed as `SelfHostedOptIn`.

- HTTPS public endpoints need no network relaxation.
- RFC1918/CGNAT access additionally requires
  `CONNECT_IT_ALLOW_PRIVATE_PROVIDER_NETWORK=true`.
- Plain HTTP additionally requires both
  `allow_insecure_http=true` in normalized Connector configuration and
  `CONNECT_IT_ALLOW_INSECURE_PROVIDER_HTTP=true` in deployment configuration.
- A private plain-HTTP endpoint therefore needs both independent deployment
  gates plus the Connector's explicit HTTP opt-in.
- Loopback/localhost, link-local, cloud metadata, multicast, unspecified and
  reserved/documentation addresses remain permanently blocked even when the
  private-network gate is enabled.

Production should keep both deployment switches at `false` unless a reviewed
self-hosted topology requires otherwise.

## Tools

| Tool ID | Risk | Purpose |
|---|---:|---|
| `list_accessible_customers` | read | List customers accessible to the OAuth identity |
| `search` | read | Run a GAQL query for a selected customer |

Tool IDs, schemas and successful Text/Structured output remain compatible with
the pre-providerkit implementation. MCP `IsError` content and protocol/HTTP
error bodies are discarded and replaced with a stable `ToolFailure`; raw GAQL
or Provider error details are not reflected to an Agent.

## Runtime limits and known constraints

- Total logical call budget: 60 seconds, including handshake, DNS, dial,
  redirects and bounded response reads.
- Decoded response limit: 10 MiB for each guarded HTTP response.
- At most 32 Remote MCP sessions run concurrently per process; queued calls
  honor caller cancellation/deadlines.
- Same-origin redirects may retain the bearer footprint. Cross-origin
  redirects are not declared and are denied; HTTPS downgrade is always denied.
- The SDK's implicit default HTTP client, environment proxy, standalone SSE
  connection and reconnect retries are disabled.
- Tool input schemas are permissive approximations of the pinned MCP server's
  `tools/list`; `mcp:verify` (or `mise run mcp-probe`) is what reconciles the
  two declared mappings against the live list. `search` accepts arbitrary valid
  GAQL within the user's Google Ads permissions; it does not add a second
  query-language sandbox.
- Neither Tool declares an `OutputSchema`, so a change in the self-hosted
  server's output shape reaches the Agent unvalidated. That is the current
  state, not an engine limit: `packages/service/exec/engine.go` routes Remote
  and Managed results through the same `finish()`, which validates whatever
  output schema the Definition declares. Declaring one requires sampling real
  responses from the pinned server version first.
- Google Ads access and developer-token approval are external prerequisites.

## Upgrade notes

`ConfigSchemaVersion` is `2`. The version 1→2 upgrader adds
`allow_insecure_http: "false"` when absent, so existing installations remain
HTTPS-only. The migration does not change connector type, Tool IDs or OAuth
scope. Changing `mcp_url` or the network flag changes policy identity, bumps
authorization generation and invalidates stale Session grants.

Re-review the official MCP commit, Google Ads API version, OAuth scope,
runtime Definition contract and this document's Source section before a
server/API upgrade.

## Real-account smoke procedure

`TestGoogleAdsRealSmoke` is skipped when none of these variables is
configured:

- `CONNECT_IT_GOOGLE_ADS_SMOKE_ACCESS_TOKEN`;
- `CONNECT_IT_GOOGLE_ADS_SMOKE_DEVELOPER_TOKEN`;
- `CONNECT_IT_GOOGLE_ADS_SMOKE_CUSTOMER_ID`;
- `CONNECT_IT_GOOGLE_ADS_SMOKE_MCP_URL`; and
- `CONNECT_IT_GOOGLE_ADS_SMOKE_ALLOW_INSECURE_HTTP=false` (normally).

Once any variable is present, all five are required. The network value must
be exactly `true` or `false`; an HTTP MCP URL also requires it to be exactly
`true` and still remains subject to the independent deployment gates in
“Egress gates.” Use a Google Ads test account, non-production Google Cloud
project, approved test developer token, short-lived OAuth token and reviewed
self-hosted MCP deployment. Export credentials without placing them in shell
history. After the implementation is committed, run the fail-closed release
gate from the repository root. The SHA must be the full current `HEAD`, the
repository must be clean, and the receipt must be an absolute path that does
not exist:

```sh
bash scripts/run-provider-real-smoke.sh \
  google_ads \
  '<FULL_APPROVED_HEAD_SHA>' \
  '/protected/provider-smoke/google_ads.tsv'
```

The gate runs only `TestGoogleAdsRealSmoke`, rejects the default no-environment
skip, withholds raw Go JSON/stderr, and publishes a mode-`0600` no-clobber
receipt only after the exact test and package explicitly pass. Its
`cleanup_mode` records that Provider mutations are not made and external
authorization/deployment cleanup remains required; it does not claim that
cleanup completed.

The harness first validates the OAuth and developer tokens against the fixed
Google Ads API v24 endpoint and proves a known-invalid OAuth token is rejected
as unauthorized. It then creates the same exact-origin dynamic providerkit
policy used for self-hosted MCP and invokes both reviewed read-only mappings
through the official MCP Go SDK. It does not call `tools/list`; that
reconciliation is the `mcp:verify` step of the release check below. The bounded
GAQL selects only campaign ID/name and limits the result to one row. The developer token is never sent to the MCP endpoint. The harness never
prints either credential, customer lists, GAQL results, MCP payloads, endpoint
query data or Provider error text, and it creates no Provider resource.

For the complete release check:

1. Configure all fields, complete OAuth and run `mcp:verify`.
2. Run the harness and confirm, outside recorded logs, that only expected test
   customers and non-sensitive campaign metadata are visible.
3. Repeat once through the intended production network topology (public HTTPS
   by preference); do not enable private/HTTP gates merely for convenience.
4. Confirm audit/health output contains stable operation/status metadata, but
   no access token, developer token, GAQL result body, URL query or raw error.
5. Revoke the test authorization and remove the smoke MCP deployment if it was
   temporary.

Record account/developer-token class, MCP commit, API version, topology, date,
tool results and cleanup outcome below; never record credentials or GAQL
results.

## Smoke records

| Date | Kind | Account/auth class | Environment | API/server version | Coverage | Cleanup status | Result |
|---|---|---|---|---|---|---|---|
| 2026-07-25 | deterministic mock | N/A | `providerkit/testkit` and local `httptest`, no public network | Google Ads API `v24` | credential-validator use of the official `listAccessibleCustomers` endpoint, pre-request developer-token requirement, safe authorization-failure mapping, malformed customer-list rejection, `allow_insecure_http` v1→v2 upgrade, smoke-config MCP endpoint bounds | N/A — no external resources | PASS (`go test -count=1 ./googleads`) |
| 2026-07-23 | deterministic mock | N/A | official Go MCP SDK over guarded `httptest` in `packages/service/mcpclient` | N/A — transport level | self-hosted HTTP double gate, RFC1918 deployment gate, permanent IP blocks, exact origin redirects, bearer isolation, bounded body, timeout, safe status mapping, `IsError` payload redaction and concurrency queue | N/A — no external resources | PASS (`go test ./mcpclient -count=5`) |
| — | real account | Google OAuth test identity and approved developer-token class | test Google Ads account, non-production Cloud project and reviewed MCP deployment required | PENDING — record API version and exact MCP commit at execution | validator and negative-auth probe, `mcp:verify` `tools/list` reconciliation, both read-only Tool calls, topology and audit redaction | PENDING — not run; N/A for read-only harness, then revoke authorization/remove temporary deployment | PENDING; no real-account claim has been made |

The transport row is coverage of `packages/service/mcpclient`, not of the
`googleads` package: those tests drive a synthetic Definition, so they pin the
Remote MCP transport every Provider shares rather than Google Ads' own
mappings or its pinned server commit.

The deterministic mock records are reproducible unit evidence, not a substitute
for the release-gated real-account smoke.
