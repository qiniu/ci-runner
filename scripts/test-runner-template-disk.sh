#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workdir="$(mktemp -d)"
trap 'find "$workdir" -depth -delete' EXIT

cat >"$workdir/qshell" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  version)
    printf 'v%s\n' "${MOCK_QSHELL_VERSION:-2.19.13}"
    ;;
  'sandbox template list --format json')
    if [ -n "${MOCK_TEMPLATE_LIST_JSON:-}" ]; then
      printf '%s\n' "$MOCK_TEMPLATE_LIST_JSON"
      exit 0
    fi
    jq -n --arg alias "${MOCK_ALIAS:-github-runner-ubuntu-24-04-large}" --argjson disk "$MOCK_DISK" \
      --arg build_status "${MOCK_BUILD_STATUS:-uploaded}" \
      '[{Aliases: [$alias], TemplateID: "existing-template-id", BuildID: "fixture-build-id",
         BuildStatus: $build_status, DiskSizeMB: $disk}]'
    ;;
  'sandbox template builds existing-template-id fixture-build-id')
    if [ -n "${MOCK_EXACT_BUILD_ERROR:-}" ]; then
      echo "$MOCK_EXACT_BUILD_ERROR" >&2
      exit "${MOCK_EXACT_BUILD_EXIT_STATUS:-0}"
    fi
    echo 'Template ID:  existing-template-id'
    echo 'Build ID:     fixture-build-id'
    echo "Status:       ${MOCK_EXACT_BUILD_STATUS:-ready}"
    echo
    echo 'Build Logs:'
    echo '  fixture build log'
    ;;
  'sandbox template publish existing-template-id -y')
    echo 'Template fixture published'
    ;;
  sandbox\ template\ build\ --wait\ --config\ *)
    grep -Fq 'name = "github-runner-ubuntu-24-04-large"' "${*: -1}"
    echo 'Template ID: existing-template-id'
    echo 'Build ID: fixture-build-id'
    echo "Status: ${MOCK_QSHELL_TERMINAL_STATUS:-ready}"
    exit "${MOCK_QSHELL_EXIT_STATUS:-0}"
    ;;
  sandbox\ create\ *)
    echo 'Sandbox ID: sb-fixture'
    ;;
  sandbox\ exec\ *)
    command="${*: -1}"
    if [[ "$command" == *'disk_bytes=$(lsblk -bndo SIZE'* ]]; then
      bash -c "$command"
    else
      echo '__QINIU_RUNNER_CONFORMANCE_REMOTE_STARTED__'
    fi
    ;;
  sandbox\ kill\ *)
    echo "Killed sandbox $3"
    ;;
  *)
    echo "unexpected qshell command: $*" >&2
    exit 1
    ;;
esac
EOF
cat >"$workdir/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
cat "$MOCK_CATALOG"
EOF
cat >"$workdir/findmnt" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '/dev/root\n'
EOF
cat >"$workdir/lsblk" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$((MOCK_DISK_MIB * 1048576))"
EOF
chmod +x "$workdir/qshell" "$workdir/curl" "$workdir/findmnt" "$workdir/lsblk"

cat >"$workdir/bash-env" <<EOF
bash() {
  if [[ "\${1:-}" == "$repository_root/scripts/prepare-runner-archive.sh" ]]; then
    return 0
  fi
  command bash "\$@"
}
EOF

expect_failure() {
  local wanted_message="$1"
  shift
  if "$@" >"$workdir/output" 2>&1; then
    echo "expected failure: $*" >&2
    exit 1
  fi
  grep -Fq "$wanted_message" "$workdir/output" || {
    cat "$workdir/output" >&2
    echo "missing expected error: $wanted_message" >&2
    exit 1
  }
}

template_dir="$repository_root/templates/github-runner-ubuntu-24.04"
large_template_config="$template_dir/qshell.sandbox.large.toml"
standard_template_config="$template_dir/qshell.sandbox.toml"
operation_script="$repository_root/scripts/run-runner-template-operation.sh"
export QINIU_SANDBOX_API_URL=https://sandbox.invalid QINIU_API_KEY=fixture
export QSHELL="$workdir/qshell"
export MOCK_DISK=22222
expect_failure 'qshell >= 2.19.13 is required' \
  env MOCK_QSHELL_VERSION=2.19.12 bash "$operation_script" build "$large_template_config"
