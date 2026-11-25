import { useMemo, type ReactNode } from "react"
import { useTranslation } from "react-i18next"
import { ReportRequestRows, type ReportActions } from "./report-request-rows"
import {
  reportGroupLabel,
  type ActivityGroup,
  type ReportDimension,
  type ReportPage,
  reportTimeFormatter,
  outcomeCategory,
  type RunnerReport,
} from "@/runner-reports"
import { useReportResource } from "@/use-report-resource"
import { ReportDisplayMode, ReportPager, ReportSelect, ResourceState } from "@/components/report-controls"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

type AccountUsageProps = ReportActions & {
  report: RunnerReport
  onSelect: (dimension: ReportDimension, key: string, label: string) => void
  children: ReactNode
}

function percent(value: number, total: number, locale?: string) {
  return total ? (value / total).toLocaleString(locale, { style: "percent", maximumFractionDigits: 1 }) : "—"
}

function OutcomeCounts({ outcomes }: { outcomes?: Record<string, number> }) {
  const { t } = useTranslation()
  return (
    <span>
      {Object.entries(outcomes ?? {})
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([key, count]) => t(`reports.outcome_${outcomeCategory(key)}`) + " " + count.toLocaleString())
        .join(" · ") || "—"}
    </span>
  )
}

export function AccountUsageReport({
  report,
  request,
  search,
  navigate,
  onSelect,
  onOpenRequest,
  children,
}: AccountUsageProps) {
  const { t, i18n } = useTranslation()
  const url = new URLSearchParams(search)
  const table = url.get("account_mode") === "table"
  const dimension = url.get("ranking") === "repository" ? "repository" : "owner"
  const sort = url.get("sort") === "runtime" ? "runtime" : "requests"
  const offset = Number(url.get("group_offset") || 0)
  const base = `/reports/${encodeURIComponent(report.snapshot)}`
  const groups = useReportResource<ReportPage<ActivityGroup>>(
    table
      ? base + "/groups?" + new URLSearchParams({ dimension, sort, offset: String(offset), limit: "20" })
      : "",
    request,
  )
  const locale = i18n.resolvedLanguage
  const time = useMemo(() => reportTimeFormatter(locale, report.query.timezone), [locale, report.query.timezone])
  const pageLabel = (page: { total: number; offset: number; items: unknown[] }) =>
    t("reports.rankingPage", {
      first: page.items.length ? page.offset + 1 : 0,
      last: page.offset + page.items.length,
      total: page.total,
    })
  const coverage = (n: number, total: number) => `${n}/${total} (${percent(n, total, locale)})`
  const totals = report.totals
  return (
    <section className="min-w-0 space-y-4">
      <div className="grid items-start justify-items-start gap-3 sm:grid-cols-[minmax(0,1fr)_auto]">
        <div className="w-full min-w-0 space-y-1">
          <p className="text-xs text-muted-foreground">{t("reports.activityIdentity")}</p>
          <p className="text-xs text-muted-foreground">
            {t("reports.observed", { time: time(report.observed_at) })}
          </p>
        </div>
        <ReportDisplayMode
          table={table}
          onChange={(value) => navigate({ account_mode: value ? "table" : null })}
        />
      </div>
      <Card size="sm">
        <CardContent className="space-y-2">
          <dl className="flex flex-wrap items-start gap-x-8 gap-y-2">
            {[
              [t("reports.requests"), totals.requests.toLocaleString(locale)],
              [t("reports.started"), totals.started.toLocaleString(locale)],
              [
                t("reports.runtime"),
                totals.runtime.samples
                  ? (totals.runtime.seconds / 3600).toLocaleString(locale, { maximumFractionDigits: 2 })
                  : "—",
              ],
            ].map(([label, value]) => (
              <div key={label}>
                <dt className="text-xs text-muted-foreground">{label}</dt>
                <dd className="text-lg font-semibold tabular-nums">{value}</dd>
              </div>
            ))}
            <div>
              <dt className="text-xs text-muted-foreground">{t("reports.jobResults")}</dt>
              <dd className="text-sm">
                <OutcomeCounts outcomes={report.outcomes} />
              </dd>
            </div>
          </dl>
          <p className="text-xs text-muted-foreground">
            {t("reports.activityCoverage", {
              runtime: coverage(totals.runtime.samples, totals.runtime.eligible),
              jobs: coverage(totals.job_results, totals.job_result_eligible),
            })}{" "}
            {t("reports.runtimeHelp")}
          </p>
        </CardContent>
      </Card>
      <div className="flex flex-wrap gap-3">
        {table && (
          <ReportSelect
            inline
            label={t("reports.rankBy")}
            value={dimension}
            options={["owner", "repository"].map((d) => [
              d,
              t(d === "owner" ? "reports.owner" : "reports.repository"),
            ])}
            onChange={(ranking) => navigate({ ranking, group_offset: null })}
          />
        )}
        <ReportSelect
          inline
          label={t("reports.distributionMetric")}
          value={sort}
          options={[
            ["requests", t("reports.requests")],
            ["runtime", t("reports.runtime")],
          ]}
          onChange={(sort) => navigate({ sort, group_offset: null })}
        />
      </div>
      {!table && children}
      {table && (
        <>
          <ResourceState resource={groups} />
          {groups.data &&
            (groups.data.items.length ? (
              <Table aria-label={t(`reports.${dimension}`)}>
                <TableHeader>
                  <TableRow>
                    {(
                      [
                        dimension,
                        "requests",
                        "requestShare",
                        "started",
                        "runtime",
                        "runtimeShare",
                        "validSamples",
                        "repositories",
                        "usedSpecs",
                        "activeDays",
                        "lastUsage",
                        "jobResults",
                        "resultCoverage",
                      ] as const
                    )
                      .filter((key) => dimension === "owner" || key !== "repositories")
                      .map((key) => (
                        <TableHead key={key}>{t(`reports.${key}`)}</TableHead>
                      ))}
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {groups.data.items.map((g) => (
                    <TableRow key={g.key}>
                      <TableCell>
                        <Button
                          variant="link"
                          size="sm"
                          className="h-auto p-0"
                          onClick={() => onSelect(dimension, g.key, g.label)}
                        >
                          {reportGroupLabel(t, dimension, g.key, g.label)}
                        </Button>
                      </TableCell>
                      <TableCell>{g.requests}</TableCell>
                      <TableCell>{percent(g.requests, totals.requests, locale)}</TableCell>
                      <TableCell>{g.started}</TableCell>
                      <TableCell>
                        {g.runtime.samples
                          ? (g.runtime.seconds / 3600).toLocaleString(locale, { maximumFractionDigits: 2 })
                          : "—"}
                      </TableCell>
                      <TableCell>
                        {g.runtime.samples ? percent(g.runtime.seconds, totals.runtime.seconds, locale) : "—"}
                      </TableCell>
                      <TableCell>{coverage(g.runtime.samples, g.runtime.eligible)}</TableCell>
                      {dimension === "owner" && <TableCell>{g.active_repositories}</TableCell>}
                      <TableCell>{g.used_specs}</TableCell>
                      <TableCell>{g.active_days}</TableCell>
                      <TableCell>{time(g.last_queued_at)}</TableCell>
                      <TableCell>
                        <OutcomeCounts outcomes={g.outcomes} />
                      </TableCell>
                      <TableCell>{coverage(g.job_results, g.job_result_eligible)}</TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            ) : (
              <p>{t("reports.empty")}</p>
            ))}
          {groups.data && (
            <ReportPager
              offset={offset}
              total={groups.data.total}
              label={pageLabel(groups.data)}
              onChange={(v) => navigate({ group_offset: String(v) })}
            />
          )}
        </>
      )}
      <ReportRequestRows report={report} request={request} search={search} navigate={navigate} onOpenRequest={onOpenRequest} />
    </section>
  )
}
