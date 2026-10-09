import {
  type ComponentProps,
  type Dispatch,
  type FormEvent,
  type SetStateAction,
  useState,
} from "react"
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
  template_source: "public" | "private"
  default_template_name: string
  published: boolean

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
  const publicTemplate = runnerSpecForm.template_source === "public"

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

        <div className="grid gap-2">
          <Label htmlFor="runner-spec-template-source">
            {t("admin.templateBinding")}
          </Label>
          <select
            id="runner-spec-template-source"
            className="h-9 rounded-md border bg-background px-3 text-sm"
            value={runnerSpecForm.template_source || "private"}
            onChange={(event) =>
              onRunnerSpecFormChange((current) => ({
                ...current,
                template_source: event.target.value as "public" | "private",
                template_id: "",
                default_template_name: "",
                published: false,
              }))
            }
          >
            <option value="private">{t("admin.privateTemplateID")}</option>
            <option value="public">{t("admin.publicTemplateName")}</option>
          </select>
        </div>
        <div className="grid gap-4">
          {publicTemplate ? (
            <div className="grid gap-2">
              <Label htmlFor="runner-spec-default-template">
                {t("admin.defaultTemplate")}
              </Label>
              <RunnerSpecTextField
                id="runner-spec-default-template"
                value={runnerSpecForm.default_template_name || ""}
                onChange={(event) =>
                  onRunnerSpecFormChange((current) => ({
                    ...current,
                    default_template_name: event.target.value,
                  }))
                }
              />
            </div>
          ) : (
            <div className="grid gap-2">
              <Label htmlFor="runner-spec-template-id">
                {t("admin.templateID")}
              </Label>
              <RunnerSpecTextField
                id="runner-spec-template-id"
                value={runnerSpecForm.template_id}
                onChange={(event) =>
                  onRunnerSpecFormChange((current) => ({
                    ...current,
                    template_id: event.target.value,
                  }))
                }
                placeholder={t("admin.templateIDPlaceholder")}
                aria-describedby="runner-spec-template-help"
              />
              <p
                id="runner-spec-template-help"
                className="text-xs text-muted-foreground"
              >
                {t("admin.templateValidationDescription")}{" "}
                <a
                  href="/admin/sandbox_service"
                  className="underline underline-offset-2"
                >
                  {t("admin.configureTemplateValidation")}
                </a>
              </p>
            </div>
          )}
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
              disabled={!publicTemplate}
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
                <TableHead>{t("common.status")}</TableHead>
                <TableHead>{t("admin.publishedSpec")}</TableHead>
                <TableHead>{t("common.labels")}</TableHead>
                <TableHead>{t("common.template")}</TableHead>
                <TableHead>{t("admin.githubGroup")}</TableHead>
                <TableHead>{t("admin.maxConcurrency")}</TableHead>
                <TableHead>{t("admin.minIdle")}</TableHead>
                <TableHead>{t("admin.priority")}</TableHead>
                <TableHead className="w-24">
                  <span className="sr-only">{t("common.actions")}</span>
                </TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {runnerSpecs.map((runnerSpec) => {
                const publicTemplate = runnerSpec.template_source === "public"
                return (
                  <TableRow
                    key={runnerSpec.name}
                    className="cursor-pointer"
                    onClick={() => onEditRunnerSpec(runnerSpec)}
                  >
                    <TableCell>
                      <div className="flex max-w-[240px] items-center gap-2">
                        <span className="truncate">{runnerSpec.name}</span>
                      </div>
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant={runnerSpec.enabled ? "success" : "secondary"}
                      >
                        {runnerSpec.enabled
                          ? t("common.enabled")
                          : t("common.disabled")}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <Badge
                        variant={runnerSpec.published ? "secondary" : "outline"}
                      >
                        {runnerSpec.published
                          ? t("admin.catalogDisplayOn")
                          : t("admin.catalogDisplayOff")}
                      </Badge>
                    </TableCell>
                    <TableCell>
                      <div
                        className="max-w-[176px] truncate"
                        title={runnerSpec.labels.join(", ")}
                      >
                        {runnerSpec.labels.join(", ")}
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className="max-w-[240px]">
                        <div className="text-[11px] font-medium uppercase tracking-wide text-muted-foreground">
                          {publicTemplate
                            ? t("admin.defaultTemplate")
                            : t("admin.templateID")}
                        </div>
                        <div
                          className="truncate"
                          title={
                            publicTemplate
                              ? runnerSpec.default_template_name || "—"
                              : runnerSpec.template_id
                          }
                        >
                          {publicTemplate
                            ? runnerSpec.default_template_name || "—"
                            : runnerSpec.template_id}
                        </div>
                      </div>
                    </TableCell>
                    <TableCell>
                      <div className="max-w-[220px] truncate">
                        {runnerSpec.runner_group || "-"}
                      </div>
                    </TableCell>
                    <TableCell>{runnerSpec.max_concurrency}</TableCell>
                    <TableCell>{runnerSpec.min_idle}</TableCell>
                    <TableCell>{runnerSpec.priority}</TableCell>
                    <TableCell>
                      <div className="flex justify-end gap-2">
                        <Button
                          type="button"
                          variant="outline"
                          size="icon"
                          className="size-8"
                          aria-label={t("admin.editNamedRunnerSpec", {
                            name: runnerSpec.name,
                          })}
                          title={t("admin.editNamedRunnerSpec", {
                            name: runnerSpec.name,
                          })}
                          onClick={(event) => {
                            event.stopPropagation()
                            onEditRunnerSpec(runnerSpec)
                          }}
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
                          title={t("admin.deleteNamedRunnerSpec", {
                            name: runnerSpec.name,
                          })}
                          onClick={(event) => {
                            event.stopPropagation()
                            setDeletingRunnerSpecName(runnerSpec.name)
                          }}
                        >
                          <Trash2 />
                        </Button>
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
