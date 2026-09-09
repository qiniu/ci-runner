# Managed GitHub runner templates

## Supported templates

| Workflow label | Physical template | Upstream baseline | Support channel | Publication state |
| --- | --- | --- | --- | --- |
| `ubuntu-slim` | `github-runner-ubuntu-slim` | Ubuntu Slim x64 | stable | verified |
| `ubuntu-22.04` | `github-runner-ubuntu-22-04` | Ubuntu 22.04 x64 | follows upstream deprecation | verified |
| `ubuntu-24.04` | `github-runner-ubuntu-24-04` | Ubuntu 24.04 x64 | stable | verified |
| `ubuntu-26.04` | `github-runner-ubuntu-26-04` | Ubuntu 26.04 x64 | preview | verified |
| `ubuntu-slim-large` | `github-runner-ubuntu-slim-large` | Ubuntu Slim x64 (80 GiB) | large | development |
| `ubuntu-22.04-large` | `github-runner-ubuntu-22-04-large` | Ubuntu 22.04 x64 (80 GiB) | follows upstream deprecation | development |
| `ubuntu-24.04-large` | `github-runner-ubuntu-24-04-large` | Ubuntu 24.04 x64 (80 GiB) | large | development |
| `ubuntu-26.04-large` | `github-runner-ubuntu-26-04-large` | Ubuntu 26.04 x64 (80 GiB) | preview | development |
| `ubuntu-latest` | `github-runner-ubuntu-24-04` | Ubuntu 24.04 x64 | stable logical mapping | verified |

The image-specific reports are [Ubuntu Slim](github-runner-ubuntu-slim/software-diff.md),
[Ubuntu 22.04](github-runner-ubuntu-22.04/software-diff.md),
[Ubuntu 24.04](github-runner-ubuntu-24.04/software-diff.md), and
[Ubuntu 26.04](github-runner-ubuntu-26.04/software-diff.md).
`ubuntu-latest` is a logical mapping to the 24.04 physical template and has no
fifth physical template directory.

