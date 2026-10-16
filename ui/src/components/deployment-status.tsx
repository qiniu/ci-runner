import { useTranslation } from "react-i18next"
import { Badge } from "@/components/ui/badge"

export function DeploymentStatus({ status }: { status: string }) {
  const { t } = useTranslation()
  const label: Record<string, string> = { queued: t("common.statusQueued"), planning: t("user.deploymentPlanning"), creating: t("common.statusCreating"), running: t("common.statusRunning"), stopping: t("common.statusStopping"), completed: t("user.deploymentStatusStopped"), expired: t("user.deploymentExpired"), failed: t("common.statusFailed") }
  return <Badge variant={status === "failed" ? "destructive" : status === "running" ? "success" : "secondary"}>{label[status] || status}</Badge>
}
