#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: scripts/run-runner-template-operation.sh build|publish|unpublish TEMPLATE_DIR [BUILD_NAME]" >&2
  exit 64
fi

operation="$1"
template_dir="$2"
build_name="${3:-}"
qshell_bin="${QSHELL:-qshell}"
script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [[ "$template_dir" != /* ]]; then
  template_dir="$script_root/$template_dir"
fi

: "${QINIU_SANDBOX_API_URL:?QINIU_SANDBOX_API_URL is required}"
: "${QINIU_API_KEY:?QINIU_API_KEY is required}"
test -d "$template_dir" || {
  echo "template directory does not exist: $template_dir" >&2
  exit 66
}
test -f "$template_dir/qshell.sandbox.toml" || {
  echo "template config does not exist: $template_dir/qshell.sandbox.toml" >&2
  exit 66
}
command -v "$qshell_bin" >/dev/null 2>&1 || {
  echo "qshell executable not found: $qshell_bin" >&2
  exit 69
}

qshell_version="$("$qshell_bin" version 2>&1 | sed -nE 's/^v([0-9]+\.[0-9]+\.[0-9]+).*$/\1/p' | head -n 1)"
test -n "$qshell_version" || {
  echo "could not determine qshell version" >&2
  exit 69
}
minimum_qshell_version=2.19.10
if [ "$operation" = build ]; then
  minimum_qshell_version=2.19.13
fi
if ! awk -v actual="$qshell_version" -v minimum="$minimum_qshell_version" '
  BEGIN {
    split(actual, a, ".")
    split(minimum, m, ".")
    for (i = 1; i <= 3; i++) {
      if ((a[i] + 0) > (m[i] + 0)) exit 0
      if ((a[i] + 0) < (m[i] + 0)) exit 1
    }
    exit 0
  }
'; then
  echo "qshell >= $minimum_qshell_version is required; found v$qshell_version" >&2
  exit 69
fi

check_template_disk() {
  local template_name="$1"
  local expected_size="$2"
  local allow_missing="$3"
  local disk_size
  command -v jq >/dev/null 2>&1 || {
    echo "jq is required to verify template disk size" >&2
    exit 69
  }
  disk_size="$(
    "$qshell_bin" sandbox template list --format json |
      jq -r --arg name "$template_name" '
        [ .[] | select((.Aliases // []) | index($name)) ] |
        if length == 0 then "missing"
        elif length == 1 then (.[0].DiskSizeMB | tostring)
        else "duplicate"
        end
      '
  )"
  if [ "$disk_size" = missing ] && [ "$allow_missing" = true ]; then
    return
  fi
  if [[ ! "$disk_size" =~ ^[0-9]+$ ]]; then
    echo "template $template_name has invalid total disk size $disk_size MiB" >&2
    exit 1
  fi
  if [ "$disk_size" -lt "$expected_size" ]; then
    echo "template $template_name total disk size $disk_size MiB is below the requested $expected_size MiB of build free space" >&2
    exit 1
  fi
}

reconcile_build_status() {
  local template_name="$1"
  local build_id="$2"
  local timeout_seconds="${TEMPLATE_BUILD_RECONCILE_TIMEOUT_SECONDS:-300}"
  local interval_seconds="${TEMPLATE_BUILD_RECONCILE_INTERVAL_SECONDS:-5}"
  local deadline status catalog_json

  command -v jq >/dev/null 2>&1 || {
    echo "jq is required to reconcile template build status" >&2
    return 1
  }
  [[ "$timeout_seconds" =~ ^[0-9]+$ ]] || {
    echo "TEMPLATE_BUILD_RECONCILE_TIMEOUT_SECONDS must be a non-negative integer" >&2
    return 1
  }
  [[ "$interval_seconds" =~ ^[1-9][0-9]*$ ]] || {
    echo "TEMPLATE_BUILD_RECONCILE_INTERVAL_SECONDS must be a positive integer" >&2
    return 1
  }

  deadline=$((SECONDS + timeout_seconds))
  while true; do
    if catalog_json="$("$qshell_bin" sandbox template list --format json)"; then
      status="$(
        jq -r --arg name "$template_name" --arg build_id "$build_id" '
          [ .[] |
            select(((.Aliases // []) | index($name)) and .BuildID == $build_id)
          ] |
          if length == 1 then (.[0].BuildStatus // "unknown") else "unknown" end
        ' <<<"$catalog_json"
      )"
      case "$status" in
        ready | uploaded)
          echo "service catalog reports build $build_id as $status"
          return 0
          ;;
        error | failed)
          echo "service catalog reports build $build_id as $status" >&2
          return 1
          ;;
      esac
    fi

    if ((SECONDS >= deadline)); then
      echo "qshell did not report terminal Status: ready and service catalog did not confirm build $build_id within ${timeout_seconds}s" >&2
      return 1
    fi
    sleep "$interval_seconds"
  done
}

expected_disk_size="$(sed -nE 's/^[[:space:]]*disk_size_mb[[:space:]]*=[[:space:]]*([0-9]+).*/\1/p' "$template_dir/qshell.sandbox.toml" | head -n 1)"
test -n "$expected_disk_size" || {
  echo "template config has no valid disk_size_mb: $template_dir/qshell.sandbox.toml" >&2
  exit 65
}
template_name="$(sed -nE 's/^[[:space:]]*name[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/p' "$template_dir/qshell.sandbox.toml" | head -n 1)"
test -n "$template_name" || {
  echo "template config has no name: $template_dir/qshell.sandbox.toml" >&2
  exit 65
}
if [ -n "$build_name" ]; then
  template_name="$build_name"
fi

# A named standard development build can keep using its existing template.
# The API reports total rootfs size, while the config requests free build space.
check_disk_size=false
if [ -z "$build_name" ] || [[ "$(basename "$template_dir")" == *-large ]]; then
  check_disk_size=true
fi

output_file="$(mktemp)"
cleanup() {
  rm -f "$output_file"
}
trap cleanup EXIT

case "$operation" in
  build)
    bash "$script_root/scripts/prepare-runner-archive.sh"
    build_command_status=0
    (
      cd "$template_dir"
      tmp_config="$(mktemp .qshell-sandbox.XXXXXX)"
      trap 'rm -f "$tmp_config"' EXIT
      cp qshell.sandbox.toml "$tmp_config"
      build_args=(sandbox template build --wait --config "$tmp_config")
      if [ -n "$build_name" ]; then
        build_args+=(--name "$build_name")
      fi
      "$qshell_bin" "${build_args[@]}" 2>&1 | tee "$output_file"
    ) || build_command_status=$?
    if ! grep -Eq '^Status:[[:space:]]+ready[[:space:]]*$' "$output_file"; then
      build_id="$(sed -nE 's/^Build ID:[[:space:]]+([^[:space:]]+).*/\1/p' "$output_file" | tail -n 1)"
      test -n "$build_id" || {
        echo "qshell did not report terminal Status: ready or a build ID (exit status $build_command_status)" >&2
        exit 1
      }
      reconcile_build_status "$template_name" "$build_id"
    fi
    ;;
  publish | unpublish)
    test -z "$build_name" || {
      echo "BUILD_NAME is only valid for build" >&2
      exit 64
    }
    if [ "$operation" = publish ] && [ "$check_disk_size" = true ]; then
      check_template_disk "$template_name" "$expected_disk_size" false
    fi
    (
      cd "$template_dir"
      "$qshell_bin" sandbox template "$operation" -y 2>&1 | tee "$output_file"
    )
    grep -Eq "^Template .+ ${operation/publish/published}$" "$output_file" || {
      if [ "$operation" = unpublish ]; then
        grep -Eq '^Template .+ unpublished$' "$output_file" && exit 0
      fi
      echo "qshell did not confirm template $operation" >&2
      exit 1
    }
    ;;
  *)
    echo "unknown template operation: $operation" >&2
    exit 64
    ;;
esac
