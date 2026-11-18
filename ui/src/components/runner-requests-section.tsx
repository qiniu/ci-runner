import { useCallback, useEffect, useRef, useState, type MouseEvent } from "react"
import { Check, ChevronsUpDown, LoaderCircle, RefreshCw, Search, Trash2 } from "lucide-react"
import { useTranslation } from "react-i18next"

import { formatTime, runnerDisplayStatus, runnerStatusLabel } from "@/admin-format"
import { activeStatuses, runnerDisplayStatuses, type RunnerDisplayStatus, type RunnerState } from "@/admin-types"
import { adminRunnerRequestDetailPath, createAutomaticPageLoadGate } from "@/app-load-policy"
import { StatusBadge } from "@/components/admin-shared"
import { stickyTableHeaderOffset } from "@/components/runner-request-table"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { cn } from "@/lib/utils"

function verticalScrollContainer(element: HTMLElement) {
  let ancestor = element.parentElement
  while (ancestor) {
    const overflowY = window.getComputedStyle(ancestor).overflowY
    if (/auto|scroll/.test(overflowY) && ancestor.scrollHeight > ancestor.clientHeight) {
      return ancestor
    }
    ancestor = ancestor.parentElement
  }
  return null
}

export function RunnerRequestsSection({
  loading,
  error,
  windowReset,
  pollingPaused,
  runners,
  hasMore,
  loadingMore,
  runnerStatusFilter,
  runnerRepositoryFilter,
  runnerSpecFilter,
  runnerSpecNames,
  requestLookup,
  requestQuery,
  onRefresh,
  onStatusFilterChange,
  onRepositoryFilterChange,
  onRunnerSpecFilterChange,
  onRequestLookupChange,
  onSearchRepositories,
  onLoadMore,
  onLookupRunnerRequest,
  onOpenRunnerRequest,
  onRetryRunner,
  onStopRunner,
}: {
  loading: boolean
  error?: string
  windowReset?: boolean
  pollingPaused?: boolean
  runners: RunnerState[]
  hasMore: boolean
  loadingMore: boolean
  runnerStatusFilter: RunnerDisplayStatus | "all"
  runnerRepositoryFilter: string
  runnerSpecFilter: string
  runnerSpecNames: string[]
  requestLookup: string
  requestQuery: string
  onRefresh: () => void
  onStatusFilterChange: (value: RunnerDisplayStatus | "all") => void
  onRepositoryFilterChange: (value: string) => void
  onRunnerSpecFilterChange: (value: string) => void
  onRequestLookupChange: (value: string) => void
  onSearchRepositories: (query: string) => Promise<{ repositories: string[]; hasMore: boolean }>
  onLoadMore: () => Promise<boolean>
  onLookupRunnerRequest: (identifier: string) => void
  onOpenRunnerRequest: (identifier: string) => void
  onRetryRunner: (id: string) => void
  onStopRunner: (id: string) => void
}) {
  const { t, i18n } = useTranslation()
  const [repositoryOpen, setRepositoryOpen] = useState(false)
  const [repositoryQuery, setRepositoryQuery] = useState("")
  const [repositoryOptions, setRepositoryOptions] = useState<string[]>([])
  const [repositoryHasMore, setRepositoryHasMore] = useState(false)
  const [repositoryLoading, setRepositoryLoading] = useState(false)
  const [repositorySearchFailed, setRepositorySearchFailed] = useState(false)
  const repositorySearchGeneration = useRef(0)
  const tableHeaderRef = useRef<HTMLTableSectionElement>(null)
  const loadMoreRef = useRef<HTMLDivElement>(null)
  const [automaticPageLoadGate] = useState(createAutomaticPageLoadGate)

  const triggerLoadMore = useCallback((manual = false) => {
    if (!automaticPageLoadGate.begin(manual)) return
    void onLoadMore().then(
      (succeeded) => automaticPageLoadGate.finish(succeeded),
      () => automaticPageLoadGate.finish(false),
    )
  }, [automaticPageLoadGate, onLoadMore])

  useEffect(() => {
    if (!hasMore || runners.length === 0) automaticPageLoadGate.reset()
  }, [automaticPageLoadGate, hasMore, runners.length])

  useEffect(() => {
    if (!repositoryOpen) return
    const generation = ++repositorySearchGeneration.current
    const timer = window.setTimeout(() => {
      setRepositoryLoading(true)
      setRepositorySearchFailed(false)
      void onSearchRepositories(repositoryQuery)
        .then((result) => {
          if (generation !== repositorySearchGeneration.current) return
          setRepositoryOptions(result.repositories)
          setRepositoryHasMore(result.hasMore)
        })
        .catch(() => {
          if (generation !== repositorySearchGeneration.current) return
          setRepositoryOptions([])
          setRepositoryHasMore(false)
          setRepositorySearchFailed(true)
        })
        .finally(() => {
          if (generation === repositorySearchGeneration.current) setRepositoryLoading(false)
        })
    }, repositoryQuery ? 200 : 0)
    return () => window.clearTimeout(timer)
  }, [onSearchRepositories, repositoryOpen, repositoryQuery])

  useEffect(() => {
    const tableHeader = tableHeaderRef.current
    const table = tableHeader?.closest("table")
    if (!tableHeader || !table) return

    const scrollContainer = verticalScrollContainer(table)
    if (!scrollContainer) return

    let animationFrame = 0
    const syncTableHeader = () => {
      window.cancelAnimationFrame(animationFrame)
      animationFrame = window.requestAnimationFrame(() => {
        const scrollportTop = scrollContainer.getBoundingClientRect().top
        const tableBounds = table.getBoundingClientRect()
        const headerHeight = tableHeader.getBoundingClientRect().height
        const offset = stickyTableHeaderOffset({
          scrollportTop,
          tableTop: tableBounds.top,
          tableHeight: tableBounds.height,
          headerHeight,
        })
        tableHeader.style.transform = offset > 0 ? `translateY(${offset}px)` : ""
      })
    }

    syncTableHeader()
    scrollContainer.addEventListener("scroll", syncTableHeader, { passive: true })
    window.addEventListener("resize", syncTableHeader)
    return () => {
      window.cancelAnimationFrame(animationFrame)
      scrollContainer.removeEventListener("scroll", syncTableHeader)
      window.removeEventListener("resize", syncTableHeader)
      tableHeader.style.transform = ""
    }
  }, [runners.length])

  useEffect(() => {
    const sentinel = loadMoreRef.current
    if (!sentinel || !hasMore) return
    const root = verticalScrollContainer(sentinel)
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting) && !loadingMore) triggerLoadMore()
      },
      { root, rootMargin: "320px 0px" },
    )
    observer.observe(sentinel)
    return () => observer.disconnect()
  }, [hasMore, loadingMore, runners.length, triggerLoadMore])

  const openRunnerRequest = (event: MouseEvent<HTMLAnchorElement>, runner: RunnerState) => {
    event.stopPropagation()
    if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    onOpenRunnerRequest(runner.id)
  }

  return (
    <div>
      <Card className="min-w-0 gap-0 py-0">
        <CardHeader className="border-b px-5 py-4">
          <div className="flex flex-col gap-3">
            <div>
              <CardTitle>{t("sidebar.runnerRequests")}</CardTitle>
              <CardDescription>
                {t("admin.requestsDescription")}
              </CardDescription>
            </div>
            <div
              data-testid="runner-request-toolbar"
              className="grid gap-2 md:grid-cols-3 xl:grid-cols-[minmax(140px,180px)_minmax(180px,240px)_minmax(180px,240px)_minmax(360px,1fr)]"
            >
              <Select value={runnerStatusFilter} onValueChange={(value) => onStatusFilterChange(value as RunnerDisplayStatus | "all")}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder={t("common.status")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">{t("admin.allStatuses")}</SelectItem>
                  {runnerDisplayStatuses.map((status) => (
                    <SelectItem key={status} value={status}>
                      {runnerStatusLabel(status)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <Popover
                open={repositoryOpen}
                onOpenChange={(open) => {
                  setRepositoryOpen(open)
                  if (open) setRepositoryQuery("")
                }}
              >
                <PopoverTrigger asChild>
                  <Button
                    type="button"
                    variant="outline"
                    role="combobox"
                    aria-expanded={repositoryOpen}
                    aria-label={t("admin.filterByRepository")}
                    className="w-full justify-between px-3 font-normal"
                  >
                    <span className="truncate">
                      {runnerRepositoryFilter === "all" ? t("admin.allRepositories") : runnerRepositoryFilter}
                    </span>
                    <ChevronsUpDown className="opacity-50" />
                  </Button>
                </PopoverTrigger>
                <PopoverContent align="start" className="w-[var(--radix-popover-trigger-width)] p-0">
                  <div className="border-b p-2">
                    <div className="relative">
                      <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
                      <Input
                        data-testid="runner-repository-search"
                        value={repositoryQuery}
                        onChange={(event) => setRepositoryQuery(event.target.value)}
                        placeholder={t("admin.searchRepositories")}
                        className="pl-8"
                        autoComplete="off"
                      />
                    </div>
                  </div>
                  <div className="max-h-64 overflow-y-auto p-1" role="listbox" aria-label={t("admin.filterByRepository")}>
                    <button
                      type="button"
                      role="option"
                      aria-selected={runnerRepositoryFilter === "all"}
                      className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
                      onClick={() => {
                        onRepositoryFilterChange("all")
                        setRepositoryOpen(false)
                      }}
                    >
                      <Check className={cn("size-4", runnerRepositoryFilter === "all" ? "opacity-100" : "opacity-0")} />
                      <span>{t("admin.allRepositories")}</span>
                    </button>
                    {repositoryOptions.map((repository) => (
                      <button
                        type="button"
                        role="option"
                        aria-selected={runnerRepositoryFilter === repository}
                        key={repository}
                        className="flex w-full items-center gap-2 rounded-sm px-2 py-1.5 text-left text-sm hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
                        onClick={() => {
                          onRepositoryFilterChange(repository)
                          setRepositoryOpen(false)
                        }}
                      >
                        <Check className={cn("size-4", runnerRepositoryFilter === repository ? "opacity-100" : "opacity-0")} />
                        <span className="truncate">{repository}</span>
                      </button>
                    ))}
                    {repositoryLoading ? (
                      <div className="flex items-center justify-center gap-2 px-2 py-4 text-sm text-muted-foreground">
                        <LoaderCircle className="size-4 animate-spin" />
                        {t("admin.loadingRepositories")}
                      </div>
                    ) : repositorySearchFailed ? (
                      <div className="px-2 py-4 text-center text-sm text-destructive">{t("admin.loadRunnerRepositoriesFailed")}</div>
                    ) : repositoryOptions.length === 0 && repositoryQuery ? (
                      <div className="px-2 py-4 text-center text-sm text-muted-foreground">{t("admin.noMatchingRepositories")}</div>
                    ) : repositoryHasMore ? (
                      <div className="border-t px-2 py-2 text-xs text-muted-foreground">{t("admin.moreRepositoriesAvailable")}</div>
                    ) : null}
                  </div>
                </PopoverContent>
              </Popover>
              <Select value={runnerSpecFilter} onValueChange={onRunnerSpecFilterChange}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder={t("common.runnerSpec")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">{t("admin.allRunnerSpecs")}</SelectItem>
                  {runnerSpecNames.map((runnerSpecName) => (
                    <SelectItem key={runnerSpecName} value={runnerSpecName}>
                      {runnerSpecName}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              <div className="flex min-w-0 gap-2 md:col-span-3 xl:col-span-1">
                <form
                  className="flex min-w-0 flex-1 gap-2"
                  onSubmit={(event) => {
                    event.preventDefault()
                    const identifier = requestLookup.trim()
                    if (identifier) onLookupRunnerRequest(identifier)
                  }}
                >
                  <label className="sr-only" htmlFor="runner-request-lookup">
                    {t("admin.runnerRequestID")}
                  </label>
                  <Input
                    id="runner-request-lookup"
                    value={requestLookup}
                    onChange={(event) => onRequestLookupChange(event.target.value)}
                    placeholder={t("admin.runnerRequestPlaceholder")}
                    autoComplete="off"
                  />
                  <Button type="submit" variant="outline" disabled={!requestLookup.trim()}>
                    <Search />
                    {t("admin.openRunnerRequest")}
                  </Button>
                </form>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  className="shrink-0"
                  onClick={onRefresh}
                  disabled={loading}
                  title={t("common.refresh")}
                >
                  <RefreshCw className={cn(loading && "animate-spin")} />
                </Button>
              </div>
            </div>
          </div>
        </CardHeader>
        <CardContent className="p-0">
          {error ? <p role="alert" className="px-6 py-3 text-sm text-destructive">{t("app.controlPlaneLoadFailed")}: {error}</p> : null}
          {windowReset || pollingPaused ? (
            <p role="status" className="px-6 py-3 text-sm text-muted-foreground">
              {windowReset ? t("admin.runnerRequestWindowReset") : null}{" "}
              {pollingPaused ? t("admin.runnerRequestPollingPaused") : null}
            </p>
          ) : null}
          <Table className="table-fixed md:min-w-max md:table-auto">
            <TableHeader ref={tableHeaderRef} className="relative z-10 bg-background will-change-transform">
              <TableRow>
                <TableHead className="w-24 md:w-auto md:min-w-24">{t("common.status")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("common.repository")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("common.runnerSpec")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("user.requestedLabels")}</TableHead>
                <TableHead className="md:min-w-44">{t("common.runner")}</TableHead>
                <TableHead className="hidden md:table-cell">{t("common.sandbox")}</TableHead>
                <TableHead className="hidden min-w-32 md:table-cell">{t("admin.githubJob")}</TableHead>
                <TableHead className="hidden min-w-36 md:table-cell">{t("common.created")}</TableHead>
                <TableHead className="hidden min-w-36 md:table-cell">{t("common.updated")}</TableHead>
                <TableHead className="hidden min-w-24 md:table-cell" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {runners.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={10} className="h-24 text-center text-muted-foreground">
                    {loading ? t("common.loading") : error ? t("app.controlPlaneLoadFailed") : t("admin.noRequestsFound")}
                  </TableCell>
                </TableRow>
              ) : (
                runners.map((runner) => (
                  <TableRow
                    key={runner.id}
                    className="cursor-pointer"
                    onClick={() => onOpenRunnerRequest(runner.id)}
                  >
                    <TableCell>
                      <StatusBadge status={runnerDisplayStatus(runner)} />
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      <div>{runner.repository_full_name || "-"}</div>
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      {runner.runner_spec_name || "-"}
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      <div
                        className="font-mono text-xs text-muted-foreground"
                      >
                        {runner.requested_labels?.join(", ") || "-"}
                      </div>
                    </TableCell>
                    <TableCell className="max-md:whitespace-normal">
                      {runner.repository_full_name ? (
                        <div className="break-all text-xs text-muted-foreground md:hidden">{runner.repository_full_name}</div>
                      ) : null}
                      <a
                        className="font-medium text-primary underline-offset-4 hover:underline max-md:break-all"
                        href={adminRunnerRequestDetailPath(runner.id, requestQuery)}
                        onClick={(event) => openRunnerRequest(event, runner)}
                      >
                        {runner.runner_name || runner.id}
                      </a>
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      <div>{runner.sandbox_id || "-"}</div>
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      {runner.github_job_url ? (
                        <a
                          className="font-mono text-primary underline-offset-4 hover:underline"
                          href={runner.github_job_url}
                          target="_blank"
                          rel="noreferrer"
                          onClick={(event) => event.stopPropagation()}
                        >
                          {runner.workflow_job_id || runner.assigned_job_id || t("common.job")}
                        </a>
                      ) : (
                        <span className="text-muted-foreground">-</span>
                      )}
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      {formatTime(runner.created_at, i18n.resolvedLanguage)}
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      {formatTime(runner.updated_at, i18n.resolvedLanguage)}
                    </TableCell>
                    <TableCell className="hidden md:table-cell">
                      <div className="flex gap-2">
                        {runnerDisplayStatus(runner) === "failed" ? (
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            onClick={(event) => {
                              event.stopPropagation()
                              onRetryRunner(runner.id)
                            }}
                          >
                            <RefreshCw />
                            {t("admin.retry")}
                          </Button>
                        ) : null}
                        {activeStatuses.has(runner.status) ? (
                          <Button
                            type="button"
                            variant="outline"
                            size="sm"
                            onClick={(event) => {
                              event.stopPropagation()
                              onStopRunner(runner.id)
                            }}
                          >
                            <Trash2 />
                            {t("admin.stop")}
                          </Button>
                        ) : null}
                      </div>
                    </TableCell>
                  </TableRow>
                ))
              )}
            </TableBody>
          </Table>
          <div ref={loadMoreRef} className="flex min-h-12 items-center justify-center border-t px-3 py-3 text-xs text-muted-foreground">
            {loadingMore ? (
              <span className="flex items-center gap-2">
                <LoaderCircle className="size-4 animate-spin" />
                {t("admin.loadingMoreRequests")}
              </span>
            ) : hasMore ? (
              <Button type="button" variant="ghost" size="sm" onClick={() => triggerLoadMore(true)} disabled={loading}>
                {t("admin.loadMoreRequests")}
              </Button>
            ) : runners.length > 0 ? (
              t("admin.allRequestsLoaded")
            ) : null}
            {runners.length > 0 ? <span className="ml-3 tabular-nums">{t("admin.loadedRequests", { count: runners.length })}</span> : null}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
