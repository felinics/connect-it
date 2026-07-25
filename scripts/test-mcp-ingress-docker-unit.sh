#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
target="${repository_root}/scripts/test-mcp-ingress-docker.sh"
bootstrap_target="${repository_root}/scripts/bootstrap-dev-compose-db.sh"
fake_bin="${repository_root}/scripts/testdata/ingress"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/connect-it-ingress-script.XXXXXX")"
trap 'rm -rf -- "${test_root}"' EXIT INT TERM

fail() {
  echo "test-mcp-ingress-docker-unit: $*" >&2
  exit 1
}

# 统一的被测脚本执行器：`--` 之前是注入的环境变量，之后是脚本参数。
run_script() {
  local case_name="$1"
  local script="$2"
  shift 2
  local env_assignments=()
  while (($# > 0)) && [[ "$1" != "--" ]]; do
    env_assignments+=("$1")
    shift
  done
  if (($# > 0)); then
    shift
  fi

  local output_file="${test_root}/${case_name}.out"
  local log_file="${test_root}/${case_name}.docker.log"
  local status=0

  : >"${log_file}"
  env \
    PATH="${fake_bin}:${PATH}" \
    FAKE_DOCKER_LOG="${log_file}" \
    ${env_assignments[@]+"${env_assignments[@]}"} \
    bash "${script}" "$@" >"${output_file}" 2>&1 || status=$?

  RUN_STATUS="${status}"
  RUN_OUTPUT_FILE="${output_file}"
  RUN_LOG_FILE="${log_file}"
}

run_target() {
  local case_name="$1"
  shift
  run_script "${case_name}" "${target}" "$@" --
}

run_bootstrap() {
  local case_name="$1"
  shift
  run_script "${case_name}" "${bootstrap_target}" "$@" \
    -- --scope dev --file docker/docker-compose.yml
}

run_bootstrap_identity() {
  local case_name="$1"
  local output_target="$2"
  shift 2
  run_script "${case_name}" "${bootstrap_target}" "$@" \
    -- --scope dev --file docker/docker-compose.yml \
    --write-identity-override "${output_target}"
}

assert_no_docker_calls() {
  [[ ! -s "${RUN_LOG_FILE}" ]] ||
    fail "expected no Docker calls; got $(tr '\n' ' ' <"${RUN_LOG_FILE}")"
}

# fake docker 日志里所有 `compose --project-name` 的取值，按调用顺序输出。
compose_project_names() {
  awk -F '\t' '
    $1 == "compose" {
      for (field_index = 1; field_index < NF; field_index++) {
        if ($field_index == "--project-name") {
          print $(field_index + 1)
        }
      }
    }
  ' "${RUN_LOG_FILE}"
}

run_script \
  bootstrap_release_alias "${bootstrap_target}" \
  -- --scope dev \
  --file docker/../docker/docker-compose.release.yml
[[ "${RUN_STATUS}" == "1" ]] ||
  fail "release alias bootstrap returned ${RUN_STATUS}, want 1"
assert_no_docker_calls
grep -q 'dev scope only accepts' "${RUN_OUTPUT_FILE}" ||
  fail "release Compose alias was not rejected by the exact scope gate"

run_script \
  bootstrap_dev_project "${bootstrap_target}" \
  -- --scope dev \
  --project-name connect-it-other \
  --file docker/docker-compose.yml
[[ "${RUN_STATUS}" == "1" ]] ||
  fail "dev project override returned ${RUN_STATUS}, want 1"
assert_no_docker_calls
grep -q 'dev scope must use the fixed project' "${RUN_OUTPUT_FILE}" ||
  fail "dev project override was not rejected"

run_bootstrap \
  bootstrap_ambient_project \
  COMPOSE_PROJECT_NAME=other \
  COMPOSE_FILE=docker/docker-compose.release.yml \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='1|t' \
  FAKE_DOCKER_BOOTSTRAP_SCHEMA_STATE='1|1|f'
[[ "${RUN_STATUS}" == "0" ]] ||
  fail "ambient project bootstrap returned ${RUN_STATUS}, want 0"
ambient_projects="$(compose_project_names | sort -u)"
[[ "${ambient_projects}" == "connect-it" ]] ||
  fail "ambient Compose environment redirected dev bootstrap to: ${ambient_projects}"
grep -q -- $'^compose\t--project-name\tconnect-it\t--file\t' \
  "${RUN_LOG_FILE}" ||
  fail "dev bootstrap did not pin its Compose project on the command line"

run_bootstrap \
  bootstrap_clean \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='1|t' \
  FAKE_DOCKER_BOOTSTRAP_SCHEMA_STATE='1|1|f'
[[ "${RUN_STATUS}" == "0" ]] ||
  fail "clean bootstrap returned ${RUN_STATUS}, want 0"
grep -q 'already clean; no-op' "${RUN_OUTPUT_FILE}" ||
  fail "clean bootstrap did not report no-op"
if grep -q -- '--set=connect_it_baseline=1' "${RUN_LOG_FILE}"; then
  fail "clean bootstrap unexpectedly applied migrations"
fi
grep -q -- '--set=connect_it_dev_configuration=1' "${RUN_LOG_FILE}" ||
  fail "clean bootstrap omitted the shared development database policy"

identity_output_root="${test_root}/identity-output"
mkdir "${identity_output_root}"
chmod 0700 "${identity_output_root}"
identity_output="${identity_output_root}/identity.yml"
run_bootstrap_identity \
  bootstrap_clean_identity \
  "${identity_output}" \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='1|t' \
  FAKE_DOCKER_BOOTSTRAP_SCHEMA_STATE='1|1|f' \
  FAKE_DOCKER_BOOTSTRAP_IDENTITY='connect_it|public|7234567890123456789'
[[ "${RUN_STATUS}" == "0" ]] ||
  fail "clean identity bootstrap returned ${RUN_STATUS}, want 0"
[[ -f "${identity_output}" && ! -L "${identity_output}" ]] ||
  fail "clean bootstrap did not publish a regular identity override"
identity_mode="$(
  stat -c '%a' "${identity_output}" 2>/dev/null ||
    stat -f '%Lp' "${identity_output}"
)"
[[ "${identity_mode}" == "600" ]] ||
  fail "identity override mode is ${identity_mode}, want 600"
grep -Fxq \
  '      CONNECT_IT_EXPECTED_DATABASE_NAME: "connect_it"' \
  "${identity_output}" ||
  fail "identity override omitted the explicit database name"
grep -Fxq \
  '      CONNECT_IT_EXPECTED_DATABASE_SCHEMA: "public"' \
  "${identity_output}" ||
  fail "identity override omitted the explicit database schema"
grep -Fxq \
  '      CONNECT_IT_EXPECTED_DATABASE_APPLICATION_ROLE: "connect_it_app"' \
  "${identity_output}" ||
  fail "identity override omitted the explicit application role"
grep -Fxq \
  '      CONNECT_IT_EXPECTED_DATABASE_OWNER_ROLE: "connect_it_owner"' \
  "${identity_output}" ||
  fail "identity override omitted the explicit owner role"
grep -Fxq \
  '      CONNECT_IT_EXPECTED_DATABASE_SYSTEM_IDENTIFIER: "7234567890123456789"' \
  "${identity_output}" ||
  fail "identity override omitted the queried system identifier"
if grep -Eq 'DATABASE_URL|PASSWORD|SECRET|COOKIE' "${identity_output}"; then
  fail "identity override contains secret-bearing configuration"
fi

preexisting_identity="${identity_output_root}/preexisting.yml"
printf '%s\n' 'operator-owned' >"${preexisting_identity}"
chmod 0600 "${preexisting_identity}"
run_bootstrap_identity \
  bootstrap_identity_no_clobber \
  "${preexisting_identity}" \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='1|t'
[[ "${RUN_STATUS}" == "1" ]] ||
  fail "preexisting identity output returned ${RUN_STATUS}, want 1"
assert_no_docker_calls
[[ "$(cat "${preexisting_identity}")" == "operator-owned" ]] ||
  fail "identity bootstrap clobbered a preexisting output"

bad_identity_output="${identity_output_root}/bad-identity.yml"
run_bootstrap_identity \
  bootstrap_wrong_identity \
  "${bad_identity_output}" \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='1|t' \
  FAKE_DOCKER_BOOTSTRAP_SCHEMA_STATE='1|1|f' \
  FAKE_DOCKER_BOOTSTRAP_IDENTITY='other|public|7234567890123456789'
[[ "${RUN_STATUS}" == "1" ]] ||
  fail "wrong database identity returned ${RUN_STATUS}, want 1"
[[ ! -e "${bad_identity_output}" ]] ||
  fail "wrong database identity published an override"
grep -q 'identity is not exactly connect_it/public' "${RUN_OUTPUT_FILE}" ||
  fail "wrong database identity rejection was not explicit"

oversized_identity_output="${identity_output_root}/oversized-identity.yml"
run_bootstrap_identity \
  bootstrap_oversized_identity \
  "${oversized_identity_output}" \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='1|t' \
  FAKE_DOCKER_BOOTSTRAP_SCHEMA_STATE='1|1|f' \
  FAKE_DOCKER_BOOTSTRAP_IDENTITY='connect_it|public|18446744073709551616'
[[ "${RUN_STATUS}" == "1" ]] ||
  fail "oversized system identifier returned ${RUN_STATUS}, want 1"
[[ ! -e "${oversized_identity_output}" ]] ||
  fail "oversized system identifier published an override"
grep -q 'system identifier exceeds uint64' "${RUN_OUTPUT_FILE}" ||
  fail "oversized system identifier rejection was not explicit"

run_bootstrap \
  bootstrap_forged_v1 \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='1|t' \
  FAKE_DOCKER_BOOTSTRAP_SCHEMA_STATE='1|1|f' \
  FAKE_DOCKER_BOOTSTRAP_SCHEMA_SHAPE=f
[[ "${RUN_STATUS}" == "1" ]] ||
  fail "forged v1 bootstrap returned ${RUN_STATUS}, want 1"
grep -q 'not an exact usable schema v1 clean state' "${RUN_OUTPUT_FILE}" ||
  fail "forged v1 schema was not rejected"

run_bootstrap \
  bootstrap_partial \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='2|f'
[[ "${RUN_STATUS}" == "1" ]] ||
  fail "partial bootstrap returned ${RUN_STATUS}, want 1"
grep -q 'database is not completely empty' "${RUN_OUTPUT_FILE}" ||
  fail "partial bootstrap was not rejected explicitly"
if grep -q -- '--set=connect_it_baseline=1' "${RUN_LOG_FILE}"; then
  fail "partial bootstrap unexpectedly applied migrations"
fi

run_bootstrap \
  bootstrap_dirty \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='20|t' \
  FAKE_DOCKER_BOOTSTRAP_SCHEMA_STATE='1|1|t'
[[ "${RUN_STATUS}" == "1" ]] ||
  fail "dirty bootstrap returned ${RUN_STATUS}, want 1"
grep -q 'schema_migrations is not exactly version 1 clean' \
  "${RUN_OUTPUT_FILE}" &&
  fail "dirty bootstrap used the obsolete error message"
grep -q 'not an exact usable schema v1 clean state' "${RUN_OUTPUT_FILE}" ||
  fail "dirty bootstrap was not rejected explicitly"

bootstrap_state="${test_root}/bootstrap.state"
bootstrap_input="${test_root}/bootstrap.sql"
run_bootstrap \
  bootstrap_empty \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='0|f' \
  FAKE_DOCKER_BOOTSTRAP_STATE_FILE="${bootstrap_state}" \
  FAKE_DOCKER_BOOTSTRAP_INPUT_FILE="${bootstrap_input}"
[[ "${RUN_STATUS}" == "0" ]] ||
  fail "empty bootstrap returned ${RUN_STATUS}, want 0"
[[ -s "${bootstrap_state}" ]] ||
  fail "empty bootstrap did not apply the migration transaction"
grep -q -- '--set=connect_it_dev_configuration=1' "${RUN_LOG_FILE}" ||
  fail "empty bootstrap omitted the shared development database policy"
grep -q 'create table admin_account' "${bootstrap_input}" ||
  fail "empty bootstrap omitted baseline tables"
grep -q 'create table schema_migrations' "${bootstrap_input}" ||
  fail "empty bootstrap omitted schema_migrations"
grep -q 'values (1, false)' "${bootstrap_input}" ||
  fail "empty bootstrap omitted current clean marker"
if grep -qE 'authorization_kind|audit_history_scrub_evidence' \
  "${bootstrap_input}"; then
  fail "empty bootstrap retained a removed legacy schema object"
fi
grep -q 'fresh schema v1 is ready' "${RUN_OUTPUT_FILE}" ||
  fail "empty bootstrap did not verify the final schema"

failed_bootstrap_state="${test_root}/failed-bootstrap.state"
run_bootstrap \
  bootstrap_apply_failure \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='0|f' \
  FAKE_DOCKER_BOOTSTRAP_STATE_FILE="${failed_bootstrap_state}" \
  FAKE_DOCKER_BOOTSTRAP_APPLY_STATUS=55
[[ "${RUN_STATUS}" == "55" ]] ||
  fail "failed migration returned ${RUN_STATUS}, want 55"
[[ ! -e "${failed_bootstrap_state}" ]] ||
  fail "failed migration was recorded as bootstrapped"

run_target project_override CONNECT_IT_INGRESS_PROJECT=connect-it
[[ "${RUN_STATUS}" == "1" ]] || fail "project override returned ${RUN_STATUS}, want 1"
assert_no_docker_calls
grep -q 'CONNECT_IT_INGRESS_PROJECT is reserved' "${RUN_OUTPUT_FILE}" ||
  fail "project override rejection was not explicit"

run_target invalid_port CONNECT_IT_INGRESS_GO_PORT=0
[[ "${RUN_STATUS}" == "1" ]] || fail "invalid port returned ${RUN_STATUS}, want 1"
assert_no_docker_calls
grep -q 'CONNECT_IT_INGRESS_GO_PORT must be an integer' "${RUN_OUTPUT_FILE}" ||
  fail "invalid port rejection was not explicit"

run_target existing_project FAKE_DOCKER_EXISTING_RESOURCES=1
[[ "${RUN_STATUS}" == "1" ]] || fail "existing project returned ${RUN_STATUS}, want 1"
grep -q '^ps	' "${RUN_LOG_FILE}" || fail "existing project did not inspect containers"
if grep -q '^compose	' "${RUN_LOG_FILE}"; then
  fail "existing project invoked Docker Compose"
fi
grep -q 'refusing cleanup' "${RUN_OUTPUT_FILE}" ||
  fail "existing project rejection was not explicit"

run_target partial_up_failure FAKE_DOCKER_UP_STATUS=42 FAKE_DOCKER_DOWN_STATUS=0
[[ "${RUN_STATUS}" == "42" ]] ||
  fail "partial startup returned ${RUN_STATUS}, want original status 42"
seen_projects="$(compose_project_names)"
up_project="${seen_projects%%$'\n'*}"
[[ "${up_project}" =~ ^connect-it-ingress-[a-f0-9]{48}$ ]] ||
  fail "generated unsafe project name: ${up_project}"
compose_projects="$(printf '%s\n' "${seen_projects}" | sort -u)"
[[ "${compose_projects}" == "${up_project}" ]] ||
  fail "startup and cleanup used different projects: ${compose_projects}"
grep -q $'^compose\t.*\tup\t' "${RUN_LOG_FILE}" ||
  fail "partial startup did not invoke compose up"
grep -q $'^compose\t.*\tdown\t--volumes$' "${RUN_LOG_FILE}" ||
  fail "partial startup did not clean its generated project"
if grep -q -- '--remove-orphans' "${RUN_LOG_FILE}"; then
  fail "cleanup unexpectedly requested orphan removal"
fi

run_target cleanup_failure FAKE_DOCKER_UP_STATUS=42 FAKE_DOCKER_DOWN_STATUS=43
[[ "${RUN_STATUS}" == "42" ]] ||
  fail "cleanup replaced primary status with ${RUN_STATUS}, want 42"
grep -q 'Docker ingress cleanup failed with status 43' "${RUN_OUTPUT_FILE}" ||
  fail "cleanup failure was not reported"

up_sequence="${test_root}/ingress-up-sequence"
identity_capture="${test_root}/ingress-identity-capture.yml"
run_target \
  ingress_identity_forwarding \
  FAKE_DOCKER_UP_SEQUENCE_FILE="${up_sequence}" \
  FAKE_DOCKER_FIRST_UP_STATUS=0 \
  FAKE_DOCKER_SECOND_UP_STATUS=47 \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='1|t' \
  FAKE_DOCKER_BOOTSTRAP_SCHEMA_STATE='1|1|f' \
  FAKE_DOCKER_BOOTSTRAP_IDENTITY='connect_it|public|7234567890123456789' \
  FAKE_DOCKER_IDENTITY_CAPTURE_FILE="${identity_capture}"
[[ "${RUN_STATUS}" == "47" ]] ||
  fail "identity forwarding returned ${RUN_STATUS}, want second-up status 47"
[[ -s "${identity_capture}" ]] ||
  fail "final ingress startup did not consume the generated identity override"
grep -Fxq \
  '      CONNECT_IT_EXPECTED_DATABASE_SYSTEM_IDENTIFIER: "7234567890123456789"' \
  "${identity_capture}" ||
  fail "final ingress startup received the wrong database identity"
second_up_identity="$(
  awk -F '\t' '
    $1 == "compose" {
      is_up = 0
      for (field_index = 2; field_index <= NF; field_index++) {
        if ($field_index == "up") is_up = 1
      }
      if (!is_up) next
      up_count++
      if (up_count != 2) next
      for (field_index = 2; field_index < NF; field_index++) {
        if ($field_index == "--file" &&
            $(field_index + 1) ~ /connect-it-ingress-identity[.][^\/]+\/identity[.]yml$/) {
          print $(field_index + 1)
        }
      }
    }
  ' "${RUN_LOG_FILE}"
)"
[[ "${second_up_identity}" == /* ]] ||
  fail "second ingress startup did not append the protected identity override"
[[ ! -e "${second_up_identity}" &&
  ! -e "$(dirname "${second_up_identity}")" ]] ||
  fail "ingress cleanup retained the generated identity override"

run_target \
  ingress_partial_database \
  FAKE_DOCKER_UP_STATUS=0 \
  FAKE_DOCKER_BOOTSTRAP_CATALOG_STATE='3|f'
[[ "${RUN_STATUS}" == "1" ]] ||
  fail "partial ingress database returned ${RUN_STATUS}, want 1"
grep -q 'database is not completely empty' "${RUN_OUTPUT_FILE}" ||
  fail "ingress runner did not fail through the bootstrap gate"
grep -q $'^compose\t.*\tdown\t--volumes$' "${RUN_LOG_FILE}" ||
  fail "bootstrap rejection did not clean the ingress project"

printf 'test-mcp-ingress-docker-unit: PASS\n'
