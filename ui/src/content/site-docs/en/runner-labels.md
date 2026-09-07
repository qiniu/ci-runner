# Managed runner labels

Use a supported Qiniu label pair to select a maintained public Sandbox template without creating a custom Runner Spec.

## Supported labels and resources

| Workflow request | CPU | Memory | System disk | Template status | Notes |
| --- | --- | --- | --- | --- | --- |
| `[qiniu, ubuntu-slim]` | 8 vCPU | 8 GiB | 20 GiB | Stable | Smaller general-purpose image |
| `[qiniu, ubuntu-22.04]` | 8 vCPU | 8 GiB | 20 GiB | Stable | Ubuntu 22.04 x64 |
| `[qiniu, ubuntu-24.04]` | 8 vCPU | 8 GiB | 20 GiB | Stable | Recommended default |
| `[qiniu, ubuntu-26.04]` | 8 vCPU | 8 GiB | 20 GiB | Preview | Preview image, use deliberately |
| `[qiniu, ubuntu-latest]` | 8 vCPU | 8 GiB | 20 GiB | Stable mapping | Currently maps to Ubuntu 24.04 |
| `[qiniu, ubuntu-slim-large]` | 8 vCPU | 8 GiB | 80 GiB | Large | Ubuntu Slim with a larger system disk |
| `[qiniu, ubuntu-22.04-large]` | 8 vCPU | 8 GiB | 80 GiB | Large | Ubuntu 22.04 x64 with a larger system disk |
| `[qiniu, ubuntu-24.04-large]` | 8 vCPU | 8 GiB | 80 GiB | Large | Recommended for disk-intensive jobs |
| `[qiniu, ubuntu-26.04-large]` | 8 vCPU | 8 GiB | 80 GiB | Preview | Ubuntu 26.04 preview with a larger system disk |
| `[qiniu, ubuntu-latest-large]` | 8 vCPU | 8 GiB | 80 GiB | Stable mapping | Currently maps to Ubuntu 24.04 large |

## Resource contract

All managed templates currently provide 8 vCPU and 8 GiB of memory. The
`-large` variants reuse the same operating system image and installed software
as their standard counterparts; only the system disk allocation changes from
20 GiB to 80 GiB. Disk capacity is supplied by the Sandbox provider rather
than a workflow setting, and the usable filesystem capacity may be slightly
lower after formatting and reserved space.

Choose a `-large` label for workloads such as container builds, dependency
caches, or large intermediate artifacts that exceed the standard system disk.
Changing to `-large` does not add CPU or memory.

## Matching contract

Each managed spec advertises `self-hosted`, `linux`, `x64`, `qiniu`, and its exact OS label. It requires `qiniu` plus that OS label.

Matching preserves:

```text
required labels ⊆ job labels ⊆ advertised labels
```

This means `[qiniu, ubuntu-24.04-large]` and the full advertised set both match. Partial or unsupported sets do not.

## Managed and custom ownership

runnerd owns managed names, labels, required labels, public template names, and priority. Operators control `enabled`, `max_concurrency`, and `min_idle`.

Custom specs remain operator-owned. They use an explicit template ID and may define different advertised and required labels. Saving a custom spec does not prove the template exists or is usable in the selected Sandbox region.

## Template resolution

Immediately before registration, runnerd resolves the managed public template name through the effective account or organization Sandbox endpoint. A stable public name can therefore resolve to different template IDs in different regions without persisting one region's ID in the spec.

See [Run your first workflow](/docs/guides/workflow) for a managed example, or [Build and use a custom runner template](/docs/guides/custom-templates) for the complete custom path.
