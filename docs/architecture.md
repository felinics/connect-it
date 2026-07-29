# Architecture

connect-it is a self-hosted connector gateway. It stores third-party
credentials centrally, manages connections through a REST API, and exposes
tools to trusted downstream services through one aggregated MCP Streamable HTTP
endpoint.

## Connector model

Every connector Definition picks exactly one implementation:

- **`remote_mcp`** — connect to the upstream MCP server named in the code.
  Tools are discovered dynamically through `tools/list` when a session is
  issued, and `tools/call` forwards arguments and results verbatim.
- **`managed`** — when a platform has no usable remote MCP server, connect-it
  defines and implements the tools itself against a REST API or SDK.

The two are never mixed inside one connector. Remote MCP tools are defined by
the upstream server; managed tools are defined by connect-it.

### Why the split

An earlier model declared the remote MCP server, a static tool schema, backend
mappings and argument transformers all in the same Definition. connect-it then
had to restate what the upstream already published through `tools/list`, and
the two descriptions could drift apart whenever a tool changed. Transformers
that were never implemented added dead code on top of that.

Responsibility is now divided by who defines and maintains a tool:

- When the upstream offers a usable MCP server, connect-it does not copy tool
  definitions. It only injects credentials, discovers tools dynamically, and
  passes the protocol through.
- When the upstream offers no usable MCP server, connect-it owns the tool
  definitions and implements them against a REST API or SDK.

A hybrid mode is deliberately excluded. Letting one connector carry both
upstream-discovered tools and locally added ones brings back name collisions,
inconsistent schemas, permissions that are hard to reason about, and versions
that drift. When new capability is needed, either the upstream MCP server adds
a tool or the connector is implemented as managed — the proxy layer does not
grow a second tool set of its own.

Some managed connectors expose a generic `api_request` tool over the official
REST API. Enabling such a tool grants the downstream service the full reach of
that credential against that REST API; `tool_allowlist` can only allow or deny
a whole tool, not restrict it further by HTTP method or path.

What this choice reduces is the number of concepts to understand and maintain,
not necessarily the code a first change touches. The registry and the execution
layer handle exactly two implementations, and adding a provider never requires
a new backend or transformer type.

## OAuth modes

OAuth comes in two flavours:

- **Provider OAuth** — uses a pre-configured OAuth app. This covers managed
  providers and any upstream that does not support native MCP OAuth.
- **Native MCP OAuth** — discovers OAuth metadata from the remote MCP endpoint
  and uses PKCE with dynamic client registration. Only available when the
  upstream supports it, and then no administrator-provided client ID or secret
  is required.

## Connections and MCP sessions

A connection is a long-lived credential handle. Third-party access tokens,
refresh tokens and API keys live only inside connect-it. Downstream services
persist nothing but the `connection_id`, and decide for themselves which user
or bot it belongs to.

An MCP session is a short-lived, immutable tool list that can span several
connections:

1. The downstream service submits a `namespace → connection_id` map.
2. connect-it discovers the tools of every connection concurrently.
3. Tools are exposed as `namespace__tool_name` and frozen into the session.
4. The downstream service uses the short-lived session token against the
   unified `/mcp` endpoint.

Freezing the list guarantees that `tools/list` and `tools/call` refer to the
same set of tools for the lifetime of a session. Tools added or removed
upstream during the session do not change an already-issued one. Sessions are
issued as a complete snapshot: if tool discovery fails for any single
connection, the whole issue fails rather than producing a partial session.

Omitting `tool_allowlist` allows every tool discovered at issue time. When an
explicit allowlist is given, every name in it must exist or the session is
refused. Revoking the API token that issued a session, deleting one of its
connections, or letting the session expire all invalidate it.

## Environment variables

| Variable | Required | Description |
|---|---|---|
| `DATABASE_URL` | yes | PostgreSQL connection string |
| `CONNECT_IT_SECRET_KEY` | yes | AES-256-GCM keyring, e.g. `1:<64-char hex>` |
| `COOKIE_SECRET` | yes | HMAC secret for the admin session cookie |
| `CONNECT_IT_BASE_URL` | yes | Public address used to build OAuth callbacks |
| `CONNECT_IT_ADMIN_PASSWORD` | first start | Seeds the `admin` account |
| `LISTEN_ADDR` | no | Defaults to `:8080` |
| `TEST_DATABASE_URL` | tests | Database for Go integration tests; those tests skip when unset |

The keyring accepts several versions, as in `1:<hex>,2:<hex>`. Writes use the
highest version and reads use the version stored with the ciphertext, which
allows rotation without downtime.

## Security boundaries

- An API token is a deployment-level credential. It can manage every connection
  in the instance and issue sessions; it is not a per-connection user
  credential. Keep it on a trusted server and never send it to a browser.
- Remote MCP endpoints are fixed in the Definitions compiled into the binary,
  and HTTPS is enforced.
- Outbound credentialed requests never follow cross-origin redirects.
- `tool_runs` records only call attribution, duration, error class and upstream
  status code. It never stores arguments, results or raw error text.
- Registering a remote MCP connector means trusting the tool schemas and
  descriptions that upstream returns.

## Next

- [Getting started](getting-started.md) — the same concepts applied end to end
- [Development](development.md) — repository layout and workflow
