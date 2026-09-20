#!/usr/bin/env bash
set -euo pipefail

: "${QINIU_SANDBOX_API_URL:?QINIU_SANDBOX_API_URL is required}"
: "${QINIU_API_KEY:?QINIU_API_KEY is required}"

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

for dependency in curl jq; do
  command -v "$dependency" >/dev/null 2>&1 || {
    echo "$dependency is required" >&2
    exit 69
  }
done

catalog_file="$(mktemp)"
cleanup() {
  rm -f "$catalog_file"
}
trap cleanup EXIT

curl -fsS \
  --header @<(printf 'X-API-Key: %s\n' "$QINIU_API_KEY") \
  "${QINIU_SANDBOX_API_URL%/}/default-templates" \
  >"$catalog_file"

jq -e 'type == "array"' "$catalog_file" >/dev/null || {
  echo "default-template catalog response is not an array" >&2
  exit 65
}

template_names=(
  github-runner-ubuntu-slim
  github-runner-ubuntu-22-04
  github-runner-ubuntu-24-04
  github-runner-ubuntu-26-04
  github-runner-ubuntu-slim-large
  github-runner-ubuntu-22-04-large
  github-runner-ubuntu-24-04-large
  github-runner-ubuntu-26-04-large
)

for template_name in "${template_names[@]}"; do
  matches="$(
    jq -c --arg name "$template_name" '
      [
        .[] |
        select(
          any(
            (.names // [])[];
            . == $name or endswith("/" + $name)
          )
        )
      ]
    ' "$catalog_file"
  )"
  count="$(jq 'length' <<<"$matches")"
  if [ "$count" -ne 1 ]; then
    echo "expected exactly one default template name match for $template_name; found $count" >&2
    exit 1
  fi
  if ! jq -e '
    .[0] |
    .public == true and
    ((.buildStatus // "") == "ready" or (.buildStatus // "") == "uploaded") and
    ((.templateID // "") != "")
  ' <<<"$matches" >/dev/null; then
    echo "default template $template_name is not public, runnable, and backed by a nonempty ID" >&2
    exit 1
  fi
  case "$template_name" in
    github-runner-ubuntu-slim*) template_directory=github-runner-ubuntu-slim ;;
    github-runner-ubuntu-22-04*) template_directory=github-runner-ubuntu-22.04 ;;
    github-runner-ubuntu-24-04*) template_directory=github-runner-ubuntu-24.04 ;;
    github-runner-ubuntu-26-04*) template_directory=github-runner-ubuntu-26.04 ;;
  esac
  template_config="$repository_root/templates/$template_directory/qshell.sandbox.toml"
  if [[ "$template_name" == *-large ]]; then
    template_config="$repository_root/templates/$template_directory/qshell.sandbox.large.toml"
  fi
  expected_disk_size="$(
    sed -nE \
      's/^[[:space:]]*disk_size_mb[[:space:]]*=[[:space:]]*([0-9]+)[[:space:]]*$/\1/p' \
      "$template_config"
  )"
  [[ "$expected_disk_size" =~ ^[1-9][0-9]*$ ]] || {
    echo "could not determine disk_size_mb from $template_config" >&2
    exit 65
  }
  if ! jq -e --argjson expected "$expected_disk_size" '.[0].diskSizeMB | type == "number" and . >= $expected' <<<"$matches" >/dev/null; then
    actual_disk_size="$(jq -r '.[0].diskSizeMB // "missing"' <<<"$matches")"
    if ! jq -e '.[0].diskSizeMB | type == "number"' <<<"$matches" >/dev/null; then
      echo "default template $template_name has invalid total disk size $actual_disk_size MiB" >&2
    else
      echo "default template $template_name total disk size $actual_disk_size MiB is below disk_size_mb $expected_disk_size MiB" >&2
    fi
    exit 1
  fi
  jq -r --arg name "$template_name" '
    .[0] |
    "\($name)\t\(.templateID)\t\(.buildStatus)"
  ' <<<"$matches"
done
