import { type ReactNode, useCallback, useEffect, useMemo, useState } from "react"
import { ArrowLeft, CheckCircle2, Clock3, ExternalLink, Loader2, Play, RefreshCw, SquareTerminal, XCircle } from "lucide-react"
import { useTranslation } from "react-i18next"

import type { RunnerJobGroup, RunnerState } from "@/admin-types"
import { activeStatuses } from "@/admin-types"
import { formatRunnerDuration, formatTime, runnerStatusLabel } from "@/admin-format"
import type { AppTFunction } from "@/i18n"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { runnerJobDetailRows, workflowRunLink } from "@/components/runner-job-detail-rows"
import { RunnerEventTimeline } from "@/components/runner-event-timeline"
import { githubJobLink } from "@/components/runner-github-links"
import { useSandboxTerminal } from "@/hooks/use-sandbox-terminal"
import { cn } from "@/lib/utils"

type RunnerJobDetailProps = {
  id: string
  apiBase: string
  onBack: () => void
  onOpenJob?: (id: string) => void
  request: (url: string, options?: RequestInit) => Promise<unknown>
}

type ActiveGuard = {
  current: boolean
}

export function RunnerJobDetail({ id, apiBase, onBack, onOpenJob, request }: RunnerJobDetailProps) {
  const { t, i18n } = useTranslation()
  const locale = i18n.resolvedLanguage
  const [job, setJob] = useState<RunnerState | null>(null)
  const [jobGroup, setJobGroup] = useState<RunnerJobGroup | null>(null)
  const [loading, setLoading] = useState(true)
  const [eventRefreshRevision, setEventRefreshRevision] = useState(0)
  const endpoint = useMemo(() => `${apiBase}/${encodeURIComponent(id)}`, [apiBase, id])
  const githubLink = job ? githubJobLink(job) : null
  const terminalAvailable = job ? isTerminalAvailable(job) : false
  const { terminalEl, terminalSession, terminalError, terminalConnecting, connectTerminal } = useSandboxTerminal({
    endpoint,
    available: terminalAvailable,
    request,
    connectingMessage: t("user.connectingTerminal"),
    streamDisconnectedMessage: t("user.terminalDisconnected"),
    connectErrorMessage: t("user.terminalConnectFailed"),
  })

  const loadJob = useCallback(async (active: ActiveGuard = { current: true }, background = false) => {
    if (!background) setLoading(true)
    try {
      const data = await request(endpoint)
      if (active.current) {
        setJob(data as RunnerState)
      }
    } finally {
      if (active.current && !background) {
        setLoading(false)
      }
    }
  }, [endpoint, request])

  const loadJobGroup = useCallback(async (active: ActiveGuard = { current: true }) => {
    try {
      const data = await request(`${endpoint}/group`)
      if (active.current) {
        setJobGroup(data as RunnerJobGroup)
      }
    } catch {
      if (active.current) {
        setJobGroup(null)
      }
    }
  }, [endpoint, request])

  useEffect(() => {
    const active = { current: true }
    void loadJob(active)
    void loadJobGroup(active)
    return () => {
      active.current = false
    }
  }, [loadJob, loadJobGroup])

  useEffect(() => {
    if (!job || !activeStatuses.has(job.status)) return
    const active = { current: true }
    let timer: number
    const poll = async () => {
      try {
        await loadJob(active, true)
      } catch {
        // Keep the last known state and retry after a transient lookup failure.
      } finally {
        if (active.current) timer = window.setTimeout(() => void poll(), 5000)
      }
    }
    timer = window.setTimeout(() => void poll(), 5000)
    return () => {
      active.current = false
      window.clearTimeout(timer)
    }
  }, [job, loadJob])

  const refreshPage = () => {
    setEventRefreshRevision((revision) => revision + 1)
    void loadJob()
    void loadJobGroup()
  }

  const openSibling = (jobID: string) => {
    if (jobID === id) return
    if (onOpenJob) {
      onOpenJob(jobID)
      return
    }
    window.history.pushState(null, "", `/jobs/${encodeURIComponent(jobID)}`)
  }

  const groupJobs = jobGroup ? [...jobGroup.current_jobs, ...jobGroup.previous_jobs] : job ? [job] : []

  return (
    <div className="flex min-h-screen flex-col bg-background">
      <header className="border-b bg-background/95 px-4 py-3 lg:px-6">
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            <Button type="button" variant="outline" size="icon" onClick={onBack} title={t("auth.jobs")}>
              <ArrowLeft />
            </Button>
            <div className="min-w-0">
              <div className="flex flex-wrap items-center gap-2">
                <h1 className="truncate text-xl font-semibold">{job ? runnerJobTitle(job) : id}</h1>
                {job ? <Badge className={statusClass(job.status)}>{runnerStatusLabel(job.status)}</Badge> : null}
              </div>
              <p className="truncate text-sm text-muted-foreground">
                {job?.repository_full_name || t("user.runnerRequest")} {job?.workflow_name ? ` / ${job.workflow_name}` : ""}
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            {githubLink ? (
              <Button type="button" variant="outline" asChild>
                <a href={githubLink.url} target="_blank" rel="noreferrer">
                  <ExternalLink />
                  {t(githubLink.labelKey)}
                </a>
              </Button>
            ) : null}
            <Button type="button" variant="outline" onClick={refreshPage} disabled={loading}>
              <RefreshCw className={cn(loading && "animate-spin")} />
              {t("common.refresh")}
            </Button>
          </div>
        </div>
      </header>

      <main className="grid min-h-0 flex-1 lg:grid-cols-[320px_minmax(0,1fr)]">
        <aside className="min-h-0 border-b bg-muted/20 lg:sticky lg:top-0 lg:h-screen lg:self-start lg:border-r lg:border-b-0">
          <div className="border-b px-4 py-4">
            <div className="flex items-center justify-between gap-3">
              <div className="min-w-0">
                <h2 className="truncate text-sm font-semibold">{jobGroupTitle(jobGroup, t)}</h2>
                <p className="text-xs text-muted-foreground">{t("user.jobCount", { count: jobGroup?.jobs.length || (job ? 1 : 0) })}</p>
              </div>
              <Button type="button" variant="outline" size="icon" onClick={() => void loadJobGroup()} title={t("user.refresh")}>
                <RefreshCw />
              </Button>
            </div>
          </div>
          <div className="max-h-72 overflow-auto p-2 lg:max-h-[calc(100vh-133px)]">
            {groupJobs.map((item) => (
              <JobListItem key={item.id} job={item} selected={item.id === id} onOpen={() => openSibling(item.id)} />
            ))}
          </div>
        </aside>

        <section className="min-w-0 p-4 lg:p-6">
          <div className="mb-4 grid gap-3 border-b pb-4 md:grid-cols-3 xl:grid-cols-5">
            {job ? (
              <>
                <SummaryMetric label={t("common.runnerSpec")} value={job.runner_spec_name || t("user.matchedByLabels")} />
                <SummaryMetric label={t("user.workflowRun")} value={workflowRunLink(job)} />
                <SummaryMetric label={t("user.queued")} value={formatTime(job.created_at, locale)} />
                <SummaryMetric label={t("common.started")} value={formatTime(job.running_at, locale)} />
                <SummaryMetric label={t("user.finished")} value={formatTime(job.completed_at || job.failed_at, locale)} />
              </>
            ) : (
              <div className="text-sm text-muted-foreground">{t("common.loading")}</div>
            )}
          </div>
          <Tabs defaultValue="logs" className="h-full">
            <TabsList>
              <TabsTrigger value="logs">{t("user.runnerLogs")}</TabsTrigger>
              <TabsTrigger value="terminal">{t("common.terminal")}</TabsTrigger>
              <TabsTrigger value="details">{t("common.details")}</TabsTrigger>
            </TabsList>
            <TabsContent value="logs" className="mt-4">
              {job ? <RunnerEventTimeline endpoint={`${endpoint}/events`} request={request} autoRefresh={activeStatuses.has(job.status)} refreshRevision={eventRefreshRevision} /> : null}
            </TabsContent>
            <TabsContent value="terminal" forceMount className="mt-4 data-[state=inactive]:hidden">
              <Card className="gap-0 py-0">
                <CardHeader className="border-b px-5 py-4">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <div>
                      <CardTitle>{t("common.terminal")}</CardTitle>
                      <CardDescription>{job?.sandbox_id || t("user.noActiveSandbox")}</CardDescription>
                    </div>
                    <Button type="button" onClick={() => void connectTerminal()} disabled={!terminalAvailable || terminalConnecting || Boolean(terminalSession)}>
                      <SquareTerminal />
                      {terminalSession ? t("common.connected") : terminalConnecting ? t("common.connecting") : t("common.connect")}
                    </Button>
                  </div>
                </CardHeader>
                <CardContent className="p-0">
                  <div className="relative min-h-[520px] bg-[#111318] p-2">
                    <div ref={terminalEl} className="h-[500px] overflow-hidden rounded-md" />
                    {!terminalSession ? (
                      <div className="absolute inset-2 flex items-center justify-center rounded-md border border-white/10 bg-[#111318] text-sm text-slate-300">
                        {terminalAvailable ? t("user.connectInteractiveShell") : t("user.terminalActiveOnly")}
                      </div>
                    ) : null}
                  </div>
                  {terminalError ? <div className="border-t px-5 py-3 text-sm text-destructive">{terminalError}</div> : null}
                </CardContent>
              </Card>
            </TabsContent>
            <TabsContent value="details" className="mt-4">
              <Card>
                <CardHeader>
                  <CardTitle>{t("common.details")}</CardTitle>
                </CardHeader>
                <CardContent className="grid gap-2 text-sm">
                  {job
                    ? runnerJobDetailRows(job, t, locale).map((row) => (
                        <Detail key={row.id} label={row.label} value={row.value} />
                      ))
                    : t("common.loading")}
                </CardContent>
              </Card>
            </TabsContent>
          </Tabs>
        </section>
      </main>
    </div>
  )
}

