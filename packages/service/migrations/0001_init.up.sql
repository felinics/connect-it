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
  mcp_verified_endpoint text,
  mcp_verified_at timestamptz,
  created_at timestamptz not null,
  updated_at timestamptz not null
);

create table connections (
  id uuid primary key,
  connector_type text not null,
  alias text not null unique,
  auth_method text not null,
  credential bytea not null,
  secret_key_version integer not null,
  profile jsonb not null default '{}',
  scopes text[] not null default '{}',
  status text not null,
  access_token_expires_at timestamptz,
  created_at timestamptz not null,
  updated_at timestamptz not null
);

create table oauth_authorizations (
  id uuid primary key,
  connector_type text not null,
  state_hash text not null unique,
  pkce_verifier bytea not null,
  connection_id uuid,
  status text not null,
  expires_at timestamptz not null,
  created_at timestamptz not null
);

create table mcp_sessions (
  id uuid primary key,
  token_hash text not null unique,
  tool_allowlist jsonb not null,
  status text not null,
  expires_at timestamptz not null,
  created_at timestamptz not null
);

create table mcp_session_connections (
  session_id uuid not null references mcp_sessions(id) on delete cascade,
  alias text not null,
  connection_id uuid not null,
  primary key (session_id, alias)
);

create table connector_health (
  connector_type text primary key,
  last_ok_at timestamptz,
  last_error_at timestamptz,
  consecutive_failures integer not null default 0,
  last_error text
);

create table tool_runs (
  id uuid primary key,
  connector_type text not null,
  connection_id uuid,
  tool_id text not null,
  session_id uuid,
  status text not null,
  error text,
  input jsonb,
  output_summary text,
  duration_ms integer,
  created_at timestamptz not null
);
