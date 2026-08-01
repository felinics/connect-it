# Getting started

This walks through a complete first run: deploy the stack, configure a
provider, create a connection, and call its tools over MCP.

## 1. Deploy

Requires Docker and Docker Compose.

```bash
git clone --recursive https://github.com/memohai/connect-it.git
cd connect-it
cp .env.example .env
```

`--recursive` matters: `packages/ui` is a submodule and the combined image will
not build without it. If you already cloned without it, run
`git submodule update --init --recursive`.

Generate the three secrets and put them in `.env`:

```bash
echo "1:$(openssl rand -hex 32)"   # CONNECT_IT_SECRET_KEY
openssl rand -hex 32               # COOKIE_SECRET
openssl rand -hex 16               # POSTGRES_PASSWORD
```

`CONNECT_IT_SECRET_KEY` encrypts every stored credential. Back it up. If you
lose it, every connection has to be authorized again.

Two more values are worth a look before you start:

- `CONNECT_IT_ADMIN_PASSWORD` seeds the `admin` account on the very first boot
  only. Changing it later in `.env` does nothing; use the admin UI instead.
- `CONNECT_IT_BASE_URL` is what OAuth callback URLs are built from. It must be
  the address providers can actually reach. Behind a domain or reverse proxy,
  set the external URL, for example `https://connect.example.com`.

Start the stack:

```bash
docker compose up -d --build
```

The Go service serves the admin UI, `/v1`, and `/mcp` from the same container
and port. Postgres lives in a named volume, so `docker compose down` keeps your
data; `docker compose down -v` deletes it.

Check it is alive:

```bash
curl http://localhost:8421/healthz
# {"status":"ok"}
```

## 2. Configure a connector

Open <http://localhost:8421> and sign in as `admin`.

Under **Connectors** you will find every provider compiled into the build, each
with a status:

| Status | Meaning |
|---|---|
| `needs_config` | The connector needs administrator config before it can be used |
| `ready` | Configured and usable |
| `config_incompatible` | The stored config is newer than the running code |
| `deprecated` | Still usable, but scheduled for removal |
| `definition_missing` | A config row exists for a connector this build does not know |

Most providers need an OAuth app. Create one in the provider's developer
console, set its redirect URL to `<CONNECT_IT_BASE_URL>/v1/oauth/callback`, and
paste the client ID and secret into the connector's config form. Secrets are
write-only: once saved, the UI shows only which keys are set, never the values.

Connectors backed by a remote MCP server with native OAuth support need no
client ID or secret at all — they register dynamically. Those are `ready` from
the start.

## 3. Create an API token

Under **API Tokens**, create one. The plaintext is shown exactly once.

This token is a deployment-level credential: it can manage every connection in
the instance and issue MCP sessions. Keep it on a trusted server, in a secret
store or environment variable. Never send it to a browser.

Everything under `/v1` authenticates with it:

```bash
export BASE=http://localhost:8421
export API_TOKEN=cit_...

curl -s "$BASE/v1/connectors" -H "Authorization: Bearer $API_TOKEN"
```

## 4. Create a connection

A connection is a long-lived credential handle. connect-it does not record who
owns it — your application stores the returned `connection_id` against whatever
user, workspace or bot it belongs to.

### OAuth providers

Start the authorization. The response comes back immediately, before the user
has done anything:

```bash
curl -s -X POST "$BASE/v1/connections/oauth" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"connector_type":"github","auth_method":"oauth","alias":"acme-github"}'
```

```json
{
  "connection_id": "0f9c…",
  "authorization_url": "https://github.com/login/oauth/authorize?…"
}
```

Store `connection_id` now — it is durable and will not change. Send the end
user to `authorization_url`. When they finish, connect-it renders its own
completion page; there is no second redirect back into your application.

Poll for the outcome:

```bash
curl -s "$BASE/v1/connections/0f9c…" -H "Authorization: Bearer $API_TOKEN"
```

```json
{
  "id": "0f9c…",
  "connector_type": "github",
  "auth_method": "oauth",
  "alias": "acme-github",
  "status": "active",
  "created_at": "2026-07-29T12:00:00Z"
}
```

