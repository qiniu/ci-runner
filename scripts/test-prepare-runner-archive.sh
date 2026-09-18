#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workdir="$(mktemp -d)"
trap 'find "$workdir" -depth -delete' EXIT

source_file="$workdir/source.tar.gz"
pin_file="$workdir/actions-runner.env"
chunks_dir="$workdir/chunks"
output="$workdir/assembled.tar.gz"
printf '%s' '0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijk' >"$source_file"
archive_size="$(wc -c <"$source_file" | tr -d '[:space:]')"
archive_sha256="$(sha256sum "$source_file" | cut -d' ' -f1)"
cat >"$pin_file" <<EOF
RUNNER_VERSION=fixture
RUNNER_ARCHIVE_SHA256=$archive_sha256
RUNNER_ARCHIVE_SIZE=$archive_size
EOF

prepare_fixture() {
  RUNNER_ARCHIVE_URL="file://$source_file" RUNNER_ARCHIVE_CHUNK_SIZE=8 \
    bash "$repository_root/scripts/prepare-runner-archive.sh" \
    "$pin_file" "$chunks_dir" "$workdir/downloads"
}

prepare_fixture
: >"$chunks_dir/.reuse-marker"
prepare_fixture
test -f "$chunks_dir/.reuse-marker" || {
  echo 'valid Runner archive chunks were replaced on reuse' >&2
  exit 1
}

printf '%s' xxxxxxxx >"$chunks_dir/part-000"
if RUNNER_ARCHIVE_CHUNK_SIZE=8 bash "$repository_root/templates/common/scripts/assemble-runner-archive" \
  "$pin_file" "$chunks_dir" "$output" >/dev/null 2>&1; then
  echo 'corrupted Runner archive unexpectedly passed verification' >&2
  exit 1
fi

prepare_fixture
RUNNER_ARCHIVE_CHUNK_SIZE=8 bash "$repository_root/templates/common/scripts/assemble-runner-archive" \
  "$pin_file" "$chunks_dir" "$output"
cmp "$source_file" "$output"

prepare_fixture &
first_prepare=$!
prepare_fixture &
second_prepare=$!
wait "$first_prepare"
wait "$second_prepare"
cat "$chunks_dir"/part-* | cmp - "$source_file"
test ! -e "${chunks_dir}.prepare-lock"
echo 'Runner archive preparation, COPY chunks, and SHA-256 verification passed'
