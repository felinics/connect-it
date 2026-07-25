#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
runner="${repository_root}/scripts/run-provider-real-smoke.sh"
test_root="$(mktemp -d "${TMPDIR:-/tmp}/connect-it-provider-smoke-gate-test.XXXXXX")"
trap 'rm -rf -- "${test_root}"' EXIT INT TERM
chmod 0700 "${test_root}"

fail() {
  printf 'test-provider-real-smoke-gate: %s\n' "$*" >&2
  exit 1
}

file_mode() {
  local target="$1"
  if stat -c '%a' "${target}" >/dev/null 2>&1; then
    stat -c '%a' "${target}"
  else
    stat -f '%Lp' "${target}"
  fi
}

approved_commit="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
wrong_commit="bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
fake_bin="${test_root}/bin"
mkdir -p "${fake_bin}"

cat >"${fake_bin}/git" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ "${1:-}" == "-C" ]]; then
  shift 2
fi
case "${1:-}" in
  rev-parse)
    if [[ "${2:-}" == "--verify" ]]; then
      printf '%s\n' "${FAKE_GIT_RESOLVED:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}"
    elif [[ "${2:-}" == "HEAD" ]]; then
      printf '%s\n' "${FAKE_GIT_HEAD:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}"
    else
      exit 91
    fi
    ;;
  status)
    if [[ "${FAKE_GIT_DIRTY:-0}" == "1" ]]; then
      printf ' M packages/connectors/all.go\n'
    fi
    ;;
  *)
    exit 92
    ;;
esac
EOF

cat >"${fake_bin}/go" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

[[ $# -eq 6 ]] || exit 81
[[ "$1" == "test" && "$2" == "-json" && "$3" == "-count=1" &&
  "$4" == "-run" ]] || exit 82
[[ "${PWD}" == "${FAKE_GO_EXPECTED_PWD:?}" ]] || exit 86

row="$(awk -F '\t' -v pkg="$6" 'NR > 1 && $2 == pkg' "${FAKE_GO_MANIFEST:?}")"
[[ -n "${row}" ]] || exit 83
IFS=$'\t' read -r _ _ expected_test _ <<<"${row}"
package="github.com/memohai/connect-it/packages/connectors/${6#./}"
[[ "$5" == "^${expected_test}$" ]] || exit 84
printf '%s\t%s\n' "${expected_test}" "$6" >>"${FAKE_GO_LOG:?}"

printf '{"Time":"2026-07-24T00:00:00Z","Action":"start","Package":"%s"}\n' \
  "${package}"
printf '{"Time":"2026-07-24T00:00:00Z","Action":"run","Package":"%s","Test":"%s"}\n' \
  "${package}" "${expected_test}"
printf '{"Time":"2026-07-24T00:00:00Z","Action":"output","Package":"%s","Test":"%s","Output":"SECRET-SMOKE-CANARY\\n"}\n' \
  "${package}" "${expected_test}"

case "${FAKE_GO_MODE:-pass}" in
  pass)
    printf '{"Time":"2026-07-24T00:00:01Z","Action":"pass","Package":"%s","Test":"%s","Elapsed":1}\n' \
      "${package}" "${expected_test}"
    printf '{"Time":"2026-07-24T00:00:01Z","Action":"pass","Package":"%s","Elapsed":1}\n' \
      "${package}"
    ;;
  skip)
    printf '{"Time":"2026-07-24T00:00:01Z","Action":"skip","Package":"%s","Test":"%s","Elapsed":0}\n' \
      "${package}" "${expected_test}"
    printf '{"Time":"2026-07-24T00:00:01Z","Action":"pass","Package":"%s","Elapsed":0}\n' \
      "${package}"
    ;;
  fail)
    printf '%s\n' 'SECRET-STDERR-CANARY' >&2
    printf '{"Time":"2026-07-24T00:00:01Z","Action":"fail","Package":"%s","Test":"%s","Elapsed":1}\n' \
      "${package}" "${expected_test}"
    printf '{"Time":"2026-07-24T00:00:01Z","Action":"fail","Package":"%s","Elapsed":1}\n' \
      "${package}"
    exit 1
    ;;
  fail-zero)
    # Exercise the JSON verifier independently of go's normal non-zero exit.
    printf '{"Time":"2026-07-24T00:00:01Z","Action":"fail","Package":"%s","Test":"%s","Elapsed":1}\n' \
      "${package}" "${expected_test}"
    printf '{"Time":"2026-07-24T00:00:01Z","Action":"pass","Package":"%s","Elapsed":1}\n' \
      "${package}"
    ;;
  *)
    exit 85
    ;;
