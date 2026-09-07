#!/usr/bin/env bash
set -euo pipefail

if [ "$#" -ne 2 ]; then
  echo "usage: scripts/smoke-runner-template.sh IMAGE_KEY TEMPLATE_ID" >&2
  exit 64
fi

image_key="$1"
template_id="$2"
repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
manifest_file="${RUNNER_IMAGES_MANIFEST:-$repository_root/templates/runner-images-compatibility.json}"
workdir="$(mktemp -d)"
augmented_manifest="$workdir/compatibility.json"
output_dir="${RUNNER_SMOKE_OUTPUT_DIR:-$repository_root/.build/runner-conformance}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
output="$output_dir/${image_key}-${timestamp}.json"
qshell_bin="${QSHELL:-qshell}"

cleanup() {
  find "$workdir" -type f -delete 2>/dev/null || true
  rmdir "$workdir" 2>/dev/null || true
}
trap cleanup EXIT

command -v "$qshell_bin" >/dev/null 2>&1 || {
  echo "qshell executable not found: $qshell_bin" >&2
  exit 69
}
qshell_version="$("$qshell_bin" version 2>&1 | sed -nE 's/^v([0-9]+\.[0-9]+\.[0-9]+).*$/\1/p' | head -n 1)"
test -n "$qshell_version" || {
  echo "could not determine qshell version" >&2
  exit 69
}
if ! awk -v actual="$qshell_version" -v minimum="2.19.10" '
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
  echo "qshell >= 2.19.10 is required; found v$qshell_version" >&2
  exit 69
fi

case "$image_key" in
  ubuntu-slim)
    expected_release=24.04
    support_channel=stable
    template_name=github-runner-ubuntu-slim
    template_directory=github-runner-ubuntu-slim
    manifest_image_key=ubuntu-slim
    ;;
  ubuntu-slim-large)
    expected_release=24.04
    support_channel=development
    template_name=github-runner-ubuntu-slim
    template_directory=github-runner-ubuntu-slim-large
    manifest_image_key=ubuntu-slim
    ;;
  ubuntu-24.04)
    expected_release=24.04
    support_channel=stable
    template_name=github-runner-ubuntu-24-04
    template_directory=github-runner-ubuntu-24.04
    manifest_image_key=ubuntu-24.04
    ;;
  ubuntu-24.04-large)
    expected_release=24.04
    support_channel=development
    template_name=github-runner-ubuntu-24-04
    template_directory=github-runner-ubuntu-24.04-large
    manifest_image_key=ubuntu-24.04
    ;;
  ubuntu-22.04)
    expected_release=22.04
    support_channel=stable
    template_name=github-runner-ubuntu-22-04
    template_directory=github-runner-ubuntu-22.04
    manifest_image_key=ubuntu-22.04
    ;;
  ubuntu-22.04-large)
    expected_release=22.04
    support_channel=development
    template_name=github-runner-ubuntu-22-04
    template_directory=github-runner-ubuntu-22.04-large
    manifest_image_key=ubuntu-22.04
    ;;
  ubuntu-26.04)
    expected_release=26.04
    support_channel=preview
    template_name=github-runner-ubuntu-26-04
    template_directory=github-runner-ubuntu-26.04
    manifest_image_key=ubuntu-26.04
    ;;
  ubuntu-26.04-large)
    expected_release=26.04
    support_channel=development
    template_name=github-runner-ubuntu-26-04
    template_directory=github-runner-ubuntu-26.04-large
    manifest_image_key=ubuntu-26.04
    ;;
  *)
    echo "unknown image key $image_key" >&2
    exit 65
    ;;
esac

template_dockerfile="$repository_root/templates/$template_directory/Dockerfile"
expected_runner_version="$(awk -F= '$1 == "ARG RUNNER_VERSION" {print $2; exit}' "$template_dockerfile")"
expected_template_version="$(awk -F= '$1 == "ARG TEMPLATE_VERSION" {print $2; exit}' "$template_dockerfile")"
test -n "$expected_runner_version" || {
  echo "could not determine RUNNER_VERSION from $template_dockerfile" >&2
  exit 65
}
test -n "$expected_template_version" || {
  echo "could not determine TEMPLATE_VERSION from $template_dockerfile" >&2
  exit 65
}

docker_smoke_command="$(
  cat <<'DOCKER_SMOKE' | tr '\n' ' '
