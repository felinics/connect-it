# Gmail Provider

Connector type: `gmail`

## Source and protocol

- Official Gmail REST API v1:
  <https://developers.google.com/workspace/gmail/api/reference/rest>
- Gmail MCP server (Developer Preview):
  <https://developers.google.com/workspace/gmail/api/reference/mcp>
- Credential validation endpoint:
  <https://developers.google.com/workspace/gmail/api/reference/rest/v1/users/getProfile>
- Reviewed implementation reference:
  `oomol-lab/open-connector` commit
  `dbc50f5b46e978a9b825f92a1f5aed585f8408d4`, Apache-2.0, under
  `src/providers/gmail/`.
- Audited repository and exact paths:
  <https://github.com/oomol-lab/open-connector>,
  `src/providers/gmail/actions.ts`,
  `src/providers/gmail/definition.ts`,
  `src/providers/gmail/executors.ts`,
  `src/providers/gmail/message.ts`, and
  `src/providers/gmail/scopes.ts`.
- API version and source review date: `v1`, 2026-07-23.

Gmail is a **hybrid** Provider: two Tools are Remote MCP calls to Google's
official Gmail MCP server, two are Managed REST handlers in this repository.
The Definition is the source of truth for schemas, scopes, risk and backend
selection, and it pins both origins:

- Remote half — endpoint fixed to `https://gmailmcp.googleapis.com/mcp/v1`
  with `gmailmcp.googleapis.com` as the only reviewed hostname; the OAuth
  access token is presented as `Authorization: Bearer`.
- Managed half — a fixed `https://gmail.googleapis.com/` PublicOnly
  providerkit policy.

Neither Connector configuration nor Tool arguments can change either origin.

## Authentication and configuration

Create a Google Cloud OAuth client and configure:

- `client_id`
- `client_secret` (secret)
- `project_id`

The OAuth callback is the deployment's documented connect-it callback URL.
Enable the Gmail API for the project and register that exact callback in the
Google Cloud console. connect-it requests offline access and consent so a
refresh token can be issued.

Requested scopes:

- `https://www.googleapis.com/auth/gmail.readonly`
- `https://www.googleapis.com/auth/gmail.compose`

The managed runtime accepts only a normalized Bearer access token. An explicit
Gmail HTTP 401 marks that credential invalid; HTTP 403 remains a
state-neutral `permission_denied`.

## Tools

| Tool ID | Backend | Risk | Purpose |
|---|---|---:|---|
| `search_threads` | official Remote MCP | read | Search Gmail threads |
| `create_draft` | official Remote MCP | write | Create, but do not send, a draft |
| `list_messages` | managed REST | read | List messages with an optional Gmail query |
| `send_message` | managed REST | destructive | Send one UTF-8 plain-text RFC 2822 message |

Tool IDs and successful structured JSON are preserved from the pre-providerkit
implementation. Managed success bodies are validated as JSON objects and
returned byte-for-byte; upstream error bodies are never returned or persisted
as a Tool failure.

Only the two managed Tools declare an `OutputSchema`. The two Remote MCP Tools
do not, so a change in the preview server's output shape reaches the Agent
unvalidated — that is the current state, not an engine limit, since Remote and
Managed results are validated by the same `finish()` in
`packages/service/exec/engine.go`.

## Runtime limits and known constraints

- Managed request budget: 30 seconds, including DNS, dial, TLS, retry and
  bounded response reading.
- Managed decoded response limit: 10 MiB.
- Remote MCP call budget: 30 seconds, including handshake, DNS, dial, TLS,
  redirects and bounded response reads; decoded responses are capped at 10 MiB
  and at most 32 Remote MCP sessions run concurrently per process.
- Redirects from managed REST endpoints are denied.
- `list_messages.max_results` is 1–100 and defaults to 20.
- `send_message` sends plain text only. It does not expose attachments, HTML
  composition or arbitrary RFC 2822 headers.
- `send_message.to` and `send_message.subject` reject CR/LF before HTTP to
  prevent RFC 2822 header injection; body text may contain normal line breaks.
- Gmail's profile endpoint proves account access but cannot report the complete
  OAuth grant, so credential validation records scopes as unknown.
- The official Gmail MCP server is a Developer Preview and can change
  independently of the managed REST tools.

## Upgrade notes

The providerkit migration does not change connector type, Tool IDs, tool
schemas, OAuth fields or `ConfigSchemaVersion` (`1`). Existing Gmail
configuration needs no data migration. The observable failure shape is now the
platform `ToolFailure` contract instead of a Provider-body-bearing legacy
`IsError` result. Re-review this document's Source section before an API
version, scope, MCP preview contract or config schema change.

## Real-account smoke procedure

