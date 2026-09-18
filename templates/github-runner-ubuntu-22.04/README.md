# GitHub runner Ubuntu 22.04 template

This 8-vCPU, 8192-MiB Linux x86_64 template requests 20 GiB of build free
space when created. It uses the pinned Canonical-published Ubuntu 22.04 OCI index in
Amazon ECR Public and the disk-bounded Ubuntu Slim-compatible core from
`actions/runner-images@e986db797519f06a2e5e53701a715cfa4c1545e8`.
Apache and the Podman/Buildah/Skopeo container tools are explicit extensions.
It follows upstream Ubuntu 22.04 deprecation.

The image supplies the `runner` account, `/home/runner/work`,
`/opt/hostedtoolcache`, a checksum-pinned GitHub Actions runner under
`/opt/actions-runner`, and the Sandbox Docker daemon/socket adaptation. The
runner process normally executes as non-root. Root is used only during image
construction and when `ensure-docker` must initialize the daemon/socket.

Build with Task 8's `task template-build-ubuntu-22-04`; this tracked file does
not contain a region-specific template ID. See
[`software-diff.md`](software-diff.md) and the repository-level compatibility
manifest for the verified contract.
