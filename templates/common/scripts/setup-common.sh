#!/usr/bin/env bash
# Shared functions for all four public Ubuntu runner templates.
# Sourced by each template setup script before any build phase runs.

phase_selected() {
  [ "$runner_template_phase" = all ] || [ "$runner_template_phase" = "$1" ]
}

should_run_installer() {
  phase_selected "$(installer_phase "$1")"
}

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
  # shellcheck source=/dev/null
  source /usr/local/share/qiniu-sandbox-runner-template/actions-runner.env
  : "${RUNNER_VERSION:?RUNNER_VERSION is required}"
  : "${RUNNER_ARCHIVE_SHA256:?RUNNER_ARCHIVE_SHA256 is required}"
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
