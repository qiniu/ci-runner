import { useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import { RefreshCw, ArrowRight, Filter } from "lucide-react"
import { ResponsiveContainer, BarChart, Bar, XAxis, YAxis, Tooltip, CartesianGrid } from "recharts"
import {
  reportOffset,
  reportPreset,
  reportQueryParams,
  type ReportQuery,
  type ReportDimension,
  type ReportGroup,
  type ReportPage,
  type ReportRow,
  type RunnerReport,
} from "@/runner-reports"
import { AppLink } from "@/components/app-link"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Card, CardContent } from "@/components/ui/card"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

type Request = (url: string, options?: RequestInit) => Promise<unknown>
type Resource<T> = { url: string; data?: T; error?: string }
function useReportResource<T>(url: string, request: Request, revision = 0) {
  const [state, setState] = useState<Resource<T>>({ url: "" })
  const [attempt, setAttempt] = useState(0)
  const retry = useCallback(() => setAttempt((v) => v + 1), [])
  useEffect(() => {
    const controller = new AbortController()
    let current = true
    if (url)
      void request(url, { signal: controller.signal })
        .then((data) => {
          if (current) setState({ url, data: data as T })
        })
        .catch((error) => {
          if (current) setState({ url, error: error instanceof Error ? error.message : String(error) })
        })
    return () => {
      current = false
      controller.abort()
    }
  }, [url, request, revision, attempt])
  return { ...(state.url === url ? state : { url }), retry }
}
function hours(seconds: number) {
  return (seconds / 3600).toLocaleString(undefined, { maximumFractionDigits: 2 })
}
function coverage(samples: number, total: number) {
  return total
    ? `${samples.toLocaleString()} / ${total.toLocaleString()} (${((samples / total) * 100).toFixed(1)}%)`
    : "—"
}

