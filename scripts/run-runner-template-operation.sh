#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -lt 2 ] || [ "$#" -gt 3 ]; then
  echo "usage: scripts/run-runner-template-operation.sh build|publish|unpublish TEMPLATE_CONFIG [BUILD_NAME]" >&2
  exit 64
fi

operation="$1"
template_config="$2"
build_name="${3:-}"
qshell_bin="${QSHELL:-qshell}"
script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
if [[ "$template_config" != /* ]]; then
  template_config="$script_root/$template_config"
fi

: "${QINIU_SANDBOX_API_URL:?QINIU_SANDBOX_API_URL is required}"
: "${QINIU_API_KEY:?QINIU_API_KEY is required}"
test -f "$template_config" || {
  echo "template config does not exist: $template_config" >&2
  exit 66
}
template_dir="$(dirname "$template_config")"
template_config="$template_dir/$(basename "$template_config")"
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

resolve_template_record() {
  local template_name="$1"
  local matches
  command -v jq >/dev/null 2>&1 || {
    echo "jq is required to resolve template $template_name" >&2
    return 69
  }
  matches="$(
    "$qshell_bin" sandbox template list --format json |
      jq -c --arg name "$template_name" '[ .[] | select((.Aliases // []) | index($name)) ]'
  )"
  case "$(jq 'length' <<<"$matches")" in
    0)
      echo "template $template_name is missing from the service catalog" >&2
      return 1
      ;;
    1)
      jq -c '.[0]' <<<"$matches"
      ;;
    *)
      echo "template $template_name is duplicated in the service catalog" >&2
      return 1
      ;;
  esac
}

check_template_disk() {
  local template_name="$1"
  local expected_size="$2"
  local template_record="$3"
  local disk_size
  disk_size="$(jq -r '.DiskSizeMB | tostring' <<<"$template_record")"
  if [[ ! "$disk_size" =~ ^[0-9]+$ ]]; then
    echo "template $template_name has invalid total disk size $disk_size MiB" >&2
    return 1
  fi
  if [ "$disk_size" -lt "$expected_size" ]; then
    echo "template $template_name total disk size $disk_size MiB is below disk_size_mb $expected_size MiB" >&2
    return 1
  fi
}

reconcile_build_status() {
  local template_id="$1"
  local build_id="$2"
  local timeout_seconds="${TEMPLATE_BUILD_RECONCILE_TIMEOUT_SECONDS:-300}"
  local interval_seconds="${TEMPLATE_BUILD_RECONCILE_INTERVAL_SECONDS:-15}"
  local deadline status=unknown build_output last_query_output=
  local query_failed=0

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
    if build_output="$("$qshell_bin" sandbox template builds "$template_id" "$build_id" 2>&1)"; then
      status="$(
        sed -nE 's/^Status:[[:space:]]+([^[:space:]]+).*/\1/p' <<<"$build_output" |
          head -n 1 |
          tr '[:upper:]' '[:lower:]'
      )"
      if [ -z "$status" ]; then
        status=unavailable
        query_failed=1
        last_query_output="$build_output"
      else
        query_failed=0
        last_query_output=
      fi
      case "$status" in
        ready | uploaded)
          echo "template build $build_id reached $status during reconciliation"
          return 0
          ;;
        error | failed)
          printf '%s\n' "$build_output" >&2
          echo "template build $build_id failed with status $status" >&2
          return 1
          ;;
      esac
    else
      status=unavailable
      query_failed=1
      last_query_output="$build_output"
    fi

    if ((SECONDS >= deadline)); then
      if [ "$query_failed" -eq 1 ]; then
        test -z "$last_query_output" || printf '%s\n' "$last_query_output" >&2
        echo "could not query template build $build_id after ${timeout_seconds}s of reconciliation" >&2
      else
        echo "template build $build_id remains $status after ${timeout_seconds}s of reconciliation" >&2
      fi
      printf 'inspect without starting another rebuild: %q sandbox template builds %q %q\n' \
        "$qshell_bin" "$template_id" "$build_id" >&2
      return 1
    fi
    sleep "$interval_seconds"
  done
}

expected_disk_size="$(sed -nE 's/^[[:space:]]*disk_size_mb[[:space:]]*=[[:space:]]*([0-9]+).*/\1/p' "$template_config" | head -n 1)"
test -n "$expected_disk_size" || {
  echo "template config has no valid disk_size_mb: $template_config" >&2
  exit 65
}
template_name="$(sed -nE 's/^[[:space:]]*name[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/p' "$template_config" | head -n 1)"
test -n "$template_name" || {
  echo "template config has no name: $template_config" >&2
  exit 65
}
if [ -n "$build_name" ]; then
  template_name="$build_name"
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
      cp "$template_config" "$tmp_config"
      build_args=(sandbox template build --wait --config "$tmp_config")
      if [ -n "$build_name" ]; then
        build_args+=(--name "$build_name")
      fi
      "$qshell_bin" "${build_args[@]}" 2>&1 | tee "$output_file"
    ) || build_command_status=$?
    if ! grep -Eq '^Status:[[:space:]]+ready[[:space:]]*$' "$output_file"; then
      template_id="$(sed -nE 's/^Template ID:[[:space:]]+([^[:space:]]+).*/\1/p' "$output_file" | tail -n 1)"
      build_id="$(sed -nE 's/^Build ID:[[:space:]]+([^[:space:]]+).*/\1/p' "$output_file" | tail -n 1)"
      if [ -z "$template_id" ] || [ -z "$build_id" ]; then
        echo "qshell did not report terminal Status: ready or a template/build ID (exit status $build_command_status)" >&2
        exit 1
      fi
      reconcile_build_status "$template_id" "$build_id"
    fi
    ;;
  publish | unpublish)
    test -z "$build_name" || {
      echo "BUILD_NAME is only valid for build" >&2
      exit 64
    }
    template_record="$(resolve_template_record "$template_name")"
    template_id="$(jq -r '.TemplateID // empty' <<<"$template_record")"
    test -n "$template_id" || {
      echo "template $template_name has no template ID in the service catalog" >&2
      exit 1
    }
    if [ "$operation" = publish ]; then
      check_template_disk "$template_name" "$expected_disk_size" "$template_record"
    fi
    (
      cd "$template_dir"
      "$qshell_bin" sandbox template "$operation" "$template_id" -y 2>&1 | tee "$output_file"
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
