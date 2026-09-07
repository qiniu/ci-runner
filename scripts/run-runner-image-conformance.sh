#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest_file="${RUNNER_IMAGES_MANIFEST:-$repository_root/templates/runner-images-compatibility.json}"
image_key=""
executor=""
target=""
output=""
sandbox_id=""
qshell_bin="${QSHELL:-qshell}"
sandbox_exec_timeout_seconds="${RUNNER_CONFORMANCE_COMMAND_TIMEOUT_SECONDS:-300}"
active_command_pid=""
active_watchdog_pid=""

usage() {
  cat >&2 <<'USAGE'
usage: scripts/run-runner-image-conformance.sh \
  --image ubuntu-24.04 \
  --executor docker|sandbox \
  --target <local-image-tag-or-template-id> \
  --output <result.json>
USAGE
  exit 64
}

while [ "$#" -gt 0 ]; do
  case "$1" in
    --image)
      image_key="${2:-}"
      shift 2
      ;;
    --executor)
      executor="${2:-}"
      shift 2
      ;;
    --target)
      target="${2:-}"
      shift 2
      ;;
    --output)
      output="${2:-}"
      shift 2
      ;;
    *)
      usage
      ;;
  esac
done

test -n "$image_key" && test -n "$executor" && test -n "$target" && test -n "$output" || usage
case "$executor" in
  docker | sandbox) ;;
  *) usage ;;
esac
case "$image_key" in
  ubuntu-slim-large) manifest_image_key=ubuntu-slim ;;
  ubuntu-22.04-large) manifest_image_key=ubuntu-22.04 ;;
  ubuntu-24.04-large) manifest_image_key=ubuntu-24.04 ;;
  ubuntu-26.04-large) manifest_image_key=ubuntu-26.04 ;;
  *) manifest_image_key="$image_key" ;;
esac
if ! [[ "$sandbox_exec_timeout_seconds" =~ ^[1-9][0-9]*$ ]]; then
  echo "RUNNER_CONFORMANCE_COMMAND_TIMEOUT_SECONDS must be a positive integer" >&2
  exit 64
fi
test -f "$manifest_file" || {
  echo "missing compatibility manifest $manifest_file" >&2
  exit 66
}
jq -e --arg image "$manifest_image_key" '.images[$image].entries | type == "array"' "$manifest_file" >/dev/null ||
  {
    echo "unknown image key $image_key" >&2
    exit 65
  }

