#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
base_compose="${repository_root}/docker/docker-compose.yml"
test_compose="${repository_root}/docker/docker-compose.ingress-test.yml"

if [[ "${CONNECT_IT_INGRESS_PROJECT+x}" == "x" ]]; then
  echo "CONNECT_IT_INGRESS_PROJECT is reserved; the ingress test creates an isolated project name" >&2
  exit 1
fi

project_nonce="$(
  node -e '
    const crypto = require("node:crypto");
    process.stdout.write(crypto.randomBytes(24).toString("hex"));
  '
)"
compose_project="connect-it-ingress-${project_nonce}"
if [[ ! "${compose_project}" =~ ^connect-it-ingress-[a-f0-9]{48}$ ]]; then
  echo "Unable to generate a safe Docker ingress Compose project name" >&2
  exit 1
fi

pick_port() {
  node -e '
    const net = require("node:net");
    const server = net.createServer();
    server.listen(0, "127.0.0.1", () => {
      process.stdout.write(String(server.address().port));
      server.close();
    });
  '
}

validate_port() {
  local variable_name="$1"
  local value="$2"
  if [[ -z "${value}" ]]; then
    return 0
  fi
  if [[ ! "${value}" =~ ^[1-9][0-9]{0,4}$ ]] || ((value > 65535)); then
    echo "${variable_name} must be an integer from 1 through 65535" >&2
    return 1
  fi
}

go_port="${CONNECT_IT_INGRESS_GO_PORT:-}"
nginx_port="${CONNECT_IT_INGRESS_NGINX_PORT:-}"
validate_port CONNECT_IT_INGRESS_GO_PORT "${go_port}"
validate_port CONNECT_IT_INGRESS_NGINX_PORT "${nginx_port}"
if [[ -n "${go_port}" && -n "${nginx_port}" && "${go_port}" == "${nginx_port}" ]]; then
  echo "CONNECT_IT_INGRESS_GO_PORT and CONNECT_IT_INGRESS_NGINX_PORT must differ" >&2
  exit 1
fi
while [[ -z "${go_port}" || "${go_port}" == "${nginx_port}" ]]; do
  go_port="$(pick_port)"
done
while [[ -z "${nginx_port}" || "${nginx_port}" == "${go_port}" ]]; do
  nginx_port="$(pick_port)"
done
export CONNECT_IT_INGRESS_GO_PORT="${go_port}"
export CONNECT_IT_INGRESS_NGINX_PORT="${nginx_port}"

compose=(
  docker compose
  --project-name "${compose_project}"
  --file "${base_compose}"
  --file "${test_compose}"
)

project_has_existing_resources() {
  local resource_ids
  if ! resource_ids="$(
    docker ps \
      --all \
      --quiet \
      --filter "label=com.docker.compose.project=${compose_project}"
  )"; then
    echo "Unable to inspect containers for the generated Docker ingress project" >&2
    return 2
  fi
  if [[ -n "${resource_ids}" ]]; then
    return 10
  fi

  if ! resource_ids="$(
    docker network ls \
      --quiet \
      --filter "label=com.docker.compose.project=${compose_project}"
  )"; then
    echo "Unable to inspect networks for the generated Docker ingress project" >&2
    return 2
  fi
  if [[ -n "${resource_ids}" ]]; then
    return 10
  fi

  if ! resource_ids="$(
    docker volume ls \
      --quiet \
      --filter "label=com.docker.compose.project=${compose_project}"
  )"; then
    echo "Unable to inspect volumes for the generated Docker ingress project" >&2
    return 2
  fi
  if [[ -n "${resource_ids}" ]]; then
    return 10
  fi

  return 0
}

project_check_status=0
project_has_existing_resources || project_check_status=$?
if ((project_check_status == 10)); then
  echo "Generated Docker ingress project unexpectedly already owns resources; refusing cleanup" >&2
  exit 1
fi
if ((project_check_status != 0)); then
  exit "${project_check_status}"
fi

identity_override_root="$(
  mktemp -d "${TMPDIR:-/tmp}/connect-it-ingress-identity.XXXXXX"
)"
identity_override="${identity_override_root}/identity.yml"