sudo -H -u runner -- bash -lc '
  set -euo pipefail;
  ensure-docker;
  docker info >/dev/null;
  smoke_root="$(mktemp -d)";
  cleanup_docker_smoke() {
    rm -rf "$smoke_root";
    docker image rm -f qiniu-runner-smoke:local >/dev/null 2>&1 || true;
  };
  trap cleanup_docker_smoke EXIT;
  install -D /bin/sh "$smoke_root/bin/sh";
  ldd /bin/sh | grep -Eo "/[^[:space:]]+" | while read -r library; do
    install -D "$library" "$smoke_root$library";
  done;
  tar -C "$smoke_root" -cf - . | docker import - qiniu-runner-smoke:local >/dev/null;
  docker run --rm --network none qiniu-runner-smoke:local /bin/sh -c "exit 0";
'
DOCKER_SMOKE
)"

nvm_smoke_command="$(
  cat <<'NVM_SMOKE' | tr '\n' ' '
sudo -H -u runner -- bash -lc '
  set -euo pipefail;
  test -s "$HOME/.nvm/nvm.sh";
  test -w "$HOME/.nvm";
  export NVM_DIR="$HOME/.nvm";
  . "$NVM_DIR/nvm.sh";
  nvm --version >/dev/null;
'
NVM_SMOKE
)"

jq \
  --arg image "$image_key" \
  --arg manifest_image "$manifest_image_key" \
  --arg expected_release "$expected_release" \
  --arg expected_runner_version "$expected_runner_version" \
  --arg expected_template_version "$expected_template_version" \
  --arg template_name "$template_name" \
  --arg nvm_smoke_command "$nvm_smoke_command" \
  --arg docker_smoke_command "$docker_smoke_command" \
  '
    .images[$image] = (.images[$manifest_image] // {}) |
    .images[$image].entries = [
      {
        category: "Release smoke",
        upstream_name: "OS release",
        status: "provided",
        verification: (". /etc/os-release; test \"$VERSION_ID\" = \"" + $expected_release + "\"")
      },
      {
        category: "Release smoke",
        upstream_name: "x86_64 architecture",
        status: "provided",
        verification: "test \"$(uname -m)\" = x86_64"
      },
      {
        category: "Release smoke",
        upstream_name: "preinstalled Actions runner",
        status: "provided",
        verification: (
          "test -x /opt/actions-runner/config.sh && "
          + "test -x /opt/actions-runner/run.sh && "
          + "test \"$(/opt/actions-runner/bin/Runner.Listener --version)\" = \""
          + $expected_runner_version
          + "\""
        )
      },
      {
        category: "Release smoke",
        upstream_name: "runtime image metadata",
        status: "provided",
        verification: (
          "test \"$IMAGE_TEMPLATE\" = \"" + $template_name + "\" && "
          + "test \"$ImageVersion\" = \"" + $expected_template_version + "\" && "
          + "test \"$IMAGE_VERSION\" = \"" + $expected_template_version + "\""
        )
      },
      {
        category: "Release smoke",
        upstream_name: "Cloudflare DNS",
        status: "provided",
        verification: (
          "test \"$(cat /etc/resolv.conf)\" = "
          + "\"$(printf \"%s\\n\" \"nameserver 1.1.1.1\" \"nameserver 1.0.0.1\")\""
        )
      },
      {
        category: "Release smoke",
        upstream_name: "runner writable NVM home",
        status: "provided",
        verification: $nvm_smoke_command
      },
      {
        category: "Release smoke",
        upstream_name: "outbound HTTPS",
        status: "provided",
        verification: "curl -fsS --connect-timeout 15 https://api.github.com/zen >/dev/null"
      },
      {
        category: "Release smoke",
        upstream_name: "Docker daemon",
        status: "provided",
        verification: $docker_smoke_command
      },
      {
        category: "Release smoke",
        upstream_name: "runner writable work and tool-cache paths",
        status: "provided",
        verification: "test -d /home/runner/work && test -w /home/runner/work && test -w /opt/hostedtoolcache"
      }
    ]
  ' "$manifest_file" >"$augmented_manifest"

mkdir -p "$output_dir"
RUNNER_IMAGES_MANIFEST="$augmented_manifest" \
  QSHELL="$qshell_bin" \
  "$repository_root/scripts/run-runner-image-conformance.sh" \
  --image "$image_key" \
  --executor sandbox \
  --target "$template_id" \
  --output "$output"

jq --arg support_channel "$support_channel" \
  '.support_channel = $support_channel' \
  "$output" >"$workdir/result.json"
mv "$workdir/result.json" "$output"

echo "$output"
