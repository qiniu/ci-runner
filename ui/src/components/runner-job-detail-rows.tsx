import type { ReactNode } from "react"
import { ExternalLink } from "lucide-react"

import type { RunnerState } from "@/admin-types"
import { formatTime } from "@/admin-format"
import type { AppTFunction } from "@/i18n"
import { workflowRunURL } from "@/components/runner-github-links"

export type RunnerJobDetailRow = {
  id: string
  label: string
  value: ReactNode
}

export function runnerJobDetailRows(
  job: RunnerState,
  t: AppTFunction,
  locale?: string,
): RunnerJobDetailRow[] {
  return [
    { id: "id", label: "ID", value: job.id },
    { id: "status", label: t("common.status"), value: runnerStatusLabel(job.status, t) },
    { id: "repository", label: t("common.repository"), value: job.repository_full_name },
    { id: "runner_spec", label: t("common.runnerSpec"), value: job.runner_spec_name || t("user.matchedByLabels") },
    { id: "runner_group", label: t("user.runnerGroup"), value: job.runner_group },
    { id: "sandbox", label: t("common.sandbox"), value: job.sandbox_id },
    { id: "pid", label: "PID", value: job.process_pid },
    { id: "job", label: t("user.jobName"), value: job.assigned_job_name || job.assigned_job_id || job.workflow_job_id },
    { id: "workflow", label: t("user.workflow"), value: job.workflow_name },
    { id: "workflow_run", label: t("user.workflowRun"), value: job.workflow_run_id },
    { id: "workflow_attempt", label: t("user.workflowAttempt"), value: job.workflow_run_attempt },
    { id: "pull_request", label: t("user.pullRequest"), value: job.pull_request_number },
    { id: "branch", label: t("user.branch"), value: job.head_branch },
    { id: "commit", label: t("user.commit"), value: job.head_sha },
    { id: "created_at", label: t("common.created"), value: formatTime(job.created_at, locale) },
    { id: "updated_at", label: t("common.updated"), value: formatTime(job.updated_at, locale) },
    { id: "completed_at", label: t("user.finished"), value: formatTime(job.completed_at, locale) },
    { id: "failed_at", label: t("user.failedAt"), value: formatTime(job.failed_at, locale) },
    { id: "retry_count", label: t("user.retryCount"), value: job.retry_count },
    { id: "next_retry_at", label: t("user.nextRetry"), value: formatTime(job.next_retry_at, locale) },
    { id: "requested_labels", label: t("user.requestedLabels"), value: job.requested_labels?.join(", ") },
    { id: "failure_reason", label: t("user.failure"), value: job.failure_reason },
    { id: "last_error", label: t("user.lastError"), value: job.last_error_message || job.error },
  ]
}

export function workflowRunLink(job: RunnerState) {
  if (!job.workflow_run_id) return "-"
  const url = workflowRunURL(job)
  if (!url) return job.workflow_run_id
  return (
    <a className="inline-flex items-center gap-1 text-primary hover:underline" href={url} target="_blank" rel="noreferrer">
      {job.workflow_run_id}
      <ExternalLink className="h-3 w-3" />
    </a>
  )
}

function runnerStatusLabel(status: RunnerState["status"], t: AppTFunction) {
  const keys = {
    queued: "common.statusQueued",
    creating: "common.statusCreating",
    running: "common.statusRunning",
    stopping: "common.statusStopping",
    completed: "common.statusCompleted",
    failed: "common.statusFailed",
  } as const
  return t(keys[status])
}
