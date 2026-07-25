# Datadog Provider

Connector type: `datadog`

## Provider form: unverified

Datadog has never been checked against the four Remote MCP criteria in
`blueprint.md` §8. The survey behind `docs/provider-mcp-decision-table.md`
covered twelve categories and none of them included Datadog, so this connector
being Managed REST is an unreviewed default rather than a conclusion. Absence
from that table is **not** evidence that Datadog has no official MCP server,
and nothing in this document should be read as evidence that it does.

Before the next form decision, check it once — starting at
`docs.datadoghq.com`, and testing specifically whether anything found is a
docs-only server rather than one that reaches the account's own data — and
record the result in the decision table.

## Source and protocol

- Authentication:
  <https://docs.datadoghq.com/api/latest/authentication/>
- API and Application Keys:
  <https://docs.datadoghq.com/account_management/api-app-keys/>
- Validate API and Application Keys:
  <https://docs.datadoghq.com/api/latest/key-management/validate-api-and-application-keys/>
- Get all monitors:
  <https://docs.datadoghq.com/api/latest/monitors/get-all-monitors/>
- Get a monitor's details:
  <https://docs.datadoghq.com/api/latest/monitors/get-a-monitors-details/>
- Get active metrics:
  <https://docs.datadoghq.com/api/latest/metrics/get-active-metrics-list/>
- Get metric metadata:
  <https://docs.datadoghq.com/api/latest/metrics/get-metric-metadata/>
- Query timeseries points:
  <https://docs.datadoghq.com/api/latest/metrics/query-timeseries-points/>
- Custom metric naming:
  <https://docs.datadoghq.com/metrics/custom_metrics/>
- Datadog Sites:
  <https://docs.datadoghq.com/getting_started/site/>
- Reviewed implementation reference:
  `oomol-lab/open-connector` commit
  `dbc50f5b46e978a9b825f92a1f5aed585f8408d4`, Apache-2.0, specifically
  `src/providers/datadog/definition.ts`, `actions.ts`, and `executors.ts`.
- Audited repository and exact paths:
  <https://github.com/oomol-lab/open-connector>,
  `src/providers/datadog/definition.ts`,
  `src/providers/datadog/actions.ts`, and
  `src/providers/datadog/executors.ts`.
- API versions and source review date: Datadog API `v1/v2`, 2026-07-23.

The code-defined Definition is the source of truth for credentials, site
selection, Tool IDs, strict schemas, permission metadata, and risk.
OpenConnector informed the initial action and response-shape review. Official
Datadog documentation is authoritative. In particular, connect-it does not
copy OpenConnector's API-key-only `/api/v1/validate` credential check.

## Authentication and fixed sites

The `api_keys` custom-credential method has exactly three per-Connection
fields:

- secret `api_key`, sent only as `DD-API-KEY`;
- secret `application_key`, sent only as `DD-APPLICATION-KEY`; and
- non-secret `site`, a required `InputSelect` with default `us1`.

Both keys are required for validation and all five read Tools. They are never
placed in a URL, policy identity, error, health row, observer event, or audit
output. Both header names belong to one authorizer credential footprint, so
redirect stripping and redaction handle the pair together.

`site` is a closed enum. It cannot contain a hostname or arbitrary base URL:

| Credential value | Official site | Fixed API hostname |
|---|---|---|
| `us1` | US1 | `api.datadoghq.com` |
| `us3` | US3 | `api.us3.datadoghq.com` |
| `us5` | US5 | `api.us5.datadoghq.com` |
| `eu` | EU1 | `api.datadoghq.eu` |
| `ap1` | AP1 | `api.ap1.datadoghq.com` |
| `ap2` | AP2 | `api.ap2.datadoghq.com` |
| `uk1` | UK1 | `api.uk1.datadoghq.com` |
| `us1_fed` | US1-FED | `api.ddog-gov.com` |
| `us2_fed` | US2-FED | `api.us2.ddog-gov.com` |

The Datadog Sites page calls the European site `EU1`, while the API reference
site selector displays `EU`. The stable credential value is `eu`. Internally,
each hostname becomes one canonical HTTPS origin with effective port `443`.
There is no self-hosted, private-network, or insecure-HTTP mode.

Credential validation calls `GET /api/v2/validate_keys` on the selected fixed
site with both key headers. That endpoint does not provide a stable
organization ID or enumerate Application Key permissions. Consequently:

- `AccountID` remains empty;
- `DisplayName` is the explicit fallback `Datadog <site>`, not a claimed
  account identity;
