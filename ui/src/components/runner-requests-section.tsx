import { useEffect, useRef, useState, type FormEvent, type MouseEvent } from "react"
import { Check, ChevronLeft, ChevronRight, ChevronsUpDown, LoaderCircle, Plus, RefreshCw, Search, Trash2 } from "lucide-react"
import { useTranslation } from "react-i18next"

import { formatTime, runnerDisplayStatus, runnerStatusLabel } from "@/admin-format"
import { activeStatuses, type RunnerDisplayStatus, type RunnerState } from "@/admin-types"
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
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
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
  hasAccess,
  loading,
  runners,
  total,
  offset,
  limit,
  createID,
  createRepository,
  createRunnerSpec,
  createLabels,
  createRunnerOpen,
  runnerStatusFilter,
  runnerRepositoryFilter,
  runnerSpecFilter,
  runnerSpecNames,
  onRefresh,
  onResetCreateRunnerForm,
  onCreateRunnerOpenChange,
  onCreateRunnerSubmit,
  onCreateIDChange,
  onCreateRepositoryChange,
  onCreateRunnerSpecChange,
  onCreateLabelsChange,
  onStatusFilterChange,
  onRepositoryFilterChange,
  onRunnerSpecFilterChange,
  onSearchRepositories,
  onPreviousPage,
  onNextPage,
  onLookupRunnerRequest,
  onOpenRunnerRequest,
  onRetryRunner,
  onStopRunner,
}: {
  hasAccess: boolean
  loading: boolean
  runners: RunnerState[]
  total: number
  offset: number
  limit: number
  createID: string
  createRepository: string
  createRunnerSpec: string
  createLabels: string
  createRunnerOpen: boolean
  runnerStatusFilter: RunnerDisplayStatus | "all"
  runnerRepositoryFilter: string
  runnerSpecFilter: string
  runnerSpecNames: string[]
  onRefresh: () => void
  onResetCreateRunnerForm: () => void
  onCreateRunnerOpenChange: (open: boolean) => void
  onCreateRunnerSubmit: (event: FormEvent<HTMLFormElement>) => void
  onCreateIDChange: (value: string) => void
  onCreateRepositoryChange: (value: string) => void
  onCreateRunnerSpecChange: (value: string) => void
  onCreateLabelsChange: (value: string) => void
  onStatusFilterChange: (value: RunnerDisplayStatus | "all") => void
  onRepositoryFilterChange: (value: string) => void
  onRunnerSpecFilterChange: (value: string) => void
  onSearchRepositories: (query: string) => Promise<{ repositories: string[]; hasMore: boolean }>
  onPreviousPage: () => void
  onNextPage: () => void
  onLookupRunnerRequest: (identifier: string) => void
  onOpenRunnerRequest: (identifier: string) => void
  onRetryRunner: (id: string) => void
  onStopRunner: (id: string) => void
}) {
  const { t, i18n } = useTranslation()
  const [requestIdentifier, setRequestIdentifier] = useState("")
  const [repositoryOpen, setRepositoryOpen] = useState(false)
  const [repositoryQuery, setRepositoryQuery] = useState("")
  const [repositoryOptions, setRepositoryOptions] = useState<string[]>([])
  const [repositoryHasMore, setRepositoryHasMore] = useState(false)
  const [repositoryLoading, setRepositoryLoading] = useState(false)
  const [repositorySearchFailed, setRepositorySearchFailed] = useState(false)
  const repositorySearchGeneration = useRef(0)
  const tableHeaderRef = useRef<HTMLTableSectionElement>(null)

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
            <div className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
              <div>
                <CardTitle>{t("sidebar.runnerRequests")}</CardTitle>
                <CardDescription>
                  {t("admin.requestsDescription")}
                </CardDescription>
              </div>
              <div className="flex gap-2">
                <Button
                  type="button"
                  onClick={() => {
                    onResetCreateRunnerForm()
                    onCreateRunnerOpenChange(true)
                  }}
                  disabled={!hasAccess}
                >
                  <Plus />
                  {t("admin.createRunnerRequest")}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="icon"
                  onClick={onRefresh}
                  disabled={loading}
                  title={t("common.refresh")}
                >
                  <RefreshCw className={cn(loading && "animate-spin")} />
                </Button>
              </div>
            </div>
            <Dialog open={createRunnerOpen} onOpenChange={onCreateRunnerOpenChange}>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>{t("admin.createRunnerRequest")}</DialogTitle>
                  <DialogDescription>
                    {t("admin.createRunnerRequestDescription")}
                  </DialogDescription>
                </DialogHeader>
                <form className="grid gap-3" onSubmit={onCreateRunnerSubmit}>
                  <Input
                    value={createID}
                    onChange={(event) => onCreateIDChange(event.target.value)}
                    placeholder={t("admin.optionalID")}
                  />
                  <Input
                    value={createRepository}
                    onChange={(event) => onCreateRepositoryChange(event.target.value)}
                    placeholder="owner/repo"
                    required
                  />
                  <Input
                    value={createRunnerSpec}
                    onChange={(event) => onCreateRunnerSpecChange(event.target.value)}
                    placeholder={t("admin.optionalRunnerSpec")}
                  />
                  <Input
                    value={createLabels}
                    onChange={(event) => onCreateLabelsChange(event.target.value)}
                    placeholder="self-hosted,e2b"
                  />
                  <DialogFooter>
                    <Button type="button" variant="outline" onClick={() => onCreateRunnerOpenChange(false)}>
                      {t("common.cancel")}
                    </Button>
                    <Button type="submit" disabled={!hasAccess}>
                      {t("admin.createRunnerRequest")}
                    </Button>
                  </DialogFooter>
                </form>
              </DialogContent>
            </Dialog>
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
                  {(["queued", "creating", "running", "stopping", "completed", "unmatched", "failed"] as RunnerDisplayStatus[]).map((status) => (
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
              <form
                className="flex min-w-0 gap-2 md:col-span-3 xl:col-span-1"
                onSubmit={(event) => {
                  event.preventDefault()
                  const identifier = requestIdentifier.trim()
                  if (identifier) onLookupRunnerRequest(identifier)
                }}
              >
                <label className="sr-only" htmlFor="runner-request-lookup">
                  {t("admin.runnerRequestID")}
                </label>
                <Input
                  id="runner-request-lookup"
                  value={requestIdentifier}
                  onChange={(event) => setRequestIdentifier(event.target.value)}
                  placeholder={t("admin.runnerRequestPlaceholder")}
                  autoComplete="off"
                />
                <Button type="submit" variant="outline" disabled={!requestIdentifier.trim()}>
                  <Search />
                  {t("admin.openRunnerRequest")}
                </Button>
              </form>
            </div>
          </div>
        </CardHeader>
        <CardContent className="p-0">
          <Table className="min-w-max">
            <TableHeader ref={tableHeaderRef} className="relative z-10 bg-background will-change-transform">
              <TableRow>
                <TableHead className="min-w-24">{t("common.status")}</TableHead>
                <TableHead>{t("common.repository")}</TableHead>
                <TableHead>{t("common.runnerSpec")}</TableHead>
                <TableHead>{t("user.requestedLabels")}</TableHead>
                <TableHead className="min-w-44">{t("common.runner")}</TableHead>
                <TableHead>{t("common.sandbox")}</TableHead>
                <TableHead className="min-w-32">{t("admin.githubJob")}</TableHead>
                <TableHead className="min-w-36">{t("common.created")}</TableHead>
                <TableHead className="min-w-36">{t("common.updated")}</TableHead>
                <TableHead className="min-w-24" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {runners.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={10} className="h-24 text-center text-muted-foreground">
                    {t("admin.noRequestsFound")}
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
                    <TableCell>
                      <div>{runner.repository_full_name || "-"}</div>
                    </TableCell>
                    <TableCell>
                      {runner.runner_spec_name || "-"}
                    </TableCell>
                    <TableCell>
                      <div
                        className="font-mono text-xs text-muted-foreground"
                      >
                        {runner.requested_labels?.join(", ") || "-"}
                      </div>
                    </TableCell>
                    <TableCell>
                      <a
                        className="font-medium text-primary underline-offset-4 hover:underline"
                        href={`/admin/runner_requests/${encodeURIComponent(runner.id)}`}
                        onClick={(event) => openRunnerRequest(event, runner)}
                      >
                        {runner.runner_name || runner.id}
                      </a>
                    </TableCell>
                    <TableCell>
                      <div>{runner.sandbox_id || "-"}</div>
                    </TableCell>
                    <TableCell>
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
                    <TableCell>
                      {formatTime(runner.created_at, i18n.resolvedLanguage)}
                    </TableCell>
                    <TableCell>
                      {formatTime(runner.updated_at, i18n.resolvedLanguage)}
                    </TableCell>
                    <TableCell>
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
          <div className="flex items-center justify-between gap-3 border-t px-3 py-2">
            <div className="text-xs text-muted-foreground tabular-nums">
              {total === 0 ? t("admin.noRequestsFound") : t("admin.resultRange", { start: offset + 1, end: offset + runners.length, total })}
            </div>
            <div className="flex items-center gap-2">
              <span className="min-w-24 text-center text-xs font-medium tabular-nums">
                {t("admin.pageOf", { page: total === 0 ? 1 : Math.floor(offset / limit) + 1, pages: Math.max(1, Math.ceil(total / limit)) })}
              </span>
              <Button
                type="button"
                variant="outline"
                size="icon"
                disabled={offset === 0 || loading}
                onClick={onPreviousPage}
                aria-label={t("admin.previousRunnerRequestPage")}
              >
                <ChevronLeft />
              </Button>
              <Button
                type="button"
                variant="outline"
                size="icon"
                disabled={offset + runners.length >= total || loading}
                onClick={onNextPage}
                aria-label={t("admin.nextRunnerRequestPage")}
              >
                <ChevronRight />
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
