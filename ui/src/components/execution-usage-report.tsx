import { useTranslation } from "react-i18next"
import { reportTimeFormatter, type ExecutionUsage } from "@/runner-reports"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

export function ExecutionUsageReport({ usage, observedAt, timezone }: {
  usage: ExecutionUsage
  observedAt: string
  timezone: string
}) {
  const { t, i18n } = useTranslation()
  const time = reportTimeFormatter(i18n.resolvedLanguage, timezone)
  const seconds = new Intl.NumberFormat(i18n.resolvedLanguage, { maximumFractionDigits: 3 })
  const rows = [{ ...usage.total, date: t("reports.periodTotal") }, ...usage.buckets]
  return (
    <div className="min-w-0 space-y-3">
      <p className="text-sm">{t("reports.executionBasis")}</p>
      <p className="text-sm text-muted-foreground">{t("reports.executionCoverage", {
        start: time(usage.collection_started_at), end: time(observedAt),
      })}</p>
      {usage.buckets.length === 0 ? <p>{t("reports.beforeCollection")}</p> : <>
        <Table>
          <TableHeader><TableRow>
            {(["executionPeriod", "attemptCount", "attemptSeconds", "provisionalSeconds", "peakRunners", "exactJobs", "jobSeconds"] as const)
              .map(key => <TableHead key={key}>{t(`reports.${key}`)}</TableHead>)}
          </TableRow></TableHeader>
          <TableBody>{rows.map(bucket => (
            <TableRow key={bucket.date}>
              <TableCell>{bucket.date}</TableCell>
              <TableCell>{bucket.attempts}</TableCell>
              <TableCell>{bucket.attempts > bucket.active_attempts ? seconds.format(bucket.attempt_seconds) : "—"}</TableCell>
              <TableCell>{bucket.active_attempts ? seconds.format(bucket.provisional_seconds) : "—"}</TableCell>
              <TableCell>{bucket.peak}</TableCell>
              <TableCell>{bucket.jobs}</TableCell>
              <TableCell>{bucket.jobs ? seconds.format(bucket.job_seconds) : "—"}</TableCell>
            </TableRow>
          ))}</TableBody>
        </Table>
        <p className="text-sm text-muted-foreground">{t("reports.incompleteJobsLabel", { count: usage.incomplete_jobs })}</p>
      </>}
    </div>
  )
}
