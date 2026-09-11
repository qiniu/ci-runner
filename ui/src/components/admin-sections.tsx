import { useCallback, useEffect, useRef, useState, type FormEvent } from "react"
import { useTranslation } from "react-i18next"
import { Activity, AlertTriangle, ArrowLeft, CheckCircle2, ChevronDown, Copy, Loader2, RefreshCw, ShieldAlert, Trash2 } from "lucide-react"

import {
  activeStatuses,
  logNames,
  type AuditEvent,
  type DiagnosticsSummary,
  type RunnerDiagnosticFinding,
  type RunnerRequestDiagnosis,
  type RunnerSpec,
  type RunnerSpecMatch,
  type RunnerState,
} from "@/admin-types"
import { localizedLogTextForView, type LocalizedLogText } from "@/app-log-state"
import { formatTime, runnerDisplayStatus } from "@/admin-format"
import type { AppTFunction } from "@/i18n"
import { Detail, StatusBadge } from "@/components/admin-shared"
import { RunnerRequestDetails } from "@/components/runner-request-details"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"

export function OverviewSection({
  runners,
  runnerSpecs,
  onEditRunnerSpec,
}: {
  runners: RunnerState[]
  runnerSpecs: RunnerSpec[]
  onEditRunnerSpec: (runnerSpec: RunnerSpec) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-4 xl:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>{t("admin.recentRunnerRequests")}</CardTitle>
          <CardDescription>{t("admin.recentRunnerRequestsDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {runners.slice(0, 8).map((runner) => (
            <div key={runner.id} className="flex items-center justify-between gap-3 rounded-md border p-3">
              <div className="min-w-0">
                <div className="truncate font-medium">{runner.repository_full_name || runner.id}</div>
                <div className="truncate text-xs text-muted-foreground">
                  {runner.runner_spec_name || "-"} · {runner.runner_name}
                </div>
              </div>
              <StatusBadge status={runnerDisplayStatus(runner)} />
            </div>
          ))}
          {runners.length === 0 ? (
            <div className="text-sm text-muted-foreground">{t("admin.noRunnerRequests")}</div>
          ) : null}
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>{t("admin.runnerSpecs")}</CardTitle>
          <CardDescription>{t("admin.specsDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
            {runnerSpecs.map((runnerSpec) => (
              <div
                key={runnerSpec.name}
                className="min-w-0 rounded-md border p-3 text-sm"
                onClick={() => onEditRunnerSpec(runnerSpec)}
              >
                <div className="truncate font-medium">{runnerSpec.name}</div>
                <div className="truncate text-xs text-muted-foreground">
                  {runnerSpec.labels.join(", ")} · {t("admin.namedTemplate", { id: runnerSpec.template_id })}
                </div>
              </div>
            ))}
        </CardContent>
      </Card>
    </div>
  )
}

export function MatchSection({
  matchRepository,
  matchLabels,
  matchResult,
  onRepositoryChange,
  onLabelsChange,
  onSubmit,
}: {
  matchRepository: string
  matchLabels: string
  matchResult: RunnerSpecMatch | null
  onRepositoryChange: (value: string) => void
  onLabelsChange: (value: string) => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
}) {
  const { t } = useTranslation()
  return (
    <div className="grid gap-4 xl:grid-cols-[420px_minmax(0,1fr)]">
      <Card>
        <CardHeader>
          <CardTitle>{t("admin.labelMatchingTest")}</CardTitle>
          <CardDescription>{t("admin.labelMatchingDescription")}</CardDescription>
        </CardHeader>
        <CardContent>
          <form className="grid gap-3" onSubmit={onSubmit}>
            <Input
              value={matchRepository}
              onChange={(event) => onRepositoryChange(event.target.value)}
              placeholder="owner/repo"
            />
            <Input
              value={matchLabels}
              onChange={(event) => onLabelsChange(event.target.value)}
              placeholder="self-hosted,e2b"
            />
            <Button type="submit">{t("admin.runMatch")}</Button>
          </form>
        </CardContent>
      </Card>
      <Card>
        <CardHeader>
          <CardTitle>{t("admin.matchResult")}</CardTitle>
          <CardDescription>{t("admin.matchResultDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          {matchResult ? (
            <>
              <Detail label={t("admin.repository")} value={matchResult.repository_full_name || "-"} />
              <Detail label={t("admin.labels")} value={matchResult.labels.join(", ") || "-"} />
              <Detail label={t("admin.runnerSpec")} value={matchResult.runner_spec?.name || "-"} />
              <Detail label={t("admin.reason")} value={matchResult.reason || t("admin.matched")} />
            </>
          ) : (
            <div className="text-sm text-muted-foreground">{t("admin.noMatchRun")}</div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

export function AuditSection({ auditEvents }: { auditEvents: AuditEvent[] }) {
  const { t, i18n } = useTranslation()
  return (
    <Card className="min-w-0">
      <CardHeader>
        <CardTitle>{t("admin.auditEvents")}</CardTitle>
        <CardDescription>{t("admin.auditDescription")}</CardDescription>
      </CardHeader>
      <CardContent className="p-0">
        <Table>
          <TableHeader>
            <TableRow>
              <TableHead>{t("admin.time")}</TableHead>
              <TableHead>{t("admin.actor")}</TableHead>
              <TableHead>{t("admin.action")}</TableHead>
              <TableHead>{t("admin.resource")}</TableHead>
              <TableHead>{t("admin.payload")}</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {auditEvents.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="h-24 text-center text-muted-foreground">
                  {t("admin.noAuditEvents")}
                </TableCell>
              </TableRow>
            ) : (
              auditEvents.map((event) => (
                <TableRow key={event.id}>
                  <TableCell>{formatTime(event.created_at, i18n.resolvedLanguage)}</TableCell>
                  <TableCell>{event.actor}</TableCell>
                  <TableCell>{event.action}</TableCell>
                  <TableCell>
                    {event.resource_type} · {event.resource_id}
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">
                    <div className="max-w-[420px] truncate">{event.payload_json || "-"}</div>
                  </TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>
      </CardContent>
    </Card>
  )
}

export function DiagnosticsSection({
  diagnostics,
  request,
}: {
  diagnostics: DiagnosticsSummary | null
  request: (url: string, options?: RequestInit) => Promise<unknown>
}) {
  return <RunnerdRuntimeDiagnostics diagnostics={diagnostics} request={request} />
}

export function RunnerRequestSection({
  identifier,
  request,
  onBackToRunnerRequests,
  onResolvedRequestID,
  onCopyRunnerID,
  onRetryRunner,
  onStopRunner,
  pollIntervalMs = 5000,
}: {
  identifier: string
  request: (url: string, options?: RequestInit) => Promise<unknown>
  onBackToRunnerRequests: () => void
  onResolvedRequestID?: (id: string) => void
  onCopyRunnerID?: (id: string) => void
  onRetryRunner?: (id: string) => Promise<boolean>
  onStopRunner?: (id: string) => Promise<boolean>
  pollIntervalMs?: number
}) {
  const { t } = useTranslation()
  const [diagnosis, setDiagnosis] = useState<RunnerRequestDiagnosis | null>(null)
  const [diagnosisError, setDiagnosisError] = useState("")
  const [diagnosing, setDiagnosing] = useState(false)
  const [requestAction, setRequestAction] = useState<"retry" | "stop" | null>(null)
  const diagnosisRequestGeneration = useRef(0)

  const runDiagnosis = useCallback(async (
    identifier: string,
    { preserveExisting = false }: { preserveExisting?: boolean } = {},
  ) => {
    const id = identifier.trim()
    if (!id) return
    const generation = ++diagnosisRequestGeneration.current
    setDiagnosing(true)
    setDiagnosisError("")
    try {
      const result = await request(`/runner_requests/${encodeURIComponent(id)}/diagnostics`) as RunnerRequestDiagnosis
      if (generation !== diagnosisRequestGeneration.current) return
      setDiagnosis(result)
      onResolvedRequestID?.(result.state.id)
    } catch (error) {
      if (generation !== diagnosisRequestGeneration.current) return
      if (!preserveExisting) setDiagnosis(null)
      setDiagnosisError(error instanceof Error ? error.message : t("admin.runnerDiagnosisFailed"))
    } finally {
      if (generation === diagnosisRequestGeneration.current) setDiagnosing(false)
    }
  }, [onResolvedRequestID, request, t])

  useEffect(() => {
    setDiagnosis(null)
    setDiagnosisError("")
    void runDiagnosis(identifier)
    return () => {
      diagnosisRequestGeneration.current += 1
    }
  }, [identifier, runDiagnosis])

  useEffect(() => {
    if (diagnosing || !diagnosis || !activeStatuses.has(diagnosis.state.status)) return
    const timer = window.setTimeout(() => {
      void runDiagnosis(diagnosis.state.id, { preserveExisting: true })
    }, pollIntervalMs)
    return () => window.clearTimeout(timer)
  }, [diagnosis, diagnosing, pollIntervalMs, runDiagnosis])

  const runRequestAction = async (
    action: "retry" | "stop",
    handler: ((id: string) => Promise<boolean>) | undefined,
  ) => {
    if (!diagnosis || !handler) return
    setRequestAction(action)
    try {
      if (await handler(diagnosis.state.id)) {
        await runDiagnosis(diagnosis.state.id, { preserveExisting: true })
      }
    } finally {
      setRequestAction(null)
    }
  }

  return (
    <Card className="shrink-0 gap-0 overflow-hidden py-0">
      <CardHeader className="gap-1 px-5 py-4">
        <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-start">
          <div className="space-y-1">
            <CardTitle className="text-base tracking-tight">{t("admin.runnerRequestDetails")}</CardTitle>
            <CardDescription className="max-w-4xl text-pretty leading-relaxed">
              {t("admin.runnerRequestDetailsDescription")}
            </CardDescription>
          </div>
          <div className="flex gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={diagnosing}
              onClick={() => void runDiagnosis(diagnosis?.state.id || identifier, { preserveExisting: true })}
            >
              <RefreshCw className={diagnosing ? "animate-spin" : undefined} />
              {t("common.refresh")}
            </Button>
            <Button type="button" variant="outline" size="sm" onClick={onBackToRunnerRequests}>
              <ArrowLeft />
              {t("admin.backToRunnerRequests")}
            </Button>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-5 border-t px-5 py-5">
        {diagnosing && !diagnosis ? (
          <div className="flex items-center justify-center gap-2 rounded-lg border border-dashed px-4 py-7 text-sm text-muted-foreground">
            <Loader2 className="animate-spin" />
            {t("admin.diagnosingRunnerRequest")}
          </div>
        ) : null}
        {diagnosisError ? (
          <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
            {diagnosisError}
          </div>
        ) : null}
        {diagnosis ? (
          <RunnerRequestDiagnosisResult
            key={diagnosis.state.id}
            diagnosis={diagnosis}
            request={request}
            requestAction={requestAction}
            onCopyRunnerID={onCopyRunnerID}
            onRetryRunner={onRetryRunner ? () => void runRequestAction("retry", onRetryRunner) : undefined}
            onStopRunner={onStopRunner ? () => void runRequestAction("stop", onStopRunner) : undefined}
          />
        ) : null}
      </CardContent>
    </Card>
  )
}

export function RunnerdRuntimeDiagnostics({
  diagnostics,
  request,
}: {
  diagnostics: DiagnosticsSummary | null
  request: (url: string, options?: RequestInit) => Promise<unknown>
}) {
  const { t } = useTranslation()
  const [runtimeVars, setRuntimeVars] = useState("")
  const [runtimeVarsError, setRuntimeVarsError] = useState("")
  const [runtimeVarsLoaded, setRuntimeVarsLoaded] = useState(false)
  const [loadingRuntimeVars, setLoadingRuntimeVars] = useState(false)

  const loadRuntimeVars = async () => {
    setLoadingRuntimeVars(true)
    setRuntimeVarsError("")
    try {
      const result = await request("/diagnostics/vars")
      setRuntimeVars(typeof result === "string" ? result : JSON.stringify(result, null, 2))
      setRuntimeVarsLoaded(true)
    } catch (error) {
      setRuntimeVarsError(error instanceof Error ? error.message : t("admin.expvarLoadFailed"))
    } finally {
      setLoadingRuntimeVars(false)
    }
  }

  return (
    <div className="grid items-start gap-4 xl:grid-cols-2">
      <Card>
        <CardHeader>
          <CardTitle>{t("admin.diagnosticsSummary")}</CardTitle>
          <CardDescription>{t("admin.diagnosticsDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-3">
          <Detail label={t("admin.stateBackend")} value={diagnostics?.state.backend || "-"} />
          <Detail label={t("admin.database")} value={diagnostics?.state.database || "-"} />
          <Detail label={t("admin.githubAuth")} value={diagnostics?.github.auth_mode || "-"} />
          <Detail label={t("admin.installation")} value={diagnostics?.github.installation_id || "-"} />
          <Detail label={t("admin.githubAPI")} value={diagnostics?.github.api_base_url || "-"} />
          <div className="space-y-2">
            <div className="text-sm font-medium">{t("admin.pprofEndpoints")}</div>
            {diagnostics?.pprof?.length ? (
              diagnostics.pprof.map((item) => (
                <div key={item.address_file} className="rounded-md border p-3 text-xs">
                  <div className="font-medium">{item.address}</div>
                  <div className="text-muted-foreground">{item.address_file}</div>
                  <div className="text-muted-foreground">{item.dump_script}</div>
                </div>
              ))
            ) : (
              <div className="text-sm text-muted-foreground">{t("admin.noPprof")}</div>
            )}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>{t("admin.expvarSnapshot")}</CardTitle>
          <CardDescription>{t("admin.expvarDescription")}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <Button variant="outline" onClick={() => void loadRuntimeVars()} disabled={loadingRuntimeVars}>
            {loadingRuntimeVars ? <Loader2 className="animate-spin" /> : <RefreshCw />}
            {loadingRuntimeVars
              ? t("admin.loadingExpvar")
              : runtimeVarsLoaded
                ? t("admin.refreshExpvar")
                : t("admin.loadExpvar")}
          </Button>
          {runtimeVarsError ? (
            <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-sm text-destructive">
              {runtimeVarsError}
            </div>
          ) : null}
          {runtimeVarsLoaded ? (
            <pre className="max-h-[56vh] overflow-auto rounded-lg border bg-muted/50 p-3 text-xs leading-relaxed whitespace-pre-wrap">
              {runtimeVars || t("admin.noDebugVars")}
            </pre>
          ) : (
            <div className="rounded-lg border border-dashed px-4 py-7 text-center text-sm text-muted-foreground">
              {t("admin.expvarNotLoaded")}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

export function RunnerRequestDiagnosisResult({
  diagnosis,
  request,
  requestAction = null,
  onCopyRunnerID,
  onRetryRunner,
  onStopRunner,
}: {
  diagnosis: RunnerRequestDiagnosis
  request?: (url: string, options?: RequestInit) => Promise<unknown>
  requestAction?: "retry" | "stop" | null
  onCopyRunnerID?: (id: string) => void
  onRetryRunner?: () => void
  onStopRunner?: () => void
}) {
  const { t, i18n } = useTranslation()
  const state = diagnosis.state
  return (
    <div className="space-y-5">
      <div className="flex flex-col justify-between gap-3 md:flex-row md:items-start">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm font-semibold">{state.id}</span>
            <StatusBadge status={runnerDisplayStatus(state)} />
            {diagnosis.github_job.lookup_status === "ok" ? (
              <Badge variant="outline">
                {t("admin.githubJobResult", { result: diagnosis.github_job.conclusion || diagnosis.github_job.status || "-" })}
              </Badge>
            ) : null}
          </div>
          <div className="mt-1 truncate text-sm text-muted-foreground">
            {state.repository_full_name || "-"} · {state.assigned_job_name || state.runner_name}
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {onCopyRunnerID ? (
            <Button type="button" size="sm" variant="outline" onClick={() => onCopyRunnerID(state.id)}>
              <Copy />
              {t("admin.copyRunnerID")}
            </Button>
          ) : null}
          {runnerDisplayStatus(state) === "failed" && onRetryRunner ? (
            <Button type="button" size="sm" variant="outline" disabled={requestAction !== null} onClick={onRetryRunner}>
              {requestAction === "retry" ? <Loader2 className="animate-spin" /> : <RefreshCw />}
              {t("admin.retry")}
            </Button>
          ) : null}
          {activeStatuses.has(state.status) && onStopRunner ? (
            <Button type="button" size="sm" variant="outline" disabled={requestAction !== null} onClick={onStopRunner}>
              {requestAction === "stop" ? <Loader2 className="animate-spin" /> : <Trash2 />}
              {t("admin.stop")}
            </Button>
          ) : null}
          {state.github_job_url ? (
            <Button asChild size="sm" variant="outline">
              <a href={state.github_job_url} target="_blank" rel="noreferrer">{t("admin.openGitHubJob")}</a>
            </Button>
          ) : null}
        </div>
      </div>

      <section className="space-y-2">
        <div className="text-xs font-semibold uppercase tracking-[0.14em] text-muted-foreground">
          {t("admin.diagnosticFindings")}
        </div>
        <div className="grid grid-cols-1 gap-2">
          {diagnosis.findings.map((finding, index) => (
            <div
              key={`${finding.code}-${index}`}
              className={`flex gap-3 rounded-lg border p-3 ${diagnosticFindingClass(finding.severity)}`}
            >
              <div className="mt-0.5 shrink-0">{diagnosticFindingIcon(finding.severity)}</div>
              <div className="min-w-0">
                <div className="text-sm font-medium">{diagnosticFindingText(finding, t)}</div>
                {finding.detail ? <div className="mt-1 break-all font-mono text-[11px] opacity-70">{finding.detail}</div> : null}
              </div>
            </div>
          ))}
        </div>
      </section>

      <details className="group overflow-hidden rounded-lg border bg-muted/10">
        <summary className="flex cursor-pointer list-none items-center justify-between gap-4 px-4 py-3 marker:content-none hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring">
          <div>
            <div className="text-sm font-medium">{t("admin.runnerRequestContext")}</div>
            <div className="mt-0.5 text-xs text-muted-foreground">{t("admin.runnerRequestContextDescription")}</div>
          </div>
          <ChevronDown className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" />
        </summary>
        <div className="border-t bg-background/70 p-4">
          <RunnerRequestDetails runner={state} />
        </div>
      </details>

      {request ? <RunnerRequestLogs key={state.id} requestID={state.id} request={request} /> : null}

      <section className="space-y-2">
        <div className="flex items-center justify-between gap-3">
          <div className="text-xs font-semibold uppercase tracking-[0.14em] text-muted-foreground">
            {t("admin.runnerEventTimeline")}
          </div>
          <span className="text-xs tabular-nums text-muted-foreground">
            {t("admin.runnerEventCount", { count: diagnosis.events.length })}
          </span>
        </div>
        {diagnosis.events_truncated ? (
          <div className="text-xs text-amber-700 dark:text-amber-300">{t("admin.runnerEventsTruncated")}</div>
        ) : null}
        <div className="rounded-lg border bg-muted/20 px-4 py-2">
          {diagnosis.events.length ? diagnosis.events.map((event) => (
            <div key={event.id} className="relative grid gap-1 border-l py-2 pl-5 sm:grid-cols-[170px_110px_minmax(0,1fr)] sm:gap-3">
              <span className="absolute -left-1 top-3.5 size-2 rounded-full bg-sky-500 ring-4 ring-background" />
              <time className="font-mono text-[11px] text-muted-foreground">
                {formatTime(event.created_at, i18n.resolvedLanguage)}
              </time>
              <span className="font-mono text-[11px] text-muted-foreground">{event.event_type}</span>
              <pre className="min-w-0 whitespace-pre-wrap break-words font-mono text-xs leading-relaxed">{event.message.trimEnd()}</pre>
            </div>
          )) : (
            <div className="py-8 text-center text-sm text-muted-foreground">{t("admin.noRunnerEvents")}</div>
          )}
        </div>
      </section>
    </div>
  )
}

type RunnerLogName = (typeof logNames)[number]

function RunnerRequestLogs({
  requestID,
  request,
}: {
  requestID: string
  request: (url: string, options?: RequestInit) => Promise<unknown>
}) {
  const { t } = useTranslation()
  const [selectedLog, setSelectedLog] = useState<RunnerLogName>("control.log")
  const [logText, setLogText] = useState<LocalizedLogText>({ kind: "message", key: "user.loadingRunnerLog" })
  const [loaded, setLoaded] = useState(false)
  const [loading, setLoading] = useState(false)
  const requestGeneration = useRef(0)

  const loadLog = useCallback(async (name: RunnerLogName) => {
    const generation = ++requestGeneration.current
    setLoading(true)
    setLogText({ kind: "message", key: "user.loadingRunnerLog" })
    try {
      const text = await request(`/runner_requests/${encodeURIComponent(requestID)}/logs/${encodeURIComponent(name)}`)
      if (generation !== requestGeneration.current) return
      setLogText(typeof text === "string" && text
        ? { kind: "text", text }
        : { kind: "message", key: "user.runnerLogEmpty" })
      setLoaded(true)
    } catch (error) {
      if (generation !== requestGeneration.current) return
      setLogText(error instanceof Error
        ? { kind: "text", text: error.message }
        : { kind: "message", key: "app.loadFailed" })
      setLoaded(true)
    } finally {
      if (generation === requestGeneration.current) setLoading(false)
    }
  }, [request, requestID])

  return (
    <details
      className="group overflow-hidden rounded-lg border bg-muted/10"
      onToggle={(event) => {
        if (event.currentTarget.open && !loaded && !loading) void loadLog(selectedLog)
      }}
    >
      <summary className="flex cursor-pointer list-none items-center justify-between gap-4 px-4 py-3 marker:content-none hover:bg-muted/40 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring">
        <div>
          <div className="text-sm font-medium">{t("admin.runnerLogs")}</div>
          <div className="mt-0.5 text-xs text-muted-foreground">{t("admin.logsDescription")}</div>
        </div>
        <ChevronDown className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-180" />
      </summary>
      <div className="space-y-3 border-t bg-background/70 p-4">
        <div className="flex items-center justify-between gap-3">
          <Tabs
            value={selectedLog}
            onValueChange={(value) => {
              const name = value as RunnerLogName
              setSelectedLog(name)
              void loadLog(name)
            }}
          >
            <TabsList>
              {logNames.map((name) => (
                <TabsTrigger key={name} value={name}>
                  {name.replace(".log", "")}
                </TabsTrigger>
              ))}
            </TabsList>
          </Tabs>
          <Button type="button" variant="outline" size="sm" disabled={loading} onClick={() => void loadLog(selectedLog)}>
            {loading ? <Loader2 className="animate-spin" /> : <RefreshCw />}
            {t("common.refresh")}
          </Button>
        </div>
        <pre className="min-h-64 rounded-lg border bg-muted/50 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words">
          {localizedLogTextForView(logText, t)}
        </pre>
      </div>
    </details>
  )
}

function diagnosticFindingText(finding: RunnerDiagnosticFinding, t: AppTFunction) {
  switch (finding.code) {
    case "github_job_failed":
      return t("admin.findingGitHubJobFailed")
    case "request_completed_after_github_failure":
      return t("admin.findingCompletedAfterGitHubFailure")
    case "runner_termination_unobserved":
      return t("admin.findingRunnerTerminationUnobserved")
    case "sandbox_gone_before_cleanup":
      return t("admin.findingSandboxGone")
    case "event_history_truncated":
      return t("admin.findingEventsTruncated")
    case "github_lookup_unavailable":
      return t("admin.findingGitHubLookupUnavailable")
    case "runner_request_failed":
      return t("admin.findingRunnerRequestFailed")
    case "runner_request_unmatched":
      return t("admin.findingRunnerRequestUnmatched")
    case "no_anomaly_detected":
      return t("admin.findingNoAnomaly")
    default:
      return t("admin.findingUnknown", { code: finding.code })
  }
}

function diagnosticFindingClass(severity: string) {
  if (severity === "critical") return "border-destructive/30 bg-destructive/5 text-destructive"
  if (severity === "warning") return "border-amber-500/30 bg-amber-500/5 text-amber-800 dark:text-amber-200"
  if (severity === "ok") return "border-emerald-500/30 bg-emerald-500/5 text-emerald-800 dark:text-emerald-200"
  return "border-muted bg-muted/30 text-foreground"
}

function diagnosticFindingIcon(severity: string) {
  if (severity === "critical") return <ShieldAlert className="size-4" />
  if (severity === "warning") return <AlertTriangle className="size-4" />
  if (severity === "ok") return <CheckCircle2 className="size-4" />
  return <Activity className="size-4" />
}
