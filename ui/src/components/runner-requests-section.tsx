import { type FormEvent } from "react"
import { Activity, Copy, ExternalLink, Plus, RefreshCw, Trash2 } from "lucide-react"
import { useTranslation } from "react-i18next"

import { formatTime, runnerDisplayStatus, runnerStatusLabel } from "@/admin-format"
import { activeStatuses, logNames, type RunnerDisplayStatus, type RunnerState } from "@/admin-types"
import { StatusBadge } from "@/components/admin-shared"
import { RunnerRequestDetails } from "@/components/runner-request-details"
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
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { cn } from "@/lib/utils"

type LogName = (typeof logNames)[number]

export function RunnerRequestsSection({
  hasAccess,
  loading,
  runners,
  filteredRunners,
  selected,
  selectedID,
  selectedLog,
  logText,
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
  onSelectRunner,
  onRetryRunner,
  onStopRunner,
  onCopySelectedID,
  onLoadLog,
  onSelectedLogChange,
}: {
  hasAccess: boolean
  loading: boolean
  runners: RunnerState[]
  filteredRunners: RunnerState[]
  selected?: RunnerState
  selectedID: string
  selectedLog: LogName
  logText: string
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
  onSelectRunner: (id: string) => void
  onRetryRunner: (id: string) => void
  onStopRunner: (id: string) => void
  onCopySelectedID: () => void
  onLoadLog: (id: string, name: LogName) => void
  onSelectedLogChange: (name: LogName) => void
}) {
  const { t, i18n } = useTranslation()
  return (
    <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(520px,640px)]">
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
            <div className="grid gap-2 md:grid-cols-[minmax(160px,220px)_minmax(180px,1fr)_minmax(180px,1fr)]">
              <Select value={runnerStatusFilter} onValueChange={(value) => onStatusFilterChange(value as RunnerDisplayStatus | "all")}>
                <SelectTrigger>
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
                <SelectTrigger>
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
                <SelectTrigger>
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
            </div>
            <div className="text-xs text-muted-foreground">
              {t("admin.requestsShown", { filtered: filteredRunners.length, total: runners.length })}
            </div>
          </div>
        </CardHeader>
        <CardContent className="max-h-[calc(100vh-18rem)] overflow-auto p-0">
          <Table>
            <TableHeader className="sticky top-0 z-10 bg-background">
              <TableRow>
                <TableHead>{t("common.status")}</TableHead>
                <TableHead>{t("common.repository")}</TableHead>
                <TableHead>{t("common.runnerSpec")}</TableHead>
                <TableHead>{t("common.runner")}</TableHead>
                <TableHead>{t("common.sandbox")}</TableHead>
                <TableHead>{t("common.github")}</TableHead>
                <TableHead>{t("common.updated")}</TableHead>
                <TableHead className="w-36" />
              </TableRow>
            </TableHeader>
            <TableBody>
              {filteredRunners.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={8} className="h-24 text-center text-muted-foreground">
                    {t("admin.noRequestsFound")}
                  </TableCell>
                </TableRow>
              ) : (
                filteredRunners.map((runner) => (
                  <TableRow
                    key={runner.id}
                    data-state={runner.id === selectedID ? "selected" : undefined}
                    className="cursor-pointer"
                    onClick={() => onSelectRunner(runner.id)}
                  >
                    <TableCell>
                      <StatusBadge status={runnerDisplayStatus(runner)} />
                    </TableCell>
                    <TableCell>
                      <div className="max-w-[220px] truncate">{runner.repository_full_name || "-"}</div>
                    </TableCell>
                    <TableCell>{runner.runner_spec_name || "-"}</TableCell>
                    <TableCell className="max-w-[260px]">
                      <div className="truncate font-medium">{runner.runner_name || runner.id}</div>
                      <div className="truncate text-xs text-muted-foreground">{runner.id}</div>
                    </TableCell>
                    <TableCell>
                      <div className="max-w-[180px] truncate">{runner.sandbox_id || "-"}</div>
                    </TableCell>
                    <TableCell>
                      {runner.github_job_url ? (
                        <Button
                          asChild
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={(event) => event.stopPropagation()}
                        >
                          <a href={runner.github_job_url} target="_blank" rel="noreferrer">
                            <ExternalLink />
                            {t("common.job")}
                          </a>
                        </Button>
                      ) : (
                        <span className="text-muted-foreground">-</span>
                      )}
                    </TableCell>
                    <TableCell>{formatTime(runner.updated_at, i18n.resolvedLanguage)}</TableCell>
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
        </CardContent>
      </Card>

      <Card className="min-w-0 gap-0 py-0">
        <CardHeader className="border-b px-5 py-4">
          <div className="flex items-start justify-between gap-3">
            <div>
              <CardTitle>{t("admin.requestDetails")}</CardTitle>
              <CardDescription>{selected?.runner_name || t("admin.selectRequest")}</CardDescription>
            </div>
            <div className="flex items-center gap-2">
              {selected ? (
                <Button asChild type="button" variant="outline" size="sm">
                  <a href={`/admin/diagnostics?runner=${encodeURIComponent(selected.runner_name || selected.id)}`}>
                    <Activity />
                    {t("admin.diagnoseRunnerRequest")}
                  </a>
                </Button>
              ) : null}
              <Button
                type="button"
                variant="outline"
                size="icon"
                onClick={onCopySelectedID}
                disabled={!selected}
                title={t("admin.copyRunnerID")}
              >
                <Copy />
              </Button>
            </div>
          </div>
        </CardHeader>
        {selected ? (
          <CardContent className="grid gap-5 p-5">
            <RunnerRequestDetails runner={selected} />
            {runnerDisplayStatus(selected) === "failed" ? (
              <Button type="button" variant="outline" onClick={() => onRetryRunner(selected.id)}>
                <RefreshCw />
                {t("admin.retryRequest")}
              </Button>
            ) : null}
            <div className="space-y-3">
              <div className="flex items-center justify-between gap-3">
                <div>
                  <div className="text-sm font-medium">{t("common.logs")}</div>
                  <div className="text-xs text-muted-foreground">{t("admin.logsDescription")}</div>
                </div>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => onLoadLog(selected.id, selectedLog)}
                >
                  <RefreshCw />
                  {t("common.refresh")}
                </Button>
              </div>
              <Tabs
                value={selectedLog}
                onValueChange={(value) => onSelectedLogChange(value as LogName)}
              >
                <TabsList>
                  {logNames.map((name) => (
                    <TabsTrigger key={name} value={name}>
                      {name.replace(".log", "")}
                    </TabsTrigger>
                  ))}
                </TabsList>
              </Tabs>
              <pre className="max-h-[52vh] min-h-80 overflow-auto rounded-lg border bg-muted/50 p-3 font-mono text-xs leading-relaxed whitespace-pre-wrap">
                {logText}
              </pre>
            </div>
          </CardContent>
        ) : (
          <CardContent className="p-8 text-sm text-muted-foreground">
            {t("admin.noRequestSelected")}
          </CardContent>
        )}
      </Card>
    </div>
  )
}
