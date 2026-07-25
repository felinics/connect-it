# Stripe Provider

Connector type: `stripe`

Stripe is served by Stripe's **official Remote MCP server**. This connector owns
no hand-written REST client. Concretely that means:

- **Stripe defines the Tools.** Tool names, arguments and results are the
  upstream server's contract, not ours.
- **Arguments pass through unchanged.** Each Tool here is a fixed mapping onto
  exactly one reviewed upstream tool name; the Definition adds no request
  shaping, no pagination, no response reshaping.
- **We are an authenticated, policed forwarder.** What this repository still
  owns is the part that must not be delegated: which upstream tools are exposed
  at all, the Risk each one carries, credential storage and injection, the
  fixed endpoint and hostname allowlist, timeouts and response caps, and error
  redaction.

## Source and protocol

- Official Stripe MCP server: <https://docs.stripe.com/mcp>
- Stripe API keys and restricted keys: <https://docs.stripe.com/keys>
- Restricted-key permission units:
  <https://docs.stripe.com/stripe-apps/reference/permissions>
- Stripe API authentication (used by credential validation only):
  <https://docs.stripe.com/api/authentication>
- Account object returned by the validator: <https://docs.stripe.com/api/accounts>
- Reviewed behavioral reference: `oomol-lab/open-connector` commit
  `dbc50f5b46e978a9b825f92a1f5aed585f8408d4`, Apache-2.0, audited at
  <https://github.com/oomol-lab/open-connector>, exact paths
  `src/providers/stripe/definition.ts`, `src/providers/stripe/actions.ts` and
  `src/providers/stripe/executors.ts`.
- Validator API version and source review date: `2026-06-24.dahlia`, 2026-07-24.

The Definition fixes the Remote MCP endpoint to `https://mcp.stripe.com`
(`EndpointFixed`, `ProvenanceOfficial`) and the allowed hostname to
`mcp.stripe.com`. Neither Connector configuration nor Tool arguments can
replace it or opt it into private-network or plain-HTTP access.

## Enablement prerequisites

This is the part integrators trip over. Before a Connection can work:

1. **A Stripe account with Dashboard access**, since the credential can only be
   created and narrowed there. No separate MCP application, allowlist or
   waitlist is known to be required — the server accepts any valid Stripe API
   key as a bearer token. *Unverified* against a live account; see Smoke
   records.
2. **A Restricted API Key (`rk_...`), created in the Stripe Dashboard**, with
   the permission units needed by the Tools you intend to grant (see the Tools
   table). A full secret key (`sk_...`) also works and is strongly discouraged
   — see the permission model below.
3. **The right account mode.** A test-mode key reaches test-mode data only and
   a live-mode key moves real money. Smoke runs are hard-gated to test-mode
   keys; production Connections are not, so pick the mode deliberately.
4. **Treasury enabled**, if you need `get_balance_summary`. It is a public
   preview upstream and may be absent from `tools/list` for accounts without
   Treasury.
5. **A standalone Stripe account, not a Connect platform sub-account.** See the
   Connect limitation under runtime constraints — connected accounts are not
   reachable through this connector at all.

Official smoke check of the raw server, outside this service:

```sh
curl https://mcp.stripe.com/ -H "Authorization: Bearer <key>"
```

## Authentication and permission model

Upstream accepts two credential kinds on the same bearer scheme: a Stripe API
key used directly as the token, and OAuth 2.0. **This connector implements only
the API-key path.** The single auth method is `api_key`, an opaque server-side
key in the secret credential field `api_key`, presented as
`Authorization: Bearer`. The Definition declares no OAuth auth method, so there
is no OAuth flow to configure here.

> **Tool-level permission is not negotiated in the MCP protocol.** Whatever the
> key grants is exactly what an Agent can reach through the upstream server.
> The Stripe Dashboard's restricted-key scoping is the only permission boundary
> that exists. Use `rk_...`, not `sk_...`.

The key is treated as opaque: the credential field's validation pattern rejects
whitespace and colons only, and asserts no `sk_`/`rk_` prefix, so Stripe can
change its key format without breaking stored Connections.

### Credential validation

Validation is the only egress this package performs itself; Tool traffic goes
to the Remote MCP server through the engine. It runs a guarded
`GET https://api.stripe.com/v1/account` with the pinned
`Stripe-Version: 2026-06-24.dahlia` so the validated response shape cannot
drift, presenting the key as a bearer token exactly the way the MCP server
will. A valid response must contain a non-empty opaque account ID and
`object=account`; the account ID becomes `CredentialProfile.AccountID` and the
display name falls back from account email to account ID.

Stripe exposes no reliable permission introspection for arbitrary keys, so
validation returns `ScopesKnown=false` and infers no grants. **A restricted key
can validate successfully and still be denied a later operation by its own
permission policy.**

