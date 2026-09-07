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

phase_selected() {
  [ "$runner_template_phase" = all ] || [ "$runner_template_phase" = "$1" ]
}

installer_phase() {
  case "$1" in
    configure-apt-sources.sh | configure-apt.sh | install-apt-vital.sh | install-ms-repos.sh | configure-image-data-file.sh | configure-environment.sh | install-apt-common.sh | install-azure-cli.sh | install-bicep.sh | install-apache.sh | install-aws-tools.sh | install-container-tools.sh | install-git.sh | install-git-lfs.sh | install-github-cli.sh | install-google-cloud-cli.sh)
      echo platform
      ;;
    *) echo toolchain ;;
  esac
}

should_run_installer() {
  phase_selected "$(installer_phase "$1")"
}

: "${RUNNER_IMAGES_REV:?RUNNER_IMAGES_REV is required}"
: "${RUNNER_IMAGES_ARCHIVE_SHA256:?RUNNER_IMAGES_ARCHIVE_SHA256 is required}"
: "${RUNNER_VERSION:?RUNNER_VERSION is required}"
: "${RUNNER_ARCHIVE_SHA256:?RUNNER_ARCHIVE_SHA256 is required}"
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

download_checked() {
  local url="$1"
  local destination="$2"
  local expected_sha256="$3"
  local attempts="${RUNNER_TEMPLATE_DOWNLOAD_ATTEMPTS:-20}"
  local retry_delay="${RUNNER_TEMPLATE_DOWNLOAD_RETRY_DELAY:-2}"
  local retry_max_delay="${RUNNER_TEMPLATE_DOWNLOAD_RETRY_MAX_DELAY:-30}"
  local attempt
  local curl_status

  touch "$destination"
  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if [ -s "$destination" ] &&
      echo "$expected_sha256  $destination" | sha256sum --check - >/dev/null 2>&1; then
      return
    fi
    if /usr/bin/curl --http1.1 -fsSL --connect-timeout 15 --max-time 1800 \
      --speed-limit 1024 --speed-time 60 --continue-at - \
      "$url" -o "$destination"; then
      if echo "$expected_sha256  $destination" | sha256sum --check -; then
        return
      fi
      echo "download checksum mismatch; restarting from byte zero" >&2
      rm -f "$destination"
      touch "$destination"
    else
      curl_status=$?
      if [ "$curl_status" -eq 33 ]; then
        echo "download server rejected resume; restarting from byte zero" >&2
        rm -f "$destination"
        touch "$destination"
      fi
    fi
    if [ "$attempt" -lt "$attempts" ]; then
      echo "download interrupted; resuming (${attempt}/${attempts})" >&2
      if [ "$retry_delay" != 0 ]; then
        sleep "$retry_delay"
        if [ "$retry_delay" -lt "$retry_max_delay" ]; then
          retry_delay=$((retry_delay * 2))
          if [ "$retry_delay" -gt "$retry_max_delay" ]; then
            retry_delay="$retry_max_delay"
          fi
        fi
      fi
    fi
  done
  echo "download failed after ${attempts} attempts: $url" >&2
  return 1
}

run_upstream_tests_if_available() {
  local test_script="$HELPER_SCRIPTS/invoke-tests.sh"
  if [ -f "$test_script" ]; then
    bash "$test_script" "$1" "$2"
  fi
}

install_azure_cli_from_microsoft_package() {
  local package=/tmp/azure-cli.deb
  local suite
  local expected_sha256
  # shellcheck disable=SC1091
  source /etc/os-release
  case "$VERSION_ID" in
    22.04)
      suite=jammy
      expected_sha256="$AZURE_CLI_JAMMY_DEB_SHA256"
      ;;
    24.04 | 26.04)
      suite=noble
      expected_sha256="$AZURE_CLI_NOBLE_DEB_SHA256"
      ;;
    *)
      echo "unsupported Ubuntu release for Azure CLI fallback: $VERSION_ID" >&2
      return 1
      ;;
  esac

  rm -f /etc/apt/sources.list.d/azure-cli.list \
    /etc/apt/sources.list.d/azure-cli.list.save \
    /etc/apt/sources.list.d/azure-cli.sources
  download_checked \
    "https://packages.microsoft.com/repos/azure-cli/pool/main/a/azure-cli/azure-cli_${AZURE_CLI_VERSION}-1~${suite}_amd64.deb" \
    "$package" \
    "$expected_sha256"
  apt-get install -y --no-install-recommends "$package"
  rm -f "$package"
  test "$(az version --query '"azure-cli"' --output tsv)" = "$AZURE_CLI_VERSION"
  run_upstream_tests_if_available "CLI.Tools" "Azure CLI"
}

