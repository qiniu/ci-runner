import { useTranslation } from "react-i18next"
import type { RunnerState } from "@/admin-types"
import { AppLink } from "@/components/app-link"
import { Button } from "@/components/ui/button"
import { deploymentCreatePath, githubContextPath, jobDeploymentContext } from "@/github-context-routes"

export function JobDeploymentLinks({ job }: { job: RunnerState }) {
  const { t } = useTranslation()
  const context = jobDeploymentContext(job)
  if (!context) return null
  const historyContext = context.kind === "branches" ? { ...context, reference: "" } : context
  return <div className="flex flex-wrap items-center gap-2">
    <Button asChild variant="outline" size="sm"><AppLink href={githubContextPath(historyContext, "deployments")}>{t("user.deploymentHistory")}</AppLink></Button>
    <Button asChild size="sm"><AppLink href={deploymentCreatePath(context, job.head_sha)}>{t("user.deployThisCommit")}</AppLink></Button>
  </div>
}
