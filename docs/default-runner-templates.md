# Public runner templates

[中文](zh/default-runner-templates.md)

Qiniu maintains eight physical Linux x64 Sandbox templates for GitHub Actions:
four standard images (Ubuntu Slim, Ubuntu 22.04, Ubuntu 24.04, and preview
Ubuntu 26.04) plus four `-large` variants with an 80-GiB provider disk.
`ubuntu-latest` is a logical runner catalog mapping to Ubuntu 24.04, not a
ninth image.

The four standard physical templates were published, catalog-checked, and release-smoke
verified in both supported Sandbox regions on 2026-08-03. The regional IDs and
evidence are retained in [Issue #38](https://github.com/qiniu/ci-runner/issues/38#issuecomment-5164811404).
The managed Runner Spec rollout was end-to-end verified on 2026-08-04 CST by
[GitHub Actions run 30858489153](https://github.com/miclle/qiniu-ci-runner-test/actions/runs/30858489153):
all five logical labels completed successfully, all five runner requests
reached `completed`, every Sandbox was cleaned, and no self-hosted runner
registration remained. See
[`templates/README.md`](../templates/README.md) for pinned upstream provenance,
the compatibility contract, and per-image differences.

The four `-large` variants reuse the standard Dockerfiles and scripts through
repository links and use distinct physical template names. They are public
operator-configured default Runner Specs: operators enable them through the
custom-spec path with explicit template IDs. They are not runnerd-managed
defaults, but all allowed workflows may use their documented labels when the
corresponding specs are enabled. Their 80-GiB root
disk is supplied by the Sandbox provider's team/tier build allocation, not a
field in `qshell.sandbox.toml` or a qshell CLI flag. Configure that allocation
to 81,920 MiB before building the large variants and verify the resulting
catalog `disk_size_mb` before publication.

## Public catalog API

`GET /api/public/runner-templates` is available to signed-out and signed-in
clients and returns the same cacheable, runnerd-owned catalog. The response is
sorted and contains four objects for the standard managed templates with only
these stable fields:

```json
[
  {
    "default_template_name": "github-runner-ubuntu-24-04",
    "runner_spec_names": ["qiniu-ubuntu-24.04", "qiniu-ubuntu-latest"],
    "workflow_labels": [
      ["qiniu", "ubuntu-24.04"],
      ["qiniu", "ubuntu-latest"]
    ]
  }
]
```

The example shows one entry; the full response contains the four standard
managed templates. The API intentionally excludes provider template IDs,
regions, credentials, endpoints, and private/custom templates, including the
four large physical templates. Provider-visible
templates remain behind the credential-bound, account or organization scoped
`GET /user/sandbox/templates?region=<id>` API. The ordinary-user Sandbox
Templates page renders these catalogs as independent sections, so a provider
catalog failure does not hide the public catalog and vice versa.

## Workflow labels

Use the exact pair for the requested environment:

```yaml
jobs:
  slim:
    runs-on: [qiniu, ubuntu-slim]
    steps:
      - uses: actions/checkout@v4
      - run: uname -a

  ubuntu_22:
    runs-on: [qiniu, ubuntu-22.04]
    steps:
      - uses: actions/checkout@v4
      - run: uname -a

  ubuntu_24:
    runs-on: [qiniu, ubuntu-24.04]
    steps:
      - uses: actions/checkout@v4
      - run: uname -a

  ubuntu_26_preview:
    runs-on: [qiniu, ubuntu-26.04]
    steps:
      - uses: actions/checkout@v4
      - run: uname -a

  latest:
    runs-on: [qiniu, ubuntu-latest]
    steps:
      - uses: actions/checkout@v4
      - run: uname -a
```

The public large defaults use the same contract and resources with an 80-GiB
system disk:

| Workflow label | Physical template | System disk |
| --- | --- | --- |
| `[qiniu, ubuntu-slim-large]` | `github-runner-ubuntu-slim-large` | 80 GiB |
| `[qiniu, ubuntu-22.04-large]` | `github-runner-ubuntu-22-04-large` | 80 GiB |
| `[qiniu, ubuntu-24.04-large]` | `github-runner-ubuntu-24-04-large` | 80 GiB |
| `[qiniu, ubuntu-26.04-large]` | `github-runner-ubuntu-26-04-large` | 80 GiB |
| `[qiniu, ubuntu-latest-large]` | `github-runner-ubuntu-24-04-large` | 80 GiB |

`ubuntu-latest-large` is a logical public label mapped to the Ubuntu 24.04
large physical template; it does not add a fifth physical large image. These
large specs are publicly documented and usable once the operator-managed Admin
entries are enabled, even though they are not returned by the runnerd-owned
managed-template API.

The `qiniu` label is mandatory. Managed matching enforces
`required_labels ⊆ job_labels ⊆ labels`, so `[ubuntu-24.04]`, `[qiniu]`, and a
request with unsupported extra labels do not match a managed default.
Operators can disable one managed spec in Admin; removing `qiniu` from a
workflow disables managed-default selection from the workflow side. Custom
specs remain available with operator-defined required labels and explicit
template IDs. The large workflow labels use the public operator-configured
default specs after an operator creates and enables the corresponding entries
in Admin. The physical large templates can still be built, published,
catalog-checked, and smoke-tested by the same task targets; they simply do not
appear in this public managed catalog.

At runner bootstrap, managed specs fail closed if their template cannot make
the Docker daemon available because Docker is part of the managed compatibility
contract. Custom specs retain the legacy best-effort behavior: runnerd logs a
warning and continues registration so non-Docker jobs can still run.

## Software compatibility

These templates track the pinned `actions/runner-images` reports item by item,
but they are not byte-for-byte GitHub-hosted runner images. The current Qiniu
Sandbox public-template build allocation exposes a 22,222-MiB root disk, while
the complete GitHub-hosted runner inventory requires more space. The three
versioned templates guarantee the Ubuntu Slim-compatible core on the requested
Ubuntu release, plus Apache, Podman, Buildah, Skopeo, Ninja, Docker support,
pinned Pester for installer validation, the preinstalled Actions runner, and
the runner filesystem contract.

[`templates/runner-images-compatibility.json`](../templates/runner-images-compatibility.json)
is the executable item-level contract. `provided` means release conformance
must verify the item. `excluded` means the public template does not guarantee
it; install the tool in the workflow or build a custom Sandbox template when
it is required. An excluded executable might be present through an OS package
dependency, but workflows must not rely on it.

## Requirements

- `qiniu/qshell` 2.19.10 or newer;
- `task`, `jq`, and `curl`;
- a `QINIU_API_KEY` for the selected Sandbox region;
- `QINIU_SANDBOX_API_URL` set to that region's endpoint.

If the required qshell is not on `PATH`, pass its executable explicitly:

```bash
task template-build-ubuntu-24-04 QSHELL=/path/to/qshell
```

Every remote target fails closed when either credential variable is empty.
The tracked `qshell.sandbox.toml` files contain stable names and resource
settings only; qshell builds from temporary copies so region-specific template
IDs are never committed.

## Build and verify one region

Run the fast, credential-free source checks first:

```bash
task template-check-all
```

Then build the actual Sandbox templates with qshell. Each target waits for
qshell to report terminal `Status: ready`; a zero process exit without that
status is treated as a failed build.

The source gate rejects Actions Runner versions below `2.336.0`. Release smoke
checks the exact Dockerfile-pinned Runner version and the template name/version
persisted into the Sandbox runtime environment. It also loads NVM as the
`runner` user and requires `/home/runner/.nvm` to be writable, preventing a
root-owned build skeleton from passing the release gate. Full runtime
conformance also checks the exact pinned Azure CLI version, so Dockerfile
versions, official checksums, and compatibility verification must be updated
together. Python and pipx installation has bounded retries for transient
package-index failures; other upstream installers are not retried automatically
because they may not be idempotent.

```bash
task template-build-ubuntu-slim
task template-build-ubuntu-22-04
task template-build-ubuntu-24-04
task template-build-ubuntu-26-04
task template-build-ubuntu-slim-large
task template-build-ubuntu-22-04-large
task template-build-ubuntu-24-04-large
task template-build-ubuntu-26-04-large
```

Before running a large build target, configure the Sandbox provider's build
allocation to 81,920 MiB. Qshell does not carry a per-template disk parameter,
so the operator must verify the resulting template's catalog `disk_size_mb`
before publication and smoke testing.

The Dockerfiles keep `bootstrap`, `platform`, `node`, `toolchain`, and
`runtime` work in separate qshell-compatible cache layers where applicable.
Template version metadata is applied after provisioning, and the runner-owned
NVM copy is isolated between `toolchain` and `runtime`, so either change keeps
the heavy installer layers reusable.
After those layers, each Dockerfile writes the final `/etc/resolv.conf` with
Cloudflare `1.1.1.1` and `1.0.0.1` before switching to the unprivileged runner
user. Keeping this override late avoids invalidating the heavy provisioning
layers. Release smoke checks the exact two-line resolver file in a real
Sandbox before publication.
Before `platform`, each Dockerfile downloads the checksum-pinned AWS SAM
archive in independent 16 MiB-or-smaller Range layers. Completed chunks remain
cacheable across a service timeout under `/opt/qiniu-runner-build-cache`;
qshell does not restore cached `/tmp` outputs. The Dockerfile checks the
assembled byte count and full SHA-256 digest, then runs the `platform`
installer in the same layer. The checked oversized archive is consumed
immediately and never becomes a cache output.
Ubuntu 26.04 additionally preinstalls the pinned runner-images apt package list
in eighteen cacheable batches before the upstream platform installer rechecks
the packages and runs its Pester contract. Large emoji-font, ICU, RPM, Tk,
Xvfb, binutils, and `systemd-coredump` dependency sets are isolated, and the
final batch is open-ended so appended pinned packages are not skipped. If a
remote build reaches the service
time limit after completing an earlier layer, rerun the same command with the
normal cache enabled. Do not force `--no-cache`; the release gate remains a
single build reaching terminal `Status: ready`.

Publish only after all eight builds are ready:

```bash
task template-publish-ubuntu-slim
task template-publish-ubuntu-22-04
task template-publish-ubuntu-24-04
task template-publish-ubuntu-26-04
task template-publish-ubuntu-slim-large
task template-publish-ubuntu-22-04-large
task template-publish-ubuntu-24-04-large
task template-publish-ubuntu-26-04-large
task template-defaults-check
```

`template-defaults-check` requires exactly one public `ready` or `uploaded`
template with a nonempty ID for every physical name, including the four large
variants. It rejects missing and duplicate catalog entries.

Retain each ID printed by the catalog check, then run actual Sandbox smoke:

```bash
task template-smoke IMAGE_KEY=ubuntu-slim TEMPLATE_ID=<slim-template-id>
task template-smoke IMAGE_KEY=ubuntu-22.04 TEMPLATE_ID=<22.04-template-id>
task template-smoke IMAGE_KEY=ubuntu-24.04 TEMPLATE_ID=<24.04-template-id>
task template-smoke IMAGE_KEY=ubuntu-26.04 TEMPLATE_ID=<26.04-template-id>
task template-smoke IMAGE_KEY=ubuntu-slim-large TEMPLATE_ID=<slim-large-template-id>
task template-smoke IMAGE_KEY=ubuntu-22.04-large TEMPLATE_ID=<22.04-large-template-id>
task template-smoke IMAGE_KEY=ubuntu-24.04-large TEMPLATE_ID=<24.04-large-template-id>
task template-smoke IMAGE_KEY=ubuntu-26.04-large TEMPLATE_ID=<26.04-large-template-id>
```

Smoke creates a temporary Sandbox with qshell and checks the OS release,
architecture, preinstalled Actions runner, outbound HTTPS, Docker daemon,
writable work/tool-cache paths, and cleanup. Preserve the emitted JSON paths as
release evidence. The full compatibility manifest remains the static inventory
contract; per-entry runtime conformance is an optional diagnostic and does not
block the release usability gate.
The Docker check imports and runs a local minimal root filesystem with
networking disabled. Registry reachability is not part of the daemon check;
outbound HTTPS is verified independently.

Local Docker builds and `task template-conformance-local` remain optional
diagnostic tools. They are not substitutes for qshell template builds or
Sandbox smoke.

## First release in both regions

Complete the whole build, publish, catalog, and smoke sequence in this order:

1. Export
   `QINIU_SANDBOX_API_URL=https://cn-yangzhou-1-sandbox.qiniuapi.com` and the
   Yangzhou `QINIU_API_KEY`.
2. Build and publish the four standard templates. After the provider
   disk-allocation gate is available, build and publish the four large variants,
   then run `task template-defaults-check` and smoke all eight returned IDs.
3. Retain the build output, catalog IDs, smoke JSON, and relevant workflow URL.
4. Export
   `QINIU_SANDBOX_API_URL=https://us-south-1-sandbox.qiniuapi.com` and the
   US South `QINIU_API_KEY`.
5. Repeat the eight builds, publication, catalog check, and smoke checks.
6. Confirm both catalog results contain one runnable public entry for every
   physical stable name.
7. Attach both-region evidence to Issue #38 before marking the template rows
   verified or enabling the separate managed-runner rollout.

Qiniu owns the eight physical images. `ubuntu-latest` changes only through a
reviewed runner catalog revision with new regional smoke evidence.
`ubuntu-26.04` remains preview until upstream promotes it.

## Rollback

Disable the managed Runner Specs and any enabled large custom specs before
removing public availability. Then
run the matching reversible publication rollback:

```bash
task template-unpublish-ubuntu-slim
task template-unpublish-ubuntu-22-04
task template-unpublish-ubuntu-24-04
task template-unpublish-ubuntu-26-04
task template-unpublish-ubuntu-slim-large
task template-unpublish-ubuntu-22-04-large
task template-unpublish-ubuntu-24-04-large
task template-unpublish-ubuntu-26-04-large
```

Do not delete template objects during an ordinary rollback. Keeping them
private preserves build history and allows a reviewed version to be
republished without changing custom Runner Specs.
