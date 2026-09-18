#!/usr/bin/env bash
set -euxo pipefail

runner_template_phase="${RUNNER_TEMPLATE_PHASE:-all}"
case "$runner_template_phase" in
  all | bootstrap | platform | toolchain | runtime) ;;
  *)
    echo "unsupported runner template phase: $runner_template_phase" >&2
    exit 64
    ;;
esac

installer_phase() {
  case "$1" in
    configure-apt-sources.sh | configure-apt.sh | install-apt-vital.sh | install-ms-repos.sh | configure-image-data-file.sh | configure-environment.sh | install-apt-common.sh | install-azure-cli.sh | install-bicep.sh | install-apache.sh | install-aws-tools.sh | install-container-tools.sh | install-git.sh | install-git-lfs.sh | install-github-cli.sh | install-google-cloud-cli.sh)
      echo platform
      ;;
    *) echo toolchain ;;
  esac
}

: "${RUNNER_IMAGES_REV:?RUNNER_IMAGES_REV is required}"
: "${RUNNER_IMAGES_ARCHIVE_SHA256:?RUNNER_IMAGES_ARCHIVE_SHA256 is required}"
: "${AZCOPY_VERSION:?AZCOPY_VERSION is required}"
: "${AZCOPY_DEB_SHA256:?AZCOPY_DEB_SHA256 is required}"
: "${AZURE_CLI_VERSION:?AZURE_CLI_VERSION is required}"
: "${AZURE_CLI_JAMMY_DEB_SHA256:?AZURE_CLI_JAMMY_DEB_SHA256 is required}"
: "${AZURE_CLI_NOBLE_DEB_SHA256:?AZURE_CLI_NOBLE_DEB_SHA256 is required}"
: "${AZURE_DEVOPS_EXTENSION_VERSION:?AZURE_DEVOPS_EXTENSION_VERSION is required}"
: "${AZURE_DEVOPS_EXTENSION_SHA256:?AZURE_DEVOPS_EXTENSION_SHA256 is required}"
: "${BICEP_VERSION:?BICEP_VERSION is required}"
: "${BICEP_NUGET_SHA256:?BICEP_NUGET_SHA256 is required}"
: "${GOOGLE_CLOUD_CLI_VERSION:?GOOGLE_CLOUD_CLI_VERSION is required}"
: "${GOOGLE_CLOUD_CLI_ARCHIVE_SHA256:?GOOGLE_CLOUD_CLI_ARCHIVE_SHA256 is required}"
: "${NVM_VERSION:?NVM_VERSION is required}"
: "${NVM_ARCHIVE_SHA256:?NVM_ARCHIVE_SHA256 is required}"
: "${AWS_CLI_VERSION:?AWS_CLI_VERSION is required}"
: "${AWS_CLI_ARCHIVE_SHA256:?AWS_CLI_ARCHIVE_SHA256 is required}"
: "${AWS_SESSION_MANAGER_PLUGIN_VERSION:?AWS_SESSION_MANAGER_PLUGIN_VERSION is required}"
: "${AWS_SESSION_MANAGER_PLUGIN_DEB_SHA256:?AWS_SESSION_MANAGER_PLUGIN_DEB_SHA256 is required}"
: "${AWS_SAM_CLI_VERSION:?AWS_SAM_CLI_VERSION is required}"
: "${AWS_SAM_CLI_ARCHIVE_SHA256:?AWS_SAM_CLI_ARCHIVE_SHA256 is required}"
: "${GITHUB_CLI_VERSION:?GITHUB_CLI_VERSION is required}"
: "${GITHUB_CLI_DEB_SHA256:?GITHUB_CLI_DEB_SHA256 is required}"
: "${YQ_VERSION:?YQ_VERSION is required}"
: "${YQ_BINARY_SHA256:?YQ_BINARY_SHA256 is required}"
: "${ZSTD_VERSION:?ZSTD_VERSION is required}"
: "${ZSTD_ARCHIVE_SHA256:?ZSTD_ARCHIVE_SHA256 is required}"
: "${DOCKER_GPG_SHA256:?DOCKER_GPG_SHA256 is required}"
: "${DOCKER_GPG_FINGERPRINT:?DOCKER_GPG_FINGERPRINT is required}"
export PATH="/usr/local/share/qiniu-sandbox-runner-template:${PATH}"
# shellcheck source=/dev/null
source /usr/local/share/qiniu-sandbox-runner-template/setup-common.sh

