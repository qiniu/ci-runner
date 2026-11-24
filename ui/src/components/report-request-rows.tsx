import { useMemo } from "react"
import { useTranslation } from "react-i18next"
import { adminRunnerRequestDetailPath } from "@/app-load-policy"
import {
  reportTimeFormatter,
  outcomeCategory,
  type RunnerReport,
  type ReportPage,
  type ReportRequestRow,
} from "@/runner-reports"
import { useReportResource, type ReportFetch } from "@/use-report-resource"
import { Button } from "./ui/button"
import { Table, TableHeader, TableHead, TableBody, TableCell, TableRow } from "./ui/table"
import { ReportPager, ResourceState } from "./report-controls"
export type ReportActions = {
  request: ReportFetch
  search: string
  navigate: (values: Record<string, string | null>) => void
  onOpenRequest: (id: string, search: string) => void
}
export function ReportRequestLink({
  id,
  search,
  onOpenRequest,
}: {
  id: string
  search: string
  onOpenRequest: ReportActions["onOpenRequest"]
}) {
  const back = new URLSearchParams(search)
  back.set("selected_request", id)
  const returnSearch = "?" + back
  const href = adminRunnerRequestDetailPath(
    id,
    "?" + new URLSearchParams({ report_return: "/admin/reports" + returnSearch }),
  )
  return (
    <a
      href={href}
      className="text-primary hover:underline"
      onClick={(event) => {
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return
        event.preventDefault()
        onOpenRequest(id, returnSearch)
      }}
    >
      {id}
    </a>
  )
}

export function ReportRequestRows({
  report,
  request,
  search,
  navigate,
  onOpenRequest,
  dimension = "",
  groupKey = "",
}: ReportActions & { report: RunnerReport; dimension?: string; groupKey?: string }) {
  const { t, i18n } = useTranslation()
  const url = new URLSearchParams(search)
  const base = "/reports/" + encodeURIComponent(report.snapshot)
  const locale = i18n.resolvedLanguage
  const time = useMemo(
    () => reportTimeFormatter(locale, report.query.timezone),
    [locale, report.query.timezone],
  )
  const duration =
    dimension === "startup" || dimension === "creation" || dimension === "cleanup" ? dimension : undefined
  const requestOffset = Number(url.get("requests_offset") || 0)
  const showRequests = url.has("requests_offset") && report.request_detail_available !== false
  const rows = useReportResource<ReportPage<ReportRequestRow>>(
    showRequests
      ? base +
          "/requests?" +
          new URLSearchParams({
            offset: String(requestOffset),
            limit: "20",
            ...(dimension && { dimension, key: groupKey }),
          })
      : "",
    request,
  )
  return (
    <>
      {report.request_detail_available === false ? (
        <p className="text-sm text-muted-foreground">{t("reports.narrowForRequests")}</p>
      ) : (
        <Button
          variant="outline"
          aria-expanded={showRequests}
          onClick={() => navigate({ requests_offset: showRequests ? null : "0", selected_request: null })}
        >
          {t("reports.requestRows")}
        </Button>
      )}
      {showRequests && (
        <>
          <ResourceState resource={rows} />
          {rows.data && (
            <>
              {rows.data.items.length ? (
                <Table aria-label={t("reports.requestRows")}>
                  <TableHeader>
                    <TableRow>
                      {(["requestId", "queuedAt", "repository", "spec", "jobResults"] as const).map((key) => (
                        <TableHead key={key}>{t(`reports.${key}`)}</TableHead>
                      ))}
                      {duration && <TableHead>{t("reports.durationSeconds")}</TableHead>}
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {rows.data.items.map((row) => (
                      <TableRow
                        key={row.id}
                        data-state={row.id === url.get("selected_request") ? "selected" : undefined}
                      >
                        <TableCell>
                          <ReportRequestLink id={row.id} search={search} onOpenRequest={onOpenRequest} />
                        </TableCell>
                        <TableCell>{time(row.queued_at)}</TableCell>
                        <TableCell>{row.repository || t("common.unknown")}</TableCell>
                        <TableCell>{row.spec_label || t("common.unknown")}</TableCell>
                        <TableCell>{t(`reports.outcome_${outcomeCategory(row.job_result)}`)}</TableCell>
                        {duration && (
                          <TableCell>{row.efficiency?.[duration]?.toLocaleString(locale) ?? "—"}</TableCell>
                        )}
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              ) : (
                <p>{t("reports.empty")}</p>
              )}
              <ReportPager
                offset={requestOffset}
                total={rows.data.total}
                label={t("reports.rankingPage", {
                  first: rows.data.items.length ? requestOffset + 1 : 0,
                  last: requestOffset + rows.data.items.length,
                  total: rows.data.total,
                })}
                onChange={(v) => navigate({ requests_offset: String(v), selected_request: null })}
              />
            </>
          )}
        </>
      )}
    </>
  )
}
