import {
  type ComponentProps,
  type Dispatch,
  type FormEvent,
  type SetStateAction,
  useState,
  useMemo,
} from "react"
import { Pencil, Plus, RefreshCw, Trash2 } from "lucide-react"
import { useTranslation } from "react-i18next"

import { type RunnerSpec } from "@/admin-types"
import { AdminTemplateInfo } from "@/components/admin-template-info"
import {
  createTemplateLookup,
  useAdminTemplateStatus,
  type TemplateLookup,
} from "@/hooks/use-admin-template-status"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Card,
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
import { cn } from "@/lib/utils"

export type RunnerSpecFormState = {
  template: string
  published: boolean

  name: string
  labels: string
  required_labels: string
  runner_group: string
  max_concurrency: string
  min_idle: string
  priority: string
  enabled: boolean
}

function RunnerSpecTextField(props: ComponentProps<"textarea">) {
  return (
    <textarea
      rows={1}
      className="field-sizing-content min-h-9 w-full min-w-0 resize-y rounded-md border border-input bg-transparent px-3 py-2 text-sm shadow-xs outline-none placeholder:text-muted-foreground focus-visible:border-ring focus-visible:ring-[3px] focus-visible:ring-ring/50 disabled:cursor-not-allowed disabled:opacity-50"
      {...props}
      onKeyDown={(event) => {
        // These are single-line identifiers; visual wrapping must not add newlines.
        if (event.key === "Enter") event.preventDefault()
      }}
      onChange={(event) => {
        event.target.value = event.target.value.replace(/[\r\n]/g, "")
        props.onChange?.(event)
      }}
    />
  )
}

export function RunnerSpecDialogForm({
  request,
  templateLookup,
  savingRunnerSpec = false,
  editingRunnerSpec,
  runnerSpecForm,
  onRunnerSpecFormChange,
  onRunnerSpecOpenChange,
  onSubmitRunnerSpec,
}: {
  request: (url: string, options?: RequestInit) => Promise<unknown>
  templateLookup?: TemplateLookup
  savingRunnerSpec?: boolean
  editingRunnerSpec: RunnerSpec | null
  runnerSpecForm: RunnerSpecFormState
  onRunnerSpecFormChange: Dispatch<SetStateAction<RunnerSpecFormState>>
  onRunnerSpecOpenChange: (open: boolean) => void
  onSubmitRunnerSpec: (event: FormEvent<HTMLFormElement>) => void
}) {
  const { t } = useTranslation()
  const originalTemplate =
    editingRunnerSpec?.default_template_name || editingRunnerSpec?.template_id
  const template = runnerSpecForm.template.trim()
  const referenceType =
    editingRunnerSpec && template === originalTemplate
      ? editingRunnerSpec.default_template_name
        ? "name"
        : "id"
      : ""
  const { status, retry } = useAdminTemplateStatus(
    request,
    template,
    referenceType,
    templateLookup,
    0,
    300,
  )
  const publicTemplate = status?.public === true && !status.error

  return (
    <form onSubmit={onSubmitRunnerSpec} aria-busy={savingRunnerSpec}>
      <fieldset
        className="grid min-w-0 gap-4 [&_select]:min-w-0 [&_select]:w-full"
        disabled={savingRunnerSpec}
      >
        <p className="text-sm leading-5 text-muted-foreground">
          {t("admin.platformSharedSpecDescription")}
        </p>

        <div className="grid gap-2">
          <Label htmlFor="runner-spec-name">{t("common.name")}</Label>
          <RunnerSpecTextField
            id="runner-spec-name"
            value={runnerSpecForm.name}
            onChange={(event) =>
              onRunnerSpecFormChange((current) => ({
                ...current,
                name: event.target.value,
              }))
            }
            placeholder={t("admin.runnerSpecNamePlaceholder")}
            disabled={editingRunnerSpec !== null}
          />
        </div>

        <div className="grid gap-4">
          <div className="grid gap-2">
            <Label htmlFor="runner-spec-labels">{t("common.labels")}</Label>
            <RunnerSpecTextField
              id="runner-spec-labels"
              value={runnerSpecForm.labels}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({
                  ...current,
                  labels: event.target.value,
                }))
              }
              placeholder="self-hosted,e2b"
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="runner-spec-required-labels">
              {t("admin.requiredLabels")}
            </Label>
            <RunnerSpecTextField
              id="runner-spec-required-labels"
              value={runnerSpecForm.required_labels}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({
                  ...current,
                  required_labels: event.target.value,
                }))
              }
              placeholder="e2b"
            />
            <p className="text-xs text-muted-foreground">
              {t("admin.requiredLabelsDescription")}
            </p>
          </div>
        </div>

        <div className="grid gap-4">
          <div className="grid gap-2">
            <Label htmlFor="runner-spec-template">{t("common.template")}</Label>
            <RunnerSpecTextField
              id="runner-spec-template"
              value={runnerSpecForm.template}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({
                  ...current,
                  template: event.target.value,
                  published: false,
                }))
              }
              aria-describedby="runner-spec-template-help"
              placeholder={t("admin.templateReferencePlaceholder")}
            />
            <AdminTemplateInfo
              status={status}
              empty={!template}
              onRetry={retry}
              helpID="runner-spec-template-help"
            />
          </div>
          <div className="grid gap-2">
            <Label htmlFor="runner-spec-github-group">
              {t("admin.githubRunnerGroup")}
            </Label>
            <RunnerSpecTextField
              id="runner-spec-github-group"
              value={runnerSpecForm.runner_group}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({
                  ...current,
                  runner_group: event.target.value,
                }))
              }
              placeholder={t("admin.optionalGitHubRunnerGroup")}
            />
          </div>
        </div>

        <div className="grid gap-3 sm:grid-cols-3">
          <div className="grid gap-2">
            <Label htmlFor="runner-spec-max-concurrency">
              {t("admin.maxConcurrency")}
            </Label>
            <Input
              id="runner-spec-max-concurrency"
              inputMode="numeric"
              value={runnerSpecForm.max_concurrency}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({
                  ...current,
                  max_concurrency: event.target.value,
                }))
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
                onRunnerSpecFormChange((current) => ({
                  ...current,
                  min_idle: event.target.value,
                }))
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
                onRunnerSpecFormChange((current) => ({
                  ...current,
                  priority: event.target.value,
                }))
              }
            />
          </div>
        </div>

        <div className="grid gap-2">
          <label className="flex items-center gap-2 text-sm">
            <input
              id="runner-spec-published"
              type="checkbox"
              checked={runnerSpecForm.published}
              disabled={!publicTemplate && !runnerSpecForm.published}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({
                  ...current,
                  published: event.target.checked,
                }))
              }
            />
            {t("admin.publishRunnerSpec")}
          </label>
          <p className="text-xs text-muted-foreground">
            {t("admin.publishRunnerSpecDescription")}
          </p>
        </div>

        <div className="grid gap-2">
          <label className="flex items-center gap-2 text-sm">
            <input
              id="runner-spec-enabled"
              type="checkbox"
              checked={runnerSpecForm.enabled}
              onChange={(event) =>
                onRunnerSpecFormChange((current) => ({
                  ...current,
                  enabled: event.target.checked,
                }))
              }
            />
            {t("common.enabled")}
          </label>
        </div>

        <DialogFooter>
          <Button
            type="button"
            variant="outline"
            disabled={savingRunnerSpec}
            onClick={() => onRunnerSpecOpenChange(false)}
          >
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

