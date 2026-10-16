import { deploymentCreatePath, deploymentListAPIPath, deploymentListPath, deploymentPath, githubContextFromPath, githubContextPath } from "@/github-context-routes"
import { ArrowLeft, CalendarDays, Copy, GitBranch, GitCommitHorizontal, ExternalLink, Loader2, Plus, RefreshCw, Square, Trash2, RotateCcw } from "lucide-react"
import { lazy, Suspense, useCallback, useEffect, useRef, useState, type ReactNode } from "react"
import { useTranslation } from "react-i18next"

import { formatTime as formatEventTime } from "@/admin-format"
import { DeploymentNavigation } from "@/components/deployment-navigation"
import { DeploymentStatus } from "@/components/deployment-status"
import { activeDeploymentStatuses as activeStatuses, deploymentListStatuses, deploymentListStatusFromSearch, type DeploymentListStatus, type DeploymentStatusCounts } from "@/deployment-status"
import { AppLink } from "@/components/app-link"
import { useSandboxRegions } from "@/components/sandbox-catalog-utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { DetailField } from "@/components/ui/detail-field"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { ToggleGroup, ToggleGroupItem } from "@/components/ui/toggle-group"
import { cn } from "@/lib/utils"
import { toast } from "sonner"

const DeploymentTerminal = lazy(() => import("@/components/deployment-terminal"))

export type Deployment = {
  id: string
  github_installation_id: number
  repository_full_name: string
  ref?: string
  source?: "branch" | "tag" | "pull_request"
  pull_request_number?: number
  pull_request_title?: string
  source_repository_full_name?: string
  source_ref?: string
  commit_sha: string
  config_path: string
  status: string
  template_id?: string
  template_name?: string
  template_cpu?: number
  template_memory_mb?: number
  template_disk_mb?: number
  sandbox_id?: string
  sandbox_region?: string
  endpoint?: string
  endpoint_access?: string
  failure_stage?: string
  failure_reason?: string
  expires_at: string
  created_at: string
  updated_at: string
  last_successful_revision_number?: number
  revision_number?: number
  revisions?: DeploymentRevision[]
  revisions_has_more?: boolean
  selected_revision?: DeploymentRevision
}

export type DeploymentRevision = {
  id: string
  number: number
  status: string
  ref?: string
  commit_sha: string
  source_repository_full_name?: string
  pull_request_title?: string
  config_path: string
  template_id?: string
  template_name?: string
  template_cpu?: number
  template_memory_mb?: number
  template_disk_mb?: number
  sandbox_id?: string
  sandbox_region?: string
  endpoint?: string
  failure_stage?: string
  failure_reason?: string
  expires_at: string
  created_at: string
  updated_at: string
}

type DeploymentEvent = {
  id: number
  event_type: string
  stage?: string
  stream?: string
  message: string
  created_at: string
}

type Request = (url: string, options?: RequestInit) => Promise<unknown>

type EventPage = { events?: DeploymentEvent[]; has_more?: boolean }
type DeploymentPage = { items?: Deployment[]; total?: number; status_counts?: DeploymentStatusCounts }

const statusFilterLabels = { running: "common.statusRunning", processing: "user.deploymentStatusProcessing", stopped: "user.deploymentStatusStopped", failed: "common.statusFailed" } as const

type DeploymentResources = Pick<Deployment, "template_id" | "template_name" | "template_cpu" | "template_memory_mb" | "template_disk_mb" | "sandbox_id" | "sandbox_region" | "expires_at"> & { remote_error?: string }

