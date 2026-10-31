# Runner labels

Use a supported Qiniu label pair from the table below to select a Sandbox template. On the Qiniu hosted service, standard Ubuntu labels and the Windows 11 preview are available through enabled platform Runner Specs; large labels are available after an operator creates and enables the corresponding custom Runner Spec. Self-hosted runnerd installations start with an empty spec catalog and require administrator setup for every label.

## Supported labels and resources

| Workflow request | CPU | Memory | System disk | Template status | Notes |
| --- | --- | --- | --- | --- | --- |
| `[qiniu, ubuntu-slim]` | 8 vCPU | 8 GiB | 20 GiB | Stable | Smaller general-purpose image |
| `[qiniu, ubuntu-slim-large]` | 8 vCPU | 8 GiB | 80 GiB | Stable after setup | Requires an enabled custom Runner Spec; larger system disk |
| `[qiniu, ubuntu-22.04]` | 8 vCPU | 8 GiB | 20 GiB | Stable | Ubuntu 22.04 x64 |
| `[qiniu, ubuntu-22.04-large]` | 8 vCPU | 8 GiB | 80 GiB | Stable after setup | Requires an enabled custom Runner Spec; Ubuntu 22.04 x64 with a larger system disk |
| `[qiniu, ubuntu-24.04]` | 8 vCPU | 8 GiB | 20 GiB | Stable | Recommended default |
| `[qiniu, ubuntu-24.04-large]` | 8 vCPU | 8 GiB | 80 GiB | Stable after setup | Requires an enabled custom Runner Spec; recommended for disk-intensive jobs |
| `[qiniu, ubuntu-latest]` | 8 vCPU | 8 GiB | 20 GiB | Stable mapping | Currently maps to Ubuntu 24.04 |
| `[qiniu, ubuntu-latest-large]` | 8 vCPU | 8 GiB | 80 GiB | Stable mapping after setup | Requires an enabled custom Runner Spec; logical mapping to the Ubuntu 24.04 large template |
| `[qiniu, ubuntu-26.04]` | 8 vCPU | 8 GiB | 20 GiB | Preview | Preview image, use deliberately |
| `[qiniu, ubuntu-26.04-large]` | 8 vCPU | 8 GiB | 80 GiB | Preview after setup | Requires an enabled custom Runner Spec; Ubuntu 26.04 preview with a larger system disk |
| `[qiniu, windows-11]` | 8 vCPU | 8 GiB | 50 GiB | Preview | Windows 11 25H2 x64 (minimal core); available on the hosted service, requires spec setup on self-hosted installations |

## Resource contract

The standard templates provide 8 vCPU and 8 GiB of memory. The Ubuntu `-large` variants
reuse the same operating system image and installed software as their standard
counterparts; only the system disk allocation changes from 20 GiB to 80 GiB.
Disk capacity is supplied by the Sandbox provider rather than a workflow setting,
and the usable filesystem capacity may be slightly lower after formatting and
reserved space. Windows 11 uses a 50 GiB physical system disk; its `C:` volume is smaller because of system partitions, and preinstalled software further reduces free space.

`ubuntu-latest-large` is a logical label for the same physical template as
`ubuntu-24.04-large`; it does not create another physical image.

Choose an Ubuntu `-large` label for workloads such as container builds, dependency
caches, or large intermediate artifacts that exceed the standard system disk.
Changing to `-large` does not add CPU or memory.

## Matching contract

Each Ubuntu spec advertises `self-hosted`, `linux`, `x64`, `qiniu`, and its exact OS label. The Windows 11 spec advertises `self-hosted`, `windows`, `x64`, `qiniu`, and `windows-11`. Each spec requires `qiniu` plus its exact OS label. A custom large spec must define an advertised and required label set that follows the same contract.

Matching preserves:

```text
required labels ⊆ job labels ⊆ advertised labels
```

This means `[qiniu, ubuntu-24.04]` and the full advertised managed set both match. Partial or unsupported sets do not. A large label matches only when its corresponding custom spec is enabled and its configured labels accept the request.

## Windows 11 preview

On the Qiniu hosted service, the Windows 11 preview spec is enabled and displayed in the [Runner Spec catalog](/runner-specs). Use `[qiniu, windows-11]` to select its `github-runner-windows-11` template.

On a self-hosted runnerd installation, an administrator must first make the Windows preview template available at the effective Sandbox endpoint with a usable default build, then create and enable a Runner Spec. Bind it to the public template name `github-runner-windows-11` when available, or to its explicit template ID. Set advertised labels to `self-hosted, windows, x64, qiniu, windows-11` and required labels to `qiniu, windows-11`. The `windows` advertised label selects the Windows launch path. Startup does not create missing specs; without a matching enabled spec, the workflow is rejected with `profile_labels_not_matched`.

Once the spec is available, run this workflow:

```yaml
name: Windows CI
on: [push, pull_request]

jobs:
  test:
    runs-on: [qiniu, windows-11]
    steps:
      - uses: actions/checkout@v4
      - name: Check Windows environment
        shell: pwsh
        run: |
          Get-CimInstance Win32_OperatingSystem | Select-Object Caption, Version
          git --version
```

The preview image provides Windows 11 25H2 x64 with a minimal core software set: Actions Runner, Git for Windows (including Bash), and PowerShell 7 (`pwsh`). Windows PowerShell 5.1 (`powershell`) and `cmd` are also available. Use Windows-compatible actions and commands; do not combine `windows-11` with `linux` or Ubuntu labels.

Templates rebuilt with the GCC setup also include MSYS2 UCRT64 `gcc` and `g++` on `PATH` for Windows amd64 cgo, without LLVM. Older template builds may not include these compilers; confirm availability in your selected template. Go remains workflow-managed.

Docker and Visual Studio Build Tools are not preinstalled. This image does not provide the full GitHub-hosted Windows software set; install additional runtimes or build tools in your workflow as needed. Runner Web Console and network diagnostics currently require a POSIX shell and do not support Windows. The template remains Preview; validate your workload before relying on it for production builds.

## Platform spec management

Administrators maintain every platform spec in Admin: labels, required labels, binding, capacity, priority, enabled state and publication. Startup preserves changes and never recreates deleted specs. Fresh installations need an administrator to create specs first. The platform directory lists enabled specs with catalog display on after public-template validation, including fixed-ID references.

Public-name bindings resolve through the effective account or organization Sandbox endpoint immediately before registration. The same stable name can resolve to different physical IDs across regions. Fixed-ID references use an explicit template ID, which may also be public. Saving validates access through the administrator's Sandbox configuration; regional workflow smoke still proves runtime usability.

Runtime behavior follows the binding, with no separate Runner preparation, Docker or sponsorship switches. Public-name bindings use the preinstalled Runner; Linux requires Docker readiness, while Windows does not require Docker. Fixed-ID references check and update the official Runner for the selected operating system before registration when needed, keep Linux Docker setup best effort, and cannot use sponsorship. Both retain GitHub Runner self-update. Public-name specs may use [organization fork sponsorship](/docs/guides/fork-sponsorship) only under the existing organization repository policy, authorization and capacity checks. Large specs may use either binding; directory inclusion requires a verified public template and explicit opt-in; runtime availability depends on a usable default build. Fixed IDs can remain unchanged; catalog display does not change execution behavior.

See [Run your first workflow](/docs/guides/workflow) for a managed example, or [Build and use a custom runner template](/docs/guides/custom-templates) for the complete custom path.
