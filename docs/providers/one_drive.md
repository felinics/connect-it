# OneDrive Provider

Connector type: `one_drive`

## Why Managed REST rather than Remote MCP

Reviewed 2026-07-24; see `docs/provider-mcp-decision-table.md` §3.10 and §5.1,
and `blueprint.md` §8. Microsoft **does** ship a first-party OneDrive MCP
server — Work IQ's `mcp_OneDriveRemoteServer` — so the reason is not "there is
no official MCP". The three real blockers are:

1. it is preview, and Microsoft states these servers "aren't meant for
   production use"; tool names and parameters may change;
2. every file read and write is hard-capped at 5 MB, which is a capability
   ceiling for a file connector; and
3. it requires an M365 Copilot licence, a Microsoft Entra application
   registration and per-server administrator authorization.

Migration would also save close to nothing: this connector is roughly 450
non-test lines. Any one of the three blockers lifting — especially GA and the
5 MB cap — is the trigger to re-evaluate. The reason is recorded as the real
one instead of a false premise precisely so that re-check has a trigger.

## Source and protocol

- Official Microsoft Graph list children:
  <https://learn.microsoft.com/en-us/graph/api/driveitem-list-children?view=graph-rest-1.0>
- Microsoft Graph simple upload:
  <https://learn.microsoft.com/en-us/graph/api/driveitem-put-content?view=graph-rest-1.0>
- Credential validation endpoint:
  <https://learn.microsoft.com/en-us/graph/api/user-get?view=graph-rest-1.0>
- Reviewed implementation reference:
  `oomol-lab/open-connector` commit
  `dbc50f5b46e978a9b825f92a1f5aed585f8408d4`, Apache-2.0, under
  `src/providers/one_drive/`.
- Audited repository and exact paths:
  <https://github.com/oomol-lab/open-connector>,
  `src/providers/one_drive/actions.ts`,
  `src/providers/one_drive/definition.ts`,
  `src/providers/one_drive/executors.ts`, and
  `src/providers/one_drive/scopes.ts`.
- API version and source review date: Microsoft Graph `v1.0`, 2026-07-23.

The Definition is the source of truth for schemas, scopes and risk. Both
managed handlers use a fixed `https://graph.microsoft.com/` PublicOnly
providerkit policy; Tool arguments can select only escaped drive-relative path
segments and cannot change the origin.

## Authentication and configuration

Register a Microsoft Entra OAuth application and configure:

- `client_id`
- `client_secret` (secret)
- `tenant` (`common` by default; `organizations`, `consumers` or a specific
  tenant ID are also supported by the OAuth endpoint template)

Register the deployment's documented connect-it OAuth callback exactly. The
authorization uses PKCE and requests:

- `offline_access`
- `User.Read`
- `Files.ReadWrite`

The managed runtime accepts only a normalized Bearer access token. An explicit
Microsoft Graph HTTP 401 marks that credential invalid; HTTP 403 remains a
state-neutral `permission_denied`.

## Tools

| Tool ID | Risk | Purpose |
|---|---:|---|
| `list_drive_items` | read | List the root or one drive-relative folder |
| `upload_file` | write | Simple-upload one UTF-8 text file |

Tool IDs, input/output schemas and successful structured JSON are preserved
from the pre-providerkit implementation. Managed success bodies are validated
as JSON objects and returned byte-for-byte; upstream error bodies are never
returned or persisted as a Tool failure.

## Runtime limits and known constraints

- Managed request budget: 30 seconds, including DNS, dial, TLS, retry and
  bounded response reading.
- Managed decoded response limit: 10 MiB.
- Redirects from Microsoft Graph managed endpoints are denied.
- `upload_file.content` is measured after the Tool JSON envelope is decoded.
  It must be valid UTF-8 and at most exactly 4 MiB (4,194,304 bytes).
- `upload_file` temporarily has a 32 MiB Tool-envelope ceiling so JSON escaping
  cannot reject otherwise valid 4 MiB text before the handler applies the
  decoded byte limit. A 4 MiB + 1 byte value is rejected before HTTP.
- This release supports only in-memory simple text upload. It does not expose
  upload sessions, binary input, downloads, delete or conflict-behavior
  selection.
- Microsoft Graph opaque access tokens have no supported complete scope
  introspection endpoint, so credential validation records scopes as unknown.

