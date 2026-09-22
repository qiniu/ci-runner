#!/usr/bin/env bash
set -euo pipefail

runner_user="${RUNNER_USER:-runner}"
actions_runner_root="${ACTIONS_RUNNER_ROOT:-/opt/actions-runner}"
workdir="${RUNNER_WORKDIR:-/home/runner/actions-runner}"
runner_job_work="${RUNNER_JOB_WORK:-/home/runner/work}"
hook_root="${RUNNER_HOOK_ROOT:-/home/runner/_runnerd-hooks}"
ensure_docker="${ENSURE_DOCKER:-/usr/local/bin/ensure-docker}"
require_docker="%[8]s"
runner_environment_file="${RUNNER_ENVIRONMENT_FILE:-/etc/environment}"
export HOME="${RUNNER_HOME:-/home/runner}"
export XDG_CONFIG_HOME="${HOME}/.config"
export GOPATH="${GOPATH:-${HOME}/go}"
export GOBIN="${GOBIN:-${GOPATH}/bin}"
export RUNNER_TOOL_CACHE="${RUNNER_TOOL_CACHE:-/opt/hostedtoolcache}"
export AGENT_TOOLSDIRECTORY="${AGENT_TOOLSDIRECTORY:-/opt/hostedtoolcache}"
export PATH="/usr/local/go/bin:${GOPATH}/bin:/usr/local/bin:${PATH}"
if [ -r "$runner_environment_file" ]; then
  set -a
  # runner-images writes shell-compatible KEY="value" entries here.
  # The environment can contain references to build-only variables. Keep
  # nounset disabled while loading it so those entries do not abort startup.
  set +u
  # shellcheck disable=SC1090
  . "$runner_environment_file"
  set -u
  set +a
fi

if [ "$(id -u)" -eq 0 ] && id -u "$runner_user" >/dev/null 2>&1 && [ "${RUNNERD_AS_RUNNER:-}" != 1 ]; then
  install -d -o "$runner_user" -g "$runner_user" \
    "$workdir" "$runner_job_work" "$HOME" "$XDG_CONFIG_HOME/git" "$hook_root"
  exec sudo -E -u "$runner_user" \
    RUNNERD_AS_RUNNER=1 \
    RUNNER_USER="$runner_user" \
    ACTIONS_RUNNER_ROOT="$actions_runner_root" \
    RUNNER_WORKDIR="$workdir" \
    RUNNER_JOB_WORK="$runner_job_work" \
    RUNNER_HOOK_ROOT="$hook_root" \
    ENSURE_DOCKER="$ensure_docker" \
    RUNNER_ENVIRONMENT_FILE="$runner_environment_file" \
    HOME="$HOME" \
    bash "$0"
fi
if [ "$(id -u)" -eq 0 ]; then
  export RUNNER_ALLOW_RUNASROOT=1
fi

mkdir -p "$workdir" "$runner_job_work" "$HOME" "$XDG_CONFIG_HOME/git" "$hook_root"
cd "$workdir"

if [ ! -x "$actions_runner_root/config.sh" ]; then
  echo "missing preinstalled GitHub Actions runner at $actions_runner_root/config.sh" >&2
  echo "build one of the managed GitHub runner templates before starting runners" >&2
  exit 1
fi

if [ ! -x ./config.sh ]; then
  echo "copying preinstalled GitHub Actions runner"
  cp -a "$actions_runner_root"/. "$workdir"/
fi

