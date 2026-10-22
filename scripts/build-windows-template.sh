#!/usr/bin/env bash
set -euo pipefail

# Builds a Windows runner template with qshell on top of the provider's
# windows11 template. It checks the Windows-only build pins, then delegates to
# the shared qshell build helper.

if [ "$#" -ne 1 ]; then
  echo "usage: scripts/build-windows-template.sh TEMPLATE_DIR" >&2
  exit 64
fi

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd -P)"
template_dir="$1"
if [[ "$template_dir" != /* ]]; then
  template_dir="$repository_root/$template_dir"
fi
template_env="$template_dir/template.env"
template_config="$template_dir/qshell.sandbox.toml"
runner_env="$repository_root/templates/common/actions-runner.env"

for file in "$template_env" "$template_config" "$template_dir/Dockerfile" "$template_dir/scripts/setup-template.ps1" "$runner_env"; do
  test -f "$file" || {
    echo "required file does not exist: $file" >&2
    exit 66
  }
done

read_value() {
  local file="$1" key="$2" value
  value="$(sed -n "s/^${key}=//p" "$file" | tail -n 1)"
  test -n "$value" || {
    echo "$key is missing from $file" >&2
    exit 65
  }
  printf '%s' "$value"
}

if grep -Eq '^(TEMPLATE_ID|template_id)=' "$template_env" ||
  grep -Eq '^[[:space:]]*template_id[[:space:]]*=' "$template_config"; then
  echo "$template_dir must not contain an environment-specific template ID" >&2
  exit 65
fi
runner_version="$(read_value "$runner_env" RUNNER_VERSION)"
windows_runner_version="$(read_value "$template_env" RUNNER_WIN_X64_VERSION)"
if [ "$windows_runner_version" != "$runner_version" ]; then
  echo "RUNNER_WIN_X64_VERSION $windows_runner_version does not match common RUNNER_VERSION $runner_version" >&2
  echo "update the Windows runner archive pin in $template_env" >&2
  exit 65
fi
template_name="$(read_value "$template_env" TEMPLATE_NAME)"
config_name="$(sed -nE 's/^[[:space:]]*name[[:space:]]*=[[:space:]]*"([^"]+)".*/\1/p' "$template_config" | head -n 1)"
if [ "$template_name" != "$config_name" ]; then
  echo "TEMPLATE_NAME $template_name does not match qshell name $config_name" >&2
  exit 65
fi

exec bash "$repository_root/scripts/run-runner-template-operation.sh" build "$template_config"