cleanup() {
  local cleanup_status=0
  "${compose[@]}" down --volumes >/dev/null 2>&1 ||
    cleanup_status=$?
  rm -f -- "${identity_override}" || cleanup_status=$?
  rmdir "${identity_override_root}" || cleanup_status=$?
  return "${cleanup_status}"
}

cleanup_on_exit() {
  local primary_status=$?
  local cleanup_status=0
  trap - EXIT INT TERM
  cleanup || cleanup_status=$?
  if ((cleanup_status != 0)); then
    echo "Docker ingress cleanup failed with status ${cleanup_status}" >&2
  fi
  if ((primary_status != 0)); then
    exit "${primary_status}"
  fi
  exit "${cleanup_status}"
}
trap cleanup_on_exit EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

"${compose[@]}" up --detach postgres --wait
bash "${repository_root}/scripts/bootstrap-dev-compose-db.sh" \
  --scope ci-ingress \
  --project-name "${compose_project}" \
  --file "${base_compose}" \
  --file "${test_compose}" \
  --write-identity-override "${identity_override}"
compose+=(--file "${identity_override}")

if [[ "${CONNECT_IT_INGRESS_SKIP_BUILD:-0}" == "1" ]]; then
  "${compose[@]}" up --detach --no-build --wait
else
  "${compose[@]}" up --detach --build --wait
fi

login_headers="$(
  curl \
    --silent \
    --show-error \
    --dump-header - \
    --output /dev/null \
    --header "Content-Type: application/json" \
    --data '{"username":"admin","password":"change-me-admin-password"}' \
    "http://127.0.0.1:${CONNECT_IT_INGRESS_NGINX_PORT}/admin/login"
)"
session_cookie_header="$(
  printf '%s\n' "${login_headers}" |
    tr -d '\r' |
    grep -i '^Set-Cookie: connect_it_admin='
)"
for required_cookie_attribute in "HttpOnly" "Secure" "SameSite=Strict"; do
  if [[ "${session_cookie_header}" != *"${required_cookie_attribute}"* ]]; then
    echo "admin Cookie missing ${required_cookie_attribute} through shipped nginx" >&2
    exit 1
  fi
done

session_token="phase-c-ingress-session-token"
"${compose[@]}" exec -T postgres \
  psql -X -v ON_ERROR_STOP=1 -U connect_it -d connect_it <<'SQL'
insert into connections (
  id,
  connector_type,
  alias,
  auth_method,
  credential,
  secret_key_version,
  profile,
  scopes,
  scopes_known,
  status,
  access_token_expires_at,
  credential_version,
  authorization_generation,
  authorization_attempt_version,
  created_at,
  updated_at
) values (
  '00000000-0000-4000-8000-0000000000c1',
  'github',
  null,
  'pat',
  decode('00', 'hex'),
  1,
  '{}'::jsonb,
  '{}'::text[],
  false,
  'active',
  null,
  1,
  1,
  0,
  now(),
  now()
);

insert into mcp_sessions (
  id,
  token_hash,
  tool_allowlist,
  status,
  expires_at,
  created_at
) values (
  '00000000-0000-4000-8000-0000000000a1',
  '29893e01107346d9c922622a0b8068772f7100447c2472a6b93727e3b649517c',
  '{"version":1,"tools":[]}'::jsonb,
  'active',
  now() + interval '1 hour',
  now()
);

insert into mcp_session_connections (
  session_id,
  alias,
  connection_id,
  authorization_generation
) values (
  '00000000-0000-4000-8000-0000000000a1',
  'ingress',
  '00000000-0000-4000-8000-0000000000c1',
  1
);
SQL

CONNECT_IT_INGRESS_PROJECT="${compose_project}" \
CONNECT_IT_INGRESS_BASE_COMPOSE="${base_compose}" \
CONNECT_IT_INGRESS_TEST_COMPOSE="${test_compose}" \
CONNECT_IT_INGRESS_GO_URL="http://127.0.0.1:${CONNECT_IT_INGRESS_GO_PORT}" \
CONNECT_IT_INGRESS_NGINX_URL="http://127.0.0.1:${CONNECT_IT_INGRESS_NGINX_PORT}" \
CONNECT_IT_INGRESS_SESSION_TOKEN="${session_token}" \
  node "${repository_root}/scripts/mcp_ingress_e2e.mjs"
