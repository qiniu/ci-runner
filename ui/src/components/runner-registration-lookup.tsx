import { useEffect, useRef, useState } from "react"
import { Loader2, Search } from "lucide-react"
import { useTranslation } from "react-i18next"

import { formatTime } from "@/admin-format"
import { Button } from "@/components/ui/button"

type RegistrationLookup = {
  lookup_status: "found" | "not_found"
  repository_full_name: string
  runner_name: string
  scope: "repository" | "organization"
  target: string
  runner_group?: string
  observed_at: string
  runner?: { id: number; status: "online" | "offline" | "unknown"; busy: boolean }
}

export function RunnerRegistrationLookup({ id, request }: {
  id: string
  request: (url: string, options?: RequestInit) => Promise<unknown>
}) {
  const { t, i18n } = useTranslation()
  const [result, setResult] = useState<RegistrationLookup | null>(null)
  const [loading, setLoading] = useState(false)
  const [failed, setFailed] = useState(false)
  const pending = useRef<AbortController | null>(null)
  useEffect(() => () => pending.current?.abort(), [])

  async function lookup() {
    if (pending.current) return
    const controller = new AbortController()
    pending.current = controller
    setLoading(true)
    setFailed(false)
    setResult(null)
    try {
      const next = await request(`/runner_requests/${encodeURIComponent(id)}/github-runner`, {
        method: "GET", signal: controller.signal,
      }) as RegistrationLookup
      if (!controller.signal.aborted) setResult(next)
    } catch {
      if (!controller.signal.aborted) setFailed(true)
    } finally {
      if (!controller.signal.aborted) {
        pending.current = null
        setLoading(false)
      }
    }
  }

  return (
    <section className="space-y-3 rounded-lg border p-4">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div>
          <div className="text-sm font-medium">{t("admin.runnerRegistrationTitle")}</div>
          <p className="mt-1 text-xs text-muted-foreground">{t("admin.runnerRegistrationDescription")}</p>
        </div>
        <Button type="button" size="sm" variant="outline" disabled={loading} onClick={() => void lookup()}>
          {loading ? <Loader2 className="animate-spin" /> : <Search />}
          {t("admin.runnerRegistrationQuery")}
        </Button>
      </div>
      <div aria-live="polite" className="space-y-1 text-sm">
        {failed ? <p role="alert" className="text-destructive">{t("admin.runnerRegistrationFailed")}</p> : null}
        {result ? (
          <>
            <p className="font-medium">{result.lookup_status === "found" ? t("admin.runnerRegistrationFound") : t("admin.runnerRegistrationAbsent")}</p>
            <p className="break-all font-mono text-xs">{result.runner_name}</p>
            <p className="text-xs text-muted-foreground">
              {result.scope === "organization"
                ? t("admin.runnerRegistrationOrganization", { target: result.target, group: result.runner_group || "-" })
                : t("admin.runnerRegistrationRepository", { target: result.target })}
            </p>
            {result.runner ? (
              <p className="text-xs">
                {t("admin.runnerRegistrationDetails", {
                  id: result.runner.id,
                  status: result.runner.status,
                  busy: result.runner.busy ? t("admin.runnerRegistrationBusy") : t("admin.runnerRegistrationIdle"),
                })}
              </p>
            ) : null}
            <p className="text-xs text-muted-foreground">{t("admin.runnerRegistrationObservedAt", { time: formatTime(result.observed_at, i18n.resolvedLanguage) })}</p>
            <p className="text-xs text-muted-foreground">{t("admin.runnerRegistrationEvidenceLimit")}</p>
          </>
        ) : null}
      </div>
    </section>
  )
}
