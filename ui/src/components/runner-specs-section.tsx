import { type Dispatch, type FormEvent, type SetStateAction } from "react"
import { Pencil, Plus, RefreshCw, Trash2 } from "lucide-react"
import { useTranslation } from "react-i18next"

import { type RunnerSpec } from "@/admin-types"
import i18n from "@/i18n"
import { Badge } from "@/components/ui/badge"
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
import { Label } from "@/components/ui/label"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { cn } from "@/lib/utils"

export type RunnerSpecFormState = {
  name: string
  labels: string
  required_labels: string
  template_id: string
  runner_group: string
  max_concurrency: string
  min_idle: string
  priority: string
  enabled: boolean
}

export function RunnerSpecDialogForm({
  savingRunnerSpec = false,
  editingRunnerSpec,
  runnerSpecForm,
  onRunnerSpecFormChange,
  onRunnerSpecOpenChange,
  onSubmitRunnerSpec,
}: {
  savingRunnerSpec?: boolean
  editingRunnerSpec: RunnerSpec | null
  runnerSpecForm: RunnerSpecFormState
  onRunnerSpecFormChange: Dispatch<SetStateAction<RunnerSpecFormState>>
  onRunnerSpecOpenChange: (open: boolean) => void
  onSubmitRunnerSpec: (event: FormEvent<HTMLFormElement>) => void
}) {
  const { t } = useTranslation()
  const managed = Boolean(editingRunnerSpec?.managed_by?.trim())

  return (
    <form onSubmit={onSubmitRunnerSpec} aria-busy={savingRunnerSpec}>
      <fieldset className="grid min-w-0 gap-4" disabled={savingRunnerSpec}>
        {managed ? (
          <div
            id="managed-runner-spec-note"
            className="flex items-start gap-3 rounded-md border bg-muted/35 px-3 py-2.5"
          >
            <Badge variant="secondary" className="mt-0.5">{t("admin.managed")}</Badge>
            <p className="text-sm leading-5 text-muted-foreground">
              {t("admin.managedSpecDescription")}
            </p>
          </div>
        ) : (
          <div
            id="platform-shared-runner-spec-note"
            className="flex items-start gap-3 rounded-md border border-primary/20 bg-primary/[0.04] px-3 py-2.5"
          >
            <Badge variant="outline" className="mt-0.5 border-primary/30 text-primary">{t("admin.platformShared")}</Badge>
            <p className="text-sm leading-5 text-muted-foreground">
              {t("admin.platformSharedSpecDescription")}
            </p>
          </div>
        )}

        <div className="grid gap-2">
          <Label htmlFor="runner-spec-name">{t("common.name")}</Label>
          <Input
            id="runner-spec-name"
            value={runnerSpecForm.name}
            onChange={(event) => onRunnerSpecFormChange((current) => ({ ...current, name: event.target.value }))}
            placeholder={t("admin.runnerSpecNamePlaceholder")}
            disabled={editingRunnerSpec !== null}
          />
        </div>

        <div className="grid gap-2 sm:grid-cols-2">
          <div className="grid gap-2">
            <Label htmlFor="runner-spec-labels">{t("common.labels")}</Label>
            <Input
              id="runner-spec-labels"
              value={runnerSpecForm.labels}
              onChange={(event) => onRunnerSpecFormChange((current) => ({ ...current, labels: event.target.value }))}
              placeholder="self-hosted,e2b"
              disabled={managed}
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="runner-spec-required-labels">{t("admin.requiredLabels")}</Label>
            <Input
              id="runner-spec-required-labels"
              value={runnerSpecForm.required_labels}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({ ...current, required_labels: event.target.value }))
              }
              placeholder="e2b"
              disabled={managed}
            />
            {!managed ? (
              <p className="text-xs text-muted-foreground">{t("admin.requiredLabelsDescription")}</p>
            ) : null}
          </div>
        </div>

        <div className="grid gap-2 sm:grid-cols-2">
          {managed ? (
            <div className="grid gap-2">
              <Label htmlFor="runner-spec-default-template">{t("admin.defaultTemplate")}</Label>
              <Input
                id="runner-spec-default-template"
                value={editingRunnerSpec?.default_template_name || ""}
                disabled
              />
            </div>
          ) : (
            <div className="grid gap-2">
              <Label htmlFor="runner-spec-template-id">{t("admin.templateID")}</Label>
              <Input
                id="runner-spec-template-id"
                value={runnerSpecForm.template_id}
                onChange={(event) =>
                  onRunnerSpecFormChange((current) => ({ ...current, template_id: event.target.value }))
                }
                placeholder={t("admin.templateIDPlaceholder")}
                aria-describedby="runner-spec-template-help"
              />
              <p id="runner-spec-template-help" className="text-xs text-muted-foreground">
                {t("admin.templateValidationDescription")}{" "}
                <a href="/admin/sandbox_service" className="underline underline-offset-2">
                  {t("admin.configureTemplateValidation")}
                </a>
              </p>
            </div>
          )}
          <div className="grid gap-2">
            <Label htmlFor="runner-spec-github-group">{t("admin.githubRunnerGroup")}</Label>
            <Input
              id="runner-spec-github-group"
              value={runnerSpecForm.runner_group}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({ ...current, runner_group: event.target.value }))
              }
              placeholder={t("admin.optionalGitHubRunnerGroup")}
              disabled={managed}
            />
          </div>
        </div>

        <div className="grid gap-3 sm:grid-cols-3">
          <div className="grid gap-2">
            <Label htmlFor="runner-spec-max-concurrency">{t("admin.maxConcurrency")}</Label>
            <Input
              id="runner-spec-max-concurrency"
              inputMode="numeric"
              value={runnerSpecForm.max_concurrency}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({ ...current, max_concurrency: event.target.value }))
              }
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="runner-spec-min-idle">{t("admin.minIdle")}</Label>
            <Input
              id="runner-spec-min-idle"
              inputMode="numeric"
              value={runnerSpecForm.min_idle}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({ ...current, min_idle: event.target.value }))
              }
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="runner-spec-priority">{t("admin.priority")}</Label>
            <Input
              id="runner-spec-priority"
              inputMode="numeric"
              value={runnerSpecForm.priority}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({ ...current, priority: event.target.value }))
              }
              disabled={managed}
            />
          </div>
        </div>

        <div className="grid gap-2">
          <label className="flex items-center gap-2 text-sm">
            <input
              id="runner-spec-enabled"
              type="checkbox"
              checked={runnerSpecForm.enabled}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({ ...current, enabled: event.target.checked }))
              }
            />
            {t("common.enabled")}
          </label>
        </div>

        <DialogFooter>
          <Button type="button" variant="outline" disabled={savingRunnerSpec} onClick={() => onRunnerSpecOpenChange(false)}>
            {t("common.cancel")}
          </Button>
          <Button type="submit" disabled={savingRunnerSpec}>
            {savingRunnerSpec ? t("admin.saving") : t("admin.saveRunnerSpec")}
          </Button>
        </DialogFooter>
      </fieldset>
    </form>
  )
}

