create table admin_account (
  id integer primary key check (id = 1),
  username text not null,
  password_hash text not null,
  updated_at timestamptz not null
);

create table api_tokens (
  id uuid primary key,
  name text not null,
  token_hash text not null unique,
  created_at timestamptz not null,
  revoked_at timestamptz
);

create table connector_configs (
  connector_type text primary key,
  config_schema_version integer not null,
  public_config jsonb not null default '{}',
  secret_config bytea not null,
  secret_key_version integer not null,
  created_at timestamptz not null,
  updated_at timestamptz not null
);

create table connector_settings (
  connector_type text primary key,
  enabled boolean not null default true,
  updated_at timestamptz not null
);

create table oauth_clients (
  id uuid primary key,
  connector_type text not null,
  resource text not null,
  issuer text not null,
  redirect_uri text not null,
  client_id text not null,
  client_secret bytea not null,
  secret_key_version integer not null,
  token_endpoint text not null,
  token_endpoint_auth_method text not null,
  client_secret_expires_at timestamptz,
  created_at timestamptz not null,
  updated_at timestamptz not null,
  unique (connector_type, resource, issuer, redirect_uri)
);

create table connections (
  id uuid primary key,
  connector_type text not null,
  alias text,
  auth_method text not null,
  credential bytea not null,
  secret_key_version integer not null,
  profile jsonb not null default '{}',
  scopes text[] not null default '{}',
  status text not null,
  access_token_expires_at timestamptz,
  oauth_client_id uuid references oauth_clients(id),
  created_at timestamptz not null,
  updated_at timestamptz not null
);

create table oauth_authorizations (
  id uuid primary key,
  connector_type text not null,
  state_hash text not null unique,
  pkce_verifier bytea not null,
  secret_key_version integer not null,
  auth_method text not null,
  connection_id uuid not null references connections(id) on delete cascade,
  oauth_client_id uuid references oauth_clients(id),
  status text not null,
  expires_at timestamptz not null,
  created_at timestamptz not null
);

create table mcp_sessions (
  id uuid primary key,
  token_hash text not null unique,
  api_token_id uuid not null references api_tokens(id) on delete cascade,
  tool_snapshot jsonb not null,
  expires_at timestamptz not null,
  created_at timestamptz not null
);
create index mcp_sessions_expires_at_idx on mcp_sessions (expires_at);
create index mcp_sessions_api_token_id_idx on mcp_sessions (api_token_id);

create table mcp_session_connections (
  session_id uuid not null references mcp_sessions(id) on delete cascade,
  alias text not null,
  connection_id uuid not null references connections(id) on delete cascade,
  primary key (session_id, alias),
  unique (session_id, connection_id)
);
create index mcp_session_connections_connection_id_idx
  on mcp_session_connections (connection_id);

create table tool_runs (
  id uuid primary key,
  connector_type text not null,
  connection_id uuid,
  tool_id text not null,
  session_id uuid,
  api_token_id uuid,
  status text not null,
  error_kind text,
  upstream_status integer,
  duration_ms integer,
  created_at timestamptz not null
);
