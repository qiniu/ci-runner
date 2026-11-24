import { lazy, Suspense, useEffect, useState, type ReactNode } from "react"
import { useTranslation } from "react-i18next"
import { RefreshCw, Filter, Info, ChevronDown } from "lucide-react"
import {
  reportChartPalette,
  reportDistribution,
  reportGroupLabel,
  reportPreset,
  reportQueryKeys,
  reportQueryParams,
  type ExecutionUsage,
  type ReportQuery,
  type ReportDimension,
  type ReportGroup,
  type ReportPage,
  type ReportSpecPage,
  type ReportSpecUsage,
  type RunnerReport,
} from "@/runner-reports"
import { SpecRepositoryCharts } from "@/components/spec-repository-charts"
import { formatTime } from "@/admin-format"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Label } from "@/components/ui/label"
import { Card, CardContent } from "@/components/ui/card"
import { ReportDisplayMode, ReportPager, ReportSelect, ResourceState } from "@/components/report-controls"
import { type ReportFetch as Request, type Resource, useReportResource } from "@/use-report-resource"
import { EfficiencyReport, CurrentBacklog } from "@/components/efficiency-report"
import { ExecutionUsageReport } from "@/components/execution-usage-report"
import { AccountUsageReport } from "@/components/account-usage-report"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip"

const ReportChart = lazy(() => import("@/components/report-chart"))

type ExecutionReport = Pick<RunnerReport, "query" | "snapshot" | "observed_at" | "expires_at"> & { execution: ExecutionUsage }
type ReportResponse = RunnerReport | ExecutionReport