export function RunnerSpecsSection({
  savingRunnerSpec = false,
  loading,
  runnerSpecs,
  runnerSpecOpen,
  editingRunnerSpec,
  runnerSpecForm,
  onRefresh,
  onResetRunnerSpecForm,
  onRunnerSpecOpenChange,
  onRunnerSpecFormChange,
  onSubmitRunnerSpec,
  onEditRunnerSpec,
  onDeleteRunnerSpec,
}: {
  savingRunnerSpec?: boolean
  loading: boolean
  runnerSpecs: RunnerSpec[]
  runnerSpecOpen: boolean
  editingRunnerSpec: RunnerSpec | null
  runnerSpecForm: RunnerSpecFormState
  onRefresh: () => void
  onResetRunnerSpecForm: () => void
  onRunnerSpecOpenChange: (open: boolean) => void
  onRunnerSpecFormChange: Dispatch<SetStateAction<RunnerSpecFormState>>
  onSubmitRunnerSpec: (event: FormEvent<HTMLFormElement>) => void
  onEditRunnerSpec: (runnerSpec: RunnerSpec) => void
  onDeleteRunnerSpec: (name: string) => void
}) {
  const t = i18n.t
  return (
    <div className="grid gap-4">
      <Card className="min-w-0">
        <CardHeader className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div>
            <CardTitle>{t("sidebar.runnerSpecs")}</CardTitle>
            <CardDescription>{t("admin.specsDescription")}</CardDescription>
          </div>
          <div className="flex gap-2">
            <Button
              type="button"
              onClick={() => {
                onResetRunnerSpecForm()
                onRunnerSpecOpenChange(true)
              }}
            >
              <Plus />
              {t("admin.createRunnerSpec")}
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
        </CardHeader>
        <CardContent className="p-0">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead>{t("common.name")}</TableHead>
                <TableHead>{t("common.labels")}</TableHead>
                <TableHead>{t("common.template")}</TableHead>
                <TableHead>{t("admin.githubGroup")}</TableHead>
                <TableHead>{t("admin.limit")}</TableHead>
                <TableHead className="w-44">
                  <span className="sr-only">{t("common.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {runnerSpecs.map((runnerSpec) => {
                const managed = Boolean(runnerSpec.managed_by?.trim())
                return (
                <TableRow key={runnerSpec.name} className="cursor-pointer" onClick={() => onEditRunnerSpec(runnerSpec)}>
                  <TableCell>
                    <div className="flex max-w-[240px] items-center gap-2">
                      <span className="truncate">{runnerSpec.name}</span>
                      {managed ? <Badge variant="secondary">{t("admin.managed")}</Badge> : null}
                    </div>
                  </TableCell>
                  <TableCell><div className="max-w-[260px] truncate">{runnerSpec.labels.join(", ")}</div></TableCell>
                  <TableCell>
                    <div className="max-w-[240px]">
                      <div className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                        {managed ? t("admin.defaultTemplate") : t("admin.templateID")}
                      </div>
                      <div className="truncate">
                        {managed ? runnerSpec.default_template_name || "—" : runnerSpec.template_id}
                      </div>
                    </div>
                  </TableCell>
                  <TableCell><div className="max-w-[220px] truncate">{runnerSpec.runner_group || "-"}</div></TableCell>
                  <TableCell>{runnerSpec.max_concurrency}</TableCell>
                  <TableCell>
                    <div className="flex justify-end gap-2">
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        aria-label={t("admin.editNamedRunnerSpec", { name: runnerSpec.name })}
                        onClick={(event) => {
                          event.stopPropagation()
                          onEditRunnerSpec(runnerSpec)
                        }}
                      >
                        <Pencil />
                        {t("common.edit")}
                      </Button>
                      {!managed ? (
                        <Button
                          type="button"
                          variant="outline"
                          size="sm"
                          onClick={(event) => {
                            event.stopPropagation()
                            onDeleteRunnerSpec(runnerSpec.name)
                          }}
                        >
                          <Trash2 />
                          {t("common.delete")}
                        </Button>
                      ) : null}
                    </div>
                  </TableCell>
                </TableRow>
                )
              })}
            </TableBody>
          </Table>
        </CardContent>
      </Card>
      <Dialog
        open={runnerSpecOpen}
        onOpenChange={(open) => {
          if (!savingRunnerSpec) onRunnerSpecOpenChange(open)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{editingRunnerSpec ? t("admin.editRunnerSpec") : t("admin.createRunnerSpec")}</DialogTitle>
            <DialogDescription>{t("admin.specDialogDescription")}</DialogDescription>
          </DialogHeader>
          <RunnerSpecDialogForm
            savingRunnerSpec={savingRunnerSpec}
            editingRunnerSpec={editingRunnerSpec}
            runnerSpecForm={runnerSpecForm}
            onRunnerSpecFormChange={onRunnerSpecFormChange}
            onRunnerSpecOpenChange={onRunnerSpecOpenChange}
            onSubmitRunnerSpec={onSubmitRunnerSpec}
          />
        </DialogContent>
      </Dialog>
    </div>
  )
}