install_bicep_from_nuget() {
  local package_name="azure.bicep.commandline.linux-x64.${BICEP_VERSION}.nupkg"
  local package_path="/tmp/${package_name}"
  local package_url="https://api.nuget.org/v3-flatcontainer/azure.bicep.commandline.linux-x64/${BICEP_VERSION}/${package_name}"
  local extract_dir=/tmp/qiniu-bicep

  download_checked "$package_url" "$package_path" "$BICEP_NUGET_SHA256"
  install -d -m 0755 "$extract_dir"
  unzip -q -j "$package_path" tools/bicep -d "$extract_dir"
  install -m 0755 "$extract_dir/bicep" /usr/local/bin/bicep
  find "$extract_dir" -mindepth 1 -delete
  rmdir "$extract_dir"
  rm -f "$package_path"
  bicep --version | grep -F "Bicep CLI version ${BICEP_VERSION} "
  run_upstream_tests_if_available "Tools" "Bicep"
}

install_git_lfs_from_ubuntu() {
  apt-get update
  apt-get install -y --no-install-recommends git-lfs
  git lfs version
  run_upstream_tests_if_available "Tools" "Git-lfs"
}

install_google_cloud_cli_from_archive() {
  local archive_name="google-cloud-cli-${GOOGLE_CLOUD_CLI_VERSION}-linux-x86_64.tar.gz"
  local archive_path="/tmp/${archive_name}"
  local archive_url="https://dl.google.com/dl/cloudsdk/channels/rapid/downloads/${archive_name}"

  rm -f /etc/apt/sources.list.d/google-cloud-sdk.list /usr/share/keyrings/cloud.google.gpg
  download_checked "$archive_url" "$archive_path" "$GOOGLE_CLOUD_CLI_ARCHIVE_SHA256"
  rm -rf /opt/google-cloud-sdk
  tar -xzf "$archive_path" -C /opt
  rm -f "$archive_path"
  /opt/google-cloud-sdk/install.sh --quiet --usage-reporting false \
    --path-update false --bash-completion false --command-completion false

  local bin
  for bin in bq docker-credential-gcloud gcloud gcloud-crc32c git-credential-gcloud.sh gsutil; do
    test -e "/opt/google-cloud-sdk/bin/$bin" || continue
    ln -sf "/opt/google-cloud-sdk/bin/$bin" "/usr/bin/$bin"
  done
  echo "google-cloud-sdk $archive_url" >>"$HELPER_SCRIPTS/apt-sources.txt"
  gcloud --version
}

install_nvm_from_archive() {
  local archive_name="nvm-v${NVM_VERSION}.tar.gz"
  local archive_path="/tmp/${archive_name}"
  local nvm_dir=/etc/skel/.nvm

  download_checked \
    "https://codeload.github.com/nvm-sh/nvm/tar.gz/refs/tags/v${NVM_VERSION}" \
    "$archive_path" \
    "$NVM_ARCHIVE_SHA256"
  install -d -m 0755 "$nvm_dir"
  tar -xzf "$archive_path" -C "$nvm_dir" --strip-components=1
  rm -f "$archive_path"

  source "$HELPER_SCRIPTS/etc-environment.sh"
  set_etc_environment_variable "NVM_DIR" '$HOME/.nvm'
  echo '[ -s "$NVM_DIR/nvm.sh" ] && \. "$NVM_DIR/nvm.sh"  # This loads nvm' >>/etc/skel/.bash_profile
  echo 'source "$NVM_DIR/nvm.sh"' >>/etc/skel/.bashrc

  export NVM_DIR="$nvm_dir"
  # shellcheck disable=SC1091
  source "$NVM_DIR/nvm.sh"
  test "$(nvm --version)" = "$NVM_VERSION"
  nvm alias default system
}