## Upgrade notes

The providerkit migration does not change connector type, Tool IDs, tool
schemas, OAuth fields or `ConfigSchemaVersion` (`1`). Existing OneDrive
configuration needs no data migration. The observable failure shape is now the
platform `ToolFailure` contract instead of a Provider-body-bearing legacy
`IsError` result. The 32 MiB envelope override remains temporary and must be
removed when file transfer moves to the bounded transit-file path. Re-review
this document's Source section before a Microsoft Graph version, scope
or config schema change.

## Real-account smoke procedure

`TestOneDriveRealSmoke` is skipped when none of these variables is configured:

- `CONNECT_IT_ONEDRIVE_SMOKE_ACCESS_TOKEN`;
- `CONNECT_IT_ONEDRIVE_SMOKE_FOLDER`; and
- `CONNECT_IT_ONEDRIVE_SMOKE_ALLOW_WRITES=true`.

Once any variable is present, all three are required and the write gate must
be exactly `true`. The folder must be a dedicated, existing drive-relative
smoke folder without `.` or `..` segments. Use a short-lived token for a
dedicated Microsoft test account or tenant and a non-production drive. Export
the variables without placing the token in shell history. After the
implementation is committed, run the fail-closed release gate from the
repository root. The SHA must be the full current `HEAD`, the repository must
be clean, and the receipt must be an absolute path that does not exist:

```sh
bash scripts/run-provider-real-smoke.sh \
  one_drive \
  '<FULL_APPROVED_HEAD_SHA>' \
  '/protected/provider-smoke/one_drive.tsv'
```

The gate runs only `TestOneDriveRealSmoke`, rejects the default no-environment
skip, withholds raw Go JSON/stderr, and publishes a mode-`0600` no-clobber
receipt only after the exact test and package explicitly pass. Its
`cleanup_mode` identifies the automatic guarded-delete path but does not claim
cleanup completed; the smoke table still requires a separately confirmed
cleanup `PASS`.

The harness validates the OAuth identity and unknown-scope snapshot, lists the
dedicated folder, then uploads one uniquely named non-sensitive UTF-8 file
through both compiled Tool schemas. Before upload it registers a fixed
test-only guarded Graph DELETE restricted to the generated smoke filename; it
uses the returned item ID when available and the exact path as a fail-safe.
Cleanup failure reports only the disposable filename and stable failure
metadata. The harness never prints the token, account profile, drive listing,
file content, response body or web URL.

For the complete release check:

1. Configure the three fields above and complete OAuth.
2. Create a Session that explicitly grants both tools.
3. Run the harness and separately call `list_drive_items` for the root if that
   broader listing is part of the intended release evidence.
4. Verify the returned ID, name and size without recording the listing or
   uploaded content.
5. Confirm audit output contains stable operation/status metadata but no
   access token, uploaded content or upstream error body.
6. Confirm automatic deletion succeeded (or delete the item manually), then
   revoke the test authorization.

For a release boundary check, also run controlled 4 MiB and 4 MiB + 1 byte
uploads against a mock endpoint; do not create oversized real-account data
merely to prove local preflight validation.

Record account type/tenant class, API version, date, tool results and cleanup
outcome below; never record credentials or uploaded content.

## Smoke records

| Date | Kind | Account/auth class | Environment | API/server version | Coverage | Cleanup status | Result |
|---|---|---|---|---|---|---|---|
| 2026-07-23 | deterministic mock | N/A | `providerkit/testkit`, local `httptest`, no public network | Microsoft Graph `v1.0` | escaped Graph paths, raw upload body, byte-identical success JSON, exact ASCII/multibyte 4 MiB boundaries, 4 MiB + 1 preflight rejection, escaped-envelope regression, bounded/invalid responses, 401 marker, 403 neutrality and redaction | N/A — no external resources | PASS (`go test ./onedrive -count=1`) |
| — | real account | dedicated personal or work/school test account; Entra OAuth | dedicated account/tenant, existing smoke folder and non-production drive required | PENDING — record Graph API version at execution | validator, both Tool calls, negative authorization checks and audit redaction | PENDING — not run; harness deletes the unique file | PENDING; no real-account claim has been made |

The deterministic mock record is reproducible unit evidence, not a substitute
for the release-gated real-account smoke.
