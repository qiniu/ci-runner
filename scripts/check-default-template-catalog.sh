#!/usr/bin/env bash
set -euo pipefail

: "${QINIU_SANDBOX_API_URL:?QINIU_SANDBOX_API_URL is required}"
: "${QINIU_API_KEY:?QINIU_API_KEY is required}"

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
  jq -r --arg name "$template_name" '
    .[0] |
    "\($name)\t\(.templateID)\t\(.buildStatus)"
  ' <<<"$matches"
done