configure_reliable_apt_sources() {
  cat >/etc/apt/apt-mirrors.txt <<'APT_MIRRORS'
https://archive.ubuntu.com/ubuntu/	priority:1
https://mirrors.edge.kernel.org/ubuntu/	priority:2
https://mirrors.tuna.tsinghua.edu.cn/ubuntu/	priority:3
APT_MIRRORS
  local source_file
  for source_file in /etc/apt/sources.list /etc/apt/sources.list.d/ubuntu.sources; do
    [ -f "$source_file" ] || continue
    sed -i \
      -e 's|http://azure.archive.ubuntu.com/ubuntu|mirror+file:/etc/apt/apt-mirrors.txt|g' \
      -e 's|http://archive.ubuntu.com/ubuntu|mirror+file:/etc/apt/apt-mirrors.txt|g' \
      -e 's|https://archive.ubuntu.com/ubuntu|mirror+file:/etc/apt/apt-mirrors.txt|g' \
      -e 's|http://security.ubuntu.com/ubuntu|mirror+file:/etc/apt/apt-mirrors.txt|g' \
      -e 's|https://security.ubuntu.com/ubuntu|mirror+file:/etc/apt/apt-mirrors.txt|g' \
      "$source_file"
  done
  cat >/etc/apt/apt.conf.d/80qiniu-network <<'APT_NETWORK'
Acquire::Retries "5";
Acquire::http::Timeout "30";
Acquire::https::Timeout "30";
APT_NETWORK
}

ensure_upstream_apt_source_layout() {
  # Canonical's ECR rootfs remains apt-functional through sources.list, while
  # runner-images' Ubuntu 24 slim setup unconditionally rewrites the deb822
  # path used by its Azure image. Keep the working source and provide only the
  # expected path; configure_reliable_apt_sources updates the active source.
  install -d -m 0755 /etc/apt/sources.list.d
  if [ ! -e /etc/apt/sources.list.d/ubuntu.sources ]; then
    install -m 0644 /dev/null /etc/apt/sources.list.d/ubuntu.sources
  fi
}

if phase_selected bootstrap; then
apt-get update
apt-get install -y --no-install-recommends ca-certificates
configure_reliable_apt_sources
apt-get update
apt-get install -y --no-install-recommends \
  curl gpg jq lsb-release man-db sudo tar wget xz-utils

if ! id -u runner >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash runner
fi
usermod -aG sudo runner
echo "runner ALL=(ALL) NOPASSWD:ALL" >/etc/sudoers.d/90-runner
chmod 0440 /etc/sudoers.d/90-runner
install -d -o runner -g runner \
  /home/runner/.config/git \
  /home/runner/actions-runner \
  /home/runner/work \
  /home/runner/_runnerd-hooks \
  /opt/actions-runner

  download_checked \
    "https://codeload.github.com/actions/runner-images/tar.gz/${RUNNER_IMAGES_REV}" \
    /tmp/runner-images.tar.gz \
    "$RUNNER_IMAGES_ARCHIVE_SHA256"
  mkdir -p /opt/qiniu-runner-images
  tar -xzf /tmp/runner-images.tar.gz -C /opt/qiniu-runner-images --strip-components=1
fi
test -d /opt/qiniu-runner-images

export DEBIAN_FRONTEND=noninteractive
export HELPER_SCRIPTS=/opt/qiniu-runner-images/images/ubuntu-slim/scripts/helpers
export INSTALLER_SCRIPT_FOLDER=/opt/qiniu-runner-images/images/ubuntu-slim/toolsets
export IMAGE_VERSION="${IMAGE_VERSION:-${ImageVersion:-local}}"
export IMAGE_OS=ubuntu24

