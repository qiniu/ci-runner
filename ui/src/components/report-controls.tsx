import { useTranslation } from "react-i18next"
import { ChartNoAxesColumn, Table2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"

export function ReportDisplayMode({
  table,
  onChange,
}: {
  table: boolean
  onChange: (table: boolean) => void
}) {
  const { t } = useTranslation()
  return (
    <div role="group" aria-label={t("reports.displayMode")} className="inline-flex rounded-lg border p-0.5">
      <Button
        size="sm"
        variant={table ? "ghost" : "secondary"}
        aria-pressed={!table}
        onClick={() => onChange(false)}
      >
        <ChartNoAxesColumn />
        {t("reports.chartMode")}
      </Button>
      <Button
        size="sm"
        variant={table ? "secondary" : "ghost"}
        aria-pressed={table}
        onClick={() => onChange(true)}
      >
        <Table2 />
        {t("reports.tableMode")}
      </Button>
    </div>
  )
}

export function ReportPager({
  offset,
  total,
  label,
  onChange,
}: {
  offset: number
  total: number
  label: string
  onChange: (offset: number) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="flex flex-wrap items-center justify-between gap-2 text-sm text-muted-foreground">
      <span>{label}</span>
      <div className="flex gap-2">
        <Button
          variant="outline"
          size="sm"
          disabled={offset === 0}
          onClick={() => onChange(Math.max(0, offset - 20))}
        >
          {t("reports.previous")}
        </Button>
        <Button
          variant="outline"
          size="sm"
          disabled={offset + 20 >= total}
          onClick={() => onChange(offset + 20)}
        >
          {t("reports.next")}
        </Button>
      </div>
    </div>
  )
}

export function ReportSelect({
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
export function ResourceState({
  resource,
}: {
  resource: { data?: unknown; error?: string; retry?: () => void }
}) {
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