export function UserDeploymentsSection({
  currentLogin,
  request,
  deploymentID,
  locationPath = window.location.pathname,
  locationSearch = window.location.search,
  onOpenDeployment,
}: {
  currentLogin?: string
  request: Request
  deploymentID?: string
  locationPath?: string
  locationSearch?: string
  onOpenDeployment: (id: string | null) => void
}) {
  const { t, i18n } = useTranslation()
  const [deployments, setDeployments] = useState<Deployment[]>([])
  const selectedID = deploymentID ?? null
  const context = githubContextFromPath(locationPath, locationSearch)
  const sourceListPath = deploymentListAPIPath(context)
  const statusFilter = deploymentListStatusFromSearch(locationSearch)
  const [statusCounts, setStatusCounts] = useState<DeploymentStatusCounts | null>(null)
  const listPath = deploymentListAPIPath(context, statusFilter)
  const listCanonicalPath = !deploymentID ? deploymentListPath(context, statusFilter) : null
  const selectStatus = (value: string) => {
    if (!value) return
    window.history.pushState(null, "", deploymentListPath(context, value as DeploymentListStatus))
    window.dispatchEvent(new window.PopStateEvent("popstate"))
  }
  useEffect(() => {
    if (!listCanonicalPath || window.location.pathname + window.location.search === listCanonicalPath) return
    window.history.replaceState(null, "", listCanonicalPath)
    window.dispatchEvent(new window.PopStateEvent("popstate"))
  }, [listCanonicalPath])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [action, setAction] = useState<string | null>(null)
  const [navigationRefresh, setNavigationRefresh] = useState(0)
  const [hasMore, setHasMore] = useState(false)
  const [loadingOlder, setLoadingOlder] = useState(false)
  const loadedPages = useRef(1)
  const listPageLoading = useRef(false)
  const viewKey = deploymentID || listPath
  const contentRef = useRef<HTMLElement>(null)
  const generation = useRef(0)
  const selectedDeployment = deployments.find((item) => item.id === selectedID) ?? null
  const [revisionSelection, setRevisionSelection] = useState<{ id: string; number?: number } | null>(null)
  const requestedRevision = revisionSelection?.id === selectedID ? revisionSelection.number : revisionNumberFromURL()
  const selectedRevisionNumber = requestedRevision ?? selectedDeployment?.revision_number
  const selectRevision = useCallback((number: number) => {
    if (!selectedID) return
    setRevisionSelection({ id: selectedID, number: number === selectedDeployment?.revision_number ? undefined : number })
    const url = new URL(window.location.href)
    if (number === selectedDeployment?.revision_number) url.searchParams.delete("revision")
    else url.searchParams.set("revision", String(number))
    window.history.pushState(null, "", url)
  }, [selectedID, selectedDeployment?.revision_number])
  useEffect(() => {
    const onHistory = () => setRevisionSelection({ id: selectedID ?? "", number: revisionNumberFromURL() })
    window.addEventListener("popstate", onHistory)
    return () => window.removeEventListener("popstate", onHistory)
  }, [selectedID])
  const selectedRevision = selectedDeployment?.revisions?.find((item) => item.number === selectedRevisionNumber) ?? (selectedDeployment?.selected_revision && selectedDeployment.selected_revision.number === selectedRevisionNumber ? selectedDeployment.selected_revision : null)
  useEffect(() => {
    if (!selectedDeployment || (requestedRevision && !selectedRevision)) return
    const canonical = deploymentPath(selectedDeployment, requestedRevision ? selectedRevision : null, requestedRevision)
    if (window.location.pathname + window.location.search === canonical) return
    window.history.replaceState(null, "", canonical)
    window.dispatchEvent(new window.PopStateEvent("popstate"))
  }, [selectedDeployment, selectedRevision, requestedRevision])
  const backToDeployments = () => {
    const source = selectedDeployment ? { kind: "repository" as const, repository: selectedDeployment.repository_full_name, reference: "" } : context
    if (!source) { onOpenDeployment(null); return }
    window.history.pushState(null, "", githubContextPath(source, "deployments"))
    window.dispatchEvent(new window.PopStateEvent("popstate"))
  }
  const deploymentEvents = useDeploymentEvents(request, selectedDeployment ? selectedID : null, selectedRevisionNumber, selectedRevision?.status ?? selectedDeployment?.status, currentLogin)
  const selectedDeploymentID = selectedDeployment?.id
  const selectedPullRequest = selectedDeployment?.pull_request_number || Number(selectedDeployment?.ref?.match(/^refs\/pull\/(\d+)\/head$/)?.[1]) || undefined
  const selectedPullRequestTitle = selectedDeployment?.pull_request_title
  useEffect(() => {
    if (!selectedDeploymentID || !selectedPullRequest || selectedPullRequestTitle?.trim()) return
    let cancelled = false
    void request(`/user/deployments/${encodeURIComponent(selectedDeploymentID)}/pull-request`)
      .then((value) => {
        const result = value as { pull_request_number?: number; pull_request_title?: string }
        if (cancelled || result.pull_request_number !== selectedPullRequest || !result.pull_request_title?.trim()) return
        const title = result.pull_request_title.trim()
        setDeployments((current) => current.map((item) => item.id === selectedDeploymentID
          ? { ...item, pull_request_title: item.pull_request_title || title }
          : item))
      })
      .catch(() => {
        // Optional GitHub metadata must not hide persisted details or logs.
      })
    return () => { cancelled = true }
  }, [request, selectedDeploymentID, selectedPullRequest, selectedPullRequestTitle, currentLogin, navigationRefresh])
  const [deploymentResources, setDeploymentResources] = useState<DeploymentResources | null>(null)
  const [deploymentResourcesLoading, setDeploymentResourcesLoading] = useState(false)

  useEffect(() => {
    if (!selectedDeploymentID) {
      setDeploymentResources(null)
      setDeploymentResourcesLoading(false)
      return
    }
    let cancelled = false
    setDeploymentResources(null)
    setDeploymentResourcesLoading(true)
    const revisionQuery = selectedDeployment?.revision_number ? `?revision=${selectedDeployment.revision_number}` : ""
    void request(`/user/deployments/${encodeURIComponent(selectedDeploymentID)}/resources${revisionQuery}`)
      .then((value) => {
        if (!cancelled) setDeploymentResources(value as DeploymentResources)
      })
      .catch(() => {
        // Provider reads are best effort. The detail view still renders the
        // immutable local snapshot when the Sandbox API is unavailable.
      })
      .finally(() => {
        if (!cancelled) setDeploymentResourcesLoading(false)
      })
    return () => { cancelled = true }
  }, [request, selectedDeploymentID, selectedDeployment?.revision_number])

  const formatTime = useCallback((value?: string) => {
    if (!value) return "—"
    const parsed = new Date(value)
    if (Number.isNaN(parsed.getTime())) return value
    return new Intl.DateTimeFormat(i18n.resolvedLanguage === "zh" ? "zh-CN" : "en", {
      dateStyle: "medium",
      timeStyle: "short",
    }).format(parsed)
  }, [i18n.resolvedLanguage])

  const loadDeployments = useCallback(async () => {
    if (listPageLoading.current) return
    const requestGeneration = ++generation.current
    setLoading(true)
    setError(null)
    try {
      const paths = deploymentID
        ? [`/user/deployments/${encodeURIComponent(deploymentID)}${requestedRevision ? `?revision=${requestedRevision}` : ""}`]
        : Array.from({ length: loadedPages.current }, (_, index) => index === 0 ? listPath : `${listPath}${listPath.includes("?") ? "&" : "?"}limit=100&offset=${index * 100}`)
      const pages = await Promise.all(paths.map((path) => request(path) as Promise<DeploymentPage & Deployment>))
      if (requestGeneration !== generation.current) return
      const next = deploymentID ? [pages[0]] : [...new Map(pages.flatMap((data) => Array.isArray(data.items) ? data.items : []).map((item) => [item.id, item])).values()]
      setHasMore(!deploymentID && (pages[0].total ?? next.length) > next.length)
      if (!deploymentID) setStatusCounts(pages[0].status_counts ?? null)
      setDeployments((current) => next.map((item) => {
        const previous = current.find((old) => old.id === item.id)
        // A detail response started before metadata was saved may still be
        // missing the title. Retain it only for the same immutable source/version.
        const snapshot = !item.pull_request_title && previous?.ref === item.ref && previous?.revision_number === item.revision_number && previous?.pull_request_title
          ? { ...item, pull_request_title: previous.pull_request_title }
          : item
        if (!previous?.revisions?.length || !item.revisions?.length) return snapshot
        const merged = new Map(previous.revisions.map((revision) => [revision.number, revision]))
        item.revisions.forEach((revision) => merged.set(revision.number, revision))
        return { ...snapshot, revisions: [...merged.values()].sort((a, b) => b.number - a.number), revisions_has_more: previous.revisions.length > item.revisions.length ? previous.revisions_has_more : item.revisions_has_more }
      }))
    } catch (cause) {
      if (requestGeneration !== generation.current) return
      setError(cause instanceof Error ? cause.message : t("user.deploymentLoadFailed"))
    } finally {
      if (requestGeneration === generation.current) setLoading(false)
    }
  }, [request, t, deploymentID, requestedRevision, listPath])

  useEffect(() => {
    if (contentRef.current) contentRef.current.scrollTop = 0
    loadedPages.current = 1
    listPageLoading.current = false
    setDeployments([])
    setHasMore(false)
    setLoadingOlder(false)
  }, [viewKey, currentLogin])

  useEffect(() => { setStatusCounts(null) }, [sourceListPath, currentLogin])

  useEffect(() => {
    void loadDeployments()
    return () => {
      generation.current += 1
      listPageLoading.current = false
      setLoadingOlder(false)
    }
  }, [loadDeployments, currentLogin])

  useEffect(() => {
    if (deploymentID && !deployments.some((item) => activeStatuses.has(item.status))) return
    const timer = window.setInterval(() => { void loadDeployments() }, deploymentID ? 3000 : 5000)
    return () => {
      window.clearInterval(timer)
    }
  }, [loadDeployments, deployments, deploymentID])

  const loadOlderDeployments = async () => {
    if (loadingOlder || loading || !hasMore) return
    const epoch = generation.current
    listPageLoading.current = true
    setLoadingOlder(true)
    try {
      const page = await request(`${listPath}${listPath.includes("?") ? "&" : "?"}limit=100&offset=${loadedPages.current * 100}`) as DeploymentPage
      if (epoch !== generation.current) return
      loadedPages.current += 1
      setDeployments((current) => [...new Map([...current, ...(page.items ?? [])].map((item) => [item.id, item])).values()])
      setHasMore((page.total ?? 0) > loadedPages.current * 100)
      setStatusCounts(page.status_counts ?? null)
    } catch (cause) {
      if (epoch === generation.current) setError(cause instanceof Error ? cause.message : t("user.deploymentLoadFailed"))
    } finally {
      if (epoch === generation.current) { listPageLoading.current = false; setLoadingOlder(false) }
    }
  }

  const loadOlderRevisions = async () => {
    if (!selectedDeployment || !selectedDeployment.revisions_has_more) return
    const id = selectedDeployment.id
    const epoch = generation.current
    const last = selectedDeployment.revisions?.[selectedDeployment.revisions.length - 1]?.number
    if (!last) return
    try {
      const page = await request(`/user/deployments/${encodeURIComponent(id)}/revisions?before_number=${last}`) as { revisions: DeploymentRevision[]; has_more: boolean }
      if (epoch !== generation.current) return
      setDeployments((current) => current.map((item) => item.id === id ? { ...item, revisions: [...(item.revisions ?? []), ...page.revisions], revisions_has_more: page.has_more } : item))
    } catch (cause) {
      if (epoch === generation.current) setError(cause instanceof Error ? cause.message : t("user.deploymentLoadFailed"))
    }
  }

  async function runAction(kind: "stop" | "delete" | "retry", retryOptions?: { mode: "latest" | "revision"; revision_number?: number }) {
    if (!selectedDeployment || action) return
    setAction(kind)
    try {
      const path = `/user/deployments/${encodeURIComponent(selectedDeployment.id)}`
      const result = await request(kind === "delete" ? path : `${path}/${kind}`, { method: kind === "delete" ? "DELETE" : "POST", ...(kind === "retry" ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(retryOptions ?? { mode: "revision" }) } : {}) }) as Deployment
      if (kind === "retry") { setRevisionSelection({ id: result.id, number: result.revision_number }); const url = new URL(window.location.href); url.searchParams.delete("revision"); window.history.replaceState(null, "", url) }
      setDeployments((current) => kind === "delete" ? current.filter((item) => item.id !== selectedDeployment.id) : current.map((item) => item.id === result.id ? { ...result, revisions: item.revisions, revisions_has_more: item.revisions_has_more } : item))
      if (kind !== "delete") void loadDeployments()
      if (kind === "delete" && deploymentID) backToDeployments()
      setNavigationRefresh((value) => value + 1)
      toast.success(t(`user.deployment${kind[0].toUpperCase()}${kind.slice(1)}d` as "user.deploymentStopped" | "user.deploymentDeleted" | "user.deploymentRetried"))
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : t("user.deploymentActionFailed"))
    } finally {
      setAction(null)
    }
  }

  const deploymentContent = <>
    {error ? <div className="rounded-lg border border-destructive/40 bg-destructive/5 p-4 text-sm text-destructive">{error}</div> : null}
    {loading && deployments.length === 0 ? <div className="flex items-center gap-2 rounded-lg border p-6 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" />{t("user.loadingDeployments")}</div> : deployments.length === 0 ? <div className="rounded-lg border border-dashed p-8 text-center"><h2 className="font-semibold">{t(!deploymentID ? "user.deploymentStatusEmpty" : "user.noDeployments")}</h2><p className="mt-1 text-sm text-muted-foreground">{t(!deploymentID ? "user.deploymentStatusEmptyDescription" : "user.noDeploymentsDescription")}</p></div> : (
      <div className="space-y-4">
        {!deploymentID ? <div className="divide-y border-y" data-testid="deployment-list">
          {deployments.map((deployment) => <AppLink key={deployment.id} href={deploymentPath(deployment, deployment.selected_revision)} className="group relative flex w-full items-start gap-3 bg-background/60 px-4 py-4 transition-colors hover:bg-accent/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring">
            <span aria-hidden="true" className={cn("absolute inset-y-0 left-0 w-1", deployment.status === "failed" ? "bg-destructive" : deployment.status === "running" ? "bg-emerald-500" : activeStatuses.has(deployment.status) ? "bg-yellow-500" : "bg-muted-foreground/40")} />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center justify-between gap-2"><span className="min-w-0 break-all text-sm font-semibold">{deployment.repository_full_name}</span><DeploymentStatus status={deployment.status} /></div>
              <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-xs text-muted-foreground">
                <code title={deployment.id}>{deployment.id.slice(0, 10)}</code>
                <span className="inline-flex min-w-0 items-center gap-1"><GitBranch className="h-3.5 w-3.5 shrink-0" /><span className="break-all">{displayRevisionRef(deployment.ref) || "—"}</span></span>
                <span className="inline-flex items-center gap-1"><GitCommitHorizontal className="h-3.5 w-3.5" /><code>{deployment.commit_sha.slice(0, 8)}</code></span>
                <span className="inline-flex items-center gap-1"><CalendarDays className="h-3.5 w-3.5" />{formatTime(deployment.created_at)}</span>
              </div>
            </div>
          </AppLink>)}
        </div> : null}
        {!deploymentID && hasMore ? <Button type="button" variant="outline" disabled={loadingOlder || loading} onClick={() => void loadOlderDeployments()}>{loadingOlder ? t("common.loading") : t("user.deploymentMore")}</Button> : null}
        {selectedDeployment ? <DeploymentDetail key={selectedDeployment.id} request={request} selectedRevisionNumber={selectedRevisionNumber} deployment={selectedDeployment} selectedRevision={selectedRevision} resources={deploymentResources} resourcesLoading={deploymentResourcesLoading} onSelectRevision={selectRevision} onLoadOlderRevisions={loadOlderRevisions} eventState={deploymentEvents} formatTime={formatTime} action={action} onAction={runAction} /> : null}
      </div>
    )}
  </>

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-y-auto xl:overflow-hidden">
      <section className="shrink-0 border-b bg-muted/35 px-4 py-4 lg:px-6">
        <div className="flex w-full flex-wrap items-center justify-between gap-3">
          <div className="flex min-w-0 items-center gap-3">
            {deploymentID ? <Button type="button" variant="outline" size="icon" className="xl:hidden" aria-label={t("user.deploymentBack")} onClick={backToDeployments}><ArrowLeft /></Button> : null}
            <div>
              <h1 className="text-xl font-semibold">{deploymentID ? t("user.deploymentDetailTitle") : t("user.deployments")}</h1>
              <p className="mt-1 max-w-2xl text-sm text-muted-foreground">{t("user.deploymentListDescription")}</p>
            </div>
          </div>
          <div className="flex shrink-0 flex-wrap gap-2">
            {!deploymentID ? <Button asChild><AppLink href={deploymentCreatePath(context)}><Plus className="h-4 w-4" />{t("user.createDeployment")}</AppLink></Button> : null}
            <Button type="button" variant="outline" onClick={() => { void loadDeployments(); setNavigationRefresh((value) => value + 1) }} disabled={loading}>
              <RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />
              {t("common.refresh")}
            </Button>
          </div>
        </div>
      </section>

      <div className="grid min-h-0 flex-1 xl:grid-cols-[360px_minmax(0,1fr)] xl:overflow-hidden" data-testid="deployment-layout">
        <DeploymentNavigation key={currentLogin} request={request} selected={selectedDeployment} selectedID={deploymentID} repository={context?.repository} refreshVersion={navigationRefresh} />
      <section ref={contentRef} className="min-h-0 w-full min-w-0 space-y-4 p-4 lg:p-6 xl:overflow-y-auto">
        <div className="min-w-0 space-y-4">
          {!deploymentID ? <div className="flex flex-wrap items-center justify-between gap-3"><h2 className="min-w-0 break-all text-2xl font-semibold">{context?.repository || t("user.deploymentAll")}</h2>{context && context.kind !== "repository" ? <Badge variant="secondary">{context.kind === "pulls" ? t("user.deploymentPullRequestNumber", { number: Number(context.reference) }) : context.name || context.reference}</Badge> : null}</div> : null}
          {!deploymentID ? <div className="flex flex-wrap items-center gap-3" data-testid="deployment-status-filter">
            <ToggleGroup type="single" role="radiogroup" value={statusFilter} onValueChange={selectStatus} className="grid w-full grid-cols-2 sm:inline-flex sm:w-fit" aria-label={t("user.deploymentStatusFilter")}>
              {deploymentListStatuses.map((status) => <ToggleGroupItem key={status} value={status}>
                {t(statusFilterLabels[status])}{statusCounts ? <Badge variant="secondary">{statusCounts[status]}</Badge> : null}
              </ToggleGroupItem>)}
            </ToggleGroup>
          </div> : null}
          {deploymentContent}
        </div>
      </section>
      </div>
    </div>
  )
}

