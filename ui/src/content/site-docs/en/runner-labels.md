# Runner labels

Use a supported Qiniu label pair from the table below to select a Sandbox template. Standard labels are available by default; large labels are available after an operator creates and enables the corresponding custom Runner Spec.

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

## Resource contract

The standard templates provide 8 vCPU and 8 GiB of memory. The `-large` variants
reuse the same operating system image and installed software as their standard
counterparts; only the system disk allocation changes from 20 GiB to 80 GiB.
Disk capacity is supplied by the Sandbox provider rather than a workflow setting,
and the usable filesystem capacity may be slightly lower after formatting and
reserved space.

`ubuntu-latest-large` is a logical label for the same physical template as
`ubuntu-24.04-large`; it does not create another physical image.

Choose a `-large` label for workloads such as container builds, dependency
caches, or large intermediate artifacts that exceed the standard system disk.
Changing to `-large` does not add CPU or memory.

## Matching contract

Each managed spec advertises `self-hosted`, `linux`, `x64`, `qiniu`, and its exact OS label. It requires `qiniu` plus that OS label. A custom large spec must define an advertised and required label set that follows the same contract.

Matching preserves:

```text
required labels ⊆ job labels ⊆ advertised labels
```

This means `[qiniu, ubuntu-24.04]` and the full advertised managed set both match. Partial or unsupported sets do not. A large label matches only when its corresponding custom spec is enabled and its configured labels accept the request.

## Managed and custom ownership

runnerd owns standard managed names, labels, required labels, public template names, and priority. Operators control `enabled`, `max_concurrency`, and `min_idle`.

Large and other custom specs remain operator-owned. They use an explicit template ID and may define different advertised and required labels. Saving a custom spec does not prove the template exists or is usable in the selected Sandbox region.

## Template resolution

Immediately before registration, runnerd resolves a managed public template name through the effective account or organization Sandbox endpoint. A stable public name can therefore resolve to different template IDs in different regions without persisting one region's ID in the spec. Custom large specs use the explicit template ID saved by the operator.

See [Run your first workflow](/docs/guides/workflow) for a managed example, or [Build and use a custom runner template](/docs/guides/custom-templates) for the complete custom path.