runner_applications_manifest="$(printf '%%s' "%[17]s" | base64 -d)"
if [ -n "$runner_applications_manifest" ]; then
  case "$(uname -m)" in
    x86_64) runner_architecture="x64" ;;
    aarch64|arm64) runner_architecture="arm64" ;;
    armv7l|armv8l) runner_architecture="arm" ;;
    *)
      echo "unsupported architecture for GitHub Actions runner preflight update: $(uname -m)" >&2
      exit 1
      ;;
  esac

  runner_target_version=""
  runner_download_url=""
  runner_sha256_checksum=""
  while IFS=$'\t' read -r application_architecture application_version application_url application_checksum; do
    if [ "$application_architecture" = "$runner_architecture" ]; then
      runner_target_version="$application_version"
      runner_download_url="$application_url"
      runner_sha256_checksum="$application_checksum"
      break
    fi
  done <<<"$runner_applications_manifest"
  if [ -z "$runner_target_version" ] || [ -z "$runner_download_url" ] || [ -z "$runner_sha256_checksum" ]; then
    echo "GitHub Actions runner preflight update has no application for architecture $runner_architecture" >&2
    exit 1
  fi

  normalize_runner_version_component() {
    local component="$1"
    while [ "${#component}" -gt 1 ] && [ "${component#0}" != "$component" ]; do
      component="${component#0}"
    done
    printf '%%s' "$component"
  }
  runner_version_at_least() {
    local current="$1"
    local target="$2"
    local current_component target_component index
    local -a current_components target_components
    [[ "$current" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || return 1
    IFS=. read -r -a current_components <<<"$current"
    IFS=. read -r -a target_components <<<"$target"
    for index in 0 1 2; do
      current_component="$(normalize_runner_version_component "${current_components[$index]}")"
      target_component="$(normalize_runner_version_component "${target_components[$index]}")"
      if [ "${#current_component}" -gt "${#target_component}" ]; then
        return 0
      fi
      if [ "${#current_component}" -lt "${#target_component}" ]; then
        return 1
      fi
      if [[ "$current_component" > "$target_component" ]]; then
        return 0
      fi
      if [[ "$current_component" < "$target_component" ]]; then
        return 1
      fi
    done
    return 0
  }

  current_runner_version="$("$workdir/bin/Runner.Listener" --version 2>/dev/null || true)"
  if [ "$current_runner_version" = "$runner_target_version" ]; then
    echo "GitHub Actions runner $runner_target_version is already installed"
  elif runner_version_at_least "$current_runner_version" "$runner_target_version"; then
    echo "GitHub Actions runner $current_runner_version is newer than target $runner_target_version; keeping installed version"
  else
    for runner_update_tool in curl tar sha256sum mktemp timeout; do
      if ! command -v "$runner_update_tool" >/dev/null 2>&1; then
        echo "missing required GitHub Actions runner update tool: $runner_update_tool" >&2
        exit 1
      fi
    done

    runner_update_root="$(mktemp -d "${workdir}.update.XXXXXX")"
    runner_update_archive="$runner_update_root/runner.tar.gz"
    runner_update_candidate="$runner_update_root/candidate"
    runner_previous_workdir=""
    cleanup_runner_update() {
      if [ -n "${runner_previous_workdir:-}" ] && [ -e "$runner_previous_workdir" ]; then
        if [ ! -e "$workdir" ]; then
          if ! mv "$runner_previous_workdir" "$workdir"; then
            echo "GitHub Actions runner work directory rollback failed: $runner_previous_workdir" >&2
            # The previous Runner is the last recoverable copy. Keep the
            # random update root instead of deleting it below.
            runner_update_root=""
          fi
        else
          rm -rf "$runner_previous_workdir"
        fi
      fi
      if [ -n "${runner_update_root:-}" ]; then
        rm -rf "$runner_update_root"
      fi
    }
    interrupt_runner_update() {
      exit 1
    }
    trap cleanup_runner_update EXIT
    trap interrupt_runner_update HUP INT TERM
    mkdir -p "$runner_update_candidate"
    echo "downloading GitHub Actions runner $runner_target_version for $runner_architecture"
    if ! (
      # Bash expresses RLIMIT_FSIZE in KiB. Keep the archive bounded even when
      # an older curl cannot apply --max-filesize without Content-Length. The
      # outer timeout caps all curl retries, including the final active transfer.
      ulimit -f 524288
      timeout --signal=KILL 300s curl \
        --fail \
        --location \
        --retry 3 \
        --retry-delay 1 \
        --retry-max-time 300 \
        --connect-timeout 10 \
        --max-time 300 \
        --max-filesize 536870912 \
        --output "$runner_update_archive" \
        "$runner_download_url"
    ); then
      echo "GitHub Actions runner download failed" >&2
      exit 1
    fi
    if ! printf '%%s  %%s\n' "$runner_sha256_checksum" "$runner_update_archive" | sha256sum -c - >/dev/null 2>&1; then
      echo "GitHub Actions runner archive checksum verification failed" >&2
      exit 1
    fi
    if ! tar -xzf "$runner_update_archive" -C "$runner_update_candidate"; then
      echo "GitHub Actions runner archive extraction failed" >&2
      exit 1
    fi
    candidate_runner_version="$("$runner_update_candidate/bin/Runner.Listener" --version 2>/dev/null || true)"
    if [ "$candidate_runner_version" != "$runner_target_version" ]; then
      echo "GitHub Actions runner archive version verification failed: got ${candidate_runner_version:-unknown}, want $runner_target_version" >&2
      exit 1
    fi

    runner_previous_workdir="$runner_update_root/previous"
    cd "$(dirname "$workdir")"
    # The EXIT trap restores this backup if the candidate move fails or a
    # catchable signal arrives between the two moves.
    if ! mv "$workdir" "$runner_previous_workdir"; then
      echo "GitHub Actions runner work directory replacement failed" >&2
      exit 1
    fi
    if ! mv "$runner_update_candidate" "$workdir"; then
      echo "GitHub Actions runner work directory replacement failed" >&2
      exit 1
    fi
    cleanup_runner_update
    runner_previous_workdir=""
    runner_update_root=""
    trap - EXIT HUP INT TERM
    cd "$workdir"
    echo "updated GitHub Actions runner from ${current_runner_version:-unknown} to $runner_target_version"
  fi
fi

if [ ! -x "$ensure_docker" ]; then
  if [ "$require_docker" = 1 ]; then
    echo "missing required Docker bootstrap helper at $ensure_docker" >&2
    exit 1
  fi
  echo "Docker bootstrap helper is unavailable; continuing without Docker" >&2
else
  echo "checking Docker daemon"
  if ! "$ensure_docker"; then
    if [ "$require_docker" = 1 ]; then
      echo "Docker daemon is required for this managed runner" >&2
      exit 1
    fi
    echo "Docker daemon is unavailable; continuing without Docker" >&2
  fi
fi

runner_url="$(printf '%%s' "%[1]s" | base64 -d)"
registration_token="$(printf '%%s' "%[2]s" | base64 -d)"
runner_name="$(printf '%%s' "%[3]s" | base64 -d)"
runner_labels="$(printf '%%s' "%[4]s" | base64 -d)"
runner_group="$(printf '%%s' "%[5]s" | base64 -d)"
runner_request_id="$(printf '%%s' "%[6]s" | base64 -d)"
sandbox_id="$(printf '%%s' "%[7]s" | base64 -d)"
cache_s3_region="$(printf '%%s' "%[9]s" | base64 -d)"
cache_s3_bucket="$(printf '%%s' "%[10]s" | base64 -d)"
cache_s3_endpoint="$(printf '%%s' "%[11]s" | base64 -d)"
cache_s3_read_prefixes="$(printf '%%s' "%[12]s" | base64 -d)"
cache_s3_write_prefix="$(printf '%%s' "%[13]s" | base64 -d)"
cache_s3_access_key="$(printf '%%s' "%[14]s" | base64 -d)"
cache_s3_secret_key="$(printf '%%s' "%[15]s" | base64 -d)"
cache_s3_session_token="$(printf '%%s' "%[16]s" | base64 -d)"

# Inject Cache S3 STS credentials for qiniu/actions-cache
if [ -n "$cache_s3_bucket" ] && [ -n "$cache_s3_access_key" ] && [ -n "$cache_s3_secret_key" ]; then
  export RUNS_ON_S3_BUCKET_CACHE="$cache_s3_bucket"
  if [ -n "$cache_s3_endpoint" ]; then
    export RUNS_ON_S3_BUCKET_ENDPOINT="$cache_s3_endpoint"
  fi
  export RUNS_ON_AWS_REGION="$cache_s3_region"
  export RUNS_ON_S3_FORCE_PATH_STYLE="true"
  # qiniu/actions-cache reads these dedicated variables into an explicit S3
  # client provider, so workflow AWS credential changes cannot replace them.
  export RUNS_ON_S3_ACCESS_KEY_ID="$cache_s3_access_key"
  export RUNS_ON_S3_SECRET_ACCESS_KEY="$cache_s3_secret_key"
  if [ -n "$cache_s3_session_token" ]; then
    export RUNS_ON_S3_SESSION_TOKEN="$cache_s3_session_token"
  fi
  # Keep AWS-compatible names for SDKs used by workflow steps. The cache action
  # snapshots the runnerd-specific names into an explicit S3 client provider.
  export AWS_ACCESS_KEY_ID="$cache_s3_access_key"
  export AWS_SECRET_ACCESS_KEY="$cache_s3_secret_key"
  if [ -n "$cache_s3_session_token" ]; then
    export AWS_SESSION_TOKEN="$cache_s3_session_token"
  fi
  if [ -n "$cache_s3_read_prefixes" ]; then
    export RUNS_ON_S3_CACHE_READ_PREFIXES="$cache_s3_read_prefixes"
  else
    echo "cache S3 read scopes are missing; restore is disabled" >&2
  fi
  if [ -n "$cache_s3_write_prefix" ]; then
    export RUNS_ON_S3_CACHE_WRITE_PREFIX="$cache_s3_write_prefix"
  else
    echo "cache S3 write scope is missing; save is disabled" >&2
  fi
  # Tune qiniu/actions-cache upload/download concurrency for better throughput.
  export UPLOAD_QUEUE_SIZE="${UPLOAD_QUEUE_SIZE:-16}"
  export UPLOAD_PART_SIZE="${UPLOAD_PART_SIZE:-16}"
  export DOWNLOAD_QUEUE_SIZE="${DOWNLOAD_QUEUE_SIZE:-16}"
  export DOWNLOAD_PART_SIZE="${DOWNLOAD_PART_SIZE:-16}"
  echo "injected cache S3 STS credentials for qiniu/actions-cache (upload_queue=${UPLOAD_QUEUE_SIZE} upload_part=${UPLOAD_PART_SIZE}MiB download_queue=${DOWNLOAD_QUEUE_SIZE} download_part=${DOWNLOAD_PART_SIZE}MiB)"
elif [ -n "$cache_s3_bucket" ]; then
  echo "cache S3 configuration is incomplete; skipping credential injection" >&2
fi
export RUNNERD_SANDBOX_ID="$sandbox_id"
export RUNNERD_REQUEST_ID="$runner_request_id"
export RUNNERD_RUNNER_NAME="$runner_name"
export RUNNERD_RUNNER_LISTENER="$workdir/bin/Runner.Listener"
hook_signal_path="$hook_root/job-started.signal"
hook_signal_fallback_path="$hook_root/job-started.signal.fallback"
hook_signal_pid=""
hook_signal_mode=""
cleanup_hook_signal() {
  if [ -n "$hook_signal_pid" ]; then
    if [ "$hook_signal_mode" = fifo ] && [ -p "$hook_signal_path" ]; then
      kill "$hook_signal_pid" 2>/dev/null || true
    elif [ "$hook_signal_mode" = fallback ] && [ ! -f "$hook_signal_fallback_path" ]; then
      kill "$hook_signal_pid" 2>/dev/null || true
    fi
    wait "$hook_signal_pid" 2>/dev/null || true
  fi
  rm -f "$hook_signal_path" "$hook_signal_fallback_path"
}
trap cleanup_hook_signal EXIT
if rm -f "$hook_signal_path" "$hook_signal_fallback_path" && mkfifo -m 600 "$hook_signal_path"; then
  cat "$hook_signal_path" &
  hook_signal_pid="$!"
  hook_signal_mode="fifo"
  export RUNNERD_HOOK_SIGNAL_PATH="$hook_signal_path"
  unset RUNNERD_HOOK_SIGNAL_FALLBACK_PATH
else
  unset RUNNERD_HOOK_SIGNAL_PATH
  (
    while [ ! -f "$hook_signal_fallback_path" ]; do
      sleep 0.05
    done
    cat "$hook_signal_fallback_path"
    rm -f "$hook_signal_fallback_path"
  ) &
  hook_signal_pid="$!"
  hook_signal_mode="fallback"
  export RUNNERD_HOOK_SIGNAL_FALLBACK_PATH="$hook_signal_fallback_path"
  echo "effective Runner version channel is unavailable; using job-start marker fallback" >&2
fi
cat >"$hook_root/job-started.sh" <<'HOOK'
#!/usr/bin/env bash
effective_runner_version="$("$RUNNERD_RUNNER_LISTENER" --version 2>/dev/null || true)"
if [ -n "${RUNNERD_HOOK_SIGNAL_PATH:-}" ] && [ -p "$RUNNERD_HOOK_SIGNAL_PATH" ]; then
  {
    if [ -n "$effective_runner_version" ] && \
      [ "${#effective_runner_version}" -le 256 ] && \
      [[ "$effective_runner_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
      printf 'RUNNERD_EFFECTIVE_RUNNER_VERSION=%%s\n' "$effective_runner_version"
    fi
    echo "RUNNERD_JOB_STARTED"
  } >"$RUNNERD_HOOK_SIGNAL_PATH"
  rm -f "$RUNNERD_HOOK_SIGNAL_PATH"
elif [ -n "${RUNNERD_HOOK_SIGNAL_FALLBACK_PATH:-}" ]; then
  fallback_tmp="${RUNNERD_HOOK_SIGNAL_FALLBACK_PATH}.tmp.$$"
  umask 077
  if printf 'RUNNERD_JOB_STARTED\n' >"$fallback_tmp"; then
    mv -f "$fallback_tmp" "$RUNNERD_HOOK_SIGNAL_FALLBACK_PATH"
  else
    rm -f "$fallback_tmp"
  fi
fi
echo "::notice title=Qiniu sandbox::sandbox_id=${RUNNERD_SANDBOX_ID} runner_request_id=${RUNNERD_REQUEST_ID} runner_name=${RUNNERD_RUNNER_NAME}"
echo "Qiniu sandbox id: ${RUNNERD_SANDBOX_ID}"
echo "Runner request id: ${RUNNERD_REQUEST_ID}"
echo "Runner name: ${RUNNERD_RUNNER_NAME}"
HOOK
cat >"$hook_root/job-completed.sh" <<'HOOK'
#!/usr/bin/env bash
echo "RUNNERD_JOB_COMPLETED"
HOOK
chmod +x "$hook_root/job-started.sh" "$hook_root/job-completed.sh"
export ACTIONS_RUNNER_HOOK_JOB_STARTED="$hook_root/job-started.sh"
export ACTIONS_RUNNER_HOOK_JOB_COMPLETED="$hook_root/job-completed.sh"

config_args=(--url "$runner_url" --token "$registration_token" --name "$runner_name" --labels "$runner_labels" --work "$runner_job_work" --ephemeral --unattended --replace)
if [ -n "$runner_group" ]; then
  config_args+=(--runnergroup "$runner_group")
fi

echo "configuring GitHub Actions runner ${runner_name}"
retries_left=10
while [ "$retries_left" -gt 0 ]; do
  if ./config.sh "${config_args[@]}"; then
    break
  fi
  retries_left=$((retries_left - 1))
  if [ "$retries_left" -eq 0 ]; then
    echo "GitHub Actions runner configuration failed" >&2
    exit 2
  fi
  echo "GitHub Actions runner configuration failed, retrying"
  sleep 1
done
cleanup() {
  cleanup_hook_signal
  ./config.sh remove --token "$registration_token" || true
}
trap cleanup EXIT
echo "starting GitHub Actions runner"
./run.sh