function RunnerSpecCard({
  runnerSpec,
  request,
  templateLookup,
  refreshVersion,
  onEdit,
  onDelete,
}: {
  runnerSpec: RunnerSpec
  request: (url: string, options?: RequestInit) => Promise<unknown>
  templateLookup: TemplateLookup
  refreshVersion: number
  onEdit: () => void
  onDelete: () => void
}) {
  const { t } = useTranslation()
  const template = runnerSpec.default_template_name || runnerSpec.template_id
  const { status, retry } = useAdminTemplateStatus(
    request,
    template,
    runnerSpec.default_template_name ? "name" : "id",
    templateLookup,
    refreshVersion,
  )
  return (
    <article
      aria-label={runnerSpec.name}
      className="grid min-w-0 gap-3 rounded-xl border bg-card p-4 text-card-foreground sm:p-5"
    >
      <div className="flex items-start justify-between gap-3">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <h2 className="min-w-0 text-base font-semibold [overflow-wrap:anywhere]">
            {runnerSpec.name}
          </h2>
          <Badge variant={runnerSpec.enabled ? "success" : "secondary"}>
            {runnerSpec.enabled ? t("common.enabled") : t("common.disabled")}
          </Badge>
          <Badge variant={runnerSpec.published ? "success" : "outline"}>
            {t("admin.catalogDisplayStatus", {
              status: runnerSpec.published
                ? t("admin.catalogDisplayOn")
                : t("admin.catalogDisplayOff"),
            })}
          </Badge>
        </div>
        <div className="flex shrink-0 gap-2">
          <Button
            type="button"
            variant="outline"
            size="icon"
            className="size-8"
            aria-label={t("admin.editNamedRunnerSpec", {
              name: runnerSpec.name,
            })}
            title={t("admin.editNamedRunnerSpec", { name: runnerSpec.name })}
            onClick={onEdit}
          >
            <Pencil />
          </Button>
          <Button
            type="button"
            variant="outline"
            size="icon"
            className="size-8"
            aria-label={t("admin.deleteNamedRunnerSpec", {
              name: runnerSpec.name,
            })}
            title={t("admin.deleteNamedRunnerSpec", { name: runnerSpec.name })}
            onClick={onDelete}
          >
            <Trash2 />
          </Button>
        </div>
      </div>
      <dl className="grid min-w-0 gap-x-6 gap-y-2 text-sm sm:grid-cols-[minmax(0,2fr)_minmax(0,1fr)]">
        {[
          [t("common.labels"), runnerSpec.labels.join(", ")],
          [
            t("admin.requiredLabels"),
            runnerSpec.required_labels.join(", ") || "—",
          ],
        ].map(([label, value]) => (
          <div
            key={label}
            className="grid min-w-0 grid-cols-[auto_minmax(0,1fr)] gap-x-3"
          >
            <dt className="text-muted-foreground">{label}</dt>
            <dd className="min-w-0 [overflow-wrap:anywhere]">{value}</dd>
          </div>
        ))}
      </dl>
      <dl className="flex min-w-0 flex-wrap gap-x-6 gap-y-2 text-sm">
        {[
          [t("admin.maxConcurrency"), runnerSpec.max_concurrency],
          [t("admin.minIdle"), runnerSpec.min_idle],
          [t("admin.priority"), runnerSpec.priority],
          [t("admin.githubRunnerGroup"), runnerSpec.runner_group || "—"],
        ].map(([label, value]) => (
          <div key={label} className="flex min-w-0 gap-x-2">
            <dt className="shrink-0 text-muted-foreground">{label}</dt>
            <dd className="min-w-0 [overflow-wrap:anywhere]">{value}</dd>
          </div>
        ))}
      </dl>
      <div className="grid min-w-0 gap-2 border-t pt-3">
        <AdminTemplateInfo
          status={status}
          empty={!template}
          onRetry={retry}
          showSuccessMessage={false}
          reference={template || "—"}
          variant="plain"
        />
      </div>
    </article>
  )
}

