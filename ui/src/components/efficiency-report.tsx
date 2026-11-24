import { lazy, Suspense } from "react"
import { useTranslation } from "react-i18next"
import {
  reportTimeFormatter,
  type EfficiencyGroup,
  type EfficiencySummary,
  type ReportQuery,
  type RunnerReport,
} from "@/runner-reports"
import { useReportResource } from "@/use-report-resource"
import { ReportRequestLink, ReportRequestRows, type ReportActions } from "./report-request-rows"
import { ReportSelect, ResourceState } from "./report-controls"
import { Button } from "./ui/button"
import { Card, CardContent } from "./ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "./ui/table"

const ReportChart = lazy(() => import("./report-chart"))

export function EfficiencyReport({
  report,
  summary,
  ...actions
}: ReportActions & { report: RunnerReport; summary: EfficiencySummary }) {
  const { t, i18n } = useTranslation()
  const url = new URLSearchParams(actions.search)
  const dimension = url.get("evidence") || ""
  const key = url.get("evidence_key") || ""
  const order = url.get("anomaly_sort") === "rate" ? "rate" : "count"
  const details = report.request_detail_available === true
  const drill = (d: string, k: string) =>
    actions.navigate({ evidence: d, evidence_key: k, requests_offset: "0", selected_request: null })
  const reason = url.get("rejection_reason")
  const reasons = Object.keys(summary.rejections ?? {}).sort()
  const selectedReason = reason !== null && reasons.includes(reason) ? reason : null
  const trend = report.trend.map((bucket) => ({
    ...bucket,
    rejected:
      selectedReason === null ? bucket.rejected : (summary.rejections?.[selectedReason]?.[bucket.date] ?? 0),
  }))
  const drillBucket = (date: string) => {
    if (selectedReason === null) drill("admission_bucket", date)
    else drill("admission_bucket_reason", date + ":" + selectedReason)
  }
  const totals = report.totals
  const denominator = (g: EfficiencyGroup) => {
    if (g.dimension === "termination") return totals.terminal
    if (g.dimension.startsWith("started_")) return totals.started
    if (g.dimension.startsWith("before_start_")) return totals.requests - totals.started
    return totals.requests
  }
  const pct = (n: number, d: number) =>
    d ? (n / d).toLocaleString(i18n.resolvedLanguage, { style: "percent", maximumFractionDigits: 2 }) : "—"
  const seconds = (v: number | null) =>
    v === null ? "—" : v.toLocaleString(i18n.resolvedLanguage, { maximumFractionDigits: 2 })
  const groups = [...summary.groups].sort((a, b) => {
    const diff = order === "rate" ? b.count / denominator(b) - a.count / denominator(a) : 0
    return diff || b.count - a.count || a.dimension.localeCompare(b.dimension) || a.key.localeCompare(b.key)
  })
  const names: Record<string, string> = {
    started_stage: t("reports.startedStage"),
    started_reason: t("reports.startedReason"),
    before_start_stage: t("reports.beforeStage"),
    before_start_reason: t("reports.beforeReason"),
    admission_reason: t("reports.admissionReason"),
    termination: t("reports.terminalSource"),
    startup: t("reports.startupDuration"),
    creation: t("reports.creationDuration"),
    cleanup: t("reports.cleanupDuration"),
    admission_bucket_reason: t("reports.admissionReason"),
    admission_bucket: t("reports.rejected"),
  }
  return (
    <section className="min-w-0 space-y-4">
      <div className="space-y-1 text-xs text-muted-foreground">
        <p>{t("reports.efficiencyBasis")}</p>
        <p>
          {t("reports.observed", {
            time: reportTimeFormatter(i18n.resolvedLanguage, report.query.timezone)(report.observed_at),
          })}
        </p>
      </div>
      <Card size="sm">
        <CardContent>
          <Table aria-label={t("reports.timingSamples")}>
            <TableHeader>
              <TableRow>
                {(["metric", "meanSeconds", "p95Seconds", "sampleCoverage", "missingSamples"] as const).map(
                  (k) => (
                    <TableHead key={k}>{t(`reports.${k}`)}</TableHead>
                  ),
                )}
              </TableRow>
            </TableHeader>
            <TableBody>
              {(["startup", "creation", "cleanup"] as const).map((k) => {
                const m = totals[k]
                return (
                  <TableRow key={k}>
                    <TableCell>
                      {details && m.samples > 0 ? (
                        <Button variant="link" className="p-0" onClick={() => drill(k, "")}>
                          {names[k]}
                        </Button>
                      ) : (
                        names[k]
                      )}
                    </TableCell>
                    <TableCell>{seconds(m.samples ? m.seconds / m.samples : null)}</TableCell>
                    <TableCell>{seconds(summary[`${k}_p95`])}</TableCell>
                    <TableCell>
                      {m.samples}/{m.eligible} · {pct(m.samples, m.eligible)}
                    </TableCell>
                    <TableCell>{m.eligible - m.samples}</TableCell>
                  </TableRow>
                )
              })}
            </TableBody>
          </Table>
          <p className="mt-2 text-xs text-muted-foreground">{t("reports.timingBasis")}</p>
        </CardContent>
      </Card>
      <Card size="sm">
        <CardContent className="space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="font-semibold">{t("reports.rejectionTrend")}</h2>
            {reasons.length > 0 && (
              <ReportSelect
                inline
                label={t("reports.admissionReason")}
                value={selectedReason === null ? "all" : "reason:" + selectedReason}
                options={[
                  ["all", t("common.all")],
                  ...reasons.map((r) => ["reason:" + r, r || t("common.unknown")]),
                ]}
                onChange={(v) =>
                  actions.navigate({
                    rejection_reason: v === "all" ? null : v.slice("reason:".length),
                    evidence: null,
                    evidence_key: null,
                    requests_offset: null,
                    selected_request: null,
                  })
                }
              />
            )}
          </div>
          <Suspense fallback={<p role="status">{t("common.loading")}</p>}>
            <ReportChart
              label={t("reports.rejectionTrend")}
              onDataClick={details ? drillBucket : undefined}
              option={(colors) => ({
                grid: { left: 48, right: 16, top: 12, bottom: 34 },
                tooltip: { trigger: "axis", renderMode: "richText", confine: true },
                xAxis: { type: "category", data: trend.map((b) => b.date) },
                yAxis: { type: "value", minInterval: 1 },
                series: [
                  {
                    type: "bar",
                    name: t("reports.rejected"),
                    itemStyle: { color: colors.series[0] },
                    data: trend.map((b) => ({ id: b.date, value: b.rejected })),
                  },
                ],
              })}
            />
          </Suspense>
          <details>
            <summary className="cursor-pointer text-sm">{t("reports.trendTable")}</summary>
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("reports.grain")}</TableHead>
                  <TableHead>{t("reports.rejected")}</TableHead>
                  <TableHead>{t("reports.requests")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {trend.map((b) => (
                  <TableRow key={b.date}>
                    <TableCell>
                      {details && b.rejected > 0 ? (
                        <Button className="p-0" variant="link" onClick={() => drillBucket(b.date)}>
                          {b.date}
                        </Button>
                      ) : (
                        b.date
                      )}
                    </TableCell>
                    <TableCell>{b.rejected}</TableCell>
                    <TableCell>{b.requests}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </details>
        </CardContent>
      </Card>
      <Card size="sm">
        <CardContent className="space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <h2 className="font-semibold">{t("reports.anomalies")}</h2>
            <ReportSelect
              inline
              label={t("reports.rankBy")}
              value={order}
              options={[
                ["count", t("reports.requests")],
                ["rate", t("reports.rate")],
              ]}
              onChange={(v) => actions.navigate({ anomaly_sort: v })}
            />
          </div>
          <p className="text-xs text-muted-foreground">{t("reports.ratioBasis")}</p>
          {groups.length ? (
            <Table>
              <TableHeader>
                <TableRow>
                  {(["metric", "category", "sampleRatio", "rate"] as const).map((k) => (
                    <TableHead key={k}>{t(`reports.${k}`)}</TableHead>
                  ))}
                </TableRow>
              </TableHeader>
              <TableBody>
                {groups.map((g) => (
                  <TableRow key={JSON.stringify([g.dimension, g.key])}>
                    <TableCell>{names[g.dimension]}</TableCell>
                    <TableCell>
                      {details ? (
                        <Button variant="link" className="p-0" onClick={() => drill(g.dimension, g.key)}>
                          {g.key || t("common.unknown")}
                        </Button>
                      ) : (
                        g.key || t("common.unknown")
                      )}
                    </TableCell>
                    <TableCell>
                      {g.count}/{denominator(g)}
                    </TableCell>
                    <TableCell>{pct(g.count, denominator(g))}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          ) : (
            <p>{t("reports.empty")}</p>
          )}
        </CardContent>
      </Card>
      {dimension && (
        <p className="text-sm">
          {names[dimension]}{" "}
          {key ||
            (dimension.endsWith("_stage") || dimension.endsWith("_reason") || dimension === "termination"
              ? t("common.unknown")
              : "")}{" "}
          <Button
            size="sm"
            variant="ghost"
            onClick={() =>
              actions.navigate({
                evidence: null,
                evidence_key: null,
                requests_offset: "0",
                selected_request: null,
              })
            }
          >
            {t("common.all")}
          </Button>
        </p>
      )}
      <ReportRequestRows report={report} dimension={dimension} groupKey={key} {...actions} />
    </section>
  )
}

type BacklogPage = {
  items: { id: string; repository: string; status: string; since: string | null; seconds: number | null }[]
  observed_at: string
  more: boolean
}
export function CurrentBacklog({
  query,
  revision,
  ...actions
}: ReportActions & { query: ReportQuery; revision: number }) {
  const { t } = useTranslation()
  const url = new URLSearchParams(actions.search)
  const status = url.get("backlog_status") || "queued"
  const minutes = url.get("backlog_minutes") || "30"
  const params = new URLSearchParams({ status, minutes })
  for (const key of ["owner", "repository", "repository_missing", "spec"] as const)
    if (query[key]) params.set(key, query[key]!)
  const resource = useReportResource<BacklogPage>("/reports/backlog?" + params, actions.request, revision)
  return (
    <Card size="sm">
      <CardContent className="space-y-2">
        <h2 className="font-semibold">{t("reports.longBacklog")}</h2>
        <p className="text-xs text-muted-foreground">{t("reports.backlogBasis")}</p>
        <div className="flex flex-wrap gap-3">
          <ReportSelect
            inline
            label={t("common.status")}
            value={status}
            options={[
              ["queued", t("reports.backlogQueued")],
              ["creating", t("reports.backlogCreating")],
              ["stopping", t("reports.backlogStopping")],
            ]}
            onChange={(v) => actions.navigate({ backlog_status: v })}
          />
          <ReportSelect
            inline
            label={t("reports.ageMinutes")}
            value={minutes}
            options={["5", "15", "30", "60", "240", "1440"].map((v) => [v, v])}
            onChange={(v) => actions.navigate({ backlog_minutes: v })}
          />
        </div>
        <ResourceState resource={resource} />
        {resource.data && (
          <>
            <p className="text-xs text-muted-foreground">
              {t("reports.observed", { time: resource.data.observed_at })}
            </p>
            {resource.data.items.length ? (
              <Table>
                <TableHeader>
                  <TableRow>
                    {(["requestId", "repository", "stateSince", "ageMinutes"] as const).map((k) => (
                      <TableHead key={k}>{t(`reports.${k}`)}</TableHead>
                    ))}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {resource.data.items.map((item) => (
                    <TableRow
                      key={item.id}
                      data-state={url.get("selected_request") === item.id ? "selected" : undefined}
                    >
                      <TableCell>
                        <ReportRequestLink
                          id={item.id}
                          search={actions.search}
                          onOpenRequest={actions.onOpenRequest}
                        />
                      </TableCell>
                      <TableCell>{item.repository || t("common.unknown")}</TableCell>
                      <TableCell>{item.since ?? "—"}</TableCell>
                      <TableCell>{item.seconds === null ? "—" : Math.floor(item.seconds / 60)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            ) : (
              <p>{t("reports.empty")}</p>
            )}
            {resource.data.more && <p>{t("reports.backlogLimit")}</p>}
          </>
        )}
      </CardContent>
    </Card>
  )
}
