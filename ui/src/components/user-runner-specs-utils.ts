import type { UserRunnerSpec } from "@/admin-types"
import { sandboxRegions, type SandboxRegion } from "@/components/sandbox-catalog-utils"

export type UserRunnerSpecFormState = { name: string; labels: string; templateID: string; runnerGroup: string; maxConcurrency: string; enabled: boolean }

export function newRunnerSpecFormState(): UserRunnerSpecFormState {
  return { name: "", labels: "", templateID: "", runnerGroup: "", maxConcurrency: "10", enabled: true }
}

export function runnerSpecCatalogRegion(regionID: string, regions: readonly SandboxRegion[] = sandboxRegions) {
	const catalog = regions.length > 0 ? regions : sandboxRegions
	return catalog.some((region) => region.id === regionID) ? regionID : catalog[0].id
}

export function runnerSpecWorkflowYAML(labels: string[]) {
  return `runs-on: [${labels.map((label) => JSON.stringify(label)).join(", ")}]`
}

export function runnerSpecOverridesGlobal(labels: string[], items: UserRunnerSpec[]) {
  const labelKey = (values: string[]) => [...new Set(values.map((label) => label.trim().toLowerCase()).filter(Boolean))].sort().join("\u0000")
  const key = labelKey(labels)
  return key !== "" && items.some((item) => item.source !== "scoped_custom" && labelKey(item.workflow_labels) === key)
}

export function runnerSpecDialogDescriptionKey(mode: "create" | "edit") {
  const descriptions = {
    create: "user.customRunnerSpecDialogDescription",
    edit: "user.customRunnerSpecDialogDescription",
  } as const
  return descriptions[mode]
}
