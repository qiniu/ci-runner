export type ReportDimension = "spec" | "owner" | "repository"
export type ReportQuery = {
  from: string
  to: string
  timezone: string
  grain: string
  owner?: string
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
export type RunnerReport = {
  query: ReportQuery
  snapshot: string
  observed_at: string
  expires_at: string
  window: { start: string; end: string; previous_start: string; previous_end: string }
  totals: ReportTotals
  previous: ReportTotals
  trend: (ReportTotals & { date: string })[]
}
export type ReportGroup = ReportTotals & { key: string; label: string }
export type ReportRow = {
  id: string
  queued_at: string
  repository: string
  owner_label: string
  spec_label: string
  status: string
  job_result: string
  runtime_seconds: number | null
}
export type ReportPage<T> = { items: T[]; total: number; offset: number; snapshot: string }
export const reportQueryKeys = ["from", "to", "timezone", "grain", "owner", "repository", "spec"] as const

export function reportQueryParams(search: string): URLSearchParams {
  const source = new URLSearchParams(search),
    result = new URLSearchParams()
  for (const key of reportQueryKeys) if (source.has(key)) result.set(key, source.get(key)!)
  if (source.has("snapshot")) result.set("snapshot", source.get("snapshot")!)
  return result
}
export function reportOffset(raw: string | null): number {
  const n = Number(raw)
  return Number.isInteger(n) && n >= 0 && n <= 100000 ? n : 0
}
export function reportReturnSearch(search: string): string | null {
  const value = new URLSearchParams(search).get("report_return")
  if (!value || !value.startsWith("/admin/reports?")) return null
  try {
    const url = new URL(value, "https://runner.invalid")
    return url.origin === "https://runner.invalid" && url.pathname === "/admin/reports" && !url.hash ? url.search : null
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