install_aws_tools_from_checked_archives() {
  local aws_archive=/tmp/qiniu-awscliv2.zip
  local aws_extract=/tmp/qiniu-awscliv2
  local session_manager_package=/tmp/qiniu-session-manager-plugin.deb
  local sam_archive=/tmp/qiniu-aws-sam-cli.zip
  local sam_extract=/tmp/qiniu-aws-sam-cli

  download_checked \
    "https://awscli.amazonaws.com/awscli-exe-linux-x86_64-${AWS_CLI_VERSION}.zip" \
    "$aws_archive" \
    "$AWS_CLI_ARCHIVE_SHA256"
  rm -rf "$aws_extract"
  install -d -m 0755 "$aws_extract"
  unzip -q "$aws_archive" -d "$aws_extract"
  "$aws_extract/aws/install" -i /usr/local/aws-cli -b /usr/local/bin
  rm -rf "$aws_extract" "$aws_archive"
  aws --version 2>&1 | grep -F "aws-cli/${AWS_CLI_VERSION} "

  download_checked \
    "https://s3.amazonaws.com/session-manager-downloads/plugin/${AWS_SESSION_MANAGER_PLUGIN_VERSION}/ubuntu_64bit/session-manager-plugin.deb" \
    "$session_manager_package" \
    "$AWS_SESSION_MANAGER_PLUGIN_DEB_SHA256"
  apt-get install -y --no-install-recommends "$session_manager_package"
  rm -f "$session_manager_package"
  test "$(session-manager-plugin --version)" = "$AWS_SESSION_MANAGER_PLUGIN_VERSION"

  download_checked \
    "https://github.com/aws/aws-sam-cli/releases/download/v${AWS_SAM_CLI_VERSION}/aws-sam-cli-linux-x86_64.zip" \
    "$sam_archive" \
    "$AWS_SAM_CLI_ARCHIVE_SHA256"
  rm -rf "$sam_extract"
  install -d -m 0755 "$sam_extract"
  unzip -q "$sam_archive" -d "$sam_extract"
  "$sam_extract/install"
  rm -rf "$sam_extract" "$sam_archive"
  test "$(sam --version)" = "SAM CLI, version ${AWS_SAM_CLI_VERSION}"

  run_upstream_tests_if_available "CLI.Tools" "AWS"
}

install_github_cli_from_checked_package() {
  local package=/tmp/qiniu-github-cli.deb

  download_checked \
    "https://github.com/cli/cli/releases/download/v${GITHUB_CLI_VERSION}/gh_${GITHUB_CLI_VERSION}_linux_amd64.deb" \
    "$package" \
    "$GITHUB_CLI_DEB_SHA256"
  apt-get install -y --no-install-recommends "$package"
  rm -f "$package"
  test "$(gh --version | awk 'NR == 1 { print $3 }')" = "$GITHUB_CLI_VERSION"
  run_upstream_tests_if_available "CLI.Tools" "GitHub CLI"
}

install_yq_from_checked_binary() {
  local binary=/tmp/qiniu-yq

  download_checked \
    "https://github.com/mikefarah/yq/releases/download/v${YQ_VERSION}/yq_linux_amd64" \
    "$binary" \
    "$YQ_BINARY_SHA256"
  install -m 0755 "$binary" /usr/bin/yq
  rm -f "$binary"
  test "$(yq --version | awk '{ print $4 }' | sed 's/^v//')" = "$YQ_VERSION"
  run_upstream_tests_if_available "Tools" "yq"
}

install_zstd_from_checked_archive() {
  local archive=/tmp/qiniu-zstd.tar.gz
  local source_dir="/tmp/zstd-${ZSTD_VERSION}"

  download_checked \
    "https://github.com/facebook/zstd/releases/download/v${ZSTD_VERSION}/zstd-${ZSTD_VERSION}.tar.gz" \
    "$archive" \
    "$ZSTD_ARCHIVE_SHA256"
  rm -rf "$source_dir"
  tar -xzf "$archive" -C /tmp
  apt-get update
  apt-get install -y --no-install-recommends liblz4-dev
  make -C "$source_dir/contrib/pzstd" -j "$(nproc)" all
  make -C "$source_dir" -j "$(nproc)" zstd-release
  for executable in zstd zstdless zstdgrep; do
    install -m 0755 "$source_dir/programs/$executable" "/usr/local/bin/$executable"
  done
  install -m 0755 "$source_dir/contrib/pzstd/pzstd" /usr/local/bin/pzstd
  for symlink in zstdcat zstdmt unzstd; do
    ln -sf /usr/local/bin/zstd "/usr/local/bin/$symlink"
  done
  rm -rf "$source_dir" "$archive"
  zstd --version | grep -F "v${ZSTD_VERSION}"
  run_upstream_tests_if_available "Tools" "Zstd"
}

