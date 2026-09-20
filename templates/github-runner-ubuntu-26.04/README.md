# GitHub runner Ubuntu 26.04 template

This preview 8-vCPU, 8192-MiB Linux x86_64 template sets a 20-GiB minimum root
disk size when created. It uses the pinned Canonical-published Ubuntu 26.04 OCI index in
Amazon ECR Public and the disk-bounded Ubuntu Slim-compatible core from
`actions/runner-images@e986db797519f06a2e5e53701a715cfa4c1545e8`.
Apache and the Podman/Buildah/Skopeo container tools are explicit extensions.

The image supplies the `runner` account, `/home/runner/work`,
`/opt/hostedtoolcache`, a checksum-pinned GitHub Actions runner under
`/opt/actions-runner`, and the Sandbox Docker daemon/socket adaptation. The
runner process normally executes as non-root. Root is used only during image
construction and when `ensure-docker` must initialize the daemon/socket.

Build the standard template with `task template-build-ubuntu-26-04`, using
`qshell.sandbox.toml`. Build the large variant with
`task template-build-ubuntu-26-04-large`, using
`qshell.sandbox.large.toml`. Both configs use this directory's Dockerfile and
request 8 vCPUs and 8192 MiB of memory; their minimum disk sizes are 20 GiB
and 80 GiB, respectively.

Before rebuilding the large name in place, set the provider team's `DiskMb` to
the required effective allocation. Qshell does not send `disk_size_mb` during
same-name rebuilds, so the build helper permits the rebuild and the publish and
catalog gates verify its resulting total capacity. Neither tracked config
contains a region-specific template ID. See
[`software-diff.md`](software-diff.md) and the repository-level compatibility
manifest for the verified contract.
