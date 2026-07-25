\set ON_ERROR_STOP on

-- Development/CI only. The caller first proves that public is either the
-- repository's fresh v1 baseline or was just initialized from it.
do $roles$
begin
  if not exists (
    select 1 from pg_catalog.pg_roles where rolname = 'connect_it_owner'
  ) then
    create role connect_it_owner nologin;
  end if;
  if not exists (
    select 1 from pg_catalog.pg_roles where rolname = 'connect_it_app'
  ) then
    create role connect_it_app login;
  end if;
end
$roles$;

alter role connect_it_owner
  with nologin noinherit nosuperuser nocreatedb nocreaterole
       noreplication nobypassrls;
alter role connect_it_app
  with login noinherit nosuperuser nocreatedb nocreaterole
       noreplication nobypassrls
       password 'connect_it_app_dev_password';
alter role connect_it_app in database connect_it set search_path to public;

alter database connect_it owner to connect_it_owner;
alter schema public owner to connect_it_owner;
alter table public.admin_account owner to connect_it_owner;
alter table public.api_tokens owner to connect_it_owner;
alter table public.connector_configs owner to connect_it_owner;
alter table public.connections owner to connect_it_owner;
alter table public.oauth_authorizations owner to connect_it_owner;
alter table public.mcp_sessions owner to connect_it_owner;
alter table public.mcp_session_connections owner to connect_it_owner;
alter table public.connector_health owner to connect_it_owner;
alter table public.tool_runs owner to connect_it_owner;
alter table public.connector_policy_identities owner to connect_it_owner;
alter table public.schema_migrations owner to connect_it_owner;

-- A login can otherwise CONNECT through PUBLIC to the stock maintenance DBs.
revoke connect on database postgres from public;
revoke connect on database template1 from public;

set role connect_it_owner;
revoke connect, temporary on database connect_it from public;
revoke usage, create on schema public from public;
revoke all privileges on database connect_it from connect_it_app;
revoke all privileges on schema public from connect_it_app;
revoke all privileges on all tables in schema public
  from public, connect_it_app;
revoke all privileges on all sequences in schema public
  from public, connect_it_app;

-- The runtime gate requires canonical owner-only defaults for functions and
-- types, whose PostgreSQL defaults otherwise grant PUBLIC access.
alter default privileges
  revoke execute on functions from public;
alter default privileges
  revoke usage on types from public;

grant connect on database connect_it to connect_it_app;
grant usage on schema public to connect_it_app;
grant select, insert, update
  on public.admin_account, public.api_tokens to connect_it_app;
grant select, insert, update, delete
  on public.connector_configs, public.connections,
     public.oauth_authorizations
  to connect_it_app;
grant select, insert
  on public.mcp_sessions, public.mcp_session_connections
  to connect_it_app;
grant select, insert, update
  on public.connector_health, public.connector_policy_identities
  to connect_it_app;
grant insert on public.tool_runs to connect_it_app;
grant select on public.schema_migrations to connect_it_app;
reset role;

-- pg_control_system() is the one routine the read-only identity gate needs.
revoke all privileges on function pg_catalog.pg_control_system()
  from public, connect_it_app, connect_it_owner;
grant execute on function pg_catalog.pg_control_system()
  to connect_it_app, connect_it_owner;