invalid_exclusion="$(
  jq -r --arg image "$manifest_image_key" '
    .images[$image].entries[] |
    select(.status == "excluded") |
    select(
      ((.reason // "") | test("Qiniu Sandbox|Sandbox runtime|Sandbox template"; "i")) | not
    ) |
    "\(.category) / \(.upstream_name)"
  ' "$manifest_file"
)"
test -z "$invalid_exclusion" || {
  echo "excluded entry lacks a Sandbox-specific reason: $invalid_exclusion" >&2
  exit 65
}

mkdir -p "$(dirname "$output")"
workdir="$(mktemp -d)"
stdout_file="$workdir/stdout"
raw_stdout_file="$workdir/stdout.raw"
stderr_file="$workdir/stderr"
entries_file="$workdir/entries.tsv"
result_tmp="$workdir/result.json"
remote_start_marker="__QINIU_RUNNER_CONFORMANCE_REMOTE_STARTED__"
cleanup_stdout_file="$workdir/cleanup.stdout"
cleanup_stderr_file="$workdir/cleanup.stderr"
completion_message=""

cleanup() {
  original_status=$?
  trap - EXIT
  cleanup_failed=0
  if [ -n "$active_watchdog_pid" ]; then
    kill "$active_watchdog_pid" 2>/dev/null || true
  fi
  if [ -n "$active_command_pid" ]; then
    kill -TERM "$active_command_pid" 2>/dev/null || true
  fi
  if [ -n "$sandbox_id" ]; then
    : >"$cleanup_stdout_file"
    : >"$cleanup_stderr_file"
    if "$qshell_bin" sandbox kill "$sandbox_id" >"$cleanup_stdout_file" 2>"$cleanup_stderr_file"; then
      cleanup_exit_status=0
    else
      cleanup_exit_status=$?
    fi
    cleanup_passed=false
    cleanup_already_absent=false
    if grep -Fq "Killed sandbox $sandbox_id" "$cleanup_stdout_file"; then
      cleanup_passed=true
    elif {
      cat "$cleanup_stdout_file" "$cleanup_stderr_file"
    } | grep -Eiq '(^|[^[:digit:]])404([^[:digit:]]|$)|not[ -]?found|already (killed|terminated)'; then
      cleanup_passed=true
      cleanup_already_absent=true
    elif [ "$cleanup_exit_status" -eq 0 ]; then
      cleanup_exit_status=69
    fi

    if [ -s "$output" ] && jq empty "$output" >/dev/null 2>&1; then
      if ! jq \
        --arg sandbox_id "$sandbox_id" \
        --rawfile stdout "$cleanup_stdout_file" \
        --rawfile stderr "$cleanup_stderr_file" \
        --argjson exit_status "$cleanup_exit_status" \
        --argjson passed "$cleanup_passed" \
        --argjson already_absent "$cleanup_already_absent" \
        '.cleanup = {
          attempted: true,
          sandbox_id: $sandbox_id,
          passed: $passed,
          already_absent: $already_absent,
          stdout: ($stdout | sub("\n$"; "")),
          stderr: ($stderr | sub("\n$"; "")),
          exit_status: $exit_status
        } |
        if $passed then . else .passed = false end' \
        "$output" >"$result_tmp"; then
        cleanup_failed=1
      elif ! mv "$result_tmp" "$output"; then
        cleanup_failed=1
      fi
    elif [ "$cleanup_passed" != true ]; then
      cleanup_failed=1
    fi
    if [ "$cleanup_passed" != true ]; then
      echo "could not confirm cleanup of Sandbox $sandbox_id" >&2
      cleanup_failed=1
    fi
  fi
  find "$workdir" -type f -delete 2>/dev/null || true
  rmdir "$workdir" 2>/dev/null || true
  if [ "$cleanup_failed" -ne 0 ] && [ "$original_status" -eq 0 ]; then
    original_status=1
  fi
  if [ "$original_status" -eq 0 ] && [ -n "$completion_message" ]; then
    echo "$completion_message"
  fi
  exit "$original_status"
}
trap cleanup EXIT

run_with_timeout() {
  local timeout_seconds="$1"
  local timeout_marker="$workdir/command.timeout"
  local exit_status
  shift
  rm -f "$timeout_marker"
  "$@" &
  active_command_pid=$!
  (
    sleep "$timeout_seconds"
    if kill -0 "$active_command_pid" 2>/dev/null; then
      : >"$timeout_marker"
      kill -TERM "$active_command_pid" 2>/dev/null || true
      sleep 2
      kill -KILL "$active_command_pid" 2>/dev/null || true
    fi
  ) &
  active_watchdog_pid=$!
  wait "$active_command_pid"
  exit_status=$?
  kill "$active_watchdog_pid" 2>/dev/null || true
  wait "$active_watchdog_pid" 2>/dev/null || true
  active_command_pid=""
  active_watchdog_pid=""
  if [ -f "$timeout_marker" ]; then
    return 124
  fi
  return "$exit_status"
}

if [ "$executor" = sandbox ]; then
  : "${QINIU_SANDBOX_API_URL:?QINIU_SANDBOX_API_URL is required}"
  : "${QINIU_API_KEY:?QINIU_API_KEY is required}"
  command -v "$qshell_bin" >/dev/null 2>&1 || {
    echo "qshell executable not found: $qshell_bin" >&2
    exit 69
  }
  create_output="$("$qshell_bin" sandbox create "$target" --timeout 1800 --detach)"
  sandbox_id="$(
    {
      jq -r '.sandbox_id // .sandboxID // .id // empty' <<<"$create_output" 2>/dev/null || true
      sed -nE 's/^Sandbox ID:[[:space:]]*([^[:space:]]+)[[:space:]]*$/\1/p' <<<"$create_output"
      grep -Eo 'sb-[A-Za-z0-9_-]+' <<<"$create_output" | head -n 1 || true
    } | awk 'NF {print; exit}'
  )"
  test -n "$sandbox_id" || {
    echo "could not determine Sandbox ID from qshell output: $create_output" >&2
    exit 69
  }
