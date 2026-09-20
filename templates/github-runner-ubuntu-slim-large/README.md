# GitHub runner Ubuntu Slim large template

This large variant reuses the pinned Ubuntu Slim Dockerfile, toolset, scripts,
and software report from the standard template. It keeps the same 8-vCPU and
8192-MiB build settings and requests 80 GiB of build free space when first created.

The image supplies the same non-root `runner` account, hosted tool cache,
checksum-pinned GitHub Actions runner, and Sandbox Docker daemon/socket
adaptation as the standard template. The tracked qshell configuration requests
`disk_size_mb = 81920`; the provider must accept that allocation.

Build with `task template-build-ubuntu-slim-large`. After the provider team's
`DiskMb` is at least 81,920 MiB, this rebuilds the existing template name and
ID in place. Qshell does not send `disk_size_mb` during the rebuild, so the
build helper does not reject the stale pre-rebuild total; publish and catalog
checks require the rebuilt total to meet the configured lower bound. This
tracked file does not contain a region-specific template ID. See the standard template's
[`software-diff.md`](../github-runner-ubuntu-slim/software-diff.md) and the
repository-level compatibility manifest for the verified contract.
