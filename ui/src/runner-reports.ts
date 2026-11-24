import type { AppTFunction } from "@/i18n"

const baseChartColors = [1, 2, 3, 4, 5].map(index => `var(--chart-${index})`)
// The two spec rankings can contain up to ten distinct identities.
export const reportChartPalette = [...baseChartColors, ...baseChartColors.map(color =>
  `color-mix(in srgb, ${color} 65%, var(--card))`,
)]

export type ReportDimension = "spec" | "owner" | "repository"
export type ReportQuery = {
  from: string
  to: string
  timezone: string
  grain: string
  owner?: string
  repository_missing?: string
  repository?: string
  spec?: string
}
export type ReportDuration = { seconds: number; samples: number; eligible: number }
export type ReportTotals = {
  requests: number
  started: number
  rejected: number
  terminal: number
  active_accounts: number
  active_repositories: number
  mapped_accounts: number
  known_specs: number
  creating_times: number
  stopping_times: number
  terminal_times: number
  job_results: number
  job_result_eligible: number
  runtime: ReportDuration
  startup: ReportDuration
  creation: ReportDuration
  cleanup: ReportDuration
  completion: ReportDuration
}
export type EfficiencyGroup = { dimension: string; key: string; count: number }
export type EfficiencySummary = {
  startup_p95: number | null
  creation_p95: number | null
  cleanup_p95: number | null
  groups: EfficiencyGroup[]
  rejections?: Record<string, Record<string, number>>
}
export type RunnerReport = {
  efficiency?: EfficiencySummary
  query: ReportQuery
  snapshot: string
  distributions?: Record<ReportDimension, { total: number; requests: ReportGroup[]; runtime: ReportGroup[] }>
  observed_at: string
  expires_at: string
  request_detail_available?: boolean
  outcomes?: Record<string, number>
  spec_dashboard?: SpecDashboard
  window: { start: string; end: string; previous_start: string; previous_end: string }
  totals: ReportTotals
  previous: ReportTotals
  trend: (ReportTotals & { date: string })[]
}
export type ReportGroup = ReportTotals & { key: string; label: string }
export function reportGroupLabel(t: AppTFunction, dimension: ReportDimension, key: string, label: string) {
  if (dimension === "owner" && key.startsWith("installation:")) {
    return t("reports.historicalInstallation", { id: key.slice("installation:".length) })
  }
  return label.trim() || t("common.unknown")
}

export type ActivityGroup = ReportGroup & {
  active_days: number
  used_specs: number
  last_queued_at: string
  outcomes: Record<string, number>
}
export type ReportRequestRow = {
  efficiency?: Partial<Record<"startup" | "creation" | "cleanup", number>>
  id: string
  queued_at: string
  repository: string
  spec_label: string
  job_result: string
}
export type ReportSpecUsage = {
  key: string
  label: string
  name: string
  source: string
  scope_type?: string
  scope_id?: number
  current: boolean
  enabled: boolean
  published: boolean
  template_source?: string
  requests: number
  started: number
  repositories: number
  runtime: ReportDuration
  creation: ReportDuration
  average_runtime_seconds?: number
  p95_runtime_seconds?: number
  average_creation_seconds?: number
  p95_creation_seconds?: number
  repository_mix?: SpecMix
  owner_mix?: SpecMix
  spec_trend?: SpecSegment[]
  job_results?: Record<string, number>
}
export type SpecMix = { requests: SpecSegment[]; runtime: SpecSegment[] }
export type SpecSegment = { key: string; label: string; started?: number; requests: number; runtime: ReportDuration; other?: boolean }
export type SpecDashboard = { summary: ReportSpecUsage; catalog: number; used_current: number; historical_groups: number; total: number; items: ReportSpecUsage[] }
export type ReportSpecPage = { items: ReportSpecUsage[]; total: number; offset: number; snapshot: string }
export type ReportPage<T> = { items: T[]; total: number; offset: number; snapshot: string }
export const reportQueryKeys = ["from", "to", "timezone", "grain", "owner", "repository", "repository_missing", "spec"] as const

export function reportQueryParams(search: string): URLSearchParams {
  const source = new URLSearchParams(search),
    result = new URLSearchParams()
  for (const key of reportQueryKeys) if (source.has(key)) result.set(key, source.get(key)!)
  if (source.has("snapshot")) result.set("snapshot", source.get("snapshot")!)
  return result
}
export function reportReturnSearch(search: string): string | null {
  const value = new URLSearchParams(search).get("report_return")
  if (!value || !value.startsWith("/admin/reports?")) return null
  try {
    const url = new URL(value, "https://runner.invalid")
    return url.origin === "https://runner.invalid" && url.pathname === "/admin/reports" && !url.hash
      ? url.search
      : null
  } catch {
    return null
  }
}
export function reportPreset(days: number, timezone: string, now = new Date()): { from: string; to: string } {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(now)
  const part = (key: string) => Number(parts.find((p) => p.type === key)!.value)
  const end = new Date(Date.UTC(part("year"), part("month") - 1, part("day") + 1))
  const start = new Date(end)
  start.setUTCDate(start.getUTCDate() - days)
  return { from: start.toISOString().slice(0, 10), to: end.toISOString().slice(0, 10) }
}

// The remainder uses full snapshot totals, never a normalized Top 5 subtotal.
export function reportDistribution(
  page: Pick<ReportPage<ReportGroup>, "items" | "total">,
  totals: ReportTotals,
  metric: "requests" | "runtime",
) {
  const value = (requests: number, runtime: ReportDuration) =>
    metric === "requests" ? requests : runtime.samples > 0 ? runtime.seconds / 3600 : null
  const rows = page.items.map((group) => ({
    key: group.key,
    label: group.label,
    value: value(group.requests, group.runtime),
    other: false,
  }))
  if (page.total > page.items.length) {
    const requests = Math.max(0, totals.requests - page.items.reduce((sum, group) => sum + group.requests, 0))
    const runtime = {
      seconds: Math.max(
        0,
        totals.runtime.seconds - page.items.reduce((sum, group) => sum + group.runtime.seconds, 0),
      ),
      samples: Math.max(
        0,
        totals.runtime.samples - page.items.reduce((sum, group) => sum + group.runtime.samples, 0),
      ),
      eligible: 0,
    }
    rows.push({ key: "", label: "", value: value(requests, runtime), other: true })
  }
  return rows
}

export function outcomeCategory(key: string) {
  switch (key) {
    case "success":
    case "failure":
    case "timed_out":
    case "cancelled":
    case "skipped":
    case "unknown":
      return key
    default:
      return "other"
  }
}

export function reportTimeFormatter(locale: string | undefined, timeZone: string) {
  const options: Intl.DateTimeFormatOptions = { dateStyle: "short", timeStyle: "medium", timeZone }
  try {
    const formatter = new Intl.DateTimeFormat(locale, options)
    return (value: string) => formatter.format(new Date(value)) + " · " + timeZone
  } catch (error) {
    // The server's timezone database can include zones the browser does not recognize.
    if (!(error instanceof RangeError)) throw error
    return (value: string) => new Date(value).toISOString()
  }
}