esac
EOF
chmod 0700 "${fake_bin}/git" "${fake_bin}/go"

run_gate() {
  local case_name="$1"
  local mode="$2"
  local dirty="$3"
  shift 3
  RUN_OUTPUT="${test_root}/${case_name}.output"
  RUN_GO_LOG="${test_root}/${case_name}.go.log"
  : >"${RUN_GO_LOG}"
  chmod 0600 "${RUN_GO_LOG}"
  RUN_STATUS=0
  PATH="${fake_bin}:${PATH}" \
    FAKE_GO_MODE="${mode}" \
    FAKE_GIT_DIRTY="${dirty}" \
    FAKE_GO_LOG="${RUN_GO_LOG}" \
    FAKE_GO_EXPECTED_PWD="${repository_root}/packages/connectors" \
    FAKE_GO_MANIFEST="${repository_root}/scripts/provider-real-smoke-manifest.tsv" \
    bash "${runner}" "$@" >"${RUN_OUTPUT}" 2>&1 || RUN_STATUS=$?
}

assert_no_raw_output() {
  local output_file="$1"
  if grep -Eq 'SECRET-SMOKE-CANARY|SECRET-STDERR-CANARY' "${output_file}"; then
    fail "raw go test output escaped the private temporary directory"
  fi
}

# One manifest, read by the runner and by this gate test, so a receipt can
# never claim a package, harness or cleanup mode the runner did not use.
manifest="${repository_root}/scripts/provider-real-smoke-manifest.tsv"
providers=()
packages=()
harnesses=()
cleanup_modes=()
while IFS=$'\t' read -r provider package harness cleanup_mode; do
  providers+=("${provider}")
  packages+=("${package}")
  harnesses+=("${harness}")
  cleanup_modes+=("${cleanup_mode}")
done < <(tail -n +2 "${manifest}")
[[ "${#providers[@]}" == "9" ]] ||
  fail "Provider manifest lists ${#providers[@]} Providers, want 9"

for index in "${!providers[@]}"; do
  provider="${providers[${index}]}"
  receipt="${test_root}/${provider}.tsv"
  run_gate "pass-${provider}" pass 0 \
    "${provider}" "${approved_commit}" "${receipt}"
  [[ "${RUN_STATUS}" == "0" ]] ||
    fail "${provider} PASS fixture returned ${RUN_STATUS}"
  assert_no_raw_output "${RUN_OUTPUT}"
  [[ -f "${receipt}" && ! -L "${receipt}" ]] ||
    fail "${provider} receipt was not a regular file"
  [[ "$(file_mode "${receipt}")" == "600" ]] ||
    fail "${provider} receipt mode was not 0600"
  [[ "$(wc -l <"${receipt}" | tr -d ' ')" == "8" ]] ||
    fail "${provider} receipt did not have the strict eight-row schema"
  [[ "$(cut -f1 "${receipt}")" == $'format\nprovider\nimplementation_commit\npackage\nharness\nexecuted_at\nresult\ncleanup_mode' ]] ||
    fail "${provider} receipt keys or key order are wrong"
  awk -F '\t' 'NF != 2 { exit 1 }' "${receipt}" ||
    fail "${provider} receipt did not contain exactly two TSV fields per row"
  grep -Fqx $'format\tconnect-it-provider-real-smoke-v1' "${receipt}" ||
    fail "${provider} receipt format is wrong"
  grep -Fqx $'provider\t'"${provider}" "${receipt}" ||
    fail "${provider} receipt Provider is wrong"
  grep -Fqx $'implementation_commit\t'"${approved_commit}" "${receipt}" ||
    fail "${provider} receipt commit is wrong"
  grep -Fqx $'package\t'"${packages[${index}]}" "${receipt}" ||
    fail "${provider} receipt package is wrong"
  grep -Fqx $'harness\t'"${harnesses[${index}]}" "${receipt}" ||
    fail "${provider} receipt harness is wrong"
  executed_at="$(awk -F '\t' '$1 == "executed_at" { print $2 }' "${receipt}")"
  [[ "${executed_at}" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$ ]] ||
    fail "${provider} receipt execution time is not RFC3339 UTC"
  grep -Fqx $'result\tPASS' "${receipt}" ||
    fail "${provider} receipt result is wrong"
  grep -Fqx $'cleanup_mode\t'"${cleanup_modes[${index}]}" "${receipt}" ||
    fail "${provider} receipt cleanup mode is wrong"
  if grep -Eq 'cleanup_(status|result)|SECRET-' "${receipt}"; then
    fail "${provider} receipt fabricated cleanup completion or leaked output"
  fi
  grep -Fqx \
    "${harnesses[${index}]}	${packages[${index}]}" \
    "${RUN_GO_LOG}" ||
    fail "${provider} did not invoke its exact fixed package and harness"
  [[ "$(wc -l <"${RUN_GO_LOG}" | tr -d ' ')" == "1" ]] ||
    fail "${provider} invoked go more than once"