- `ScopesKnown=false` and `GrantedScopes` is empty; and
- validation success does not imply that every Tool permission is granted.

Application Key permission names are case-sensitive. Newly created or rotated
keys can be temporarily rejected while Datadog propagates them. A validation
`401` or `403` remains `authorization_failed` with safe retry guidance; the
transport must not automatically retry authentication failures or treat them
as permanent organization identity.

## Tools

| Tool ID | Risk | Required permission | Endpoint |
|---|---:|---|---|
| `list_monitors` | read | `monitors_read` | `GET /api/v1/monitor` |
| `get_monitor` | read | `monitors_read` | `GET /api/v1/monitor/{monitor_id}` |
| `list_active_metrics` | read | `metrics_read` | `GET /api/v1/metrics` |
| `get_metric_metadata` | read | `metrics_read` | `GET /api/v1/metrics/{metric_name}` |
| `query_timeseries_points` | read | `timeseries_query` | `GET /api/v1/query` |

These permissions are required metadata, not inferred grants. Because
credential validation cannot introspect Application Key permissions, Datadog
can still return `403 permission_denied` during execution.

### Monitor inputs and outputs

`list_monitors` always sends `page` and `page_size`, including when the caller
omits them. Schema defaults are `page=0` and `page_size=100`; `page_size` is
bounded to `1`–`100`. This is a safety boundary: Datadog returns all monitors
without pagination when `page` is omitted.

Optional `group_states` values are exactly `all`, `alert`, `warn`, and
`no data`. The `tags` and `monitor_tags` arrays are bounded and reject commas
or control characters before the runtime joins them into Datadog's
comma-separated query parameters. `get_monitor` accepts only a positive,
JSON-safe integer ID.

Both Tools return normalized monitor objects with only:

- nullable `id`, `name`, `type`, `query`, `message`, and `overall_state`; and
- non-null `tags`, normalized to an empty string array when absent.

Unknown upstream fields are deliberately not copied to structured output.

### Metric inputs and outputs

`list_active_metrics` requires non-negative Unix seconds in `from`. Optional
`host` and `tag_filter` are mutually exclusive because Datadog documents that
`tag_filter` cannot be combined with other filters. Datadog returns `from` as
a decimal string; the structured output preserves that documented type and
normalizes `metrics` to a string array.

`get_metric_metadata` accepts one bounded Datadog metric name, percent-encodes
it once as a path segment, and returns:

- the input-derived `metric_name`; and
- nullable `description`, `integration`, `per_unit`, `short_name`,
  `statsd_interval`, `type`, and `unit`.

The API response itself has no metric-name property; including the
input-derived value is an explicit normalization, not an upstream claim.

`query_timeseries_points` requires non-negative `from` and `to`, a bounded
query expression, and runtime validation that `from <= to`. Connect-it also
applies its own 31-day maximum query window; this is a first-release resource
bound, not a Datadog-documented API limit. Its response must be a JSON object
whose `status` is exactly `ok`; a missing or null status is
`invalid_response`, while another status is `provider_error`, even when
Datadog returned HTTP 200.
`response_type` remains nullable, and `series` is normalized. Missing or null
upstream series, unit arrays, and pointlists normalize to empty arrays. Series
strings are nullable, unit entries can be null, and each point is an exact
timestamp/value pair. The timestamp must be a non-negative, JSON-safe,
mathematically integral number in milliseconds (`123`, `123.0`, and an
equivalent exponent form normalize to the same integer); fractional
timestamps and string coercion are rejected. Only the point value may be
null.

## Runtime limits and error behavior

- Total request budget: 30 seconds, including DNS, dial, TLS, retry attempts,
  and bounded response reading.
- Decoded response limit: 10 MiB.
- Timeseries query window: at most 31 days.
- Redirects are denied. Only the one reviewed origin selected by `site` is
  authorized for a request.
- Tool inputs and normalized successful outputs are validated against the
  code-defined schemas.
- `400` is `invalid_input`; execution `401` is credential-invalid; `403` is
  state-neutral `permission_denied`; `404` is `not_found`; and `429` is
  `rate_limited` with only a safe bounded retry hint.
- Provider error bodies, queries, metric expressions, monitor content, and
  credential values never enter failure text, health, observer events, or
  audit records.
- The first release does not support OAuth, monitor mutation, arbitrary
  Datadog endpoints, caller-provided origins, v2 metrics pagination, logs,
  traces, dashboards, or events.

