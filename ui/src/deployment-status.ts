export const activeDeploymentStatuses = new Set(["queued", "planning", "creating", "running", "stopping"])

export const deploymentListStatuses = ["running", "processing", "stopped", "failed"] as const
export type DeploymentListStatus = typeof deploymentListStatuses[number]
export type DeploymentStatusCounts = Record<DeploymentListStatus, number> & { all: number }

export function deploymentListStatusFromSearch(search: string): DeploymentListStatus {
  const values = new URLSearchParams(search).getAll("status")
  return values.length === 1 && deploymentListStatuses.some((status) => status === values[0]) ? values[0] as DeploymentListStatus : "running"
}
