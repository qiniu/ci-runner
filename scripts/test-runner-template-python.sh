#!/usr/bin/env bash
set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dockerfile="$repository_root/templates/github-runner-ubuntu-26.04/Dockerfile"
base_image="$(awk '/^FROM / {print $NF; exit}' "$dockerfile")"
revision="$(sed -n 's/^ARG RUNNER_IMAGES_REV=//p' "$dockerfile")"

# Run against the actual Ubuntu rootfs and pinned upstream installer. Only the
# environment/Pester helpers are fixtures; pip, venv and pipx are real.
docker run --rm --platform linux/amd64 -i \
  -v "$repository_root/templates/common/scripts:/runner-template:ro" \
  -e RUNNER_IMAGES_REV="$revision" "$base_image" bash -se <<'CONTAINER'
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y --no-install-recommends ca-certificates curl sudo python3 python3-dev python3-pip python3-venv python3-packaging
export HELPER_SCRIPTS=/tmp/helpers
mkdir -p "$HELPER_SCRIPTS" /tmp/upstream
cat >"$HELPER_SCRIPTS/etc-environment.sh" <<'HELPERS'
set_etc_environment_variable() {
  printf '%s=%s\n' "$1" "$2" >>/tmp/runner-environment
}
prepend_etc_environment_path() { :; }
invoke_tests() { printf '%s\n' "$*" >>/tmp/upstream-tests; }
HELPERS
echo 'is_ubuntu22() { return 1; }' >"$HELPER_SCRIPTS/os.sh"
curl --fail --silent --show-error --location --max-time 60 \
  "https://raw.githubusercontent.com/actions/runner-images/${RUNNER_IMAGES_REV}/images/ubuntu/scripts/build/install-python.sh" \
  -o /tmp/upstream/install-python.sh
source /runner-template/setup-common.sh

# The conflicting distribution is apt-owned and cannot be uninstalled by pip.
python3 -c 'import importlib.metadata as m; assert m.distribution("packaging").read_text("RECORD") is None'
dpkg-query -W python3-packaging >/tmp/packaging-before
find /usr/lib/python3/dist-packages/packaging* -type f -exec sha256sum {} + | sort >/tmp/packaging-hashes-before
run_upstream_installer /tmp/upstream/install-python.sh
pipx --version
test "$(readlink /usr/local/bin/pipx)" = /opt/pipx-bootstrap/bin/pipx
/opt/pipx-bootstrap/bin/python -c 'import packaging, sys; assert packaging.__file__.startswith(sys.prefix + "/")'
dpkg-query -W python3-packaging | diff /tmp/packaging-before -
find /usr/lib/python3/dist-packages/packaging* -type f -exec sha256sum {} + | sort | diff /tmp/packaging-hashes-before -
grep -Fx 'PIPX_BIN_DIR=/opt/pipx_bin' /tmp/runner-environment
grep -Fx 'PIPX_HOME=/opt/pipx' /tmp/runner-environment
grep -Fx 'Tools Python' /tmp/upstream-tests

# Later tool installs and an ordinary-user shell must use the isolated pipx.
export PIPX_HOME=/opt/pipx PIPX_BIN_DIR=/opt/pipx_bin
pipx install --python /usr/bin/python3 --pip-args='--timeout 120 --retries 10' pycowsay
test -x /opt/pipx_bin/pycowsay
test -d /opt/pipx/venvs/pycowsay
sudo -u nobody /usr/local/bin/pipx --version

# Source drift and installer failures must still stop the build.
echo '# changed upstream installer' >/tmp/upstream/install-python.sh
if run_upstream_installer /tmp/upstream/install-python.sh; then
  echo 'unexpected success after upstream source drift' >&2
  exit 1
fi
printf 'set -e\nfalse\npython3 -m pip install pipx\npython3 -m pipx ensurepath\n' >/tmp/upstream/install-python.sh
if RUNNER_TEMPLATE_UPSTREAM_INSTALL_ATTEMPTS=1 run_upstream_installer /tmp/upstream/install-python.sh; then
  echo 'unexpected success after installer failure' >&2
  exit 1
fi
test -z "$(find /tmp -maxdepth 1 -name 'qiniu-install-python.*' -print -quit)"
echo 'runner template Python isolation regression passed'
CONTAINER
