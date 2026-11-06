# GitHub runner Ubuntu Slim template

This 8-vCPU, 8192-MiB Linux x86_64 template requires 20 GiB of available space at release. It is derived from the pinned upstream Ubuntu Slim Dockerfile,
toolset, scripts, and software report at
`actions/runner-images@e986db797519f06a2e5e53701a715cfa4c1545e8`.
The base is pinned to the Canonical-published Ubuntu 24.04 OCI index digest in
Amazon ECR Public so Sandbox builders can resolve the same reviewed input in
both supported regions.

The upstream install chain is retained. Qiniu-specific changes add the
non-root `runner` account, `/home/runner/work`, `/opt/hostedtoolcache`, a
checksum-pinned GitHub Actions runner under `/opt/actions-runner`, and an
idempotent Docker daemon/socket helper. Runtime runner registration is
ephemeral.

Build the standard template with `task template-build-ubuntu-slim`, using
`qshell.sandbox.toml`. Build the large variant with
`task template-build-ubuntu-slim-large`, using
`qshell.sandbox.large.toml`. Both configs use this directory's Dockerfile and
request 8 vCPUs and 8192 MiB of memory; release requires 20 GiB and 80 GiB available, respectively.

Qshell applies the free-space setting only on creation. For a same-name
rebuild, verify the available space in a fresh Sandbox before promotion. Neither tracked config
contains a region-specific template ID. See
[`software-diff.md`](software-diff.md) and the repository-level compatibility
manifest for the verified contract.