if [ "${TEMPLATE_FLAVOR:-}" = slim ]; then
  upstream_build=/opt/qiniu-runner-images/images/ubuntu-slim/scripts/build
  if phase_selected platform; then
    ensure_upstream_apt_source_layout
    install_azcopy_from_microsoft_package
  fi
  for installer in \
    configure-apt-sources.sh \
    configure-apt.sh \
    install-apt-vital.sh \
    install-ms-repos.sh \
    configure-image-data-file.sh \
    configure-environment.sh \
    install-apt-common.sh \
    install-azure-cli.sh \
    install-bicep.sh \
    install-aws-tools.sh \
    install-git.sh \
    install-git-lfs.sh \
    install-github-cli.sh \
    install-google-cloud-cli.sh \
    install-nvm.sh \
    install-nodejs.sh \
    install-powershell.sh \
    configure-dpkg.sh \
    install-yq.sh \
    install-python.sh \
    install-zstd.sh \
    install-pipx-packages.sh \
    install-docker-cli.sh \
    configure-system.sh; do
    should_run_installer "$installer" || continue
    run_upstream_installer "$upstream_build/$installer"
    if [ "$installer" = configure-apt-sources.sh ]; then
      configure_reliable_apt_sources
    fi
    if [ "$installer" = install-azure-cli.sh ]; then
      install_azure_devops_extension
    fi
  done
  if phase_selected toolchain; then
    ln -sfn /etc/skel/.nvm /home/runner/.nvm
  fi
else
  . /etc/os-release
  export IMAGE_OS="ubuntu${VERSION_ID/.}"
  if phase_selected bootstrap; then
  install -d -m 0755 /tmp/qiniu-runner-build-tools
  cat >/tmp/qiniu-runner-build-tools/systemctl <<'SYSTEMCTL'
#!/bin/sh
action=${1:-}
if [ "$action" = --version ]; then
  exec /usr/bin/systemctl "$@"
fi
shift || true
if [ "$action" = is-active ] && [ "${1:-}" = --quiet ]; then
  shift
fi
unit=${1:-}
unit=${unit%.service}
run_isolated() {
  /usr/bin/python3 - "$@" <<'PYTHON'
import subprocess
import sys

try:
    result = subprocess.run(sys.argv[1:], stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, close_fds=True, timeout=30)
except subprocess.TimeoutExpired:
    sys.exit(124)
sys.exit(result.returncode)
PYTHON
}
case "$unit:$action" in
  apache2:start|apache2:stop|apache2:restart)
    run_isolated /usr/sbin/apachectl "$action"
    exit $?
    ;;
  apache2:is-active)
    test -s /run/apache2/apache2.pid && kill -0 "$(cat /run/apache2/apache2.pid)" 2>/dev/null
    exit $?
    ;;
  nginx:start)
    run_isolated /usr/sbin/nginx
    exit $?
    ;;
  nginx:stop)
    run_isolated /usr/sbin/nginx -s quit
    exit $?
    ;;
  nginx:restart)
    if test -s /run/nginx.pid && kill -0 "$(cat /run/nginx.pid)" 2>/dev/null; then
      run_isolated /usr/sbin/nginx -s reload
      exit $?
    fi
    run_isolated /usr/sbin/nginx
    exit $?
    ;;
  nginx:is-active)
    test -s /run/nginx.pid && kill -0 "$(cat /run/nginx.pid)" 2>/dev/null
    exit $?
    ;;
esac
# VM-oriented upstream installers assume service operations always return. Bound
# SysV calls so a missing init environment cannot wedge a Sandbox build forever.
# Isolate their file descriptors so a daemon cannot keep Pester's capture pipe
# open after the service command exits or is killed.
service_status() {
  run_isolated /usr/sbin/service "$unit" status
}
case "$action" in
  start|stop|restart)
    if [ -x "/etc/init.d/$unit" ]; then
      run_isolated /usr/sbin/service "$unit" "$action"
      service_result=$?
      if [ "$service_result" -eq 0 ]; then
        exit 0
      fi
      if service_status; then
        [ "$action" = stop ] && exit "$service_result"
        exit 0
      fi
      [ "$action" = stop ] && exit 0
      exit "$service_result"
    fi
    ;;
  is-active)
    if [ -x "/etc/init.d/$unit" ]; then
      service_status
      exit $?
    fi
    ;;
