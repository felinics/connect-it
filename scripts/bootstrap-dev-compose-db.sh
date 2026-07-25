#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

# This is only for the fixed disposable dev Compose database and the isolated
# ingress test. Production uses connect-it-db against a separately approved
# empty database.
repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
base_compose="${repository_root}/docker/docker-compose.yml"
ingress_compose="${repository_root}/docker/docker-compose.ingress-test.yml"
baseline="${repository_root}/packages/service/migrations/0001_baseline.up.sql"
configuration="${repository_root}/scripts/configure-dev-database.sql"
scope=""
project=""
identity_output=""
compose_files=()
compose_args=()

fail() {
  echo "bootstrap-dev-compose-db: $*" >&2
  exit 1
}

while (($#)); do
  case "$1" in
    --scope)
      (($# >= 2)) || fail "--scope requires dev or ci-ingress"
      [[ -z "${scope}" ]] || fail "--scope may only be provided once"
      scope="$2"
      shift 2
      ;;
    --project-name)
      (($# >= 2)) || fail "--project-name requires a value"
      [[ -z "${project}" ]] ||
        fail "--project-name may only be provided once"
      project="$2"
      shift 2
      ;;
    --file)
      (($# >= 2)) || fail "--file requires a value"
      compose_file="$2"
      [[ "${compose_file}" == /* ]] ||
        compose_file="${repository_root}/${compose_file}"
      [[ -f "${compose_file}" ]] ||
        fail "Compose file does not exist: ${compose_file}"
      compose_file="$(realpath "${compose_file}")"
      compose_files+=("${compose_file}")
      compose_args+=(--file "${compose_file}")
      shift 2
      ;;
    --write-identity-override)
      (($# >= 2)) ||
        fail "--write-identity-override requires an absolute output path"
      [[ -z "${identity_output}" ]] ||
        fail "--write-identity-override may only be provided once"
      identity_output="$2"
      shift 2
      ;;
    *)
      fail "unsupported argument: $1"
      ;;
  esac
done

[[ "${scope}" == "dev" || "${scope}" == "ci-ingress" ]] ||
  fail "--scope must explicitly be dev or ci-ingress"
if ((${#compose_files[@]} == 0)); then
  compose_files=("${base_compose}")
  compose_args=(--file "${base_compose}")
fi

if [[ "${scope}" == "dev" ]]; then
  [[ -z "${project}" ]] ||
    fail "dev scope must use the fixed project from docker-compose.yml"
  ((${#compose_files[@]} == 1)) &&
    [[ "${compose_files[0]}" == "${base_compose}" ]] ||
    fail "dev scope only accepts docker/docker-compose.yml"
  project="connect-it"
else
  [[ "${project}" =~ ^connect-it-ingress-[a-f0-9]{48}$ ]] ||
    fail "ci-ingress scope requires its generated isolated project name"
  ((${#compose_files[@]} == 2)) &&
    [[ "${compose_files[0]}" == "${base_compose}" ]] &&
    [[ "${compose_files[1]}" == "${ingress_compose}" ]] ||
    fail "ci-ingress scope requires the exact base and ingress Compose files"
fi

file_owner_uid() {
  stat -c '%u' "$1" 2>/dev/null || stat -f '%u' "$1"
}

file_mode() {
  stat -c '%a' "$1" 2>/dev/null || stat -f '%Lp' "$1"
}

if [[ -n "${identity_output}" ]]; then
  [[ "${identity_output}" == /* ]] ||
    fail "identity override output must be an absolute path"
  [[ ! -e "${identity_output}" && ! -L "${identity_output}" ]] ||
    fail "identity override output must not already exist"
  identity_parent="$(dirname "${identity_output}")"
  [[ -d "${identity_parent}" && ! -L "${identity_parent}" ]] ||
    fail "identity override parent must be a non-symlink directory"
  [[ "$(file_owner_uid "${identity_parent}")" == "$(id -u)" ]] ||
    fail "identity override parent must be owned by the current uid"
  identity_parent_mode="$(file_mode "${identity_parent}")"
  (( (8#${identity_parent_mode} & 8#077) == 0 )) ||
    fail "identity override parent must not grant group or other permissions"
fi

# Ambient Compose variables must not redirect the reviewed model.
unset COMPOSE_PROJECT_NAME COMPOSE_FILE
compose=(docker compose --project-name "${project}" "${compose_args[@]}")

[[ -f "${baseline}" && -f "${configuration}" ]] ||
  fail "fresh baseline or development database configuration is missing"
up_migrations=("${repository_root}"/packages/service/migrations/*.up.sql)
((${#up_migrations[@]} == 1)) &&
  [[ "$(realpath "${up_migrations[0]}")" == "$(realpath "${baseline}")" ]] ||
  fail "migration set must contain only the fresh-database baseline"

psql=(
  "${compose[@]}" exec -T postgres
  psql -X --no-psqlrc --set ON_ERROR_STOP=1
  --username connect_it --dbname connect_it
)

catalog_state="$(
  "${psql[@]}" --tuples-only --no-align --field-separator '|' --command "
    select count(*) filter (
             where c.relkind in ('r', 'p', 'v', 'm', 'S', 'f')
           ),
           to_regclass('public.schema_migrations') is not null
    from pg_catalog.pg_class c
    join pg_catalog.pg_namespace n on n.oid = c.relnamespace
    where n.nspname = 'public';
  "
)"
catalog_state="${catalog_state//[[:space:]]/}"

marker_state() {
  local value
  value="$(
    "${psql[@]}" --tuples-only --no-align --field-separator '|' --command "
      select count(*), coalesce(min(version), -1), coalesce(bool_or(dirty), true)
      from public.schema_migrations;
    "
  )"
  printf '%s' "${value//[[:space:]]/}"
}

schema_shape_is_v1() {
  local value
  value="$(
    "${psql[@]}" --tuples-only --no-align --command "
      /* dev_bootstrap_schema_shape */
      with expected_tables(name) as (
        values ('admin_account'), ('api_tokens'), ('connector_configs'),
          ('connections'), ('oauth_authorizations'), ('mcp_sessions'),
          ('mcp_session_connections'), ('connector_health'), ('tool_runs'),
          ('connector_policy_identities'), ('schema_migrations')
      ),
      actual_tables(name) as (
        select table_name
        from information_schema.tables
        where table_schema = 'public' and table_type = 'BASE TABLE'
      )
      select
        not exists (
          (select name from expected_tables except select name from actual_tables)
          union all
          (select name from actual_tables except select name from expected_tables)
        )
        and exists (
          select 1 from information_schema.columns
          where table_schema = 'public'
            and table_name = 'oauth_authorizations'
            and column_name = 'context_ciphertext'
            and data_type = 'bytea' and is_nullable = 'NO'
        )
        and exists (
          select 1 from information_schema.columns
          where table_schema = 'public'
            and table_name = 'schema_migrations'
            and column_name = 'dirty'
            and data_type = 'boolean' and is_nullable = 'NO'
        );
    "
  )"
  [[ "${value//[[:space:]]/}" == "t" ]]
}

if [[ "${catalog_state}" == *"|t" ]]; then
  [[ "$(marker_state)" == "1|1|f" ]] && schema_shape_is_v1 ||
    fail "database is not an exact usable schema v1 clean state"
  echo "bootstrap-dev-compose-db: schema v1 is already clean; no-op"
elif [[ "${catalog_state}" == "0|f" ]]; then
  echo "bootstrap-dev-compose-db: applying fresh dev/CI schema baseline v1"
  command cat "${baseline}" |
    "${psql[@]}" --set=connect_it_baseline=1 \
      --single-transaction --file -
  [[ "$(marker_state)" == "1|1|f" ]] && schema_shape_is_v1 ||
    fail "post-bootstrap schema is not the exact clean v1 baseline"
  echo "bootstrap-dev-compose-db: fresh schema v1 is ready"
else
  fail "database is not completely empty and has no valid schema_migrations state (found ${catalog_state})"
fi

# One reviewed fixed policy is shared by Compose, ingress CI, and mise dev.
command cat "${configuration}" |
  "${psql[@]}" --set=connect_it_dev_configuration=1 \
    --single-transaction --file -

if [[ -n "${identity_output}" ]]; then
  identity="$(
    "${psql[@]}" --tuples-only --no-align --field-separator '|' --command "
      select pg_catalog.current_database(), pg_catalog.current_schema(),
             (pg_catalog.pg_control_system()).system_identifier::text;
    "
  )"
  identity="${identity//$'\r'/}"
  [[ "${identity}" != *$'\n'* ]] ||
    fail "development database identity query returned multiple rows"
  IFS='|' read -r database schema system_identifier extra <<<"${identity}"
  [[ "${database}" == "connect_it" && "${schema}" == "public" &&
    -z "${extra}" ]] ||
    fail "development database identity is not exactly connect_it/public"
  [[ "${system_identifier}" =~ ^[1-9][0-9]{0,19}$ ]] ||
    fail "development database system identifier is malformed"
  if ((${#system_identifier} == 20)) &&
    [[ "${system_identifier}" > "18446744073709551615" ]]; then
    fail "development database system identifier exceeds uint64"
  fi

  identity_tmp="$(mktemp "${identity_parent}/.connect-it-identity.XXXXXX")" ||
    fail "could not allocate the identity override temporary file"
  chmod 0600 "${identity_tmp}" || {
    rm -f -- "${identity_tmp}"
    fail "could not protect the identity override temporary file"
  }
  {
    printf '%s\n' \
      'services:' \
      '  connect-it:' \
      '    environment:' \
      '      CONNECT_IT_EXPECTED_DATABASE_NAME: "connect_it"' \
      '      CONNECT_IT_EXPECTED_DATABASE_SCHEMA: "public"' \
      '      CONNECT_IT_EXPECTED_DATABASE_APPLICATION_ROLE: "connect_it_app"' \
      '      CONNECT_IT_EXPECTED_DATABASE_OWNER_ROLE: "connect_it_owner"'
    printf '      CONNECT_IT_EXPECTED_DATABASE_SYSTEM_IDENTIFIER: "%s"\n' \
      "${system_identifier}"
  } >"${identity_tmp}" || {
    rm -f -- "${identity_tmp}"
    fail "could not write the identity override temporary file"
  }
  ln "${identity_tmp}" "${identity_output}" || {
    rm -f -- "${identity_tmp}"
    fail "identity override output appeared concurrently"
  }
  rm -f -- "${identity_tmp}"
  [[ "$(file_mode "${identity_output}")" == "600" ]] ||
    fail "identity override output permissions are not exactly 0600"
  echo "bootstrap-dev-compose-db: protected database identity override written"
fi