fi

jq -r --arg image "$manifest_image_key" '
  .images[$image].entries[] |
  select(.status == "provided") |
  [
    (.category // ""),
    (.upstream_name // ""),
    (.verification // "")
  ] |
  @tsv
' "$manifest_file" >"$entries_file"

jq -n \
  --arg image "$image_key" \
  --arg executor "$executor" \
  --arg target "$target" \
  --arg started_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '{
    image: $image,
    executor: $executor,
    target: $target,
    started_at: $started_at,
    completed: false,
    passed: false,
    results: []
  }' >"$output"

failed=0
while IFS=$'\t' read -r category upstream_name verification; do
  test -n "$verification" || {
    echo "provided entry has no verification command: $category / $upstream_name" >&2
    failed=1
    break
  }
  : >"$stdout_file"
  : >"$raw_stdout_file"
  : >"$stderr_file"
  execution_command='set -a; if [ -r /etc/environment ]; then . /etc/environment; fi; set +a; '"$verification"
  set +e
  if [ "$executor" = docker ]; then
    docker run --rm --privileged "$target" bash -lc "$execution_command" >"$stdout_file" 2>"$stderr_file"
    exit_status=$?
  else
    execution_command='printf "__QINIU_RUNNER_CONFORMANCE_REMOTE_STARTED__\n"; '"$execution_command"
    run_with_timeout "$sandbox_exec_timeout_seconds" \
      "$qshell_bin" sandbox exec "$sandbox_id" -u runner -- bash -lc "$execution_command" \
      </dev/null >"$raw_stdout_file" 2>"$stderr_file"
    exit_status=$?
    if [ "$exit_status" -eq 124 ]; then
      printf 'qshell sandbox exec timed out after %s seconds\n' \
        "$sandbox_exec_timeout_seconds" >>"$stderr_file"
    fi
    if [ "$(sed -n '1p' "$raw_stdout_file")" = "$remote_start_marker" ]; then
      tail -n +2 "$raw_stdout_file" >"$stdout_file"
    else
      cp "$raw_stdout_file" "$stdout_file"
      if [ "$exit_status" -eq 0 ]; then
        exit_status=69
        printf '%s\n' \
          "qshell sandbox exec returned success without starting the remote command" \
          >>"$stderr_file"
      fi
    fi
  fi
  set -e

  jq \
    --arg category "$category" \
    --arg name "$upstream_name" \
    --arg command "$verification" \
    --rawfile stdout "$stdout_file" \
    --rawfile stderr "$stderr_file" \
    --argjson exit_status "$exit_status" \
    '.results += [{
      category: $category,
      name: $name,
      command: $command,
      stdout: ($stdout | sub("\n$"; "")),
      stderr: ($stderr | sub("\n$"; "")),
      exit_status: $exit_status
    }] |
    if $exit_status == 0 then . else .passed = false end' \
    "$output" >"$result_tmp"
  mv "$result_tmp" "$output"

  if [ "$exit_status" -ne 0 ]; then
    echo "conformance failed: $image_key / $category / $upstream_name (exit $exit_status)" >&2
    failed=1
    break
  fi
done <"$entries_file"

overall_passed=true
if [ "$failed" -ne 0 ]; then
  overall_passed=false
fi
jq \
  --arg finished_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --argjson passed "$overall_passed" \
  '.finished_at = $finished_at | .completed = true | .passed = $passed' \
  "$output" >"$result_tmp"
mv "$result_tmp" "$output"

if [ "$failed" -ne 0 ]; then
  exit 1
fi
completion_message="runner image conformance passed: $output"
exit 0