done

skip_receipt="${test_root}/skip.tsv"
run_gate skip skip 0 github "${approved_commit}" "${skip_receipt}"
[[ "${RUN_STATUS}" == "1" && ! -e "${skip_receipt}" ]] ||
  fail "a skipped exact smoke test was accepted"
assert_no_raw_output "${RUN_OUTPUT}"
grep -Fq 'did not produce one explicit non-skipped PASS' "${RUN_OUTPUT}" ||
  fail "skip rejection was not explicit"

failed_receipt="${test_root}/failed.tsv"
run_gate fail fail 0 github "${approved_commit}" "${failed_receipt}"
[[ "${RUN_STATUS}" == "1" && ! -e "${failed_receipt}" ]] ||
  fail "a failed exact smoke test was accepted"
assert_no_raw_output "${RUN_OUTPUT}"
grep -Fq 'raw output was withheld' "${RUN_OUTPUT}" ||
  fail "test failure did not document the withheld-output boundary"

failed_zero_receipt="${test_root}/failed-zero.tsv"
run_gate fail-zero fail-zero 0 github "${approved_commit}" "${failed_zero_receipt}"
[[ "${RUN_STATUS}" == "1" && ! -e "${failed_zero_receipt}" ]] ||
  fail "a JSON FAIL event with zero go status was accepted"
assert_no_raw_output "${RUN_OUTPUT}"
grep -Fq 'did not produce one explicit non-skipped PASS' "${RUN_OUTPUT}" ||
  fail "JSON FAIL rejection did not come from the evidence verifier"

dirty_receipt="${test_root}/dirty.tsv"
run_gate dirty pass 1 github "${approved_commit}" "${dirty_receipt}"
[[ "${RUN_STATUS}" == "1" && ! -e "${dirty_receipt}" ]] ||
  fail "a dirty source/documentation worktree was accepted"
[[ ! -s "${RUN_GO_LOG}" ]] ||
  fail "dirty-worktree rejection happened after go test"

wrong_receipt="${test_root}/wrong-sha.tsv"
run_gate wrong-sha pass 0 github "${wrong_commit}" "${wrong_receipt}"
[[ "${RUN_STATUS}" == "1" && ! -e "${wrong_receipt}" ]] ||
  fail "a wrong approved commit was accepted"
[[ ! -s "${RUN_GO_LOG}" ]] ||
  fail "wrong-SHA rejection happened after go test"

symlink_target="${test_root}/symlink-target"
printf 'do-not-overwrite\n' >"${symlink_target}"
symlink_receipt="${test_root}/symlink.tsv"
ln -s "${symlink_target}" "${symlink_receipt}"
run_gate symlink pass 0 github "${approved_commit}" "${symlink_receipt}"
[[ "${RUN_STATUS}" == "1" && -L "${symlink_receipt}" ]] ||
  fail "a symlink receipt target was accepted or replaced"
grep -Fqx 'do-not-overwrite' "${symlink_target}" ||
  fail "symlink receipt rejection overwrote its target"
[[ ! -s "${RUN_GO_LOG}" ]] ||
  fail "symlink receipt rejection happened after go test"

unknown_receipt="${test_root}/unknown.tsv"
run_gate unknown pass 0 unknown "${approved_commit}" "${unknown_receipt}"
[[ "${RUN_STATUS}" == "1" && ! -e "${unknown_receipt}" ]] ||
  fail "an unknown Provider was accepted"

run_gate missing-arguments pass 0
[[ "${RUN_STATUS}" == "2" ]] ||
  fail "missing arguments returned ${RUN_STATUS}, want 2"

run_gate relative-receipt pass 0 github "${approved_commit}" relative.tsv
[[ "${RUN_STATUS}" == "1" ]] ||
  fail "a relative receipt path was accepted"

existing_receipt="${test_root}/existing.tsv"
printf 'existing\n' >"${existing_receipt}"
run_gate existing pass 0 github "${approved_commit}" "${existing_receipt}"
[[ "${RUN_STATUS}" == "1" ]] ||
  fail "a pre-existing receipt was accepted"
grep -Fqx 'existing' "${existing_receipt}" ||
  fail "a pre-existing receipt was overwritten"

printf 'test-provider-real-smoke-gate: PASS\n'