function JobListItem({ job, selected, onOpen }: { job: RunnerState; selected: boolean; onOpen: () => void }) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className={cn(
        "mb-1 grid w-full grid-cols-[22px_minmax(0,1fr)] gap-2 rounded-md px-3 py-3 text-left text-sm transition-colors",
        selected ? "bg-background shadow-sm ring-1 ring-border" : "hover:bg-background/70"
      )}
    >
      <span className={cn("mt-0.5", jobStatusIconClass(job.status))}>{jobStatusIcon(job.status)}</span>
      <span className="min-w-0">
        <span className="flex min-w-0 items-center justify-between gap-2">
          <span className="truncate font-medium">{runnerJobTitle(job)}</span>
          <span className="shrink-0 text-xs text-muted-foreground">{formatRunnerDuration(job)}</span>
        </span>
        <span className="mt-1 block truncate text-xs text-muted-foreground">{job.runner_spec_name || job.runner_name || job.id}</span>
      </span>
    </button>
  )
}

function SummaryMetric({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="min-w-0">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className="mt-1 truncate text-sm font-medium">{value || "-"}</div>
    </div>
  )
}

function Detail({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="grid grid-cols-[140px_minmax(0,1fr)] gap-3 border-b py-2 last:border-b-0">
      <span className="text-muted-foreground">{label}</span>
      <span className="min-w-0 break-words font-medium">{value || "-"}</span>
    </div>
  )
}

