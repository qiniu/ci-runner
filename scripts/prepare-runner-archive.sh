#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pin_file="${1:-$repository_root/templates/common/actions-runner.env}"
chunks_dir="${2:-$repository_root/templates/common/.build/actions-runner}"
archive_dir="${3:-$repository_root/.build/runner-archives}"
chunk_size="${RUNNER_ARCHIVE_CHUNK_SIZE:-16777216}"
chunk_slots=16
for required_command in curl sha256sum split; do
  command -v "$required_command" >/dev/null 2>&1 || {
    echo "required Runner archive tool is missing: $required_command" >&2
    exit 69
  }
done

# shellcheck source=/dev/null
source "$pin_file"
: "${RUNNER_VERSION:?RUNNER_VERSION is required}"
: "${RUNNER_ARCHIVE_SHA256:?RUNNER_ARCHIVE_SHA256 is required}"
: "${RUNNER_ARCHIVE_SIZE:?RUNNER_ARCHIVE_SIZE is required}"
[[ "$RUNNER_ARCHIVE_SIZE" =~ ^[0-9]+$ ]] && [ "$RUNNER_ARCHIVE_SIZE" -gt 0 ] || {
  echo 'invalid Runner archive size' >&2
  exit 64
}
[[ "$chunk_size" =~ ^[0-9]+$ ]] && [ "$chunk_size" -gt 0 ] || {
  echo 'invalid Runner archive chunk size' >&2
  exit 64
}
[ "$RUNNER_ARCHIVE_SIZE" -le $((chunk_slots * chunk_size)) ] || {
  echo 'Runner archive exceeds the available COPY chunks' >&2
  exit 64
}

archive_url="${RUNNER_ARCHIVE_URL:-https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz}"
archive_file="$archive_dir/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz"
partial_file="${archive_file}.partial"
install -d -m 0755 "$archive_dir" "$(dirname "$chunks_dir")"
lock_dir="${chunks_dir}.prepare-lock"
lock_wait=0
while ! mkdir "$lock_dir" 2>/dev/null; do
  if [ "$lock_wait" -ge 3600 ]; then
    echo "timed out waiting for Runner archive preparation lock: $lock_dir" >&2
    exit 75
  fi
  sleep 1
  lock_wait=$((lock_wait + 1))
done
temporary_chunks=""
cleanup() {
  if [ -n "$temporary_chunks" ] && [ -d "$temporary_chunks" ]; then
    find "$temporary_chunks" -depth -delete
  fi
  rmdir "$lock_dir"
}
trap cleanup EXIT

archive_valid() {
  [ -f "$1" ] &&
    [ "$(wc -c <"$1" | tr -d '[:space:]')" -eq "$RUNNER_ARCHIVE_SIZE" ] &&
    printf '%s  %s\n' "$RUNNER_ARCHIVE_SHA256" "$1" | sha256sum --check - >/dev/null 2>&1
}

chunks_valid() {
  [ -d "$chunks_dir" ] || return 1
  local index part expected_size actual_size
  for ((index = 0; index < chunk_slots; index++)); do
    printf -v part '%s/part-%03d' "$chunks_dir" "$index"
    [ -f "$part" ] || return 1
    expected_size=$((RUNNER_ARCHIVE_SIZE - index * chunk_size))
    if [ "$expected_size" -lt 0 ]; then
      expected_size=0
    elif [ "$expected_size" -gt "$chunk_size" ]; then
      expected_size="$chunk_size"
    fi
    actual_size="$(wc -c <"$part" | tr -d '[:space:]')"
    [ "$actual_size" -eq "$expected_size" ] || return 1
  done
  cat "$chunks_dir"/part-* | sha256sum | cut -d' ' -f1 | grep -Fxq "$RUNNER_ARCHIVE_SHA256"
}

if chunks_valid; then
  echo "Reusing checked Actions Runner ${RUNNER_VERSION} COPY chunks"
  exit 0
fi

if ! archive_valid "$archive_file"; then
  if [ -f "$archive_file" ]; then
    rm -f "$archive_file"
  fi
  if [ -f "$partial_file" ] && [ "$(wc -c <"$partial_file" | tr -d '[:space:]')" -ge "$RUNNER_ARCHIVE_SIZE" ]; then
    rm -f "$partial_file"
  fi
  if curl --http1.1 -fsSL --connect-timeout 15 --max-time 1800 --continue-at - \
    "$archive_url" -o "$partial_file"; then
    :
  else
    curl_status=$?
    if [ "$curl_status" -ne 33 ]; then
      exit "$curl_status"
    fi
    echo 'Runner archive server rejected resume; restarting download' >&2
    rm -f "$partial_file"
    curl --http1.1 -fsSL --connect-timeout 15 --max-time 1800 \
      "$archive_url" -o "$partial_file"
  fi
  archive_valid "$partial_file" || {
    echo 'downloaded Runner archive failed size or SHA-256 verification' >&2
    rm -f "$partial_file"
    exit 1
  }
  mv "$partial_file" "$archive_file"
fi

temporary_chunks="$(mktemp -d "${chunks_dir}.tmp.XXXXXX")"
split -b "$chunk_size" -d -a 3 "$archive_file" "$temporary_chunks/part-"
for ((index = 0; index < chunk_slots; index++)); do
  printf -v part '%s/part-%03d' "$temporary_chunks" "$index"
  if [ ! -f "$part" ]; then
    : >"$part"
  fi
done
cat "$temporary_chunks"/part-* | sha256sum | cut -d' ' -f1 | grep -Fxq "$RUNNER_ARCHIVE_SHA256" || {
  echo 'Runner archive chunks failed SHA-256 verification' >&2
  exit 1
}
if [ -d "$chunks_dir" ]; then
  find "$chunks_dir" -depth -delete
fi
mv "$temporary_chunks" "$chunks_dir"
temporary_chunks=""
echo "Prepared Actions Runner ${RUNNER_VERSION} in 16 checked COPY chunks"