The four standard physical templates were published, catalog-checked, and release-smoke
verified in `cn-yangzhou-1` and `us-south-1` on 2026-08-03. The regional IDs
and smoke evidence are retained in [Issue #38](https://github.com/qiniu/ci-runner/issues/38#issuecomment-5164811404).
The `ubuntu-latest` row inherits the verified publication state of its 24.04
physical template. The managed Runner Spec rollout and all five workflow
labels were end-to-end verified by
[GitHub Actions run 30858489153](https://github.com/miclle/qiniu-ci-runner-test/actions/runs/30858489153)
on 2026-08-04 CST; every request completed and its Sandbox was cleaned.

The four `-large` variants reuse the standard Dockerfiles and scripts through
in-repository links, but use distinct provider template names and an 80-GiB
provider allocation. Disk size is controlled by the Sandbox provider's
team/tier build allocation rather than qshell configuration. Set it to 81,920
MiB before building, verify catalog `disk_size_mb`, and keep these variants in
`development` until they reach the same regional smoke gate.

Publication state is restricted to `development`, `published`, or `verified`.
`published` means the physical template is public in both supported regions.
`verified` additionally requires Task 10 to attach successful two-region smoke
evidence to Issue #38. A local build or one-region publication remains
`development`.

## Compatibility contract

Compatibility is independently verified across these dimensions:

- Linux x86_64 OS/release identity;
- the non-root `runner` user, `/home/runner`, `/home/runner/work`, and the
  preinstalled runner under `/opt/actions-runner`;
- image identification variables, `RUNNER_TOOL_CACHE`,
  `AGENT_TOOLSDIRECTORY`, and upstream environment variables;
- the `/opt/hostedtoolcache` layout and every item in the pinned upstream
  software inventory, either as a guaranteed item or an explained exclusion;
- Docker daemon/socket behavior, Apache direct-process behavior, outbound
  HTTPS, runner registration, job execution, and cleanup.

The contract has no compatibility percentage. Every upstream item is either
`provided` with an executable verification command or `excluded` with a
specific Qiniu Sandbox limitation.

The current public-template build allocation exposes a 22,222-MiB root disk.
The complete GitHub-hosted runner image exceeds that allocation. The three
versioned templates therefore guarantee the pinned Ubuntu Slim-compatible
core on the requested Ubuntu release, plus Apache, Podman, Buildah, Skopeo,
Ninja, pinned Pester for build-time validation, and the Qiniu runner contract.
Full-image-only tools remain in the manifest as `excluded`; they are not
guaranteed to be preinstalled and should be installed in the workflow or
included in a custom template. `excluded` describes the support contract, not
a promise that the executable is absent.

All four templates install checksum-pinned AzCopy 10.32.6 from Microsoft's
official Ubuntu package pool instead of the upstream floating `aka.ms`
archive. They also install the official, checksum-pinned Azure CLI 2.88.0
package for Jammy or Noble directly and verify the installed version. They
intentionally do not prewarm the GitHub action
archive cache;
the runner resolves actions through the normal job-time GitHub protocol, so
this changes build size and network exposure rather than workflow semantics.
After Azure CLI is installed, they also install the upstream-selected Azure
DevOps extension 1.0.6 from its official Microsoft Blob wheel with a pinned
checksum and bounded curl retries; versioned templates retain the upstream
Pester assertion for that extension.

All four templates install Bicep 0.45.15 from Microsoft's signed NuGet
package instead of resolving and downloading a floating GitHub release asset.
The package checksum is pinned in each Dockerfile. The three versioned
templates run the upstream Bicep Pester assertion after installation; the Slim
template, whose pinned upstream tree has no Pester invocation helper, verifies
the installed CLI version directly.

AWS CLI, the AWS Session Manager plugin, AWS SAM CLI, GitHub CLI, yq, zstd,
and Ninja use the versions recorded by each image's pinned compatibility
report. Google Cloud CLI uses the common version pinned by these template
Dockerfiles. Their official versioned artifacts and SHA-256 digests are pinned
in the Dockerfiles, and setup verifies the installed commands or versions
before continuing. These compatibility-critical tools do not resolve GitHub
`latest` releases, the AWS `latest` download path, or a floating Google Cloud
APT package at build time. Exact GitHub release assets are downloaded without
first querying the anonymous GitHub release API, avoiding both rate-limit
failures and silent version drift between the compatibility manifest and the
built template.

Checksum-pinned downloads retain partial output and resume it across up to 20
bounded attempts. The large AWS SAM archive is additionally split into
independent 16 MiB-or-smaller Range-download layers before `platform`
provisioning, allowing successful chunks to survive the remote builder's
per-build time limit. The Dockerfile verifies the assembled byte count and
full SHA-256 digest, then continues into `platform` provisioning in the same
`RUN` so the installer consumes the checked archive immediately. Only the
bounded chunks under `/opt/qiniu-runner-build-cache` cross cache-layer
boundaries because qshell does not restore cached `/tmp` outputs; one oversized
temporary file never becomes a cache output. A digest mismatch or a server
that ignores the requested ranges therefore fails closed.

The curl compatibility wrapper remains available to unmodified upstream
installers that resolve a fixed release through the GitHub API. It caches
validated release metadata and uses bounded retries, but it is not part of the
installation path for the pinned tools above.

Git LFS comes from the configured Ubuntu archive instead of adding the
packagecloud repository. This keeps the package on the OS-supported channel,
avoids another build-time repository key, and retains the upstream Git LFS
Pester assertion on the three versioned templates. The Slim template verifies
the installed Git LFS CLI directly.

All four templates install NVM 0.40.6 from the checksum-pinned official tag
archive instead of cloning the repository during the build. The resulting
profile setup and system-Node default match the pinned upstream installer.
The build copies that skeleton into `/home/runner/.nvm` with runner ownership;
release smoke sources it as the runner user and verifies that the directory is
writable before the template can be promoted.

Google Cloud CLI is installed from Google's official versioned x86_64 archive.
The common version and SHA-256 digest are pinned in each Dockerfile, avoiding
different APT repository results between build regions, and the resulting
`gcloud` command is verified before the build continues.

The versioned image build keeps the upstream Podman, Buildah, and Skopeo CLI
checks. BuildKit cannot create the nested namespace needed by the upstream
`podman networking` assertion, so setup marks only that assertion skipped in
the staged `/imagegeneration/tests` copy; the pinned upstream source remains
unchanged. Podman is still `provided`: the generated compatibility command
creates, lists, and removes a uniquely named bridge network. Local Docker
conformance runs that command in the existing privileged executor, while
Sandbox conformance runs it in the actual Qiniu template runtime.

## Source of truth

- [`runner-images-upstream.lock.json`](runner-images-upstream.lock.json) pins the
  `actions/runner-images` revision, report paths, and report checksums.
- Each template Dockerfile pins the Canonical-published Ubuntu OCI index from
  `public.ecr.aws/ubuntu/ubuntu` by digest. Canonical publishes the same Ubuntu
  images through its documented OCI registry endpoints; the ECR endpoint is
  reachable by Sandbox builders in both supported regions. The template matrix
  check rejects a tag-only reference or a different registry.
- [`runner-images-compatibility.json`](runner-images-compatibility.json) is the
  machine-readable coverage and conformance input. It is the only full
  inventory in this repository.
- Each template README records build input, resources, and runner behavior.
- Each `software-diff.md` records image-specific exclusions and Sandbox
  service differences without duplicating the inventory.

## Build and verification

Qiniu Sandbox templates are officially built and published with
`qiniu/qshell` 2.19.10 or newer.
Docker builds are local conformance inputs only: a successful Docker build does
not create, rebuild, or publish a Qiniu Sandbox template.

The build and verification commands are:

```bash
task template-check-all
task template-build-ubuntu-slim
task template-build-ubuntu-22-04
task template-build-ubuntu-24-04
task template-build-ubuntu-26-04
task template-build-ubuntu-slim-large
task template-build-ubuntu-22-04-large
task template-build-ubuntu-24-04-large
task template-build-ubuntu-26-04-large
task template-conformance-local
task template-smoke IMAGE_KEY=ubuntu-24.04 TEMPLATE_ID=<published-template-id>
```

The formal template gate is a qshell build reaching terminal `Status: ready`,
followed by release smoke inside a real Sandbox created from that template.
The Slim Dockerfile divides setup into four cacheable qshell-compatible phases:
`bootstrap`, `platform`, `toolchain`, and `runtime`. The versioned templates add
a dedicated `node` phase between `platform` and `toolchain`, keeping their large
Node/npm toolset within the remote builder's per-layer time limit. Ubuntu 26.04
also preinstalls the pinned runner-images apt package list in eighteen cacheable
batches before the upstream platform installer rechecks every package and runs
its Pester contract. Large emoji-font, ICU, RPM, Tk, Xvfb, binutils, and
`systemd-coredump` dependency sets are isolated, and the final batch is
open-ended so appended pinned packages are not skipped; this keeps slow
Resolute mirrors from trapping the whole package set in one non-cacheable
timeout. If the remote builder hits its hard
time limit after one or more phases finish, rerun the same
`template-build-*` task with cache enabled; completed phases are reused. Do not
use `--no-cache` for that recovery, and do not publish until one build reaches
terminal `Status: ready`.
Template version metadata and the runner-owned NVM copy are applied only after
the heavy provisioning layers, so a release identity bump or NVM ownership fix
does not invalidate otherwise reusable installer caches.
Each Dockerfile then writes the final `/etc/resolv.conf` with Cloudflare
`1.1.1.1` and `1.0.0.1` before switching to the unprivileged runner user. This
late layer overrides the Sandbox builder default without invalidating the
heavy provisioning layers. Release smoke requires the exact two-line file.
The checksum-pinned AWS SAM Range layers sit between `bootstrap` and
`platform`; rerunning the identical source reuses every completed chunk rather
than restarting the whole archive. The platform installer runs in the same
layer as reassembly instead of depending on a cached oversized archive.
Release smoke checks the OS, architecture, the exact Dockerfile-pinned Actions
Runner version, persisted runtime template name/version metadata, outbound
HTTPS, the exact Cloudflare resolver configuration, Docker, a runner-owned
writable NVM home, writable work/tool-cache paths, and cleanup. Full
per-inventory runtime conformance and local Docker builds remain optional
diagnostics; neither is a substitute for the remote usability gate.
The source gate rejects an Actions Runner version below `2.336.0`, while the
compatibility contract checks the exact version pinned by each Dockerfile.
Update the runner version, official archive checksum, and compatibility
verification together. Python and pipx upstream installers use bounded retries
and longer pip read timeouts because remote template builds must tolerate
transient package-index failures without retrying unrelated installers.
The Docker check imports a minimal root filesystem from the Sandbox itself and
runs it with networking disabled. This verifies daemon, socket, image-import,
and container execution behavior without conflating template correctness with
regional Docker Hub availability; outbound HTTPS remains a separate check.

Each `template-build-*` target must:

1. copy the tracked template's `qshell.sandbox.toml` to a temporary file;
2. keep the working directory at that template directory so the relative
   Dockerfile and build context continue to resolve;
3. run `qshell sandbox template build --wait --config <temporary-file>`; and
4. remove the temporary file on exit.

The temporary copy is required because a first Qiniu template creation writes
the resulting `template_id` into the configuration file. The tracked
`qshell.sandbox.toml` remains a stable, reviewable input and must not receive
that environment-specific identifier.

The underlying Task 7 gates can also be run directly:

```bash
bash scripts/check-runner-template-matrix.sh
bash scripts/check-runner-image-compatibility.sh
```

## Maintenance and promotion

Update the upstream lock in a reviewed change, regenerate the compatibility
manifest from all four pinned reports, inspect every changed
`provided`/`excluded` decision, and rebuild all physical templates. The runner
platform owner is responsible for the lock and compatibility review; the Qiniu
Sandbox operator owns regional publication.

Promotion requires successful release smoke in both supported regions. Attach
the JSON evidence to Issue #38 before moving a row to `verified`. Rollback
restores the previous public template ID/name mapping and the previous reviewed
lock/manifest together, then repeats both-region smoke.
Task 8 exposes matching `task template-publish-*` and
`task template-unpublish-*` commands for the eight physical templates; use
`task template-defaults-check` before promotion and the matching unpublish
target for rollback. Qshell publish and unpublish do not support the build-only
`--config` option. Their Task targets run from the matching template directory,
read the stable name from its tracked `qshell.sandbox.toml`, and invoke the
matching operation with `-y`; no Region-specific template ID is committed.