function DeploymentDetail({ request, selectedRevisionNumber, deployment, selectedRevision, resources, resourcesLoading, onSelectRevision, onLoadOlderRevisions, eventState, formatTime, action, onAction }: {
  request: Request
  selectedRevisionNumber?: number
  deployment: Deployment
  selectedRevision: DeploymentRevision | null
  resources: DeploymentResources | null
  resourcesLoading: boolean
  onSelectRevision: (number: number) => void
  onLoadOlderRevisions: () => void
  eventState: ReturnType<typeof useDeploymentEvents>
  formatTime: (value?: string) => string
  action: string | null
  onAction: (kind: "stop" | "delete" | "retry", retryOptions?: { mode: "latest" | "revision"; revision_number?: number }) => void
}) {
  const { t, i18n } = useTranslation()
  const sandboxRegions = useSandboxRegions()
  const { events, loading, error, hasOlder, refresh, loadOlder } = eventState
  const canStop = activeStatuses.has(deployment.status)
  const canRetry = ["failed", "completed", "expired"].includes(deployment.status)
  const terminalAvailable = deployment.status === "running" && Boolean(deployment.sandbox_id)
  const viewingHistory = Boolean(selectedRevisionNumber && selectedRevisionNumber !== deployment.revision_number)
  const [activeTab, setActiveTab] = useState("logs")
  const viewRevision = (number: number) => { onSelectRevision(number); setActiveTab("logs") }
  const revisions = deployment.revisions ?? []
  const [redeployOpen, setRedeployOpen] = useState(false)
  const [redeployMode, setRedeployMode] = useState<"latest" | "revision">(deployment.source === "branch" || deployment.source === "pull_request" ? "latest" : "revision")
  const [redeployRevision, setRedeployRevision] = useState(selectedRevision?.number ?? deployment.revision_number ?? revisions[0]?.number ?? 1)
  const resourceSnapshot = {
    ...deployment,
    ...(resources?.template_id ? { template_id: resources.template_id } : {}),
    ...(resources?.template_name ? { template_name: resources.template_name } : {}),
    ...(resources?.template_cpu ? { template_cpu: resources.template_cpu } : {}),
    ...(resources?.template_memory_mb ? { template_memory_mb: resources.template_memory_mb } : {}),
    ...(resources?.template_disk_mb ? { template_disk_mb: resources.template_disk_mb } : {}),
    ...(resources?.sandbox_id ? { sandbox_id: resources.sandbox_id } : {}),
    ...(resources?.sandbox_region ? { sandbox_region: resources.sandbox_region } : {}),
    ...(resources?.expires_at ? { expires_at: resources.expires_at } : {}),
  }
  const sourcePullRequest = deployment.pull_request_number || Number(deployment.ref?.match(/^refs\/pull\/(\d+)\/head$/)?.[1])
  const sourceLabel = sourcePullRequest
    ? t("user.deploymentPullRequest")
    : t(deployment.source === "tag" || deployment.ref?.startsWith("refs/tags/") ? "user.deploymentTag" : "user.deploymentBranch")
  const sourceValue = sourcePullRequest ? `#${sourcePullRequest}` : displayRevisionRef(deployment.ref) || deployment.commit_sha.slice(0, 8)
  const sourcePullRequestTitle = deployment.pull_request_title?.trim()
  const sourceTitle = sourcePullRequest && sourcePullRequestTitle ? ` · ${sourcePullRequestTitle}` : ""
  return <div className="space-y-4">
    <Card className="gap-4 rounded-none border-0 border-b bg-transparent pb-4 pt-0 shadow-none">
      <CardHeader className="space-y-2 px-0">
        <CardDescription className="break-words text-sm font-medium text-foreground">{deployment.repository_full_name}</CardDescription>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <div className="w-full min-w-0 sm:w-auto sm:flex-1">
            <CardTitle className="inline break-words text-2xl leading-snug"><DeploymentSourceLink deployment={deployment}>{sourceLabel} {sourceValue}{sourceTitle}</DeploymentSourceLink></CardTitle>
            <span className="ml-2 inline-flex align-middle"><DeploymentStatus status={deployment.status} /></span>
          </div>
          <div className="flex shrink-0 flex-wrap items-center gap-2">
            <Button type="button" size="sm" variant="outline" disabled={!canStop || Boolean(action)} onClick={() => onAction("stop")}><Square className="h-3.5 w-3.5" />{t("user.stopDeployment")}</Button>
            <Button type="button" size="sm" disabled={!canRetry || Boolean(action)} onClick={() => { setRedeployMode(deployment.source === "branch" || deployment.source === "pull_request" ? "latest" : "revision"); setRedeployRevision(deployment.revision_number ?? revisions[0]?.number ?? 1); setRedeployOpen(true) }}><RotateCcw className="h-3.5 w-3.5" />{t("user.retryDeployment")}</Button>
            <Button type="button" size="sm" variant="outline" disabled={Boolean(action)} onClick={() => onAction("delete")}><Trash2 className="h-3.5 w-3.5" />{t("common.delete")}</Button>
          </div>
        </div>
        {deployment.source_repository_full_name ? <p className="text-sm text-muted-foreground">{deployment.source_repository_full_name}:{deployment.source_ref}</p> : null}
        <div className="flex flex-wrap items-center gap-x-5 gap-y-2 text-xs text-muted-foreground">
          <span>{t("user.deploymentCurrentRevision", { number: deployment.revision_number ?? 1 })}</span>
          <DeploymentCommit repository={deployment.source_repository_full_name || deployment.repository_full_name} sha={deployment.commit_sha} />
          <span>{t("user.deploymentCreatedAt")} {formatTime(deployment.created_at)}</span>
          <span>{t("user.deploymentExpires")} {formatTime(resourceSnapshot.expires_at)}</span>
          {deployment.last_successful_revision_number ? <span>
            {t("user.deploymentLastSuccessful")} <button type="button" className="text-primary hover:underline" onClick={() => viewRevision(deployment.last_successful_revision_number!)}>{t("user.deploymentRevisionShortNumber", { number: deployment.last_successful_revision_number })}</button>
          </span> : null}
        </div>
      </CardHeader>
      {deployment.endpoint ? <CardContent className="px-0">
        <DeploymentEndpoint endpoint={deployment.endpoint} isPublic={deployment.endpoint_access === "public"} />
      </CardContent> : null}
    </Card>
    <section aria-label={t("user.deploymentResources")} aria-busy={resourcesLoading} className="border-b pb-4 text-sm">
      {resourcesLoading ? <div role="status" className="mb-2"><Loader2 className="h-4 w-4 animate-spin text-muted-foreground" aria-label={t("common.loading")} /></div> : null}
      <div className="grid gap-3 sm:grid-cols-3">
        <DetailField label={t("user.deploymentTemplate")} value={resourceSnapshot.template_name || resourceSnapshot.template_id || "—"} mono={!resourceSnapshot.template_name} />
        <DetailField label={t("user.deploymentSandboxRegion")} value={sandboxRegions.find((region) => region.id === resourceSnapshot.sandbox_region)?.label || resourceSnapshot.sandbox_region || "—"} />
        <DetailField label={t("user.deploymentSandbox")} value={resourceSnapshot.sandbox_id || "—"} mono />
        <DetailField label={t("user.deploymentCPU")} value={resourceSnapshot.template_cpu ? `${resourceSnapshot.template_cpu} vCPU` : "—"} />
        <DetailField label={t("user.deploymentMemory")} value={resourceSnapshot.template_memory_mb ? `${resourceSnapshot.template_memory_mb} MB` : "—"} />
        <DetailField label={t("user.deploymentDisk")} value={resourceSnapshot.template_disk_mb ? `${resourceSnapshot.template_disk_mb} MB` : "—"} />
      </div>
    </section>
    <div className="min-w-0" data-testid="deployment-workspace">
      <Tabs value={activeTab} onValueChange={setActiveTab} className="min-w-0 gap-0" data-testid="deployment-output">
        <TabsList variant="line">
          <TabsTrigger variant="line" value="logs">{t("user.deploymentEvents")}</TabsTrigger>
          <TabsTrigger variant="line" value="terminal">{t("common.terminal")}</TabsTrigger>
          <TabsTrigger variant="line" value="history">{t("user.deploymentHistory")}</TabsTrigger>
        </TabsList>
        <TabsContent value="logs" className="m-0 space-y-2 pt-2">
          {viewingHistory ? <div className="flex flex-wrap items-center justify-between gap-2 rounded-md border bg-muted/30 px-3 py-2 text-sm" data-testid="deployment-historical-notice">
            <span>{t("user.deploymentViewingHistory", { number: selectedRevisionNumber! })}</span>
            <Button type="button" variant="outline" size="sm" onClick={() => viewRevision(deployment.revision_number!)}>{t("user.deploymentReturnCurrent")}</Button>
          </div> : null}
      <Card className="min-w-0 gap-0 rounded-none border-0 bg-transparent py-0 shadow-none">
        <CardHeader className="px-0 pb-2 pt-0">
          <div className="flex flex-wrap items-center justify-between gap-3">
            {selectedRevision ? <CardDescription className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <span>{t("user.deploymentRevisionNumber", { number: selectedRevision.number })}</span>
              {selectedRevision.ref ? <><span aria-hidden="true">·</span><span>{t(selectedRevision.ref.startsWith("refs/pull/") ? "user.deploymentPullRequest" : selectedRevision.ref.startsWith("refs/tags/") ? "user.deploymentTag" : "user.deploymentBranch")} <code>{displayRevisionRef(selectedRevision.ref)}</code></span></> : null}
              <span aria-hidden="true">·</span><DeploymentCommit repository={selectedRevision.source_repository_full_name || deployment.repository_full_name} sha={selectedRevision.commit_sha} />
            </CardDescription> : null}
            <div className="ml-auto flex shrink-0 items-center gap-3 text-xs text-muted-foreground">
              <Button type="button" variant="outline" size="sm" disabled={loading} onClick={() => void refresh()}><RefreshCw className={cn("h-4 w-4", loading && "animate-spin")} />{t("common.refresh")}</Button>
              <span>{t("user.deploymentLogLines", { count: events.length })}</span>
            </div>
          </div>
        </CardHeader>
        <CardContent className="space-y-2 px-0">
          {selectedRevision?.failure_reason ? <p className="rounded-md border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive">{selectedRevision.failure_stage || t("user.deploymentFailure")}: {selectedRevision.failure_reason}</p> : null}
          {error ? <p role="alert" className="text-sm text-destructive">{t("user.deploymentEventsFailed")}: {error}</p> : null}
          {hasOlder ? <Button type="button" variant="outline" size="sm" disabled={loading} onClick={() => void loadOlder()}>{t("user.deploymentEventsOlder")}</Button> : null}
          <div data-testid="deployment-logs" className="deployment-console grid rounded-lg border bg-muted/20 px-4 py-2 sm:grid-cols-[max-content_max-content_minmax(0,1fr)] sm:gap-x-3" aria-busy={loading}>
            {events.length ? events.map((event) => <div data-testid="deployment-log-line" key={event.id} className="relative grid gap-1 border-l py-0.5 pl-5 transition-colors hover:bg-muted/50 sm:col-span-full sm:grid-cols-subgrid sm:gap-3">
              <span aria-hidden="true" className={cn("absolute -left-1 top-2 size-2 rounded-full ring-4 ring-background", event.event_type === "failure" ? "bg-destructive" : event.stream === "stderr" ? "bg-amber-500" : event.stream === "stdout" ? "bg-slate-400" : "bg-sky-500")} />
              <time dateTime={event.created_at} className="whitespace-nowrap font-mono text-[11px] text-muted-foreground">{formatEventTime(event.created_at, i18n.resolvedLanguage, { fractionalSecondDigits: 3 })}</time>
              <span className="whitespace-nowrap font-mono text-[11px] text-muted-foreground">{event.stream || event.event_type}{event.stage ? ` · ${event.stage}` : ""}</span>
              <pre className={cn("min-w-0 whitespace-pre-wrap break-words font-mono text-xs leading-normal", event.event_type === "failure" && "text-destructive")}>{event.message.trimEnd()}</pre>
            </div>)
            : <p className="col-span-full py-8 text-center text-sm text-muted-foreground">{loading ? t("common.loading") : t("user.deploymentNoEvents")}</p>}
          </div>
        </CardContent>
      </Card>
        </TabsContent>
        <TabsContent value="terminal" className="m-0 space-y-2 pt-2">
          <p className="text-xs text-muted-foreground">{t("user.deploymentCurrentRevision", { number: deployment.revision_number ?? 1 })}</p>
          {terminalAvailable ? <Suspense fallback={<p className="p-5 text-sm text-muted-foreground">{t("common.loading")}</p>}>
            <DeploymentTerminal key={`${deployment.id}:${deployment.revision_number}:${deployment.sandbox_id}:${selectedRevisionNumber}`} endpoint={`/user/deployments/${encodeURIComponent(deployment.id)}/revisions/${deployment.revision_number}`} request={request} />
          </Suspense> : <div className="bg-muted/20 px-4 py-12 text-center text-sm text-muted-foreground">{t("user.deploymentTerminalUnavailable")}</div>}
        </TabsContent>
        <TabsContent value="history" className="m-0 space-y-2 py-5" data-testid="deployment-history">
          <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-muted-foreground">
            <p>{t("user.deploymentHistoryDescription")}</p>
            <span>{t("user.deploymentHistoryCount", { count: revisions.length })}</span>
          </div>
          <div className="divide-y border-y">
            {revisions.map((revision) => <div key={revision.id} className={cn("flex flex-wrap items-center justify-between gap-3 border-l-2 px-4 py-3", revision.number === deployment.revision_number ? "border-primary bg-primary/5" : "border-transparent")}>
              <div className="min-w-0 space-y-2">
                <div className="flex flex-wrap items-center gap-2 text-sm">
                  <strong>{t("user.deploymentRevisionNumber", { number: revision.number })}</strong>
                  <DeploymentStatus status={revision.status} />
                  {revision.number === deployment.revision_number ? <Badge variant="secondary">{t("user.deploymentCurrent")}</Badge> : null}
                </div>
                <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
                  <span>{displayRevisionRef(revision.ref)}</span>
                  <DeploymentCommit repository={revision.source_repository_full_name || deployment.repository_full_name} sha={revision.commit_sha} />
                  <span>{formatTime(revision.created_at)}</span>
                </div>
              </div>
              <div className="flex flex-wrap gap-2">
                <Button type="button" variant="outline" size="sm" onClick={() => viewRevision(revision.number)}>{t("user.deploymentViewRevisionLogs", { number: revision.number })}</Button>
                <Button type="button" variant="outline" size="sm" disabled={!canRetry || Boolean(action)} onClick={() => { setRedeployMode("revision"); setRedeployRevision(revision.number); setRedeployOpen(true) }}>{t("user.deploymentRedeployRevision")}</Button>
              </div>
            </div>)}
          </div>
          {deployment.revisions_has_more ? <Button type="button" variant="outline" size="sm" onClick={onLoadOlderRevisions}>{t("user.deploymentHistoryOlder")}</Button> : null}
        </TabsContent>
      </Tabs>
    </div>
    <Dialog open={redeployOpen} onOpenChange={(open) => !action && setRedeployOpen(open)}>
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{t("user.redeployChooseTitle")}</DialogTitle>
          <DialogDescription>{t("user.redeployChooseDescription")}</DialogDescription>
        </DialogHeader>
        <div className="space-y-3">
          {deployment.source === "branch" || deployment.source === "pull_request" ? <RedeployOption selected={redeployMode === "latest"} onSelect={() => setRedeployMode("latest")} title={t(deployment.source === "pull_request" ? "user.redeployLatestPullRequestTitle" : "user.redeployLatestTitle")} description={t(deployment.source === "pull_request" ? "user.redeployLatestPullRequestDescription" : "user.redeployLatestDescription")}>
            <Badge variant="secondary">{displayRevisionRef(deployment.ref) || deployment.commit_sha.slice(0, 8)}</Badge>
          </RedeployOption> : null}
          <RedeployOption selected={redeployMode === "revision"} onSelect={() => setRedeployMode("revision")} title={t("user.redeployRevisionTitle")} description={t("user.redeployRevisionDescription")}>
            <Badge variant="secondary">{t("user.deploymentRevisionShortNumber", { number: redeployRevision })} · {revisions.find((revision) => revision.number === redeployRevision)?.commit_sha.slice(0, 8) ?? deployment.commit_sha.slice(0, 8)}</Badge>
          </RedeployOption>
        </div>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={() => setRedeployOpen(false)}>{t("common.cancel")}</Button>
          <Button type="button" disabled={Boolean(action)} onClick={() => { setRedeployOpen(false); onAction("retry", { mode: redeployMode, ...(redeployMode === "revision" ? { revision_number: redeployRevision } : {}) }) }}>{t("user.startRedeploy")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  </div>
}

function deploymentSourceHref(deployment: Pick<Deployment, "repository_full_name" | "ref" | "source" | "pull_request_number">, pullRequest?: number) {
  const number = pullRequest || deployment.pull_request_number || Number(deployment.ref?.match(/^refs\/pull\/(\d+)\/head$/)?.[1])
  if (number) return `https://github.com/${deployment.repository_full_name}/pull/${number}`
  const value = displayRevisionRef(deployment.ref)
  if (!value) return ""
  const encoded = encodeURIComponent(value).replace(/%2F/gi, "/")
  const tag = deployment.source === "tag" || deployment.ref?.startsWith("refs/tags/")
  return `https://github.com/${deployment.repository_full_name}/${tag ? "tree/refs/tags" : "tree"}/${encoded}`
}

function DeploymentSourceLink({ deployment, href, children }: { deployment: Pick<Deployment, "repository_full_name" | "ref" | "source" | "pull_request_number">; href?: string; children: ReactNode }) {
  const target = href || deploymentSourceHref(deployment)
  if (!target) return <span>{children}</span>
  return <a href={target} target="_blank" rel="noopener noreferrer" className="text-primary hover:underline" aria-label={typeof children === "string" ? children : undefined}>{children}</a>
}

function DeploymentEndpoint({ endpoint, isPublic }: { endpoint: string; isPublic: boolean }) {
  const { t } = useTranslation()
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(endpoint)
      toast.success(t("user.deploymentEndpointCopied"))
    } catch {
      toast.error(t("user.deploymentEndpointCopyFailed"))
    }
  }
  return <div className="space-y-1.5">
    <div className="flex min-w-0 items-start gap-3 rounded-md border border-border bg-muted/30 px-3 py-2">
      <span className="shrink-0 pt-0.5 text-xs text-muted-foreground">{t("user.deploymentEndpoint")}</span>
      <a className="min-w-0 flex-1 break-all text-sm font-medium text-primary hover:underline" href={endpoint} target="_blank" rel="noopener noreferrer">{endpoint}</a>
      <Button type="button" variant="ghost" size="icon" className="size-6 shrink-0" aria-label={t("user.deploymentCopyEndpoint")} title={t("user.deploymentCopyEndpoint")} onClick={() => void copy()}><Copy className="size-3.5" /></Button>
      <Button asChild variant="ghost" size="icon" className="size-6 shrink-0"><a href={endpoint} target="_blank" rel="noopener noreferrer" aria-label={t("user.deploymentOpenEndpoint")} title={t("user.deploymentOpenEndpoint")}><ExternalLink className="size-3.5" /></a></Button>
    </div>
    {isPublic ? <p className="text-xs text-muted-foreground">{t("user.deploymentEndpointPublic")}</p> : null}
  </div>
}

