#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repository_root"
templates_readme="${RUNNER_TEMPLATES_README:-templates/README.md}"
minimum_runner_version="${MINIMUM_ACTIONS_RUNNER_VERSION:-2.336.0}"

fail() {
  echo "runner template matrix: $*" >&2
  exit 1
}

version_at_least() {
  awk -v actual="$1" -v minimum="$2" '
    BEGIN {
      split(actual, actual_parts, ".")
      split(minimum, minimum_parts, ".")
      for (part = 1; part <= 3; part++) {
        if ((actual_parts[part] + 0) > (minimum_parts[part] + 0)) exit 0
        if ((actual_parts[part] + 0) < (minimum_parts[part] + 0)) exit 1
      }
      exit 0
    }
  '
}

test -f "$templates_readme" || fail "missing templates README $templates_readme"

expected_name() {
  case "$1" in
    ubuntu-slim) echo github-runner-ubuntu-slim ;;
    ubuntu-22.04) echo github-runner-ubuntu-22-04 ;;
    ubuntu-24.04) echo github-runner-ubuntu-24-04 ;;
    ubuntu-26.04) echo github-runner-ubuntu-26-04 ;;
    ubuntu-slim-large) echo github-runner-ubuntu-slim-large ;;
    ubuntu-22.04-large) echo github-runner-ubuntu-22-04-large ;;
    ubuntu-24.04-large) echo github-runner-ubuntu-24-04-large ;;
    ubuntu-26.04-large) echo github-runner-ubuntu-26-04-large ;;
    *) return 1 ;;
  esac
}

base_image_key() {
  case "$1" in
    ubuntu-slim-large) echo ubuntu-slim ;;
    ubuntu-22.04-large) echo ubuntu-22.04 ;;
    ubuntu-24.04-large) echo ubuntu-24.04 ;;
    ubuntu-26.04-large) echo ubuntu-26.04 ;;
    *) echo "$1" ;;
  esac
}

expected_base_reference() {
  case "$(base_image_key "$1")" in
    ubuntu-slim | ubuntu-24.04)
      echo public.ecr.aws/ubuntu/ubuntu:24.04@sha256:be20a0347f238b7d373edddc55923443b21dd9a60277bf8a93e43458cd0bf2fc
      ;;
    ubuntu-22.04)
      echo public.ecr.aws/ubuntu/ubuntu:22.04@sha256:0bc16241504efa4cf92fcc8c8039dac604c6bb832b3d8fcc24097c6b05b60b7c
      ;;
    ubuntu-26.04)
      echo public.ecr.aws/ubuntu/ubuntu:26.04@sha256:a5da6f6b18c3a4b8dcc73244592f7096f417d2667966d0e33460e9e308f25f67
      ;;
    *) return 1 ;;
  esac
}

expected_build_target() {
  case "$1" in
    ubuntu-slim) echo template-build-ubuntu-slim ;;
    ubuntu-22.04) echo template-build-ubuntu-22-04 ;;
    ubuntu-24.04) echo template-build-ubuntu-24-04 ;;
    ubuntu-26.04) echo template-build-ubuntu-26-04 ;;
    ubuntu-slim-large) echo template-build-ubuntu-slim-large ;;
    ubuntu-22.04-large) echo template-build-ubuntu-22-04-large ;;
    ubuntu-24.04-large) echo template-build-ubuntu-24-04-large ;;
    ubuntu-26.04-large) echo template-build-ubuntu-26-04-large ;;
    *) return 1 ;;
  esac
}