export function RunnerReportsSection({
  search,
  request,
  onNavigate,
}: {
  search: string
  request: Request
  onNavigate: (search: string) => void
}) {
  const { t } = useTranslation()
  const [revision, setRevision] = useState(0)
  const params = reportQueryParams(search)
  const query = params.toString()
  const overview = useReportResource<RunnerReport>(`/reports/overview?${query}`, request, revision)
  const activeParams = new URLSearchParams(params)
  activeParams.delete("snapshot")
  const active = useReportResource<{ counts: Record<string, number>; observed_at: string }>(
    `/reports/active?${activeParams}`,
    request,
    revision,
  )
  const report = overview.data
  const urlState = new URLSearchParams(search)
  const navigate = (values: Record<string, string | null>) => {
    const next = new URLSearchParams(search)
    if (report) {
      for (const [key, value] of Object.entries(report.query)) if (value) next.set(key, value)
      next.set("snapshot", report.snapshot)
    }
    for (const [key, value] of Object.entries(values)) {
      if (value === null) next.delete(key)
      else next.set(key, value)
    }
    onNavigate(`?${next}`)
  }
  const refresh = () => {
    navigate({ snapshot: null })
    setRevision((v) => v + 1)
  }
  const filter = (dimension: ReportDimension, key: string) => {
    const next = new URLSearchParams(
      report ? Object.entries(report.query).filter(([, value]) => Boolean(value)) : params,
    )
    next.delete("snapshot")
    next.set(dimension, key)
    onNavigate(`?${next}`)
  }
  const defaults: ReportQuery = { ...reportPreset(30, "UTC"), timezone: "UTC", grain: "day" }
  const filterQuery = Object.fromEntries([...params].filter(([key]) => key !== "snapshot"))
  const selected: ReportQuery = { ...defaults, ...filterQuery }
  const dimension = urlState.get("dimension") || ""
  const sort = urlState.get("sort") === "runtime" ? "runtime" : "requests"
  return (
    <section className="min-w-0 space-y-6" aria-labelledby="reports-title">
      <div className="flex items-start justify-between gap-3">
        <div>
          <h1 id="reports-title" className="text-xl font-semibold">
            {t("reports.title")}
          </h1>
          <p className="mt-1 max-w-3xl text-sm text-muted-foreground">{t("reports.description")}</p>
        </div>
        <Button variant="outline" size="icon" onClick={refresh} aria-label={t("common.refresh")}>
          <RefreshCw />
        </Button>
      </div>
      <ReportFilters
        key={JSON.stringify(selected)}
        query={selected}
        onApply={(q) => onNavigate(`?${new URLSearchParams(Object.entries(q).filter(([, value]) => Boolean(value)))}`)}
      />
      <Card>
        <CardContent className="space-y-3 pt-5">
          <div>
            <h2 className="font-semibold">{t("reports.active")}</h2>
            <p className="text-sm text-muted-foreground">{t("reports.activeHelp")}</p>
          </div>
          <ResourceState resource={active} />
          {active.data && (
            <>
              <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
                {(["queued", "creating", "running", "stopping"] as const).map((status) => (
                  <div key={status}>
                    <p className="text-sm text-muted-foreground">{t(`reports.${status}`)}</p>
                    <p className="text-2xl font-semibold tabular-nums">
                      {active.data!.counts[status].toLocaleString()}
                    </p>
                  </div>
                ))}
              </div>
              <p className="text-xs text-muted-foreground">
                {t("reports.observed", { time: new Date(active.data.observed_at).toLocaleString() })}
              </p>
            </>
          )}
        </CardContent>
      </Card>
      <ResourceState resource={overview} />
      {report && (
        <>
          <div className="space-y-1 text-xs text-muted-foreground">
            <p>
              {t("reports.window", {
                from: report.window.start,
                to: report.window.end,
                timezone: report.query.timezone,
              })}
            </p>
            <p>{t("reports.comparison", { from: report.window.previous_start, to: report.window.previous_end })}</p>
            <p>{t("reports.observed", { time: new Date(report.observed_at).toLocaleString() })}</p>
          </div>
          <div className="grid grid-cols-2 gap-3 lg:grid-cols-3">
            {(["requests", "started", "rejected", "active_accounts", "active_repositories", "runtime"] as const).map(
              (metric) => {
                const value = metric === "runtime" ? report.totals.runtime.seconds : report.totals[metric]
                const previous = metric === "runtime" ? report.previous.runtime.seconds : report.previous[metric]
                const available = metric !== "runtime" || report.totals.runtime.samples > 0
                const comparable = metric !== "runtime" || report.previous.runtime.samples > 0
                const delta =
                  !available || !comparable
                    ? "—"
                    : previous === 0
                      ? value > 0
                        ? t("reports.new")
                        : "—"
                      : `${value >= previous ? "+" : ""}${(((value - previous) / previous) * 100).toFixed(1)}%`
                return (
                  <Card key={metric}>
                    <CardContent className="pt-4">
                      <p className="text-sm text-muted-foreground">{t(`reports.${metric}`)}</p>
                      <p className="my-2 text-2xl font-semibold tabular-nums">
                        {!available ? "—" : metric === "runtime" ? hours(value) : value.toLocaleString()}
                      </p>
                      <p className="text-xs text-muted-foreground">
                        {delta} · {t("reports.vsPrevious")}
                      </p>
                    </CardContent>
                  </Card>
                )
              },
            )}
          </div>
          <p className="text-sm text-muted-foreground">{t("reports.runtimeHelp")}</p>
          <Trend report={report} />
          <Coverage report={report} />
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h2 className="font-semibold">{t("reports.distribution")}</h2>
            <ReportSelect
              label={t("reports.sort")}
              value={sort}
              onChange={(value) =>
                navigate({ sort: value, spec_offset: null, owner_offset: null, repository_offset: null })
              }
              options={[
                ["requests", t("reports.requests")],
                ["runtime", t("reports.runtime")],
              ]}
            />
          </div>
          <div className="grid min-w-0 gap-4 xl:grid-cols-3">
            {(["spec", "owner", "repository"] as const).map((d) => (
              <Distribution
                key={`${d}-${revision}`}
                dimension={d}
                report={report}
                request={request}
                sort={sort}
                offset={reportOffset(urlState.get(`${d}_offset`))}
                onPage={(offset) => navigate({ [`${d}_offset`]: String(offset) })}
                onOpen={(key) => navigate({ dimension: d, key, offset: null })}
                onFilter={(key) => filter(d, key)}
              />
            ))}
          </div>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <h2 className="font-semibold">{t("reports.details")}</h2>
            <Button variant="outline" size="sm" onClick={() => navigate({ dimension: null, key: null, offset: null })}>
              {t("reports.allRequests")}
            </Button>
          </div>
          {dimension && <p className="break-all text-sm text-muted-foreground">{t("reports.selectedGroup")}</p>}
          <RequestRows
            key={revision}
            report={report}
            dimension={dimension}
            groupKey={urlState.get("key") || ""}
            offset={reportOffset(urlState.get("offset"))}
            request={request}
            search={search}
            onPage={(offset) => navigate({ offset: String(offset) })}
          />
        </>
      )}
    </section>
  )
}