install_ninja_from_checked_archive() {
  local archive=/tmp/qiniu-ninja.zip
  local extract_dir=/tmp/qiniu-ninja

  download_checked \
    "https://github.com/ninja-build/ninja/releases/download/v${NINJA_VERSION}/ninja-linux.zip" \
    "$archive" \
    "$NINJA_ARCHIVE_SHA256"
  rm -rf "$extract_dir"
  install -d -m 0755 "$extract_dir"
  unzip -q "$archive" -d "$extract_dir"
  install -m 0755 "$extract_dir/ninja" /usr/local/bin/ninja
  rm -rf "$extract_dir" "$archive"
  test "$(ninja --version)" = "$NINJA_VERSION"
  run_upstream_tests_if_available "Tools" "Ninja"
}

run_retryable_upstream_installer() {
  local installer_path="$1"
  local installer_name="${installer_path##*/}"
  local attempts="${RUNNER_TEMPLATE_UPSTREAM_INSTALL_ATTEMPTS:-3}"
  local retry_delay="${RUNNER_TEMPLATE_UPSTREAM_INSTALL_RETRY_DELAY:-2}"
  local attempt

  for ((attempt = 1; attempt <= attempts; attempt++)); do
    if PIP_DEFAULT_TIMEOUT="${PIP_DEFAULT_TIMEOUT:-120}" \
      PIP_RETRIES="${PIP_RETRIES:-10}" \
      bash "$installer_path"; then
      return 0
    fi
    if [ "$attempt" -lt "$attempts" ]; then
      echo "upstream installer interrupted; retrying $installer_name (${attempt}/${attempts})" >&2
      if [ "$retry_delay" != 0 ]; then
        sleep "$retry_delay"
      fi
    fi
  done
  echo "upstream installer failed after ${attempts} attempts: $installer_name" >&2
  return 1
}

run_upstream_installer() {
  local installer_path="$1"
  local installer_name="${installer_path##*/}"
  case "$installer_name" in
    install-azure-cli.sh)
      install_azure_cli_from_microsoft_package
      return
      ;;
    install-aws-tools.sh)
      install_aws_tools_from_checked_archives
      return
      ;;
    install-bicep.sh)
      install_bicep_from_nuget
      return
      ;;
    install-container-tools.sh)
      RUNNER_TEMPLATE_DIRECT_GITHUB_ASSETS=1 bash "$installer_path"
      return
      ;;
    install-git-lfs.sh)
      install_git_lfs_from_ubuntu
      return
      ;;
    install-github-cli.sh)
      install_github_cli_from_checked_package
      return
      ;;
    install-google-cloud-cli.sh)
      install_google_cloud_cli_from_archive
      return
      ;;
    install-nvm.sh)
      install_nvm_from_archive
      return
      ;;
    install-yq.sh)
      install_yq_from_checked_binary
      return
      ;;
    install-zstd.sh)
      install_zstd_from_checked_archive
      return
      ;;
    install-ninja.sh)
      install_ninja_from_checked_archive
      return
      ;;
    install-python.sh | install-pipx-packages.sh)
      if run_retryable_upstream_installer "$installer_path"; then
        return 0
      fi
      return 1
      ;;
  esac

  if bash "$installer_path"; then
    return 0
  fi
  if [ "$installer_name" = install-docker-cli.sh ]; then
    echo "upstream Docker CLI installer unavailable; deferring to sandbox-aware installer" >&2
    return 0
  fi
  echo "upstream installer failed: $installer_name" >&2
  return 1
}