seen_names_file="$(mktemp)"
cleanup() {
  find "$seen_names_file" -type f -delete 2>/dev/null || true
}
trap cleanup EXIT
for image_key in ubuntu-slim ubuntu-22.04 ubuntu-24.04 ubuntu-26.04 ubuntu-slim-large ubuntu-22.04-large ubuntu-24.04-large ubuntu-26.04-large; do
  directory="templates/github-runner-${image_key}"
  base_key="$(base_image_key "$image_key")"
  base_directory="templates/github-runner-${base_key}"
  base_dir_name="github-runner-${base_key}"
  test -d "$directory" || fail "missing directory $directory"
  for required_file in Dockerfile qshell.sandbox.toml README.md software-diff.md scripts/setup-template.sh scripts/ensure-docker scripts/download-checked-range; do
    test -f "$directory/$required_file" || fail "missing $directory/$required_file"
  done

  runner_version="$(awk -F= '$1 == "ARG RUNNER_VERSION" {print $2; exit}' "$directory/Dockerfile")"
  test -n "$runner_version" || fail "$image_key does not pin RUNNER_VERSION"
  version_at_least "$runner_version" "$minimum_runner_version" ||
    fail "$image_key Actions Runner $runner_version is below required $minimum_runner_version"

  template_name="$(
    awk -F= '/^[[:space:]]*name[[:space:]]*=/ {
      value=$2
      gsub(/[[:space:]"]/, "", value)
      print value
      exit
    }' "$directory/qshell.sandbox.toml"
  )"
  wanted_name="$(expected_name "$image_key")"
  test "$template_name" = "$wanted_name" ||
    fail "$image_key template name is $template_name, want $wanted_name"
  if grep -Fxq "$template_name" "$seen_names_file"; then
    fail "duplicate physical template name $template_name"
  fi
  echo "$template_name" >>"$seen_names_file"

  cpu_count="$(awk -F= '/^[[:space:]]*cpu_count[[:space:]]*=/ {gsub(/[[:space:]]/, "", $2); print $2; exit}' "$directory/qshell.sandbox.toml")"
  memory_mb="$(awk -F= '/^[[:space:]]*memory_mb[[:space:]]*=/ {gsub(/[[:space:]]/, "", $2); print $2; exit}' "$directory/qshell.sandbox.toml")"
  test "$cpu_count" = 8 || fail "$image_key cpu_count is $cpu_count, want 8"
  test "$memory_mb" = 8192 || fail "$image_key memory_mb is $memory_mb, want 8192"

  base_reference="$(
    awk '
      toupper($1) == "FROM" {
        for (field_index = 1; field_index <= NF; field_index++) {
          if ($field_index ~ /^public\.ecr\.aws\/ubuntu\/ubuntu:[0-9][0-9]\.[0-9][0-9]@sha256:[0-9a-f]{64}$/) {
            print $field_index
            exit
          }
        }
      }' "$directory/Dockerfile"
  )"
  wanted_base_reference="$(expected_base_reference "$image_key")"
  test "$base_reference" = "$wanted_base_reference" ||
    fail "$image_key base reference is $base_reference, want $wanted_base_reference"

  if grep -Eq '^[[:space:]]*RUN[[:space:]]+--mount=|/run/secrets/github_token' "$directory/Dockerfile"; then
    fail "$image_key uses a BuildKit-only RUN secret that qshell v2 cannot execute"
  fi
  grep -Eq '^[[:space:]]*RUN[[:space:]]+TEMPLATE_FLAVOR=' "$directory/Dockerfile" ||
    fail "$image_key setup must use a plain qshell-compatible RUN instruction"
  template_flavor=versioned
  if [ "$base_key" = ubuntu-slim ]; then
    template_flavor=slim
  fi
  phases="bootstrap platform toolchain runtime"
  if [ "$base_key" != ubuntu-slim ]; then
    phases="bootstrap platform node toolchain runtime"
  fi
  phase_count=0
  for phase in $phases; do
    grep -Fq \
      "TEMPLATE_FLAVOR=$template_flavor RUNNER_TEMPLATE_PHASE=$phase bash /usr/local/share/qiniu-sandbox-runner-template/setup-template.sh" \
      "$directory/Dockerfile" ||
      fail "$image_key is missing cacheable setup phase $phase"
    phase_count=$((phase_count + 1))
  done
  actual_phase_count="$(grep -Ec 'TEMPLATE_FLAVOR=(slim|versioned)[[:space:]]+RUNNER_TEMPLATE_PHASE=' "$directory/Dockerfile")"
  test "$actual_phase_count" -eq "$phase_count" ||
    fail "$image_key has $actual_phase_count setup phases, want $phase_count"
  cloudflare_primary_line="$(grep -nF "'nameserver 1.1.1.1'" "$directory/Dockerfile" | cut -d: -f1 || true)"
  cloudflare_secondary_line="$(grep -nF "'nameserver 1.0.0.1'" "$directory/Dockerfile" | cut -d: -f1 || true)"
  resolv_conf_line="$(grep -nF '>/etc/resolv.conf' "$directory/Dockerfile" | cut -d: -f1 || true)"
  runtime_phase_line="$(grep -nF "RUNNER_TEMPLATE_PHASE=runtime" "$directory/Dockerfile" | cut -d: -f1)"
  user_line="$(grep -nE '^USER[[:space:]]+' "$directory/Dockerfile" | cut -d: -f1)"
  test -n "$cloudflare_primary_line" && test -n "$cloudflare_secondary_line" && test -n "$resolv_conf_line" ||
    fail "$image_key must configure Cloudflare nameservers 1.1.1.1 and 1.0.0.1"
  test "$runtime_phase_line" -lt "$cloudflare_primary_line" &&
    test "$cloudflare_primary_line" -lt "$cloudflare_secondary_line" &&
    test "$cloudflare_secondary_line" -le "$resolv_conf_line" &&
    test "$resolv_conf_line" -lt "$user_line" ||
    fail "$image_key must configure Cloudflare nameservers after runtime setup and before USER"
  if grep -Eq "['\"]nameserver[[:space:]]+8\\.8\\.(8\\.8|4\\.4)['\"]" "$directory/Dockerfile"; then
    fail "$image_key must not retain Google Public DNS in the final template configuration"
  fi
  if grep -Fq 'Acquire::https::Verify-Peer=false' "$directory/scripts/setup-template.sh"; then
    fail "$image_key setup must not disable apt HTTPS peer verification"
  fi
  if [ "$base_key" != ubuntu-22.04 ]; then
    grep -Fq 'ensure_upstream_apt_source_layout' \
      "$directory/scripts/setup-template.sh" ||
      fail "$image_key must adapt the Canonical ECR apt layout before upstream setup"
    grep -Fq 'install -m 0644 /dev/null /etc/apt/sources.list.d/ubuntu.sources' \
      "$directory/scripts/setup-template.sh" ||
      fail "$image_key must provide the deb822 path expected by upstream setup"
  fi
  if [ "$base_key" != ubuntu-slim ]; then
    grep -Fq 'install-nvm.sh | install-nodejs.sh)' \
      "$directory/scripts/setup-template.sh" ||
      fail "$image_key must isolate Node installation in its cacheable node phase"
    for required_installer in \
      install-apt-common.sh \
      install-apache.sh \
      install-container-tools.sh \
      install-github-cli.sh \
      install-nodejs.sh \
      install-python.sh \
      install_azcopy_from_microsoft_package \
      install_pester_for_upstream_tests; do
      grep -Fq "$required_installer" "$directory/scripts/setup-template.sh" ||
        fail "$image_key disk-bounded toolset is missing $required_installer"
    done
    for full_only_installer in \
      install-dotnetcore-sdk.sh \
      install-android-sdk.sh \
      install-codeql-bundle.sh \
      install-homebrew.sh \
      Install-PowerShellModules.ps1 \
      Install-PowerShellAzModules.ps1 \
      Install-Toolset.ps1 \
      Configure-Toolset.ps1; do
      if grep -Fq "$full_only_installer" "$directory/scripts/setup-template.sh"; then
        fail "$image_key disk-bounded toolset must not install $full_only_installer"
      fi
    done
    grep -Fq 'start_new_session=True' \
      "$directory/scripts/setup-template.sh" ||
      fail "$image_key must detach service startup from the Sandbox build session"
    grep -Fq 'start_apache' \
      "$directory/scripts/setup-template.sh" ||
      fail "$image_key must confirm detached Apache startup before Pester continues"
  fi
  if [[ "$image_key" != *-large ]]; then
    build_target="$(expected_build_target "$image_key")"
    grep -Fq "task $build_target" "$directory/README.md" ||
      fail "$image_key README must use the exact task $build_target build command"
  fi

  if [[ "$image_key" == *-large ]]; then
    large_build_target="$(expected_build_target "$image_key")"
    test -f "$directory/README.md" || fail "$image_key must provide README.md"
    test ! -L "$directory/README.md" || fail "$image_key README.md must document its large build target"
    grep -Fq "task $large_build_target" "$directory/README.md" ||
      fail "$image_key README must use the exact task $large_build_target build command"
    for shared_entry in Dockerfile software-diff.md scripts; do
      test -L "$directory/$shared_entry" || fail "$image_key must symlink $shared_entry"
      test "$(readlink "$directory/$shared_entry")" = "../$base_dir_name/$shared_entry" ||
        fail "$image_key $shared_entry must point to $base_dir_name/$shared_entry"
    done
    expected_dockerfile="../$base_dir_name/Dockerfile"
    expected_path="../$base_dir_name"
    grep -Fq "dockerfile = \"$expected_dockerfile\"" "$directory/qshell.sandbox.toml" ||
      fail "$image_key qshell config must use the in-context Dockerfile $expected_dockerfile"
    grep -Fq "path = \"$expected_path\"" "$directory/qshell.sandbox.toml" ||
      fail "$image_key qshell config must use the in-context path $expected_path"
  fi
