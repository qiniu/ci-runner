# GitHub runner Ubuntu 24.04 large template

This large variant reuses the pinned Ubuntu 24.04 Dockerfile, toolset, scripts,
and software report from the standard template. It keeps the same 8-vCPU and
8192-MiB build settings while using an 80-GiB provider disk allocation.

The image supplies the same non-root `runner` account, hosted tool cache,
checksum-pinned GitHub Actions runner, and Sandbox Docker daemon/socket
adaptation as the standard template. Disk size is controlled by the Sandbox
provider allocation, not qshell configuration.

Build with `task template-build-ubuntu-24-04-large` after setting the provider
build allocation to 81,920 MiB. This tracked file does not contain a
region-specific template ID. See the standard template's
[`software-diff.md`](../github-runner-ubuntu-24.04/software-diff.md) and the
repository-level compatibility manifest for the verified contract.