BASH_ENV="$workdir/bash-env" bash "$operation_script" build "$large_template_config" >"$workdir/output"
grep -Fq 'Template ID: existing-template-id' "$workdir/output"
grep -Fq 'Status: ready' "$workdir/output"
MOCK_QSHELL_TERMINAL_STATUS=error MOCK_QSHELL_EXIT_STATUS=201 \
  MOCK_TEMPLATE_LIST_JSON='[]' TEMPLATE_BUILD_RECONCILE_TIMEOUT_SECONDS=0 \
  BASH_ENV="$workdir/bash-env" bash "$operation_script" build "$large_template_config" >"$workdir/output"
grep -Fq 'template build fixture-build-id reached ready during reconciliation' "$workdir/output"
expect_failure 'template build fixture-build-id failed' \
  env MOCK_QSHELL_TERMINAL_STATUS=error MOCK_QSHELL_EXIT_STATUS=201 \
  MOCK_EXACT_BUILD_STATUS=failed TEMPLATE_BUILD_RECONCILE_TIMEOUT_SECONDS=0 \
  BASH_ENV="$workdir/bash-env" bash "$operation_script" build "$large_template_config"
expect_failure 'template build fixture-build-id remains building after 0s' \
  env MOCK_QSHELL_TERMINAL_STATUS=error MOCK_QSHELL_EXIT_STATUS=201 \
  MOCK_EXACT_BUILD_STATUS=building TEMPLATE_BUILD_RECONCILE_TIMEOUT_SECONDS=0 \
  BASH_ENV="$workdir/bash-env" bash "$operation_script" build "$large_template_config"
expect_failure 'Error: fixture exact-build query failed' \
  env MOCK_QSHELL_TERMINAL_STATUS=error MOCK_QSHELL_EXIT_STATUS=201 \
  MOCK_EXACT_BUILD_ERROR='Error: fixture exact-build query failed' \
  TEMPLATE_BUILD_RECONCILE_TIMEOUT_SECONDS=0 \
  BASH_ENV="$workdir/bash-env" bash "$operation_script" build "$large_template_config"
grep -Fq 'could not query template build fixture-build-id after 0s of reconciliation' "$workdir/output"
expect_failure 'total disk size 22222 MiB is below disk_size_mb 81920 MiB' \
  bash "$operation_script" publish "$large_template_config"

MOCK_DISK=83662 bash "$operation_script" publish "$large_template_config" >"$workdir/output"
grep -Fq 'Template fixture published' "$workdir/output"
MOCK_DISK=83662 MOCK_QSHELL_VERSION=2.19.12 \
  bash "$operation_script" publish "$large_template_config" >"$workdir/output"
expect_failure 'is missing from the service catalog' \
  env MOCK_TEMPLATE_LIST_JSON='[]' bash "$operation_script" publish "$large_template_config"
expect_failure 'is duplicated in the service catalog' \
  env MOCK_TEMPLATE_LIST_JSON='[{"Aliases":["github-runner-ubuntu-24-04-large"]},{"Aliases":["github-runner-ubuntu-24-04-large"]}]' \
  bash "$operation_script" publish "$large_template_config"

MOCK_ALIAS=github-runner-ubuntu-24-04 MOCK_DISK=22222 \
  bash "$operation_script" publish "$standard_template_config" >"$workdir/output"
expect_failure 'total disk size 19000 MiB is below disk_size_mb 20480 MiB' \
  env MOCK_ALIAS=github-runner-ubuntu-24-04 MOCK_DISK=19000 bash "$operation_script" publish "$standard_template_config"

mkdir "$workdir/missing-disk" "$workdir/invalid-disk"
sed '/^disk_size_mb[[:space:]]*=/d' "$standard_template_config" \
  >"$workdir/missing-disk/fixture.toml"
sed 's/^disk_size_mb[[:space:]]*=.*/disk_size_mb = "20480"/' \
  "$standard_template_config" \
  >"$workdir/invalid-disk/fixture.toml"
expect_failure 'template config has no valid disk_size_mb' \
  bash "$operation_script" publish "$workdir/missing-disk/fixture.toml"
expect_failure 'template config has no valid disk_size_mb' \
  bash "$operation_script" publish "$workdir/invalid-disk/fixture.toml"

