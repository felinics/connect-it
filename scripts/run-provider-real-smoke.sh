#!/usr/bin/env bash
set -euo pipefail

export LC_ALL=C
umask 077
unset -f git go node 2>/dev/null || true
hash -r

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
json_verifier="${repository_root}/scripts/verify-provider-real-smoke-json.mjs"
manifest="${repository_root}/scripts/provider-real-smoke-manifest.tsv"
smoke_temporary_root=""
receipt_temporary_file=""

cleanup() {
  if [[ -n "${receipt_temporary_file}" ]]; then
    rm -f -- "${receipt_temporary_file}"
  fi
  if [[ -n "${smoke_temporary_root}" ]]; then
    rm -rf -- "${smoke_temporary_root}"
  fi
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  printf 'run-provider-real-smoke: %s\n' "$*" >&2
  exit 1
}

usage() {
  cat >&2 <<EOF
usage:
  run-provider-real-smoke.sh PROVIDER APPROVED_FULL_HEAD_SHA /ABSOLUTE/RECEIPT.tsv

Supported PROVIDER values:
  $(tail -n +2 "${manifest}" 2>/dev/null | cut -f1 | tr '\n' ' ')

Provider credentials and explicit write gates are inherited from the protected
test environment documented in docs/providers/<provider>.md. They must never be
passed on this command line.
EOF
  exit 2
}

[[ $# -eq 3 ]] || usage
provider="$1"
approved_commit="$2"
receipt_path="$3"

# The nine-Provider manifest lives in exactly one place. Both this runner and
# its gate test read it, so a Provider can never be wired to the wrong package,
# harness or cleanup mode in one copy but not the other.
[[ -f "${manifest}" && ! -L "${manifest}" ]] ||
  fail "Provider manifest is missing"
[[ "${provider}" =~ ^[a-z_]{1,32}$ ]] ||
  fail "unsupported Provider"
manifest_row="$(
  awk -F '\t' -v provider="${provider}" 'NR > 1 && $1 == provider' \
    "${manifest}"
)"
[[ "$(printf '%s' "${manifest_row}" | grep -c .)" == "1" ]] ||
  fail "unsupported Provider"
IFS=$'\t' read -r _ package_argument harness cleanup_mode \
  <<<"${manifest_row}"
[[ "${package_argument}" =~ ^\./[a-z]{1,32}$ &&
  "${harness}" =~ ^Test[A-Za-z]{1,48}RealSmoke$ &&
  "${cleanup_mode}" =~ ^[a-z_]{1,64}$ ]] ||
  fail "Provider manifest row is malformed"
package_import="github.com/memohai/connect-it/packages/connectors/${package_argument#./}"

[[ "${approved_commit}" =~ ^[0-9a-f]{40}([0-9a-f]{24})?$ ]] ||
  fail "approved commit must be a full lowercase Git object ID"
[[ "${receipt_path}" == /* ]] ||
  fail "receipt path must be absolute"
[[ ! -e "${receipt_path}" && ! -L "${receipt_path}" ]] ||
  fail "receipt path already exists"
receipt_directory="$(dirname "${receipt_path}")"
receipt_name="$(basename "${receipt_path}")"
[[ -d "${receipt_directory}" && ! -L "${receipt_directory}" ]] ||
  fail "receipt parent must be an existing non-symlink directory"
[[ "${receipt_name}" != "." && "${receipt_name}" != ".." ]] ||
  fail "receipt path is invalid"
[[ -f "${json_verifier}" && ! -L "${json_verifier}" ]] ||
  fail "JSON verifier is missing"

verify_approved_worktree() {
  local resolved_commit head_commit worktree_status
  resolved_commit="$(
    command git -C "${repository_root}" \
      rev-parse --verify "${approved_commit}^{commit}" 2>/dev/null
  )" || fail "approved commit does not resolve"
  [[ "${resolved_commit}" == "${approved_commit}" ]] ||
    fail "approved commit did not resolve exactly"
  head_commit="$(
    command git -C "${repository_root}" rev-parse HEAD 2>/dev/null
  )" || fail "repository HEAD is unavailable"
  [[ "${head_commit}" == "${approved_commit}" ]] ||
    fail "repository HEAD is not the approved commit"
  worktree_status="$(
    command git -C "${repository_root}" \
      status --porcelain=v1 --untracked-files=all 2>/dev/null
  )" || fail "repository worktree status is unavailable"
  [[ -z "${worktree_status}" ]] ||
    fail "repository source and documentation worktree must be clean"
}

verify_approved_worktree

smoke_temporary_root="$(
  mktemp -d "${TMPDIR:-/tmp}/connect-it-provider-smoke.XXXXXX"
)"
chmod 0700 "${smoke_temporary_root}"
raw_json="${smoke_temporary_root}/go-test.json"
raw_stderr="${smoke_temporary_root}/go-test.stderr"
: >"${raw_json}"
: >"${raw_stderr}"
chmod 0600 "${raw_json}" "${raw_stderr}"

go_status=0
(
  cd "${repository_root}/packages/connectors"
  command go test \
    -json \
    -count=1 \
    -run "^${harness}$" \
    "${package_argument}"
) >"${raw_json}" 2>"${raw_stderr}" || go_status=$?
if ((go_status != 0)); then
  fail "real-account smoke test failed; raw output was withheld"
fi

if ! command node "${json_verifier}" \
  "${raw_json}" "${package_import}" "${harness}" \
  >/dev/null 2>/dev/null; then
  fail "real-account smoke did not produce one explicit non-skipped PASS"
fi

# Recheck after compilation and network execution so a concurrent persistent
# source/document edit cannot be followed by a receipt for the old HEAD.
verify_approved_worktree

executed_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
[[ "${executed_at}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
  fail "could not obtain an RFC3339 UTC execution time"

receipt_temporary_file="$(
  mktemp "${receipt_directory}/.${receipt_name}.tmp.XXXXXX"
)"
chmod 0600 "${receipt_temporary_file}"
{
  printf 'format\tconnect-it-provider-real-smoke-v1\n'
  printf 'provider\t%s\n' "${provider}"
  printf 'implementation_commit\t%s\n' "${approved_commit}"
  printf 'package\t%s\n' "${package_argument}"
  printf 'harness\t%s\n' "${harness}"
  printf 'executed_at\t%s\n' "${executed_at}"
  printf 'result\tPASS\n'
  printf 'cleanup_mode\t%s\n' "${cleanup_mode}"
} >"${receipt_temporary_file}"

# A same-directory hard link is an atomic no-clobber publish. It rejects both
# regular pre-existing outputs and dangling symlinks.
if ! ln "${receipt_temporary_file}" "${receipt_path}" 2>/dev/null; then
  fail "receipt output appeared concurrently"
fi
rm -f -- "${receipt_temporary_file}"
receipt_temporary_file=""

printf 'run-provider-real-smoke: PASS provider=%s implementation_commit=%s\n' \
  "${provider}" "${approved_commit}"
