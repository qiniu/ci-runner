import { ExternalLink } from "lucide-react"
import { useTranslation } from "react-i18next"

import { formatRunnerCleanupDuration, formatTime, runnerDisplayStatus, runnerStatusLabel } from "@/admin-format"
import type { RunnerState } from "@/admin-types"
import { Detail } from "@/components/admin-shared"
import type { AppTFunction } from "@/i18n"

export function RunnerRequestDetails({ runner }: { runner: RunnerState }) {
  const { t, i18n } = useTranslation()

  return (
    <div className="grid grid-cols-1 gap-x-10 gap-y-2 xl:grid-cols-2">
      <Detail label="ID" value={runner.id} />
      <Detail label={t("common.status")} value={runnerStatusLabel(runnerDisplayStatus(runner))} />
      <Detail label={t("common.repository")} value={runner.repository_full_name || "-"} />
      <Detail label={t("common.runnerSpec")} value={runner.runner_spec_name || "-"} />
      <Detail label={t("common.sandbox")} value={runner.sandbox_id || "-"} />
      <Detail label={t("admin.sandboxConfig")} value={sandboxConfigSourceDisplay(runner.sandbox_config_source, t)} />
      <Detail label={t("admin.sandboxRegion")} value={runner.sandbox_region || "-"} />
      <Detail label={t("admin.resolvedTemplateID")} value={runner.resolved_template_id || "-"} />
      <Detail label={t("admin.templateVersion")} value={runner.template_version || "-"} />
      <Detail label={t("admin.runnerVersion")} value={runner.runner_version || "-"} />
      <Detail label={t("admin.effectiveRunnerVersion")} value={runner.effective_runner_version || "-"} />
      <Detail label="PID" value={runner.process_pid || "-"} />
      <Detail label={t("user.jobName")} value={runner.assigned_job_name || runner.assigned_job_id || "-"} />
      <Detail
        label={t("admin.githubJob")}
        value={runner.github_job_url ? (
          <a
            className="inline-flex items-center gap-1 text-primary underline-offset-4 hover:underline"
            href={runner.github_job_url}
            target="_blank"
            rel="noreferrer"
          >
            {t("admin.openJob")}
            <ExternalLink className="size-3.5" />
          </a>
        ) : "-"}
      />
      <Detail label={t("user.workflowRun")} value={runner.workflow_run_id || "-"} />
      <Detail label={t("user.workflow")} value={runner.workflow_name || "-"} />
      <Detail label={t("user.workflowAttempt")} value={runner.workflow_run_attempt || "-"} />
      <Detail label={t("user.pullRequest")} value={runner.pull_request_number || "-"} />
      <Detail label={t("user.branch")} value={runner.head_branch || "-"} />
      <Detail label={t("user.commit")} value={runner.head_sha || "-"} />
      <Detail label={t("common.created")} value={formatTime(runner.created_at, i18n.resolvedLanguage)} />
      <Detail label={t("common.updated")} value={formatTime(runner.updated_at, i18n.resolvedLanguage)} />
      <Detail label={t("common.started")} value={formatTime(runner.running_at, i18n.resolvedLanguage)} />
      <Detail label={t("admin.stoppingAt")} value={formatTime(runner.stopping_at, i18n.resolvedLanguage)} />
      <Detail label={t("admin.completedAt")} value={formatTime(runner.completed_at, i18n.resolvedLanguage)} />
      <Detail label={t("user.failedAt")} value={formatTime(runner.failed_at, i18n.resolvedLanguage)} />
      <Detail
        label={t("admin.cleanupDuration")}
        value={formatRunnerCleanupDuration(runner, t("admin.cleanupInProgress"))}
      />
      <Detail label={t("admin.terminationSource")} value={terminationSourceDisplay(runner.termination_source, t)} />
      <Detail label={t("admin.runnerExitCode")} value={runner.runner_exit_code ?? "-"} />
      <Detail label={t("user.retryCount")} value={runner.retry_count || "-"} />
      <Detail label={t("user.nextRetry")} value={formatTime(runner.next_retry_at, i18n.resolvedLanguage)} />
      <Detail label={t("user.requestedLabels")} value={runner.requested_labels?.join(", ") || "-"} />
      <Detail label={t("user.failure")} value={runner.failure_reason || "-"} />
      <Detail label={t("admin.lastErrorCode")} value={runner.last_error_code || "-"} />
      <Detail label={t("admin.error")} value={runner.error || "-"} />
    </div>
  )
}

function terminationSourceDisplay(source: string | undefined, t: AppTFunction) {
  switch (source) {
    case "process_exit": return `${t("admin.terminationSourceProcessExit")} (${source})`
    case "workflow_job_webhook": return `${t("admin.terminationSourceWorkflowJobWebhook")} (${source})`
    case "manual_stop": return `${t("admin.terminationSourceManualStop")} (${source})`
    case "recovery_cleanup": return `${t("admin.terminationSourceRecoveryCleanup")} (${source})`
    case "failure_cleanup": return `${t("admin.terminationSourceFailureCleanup")} (${source})`
    case "idle_cleanup": return `${t("admin.terminationSourceIdleCleanup")} (${source})`
    default: return source || "-"
  }
}

function sandboxConfigSourceDisplay(source: string | undefined, t: AppTFunction) {
  switch (source) {
    case "installation": return t("admin.configInstallation")
    case "account": return t("admin.configAccount")
    case "inherited_account": return t("admin.configInheritedAccount")
    case "admin_default": return t("admin.configAdminDefault")
    case "request_snapshot": return t("admin.configRequestSnapshot")
    default: return "-"
  }
}