function pinnedReportUrl({ url, data }: Resource<ReportResponse>) {
  if (!data) return
  const params = new URLSearchParams()
  for (const key of reportQueryKeys) {
    const value = data.query[key]
    if (value) params.set(key, value)
  }
  params.set("snapshot", data.snapshot)
  return `${url.split("?")[0]}?${params}`
}
function hours(seconds: number) {
  return (seconds / 3600).toLocaleString(undefined, { maximumFractionDigits: 2 })
}
export function RunnerReportsSection({
  search,
  request,
  onNavigate,
  onOpenRequest,
}: {
  search: string
  request: Request
  onNavigate: (search: string, replace?: boolean) => void
  onOpenRequest: (id: string, reportSearch: string) => void
}) {
  const { t, i18n } = useTranslation()
  const [revision, setRevision] = useState(0)
  const [trendMetric, setTrendMetric] = useState("requests-runtime")
  const params = reportQueryParams(search)
  const urlState = new URLSearchParams(search)
  const tab = urlState.get("view")
  const view = tab === "specs" || tab === "accounts" || tab === "efficiency" || tab === "execution" ? tab : "overview"
  const sharedUsage = view === "accounts" || view === "efficiency"
  // Older spec links used a cohort-wide filter and may carry a scoped snapshot.
  if (view === "specs" && params.has("spec")) {
    params.delete("spec")
    params.delete("snapshot")
  }
  const query = params.toString()
  const endpoint = { overview: "overview", specs: "spec-usage", accounts: "account-usage", efficiency: "efficiency", execution: "execution-usage" }[view]
  const overview = useReportResource<ReportResponse>(
    `/reports/${endpoint}?${query}`,
    request,
    revision,
    pinnedReportUrl,
  )
  const response = overview.data
  const report = response && "totals" in response ? response : undefined
  useEffect(() => {
    if (!sharedUsage || !report || new URLSearchParams(search).has("snapshot")) return
    const pinned = new URLSearchParams(search)
    for (const [key, value] of Object.entries(report.query)) if (value) pinned.set(key, value)
    pinned.set("snapshot", report.snapshot)
    onNavigate(`?${pinned}`, true)
  }, [sharedUsage, report, search, onNavigate])
  const navigate = (values: Record<string, string | null>) => {
    const next = new URLSearchParams(search)
    if (response) {
      for (const [key, value] of Object.entries(response.query)) if (value) next.set(key, value)
      next.set("snapshot", response.snapshot)
    }
    for (const [key, value] of Object.entries(values)) {
      if (value === null) next.delete(key)
      else next.set(key, value)
    }
    onNavigate(`?${next}`)
  }
  const actions = { request, search, navigate, onOpenRequest }
  const refresh = () => {
    navigate({
      snapshot: null,
      focus_snapshot: null,
      requests_offset: null,
      spec_offset: null,
      group_offset: null,
      selected_request: null,
    })
    setRevision((v) => v + 1)
  }
  const defaults: ReportQuery = { ...reportPreset(30, "UTC"), timezone: "UTC", grain: "day" }
  const filterQuery = Object.fromEntries([...params].filter(([key]) => key !== "snapshot"))
  const selected: ReportQuery = response?.query ?? { ...defaults, ...filterQuery }
  const filterKey = JSON.stringify(reportQueryKeys.map((key) => selected[key] || ""))
  const [edits, setEdits] = useState<{ key: string; query: ReportQuery } | null>(null)
  if (edits && edits.key !== filterKey) setEdits(null)
  const draft = edits?.key === filterKey ? edits.query : selected
  const applyFilters = (query: ReportQuery, ranking?: string) => {
    setEdits(null)
    const next = new URLSearchParams(Object.entries(query).filter(([, value]) => Boolean(value)))
    for (const key of [
      "sort",
      "view",
      "spec_mode",
      "spec_sort",
      "spec_q",
      "focus_spec",
      "focus_label",
      "account_mode",
      "anomaly_sort",
      "backlog_status",
      "backlog_minutes",
      "ranking",
    ]) {
      if (urlState.has(key)) next.set(key, urlState.get(key)!)
    }
    if (ranking) next.set("ranking", ranking)
    if (view === "specs" && !next.has("focus_spec") && urlState.get("spec")) {
      next.set("focus_spec", urlState.get("spec")!)
    }
    onNavigate(`?${next}`)
  }
  const selectGroup = (dimension: ReportDimension, key: string | null) => {
    const next = { ...draft, [dimension]: key ?? "" }
    if (dimension === "repository") next.repository_missing = key === "" ? "1" : ""
    applyFilters(next, dimension === "owner" && key !== null ? "repository" : undefined)
  }
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
      <ReportFilters
        key={filterKey}
        draft={draft}
        onDraftChange={(query) => setEdits({ key: filterKey, query })}
        onApply={applyFilters}
        identityFilters={
          sharedUsage &&
          report &&
          (["owner", "repository", "spec"] as const).map((dimension) => {
            const focus =
              report.query[dimension] ??
              (dimension === "repository" && report.query.repository_missing ? "" : null)
            const group = report.distributions?.[dimension].requests.find((item) => item.key === focus)
            return (
              <IdentityFilter
                key={dimension}
                dimension={dimension}
                report={report}
                request={request}
                focus={focus}
                label={focus === null ? "" : reportGroupLabel(t, dimension, focus, group?.label || focus)}
                onSelect={(key) => selectGroup(dimension, key)}
              />
            )
          })
        }
      />
      <Tabs
        className="min-w-0 gap-4"
        value={view}
        onValueChange={(next) => {
          if (next === view) return
          navigate({
            view: next === "overview" ? null : next,
            snapshot: null,
            requests_offset: urlState.has("requests_offset") && (next === "accounts" || next === "efficiency") ? "0" : null,
            selected_request: null,
          })
        }}
      >
        <TabsList variant="line" className="h-auto max-w-full flex-wrap justify-start" aria-label={t("reports.views")}>
          {(["overview", "specs", "accounts", "efficiency", "execution"] as const).map((tab) => (
            <TabsTrigger key={tab} value={tab} variant="line">
              {t(`reports.${tab}`)}
            </TabsTrigger>
          ))}
        </TabsList>
        <ResourceState resource={overview} />
        <TabsContent value="execution" className="min-w-0">
          {view === "execution" && response && "execution" in response && (
            <ExecutionUsageReport usage={response.execution} observedAt={response.observed_at} timezone={response.query.timezone} />
          )}
        </TabsContent>
        <TabsContent value="efficiency" className="min-w-0 space-y-4">
          {view === "efficiency" && (
            <>
              {report?.efficiency && <EfficiencyReport report={report} summary={report.efficiency} {...actions} />}
              <CurrentBacklog query={selected} revision={revision} {...actions} />
            </>
          )}
        </TabsContent>
        <TabsContent value="accounts" className="min-w-0">
          {view === "accounts" && report && (
            <AccountUsageReport
              report={report}
              search={search}
              request={request}
              navigate={navigate}
              onOpenRequest={onOpenRequest}
              onSelect={selectGroup}
            >
              <div className="grid min-w-0 gap-3 xl:grid-cols-3">
                {(["owner", "repository", "spec"] as const).map((d) => (
                  <Distribution
                    key={d}
                    dimension={d}
                    report={report}
                    request={request}
                    sort={sort}
                    onSelect={(key) => selectGroup(d, key)}
                  />
                ))}
              </div>
              <Trend report={report} metric={trendMetric} onMetricChange={setTrendMetric} />
            </AccountUsageReport>
          )}
        </TabsContent>
        <TabsContent value="specs" className="min-w-0">
          {report && view === "specs" && (
            <SpecUsageReport
              key={revision}
              report={report}
              search={search}
              request={request}
              navigate={navigate}
            />
          )}
        </TabsContent>
        <TabsContent value="overview" className="min-w-0 space-y-4">
          {report && view === "overview" && (
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
                <p>
                  {t("reports.observed", { time: formatTime(report.observed_at, i18n.resolvedLanguage) })}
                </p>
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
                            <span>
                              {t("reports.runtimeRecords", { count: report.totals.runtime.samples })}
                            </span>
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
                  <p className="mt-1 text-xs text-muted-foreground">
                    {t("reports.topGroups")} {t("reports.filterHelp")}
                  </p>
                </div>
                <ReportSelect
                  inline
                  label={t("reports.distributionMetric")}
                  value={sort}
                  onChange={(value) => {
                    if (report.distributions) {
                      const next = new URLSearchParams(search)
                      next.set("sort", value)
                      onNavigate(`?${next}`)
                    } else {
                      navigate({ sort: value })
                    }
                  }}
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
        </TabsContent>
      </Tabs>
    </section>
  )
}

function formatReportSeconds(value?: number) {
  return value === undefined ? "—" : `${value.toLocaleString(undefined, { maximumFractionDigits: 1 })} s`
}

function SpecUsageReport({
  report,
  search,
  request,
  navigate,
}: {
  report: RunnerReport
  search: string
  request: Request
  navigate: SpecURLUpdate
}) {
  const { t } = useTranslation()
  const url = new URLSearchParams(search)
  const table = url.get("spec_mode") === "table"
  const focus = url.get("focus_spec") || url.get("spec") || ""
  const update: SpecURLUpdate = (values) =>
    navigate({ spec: null, requests_offset: null, focus_snapshot: null, focus_spec: focus || null, ...values })
  const dashboard = report.spec_dashboard
  const embedded = dashboard?.items.find((item) => item.key === focus)
  const lookup = useReportResource<ReportSpecPage>(
    focus && !embedded
      ? `/reports/${encodeURIComponent(report.snapshot)}/specs?key=${encodeURIComponent(focus)}&limit=1`
      : "",
    request,
  )
  const item = focus ? (embedded ?? lookup.data?.items[0]) : dashboard?.summary
  const select = (key: string, label: string) =>
    update({ focus_spec: key || null, focus_label: label || null, spec_offset: null })
  return (
    <section className="min-w-0 space-y-4" aria-labelledby="spec-usage-title">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <h2 id="spec-usage-title" className="font-semibold">
            {t("reports.specUsageTitle")}
          </h2>
          <p className="mt-1 text-xs text-muted-foreground">
            {t(table ? "reports.specUsageDescription" : "reports.specChartHelp")}
          </p>
        </div>
        <ReportDisplayMode table={table} onChange={(table) => update({ spec_mode: table ? "table" : null })} />
      </div>
      {dashboard && (
        <IdentityFilter
          dimension="spec"
          report={report}
          request={request}
          focus={focus || null}
          label={item?.label || url.get("focus_label")}
          onSelect={(key, label) => select(key || "", label)}
        />
      )}
      {focus && !embedded && <ResourceState resource={lookup} />}
      {focus && lookup.data && !item && <p className="text-muted-foreground">{t("reports.noSpecs")}</p>}
      {table && !focus && (
        <SpecTable
          report={report}
          url={url}
          request={request}
          focus={focus}
          onSelect={select}
          setState={update}
        />
      )}
      {table && focus && item && (
        <SpecRows
          items={[item]}
          totalRuntime={report.totals.runtime.seconds}
          focus={focus}
          onSelect={select}
        />
      )}
      {!table && !focus && (
        <div className="@container/spec-shares grid min-w-0 grid-cols-1 gap-3">
          {(["requests", "runtime"] as const).map((metric) => (
            <Distribution
              key={metric}
              dimension="spec"
              report={report}
              request={request}
              sort={metric}
              title={t(metric === "requests" ? "reports.specRequestShare" : "reports.specRuntimeShare")}
              onSelect={select}
            />
          ))}
        </div>
      )}
      {!table && item && (
        <SpecRepositoryCharts item={item} totals={focus ? report.totals : undefined}>
          <SpecTrend report={report} item={focus ? item : undefined} />
        </SpecRepositoryCharts>
      )}
    </section>
  )
}

type SpecURLUpdate = (values: Record<string, string | null>) => void

function SpecTable({
  report,
  url,
  request,
  focus,
  onSelect,
  setState,
}: {
  report: RunnerReport
  url: URLSearchParams
  request: Request
  focus: string
  onSelect: (key: string, label: string) => void
  setState: SpecURLUpdate
}) {
  const { t } = useTranslation()
  const sort = ["requests", "runtime", "average_runtime", "repositories"].includes(url.get("spec_sort") || "")
    ? url.get("spec_sort")!
    : "requests"
  const offset = Math.max(0, Number(url.get("spec_offset") || 0) || 0)
  const q = url.get("spec_q") || ""
  const params = new URLSearchParams({ sort, offset: String(offset), limit: "20" })
  if (q) params.set("q", q)
  const initial = !q && offset === 0 && sort === "requests" ? report.spec_dashboard : undefined
  const resource = useReportResource<ReportSpecPage>(
    initial ? "" : `/reports/${encodeURIComponent(report.snapshot)}/specs?${params}`,
    request,
  )
  const data = initial ?? resource.data
  return (
    <div className="space-y-3">
      <div className="flex flex-wrap items-end gap-2">
        <div className="flex flex-col gap-1.5">
          <Label htmlFor="report-spec-search">{t("reports.specSearch")}</Label>
          <Input
            id="report-spec-search"
            value={q}
            onChange={(event) => setState({ spec_q: event.target.value, spec_offset: "0" })}
            placeholder={t("reports.specSearchPlaceholder")}
          />
        </div>
        <ReportSelect
          label={t("reports.specSort")}
          value={sort}
          onChange={(value) => setState({ spec_sort: value, spec_offset: "0" })}
          options={[
            ["requests", t("reports.requests")],
            ["runtime", t("reports.runtime")],
            ["average_runtime", t("reports.averageRuntime")],
            ["repositories", t("reports.repositories")],
          ]}
        />
      </div>
      {!initial && <ResourceState resource={resource} />}
      {data && (
        <>
          {data.items.length ? (
            <SpecRows
              items={data.items}
              totalRuntime={report.totals.runtime.seconds}
              focus={focus}
              onSelect={onSelect}
            />
          ) : (
            <p className="py-8 text-center text-muted-foreground">{t("reports.noSpecs")}</p>
          )}
          <ReportPager
            offset={offset}
            total={data.total}
            label={t("reports.specCount", { count: data.total })}
            onChange={(value) => setState({ spec_offset: String(value) })}
          />
        </>
      )}
    </div>
  )
}

function SpecRows({
  items,
  totalRuntime,
  focus,
  onSelect,
}: {
  items: ReportSpecUsage[]
  totalRuntime: number
  focus: string
  onSelect: (key: string, label: string) => void
}) {
  const { t, i18n } = useTranslation()
  const percent = new Intl.NumberFormat(i18n.resolvedLanguage, { style: "percent", maximumFractionDigits: 1 })
  return (
    <div className="overflow-x-auto rounded-lg border">
      <Table>
        <TableHeader>
          <TableRow>
            <TableHead>{t("reports.spec")}</TableHead>
            <TableHead>{t("reports.specStatus")}</TableHead>
            <TableHead>{t("reports.requests")}</TableHead>
            <TableHead>{t("reports.started")}</TableHead>
            <TableHead>{t("reports.runtime")}</TableHead>
            <TableHead>{t("reports.runtimeShare")}</TableHead>
            <TableHead>{t("reports.validSamples")}</TableHead>
            <TableHead>{t("reports.averageRuntime")}</TableHead>
            <TableHead>{t("reports.p95Runtime")}</TableHead>
            <TableHead>{t("reports.creationTime")}</TableHead>
            <TableHead>{t("reports.repositories")}</TableHead>
            <TableHead>{t("reports.jobResults")}</TableHead>
          </TableRow>
        </TableHeader>
        <TableBody>
          {items.map((item) => (
            <TableRow key={item.key} data-state={focus === item.key ? "selected" : undefined}>
              <TableCell className="min-w-48">
                <button
                  type="button"
                  className="text-left font-medium text-primary underline-offset-4 hover:underline"
                  aria-pressed={focus === item.key}
                  onClick={() => onSelect(item.key, item.label)}
                >
                  {item.label || t("common.unknown")}
                </button>
                <div className="text-xs text-muted-foreground">
                  {item.current ? t("reports.currentCatalog") : t("reports.historicalSpec")}
                </div>
              </TableCell>
              <TableCell>
                {item.current ? (item.enabled ? t("reports.enabled") : t("reports.disabled")) : "—"}
              </TableCell>
              <TableCell className="tabular-nums">{item.requests.toLocaleString()}</TableCell>
              <TableCell className="tabular-nums">{item.started.toLocaleString()}</TableCell>
              <TableCell className="tabular-nums">
                {item.runtime.samples ? hours(item.runtime.seconds) : "—"}
              </TableCell>
              <TableCell className="tabular-nums">
                {item.runtime.samples && totalRuntime > 0
                  ? percent.format(item.runtime.seconds / totalRuntime)
                  : "—"}
              </TableCell>
              <TableCell className="tabular-nums">
                {item.runtime.samples} / {item.runtime.eligible}
              </TableCell>
              <TableCell className="tabular-nums">
                {formatReportSeconds(item.average_runtime_seconds)}
              </TableCell>
              <TableCell className="tabular-nums">{formatReportSeconds(item.p95_runtime_seconds)}</TableCell>
              <TableCell className="tabular-nums">
                {formatReportSeconds(item.average_creation_seconds)}
              </TableCell>
              <TableCell className="tabular-nums">{item.repositories.toLocaleString()}</TableCell>
              <TableCell className="max-w-48 text-xs">
                {Object.entries(item.job_results || {})
                  .map(([key, value]) => `${key}: ${value}`)
                  .join(" · ") || "—"}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>
    </div>
  )
}

function IdentityFilter({
  dimension,
  report,
  request,
  focus,
  label,
  onSelect,
}: {
  dimension: ReportDimension
  report: RunnerReport
  request: Request
  focus: string | null
  label?: string | null
  onSelect: (key: string | null, label: string) => void
}) {
  const { t } = useTranslation()
  const [open, setOpen] = useState(false)
  const [search, setSearch] = useState("")
  const [offset, setOffset] = useState(0)
  const spec = dimension === "spec"
  const ranking = report.distributions?.[dimension]
  const catalog = spec && Boolean(report.spec_dashboard)
  const firstPage = catalog
    ? report.spec_dashboard
    : ranking && ranking.total <= ranking.requests.length
      ? { items: ranking.requests, total: ranking.total }
      : undefined
  const initial = !search && !offset && firstPage
  const total = catalog ? report.spec_dashboard?.total : ranking?.total
  const fieldLabel = spec ? t("reports.specFilter") : t(`reports.${dimension}`)
  const allLabel = spec ? t("reports.allSpecs") : t("common.all")
  const searchLabel = spec ? t("reports.specSearch") : t("reports.findGroup")
  const params = new URLSearchParams({ q: search, offset: String(offset), limit: "20" })
  if (!catalog) params.set("dimension", dimension)
  const resource = useReportResource<ReportPage<{ key: string; label: string }>>(
    open && !initial
      ? `/reports/${encodeURIComponent(report.snapshot)}/${catalog ? "specs" : "groups"}?${params}`
      : "",
    request,
  )
  const page = initial || resource.data
  const choose = (key: string | null, name: string) => {
    setOpen(false)
    onSelect(key, name)
  }
  return (
    <div className={catalog ? "flex min-w-0 items-center gap-2" : "flex min-w-0 flex-col gap-1.5"}>
      <span className="shrink-0 text-sm font-medium">{fieldLabel}</span>
      <Popover
        open={open}
        onOpenChange={(value) => {
          setOpen(value)
          setSearch("")
          setOffset(0)
        }}
      >
        <PopoverTrigger asChild>
          <Button type="button" variant="outline" size="sm" className="min-w-0" aria-label={fieldLabel}>
            <span className="truncate">{focus !== null ? label || t("common.unknown") : allLabel}</span>
            <ChevronDown />
          </Button>
        </PopoverTrigger>
        <PopoverContent align="start" aria-label={fieldLabel} className="space-y-2 p-2">
          {(total ?? 0) > 20 && (
            <Input
              aria-label={searchLabel}
              placeholder={spec ? t("reports.specSearchPlaceholder") : searchLabel}
              value={search}
              onChange={(e) => {
                setSearch(e.target.value)
                setOffset(0)
              }}
            />
          )}
          <div className="max-h-64 space-y-1 overflow-y-auto">
            <Button
              className="w-full justify-start"
              size="sm"
              variant={focus !== null ? "ghost" : "secondary"}
              aria-pressed={focus === null}
              onClick={() => choose(null, "")}
            >
              {allLabel}
            </Button>
            {page?.items.map((option) => (
              <Button
                key={option.key}
                className="w-full justify-start"
                size="sm"
                variant={focus === option.key ? "secondary" : "ghost"}
                aria-pressed={focus === option.key}
                onClick={() => choose(option.key, option.label)}
              >
                <span className="truncate">{reportGroupLabel(t, dimension, option.key, option.label)}</span>
              </Button>
            ))}
          </div>
          {!initial && <ResourceState resource={resource} />}
          {page?.items.length === 0 && (
            <p className="text-sm text-muted-foreground">
              {spec ? t("reports.noSpecs") : t("reports.empty")}
            </p>
          )}
          {page && page.total > 20 && (
            <ReportPager
              offset={offset}
              total={page.total}
              label={
                spec
                  ? t("reports.specCount", { count: page.total })
                  : t("reports.rankingPage", {
                      first: offset + 1,
                      last: offset + page.items.length,
                      total: page.total,
                    })
              }
              onChange={setOffset}
            />
          )}
        </PopoverContent>
      </Popover>
    </div>
  )
}

function ReportFilters({
  identityFilters,
  draft,
  onDraftChange,
  onApply,
}: {
  identityFilters?: ReactNode
  draft: ReportQuery
  onDraftChange: (query: ReportQuery) => void
  onApply: (query: ReportQuery) => void
}) {
  const { t } = useTranslation()
  const [error, setError] = useState("")
  const clearDimensions = (draft.owner || draft.spec || draft.repository || draft.repository_missing) && (
    <Button
      type="button"
      variant="ghost"
      onClick={() => {
        const next = { ...draft, owner: "", spec: "", repository: "", repository_missing: "" }
        onApply(next)
      }}
    >
      {t("reports.clearDimensions")}
    </Button>
  )
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
        {identityFilters && clearDimensions}
      </div>
      <div
        className={`grid min-w-0 items-end gap-2 ${
          identityFilters ? "sm:grid-cols-[repeat(auto-fit,minmax(9rem,1fr))]" : "sm:grid-cols-2 lg:grid-cols-5"
        }`}
      >
        {(["from", "to", "timezone", "repository"] as const)
          .filter((key) => key !== "repository" || !identityFilters)
          .map((key) => (
            <div className="flex min-w-0 flex-col gap-1.5" key={key}>
              <Label htmlFor={`report-${key}`}>{t(`reports.${key}`)}</Label>
              <Input
                id={`report-${key}`}
                type={key === "from" || key === "to" ? "date" : "text"}
                value={draft[key] || ""}
                onChange={(e) =>
                  onDraftChange({
                    ...draft,
                    [key]: e.target.value,
                    ...(key === "repository" && { repository_missing: "" }),
                  })
                }
              />
            </div>
          ))}
        {identityFilters}
        <ReportSelect
          label={t("reports.grain")}
          value={draft.grain}
          onChange={(grain) => onDraftChange({ ...draft, grain })}
          options={(["day", "week", "month"] as const).map((g) => [g, t(`reports.${g}`)])}
        />
      </div>
      <div className="flex flex-wrap self-end gap-2">
        <Button type="submit">{t("reports.apply")}</Button>
        {!identityFilters && clearDimensions}
      </div>
      {(draft.owner || draft.spec || draft.repository_missing) && (
        <p className="text-sm text-muted-foreground">
          {t("reports.filtered", {
            dimensions: [
              draft.owner ? reportGroupLabel(t, "owner", draft.owner, draft.owner) : "",
              draft.spec ? t("reports.spec") : "",
              draft.repository_missing ? t("reports.unknownRepository") : "",
            ]
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
function SpecTrend({ report, item }: { report: RunnerReport; item?: ReportSpecUsage }) {
  const [metric, setMetric] = useState("requests-runtime")
  if (!item) return <Trend report={report} metric={metric} onMetricChange={setMetric} />
  const observed = new Map(item.spec_trend?.map((bucket) => [bucket.key, bucket]))
  const trend = report.trend.map(({ date }) => {
    const b = observed.get(date)
    return {
      date,
      requests: b?.requests ?? 0,
      started: b?.started ?? 0,
      runtime: b?.runtime ?? { samples: 0, eligible: 0, seconds: 0 },
    }
  })
  return <Trend report={{ trend }} metric={metric} onMetricChange={setMetric} />
}

function Trend({
  report,
  metric,
  onMetricChange,
}: {
  report: { trend: Pick<RunnerReport["trend"][number], "date" | "requests" | "started" | "runtime">[] }
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
function Distribution({
  dimension,
  report,
  request,
  sort,
  onFilter,
  title,
  onSelect,
}: {
  dimension: ReportDimension
  report: RunnerReport
  request: Request
  sort: "requests" | "runtime"
  title?: string
  onSelect?: (key: string, label: string) => void
  onFilter?: (key: string) => void
}) {
  const { t } = useTranslation()
  const distribution = report.distributions?.[dimension]
  // Older servers return the snapshot ID without embedded rankings during a rollout.
  const fallback = useReportResource<ReportPage<ReportGroup>>(
    distribution
      ? ""
      : `/reports/${encodeURIComponent(report.snapshot)}/groups?${new URLSearchParams({ dimension, sort, offset: "0", limit: "5" })}`,
    request,
  )
  const total = sort === "runtime" ? report.totals.runtime.seconds / 3600 : report.totals.requests
  const groups = distribution ? { items: distribution[sort], total: distribution.total } : fallback.data
  const specKeys =
    distribution && onSelect
      ? [...new Set([...distribution.requests, ...distribution.runtime].map((group) => group.key))]
      : null
  const points = groups
    ? reportDistribution(groups, report.totals, sort).map((row, index) => {
        const label = reportGroupLabel(t, dimension, row.key, row.label)
        const colorIndex = specKeys ? specKeys.indexOf(row.key) : index
        return {
          ...row,
          label: row.other ? t("reports.otherGroups") : label,
          colorIndex,
          color: row.other ? "var(--muted-foreground)" : reportChartPalette[colorIndex],
        }
      })
    : []
  const bars = dimension === "repository" || (dimension === "spec" && onSelect && sort === "requests")
  const specRuntime = dimension === "spec" && sort === "runtime" && Boolean(onSelect)
  const metric = t(sort === "runtime" ? "reports.runtime" : "reports.requests")
  const format = (value: number | null) =>
    value === null ? "—" : value.toLocaleString(undefined, { maximumFractionDigits: 2 })
  const share = (value: number | null) =>
    value === null || total <= 0 ? "—" : `${((value / total) * 100).toFixed(1)}%`
  return (
    <Card size="sm" className="min-w-0">
      <CardContent className="space-y-2">
        <div>
          <h3 className="font-semibold">{title || t(`reports.${dimension}`)}</h3>
        </div>
        {!distribution && <ResourceState resource={fallback} />}
        {groups &&
          (points.length === 0 ? (
            <p className="py-8 text-center text-sm text-muted-foreground">{t("reports.empty")}</p>
          ) : (
            <div className="grid min-w-0 gap-2 @3xl/spec-shares:grid-cols-[minmax(0,3fr)_minmax(0,2fr)] @3xl/spec-shares:items-center @3xl/spec-shares:gap-6">
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
                    large={Boolean(onSelect)}
                    onDataClick={
                      onSelect
                        ? (key) => {
                            const point = points.find((row) => row.key === key && !row.other)
                            if (point) onSelect(point.key, point.label)
                          }
                        : undefined
                    }
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
                      ...(specRuntime && {
                        title: {
                          left: "center",
                          top: "center",
                          padding: 0,
                          itemGap: 4,
                          text: t("reports.hoursValue", { value: format(total) }),
                          subtext: t("reports.totalRuntime"),
                          textStyle: { color: colors.text, fontSize: 18 },
                          subtextStyle: { color: colors.text, fontSize: 12 },
                        },
                        media: [
                          {
                            query: { maxWidth: 480 },
                            option: { series: [{ label: { formatter: ({ value }: { value: number }) => share(value) } }] },
                          },
                          {
                            option: {
                              series: [{
                                label: {
                                  formatter: ({ name, value }: { name: string; value: number }) =>
                                    `${name}\n${t("reports.hoursValue", { value: format(value) })} · ${share(value)}`,
                                },
                              }],
                            },
                          },
                        ],
                      }),
                      ...(bars
                        ? {
                            grid: { left: 4, right: 15, top: 10, bottom: 30 },
                            media: [
                              { query: { maxWidth: 359 }, option: { yAxis: { axisLabel: { width: 120 } } } },
                              { query: { minWidth: 360, maxWidth: 599 }, option: { yAxis: { axisLabel: { width: 168 } } } },
                              { option: { yAxis: { axisLabel: { width: 260 } } } },
                            ],
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
                              axisLabel: {
                                color: colors.text,
                                interval: 0,
                                overflow: "breakAll" as const,
                                lineHeight: 12,
                                height: 24,
                                lineOverflow: "truncate" as const,
                              },
                              axisLine: { show: false },
                              axisTick: { show: false },
                            },
                          }
                        : {}),
                      series: bars
                        ? [
                            {
                              type: "bar",
                              name: metric,
                              barMaxWidth: 22,
                              data: points.map((row) => ({
                                id: row.other ? undefined : row.key,
                                name: row.label,
                                value: row.value ?? "-",
                                itemStyle: { color: row.other ? colors.text : colors.series[row.colorIndex] },
                              })),
                            },
                          ]
                        : [
                            {
                              type: "pie",
                              name: metric,
                              radius: specRuntime ? ["43%", "67%"] : ["52%", "78%"],
                              label: {
                                show: specRuntime,
                                color: colors.text,
                                alignTo: "edge",
                                edgeDistance: 4,
                                lineHeight: 16,
                                overflow: "truncate",
                              },
                              labelLine: { length: 8, length2: 6 },
                              data: points.flatMap((row) =>
                                row.value !== null && row.value > 0
                                  ? [
                                      {
                                        id: row.other ? undefined : row.key,
                                        name: row.label,
                                        value: row.value,
                                        itemStyle: {
                                          color: row.other ? colors.text : colors.series[row.colorIndex],
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
              <div className="min-w-0 space-y-2">
                <p className="text-xs text-muted-foreground">
                  {t("reports.distributionTotal", {
                    metric,
                    value: sort === "runtime" && !report.totals.runtime.samples ? "—" : format(total),
                  })}
                </p>
                <ul
                  className="space-y-1 @3xl/spec-shares:space-y-2"
                  aria-label={t("reports.chartValues", { dimension: t(`reports.${dimension}`) })}
                >
                  {points.map((row, index) => (
                    <li key={index} className="flex min-w-0 items-start gap-2 text-sm">
                      <span className="mt-1.5 size-2.5 shrink-0 rounded-sm" style={{ background: row.color }} />
                      {onSelect && !row.other ? (
                        <button
                          type="button"
                          className="min-w-0 flex-1 break-all text-left text-primary hover:underline aria-pressed:font-semibold aria-pressed:underline"
                          onClick={() => onSelect(row.key, row.label)}
                        >
                          {row.label}
                        </button>
                      ) : (
                        <span className="min-w-0 flex-1 break-all">{row.label}</span>
                      )}
                      <span className="shrink-0 text-right tabular-nums">
                        {format(row.value)}{" "}
                        <span className="text-xs text-muted-foreground">· {share(row.value)}</span>
                      </span>
                      {!row.other && onFilter && (
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
              </div>
            </div>
          ))}
      </CardContent>
    </Card>
  )
}