`real_smoke_test.go` is default-skipped unless its complete, write-capable
environment is present. Use one dedicated, non-production test mailbox as both
the authenticated account and recipient. The runtime token must carry the
Connector's reviewed `gmail.readonly` and `gmail.compose` scopes. A separate
test-only janitor token for the same mailbox must carry
`https://mail.google.com/`; the production Connector grant intentionally cannot
delete sent messages. The runtime and janitor tokens must be distinct.

Securely inject these four variables into the test process without placing
credential values in shell history:

- `CONNECT_IT_GMAIL_SMOKE_ACCESS_TOKEN`;
- `CONNECT_IT_GMAIL_SMOKE_CLEANUP_ACCESS_TOKEN`;
- `CONNECT_IT_GMAIL_SMOKE_MAILBOX`; and
- `CONNECT_IT_GMAIL_SMOKE_ALLOW_WRITES=true`.

After the implementation is committed, run the fail-closed release gate from
the repository root. The SHA must be the full current `HEAD`, the repository
must be clean, and the receipt must be an absolute path that does not exist:

```sh
bash scripts/run-provider-real-smoke.sh \
  gmail \
  '<FULL_APPROVED_HEAD_SHA>' \
  '/protected/provider-smoke/gmail.tsv'
```

The gate runs only `TestGmailRealSmoke`, rejects the default no-environment
skip, withholds raw Go JSON/stderr, and publishes a mode-`0600` no-clobber
receipt only after the exact test and package explicitly pass. Its
`cleanup_mode` identifies the automatic guarded deletion path but does not
claim that draft/message deletion or authorization revocation completed; the
smoke table records cleanup separately.

The harness fails closed on a partial environment and accepts the write gate
only when its value is exactly `true`. It validates both credentials against
the configured mailbox, proves a known-invalid OAuth token is rejected as
unauthorized, validates every invocation against the compiled local input
schema, and covers all four first-release Tools, calling `search_threads` and
`create_draft` through their reviewed remote mappings. It does not call
`tools/list`; reconciling those mappings against the live list is `mcp:verify`
/ `mise run mcp-probe` and must be run separately. It
creates a uniquely UTC-and-random-tagged draft and a self-addressed message.
Cleanup is registered before each write. Even if a write returns an uncertain
timeout/protocol failure, cleanup performs bounded, explicit lookup retries:
the draft is located by exact generated Subject plus sole recipient and
deleted, while the returned sent-message ID (or the same exact fallback
identity) is re-read and verified before permanent deletion. A cleanup failure
reports only the safe resource ID and stable failure code/status and fails the
test.

After execution, confirm the dedicated mailbox has no matching smoke draft or
message, the cleanup status is PASS, and audit output contains only stable
operation/status metadata. Never record tokens, mailbox contents, generated
Subjects or bodies, MCP/REST response payloads, or Provider error text. Revoke
both test authorizations when the release check is complete.

## Smoke records

| Date | Kind | Account/auth class | Environment | API/server version | Coverage | Cleanup status | Result |
|---|---|---|---|---|---|---|---|
| 2026-07-23 | deterministic mock | N/A | `providerkit/testkit`, local `httptest`, no public network | Gmail REST `v1`; MCP Developer Preview reviewed 2026-07-23 | managed paths/query/auth, RFC 2822 envelope and CR/LF preflight, byte-identical success JSON, bounded/invalid responses, status mapping, 401 marker, 403 neutrality and redaction | N/A — no external resources | PASS (`go test ./gmail -count=1`) |
| 2026-07-24 | deterministic smoke-harness test | N/A | `providerkit/testkit` and local `httptest`, no public network | Gmail REST `v1`; MCP Developer Preview reviewed 2026-07-23 | default-skip/config gate; cleanup registered before mutation; uncertain draft/message response lookup; exact identity verification; retry exhaustion and redaction; `send_message` destructive-risk contract | N/A — no external resources; waiter is injected and does not sleep | PASS (`go test -race -count=1 -skip 'RealSmoke$' ./gmail`) |
| — | real account | dedicated non-production mailbox; Google OAuth runtime token (`gmail.readonly` + `gmail.compose`) and distinct same-mailbox janitor token (`mail.google.com`) | dedicated mailbox and non-production Cloud project required | PENDING — record Gmail REST `v1` and official MCP server version/review date at execution | validator and negative-auth probe; compiled input schemas; all four Tools through their reviewed backends; exact write gate; redaction; `tools/list` reconciliation via a separate `mcp:verify` run | PENDING — record draft and sent-message deletion separately; not run | PENDING; no real-account claim has been made |

The deterministic mock record is reproducible unit evidence, not a substitute
for the release-gated real-account smoke.