install_azcopy_from_microsoft_package() {
  local package=/tmp/azcopy.deb
  download_checked \
    "https://packages.microsoft.com/ubuntu/24.04/prod/pool/main/a/azcopy/azcopy_${AZCOPY_VERSION}_amd64.deb" \
    "$package" \
    "$AZCOPY_DEB_SHA256"
  install -d -m 0755 /etc/apt/preferences.d
  cat >/etc/apt/preferences.d/qiniu-azcopy <<APT_PREFERENCE
Package: azcopy
Pin: version ${AZCOPY_VERSION}
Pin-Priority: 1001
APT_PREFERENCE
  dpkg -i "$package"
  rm -f "$package"
  ln -sf "$(command -v azcopy)" /usr/local/bin/azcopy10
  test "$(azcopy --version)" = "azcopy version $AZCOPY_VERSION"
}

install_azure_devops_extension() {
  local wheel="/tmp/azure_devops-${AZURE_DEVOPS_EXTENSION_VERSION}-py2.py3-none-any.whl"
  source "$HELPER_SCRIPTS/etc-environment.sh"
  export AZURE_EXTENSION_DIR=/opt/az/azcliextensions
  set_etc_environment_variable "AZURE_EXTENSION_DIR" "$AZURE_EXTENSION_DIR"
  download_checked \
    "https://azcliprod.blob.core.windows.net/cli-extensions/azure_devops-${AZURE_DEVOPS_EXTENSION_VERSION}-py2.py3-none-any.whl" \
    "$wheel" \
    "$AZURE_DEVOPS_EXTENSION_SHA256"
  az extension add --yes --source "$wheel"
  rm -f "$wheel"
  test "$(az extension show --name azure-devops --query version -o tsv)" = "$AZURE_DEVOPS_EXTENSION_VERSION"
}

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

configure_docker_apt_repository() {
  install -d -m 0755 /etc/apt/keyrings
  rm -f /tmp/docker.gpg /etc/apt/keyrings/docker.gpg /etc/apt/sources.list.d/docker.list
  download_checked https://download.docker.com/linux/ubuntu/gpg /tmp/docker.gpg "$DOCKER_GPG_SHA256" || return 1
  local actual_fingerprint
  actual_fingerprint="$(gpg --show-keys --with-colons /tmp/docker.gpg | awk -F: '$1 == "fpr" {print $10; exit}')" || return 1
  if [ "$actual_fingerprint" != "$DOCKER_GPG_FINGERPRINT" ]; then
    echo "Docker repository key fingerprint mismatch" >&2
    return 1
  fi
  gpg --dearmor -o /etc/apt/keyrings/docker.gpg /tmp/docker.gpg || return 1
  chmod a+r /etc/apt/keyrings/docker.gpg || return 1
  . /etc/os-release
  echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] https://download.docker.com/linux/ubuntu ${VERSION_CODENAME} stable" \
    >/etc/apt/sources.list.d/docker.list
  apt-get update || return 1
  apt-cache show docker-ce >/dev/null 2>&1
}

remove_official_docker_packages() {
  local official_docker_packages=(
    containerd.io
    docker-buildx-plugin
    docker-ce
    docker-ce-cli
    docker-ce-rootless-extras
    docker-compose-plugin
  )
  if ! apt-get purge -y "${official_docker_packages[@]}"; then
    dpkg --purge --force-depends "${official_docker_packages[@]}" || true
  fi
  apt-get -f install -y
}

install_docker_for_sandbox() {
  if configure_docker_apt_repository && \
    apt-get install -y --no-install-recommends \
      containerd.io docker-buildx-plugin docker-ce docker-ce-cli docker-compose-plugin; then
    echo "installed Docker packages from the official Docker repository"
  else
    echo "official Docker packages unavailable; using Ubuntu archive packages" >&2
    remove_official_docker_packages
    rm -f /etc/apt/sources.list.d/docker.list /etc/apt/keyrings/docker.gpg
    apt-get update
    apt-get install -y --no-install-recommends docker.io docker-buildx docker-compose-v2
  fi
  docker --version
  docker buildx version
  docker compose version
}

install_runner() {
  install -d -m 0755 /opt/actions-runner
  download_checked \
    "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/actions-runner-linux-x64-${RUNNER_VERSION}.tar.gz" \
    /tmp/actions-runner.tar.gz \
    "$RUNNER_ARCHIVE_SHA256"
  tar -xzf /tmp/actions-runner.tar.gz -C /opt/actions-runner
  /opt/actions-runner/bin/installdependencies.sh
  test -x /opt/actions-runner/config.sh
  test -x /opt/actions-runner/run.sh
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