## Upgrade notes

This is the initial `datadog` connector (`ConfigSchemaVersion` `1`), so there
is no prior configuration migration.

Adding an official Datadog site requires one reviewed change that updates the
credential enum, fixed hostname map, Definition tests, and this document.
Existing site values must retain their meaning. Do not replace `site` with a
free-text URL. Re-review the official endpoint versions, permission names,
schemas, this document's Source section, and real-account evidence before changing any
credential field or published Tool ID.

## Real-account smoke procedure

The opt-in `TestDatadogRealSmoke` harness is skipped unless all five variables
below are present:

- `CONNECT_IT_DATADOG_SMOKE_SITE`;
- `CONNECT_IT_DATADOG_SMOKE_API_KEY`;
- `CONNECT_IT_DATADOG_SMOKE_APPLICATION_KEY`;
- `CONNECT_IT_DATADOG_SMOKE_MONITOR_ID`; and
- `CONNECT_IT_DATADOG_SMOKE_METRIC_NAME`.

It validates the key pair and calls all five read-only Tools over real HTTPS,
using explicit monitor pagination and a five-minute metric window. Every input
and structured output is checked against the compiled Definition schemas.
The harness does not print either key, the generated metric query, or Provider
response bodies. It is the repeatable happy-path runner; the permission,
negative-site, audit/health, rate-limit, and cleanup checks below remain
manual release evidence.

After the implementation is committed, run the fail-closed release gate from
the repository root. The SHA must be the full current `HEAD`, the repository
must be clean, and the receipt must be an absolute path that does not exist:

```sh
bash scripts/run-provider-real-smoke.sh \
  datadog \
  '<FULL_APPROVED_HEAD_SHA>' \
  '/protected/provider-smoke/datadog.tsv'
```

The gate runs only `TestDatadogRealSmoke`, rejects the default no-environment
skip, withholds raw Go JSON/stderr, and publishes a mode-`0600` no-clobber
receipt only after the exact test and package explicitly pass. Its
`cleanup_mode` describes the required cleanup path; it does not claim that key
revocation or other external cleanup completed. Record cleanup separately
below.

Use a dedicated, non-production Datadog organization or test account, keys
with an explicit short lifetime where available, and non-sensitive fixtures.
Never record either key value.

1. Select the account's actual `site`; prove that another site fails and that
   the caller cannot substitute a hostname.
2. Validate the API/Application Key pair through `/api/v2/validate_keys`.
   Confirm the stored profile has empty `AccountID`, the documented fallback
   display name, and `ScopesKnown=false`.
3. With an Application Key granted `monitors_read`, call `list_monitors`
   using explicit pagination and `get_monitor` against a disposable monitor.
4. With `metrics_read`, call `list_active_metrics` and
   `get_metric_metadata` for a known non-sensitive metric.
5. With `timeseries_query`, call `query_timeseries_points` for a small bounded
   window. Also verify a valid empty-result query.
6. Remove one permission and confirm the affected Tool returns
   `permission_denied` without marking the credential invalid. Test one
   missing monitor and a bounded `429` if it can be induced safely.
7. Confirm all five results pass their Definition output schemas. Confirm
   audit/health/observer output contains stable operation and status metadata
   but no keys, queries, monitor content, metric data, or Provider error body.
8. Revoke the keys and delete disposable monitors or metrics. Record site,
   account class, key type and permission names (never values), date, result,
   and cleanup outcome.

## Smoke records

| Date | Kind | Account/auth class | Environment | API/server version | Coverage | Cleanup status | Result |
|---|---|---|---|---|---|---|---|
| 2026-07-23 | deterministic mock | N/A | `providerkit/testkit` and local `httptest`, no public network | Datadog API `v1/v2` | nine fixed PublicOnly site policies, dual-header footprint and redaction, v2 pair validation, all five request/response schemas, forced monitor pagination, 31-day window, strict point tuples, nullable/empty normalization, redirects, private DNS, response cap, 400/401/403/404/408/429/5xx mapping | N/A — no external resources | PASS (`go test -race -count=3 ./datadog`) |
| — | real account | dedicated test organization; API Key plus least-privilege Application Key | dedicated Datadog organization and credentials required | PENDING — record site and API versions at execution | full procedure above | **PENDING — not run** | **PENDING** — no Datadog credential was available and no real-account claim has been made |

The deterministic mock record is reproducible engineering evidence. It does
not replace the real-account release gate.