function DeploymentCommit({ repository, sha }: { repository: string; sha: string }) {
  const { t } = useTranslation()
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(sha)
      toast.success(t("user.deploymentCommitCopied"))
    } catch {
      toast.error(t("user.deploymentCommitCopyFailed"))
    }
  }
  return <span className="inline-flex items-center gap-1">
    <a href={`https://github.com/${repository.split("/").map(encodeURIComponent).join("/")}/commit/${encodeURIComponent(sha)}`} target="_blank" rel="noopener noreferrer" title={sha} className="hover:text-primary hover:underline"><code>{sha.slice(0, 8)}</code></a>
    <Button type="button" variant="ghost" size="icon" className="size-6" aria-label={t("user.deploymentCopyCommit")} onClick={() => void copy()}><Copy className="size-3" /></Button>
  </span>
}

function RedeployOption({ selected, onSelect, title, description, children }: { selected: boolean; onSelect: () => void; title: string; description: string; children: React.ReactNode }) {
  return <button type="button" onClick={onSelect} className={cn("flex w-full items-center gap-4 rounded-lg border p-4 text-left transition-colors", selected ? "border-primary bg-primary/5 ring-2 ring-primary/20" : "hover:bg-accent")}>
    <span aria-hidden="true" className={cn("h-5 w-5 shrink-0 rounded-full border-2 p-1", selected ? "border-primary" : "border-muted-foreground/40")}><span className={cn("block h-full w-full rounded-full", selected ? "bg-primary" : "bg-transparent")} /></span>
    <span className="min-w-0 flex-1"><span className="flex flex-wrap items-center gap-2 font-semibold">{title}{children}</span><span className="mt-1 block text-sm text-muted-foreground">{description}</span></span>
  </button>
}