function runnerJobTitle(job: RunnerState) {
  if (job.assigned_job_name && job.assigned_job_name !== "__runner_job_started__") return job.assigned_job_name
  return job.workflow_name || job.runner_name || job.id
}

function isTerminalAvailable(job: RunnerState) {
  return Boolean(job.sandbox_id && ["creating", "running", "stopping"].includes(job.status))
}

function jobGroupTitle(group: RunnerJobGroup | null, t: AppTFunction) {
  if (!group) return t("user.workflowJobs")
  return group.title ? t("user.namedJobs", { name: group.title }) : t("user.workflowJobs")
}

function jobStatusIcon(status: RunnerState["status"]) {
  switch (status) {
    case "completed":
      return <CheckCircle2 className="h-4 w-4" />
    case "failed":
      return <XCircle className="h-4 w-4" />
    case "running":
      return <Play className="h-4 w-4" />
    case "creating":
      return <Loader2 className="h-4 w-4 animate-spin" />
    case "stopping":
      return <Clock3 className="h-4 w-4" />
    default:
      return <Clock3 className="h-4 w-4" />
  }
}

function jobStatusIconClass(status: RunnerState["status"]) {
  switch (status) {
    case "completed":
      return "text-emerald-600"
    case "failed":
      return "text-destructive"
    case "running":
      return "text-blue-600"
    case "creating":
    case "stopping":
      return "text-amber-600"
    default:
      return "text-muted-foreground"
  }
}

function statusClass(status: RunnerState["status"]) {
  switch (status) {
    case "completed":
      return "bg-emerald-100 text-emerald-700 dark:bg-emerald-500/15 dark:text-emerald-300"
    case "failed":
      return "bg-red-100 text-red-700 dark:bg-red-500/15 dark:text-red-300"
    case "running":
      return "bg-blue-100 text-blue-700 dark:bg-blue-500/15 dark:text-blue-300"
    default:
      return "bg-muted text-muted-foreground"
  }
}
