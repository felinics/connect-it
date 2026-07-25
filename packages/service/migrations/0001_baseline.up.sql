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
  alias text,
  auth_method text not null,
  credential bytea not null,
  secret_key_version integer not null,
  profile jsonb not null default '{}',
  scopes text[] not null default '{}',
  status text not null,
  access_token_expires_at timestamptz,
  created_at timestamptz not null,
  updated_at timestamptz not null,
  authorization_generation bigint not null default 1,
  scopes_known boolean not null default false,
  credential_version bigint not null default 1,
  authorization_attempt_version bigint not null default 0,
  refresh_owner uuid,
  refresh_lease_until timestamptz,
  refresh_state text,
  constraint connections_credential_version_positive
    check (credential_version >= 1),
  constraint connections_authorization_generation_positive
    check (authorization_generation >= 1),
  constraint connections_authorization_attempt_version_nonnegative
    check (authorization_attempt_version >= 0),
  constraint connections_refresh_lease_consistent
    check (
      case
        when refresh_state is null then
          refresh_owner is null and refresh_lease_until is null
        else
          refresh_state in ('leased', 'requesting')
          and refresh_owner is not null
          and refresh_lease_until is not null
      end
    )
);

create table oauth_authorizations (
  id uuid primary key,
  connector_type text not null,
  state_hash text not null unique,
  context_ciphertext bytea not null,
  connection_id uuid not null
    references connections(id) on delete cascade,
  status text not null,
  expires_at timestamptz not null,
  created_at timestamptz not null,
  auth_method text not null,
  alias text not null,
  secret_key_version integer not null,
  redirect_url text not null,
  attempt_version bigint not null,
  expected_authorization_generation bigint not null,
  flow_kind text not null,
  requested_scopes text[] not null,
  context_version smallint not null default 1,
  constraint oauth_authorizations_attempt_version_positive
    check (attempt_version >= 1),
  constraint oauth_authorizations_expected_generation_positive
    check (expected_authorization_generation >= 1),
  constraint oauth_authorizations_flow_kind_valid
    check (flow_kind in ('initial', 'reauth')),
  constraint oauth_authorizations_context_version_valid
    check (context_version = 1)
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
  authorization_generation bigint not null,
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
  error_code text,
  upstream_status integer,
  input jsonb,
  output_summary text,
  duration_ms integer,
  created_at timestamptz not null,
  constraint tool_runs_error_code_shape
    check (
      error_code is null
      or (
        octet_length(error_code) between 1 and 64
        and error_code ~ '^[a-z][a-z0-9_]*$'
      )
    ),
  constraint tool_runs_upstream_status_valid
    check (
      upstream_status is null
      or upstream_status between 100 and 599
    )
);

create table connector_policy_identities (
  connector_type text primary key,
  identity_version integer not null,
  identity_digest bytea not null,
  definition_digest bytea not null,
  initialized boolean not null default false,
  created_at timestamptz not null default now(),
  updated_at timestamptz not null default now(),
  constraint connector_policy_identities_version_positive
    check (identity_version >= 1),
  constraint connector_policy_identities_digest_shape
    check (
      (
        not initialized
        and octet_length(identity_digest) = 0
        and octet_length(definition_digest) = 0
      )
      or
      (
        initialized
        and octet_length(identity_digest) = 32
        and octet_length(definition_digest) = 32
      )
    )
);

create table schema_migrations (
  version bigint primary key,
  dirty boolean not null
);

insert into schema_migrations (version, dirty) values (1, false);
