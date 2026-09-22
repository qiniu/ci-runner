# Build and use a custom runner template

Build a private Qiniu Sandbox template for your own tools, connect it to a custom Runner Spec, and select it from a GitHub Actions workflow.

## Decide whether you need a custom template

Use a [managed runner label](/docs/reference/runner-labels) when the maintained Ubuntu images already contain what the job needs. Choose a custom template when you need a different base image, preinstalled toolchain, system package, certificate, or other image-level customization.

A custom template is private to its Sandbox environment. It does not need to be published. Build it in the same Sandbox region and account or organization scope that runnerd will use for the target repository.

## Before you start

You need:

- qshell 2.19.10 or newer;
- a Qiniu Sandbox endpoint and API key for the target region;
- administrator access to runnerd;
- a GitHub repository connected to the same account or organization scope.

Keep credentials in environment variables or a local secret store. Do not commit API keys or a region-specific template ID.

## Start from a compatible runner image

The safest starting point is one of the existing `templates/github-runner-*` directories in the Qiniu CI Runner repository. Copy the closest image, rename it, and add only the tools your workflows need.

A compatible image must provide executable GitHub Actions runner scripts at:

```text
/opt/actions-runner/config.sh
/opt/actions-runner/run.sh
```

It should also provide a writable `/home/runner` and allow outbound HTTPS access to GitHub. A `runner` user, `/opt/hostedtoolcache`, and `/usr/local/bin/ensure-docker` follow the maintained image convention. Docker startup is best-effort for custom specs, so omit it only when workflows do not need containers or service containers.

The preinstalled Runner is a bootstrap baseline. Before requesting a registration token for a custom Runner Spec, runnerd resolves GitHub's official Linux Runner applications and strictly validates their release URL, filename, architecture, version, and SHA-256. Inside the Sandbox, a matching or newer installed version registers immediately and is never downgraded; a stale writable copy is downloaded under a hard five-minute deadline and 512-MiB bound, checksum-verified, extracted, and version-verified before `config.sh` runs. Any lookup, tool, download, checksum, extraction, or version failure stops before registration, so the queued Job cannot start on the stale Runner.

This update is intentionally ephemeral. If the immutable template remains old, every fresh Sandbox downloads the current Runner again. Periodically rebuild the custom template to reduce startup time and upstream download dependence. runnerd still leaves GitHub's official self-update enabled after registration.

runnerd injects a Bash startup script into the Sandbox. The image must provide `bash`, `base64`, `install`, `cp`, `mkdir`, and `id`. A stale Runner also requires `curl`, `tar`, `sha256sum`, `mktemp`, and `timeout`; the complete retrying download has a hard five-minute deadline. A matching or newer installed Runner skips those five requirements. If the conventional `runner` user exists, it must also provide `sudo` so the startup script can switch to that user without interaction. Only the validated public URL, version, architecture, and checksum enter the Sandbox; GitHub API credentials do not.

A locally successful `docker build` is useful diagnostics, but it does not prove that a remote Sandbox template exists or can start in the target region.

## Configure the template build

Place `qshell.sandbox.toml` beside the Dockerfile. Start with a portable name-only configuration:

```toml
name = "acme-runner-ubuntu-24-04"
dockerfile = "./Dockerfile"
path = "."
cpu_count = 8
memory_mb = 8192
no_cache = false
```

The name must be unique in one Sandbox environment. qshell can resolve an existing template by name and rebuild it; otherwise it creates one. Some qshell versions may write `template_id` back to the file after the first build. Review the file before committing and remove that region-specific value from shared source.

## Build in Qiniu Sandbox

Set the credentials for the target region, enter the template directory, and run the remote build:

```bash
export QINIU_SANDBOX_API_URL="https://<sandbox-region-endpoint>"
read -r -s QINIU_API_KEY
export QINIU_API_KEY

cd path/to/acme-runner-ubuntu-24-04
qshell sandbox template build --wait
```

Do not continue until qshell reports `Status: ready`. Use the same command to rebuild a name-resolved template after changing its Dockerfile. Then list the templates and record the ID returned for this region:

```bash
qshell sandbox template list --format json
qshell sandbox template get <template-id>
```

## Verify the remote template

Create a short-lived Sandbox from the template before connecting it to runnerd:

```bash
qshell sandbox create <template-id-or-name> --timeout 300
```

In the Sandbox terminal, verify the runner contract and the tools your workflow needs:

```bash
command -v bash base64 install cp mkdir id curl tar sha256sum mktemp timeout
timeout --signal=KILL 1s true
test -x /opt/actions-runner/config.sh
test -x /opt/actions-runner/run.sh
test -w /home/runner
git --version

if id -u runner >/dev/null 2>&1; then
  command -v sudo
  sudo -E -u runner true
fi
```

When Docker is required, also run `/usr/local/bin/ensure-docker` and `docker info`. Exit the session, use `qshell sandbox list` to check for a remaining instance, and clean it up with `qshell sandbox kill <sandbox-id>`.

## Create a custom Runner Spec

Sign in as a runnerd administrator, open `/admin/runner_specs`, and create a custom spec with:

- a recognizable name;
- advertised labels such as `self-hosted`, `linux`, `x64`, and `acme-linux-x64`;
- required labels containing `acme-linux-x64`;
- the exact template ID from the target Sandbox region;
- an optional GitHub Runner Group for an organization repository, left blank for a personal repository;
- suitable `max_concurrency`, `min_idle`, and priority values;
- `enabled` turned on.

Every enabled spec is eligible for an admitted repository when its workflow labels match. Use unique required labels and add them only to the intended workflows; runnerd does not use an internal Runner Group or Repository Policy to authorize the spec.

Before creating the spec, configure the administrator's Sandbox endpoint and API key at `/admin/sandbox_service`. New specs and changed template IDs are checked with those credentials for access and a usable default build. An owned template or public default template must expose a usable default build in the provider catalog; a new rebuild does not invalidate an older usable default. Public templates outside that catalog cannot have readiness confirmed and are rejected.

Missing configuration, missing/unready templates, permission errors, or a failed/timed-out check prevent saving; correct the issue and retry. The dialog keeps your input. Editing controls without changing the template does not repeat the check. Managed default specs remain manageable without admin Sandbox credentials.

Jobs still use the repository owner's effective Sandbox configuration. Ensure that scope can access the same template; save-time validation does not check runner binaries or replace the sandbox smoke test above.

## Use the custom labels

Runner matching preserves `required labels ⊆ job labels ⊆ advertised labels`. The workflow must include every required label and must not request a label that the spec does not advertise.

For the example spec, use:

```yaml
name: Custom runner check

on:
  workflow_dispatch:

jobs:
  verify:
    runs-on: [self-hosted, linux, x64, acme-linux-x64]
    steps:
      - uses: actions/checkout@v4
      - run: git --version
```

Dispatch the workflow, then open Jobs in Qiniu CI Runner. A successful result shows the request matched the custom spec, created a Sandbox from its template ID, registered the runner, and completed the job.

## Roll out safely and troubleshoot

Start by adding the unique labels to one repository and one manually dispatched smoke workflow. Increase concurrency or add the labels to more workflows only after the job has completed and the Sandbox was cleaned up.

If the job stays queued, compare the job labels, required labels, advertised labels, and `enabled`. If runner creation fails, confirm the effective account or organization Sandbox endpoint, template ID, and `Status: ready`. For runtime failures, re-run the remote template checks and inspect runnerd logs.

When a rebuild produces a new template ID, update the custom Runner Spec before the next job. Continue with [Troubleshooting](/docs/troubleshooting) for symptom-based checks.