function useDeploymentEvents(request: Request, id: string | null, revisionNumber?: number, status?: string, login?: string) {
  const [events, setEvents] = useState<DeploymentEvent[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [hasOlder, setHasOlder] = useState(false)
  const generation = useRef(0)
  const cursor = useRef({ newest: 0, oldest: 0, busy: false })

  useEffect(() => {
    generation.current += 1
    cursor.current = { newest: 0, oldest: 0, busy: false }
    setEvents([])
    setError(null)
    setLoading(false)
    setHasOlder(false)
    return () => { generation.current += 1 }
  }, [id, revisionNumber, request, login])

  const load = useCallback(async (older = false) => {
    if (!id || cursor.current.busy) return
    const epoch = generation.current
    cursor.current.busy = true
    setLoading(true)
    setError(null)
    try {
      let next = older ? cursor.current.oldest : cursor.current.newest
      const initial = next === 0
      do {
        const query = next > 0 ? `?${older ? "before_id" : "after_id"}=${next}` : ""
        const endpoint = revisionNumber ? `/user/deployments/${encodeURIComponent(id)}/revisions/${revisionNumber}/events` : `/user/deployments/${encodeURIComponent(id)}/events`
        const page = await request(`${endpoint}${query}`) as EventPage
        if (epoch !== generation.current) return
        if (!Array.isArray(page.events)) throw new Error("Invalid deployment events response")
        const incoming = page.events
        setEvents((current) => {
          const merged = new Map(current.map((event) => [event.id, event]))
          incoming.forEach((event) => merged.set(event.id, event))
          return [...merged.values()].sort((a, b) => a.id - b.id)
        })
        if (incoming.length > 0) {
          cursor.current.newest = Math.max(cursor.current.newest, incoming[incoming.length - 1].id)
          if (initial || older) cursor.current.oldest = incoming[0].id
        }
        if (initial || older) setHasOlder(Boolean(page.has_more))
        if (initial || older || !page.has_more || incoming.length === 0) break
        next = incoming[incoming.length - 1].id
      } while (epoch === generation.current)
    } catch (cause) {
      if (epoch === generation.current) setError(cause instanceof Error ? cause.message : String(cause))
    } finally {
      if (epoch === generation.current) {
        cursor.current.busy = false
        setLoading(false)
      }
    }
  }, [id, revisionNumber, request])

  const refresh = useCallback(() => load(false), [load])
  const loadOlder = useCallback(() => load(true), [load])
  useEffect(() => {
    void refresh()
    if (!status || !activeStatuses.has(status)) return
    const timer = window.setInterval(() => { void refresh() }, 3000)
    return () => window.clearInterval(timer)
  }, [refresh, status, login])

  return { events, loading, error, hasOlder, refresh, loadOlder }
}

function revisionNumberFromURL(): number | undefined {
  if (typeof window === "undefined") return undefined
  const value = new URLSearchParams(window.location.search).get("revision")
  if (!value || !/^[1-9]\d*$/.test(value)) return undefined
  const number = Number(value)
  return Number.isSafeInteger(number) ? number : undefined
}

function displayRevisionRef(ref?: string) { return ref?.replace(/^refs\/pull\/(\d+)\/head$/, "#$1").replace(/^refs\/(heads|tags)\//, "") ?? "" }
