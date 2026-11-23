import type { ReactNode } from "react"
import { useTranslation } from "react-i18next"
import type { ReportSpecUsage, ReportTotals, SpecSegment } from "@/runner-reports"
import { Card, CardContent } from "@/components/ui/card"

function share(value: number | undefined, total: number, locale?: string) {
  if (value === undefined || total <= 0) return "—"
  return (value / total).toLocaleString(locale, { style: "percent", maximumFractionDigits: 1 })
}

function ShareBars({
  title,
  rows,
  runtime,
  total,
}: {
  title: string
  rows: SpecSegment[]
  runtime: boolean
  total: number
}) {
  const { t, i18n } = useTranslation()
  return (
    <div className="min-w-0">
      <h4 className="mb-4 font-medium">{title}</h4>
      <ul aria-label={title} className="space-y-3">
        {rows.map((row) => {
          const value = runtime ? (row.runtime.samples ? row.runtime.seconds : undefined) : row.requests
          const size = total > 0 && value !== undefined ? (100 * value) / total : 0
          const label = row.other ? t("reports.otherGroups") : row.label || t("common.unknown")
          const unit =
            value === undefined
              ? "—"
              : runtime
                ? t("reports.hoursValue", {
                    value: (value / 3600).toLocaleString(i18n.resolvedLanguage, { maximumFractionDigits: 2 }),
                  })
                : value.toLocaleString(i18n.resolvedLanguage)
          return (
            <li key={row.other ? "other" : "group:" + row.key}>
              <div className="mb-1 flex items-baseline justify-between gap-3 text-sm">
                <span className="min-w-0 break-all">{label}</span>
                <span className="shrink-0 tabular-nums">
                  {share(value, total, i18n.resolvedLanguage)}{" "}
                  <span className="text-xs text-muted-foreground">· {unit}</span>
                </span>
              </div>
              <div aria-hidden="true" className="h-2.5 overflow-hidden rounded-sm bg-muted">
                <div
                  className="h-full rounded-sm"
                  style={{
                    width: `${Math.min(size, 100)}%`,
                    background: row.other
                      ? "var(--muted-foreground)"
                      : runtime
                        ? "var(--chart-1)"
                        : "var(--chart-2)",
                  }}
                />
              </div>
            </li>
          )
        })}
      </ul>
    </div>
  )
}

export function SpecRepositoryCharts({
  item,
  totals,
  children,
}: {
  children?: ReactNode
  item: ReportSpecUsage
  totals?: ReportTotals
}) {
  const { t, i18n } = useTranslation()
  const number = (value?: number) =>
    value === undefined ? "—" : value.toLocaleString(i18n.resolvedLanguage, { maximumFractionDigits: 1 })
  const metrics = [
    [
      t("reports.requests"),
      number(item.requests),
      totals && share(item.requests, totals.requests, i18n.resolvedLanguage),
    ],
    [
      t("reports.runtime"),
      item.runtime.samples ? number(item.runtime.seconds / 3600) : "—",
      totals &&
        share(
          item.runtime.samples ? item.runtime.seconds : undefined,
          totals.runtime.seconds,
          i18n.resolvedLanguage,
        ),
    ],
    [t("reports.started"), number(item.started)],
    [t("reports.repositories"), number(item.repositories)],
    [
      t("reports.averageRuntime"),
      item.average_runtime_seconds === undefined
        ? "—"
        : t("reports.secondsValue", { value: number(item.average_runtime_seconds) }),
    ],
    [
      t("reports.creationTime"),
      item.average_creation_seconds === undefined
        ? "—"
        : t("reports.secondsValue", { value: number(item.average_creation_seconds) }),
    ],
    [
      t("reports.p95Runtime"),
      item.p95_runtime_seconds === undefined
        ? "—"
        : t("reports.secondsValue", { value: number(item.p95_runtime_seconds) }),
    ],
  ]
  return (
    <Card className="min-w-0">
      <CardContent className="space-y-5">
        {item.requests > 0 ? (
          <>
            <dl className="grid grid-cols-2 gap-3 sm:grid-cols-3 xl:grid-cols-7">
              {metrics.map(([label, value, percent]) => (
                <div key={label}>
                  <dt className="text-xs text-muted-foreground">{label}</dt>
                  <dd className="mt-1 font-semibold tabular-nums">
                    {value}{" "}
                    {percent && (
                      <span className="text-xs font-normal text-muted-foreground">· {percent}</span>
                    )}
                  </dd>
                </div>
              ))}
            </dl>
            {item.repository_mix && (
              <div className="grid gap-6 border-t pt-5 lg:grid-cols-2 lg:gap-10">
                <ShareBars
                  title={t("reports.repoRequestShare")}
                  rows={item.repository_mix.requests}
                  runtime={false}
                  total={item.requests}
                />
                <ShareBars
                  title={t("reports.repoRuntimeShare")}
                  rows={item.repository_mix.runtime}
                  runtime
                  total={item.runtime.seconds}
                />
              </div>
            )}
            {item.owner_mix && (
              <div className="grid gap-6 border-t pt-5 lg:grid-cols-2 lg:gap-10">
                <ShareBars
                  title={t("reports.ownerRequestShare")}
                  rows={item.owner_mix.requests}
                  runtime={false}
                  total={item.requests}
                />
                <ShareBars
                  title={t("reports.ownerRuntimeShare")}
                  rows={item.owner_mix.runtime}
                  runtime
                  total={item.runtime.seconds}
                />
              </div>
            )}
            {children}
            {item.job_results && (
              <div>
                <h4 className="mb-2 font-medium">{t("reports.jobResults")}</h4>
                <ul className="flex flex-wrap gap-3 text-sm">
                  {Object.entries(item.job_results)
                    .sort(([a], [b]) => a.localeCompare(b))
                    .map(([key, n]) => (
                      <li key={key} className="rounded-md bg-muted px-3 py-1 tabular-nums">
                        {key === "unknown" ? t("common.unknown") : key} · {number(n)} ·{" "}
                        {share(n, item.requests, i18n.resolvedLanguage)}
                      </li>
                    ))}
                </ul>
              </div>
            )}
            <p className="text-xs text-muted-foreground">
              {t("reports.specCoverage", { samples: item.runtime.samples, eligible: item.runtime.eligible })}
            </p>
          </>
        ) : (
          <p className="text-sm text-muted-foreground">{t("reports.unusedInPeriod")}</p>
        )}
      </CardContent>
    </Card>
  )
}