esac
echo "qiniu runner template build: skipping VM-only systemctl $action $*" >&2
exit 0
SYSTEMCTL
  chmod 0755 /tmp/qiniu-runner-build-tools/systemctl
  ln -s /tmp/qiniu-runner-build-tools/systemctl /usr/local/bin/systemctl
  echo 'APT::Get::Assume-Yes "true";' >/etc/apt/apt.conf.d/90assumeyes
  install -d -m 0755 /etc/cloud/templates
  cat >/etc/waagent.conf <<'WAAGENT'
ResourceDisk.Format=n
ResourceDisk.EnableSwap=n
ResourceDisk.SwapSizeMB=0
WAAGENT
  fi
  export PATH="/tmp/qiniu-runner-build-tools:$PATH"
  release_digits="${VERSION_ID/.}"
  upstream_build=/opt/qiniu-runner-images/images/ubuntu/scripts/build
  export HELPER_SCRIPTS=/opt/qiniu-runner-images/images/ubuntu/scripts/helpers
  export INSTALLER_SCRIPT_FOLDER=/opt/qiniu-runner-images/images/ubuntu/scripts/build
  if phase_selected bootstrap; then
  cp "/opt/qiniu-runner-images/images/ubuntu/toolsets/toolset-${release_digits}.json" \
    "$INSTALLER_SCRIPT_FOLDER/toolset.json"
  install -d -m 0755 /imagegeneration
  cp -a "$HELPER_SCRIPTS" /imagegeneration/helpers
  cp -a "$HELPER_SCRIPTS/../tests" /imagegeneration/tests
  # BuildKit denies the namespace clone used by this one runtime assertion.
  # Keep the three CLI checks here and run the network lifecycle in conformance.
  podman_networking_test=/imagegeneration/tests/Tools.Tests.ps1
  test "$(grep -Fxc '    It "podman networking" -TestCases "podman CNI plugins" {' "$podman_networking_test" || true)" -eq 1
  sed -i 's/    It "podman networking" -TestCases "podman CNI plugins" {/    It "podman networking" -Skip -TestCases "podman CNI plugins" {/' "$podman_networking_test"
  test "$(grep -Fxc '    It "podman networking" -Skip -TestCases "podman CNI plugins" {' "$podman_networking_test" || true)" -eq 1
  test "$(grep -Fxc '    $testCases = @("podman", "buildah", "skopeo") | ForEach-Object { @{ContainerCommand = $_} }' "$podman_networking_test" || true)" -eq 1
  test "$(grep -Fxc '    It "<ContainerCommand>" -TestCases $testCases {' "$podman_networking_test" || true)" -eq 1
  test "$(grep -Fxc '        "$ContainerCommand -v" | Should -ReturnZeroExitCode' "$podman_networking_test" || true)" -eq 1

  bash "$upstream_build/install-ms-repos.sh"
  install_azcopy_from_microsoft_package
  bash "$upstream_build/configure-apt-sources.sh"
  bash "$upstream_build/configure-apt.sh"
  bash "$upstream_build/configure-environment.sh"
  bash "$upstream_build/install-apt-vital.sh"
  bash "$upstream_build/install-powershell.sh"
  pwsh -File "$upstream_build/Install-PowerShellModules.ps1"
  pwsh -File "$upstream_build/Install-PowerShellAzModules.ps1"
  bash "$HELPER_SCRIPTS/invoke-tests.sh" Tools azcopy
  fi

  for installer in \
    install-apt-common.sh \
    install-azure-cli.sh \
    install-bicep.sh \
    install-apache.sh \
    install-aws-tools.sh \
    install-clang.sh \
    install-swift.sh \
    install-cmake.sh \
    install-codeql-bundle.sh \
    install-awf.sh \
    install-container-tools.sh \
    install-dotnetcore-sdk.sh \
    install-microsoft-edge.sh \
    install-gcc-compilers.sh \
    install-firefox.sh \
    install-gfortran.sh \
    install-git.sh \
    install-git-lfs.sh \
    install-github-cli.sh \
    install-google-chrome.sh \
    install-google-cloud-cli.sh \
    install-haskell.sh \
    install-java-tools.sh \
    install-kubernetes-tools.sh \
    install-miniconda.sh \
    install-kotlin.sh \
    install-mysql.sh \
    install-nginx.sh \
    install-nvm.sh \
    install-nodejs.sh \
    install-copilot-cli.sh \
    install-bazel.sh \
    install-php.sh \
    install-postgresql.sh \
    install-pulumi.sh \
    install-ruby.sh \
    install-rust.sh \
    install-julia.sh \
    install-selenium.sh \
    install-packer.sh \
    install-vcpkg.sh \
    configure-dpkg.sh \
    install-yq.sh \
    install-android-sdk.sh \
    install-pypy.sh \
    install-python.sh \
    install-zstd.sh \
    install-ninja.sh; do
    should_run_installer "$installer" || continue
    run_upstream_installer "$upstream_build/$installer"
    if [ "$installer" = install-azure-cli.sh ]; then
      install_azure_devops_extension
      bash "$HELPER_SCRIPTS/invoke-tests.sh" CLI.Tools "Azure DevOps CLI"
    fi
  done

  if [ "$VERSION_ID" = 22.04 ]; then
    for installer in \
      install-aliyun-cli.sh \
      install-heroku.sh \
      install-leiningen.sh \
      install-mssql-tools.sh \
      install-oc-cli.sh \
      install-oras-cli.sh \
      install-rlang.sh \
      install-mono.sh \
      install-sbt.sh \
      install-sqlpackage.sh \
      install-terraform.sh; do
      if [ "$installer" = install-mono.sh ]; then
        # Mono 6.12 JIT aborts when an amd64 image is built through arm64
        # emulation. Scope interpreter mode to installation-time validation;
        # the completed amd64 image retains the native JIT default.
        MONO_ENV_OPTIONS=--interp run_upstream_installer "$upstream_build/$installer"
      else
        run_upstream_installer "$upstream_build/$installer"
      fi
    done
  fi

  pwsh -File "$upstream_build/Install-Toolset.ps1"
  pwsh -File "$upstream_build/Configure-Toolset.ps1"
  run_upstream_installer "$upstream_build/install-pipx-packages.sh"
  sudo -H -u runner \
    HELPER_SCRIPTS="$HELPER_SCRIPTS" \
    INSTALLER_SCRIPT_FOLDER="$INSTALLER_SCRIPT_FOLDER" \
    bash "$upstream_build/install-homebrew.sh"
fi

if phase_selected runtime; then
install_docker_for_sandbox
install_runner
install -m 0755 \
  /usr/local/share/qiniu-sandbox-runner-template/ensure-docker \
  /usr/local/bin/ensure-docker
usermod -aG docker runner
chown -R runner:runner \
  /home/runner \
  /opt/actions-runner \
  /opt/hostedtoolcache
chmod -R a+rX /opt/actions-runner /opt/hostedtoolcache

apt-get clean
find /var/lib/apt/lists -mindepth 1 -delete
find /opt/qiniu-runner-images -mindepth 1 -delete
find /opt/qiniu-runner-images -depth -type d -empty -delete
if [ -d /imagegeneration ]; then
  find /imagegeneration -mindepth 1 -delete
  rmdir /imagegeneration
fi
rm -f /usr/local/bin/systemctl
find /tmp/qiniu-runner-build-tools -mindepth 1 -delete 2>/dev/null || true
rmdir /tmp/qiniu-runner-build-tools 2>/dev/null || true
rm -f /tmp/actions-runner.tar.gz /tmp/docker.gpg /tmp/runner-images.tar.gz
fi