write_catalog() {
  local standard_disk="$1"
  local large_disk="$2"
  jq -n --argjson standard_disk "$standard_disk" --argjson large_disk "$large_disk" '
    ["github-runner-ubuntu-slim", "github-runner-ubuntu-22-04",
     "github-runner-ubuntu-24-04", "github-runner-ubuntu-26-04",
     "github-runner-ubuntu-slim-large", "github-runner-ubuntu-22-04-large",
     "github-runner-ubuntu-24-04-large", "github-runner-ubuntu-26-04-large"] |
    map({names: [.], templateID: ., buildStatus: "ready", public: true,
         diskSizeMB: (if endswith("-large") then $large_disk else $standard_disk end)})
  ' >"$workdir/catalog.json"
}
export MOCK_CATALOG="$workdir/catalog.json"
export PATH="$workdir:$PATH"
write_catalog 22222 81920
bash "$repository_root/scripts/check-default-template-catalog.sh" >"$workdir/output"
write_catalog 22222 22222
expect_failure 'total disk size 22222 MiB is below disk_size_mb 81920 MiB' \
  bash "$repository_root/scripts/check-default-template-catalog.sh"
write_catalog 19000 83662
expect_failure 'total disk size 19000 MiB is below disk_size_mb 20480 MiB' \
  bash "$repository_root/scripts/check-default-template-catalog.sh"
write_catalog 22222 83662
bash "$repository_root/scripts/check-default-template-catalog.sh" >"$workdir/output"
test "$(wc -l <"$workdir/output" | tr -d '[:space:]')" = 8
jq '.[0].diskSizeMB = "999999"' "$workdir/catalog.json" >"$workdir/catalog-incorrect-type.json"
MOCK_CATALOG="$workdir/catalog-incorrect-type.json" \
  expect_failure 'invalid total disk size 999999 MiB' \
  bash "$repository_root/scripts/check-default-template-catalog.sh"

mkdir "$workdir/standard-smoke" "$workdir/standard-low-smoke" \
  "$workdir/large-smoke" "$workdir/large-low-smoke"
if ! MOCK_DISK_MIB=20480 RUNNER_SMOKE_OUTPUT_DIR="$workdir/standard-smoke" \
  bash "$repository_root/scripts/smoke-runner-template.sh" ubuntu-24.04 sb-fixture >"$workdir/output" 2>&1; then
  cat "$workdir/output" >&2
  jq '.results[] | select(.name == "runtime root disk size")' "$workdir/standard-smoke"/*.json >&2
  exit 1
fi
jq -e '.results | any(.name == "runtime root disk size" and .exit_status == 0)' \
  "$workdir/standard-smoke"/*.json >/dev/null
MOCK_DISK_MIB=20479 RUNNER_SMOKE_OUTPUT_DIR="$workdir/standard-low-smoke" \
  expect_failure 'conformance failed: ubuntu-24.04 / Release smoke / runtime root disk size' \
  bash "$repository_root/scripts/smoke-runner-template.sh" ubuntu-24.04 sb-fixture
MOCK_DISK_MIB=81920 RUNNER_SMOKE_OUTPUT_DIR="$workdir/large-smoke" \
  bash "$repository_root/scripts/smoke-runner-template.sh" ubuntu-24.04-large sb-fixture >"$workdir/output"
jq -e '.results | any(.name == "runtime root disk size" and .exit_status == 0)' \
  "$workdir/large-smoke"/*.json >/dev/null
MOCK_DISK_MIB=81919 RUNNER_SMOKE_OUTPUT_DIR="$workdir/large-low-smoke" \
  expect_failure 'conformance failed: ubuntu-24.04-large / Release smoke / runtime root disk size' \
  bash "$repository_root/scripts/smoke-runner-template.sh" ubuntu-24.04-large sb-fixture

# The checked-in compatibility manifest has no large-specific entry; retain
# its existing fallback when no augmented smoke manifest is supplied.
bash "$repository_root/scripts/run-runner-image-conformance.sh" \
  --image ubuntu-24.04-large --executor sandbox --target sb-fixture \
  --output "$workdir/large-fallback.json" >"$workdir/output"
jq -e '.passed == true and (.results | any(.name == "runtime root disk size") | not)' \
  "$workdir/large-fallback.json" >/dev/null

echo 'standard/large template total-disk and qshell version gates passed'