Connection status values:

| Status | Meaning |
|---|---|
| `pending` | Authorization started, the user has not finished |
| `active` | Usable |
| `reauth_required` | The refresh token was rejected; the user must authorize again |
| `authorization_failed` | The user denied access or the flow was abandoned |

`alias` is an optional display label for the admin UI. It is not unique and
carries no meaning for connect-it.

### API-key providers

Some providers use a personal access token instead. Ask the connector which
fields it wants:

```bash
curl -s "$BASE/v1/connectors/linear" -H "Authorization: Bearer $API_TOKEN"
```

Then submit them. The connection is `active` right away:

```bash
curl -s -X POST "$BASE/v1/connections/api-key" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
        "connector_type": "linear",
        "auth_method": "api_key",
        "alias": "acme-linear",
        "fields": {"api_key": "lin_api_…"}
      }'
```

### Re-authorizing

When a connection turns `reauth_required`, mint a fresh authorization URL for
the same ID. The connection ID never changes, so nothing in your database needs
updating:

```bash
curl -s -X POST "$BASE/v1/connections/0f9c…/reauth" \
  -H "Authorization: Bearer $API_TOKEN"
```

Operators can do the same from the admin UI and hand the link to the right user
manually.

## 5. Issue an MCP session

A session bundles several connections into one endpoint. You choose the
namespace each connection appears under:

```bash
curl -s -X POST "$BASE/v1/mcp-sessions" \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
        "connections": {"github": "0f9c…", "linear": "7a21…"},
        "ttl_seconds": 3600
      }'
```

```json
{
  "token": "4f1c9ae2…",
  "expires_at": "2026-07-29T13:00:00Z"
}
```

The session token is 64 hex characters and is stored only as a hash, so this
response is the one and only time you see it.

Tools are exposed as `namespace__tool_name`, so the same provider can appear
twice under different namespaces without collisions.

The tool list is discovered and frozen when the session is issued. It will not
change for the session's lifetime, even if an upstream server adds or removes
tools meanwhile. Issuing is all-or-nothing: if discovery fails for any single
connection, the whole request fails rather than returning a partial session.

To restrict what the session can reach, pass `tool_allowlist` with fully
namespaced names:

```json
{
  "connections": {"github": "0f9c…"},
  "tool_allowlist": ["github__search_issues", "github__get_file_contents"],
  "ttl_seconds": 3600
}
```

Omitting it allows every tool discovered at issue time. When present, every
name in the list must exist or the request is rejected — a typo fails loudly
instead of silently granting less than you meant.

A session dies when its TTL expires, when one of its connections is deleted, or
when the API token that issued it is revoked.

## 6. Connect an MCP client

Point any MCP client at `/mcp` over Streamable HTTP, with the session token as
a bearer credential:

```text
POST http://localhost:8421/mcp
Authorization: Bearer 4f1c9ae2…
```

Go services should use [the Go SDK](go-sdk.md) instead of doing this by hand.
It keeps the short-lived session token in memory, refreshes it before it
expires, and re-issues once after a `401` or `403`.

## 7. Day-to-day operations

- **Watch connection health.** The admin UI's overview lists connections in an
  abnormal state. `needs_config` connectors are deliberately excluded — an
  unconfigured connector is a normal resting state, not a problem.
- **Revoke access.** Deleting a connection removes its credential and kills
  every session bound to it. Revoking an API token kills every session it
  issued, immediately.
- **Rotate the encryption key.** `CONNECT_IT_SECRET_KEY` accepts several
  versions, as in `1:<hex>,2:<hex>`. Writes use the highest version and reads
  use the version stored with each ciphertext, so you can add a key and let
  credentials migrate as they are rewritten, without downtime.
- **Audit.** `tool_runs` records call attribution, duration, error class and
  upstream status code. Arguments, results and raw error text are deliberately
  never stored.

## Next

- [Architecture](architecture.md) — the connector model and why it is shaped this way
- [Go SDK](go-sdk.md) — for trusted downstream services
- REST API reference — `/swagger/index.html` on your running instance
