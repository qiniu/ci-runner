# GitHub runner Ubuntu Slim template

This 8-vCPU, 8192-MiB Linux x86_64 template requests 20 GiB of build free
space when created. It is derived from the pinned upstream Ubuntu Slim Dockerfile,
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

Build with Task 8's `task template-build-ubuntu-slim`; this tracked file does
not contain a region-specific template ID. See
[`software-diff.md`](software-diff.md) and the repository-level compatibility
manifest for the verified contract.
