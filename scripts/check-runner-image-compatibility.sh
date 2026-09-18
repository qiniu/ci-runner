#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
lock_file="${RUNNER_IMAGES_LOCK:-$repository_root/templates/runner-images-upstream.lock.json}"
manifest_file="${RUNNER_IMAGES_MANIFEST:-$repository_root/templates/runner-images-compatibility.json}"
runner_env="$repository_root/templates/common/actions-runner.env"
parser="$repository_root/scripts/lib/parse-runner-image-report.awk"

fail() {
  echo "runner image compatibility: $*" >&2
  exit 1
}

command -v jq >/dev/null 2>&1 || fail "jq is required"
test -f "$lock_file" || fail "missing lock file $lock_file"
test -f "$manifest_file" || fail "missing compatibility manifest $manifest_file"
test -f "$runner_env" || fail "missing shared Actions Runner pin $runner_env"
test -f "$parser" || fail "missing report parser $parser"
runner_version="$(sed -n 's/^RUNNER_VERSION=//p' "$runner_env")"
test -n "$runner_version" || fail "shared Actions Runner version is empty"

repository="$(jq -er '.repository' "$lock_file")"
commit="$(jq -er '.commit' "$lock_file")"
test "$repository" = "actions/runner-images" || fail "lock repository must be actions/runner-images"
[[ "$commit" =~ ^[0-9a-f]{40}$ ]] || fail "lock commit is not a full Git SHA"
manifest_commit="$(jq -er '.upstream_commit' "$manifest_file")"
test "$manifest_commit" = "$commit" || fail "manifest commit $manifest_commit does not match lock $commit"

report_workspace="$(mktemp -d)"
cleanup() {
  find "$report_workspace" -type f -delete 2>/dev/null || true
  rmdir "$report_workspace" 2>/dev/null || true
}
trap cleanup EXIT

for image_key in ubuntu-slim ubuntu-22.04 ubuntu-24.04 ubuntu-26.04; do
  report_path="$(jq -er --arg image "$image_key" '.reports[$image]' "$lock_file")"
  report_name="$(basename "$report_path")"
  if [ -n "${RUNNER_IMAGES_REPORT_DIR:-}" ]; then
    source_report="$RUNNER_IMAGES_REPORT_DIR/$report_name"
    test -f "$source_report" || fail "missing fixture report $source_report"
    cp "$source_report" "$report_workspace/$report_name"
  else
    curl -fsSL --retry 3 \
      "https://raw.githubusercontent.com/$repository/$commit/$report_path" \
      -o "$report_workspace/$report_name"
  fi

  expected_sha="$(jq -r --arg image "$image_key" '.report_sha256[$image] // empty' "$lock_file")"
  if [ -n "$expected_sha" ]; then
    actual_sha="$(sha256sum "$report_workspace/$report_name" | awk '{print $1}')"
    test "$actual_sha" = "$expected_sha" ||
      fail "$image_key report checksum $actual_sha does not match lock $expected_sha"
  fi

  parsed_entries="$report_workspace/$image_key.tsv"
  awk -f "$parser" "$report_workspace/$report_name" >"$parsed_entries"
  test -s "$parsed_entries" || fail "$image_key report produced no compatibility items"

  while IFS=$'\t' read -r category upstream_name upstream_value kind; do
    matches="$(
      jq -c \
        --arg image "$image_key" \
        --arg category "$category" \
        --arg name "$upstream_name" \
        '[.images[$image].entries[]? | select(.category == $category and .upstream_name == $name)]' \
        "$manifest_file"
    )"
    count="$(jq 'length' <<<"$matches")"
    test "$count" -gt 0 ||
      fail "missing compatibility entry: $image_key / $category / $upstream_name"
    test "$count" -eq 1 ||
      fail "duplicate compatibility entries: $image_key / $category / $upstream_name"
  done <"$parsed_entries"

  manifest_count="$(jq --arg image "$image_key" '.images[$image].entries | length' "$manifest_file")"
  test "$manifest_count" -gt 0 || fail "$image_key has an empty compatibility manifest"
done

invalid_status="$(
  jq -r '
    .images | to_entries[] as $image |
    $image.value.entries[] |
    select(.status != "provided" and .status != "excluded") |
    "\($image.key) / \(.category) / \(.upstream_name)"
  ' "$manifest_file"
)"
test -z "$invalid_status" || fail "status must be provided or excluded: $invalid_status"

missing_verification="$(
  jq -r '
    .images | to_entries[] as $image |
    $image.value.entries[] |
    select((.verification // "") | gsub("[[:space:]]"; "") | length == 0) |
    "\($image.key) / \(.category) / \(.upstream_name)"
  ' "$manifest_file"
)"
test -z "$missing_verification" || fail "missing verification command: $missing_verification"

bad_exclusion="$(
  jq -r '
    .images | to_entries[] as $image |
    $image.value.entries[] |
    select(.status == "excluded") |
    select(
      ((.reason // "") | test("Qiniu Sandbox|Sandbox runtime|Sandbox template"; "i")) | not
    ) |
    "\($image.key) / \(.category) / \(.upstream_name)"
  ' "$manifest_file"
)"
test -z "$bad_exclusion" ||
  fail "excluded entries require a concrete Sandbox-specific reason: $bad_exclusion"

for image_key in ubuntu-slim ubuntu-22.04 ubuntu-24.04 ubuntu-26.04; do
  jq -e --arg image "$image_key" --arg version "$runner_version" '
    [.images[$image].entries[]? | select(.upstream_name == "preinstalled GitHub Actions runner")] as $matches |
    ($matches | length) == 1 and
    $matches[0].upstream_value == ("/opt/actions-runner (" + $version + ")") and
    ($matches[0].verification | endswith(" = " + $version))
  ' "$manifest_file" >/dev/null ||
    fail "$image_key Actions Runner contract differs from common pin $runner_version"
done

echo "runner image compatibility: pinned upstream coverage is complete for 4 reports"