function ReportFilters({ query, onApply }: { query: ReportQuery; onApply: (query: ReportQuery) => void }) {
  const { t } = useTranslation()
  const [draft, setDraft] = useState(query),
    [error, setError] = useState("")
  return (
    <form
      className="space-y-3 rounded-lg border p-4"
      onSubmit={(e) => {
        e.preventDefault()
        onApply(draft)
      }}
    >
      <div className="flex flex-wrap items-center gap-2">
        {[7, 30, 90].map((days) => (
          <Button
            type="button"
            variant="outline"
            size="sm"
            key={days}
            onClick={() => {
              try {
                const next = { ...draft, ...reportPreset(days, draft.timezone) }
                setDraft(next)
                setError("")
                onApply(next)
              } catch {
                setError(t("reports.invalidTimezone"))
              }
            }}
          >
            {t("reports.lastDays", { days })}
          </Button>
        ))}
      </div>
      <div className="grid items-end gap-3 sm:grid-cols-2 lg:grid-cols-5">
        {(["from", "to", "timezone", "repository"] as const).map((key) => (
          <div className="min-w-0 space-y-1.5" key={key}>
            <Label htmlFor={`report-${key}`}>{t(`reports.${key}`)}</Label>
            <Input
              id={`report-${key}`}
              type={key === "from" || key === "to" ? "date" : "text"}
              value={draft[key] || ""}
              onChange={(e) => setDraft({ ...draft, [key]: e.target.value })}
            />
          </div>
        ))}
        <ReportSelect
          label={t("reports.grain")}
          value={draft.grain}
          onChange={(grain) => setDraft({ ...draft, grain })}
          options={(["day", "week", "month"] as const).map((g) => [g, t(`reports.${g}`)])}
        />
      </div>
      <div className="flex flex-wrap items-center gap-3">
        <Button type="submit">{t("reports.apply")}</Button>
        {(draft.owner || draft.spec || draft.repository) && (
          <Button
            type="button"
            variant="ghost"
            onClick={() => {
              const next = { ...draft, owner: "", spec: "", repository: "" }
              setDraft(next)
              onApply(next)
            }}
          >
            {t("reports.clearDimensions")}
          </Button>
        )}
        <span className="text-xs text-muted-foreground">{t("reports.filterHelp")}</span>
      </div>
      {(draft.owner || draft.spec) && (
        <p className="text-sm text-muted-foreground">
          {t("reports.filtered", {
            dimensions: [draft.owner ? t("reports.owner") : "", draft.spec ? t("reports.spec") : ""]
              .filter(Boolean)
              .join(" / "),
          })}
        </p>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
    </form>
  )
}
function ReportSelect({
  label,
  value,
  options,
  onChange,
}: {
  label: string
  value: string
  options: string[][]
  onChange: (value: string) => void
}) {
  return (
    <div className="min-w-0 space-y-1.5">
      <span className="text-sm font-medium">{label}</span>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger className="w-full min-w-32" aria-label={label}>
          <SelectValue />
        </SelectTrigger>
        <SelectContent>
          {options.map(([key, text]) => (
            <SelectItem value={key} key={key}>
              {text}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
    </div>
  )
}
function ResourceState({ resource }: { resource: { data?: unknown; error?: string; retry?: () => void } }) {
  const { t } = useTranslation()
  if (resource.error)
    return (
      <div role="alert" className="break-words rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm">
        <p>{t("reports.loadFailed")}</p>
        <p>{resource.error}</p>
        <p className="mt-1 text-muted-foreground">{t("reports.retryHelp")}</p>
        <Button variant="outline" size="sm" className="mt-2" onClick={resource.retry}>
          {t("auth.tryAgain")}
        </Button>
      </div>
    )
  if (!resource.data)
    return (
      <p role="status" className="py-6 text-center text-sm text-muted-foreground">
        {t("common.loading")}
      </p>
    )
  return null
}
function Trend({ report }: { report: RunnerReport }) {
  const { t } = useTranslation()
  const [metric, setMetric] = useState("requests")
  const points = report.trend.map((b) => ({
    date: b.date,
    value:
      metric === "requests"
        ? b.requests
        : metric === "started"
          ? b.started
          : b.runtime.samples
            ? b.runtime.seconds / 3600
            : null,
  }))
  return (
    <Card>
      <CardContent className="space-y-4 pt-5">
        <div className="flex flex-wrap justify-between gap-3">
          <h2 className="font-semibold">{t("reports.trend")}</h2>
          <ReportSelect
            label={t("reports.metric")}
            value={metric}
            onChange={setMetric}
            options={[
              ["requests", t("reports.requests")],
              ["started", t("reports.started")],
              ["runtime", t("reports.runtime")],
            ]}
          />
        </div>
        <div className="h-64 min-w-0" aria-label={t("reports.trend")}>
          <ResponsiveContainer width="100%" height="100%">
            <BarChart data={points}>
              <CartesianGrid vertical={false} stroke="var(--border)" />
              <XAxis dataKey="date" tick={{ fontSize: 11, fill: "var(--muted-foreground)" }} minTickGap={35} />
              <YAxis width={45} tick={{ fontSize: 11, fill: "var(--muted-foreground)" }} />
              <Tooltip
                contentStyle={{ background: "var(--background)", borderColor: "var(--border)", borderRadius: 8 }}
              />
              <Bar
                dataKey="value"
                name={
                  metric === "requests"
                    ? t("reports.requests")
                    : metric === "started"
                      ? t("reports.started")
                      : t("reports.runtime")
                }
                fill="var(--primary)"
                radius={[3, 3, 0, 0]}
              />
            </BarChart>
          </ResponsiveContainer>
        </div>
        <details>
          <summary className="cursor-pointer text-sm text-muted-foreground">{t("reports.trendTable")}</summary>
          <div className="max-h-64 overflow-auto">
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("reports.from")}</TableHead>
                  <TableHead>{t("reports.requests")}</TableHead>
                  <TableHead>{t("reports.started")}</TableHead>
                  <TableHead>{t("reports.runtime")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {report.trend.map((b) => (
                  <TableRow key={b.date}>
                    <TableCell>{b.date}</TableCell>
                    <TableCell>{b.requests}</TableCell>
                    <TableCell>{b.started}</TableCell>
                    <TableCell>{b.runtime.samples ? hours(b.runtime.seconds) : "—"}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </details>
      </CardContent>
    </Card>
  )
}
function Coverage({ report }: { report: RunnerReport }) {
  const { t } = useTranslation(),
    totals = report.totals
  const values = [
    [t("reports.owner"), totals.mapped_accounts, totals.requests],
    [t("reports.spec"), totals.known_specs, totals.requests],
    [t("reports.creatingTime"), totals.creating_times, totals.requests],
    [t("reports.runningTime"), totals.started, totals.requests],
    [t("reports.stoppingTime"), totals.stopping_times, totals.requests],
    [t("reports.terminalTime"), totals.terminal_times, totals.terminal],
    [t("reports.jobResult"), totals.job_results, totals.job_result_eligible],
  ] as const
  return (
    <details className="rounded-lg border p-4">
      <summary className="cursor-pointer font-medium">
        {t("reports.coverage")} ·{" "}
        {t("reports.runtimeSamples", { value: coverage(totals.runtime.samples, totals.runtime.eligible) })}
      </summary>
      <p className="my-3 text-sm text-muted-foreground">{t("reports.coverageHelp")}</p>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {values.map(([label, n, denominator]) => (
          <div key={label} className="text-sm">
            <p className="text-muted-foreground">{label}</p>
            <p>{coverage(n, denominator)}</p>
          </div>
        ))}
        {(["runtime", "startup", "creation", "cleanup", "completion"] as const).map((metric) => (
          <div className="text-sm" key={metric}>
            <p className="text-muted-foreground">{t(`reports.${metric}`)}</p>
            <p>{coverage(totals[metric].samples, totals[metric].eligible)}</p>
          </div>
        ))}
      </div>
    </details>
  )
}
function Distribution({
  dimension,
  report,
  request,
  sort,
  offset,
  onPage,
  onOpen,
  onFilter,
}: {
  dimension: ReportDimension
  report: RunnerReport
  request: Request
  sort: string
  offset: number
  onPage: (offset: number) => void
  onOpen: (key: string) => void
  onFilter: (key: string) => void
}) {
  const { t } = useTranslation()
  const groups = useReportResource<ReportPage<ReportGroup>>(
    `/reports/${encodeURIComponent(report.snapshot)}/groups?${new URLSearchParams({ dimension, sort, offset: String(offset), limit: "5" })}`,
    request,
  )
  const pageRequests = groups.data?.items.reduce((sum, row) => sum + row.requests, 0) || 0
  return (
    <Card className="min-w-0">
      <CardContent className="space-y-4 pt-5">
        <h3 className="font-semibold">{t(`reports.${dimension}`)}</h3>
        <ResourceState resource={groups} />
        {groups.data && (
          <>
            {groups.data.items.length === 0 && <p className="text-sm text-muted-foreground">{t("reports.empty")}</p>}
            {groups.data.items.map((g) => (
              <div key={g.key} className="space-y-1.5 border-b pb-3 last:border-b-0">
                <div className="flex min-w-0 items-start gap-2">
                  <Button
                    variant="link"
                    className="h-auto min-w-0 flex-1 justify-start break-all whitespace-normal p-0 text-left"
                    onClick={() => onOpen(g.key)}
                  >
                    {g.label || t("common.unknown")}
                  </Button>
                  <Button
                    variant="ghost"
                    size="icon"
                    className="size-7 shrink-0"
                    aria-label={t("reports.filterGroup", { name: g.label || t("common.unknown") })}
                    onClick={() => onFilter(g.key)}
                    disabled={!g.key}
                  >
                    <Filter className="size-3.5" />
                  </Button>
                </div>
                <p className="text-xs text-muted-foreground">
                  {t("reports.requestShare", { value: coverage(g.requests, report.totals.requests) })} ·{" "}
                  {g.runtime.samples ? t("reports.hours", { value: hours(g.runtime.seconds) }) : "—"} ·{" "}
                  {t("reports.runtimeShare", {
                    value:
                      g.runtime.samples > 0 && report.totals.runtime.seconds > 0
                        ? ((g.runtime.seconds / report.totals.runtime.seconds) * 100).toFixed(1) + "%"
                        : "—",
                  })}
                </p>
                <div className="h-1 rounded bg-muted">
                  <div
                    className="h-1 rounded bg-primary/70"
                    style={{
                      width: `${sort === "runtime" ? (report.totals.runtime.seconds ? (g.runtime.seconds / report.totals.runtime.seconds) * 100 : 0) : report.totals.requests ? (g.requests / report.totals.requests) * 100 : 0}%`,
                    }}
                  />
                </div>
              </div>
            ))}
            <p className="text-xs text-muted-foreground">
              {t("reports.other", { count: report.totals.requests - pageRequests })}
            </p>
            <Pagination offset={offset} total={groups.data.total} size={5} onPage={onPage} />
          </>
        )}
      </CardContent>
    </Card>
  )
}
function RequestRows({
  report,
  dimension,
  groupKey,
  offset,
  request,
  search,
  onPage,
}: {
  report: RunnerReport
  dimension: string
  groupKey: string
  offset: number
  request: Request
  search: string
  onPage: (offset: number) => void
}) {
  const { t } = useTranslation()
  const rows = useReportResource<ReportPage<ReportRow>>(
    `/reports/${encodeURIComponent(report.snapshot)}/requests?${new URLSearchParams({ dimension, key: groupKey, offset: String(offset), limit: "20" })}`,
    request,
  )
  const returnParams = new URLSearchParams(search)
  for (const [key, value] of Object.entries(report.query)) if (value) returnParams.set(key, value)
  returnParams.set("snapshot", report.snapshot)
  return (
    <div className="min-w-0 space-y-3">
      <ResourceState resource={rows} />
      {rows.data && (
        <>
          <div className="overflow-hidden rounded-md border">
            <Table>
              <TableHeader>
                <TableRow>
                  {[
                    t("reports.request"),
                    t("reports.repository"),
                    t("reports.spec"),
                    t("common.status"),
                    t("reports.jobResult"),
                    t("reports.runtime"),
                  ].map((label) => (
                    <TableHead key={label}>{label}</TableHead>
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.data.items.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={6} className="h-24 text-center text-muted-foreground">
                      {t("reports.empty")}
                    </TableCell>
                  </TableRow>
                ) : (
                  rows.data.items.map((row) => (
                    <TableRow key={row.id}>
                      <TableCell>
                        <AppLink
                          className="inline-flex max-w-64 items-center gap-2 break-all whitespace-normal font-mono text-xs hover:underline"
                          href={`/admin/runner_requests/${encodeURIComponent(row.id)}?${new URLSearchParams({ report_return: `/admin/reports?${returnParams}` })}`}
                        >
                          {row.id}
                          <ArrowRight className="size-3 shrink-0" />
                        </AppLink>
                      </TableCell>
                      <TableCell className="max-w-64 break-all whitespace-normal">
                        {row.repository || t("common.unknown")}
                      </TableCell>
                      <TableCell className="max-w-64 break-all whitespace-normal">
                        {row.spec_label || t("common.unknown")}
                      </TableCell>
                      <TableCell>{row.status}</TableCell>
                      <TableCell>{row.job_result === "unknown" ? t("common.unknown") : row.job_result}</TableCell>
                      <TableCell>{row.runtime_seconds === null ? "—" : hours(row.runtime_seconds)}</TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          </div>
          <Pagination offset={offset} total={rows.data.total} size={20} onPage={onPage} />
        </>
      )}
    </div>
  )
}
function Pagination({
  offset,
  total,
  size,
  onPage,
}: {
  offset: number
  total: number
  size: number
  onPage: (offset: number) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
      <span>
        {t("reports.page", {
          from: total ? Math.min(offset + 1, total) : 0,
          to: Math.min(offset + size, total),
          total,
        })}
      </span>
      <div className="flex gap-1">
        <Button variant="outline" size="sm" disabled={offset === 0} onClick={() => onPage(Math.max(0, offset - size))}>
          {t("reports.previousPage")}
        </Button>
        <Button variant="outline" size="sm" disabled={offset + size >= total} onClick={() => onPage(offset + size)}>
          {t("reports.nextPage")}
        </Button>
      </div>
    </div>
  )
}