## Tools

| Tool ID | Purpose | Risk | Permission metadata |
|---|---|---:|---|
| `get_stripe_account_info` | Retrieve the Stripe account this credential authenticates | read | `connected_account_read` |
| `get_balance_summary` | Summarize funds across the Stripe balance and Treasury accounts | read | `balance_read` |
| `search_stripe_resources` | Search Stripe objects through the Stripe Search API | read | — |
| `fetch_stripe_resources` | Fetch Stripe objects by identifier | read | — |
| `create_refund` | Refund a PaymentIntent, fully or by amount in the smallest currency unit | **destructive** | `refund_write` |

Every Tool ID is also its upstream remote tool name; the mapping is 1:1 by
design so a reviewer can read the Definition and know exactly what is called.

`RequiredScopes` name Stripe restricted-key permission units and are
**documentation only**: the validator always reports `ScopesKnown=false`, so
nothing is preflighted against them. Tools that legitimately span arbitrary
resources declare none.

`create_refund` is classified destructive rather than write because refunds
move real money and cannot be undone.

MCP `IsError` content and protocol/HTTP error bodies are discarded and replaced
with a stable `ToolFailure`; Provider text or structured error payloads never
cross the Tool boundary.

### Tools deliberately not exposed

The upstream server publishes 13 tools; 5 are exposed. This is a product
decision, not an oversight:

- `stripe_api_read` / `stripe_api_write` / `stripe_api_search` /
  `stripe_api_details` are a **generic REST proxy** over roughly 86 Stripe API
  methods. The blueprint's "explicitly out of scope" list rules out generic
  proxy Tools, and `stripe_api_write` is the clearest case for that rule: it
  would hand an Agent one universal key to every write endpoint the credential
  permits, with no per-capability grant, no reviewable per-Tool Risk, and no
  argument surface a Definition could police.
- `search_stripe_documentation`, `stripe_implementation_planner` and
  `send_stripe_mcp_feedback` are developer aids, not account capabilities.
- `stripe_report` mixes report reads with report-run creation behind a single
  undocumented schema, so no honest Risk can be assigned yet. Deferred until a
  `tools/list` dump settles its shape.

### Capability regression versus the previous REST connector

Not sugar-coated: the upstream remote toolset is not a 1:1 replacement for the
previous hand-written REST Tools, and some capability was lost.

- **Customer and product CRUD is gone.** `list_customers`, `get_customer`,
  `create_customer`, `list_products` and `create_product` have no named
  upstream equivalent — Stripe routes that per-resource CRUD through the
  generic `stripe_api_*` proxy tools, which are deliberately not exposed.
  `search_stripe_resources` and `fetch_stripe_resources` may cover some read
  cases, but with unpublished argument names and no guarantee of parity.
- **No writes other than refunds.** The only exposed write is `create_refund`.
- **Connect sub-accounts became unreachable** (see below).

What was gained: no hand-maintained REST surface to drift, Stripe-owned tool
semantics, and a much smaller reviewable footprint.

## Schema accuracy and calibration

Schemas here are **loose approximations and should be treated as unverified.**
Stripe publishes tool names but, for all but a couple of tools, no input
schema. So:

- Every Tool schema keeps `"additionalProperties": true`. Remote MCP arguments
  pass through and the upstream server is the authority; an over-strict schema
  would reject valid calls.
- `create_refund` is the only Tool declaring confirmed properties
  (`payment_intent`, `amount`).
- `get_stripe_account_info`, `get_balance_summary`, `search_stripe_resources`
  and `fetch_stripe_resources` publish **empty `properties`**. A guessed
  property name actively misleads an Agent, whereas an empty open schema still
  passes every argument through. Expected parameters are described in prose in
  each Tool's Description.
- No Tool declares an `OutputSchema`: no captured response sample exists, and a
  wrong `OutputSchema` would make the Tool fail permanently at runtime.

Calibrate before trusting them. Dump the real upstream contract with the probe,
which takes flags and requires a key — it performs a real authenticated
`tools/list`:

```sh
mise run mcp-probe -- -endpoint https://mcp.stripe.com -token "$STRIPE_RK_TEST_KEY"
```

and reconcile the stored contract through the admin endpoint
`POST /admin/connectors/stripe/mcp:verify`. Any schema change here should cite
that dump.

## Runtime limits and known constraints

- Total logical call budget: 30 seconds, including MCP handshake, DNS, dial,
  TLS, redirects and bounded response reads.
- Decoded response limit: 10 MiB per guarded HTTP response (64 KiB for the
  credential validator).
- At most 32 Remote MCP sessions run concurrently per process; queued calls
  honor caller cancellation/deadlines.
