import { lazy, Suspense, useCallback, useEffect, useState } from "react"
import { useTranslation } from "react-i18next"
import { RefreshCw, Filter, Info } from "lucide-react"
import {
  reportDistribution,
  reportPreset,
  reportQueryParams,
  type ReportQuery,
  type ReportDimension,
  type ReportGroup,
  type ReportPage,
  type RunnerReport,
} from "@/runner-reports"
import { formatTime } from "@/admin-format"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Card, CardContent } from "@/components/ui/card"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"

const ReportChart = lazy(() => import("@/components/report-chart"))

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
export function RunnerReportsSection({
  search,
  request,
  onNavigate,
}: {
  search: string
  request: Request
  onNavigate: (search: string) => void
}) {
  const { t, i18n } = useTranslation()
  const [revision, setRevision] = useState(0)
  const [trendMetric, setTrendMetric] = useState("requests-runtime")
  const params = reportQueryParams(search)
  const query = params.toString()
  const overview = useReportResource<RunnerReport>(`/reports/overview?${query}`, request, revision)
  const report = overview.data
  const urlState = new URLSearchParams(search)
  const navigate = (values: Record<string, string | null>) => {
    const next = reportQueryParams(search)
    if (urlState.has("sort")) next.set("sort", urlState.get("sort")!)
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
  const applyFilters = (query: ReportQuery) => {
    const next = new URLSearchParams(Object.entries(query).filter(([, value]) => Boolean(value)))
    if (urlState.has("sort")) next.set("sort", urlState.get("sort")!)
    onNavigate(`?${next}`)
  }
  const defaults: ReportQuery = { ...reportPreset(30, "UTC"), timezone: "UTC", grain: "day" }
  const filterQuery = Object.fromEntries([...params].filter(([key]) => key !== "snapshot"))
  const selected: ReportQuery = report?.query ?? { ...defaults, ...filterQuery }
  const sort = urlState.get("sort") === "runtime" ? "runtime" : "requests"
  return (
    <section className="min-w-0 space-y-4" aria-labelledby="reports-title">
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
      <ReportFilters key={JSON.stringify(selected)} query={selected} onApply={applyFilters} />
      <ResourceState resource={overview} />
      {report && (
        <>
          <div className="flex flex-wrap gap-x-4 gap-y-1 break-words text-xs text-muted-foreground">
            <p>
              {t("reports.window", {
                from: report.window.start,
                to: report.window.end,
                timezone: report.query.timezone,
              })}
            </p>
            <p>
              {t("reports.comparison", {
                from: report.window.previous_start,
                to: report.window.previous_end,
              })}
            </p>
            <p>{t("reports.observed", { time: formatTime(report.observed_at, i18n.resolvedLanguage) })}</p>
          </div>
          <div className="grid grid-cols-2 gap-2 sm:grid-cols-3 xl:grid-cols-6">
            {(
              [
                "requests",
                "started",
                "rejected",
                "active_accounts",
                "active_repositories",
                "runtime",
              ] as const
            ).map((metric) => {
              const value = metric === "runtime" ? report.totals.runtime.seconds : report.totals[metric]
              const previous =
                metric === "runtime" ? report.previous.runtime.seconds : report.previous[metric]
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
                <Card size="sm" key={metric}>
                  <CardContent>
                    <p className="text-sm text-muted-foreground">{t(`reports.${metric}`)}</p>
                    <p className="my-1 text-2xl font-semibold tabular-nums">
                      {!available ? "—" : metric === "runtime" ? hours(value) : value.toLocaleString()}
                    </p>
                    <p className="text-xs text-muted-foreground">
                      {delta} · {t("reports.vsPrevious")}
                    </p>
                    {metric === "runtime" && (
                      <div className="mt-1 flex items-center gap-1 text-xs text-muted-foreground">
                        <span>{t("reports.runtimeRecords", { count: report.totals.runtime.samples })}</span>
                        <Tooltip>
                          <TooltipTrigger asChild>
                            <Button
                              variant="ghost"
                              size="icon"
                              className="size-6"
                              aria-label={t("reports.runtimeDefinition")}
                            >
                              <Info className="size-3.5" />
                            </Button>
                          </TooltipTrigger>
                          <TooltipContent className="max-w-72">{t("reports.runtimeHelp")}</TooltipContent>
                        </Tooltip>
                      </div>
                    )}
                  </CardContent>
                </Card>
              )
            })}
          </div>
          <Trend report={report} metric={trendMetric} onMetricChange={setTrendMetric} />
          <div className="flex flex-wrap items-center justify-between gap-3">
            <div>
              <h2 className="font-semibold">{t("reports.distribution")}</h2>
              <p className="mt-1 text-xs text-muted-foreground">{t("reports.topGroups")}</p>
            </div>
            <ReportSelect
              inline
              label={t("reports.distributionMetric")}
              value={sort}
              onChange={(value) => navigate({ sort: value })}
              options={[
                ["requests", t("reports.requests")],
                ["runtime", t("reports.runtime")],
              ]}
            />
          </div>
          <div className="grid min-w-0 gap-3 xl:grid-cols-3">
            {(["spec", "owner", "repository"] as const).map((d) => (
              <Distribution
                key={`${d}-${revision}`}
                dimension={d}
                report={report}
                request={request}
                sort={sort}
                onFilter={(key) => applyFilters({ ...report.query, [d]: key })}
              />
            ))}
          </div>
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
      className="grid gap-3 rounded-lg border p-3 lg:grid-cols-[1fr_auto]"
      onSubmit={(e) => {
        e.preventDefault()
        onApply(draft)
      }}
    >
      <div className="flex flex-wrap items-center gap-2 lg:col-span-2">
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
      <div className="grid min-w-0 items-end gap-2 sm:grid-cols-2 lg:grid-cols-5">
        {(["from", "to", "timezone", "repository"] as const).map((key) => (
          <div className="flex min-w-0 flex-col gap-1.5" key={key}>
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
      <div className="flex flex-wrap self-end gap-2">
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
      </div>
      <p className="text-xs text-muted-foreground lg:col-span-2">{t("reports.filterHelp")}</p>
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
  inline = false,
}: {
  inline?: boolean
  label: string
  value: string
  options: string[][]
  onChange: (value: string) => void
}) {
  return (
    <div className={inline ? "flex min-w-0 flex-wrap items-center gap-2" : "flex min-w-0 flex-col gap-1.5"}>
      <span className="text-sm font-medium">{label}</span>
      <Select value={value} onValueChange={onChange}>
        <SelectTrigger className={inline ? "w-auto min-w-32" : "w-full min-w-32"} aria-label={label}>
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
      <div
        role="alert"
        className="break-words rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm"
      >
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
function Trend({
  report,
  metric,
  onMetricChange,
}: {
  report: RunnerReport
  metric: string
  onMetricChange: (value: string) => void
}) {
  const { t } = useTranslation()
  const combined = metric === "requests-runtime"
  const seriesMetrics = combined
    ? (["requests", "runtime"] as const)
    : ([metric === "started" ? "started" : metric === "runtime" ? "runtime" : "requests"] as const)
  return (
    <Card size="sm">
      <CardContent className="space-y-2">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <h2 className="font-semibold">{t("reports.trend")}</h2>
          <ReportSelect
            inline
            label={t("reports.metric")}
            value={metric}
            onChange={onMetricChange}
            options={[
              ["requests-runtime", t("reports.requestsAndRuntime")],
              ["requests", t("reports.requests")],
              ["started", t("reports.started")],
              ["runtime", t("reports.runtime")],
            ]}
          />
        </div>
        <div
          className="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground"
          aria-label={t("reports.trendLegend")}
        >
          {seriesMetrics.map((key) => (
            <span key={key} className="inline-flex items-center gap-1.5">
              <span
                className={
                  key === "runtime" ? "h-3 w-2 shrink-0 rounded-sm opacity-50" : "h-0.5 w-3 shrink-0"
                }
                style={{ background: key === "runtime" ? "var(--chart-1)" : "var(--chart-2)" }}
              />
              {t(`reports.${key}`)}
            </span>
          ))}
        </div>
        <Suspense
          fallback={
            <p role="status" className="grid h-48 place-items-center text-sm text-muted-foreground">
              {t("common.loading")}
            </p>
          }
        >
          <ReportChart
            label={t("reports.trend")}
            option={(colors) => ({
              tooltip: {
                trigger: "axis",
                renderMode: "richText",
                confine: true,
                backgroundColor: colors.background,
                borderColor: colors.border,
                textStyle: { color: colors.text },
                valueFormatter: (value) =>
                  value === null || value === undefined || value === "-"
                    ? "—"
                    : Number(value).toLocaleString(undefined, { maximumFractionDigits: 2 }),
              },
              grid: { left: 45, right: combined ? 45 : 15, top: 25, bottom: 30 },
              xAxis: {
                type: "category",
                data: report.trend.map((p) => p.date),
                axisLabel: { color: colors.text, hideOverlap: true },
                axisLine: { lineStyle: { color: colors.border } },
              },
              yAxis: seriesMetrics.map((key, index) => ({
                type: "value" as const,
                position: index === 1 ? ("right" as const) : ("left" as const),
                name: key === "runtime" ? t("reports.hoursAxis") : t("reports.requests"),
                min: 0,
                minInterval: key === "runtime" ? undefined : 1,
                nameTextStyle: { color: key === "runtime" ? colors.series[0] : colors.series[1] },
                axisLabel: { color: colors.text },
                splitLine: { show: index === 0, lineStyle: { color: colors.border } },
              })),
              series: seriesMetrics.map((key, index) => ({
                ...(key === "runtime"
                  ? {
                      type: "bar" as const,
                      barMaxWidth: 24,
                      z: 1,
                      itemStyle: { color: colors.series[0], opacity: 0.45 },
                    }
                  : {
                      type: "line" as const,
                      smooth: true,
                      symbolSize: 5,
                      z: 3,
                      itemStyle: { color: colors.series[1] },
                    }),
                name: t(`reports.${key}`),
                yAxisIndex: index,
                data: report.trend.map((bucket) =>
                  key === "runtime"
                    ? bucket.runtime.samples > 0
                      ? bucket.runtime.seconds / 3600
                      : null
                    : bucket[key],
                ),
                connectNulls: false,
              })),
            })}
          />
        </Suspense>
        <details>
          <summary className="cursor-pointer text-sm text-muted-foreground">
            {t("reports.trendTable")}
          </summary>
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
const chartColors = [1, 2, 3, 4, 5].map((n) => `var(--chart-${n})`)

function Distribution({
  dimension,
  report,
  request,
  sort,
  onFilter,
}: {
  dimension: ReportDimension
  report: RunnerReport
  request: Request
  sort: "requests" | "runtime"
  onFilter: (key: string) => void
}) {
  const { t } = useTranslation()
  const groups = useReportResource<ReportPage<ReportGroup>>(
    `/reports/${encodeURIComponent(report.snapshot)}/groups?${new URLSearchParams({ dimension, sort, offset: "0", limit: "5" })}`,
    request,
  )
  const total = sort === "runtime" ? report.totals.runtime.seconds / 3600 : report.totals.requests
  const points = groups.data
    ? reportDistribution(groups.data, report.totals, sort).map((row, index) => {
        const label = row.label.trim()
        return {
          ...row,
          label: row.other ? t("reports.otherGroups") : label || t("common.unknown"),
          color: row.other ? "var(--muted-foreground)" : chartColors[index],
        }
      })
    : []
  const metric = t(sort === "runtime" ? "reports.runtime" : "reports.requests")
  const format = (value: number | null) =>
    value === null ? "—" : value.toLocaleString(undefined, { maximumFractionDigits: 2 })
  const share = (value: number | null) =>
    value === null || total <= 0 ? "—" : `${((value / total) * 100).toFixed(1)}%`
  return (
    <Card size="sm" className="min-w-0">
      <CardContent className="space-y-2">
        <div>
          <h3 className="font-semibold">{t(`reports.${dimension}`)}</h3>
        </div>
        <ResourceState resource={groups} />
        {groups.data &&
          (points.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">{t("reports.empty")}</p>
          ) : (
            <>
              {total > 0 ? (
                <Suspense
                  fallback={
                    <p role="status" className="grid h-48 place-items-center text-sm text-muted-foreground">
                      {t("common.loading")}
                    </p>
                  }
                >
                  <ReportChart
                    label={t("reports.distributionChart", { dimension: t(`reports.${dimension}`), metric })}
                    option={(colors) => ({
                      tooltip: {
                        trigger: "item",
                        renderMode: "richText",
                        confine: true,
                        backgroundColor: colors.background,
                        borderColor: colors.border,
                        textStyle: { color: colors.text, width: 220, overflow: "breakAll" },
                        valueFormatter: (value) => format(typeof value === "number" ? value : null),
                      },
                      ...(dimension === "repository"
                        ? {
                            grid: { left: 130, right: 15, top: 10, bottom: 30 },
                            xAxis: {
                              type: "value" as const,
                              minInterval: sort === "requests" ? 1 : undefined,
                              axisLabel: { color: colors.text },
                              splitLine: { lineStyle: { color: colors.border } },
                            },
                            yAxis: {
                              type: "category" as const,
                              inverse: true,
                              data: points.map((row) => row.label),
                              axisLabel: { color: colors.text, width: 115, overflow: "truncate" as const },
                              axisLine: { show: false },
                              axisTick: { show: false },
                            },
                          }
                        : {}),
                      series:
                        dimension === "repository"
                          ? [
                              {
                                type: "bar",
                                name: metric,
                                barMaxWidth: 22,
                                data: points.map((row, index) => ({
                                  name: row.label,
                                  value: row.value ?? "-",
                                  itemStyle: { color: row.other ? colors.text : colors.series[index] },
                                })),
                              },
                            ]
                          : [
                              {
                                type: "pie",
                                name: metric,
                                radius: ["52%", "78%"],
                                label: { show: false },
                                data: points.flatMap((row, index) =>
                                  row.value !== null && row.value > 0
                                    ? [
                                        {
                                          name: row.label,
                                          value: row.value,
                                          itemStyle: {
                                            color: row.other ? colors.text : colors.series[index],
                                          },
                                        },
                                      ]
                                    : [],
                                ),
                              },
                            ],
                    })}
                  />
                </Suspense>
              ) : (
                <p className="py-8 text-center text-sm text-muted-foreground">
                  {t(report.totals.runtime.samples ? "reports.zeroRuntime" : "reports.noRuntime")}
                </p>
              )}
              <p className="text-xs text-muted-foreground">
                {t("reports.distributionTotal", {
                  metric,
                  value: sort === "runtime" && !report.totals.runtime.samples ? "—" : format(total),
                })}
              </p>
              <ul
                className="space-y-1"
                aria-label={t("reports.chartValues", { dimension: t(`reports.${dimension}`) })}
              >
                {points.map((row, index) => (
                  <li key={index} className="flex min-w-0 items-start gap-2 text-sm">
                    <span className="mt-1.5 size-2.5 shrink-0 rounded-sm" style={{ background: row.color }} />
                    <span className="min-w-0 flex-1 break-all">{row.label}</span>
                    <span className="shrink-0 text-right tabular-nums">
                      {format(row.value)}{" "}
                      <span className="text-xs text-muted-foreground">· {share(row.value)}</span>
                    </span>
                    {!row.other && (
                      <Button
                        variant="ghost"
                        size="icon"
                        className="size-6 shrink-0"
                        aria-label={t("reports.filterGroup", { name: row.label })}
                        onClick={() => onFilter(row.key)}
                        disabled={!row.key}
                      >
                        <Filter className="size-3.5" />
                      </Button>
                    )}
                  </li>
                ))}
              </ul>
            </>
          ))}
      </CardContent>
    </Card>
  )
}