done

if grep -En 'runner-template-build-all|qshell sandbox template (publish|unpublish).*--config' \
  "$templates_readme" templates/github-runner-*/README.md >/dev/null; then
  fail "template docs reference an unsupported aggregate target or publish/unpublish --config"
fi
grep -Fq 'Qshell publish and unpublish do not support the build-only' "$templates_readme" ||
  fail "templates README must state that publish/unpublish do not support build-only --config"
grep -Fq 'read the stable name from its tracked `qshell.sandbox.toml`' "$templates_readme" ||
  fail "templates README must use the tracked stable name for publish/unpublish"

readme_catalog="$(
  awk -F'|' '
    /^\| `ubuntu-/ {
      for (field_index = 2; field_index <= 5; field_index++) {
        gsub(/^[[:space:]]+|[[:space:]]+$/, "", $field_index)
        gsub(/`/, "", $field_index)
      }
      print $2 "\t" $3 "\t" $4 "\t" $5
    }' "$templates_readme"
)"
expected_catalog=$'ubuntu-slim\tgithub-runner-ubuntu-slim\tUbuntu Slim x64\tstable\nubuntu-22.04\tgithub-runner-ubuntu-22-04\tUbuntu 22.04 x64\tfollows upstream deprecation\nubuntu-24.04\tgithub-runner-ubuntu-24-04\tUbuntu 24.04 x64\tstable\nubuntu-26.04\tgithub-runner-ubuntu-26-04\tUbuntu 26.04 x64\tpreview\nubuntu-slim-large\tgithub-runner-ubuntu-slim-large\tUbuntu Slim x64 (80 GiB)\tlarge\nubuntu-22.04-large\tgithub-runner-ubuntu-22-04-large\tUbuntu 22.04 x64 (80 GiB)\tfollows upstream deprecation\nubuntu-24.04-large\tgithub-runner-ubuntu-24-04-large\tUbuntu 24.04 x64 (80 GiB)\tlarge\nubuntu-26.04-large\tgithub-runner-ubuntu-26-04-large\tUbuntu 26.04 x64 (80 GiB)\tpreview\nubuntu-latest\tgithub-runner-ubuntu-24-04\tUbuntu 24.04 x64\tstable logical mapping'
test "$readme_catalog" = "$expected_catalog" ||
  fail "templates/README.md support matrix does not match the nine public logical rows"

publication_states="$(
  awk -F'|' '
    /^\| `ubuntu-/ {
      label=$2
      state=$6
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", label)
      gsub(/^[[:space:]]+|[[:space:]]+$/, "", state)
      gsub(/`/, "", label)
      print label "\t" state
    }' "$templates_readme"
)"
invalid_states="$(
  awk -F'\t' '$2 != "development" && $2 != "published" && $2 != "verified" {print $1 "=" $2}' \
    <<<"$publication_states"
)"
test -z "$invalid_states" ||
  fail "unsupported publication state: $invalid_states"
state_24="$(awk -F'\t' '$1 == "ubuntu-24.04" {print $2}' <<<"$publication_states")"
state_latest="$(awk -F'\t' '$1 == "ubuntu-latest" {print $2}' <<<"$publication_states")"
test -n "$state_24" && test "$state_latest" = "$state_24" ||
  fail "ubuntu-latest publication state must match ubuntu-24.04"

if find templates -mindepth 1 -maxdepth 1 -type d -name '*ubuntu-latest*' | grep -q .; then
  fail "ubuntu-latest must not have a fifth physical template directory"
fi

echo "runner template matrix: 8 physical templates and 9 logical mappings verified"