- Same-origin redirects are allowed and retain the declared bearer footprint.
  Cross-origin redirects are not declared and are denied, so credentials cannot
  move to another origin. HTTPS downgrade is always denied.
- The SDK's implicit default HTTP client, environment proxy, standalone SSE
  connection and reconnect retries are disabled.
- **Stripe Connect is not supported.** Platform use requires an extra
  `Stripe-Account: acct_...` request header. `MCPAuthBinding` carries only a
  scheme plus a credential field and cannot attach additional headers, so
  connected sub-accounts are out of scope. One Connection speaks for exactly
  one Stripe account: the account owning the key.

## Upgrade notes

`ConfigSchemaVersion` is `1` and unchanged, but this release replaced the
hand-written **Managed REST** connector with the **Remote MCP** connector.

- **No credential migration.** The connector type and the `api_key` credential
  field are unchanged; the same key value keeps working.
- **Credential presentation changed** from HTTP Basic (against
  `api.stripe.com`) to `Authorization: Bearer` (against `mcp.stripe.com`).
- **Tool IDs changed completely.** The previous `identify_account`,
  `list_customers`, `get_customer`, `create_customer`, `list_products` and
  `create_product` Tools no longer exist. **Session grants naming the old Tool
  IDs no longer authorize anything and must be reissued.** See the capability
  regression section for what has no replacement.
- Re-review the fixed endpoint, allowed hostname, exposed tool selection,
  validator API version and this document's Source section before changing the
  hosted MCP contract.

## Real-account smoke procedure

`TestStripeRealSmoke` (in `packages/connectors/stripe/real_smoke_test.go`) is
retained and skipped unless `CONNECT_IT_STRIPE_SMOKE_API_KEY` is configured.
The harness hard-rejects anything that is not an `sk_test_`/`rk_test_` key, so
a live-mode key cannot be used. Prefer a short-lived restricted key in a
dedicated Stripe **test-mode** account.

There is no write gate because the harness performs no writes. `create_refund`
moves real money and is irreversible, so it is deliberately never exercised;
consequently there are no Provider resources to clean up.

After the implementation is committed, run the fail-closed release gate from
the repository root. The SHA must be the full current `HEAD`, the repository
must be clean, and the receipt must be an absolute path that does not exist:

```sh
bash scripts/run-provider-real-smoke.sh \
  stripe \
  '<FULL_APPROVED_HEAD_SHA>' \
  '/protected/provider-smoke/stripe.tsv'
```

The gate runs only `TestStripeRealSmoke`, rejects the default no-environment
skip, withholds raw Go JSON/stderr, and publishes a mode-`0600` no-clobber
receipt only after the exact test and package explicitly pass.

What the harness actually does today, precisely:

1. Validates the key against the Stripe API and asserts the profile carries an
   account ID with `ScopesKnown=false` and no granted scopes.
2. Proves a known-invalid key is rejected.
3. Builds the same exact-origin guarded providerkit policy used at runtime and
   opens one Remote MCP session over it.
4. Validates arguments against the compiled Definition schema and calls exactly
   **one** read-only Tool, `get_stripe_account_info`, through its reviewed
   remote mapping.

It does **not** reconcile the full upstream `tools/list` — that remains the job
of `mise run mcp-probe` / `mcp:verify`. It never prints the key, the account
profile, MCP payloads or Provider error text.

For the complete release check:

1. Create a restricted-key Connection and run the harness.
2. Confirm Session authorization prevents `create_refund` unless it was
   explicitly granted.
3. Confirm audit/health output contains stable operation and status metadata,
   but no key, account profile, MCP payload or Provider error body.
4. Revoke the test key.

Record account class, key type (never its value), date, tool results and
cleanup outcome below; never record credentials or business content.

## Smoke records

| Date | Kind | Account/auth class | Environment | API/server version | Coverage | Cleanup status | Result |
|---|---|---|---|---|---|---|---|
| 2026-07-24 | deterministic mock | N/A | `providerkit/testkit` and local `httptest`, no public network | Stripe API `2026-06-24.dahlia` for validation; hosted MCP contract reviewed 2026-07-24 | fixed endpoint/hostname derivation, bearer binding, strict validator, exposed/withheld tool set, schema compilation and pass-through | N/A — no external resources | PASS (`go test -count=1 ./stripe`) |
| — | real account | dedicated test-mode account; short-lived restricted `rk_test_` key | dedicated Stripe test-mode account required | PENDING — record hosted MCP contract and `Stripe-Version` at execution | validator plus negative-auth probe, then one read-only Tool call | N/A — read-only harness creates no Provider resource; revoke the key afterwards | **PENDING** — no real-account run or claim has been made |

The deterministic mock record is reproducible unit evidence, not a substitute
for the release-gated real-account smoke. Everything in this document marked
*unverified* stays unverified until that row turns green.
