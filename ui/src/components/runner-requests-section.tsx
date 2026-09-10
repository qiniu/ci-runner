import { useState, type FormEvent, type MouseEvent } from "react"
import { Plus, RefreshCw, Search, Trash2 } from "lucide-react"
import { useTranslation } from "react-i18next"

import { formatTime, runnerDisplayStatus, runnerStatusLabel } from "@/admin-format"
import { activeStatuses, type RunnerDisplayStatus, type RunnerState } from "@/admin-types"
import { StatusBadge } from "@/components/admin-shared"
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

export function RunnerRequestsSection({
  hasAccess,
  loading,
  runners,
  filteredRunners,
  createID,
  createRepository,
  createRunnerSpec,
  createLabels,
  createRunnerOpen,
  runnerStatusFilter,
  runnerRepositoryFilter,
  runnerSpecFilter,
  runnerRepositories,
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
  onOpenRunnerRequest,
  onRetryRunner,
  onStopRunner,
}: {
  hasAccess: boolean
  loading: boolean
  runners: RunnerState[]
  filteredRunners: RunnerState[]
  createID: string
  createRepository: string
  createRunnerSpec: string
  createLabels: string
  createRunnerOpen: boolean
  runnerStatusFilter: RunnerDisplayStatus | "all"
  runnerRepositoryFilter: string
  runnerSpecFilter: string
  runnerRepositories: string[]
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
  onOpenRunnerRequest: (identifier: string) => void
  onRetryRunner: (id: string) => void
  onStopRunner: (id: string) => void
}) {
  const { t, i18n } = useTranslation()
  const [requestIdentifier, setRequestIdentifier] = useState("")

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
              <Select value={runnerRepositoryFilter} onValueChange={onRepositoryFilterChange}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder={t("common.repository")} />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="all">{t("admin.allRepositories")}</SelectItem>
                  {runnerRepositories.map((repository) => (
                    <SelectItem key={repository} value={repository}>
                      {repository}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
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
                  if (identifier) onOpenRunnerRequest(identifier)
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
        <CardContent className="p-0 [&_[data-slot=table-container]]:overflow-visible">
          <Table className="table-fixed">
            <TableHeader className="sticky top-0 z-10 bg-background">
              <TableRow>
                <TableHead className="w-24">{t("common.status")}</TableHead>
                <TableHead>{t("common.repository")}</TableHead>
                <TableHead className="hidden 2xl:table-cell">{t("common.runnerSpec")}</TableHead>
                <TableHead className="hidden lg:table-cell">{t("user.requestedLabels")}</TableHead>
                <TableHead className="w-44">{t("common.runner")}</TableHead>
                <TableHead className="hidden 2xl:table-cell">{t("common.sandbox")}</TableHead>
                <TableHead className="hidden w-32 md:table-cell">{t("admin.githubJob")}</TableHead>
                <TableHead className="hidden w-36 xl:table-cell">{t("common.created")}</TableHead>
                <TableHead className="hidden w-36 2xl:table-cell">{t("common.updated")}</TableHead>
                <TableHead className="w-24" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filteredRunners.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={10} className="h-24 text-center text-muted-foreground">
                    {t("admin.noRequestsFound")}
                  </TableCell>
                </TableRow>
              ) : (
                filteredRunners.map((runner) => (
                  <TableRow
                    key={runner.id}
                    className="cursor-pointer"
                    onClick={() => onOpenRunnerRequest(runner.id)}
                  >
                    <TableCell className="truncate">
                      <StatusBadge status={runnerDisplayStatus(runner)} />
                    </TableCell>
                    <TableCell className="truncate" title={runner.repository_full_name || undefined}>
                      <div className="truncate">{runner.repository_full_name || "-"}</div>
                    </TableCell>
                    <TableCell className="hidden truncate 2xl:table-cell" title={runner.runner_spec_name || undefined}>
                      {runner.runner_spec_name || "-"}
                    </TableCell>
                    <TableCell className="hidden truncate lg:table-cell">
                      <div
                        className="truncate font-mono text-xs text-muted-foreground"
                        title={runner.requested_labels?.join(", ") || undefined}
                      >
                        {runner.requested_labels?.join(", ") || "-"}
                      </div>
                    </TableCell>
                    <TableCell className="truncate">
                      <a
                        className="block truncate font-medium text-primary underline-offset-4 hover:underline"
                        href={`/admin/runner_requests/${encodeURIComponent(runner.id)}`}
                        title={runner.runner_name || runner.id}
                        onClick={(event) => openRunnerRequest(event, runner)}
                      >
                        {runner.runner_name || runner.id}
                      </a>
                    </TableCell>
                    <TableCell className="hidden truncate 2xl:table-cell" title={runner.sandbox_id || undefined}>
                      <div className="truncate">{runner.sandbox_id || "-"}</div>
                    </TableCell>
                    <TableCell className="hidden truncate md:table-cell">
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
                    <TableCell className="hidden truncate xl:table-cell" title={formatTime(runner.created_at, i18n.resolvedLanguage)}>
                      {formatTime(runner.created_at, i18n.resolvedLanguage)}
                    </TableCell>
                    <TableCell className="hidden truncate 2xl:table-cell" title={formatTime(runner.updated_at, i18n.resolvedLanguage)}>
                      {formatTime(runner.updated_at, i18n.resolvedLanguage)}
                    </TableCell>
                    <TableCell className="truncate">
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
          <div className="border-t px-3 py-2 text-xs text-muted-foreground">
            {t("admin.requestsShown", { filtered: filteredRunners.length, total: runners.length })}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