export function RunnerSpecsSection({
  request,
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
  request: (url: string, options?: RequestInit) => Promise<unknown>
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
  const { t } = useTranslation()
  const templateLookup = useMemo(() => createTemplateLookup(request), [request])
  const [refreshVersion, setRefreshVersion] = useState(0)
  const [deletingRunnerSpecName, setDeletingRunnerSpecName] = useState<
    string | null
  >(null)
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
              onClick={() => {
                templateLookup.invalidate()
                setRefreshVersion((current) => current + 1)
                onRefresh()
              }}
              disabled={loading}
              title={t("common.refresh")}
            >
              <RefreshCw className={cn(loading && "animate-spin")} />
            </Button>
          </div>
        </CardHeader>
      </Card>
      <div className="grid min-w-0 gap-4" aria-busy={loading}>
        {runnerSpecs.map((runnerSpec) => (
          <RunnerSpecCard
            key={runnerSpec.name}
            runnerSpec={runnerSpec}
            request={request}
            templateLookup={templateLookup}
            refreshVersion={refreshVersion}
            onEdit={() => onEditRunnerSpec(runnerSpec)}
            onDelete={() => setDeletingRunnerSpecName(runnerSpec.name)}
          />
        ))}
        {!runnerSpecs.length ? (
          <p
            className="py-8 text-center text-sm text-muted-foreground"
            role="status"
          >
            {loading ? t("common.loading") : t("admin.noRunnerSpecs")}
          </p>
        ) : null}
      </div>
      <Dialog
        open={deletingRunnerSpecName !== null}
        onOpenChange={(open) => {
          if (!open) setDeletingRunnerSpecName(null)
        }}
      >
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("admin.deleteRunnerSpecTitle")}</DialogTitle>
            <DialogDescription>
              {t("admin.confirmDeleteRunnerSpec", {
                name: deletingRunnerSpecName || "",
              })}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button
              type="button"
              variant="outline"
              onClick={() => setDeletingRunnerSpecName(null)}
            >
              {t("common.cancel")}
            </Button>
            <Button
              type="button"
              variant="destructive"
              onClick={() => {
                if (deletingRunnerSpecName !== null) {
                  onDeleteRunnerSpec(deletingRunnerSpecName)
                  setDeletingRunnerSpecName(null)
                }
              }}
            >
              {t("common.delete")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog
        open={runnerSpecOpen}
        onOpenChange={(open) => {
          if (!savingRunnerSpec) onRunnerSpecOpenChange(open)
        }}
      >
        <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto sm:max-w-3xl">
          <DialogHeader>
            <DialogTitle>
              {editingRunnerSpec
                ? t("admin.editRunnerSpec")
                : t("admin.createRunnerSpec")}
            </DialogTitle>
            <DialogDescription>
              {t("admin.specDialogDescription")}
            </DialogDescription>
          </DialogHeader>
          <RunnerSpecDialogForm
            request={request}
            templateLookup={templateLookup}
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
