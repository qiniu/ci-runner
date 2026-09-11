import {
  activeStatuses,
  type AdminAccountStats,
  type Metric,
  type RunnerState,
  type RunnerDisplayStatus,
} from "@/admin-types"
import i18n from "@/i18n"
import type { AppTFunction } from "@/i18n"

const runnerStatusKeys = {
  queued: "common.statusQueued",
  creating: "common.statusCreating",
  running: "common.statusRunning",
  stopping: "common.statusStopping",
  completed: "common.statusCompleted",
  failed: "common.statusFailed",
  unmatched: "common.statusUnmatched",
} as const satisfies Record<RunnerDisplayStatus, string>

export function runnerStatusLabel(status: RunnerDisplayStatus) {
  return i18n.t(runnerStatusKeys[status])
}

export function runnerDisplayStatus(runner: RunnerState): RunnerDisplayStatus {
  if (
    runner.status === "failed"
    && runner.failure_stage === "admission"
    && runner.failure_reason === "profile_labels_not_matched"
  ) {
    return "unmatched"
  }
  return runner.status
}

export function runnerMetrics(
  runners: RunnerState[],
  runnerSpecCount: number,
  t: AppTFunction,
): Metric[] {
  const count = (status: RunnerDisplayStatus) => runners.filter((runner) => runnerDisplayStatus(runner) === status).length
  return [
    {
      id: "active",
      label: t("admin.activeMetric"),
      value: runners.filter((runner) => activeStatuses.has(runner.status)).length,
      description: t("admin.activeMetricDescription"),
    },
    {
      id: "completed",
      label: t("admin.completedMetric"),
      value: count("completed"),
      description: t("admin.completedMetricDescription"),
    },
    {
      id: "failed",
      label: t("admin.failedMetric"),
      value: count("failed"),
      description: t("admin.failedMetricDescription"),
    },
    {
      id: "runner-specs",
      label: t("sidebar.runnerSpecs"),
      value: runnerSpecCount,
      description: t("admin.runnerSpecsMetricDescription"),
    },
  ]
}

export function accountMetrics(stats: AdminAccountStats, t: AppTFunction): Metric[] {
  return [
    {
      id: "accounts",
      label: t("admin.accountsMetric"),
      value: stats.total_accounts,
      description: t("admin.accountsMetricDescription"),
    },
    {
      id: "administrators",
      label: t("admin.administratorsMetric"),
      value: stats.admin_accounts,
      description: t("admin.administratorsMetricDescription"),
    },
    {
      id: "users",
      label: t("admin.users"),
      value: stats.user_accounts,
      description: t("admin.usersMetricDescription"),
    },
    {
      id: "identities",
      label: t("admin.linkedIdentities"),
      value: stats.oauth_identities,
      description: t("admin.identitiesMetricDescription"),
    },
  ]
}

export function formatTime(
  value?: string,
  locale?: string,
  options?: { fractionalSecondDigits?: 1 | 2 | 3 },
) {
  if (!value) return "-"
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value
  if (options?.fractionalSecondDigits) {
    const formatOptions = {
      year: "numeric",
      month: "numeric",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      second: "2-digit",
      fractionalSecondDigits: options.fractionalSecondDigits,
      hour12: false,
    } as Intl.DateTimeFormatOptions
    return date.toLocaleString(locale, formatOptions)
  }
  return date.toLocaleString(locale)
}

export function formatRunnerDuration(job: {
  running_at?: string
  created_at?: string
  completed_at?: string
  failed_at?: string
  updated_at?: string
}) {
  const start = timeValue(job.running_at || job.created_at)
  const end = timeValue(job.completed_at || job.failed_at || job.updated_at)
  if (!start || !end || end <= start) return ""
  const totalSeconds = Math.max(0, Math.round((end - start) / 1000))
  if (totalSeconds < 60) return `${totalSeconds}s`
  const minutes = Math.floor(totalSeconds / 60)
  const seconds = totalSeconds % 60
  if (minutes < 60) return seconds ? `${minutes}m ${seconds}s` : `${minutes}m`
  const hours = Math.floor(minutes / 60)
  return `${hours}h ${minutes % 60}m`
}

function timeValue(value?: string) {
  if (!value) return 0
  const time = Date.parse(value)
  return Number.isFinite(time) ? time : 0
}
