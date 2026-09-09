import { Boxes, Copy, Loader2, Pencil, Plus, Trash2 } from "lucide-react"
import { useCallback, useEffect, useMemo, useRef, useState, type FormEvent } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import type { SandboxTemplate, UserRunnerSpec, UserRunnerSpecList } from "@/admin-types"
import { newRunnerSpecFormState, runnerSpecCatalogRegion, runnerSpecDialogDescriptionKey, runnerSpecOverridesGlobal, runnerSpecWorkflowYAML, type UserRunnerSpecFormState } from "@/components/user-runner-specs-utils"
import { useSandboxRegions } from "@/components/sandbox-catalog-utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

type RequestFn = (url: string, options?: RequestInit) => Promise<unknown>
type FormState = UserRunnerSpecFormState

function parseLabels(value: string) {
  return value.split(",").map((label) => label.trim()).filter(Boolean)
}

function sandboxTemplateOptionLabel(template: SandboxTemplate) {
  const alias = template.aliases?.find((value) => value.trim())?.trim()
  return alias ? `${alias} · ${template.template_id}` : template.template_id
}

export function RunnerSpecDetails({ item }: { item: UserRunnerSpec }) {
  const { t } = useTranslation()
  const template = item.default_template_name || item.template_id || "-"
  if (item.source === "scoped_custom") {
    return <div className="mt-2 grid gap-1 text-xs text-muted-foreground sm:grid-cols-2">
      <span>{t("user.maxConcurrency")}: {item.max_concurrency || t("user.unlimited")}</span>
      <span>{t("common.template")}: {template}</span>
      {item.runner_group ? <span>{t("user.runnerGroup")}: {item.runner_group}</span> : null}
    </div>
  }
  return template === "-" ? null : <div className="mt-2 text-xs text-muted-foreground">{t("common.template")}: {template}</div>
}

export function RunnerSpecItem({ item, onCopy, onEdit, onDelete, copying }: {
  item: UserRunnerSpec
  onCopy: (item: UserRunnerSpec) => void
  onEdit: (item: UserRunnerSpec) => void
  onDelete: (item: UserRunnerSpec) => void
  copying: boolean
}) {
  const { t } = useTranslation()
  return <div className="flex min-w-0 flex-wrap items-center justify-between gap-3 rounded-md border p-3">
    <div className="min-w-0"><div className="flex flex-wrap items-center gap-2"><span className="font-medium">{item.name}</span>{item.source === "scoped_custom" ? <><Badge variant={item.enabled ? "default" : "secondary"}>{item.enabled ? t("common.enabled") : t("common.disabled")}</Badge><Badge variant="outline">{t("user.runnerSpecSourceCustom")}</Badge>{item.overrides_global ? <Badge variant="outline">{t("user.overridesGlobal")}</Badge> : null}</> : null}</div><div className="mt-1 break-words font-mono text-xs text-muted-foreground">{t("user.runsOn")}: [{item.workflow_labels.join(", ")}]</div><RunnerSpecDetails item={item} /></div>
    <div className="flex items-center gap-1"><Button type="button" size="icon" variant="ghost" onClick={() => onCopy(item)} disabled={copying} title={t("user.copyRunnerSpecYAML")} aria-label={t("user.copyRunnerSpecYAML")}><Copy className="h-4 w-4" /></Button>{item.source === "scoped_custom" ? <><Button type="button" size="icon" variant="ghost" onClick={() => onEdit(item)} title={t("common.edit")} aria-label={`${t("common.edit")} ${item.name}`}><Pencil className="h-4 w-4" /></Button><Button type="button" size="icon" variant="ghost" onClick={() => onDelete(item)} title={t("common.delete")} aria-label={`${t("common.delete")} ${item.name}`}><Trash2 className="h-4 w-4" /></Button></> : null}</div>
  </div>
}

export function RunnerSpecForm({ mode, form, templates, saving, allowRunnerGroup = false, sandboxTemplatesHref, onChange, onSubmit, onClose }: {
  mode: "create" | "edit"
  form: FormState
  templates: SandboxTemplate[]
  saving: boolean
  allowRunnerGroup?: boolean
  sandboxTemplatesHref?: string
  onChange: (next: Partial<FormState>) => void
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
  onClose: () => void
}) {
  const { t } = useTranslation()
  const workflowPreview = runnerSpecWorkflowYAML(parseLabels(form.labels))
  const submitLabel = mode === "create" ? t("user.createCustomRunnerSpec") : t("user.saveChanges")

  return <form onSubmit={onSubmit} className="grid gap-5">
    <>
      <div className="grid gap-1.5">
        <Label htmlFor="user-runner-name">{t("common.name")}</Label>
        <Input id="user-runner-name" value={form.name} placeholder={t("user.runnerSpecNamePlaceholder")} onChange={(event) => onChange({ name: event.target.value })} required disabled={mode === "edit"} />
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="user-runner-labels">{t("user.runnerSpecLabels")}</Label>
        <Input id="user-runner-labels" value={form.labels} placeholder={t("user.runnerSpecLabelsPlaceholder")} onChange={(event) => onChange({ labels: event.target.value })} required aria-describedby="user-runner-labels-help" />
        <p id="user-runner-labels-help" className="text-xs leading-5 text-muted-foreground">{t("user.runnerSpecLabelsDescription")}</p>
        <div className="flex min-w-0 items-start justify-between gap-4 rounded-md bg-muted/50 px-3 py-2">
          <span className="shrink-0 text-[11px] font-medium uppercase tracking-wide text-muted-foreground">{t("user.workflowPreview")}</span>
          <code className="min-w-0 break-all text-right text-xs leading-5">{workflowPreview}</code>
        </div>
      </div>
    </>

    <div className="grid gap-4">
      <div className="grid gap-1.5">
        <div className="flex items-center justify-between gap-3">
          <Label htmlFor="user-runner-template">{t("common.template")}</Label>
          {sandboxTemplatesHref ? <a className="text-xs text-primary underline-offset-4 hover:underline" href={sandboxTemplatesHref}>{t("user.manageSandboxTemplates")}</a> : null}
        </div>
        {templates.length > 0 ? <select id="user-runner-template" className="h-9 min-w-0 rounded-md border bg-background px-3 text-sm" value={form.templateID} onChange={(event) => onChange({ templateID: event.target.value })} required><option value="">{t("user.selectTemplate")}</option>{templates.map((template) => <option key={template.template_id} value={template.template_id}>{sandboxTemplateOptionLabel(template)}</option>)}</select> : <Input id="user-runner-template" value={form.templateID} onChange={(event) => onChange({ templateID: event.target.value })} required />}
      </div>
      <div className="grid gap-1.5">
        <Label htmlFor="user-runner-concurrency">{t("user.maxConcurrency")}</Label>
        <Input id="user-runner-concurrency" inputMode="numeric" min="0" type="number" value={form.maxConcurrency} onChange={(event) => onChange({ maxConcurrency: event.target.value })} aria-describedby="user-runner-concurrency-help" />
        <p id="user-runner-concurrency-help" className="text-xs leading-5 text-muted-foreground">{t("user.maxConcurrencyDescription")}</p>
      </div>
    </div>

    {allowRunnerGroup ? <div className="grid gap-1.5"><Label htmlFor="user-runner-group">{t("user.runnerGroup")}</Label><Input id="user-runner-group" value={form.runnerGroup} onChange={(event) => onChange({ runnerGroup: event.target.value })} /></div> : null}

    <label className="flex cursor-pointer items-start gap-3 rounded-md border bg-muted/20 px-3 py-2.5 text-sm">
      <input className="mt-0.5" type="checkbox" checked={form.enabled} onChange={(event) => onChange({ enabled: event.target.checked })} aria-describedby="user-runner-enabled-description" />
      <span><span className="block font-medium">{t("common.enabled")}</span><span id="user-runner-enabled-description" className="mt-0.5 block text-xs leading-5 text-muted-foreground">{t("user.runnerSpecEnabledDescription")}</span></span>
    </label>

    <DialogFooter className="border-t pt-4"><Button type="button" variant="outline" onClick={onClose}>{t("common.cancel")}</Button><Button type="submit" disabled={saving}>{saving ? t("admin.saving") : submitLabel}</Button></DialogFooter>
  </form>
}

export function UserRunnerSpecsSection({ request, installationID, scopeName, sandboxServiceHref, sandboxTemplatesHref, view = "custom" }: { request: RequestFn; installationID?: number; scopeName?: string; sandboxServiceHref?: string; sandboxTemplatesHref?: string; view?: "custom" | "platform" }) {
  const { t } = useTranslation()
  const sandboxRegions = useSandboxRegions()
  const [items, setItems] = useState<UserRunnerSpec[]>([])
  const [sandboxSource, setSandboxSource] = useState("none")
  const [sandboxRegion, setSandboxRegion] = useState("us-south-1")
  const [templates, setTemplates] = useState<SandboxTemplate[]>([])
  const [loading, setLoading] = useState(true)
  const [loadedQuery, setLoadedQuery] = useState<string | null>(null)
  const [loadError, setLoadError] = useState<{ query: string; message: string } | null>(null)
  const [copying, setCopying] = useState<string | null>(null)
  const [saving, setSaving] = useState(false)
  const [dialog, setDialog] = useState<"create" | "edit" | null>(null)
  const [selected, setSelected] = useState<UserRunnerSpec | null>(null)
  const [form, setForm] = useState<FormState>(newRunnerSpecFormState)
  const query = installationID ? `?installation_id=${installationID}` : ""
  const loadGeneration = useRef(0)
  const currentQuery = useRef(query)
  currentQuery.current = query

  const load = useCallback(async () => { const generation = ++loadGeneration.current; setLoading(true); setLoadError(null); try { const response = await request(`/user/runner-specs${query}`) as UserRunnerSpecList; if (generation !== loadGeneration.current || currentQuery.current !== query) return; setItems(response.items || []); setSandboxSource(response.sandbox_source || "none"); setSandboxRegion(runnerSpecCatalogRegion(response.sandbox_region || "", sandboxRegions)); setLoadedQuery(query) } catch (error) { if (generation === loadGeneration.current && currentQuery.current === query) { const message = error instanceof Error ? error.message : t("user.runnerSpecsLoadFailed"); setLoadError({ query, message }) } } finally { if (generation === loadGeneration.current && currentQuery.current === query) setLoading(false) } }, [query, request, sandboxRegions, t])
  useEffect(() => { const generationRef = loadGeneration; setDialog(null); setSelected(null); setTemplates([]); setSaving(false); setLoadError(null); void load(); return () => { generationRef.current++ } }, [load])
  const openCreate = async () => { const operationQuery = query; setSelected(null); setForm(newRunnerSpecFormState()); setDialog("create"); try { const suffix = installationID ? `&installation_id=${installationID}` : ""; const data = await request(`/user/sandbox/templates?region=${encodeURIComponent(sandboxRegion)}${suffix}`); if (currentQuery.current !== operationQuery) return; setTemplates(Array.isArray(data) ? data as SandboxTemplate[] : []) } catch (error) { if (currentQuery.current !== operationQuery) return; setTemplates([]); toast.error(error instanceof Error ? error.message : t("user.runnerSpecsLoadFailed")) } }
  const openEdit = (item: UserRunnerSpec) => { setSelected(item); setForm({ name: item.name, labels: item.workflow_labels.join(", "), templateID: item.template_id || "", runnerGroup: item.runner_group || "", maxConcurrency: String(item.max_concurrency), enabled: item.enabled }); setDialog("edit") }
  const closeDialog = () => { if (!saving) setDialog(null) }
  const submit = async (event: FormEvent<HTMLFormElement>) => { event.preventDefault(); if (saving) return; const operationQuery = query; const labels = parseLabels(form.labels); if (runnerSpecOverridesGlobal(labels, items) && !window.confirm(t("user.confirmRunnerSpecOverride"))) return; setSaving(true); try { const runnerGroup = installationID ? form.runnerGroup : undefined; let url = `/user/runner-specs${query}`; let options: RequestInit = { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name: form.name, workflow_labels: labels, template_id: form.templateID, runner_group: runnerGroup, max_concurrency: Number(form.maxConcurrency) || 0, enabled: form.enabled }) }; if (dialog === "edit" && selected) { url = `/user/runner-specs/${encodeURIComponent(selected.name)}${query}`; options = { method: "PATCH", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ workflow_labels: labels, template_id: form.templateID, runner_group: runnerGroup, max_concurrency: Number(form.maxConcurrency) || 0, enabled: form.enabled, expected_updated_at: selected.updated_at }) } } await request(url, options); if (currentQuery.current !== operationQuery) return; toast.success(t("user.runnerSpecSaved")); setDialog(null); await load() } catch (error) { if (currentQuery.current === operationQuery) toast.error(error instanceof Error ? error.message : t("user.runnerSpecSaveFailed")) } finally { if (currentQuery.current === operationQuery) setSaving(false) } }
  const remove = async (item: UserRunnerSpec) => { if (saving || !window.confirm(t("user.confirmDeleteRunnerSpec", { name: item.name }))) return; const operationQuery = query; setSaving(true); try { const separator = query ? "&" : "?"; await request(`/user/runner-specs/${encodeURIComponent(item.name)}${query}${separator}expected_updated_at=${encodeURIComponent(item.updated_at)}`, { method: "DELETE" }); if (currentQuery.current !== operationQuery) return; toast.success(t("user.runnerSpecDeleted")); await load() } catch (error) { if (currentQuery.current === operationQuery) toast.error(error instanceof Error ? error.message : t("user.runnerSpecDeleteFailed")) } finally { if (currentQuery.current === operationQuery) setSaving(false) } }
  const copyYAML = async (item: UserRunnerSpec) => { setCopying(item.name); try { await navigator.clipboard?.writeText(runnerSpecWorkflowYAML(item.workflow_labels)); toast.success(t("user.runnerSpecCopied")) } catch { toast.error(t("user.runnerSpecCopyFailed")) } finally { setCopying(null) } }
  const scopeLoaded = loadedQuery === query
  const currentLoadError = loadError?.query === query ? loadError.message : null
  const scopeLoading = loading || (!scopeLoaded && !currentLoadError)
  const needsSandboxConfig = scopeLoaded && (sandboxSource === "none" || sandboxSource === "admin_default")
  const canCreate = scopeLoaded && !needsSandboxConfig
  const customItems = useMemo(() => scopeLoaded ? items.filter((item) => item.source === "scoped_custom").sort((a, b) => a.name.localeCompare(b.name)) : [], [items, scopeLoaded])
  const platformItems = useMemo(() => scopeLoaded ? items.filter((item) => item.source !== "scoped_custom" && item.enabled).sort((a, b) => a.name.localeCompare(b.name)) : [], [items, scopeLoaded])
  const displayScopeName = scopeName?.trim() || t("user.currentScope")

  const renderItem = (item: UserRunnerSpec) => <RunnerSpecItem key={`${item.source}:${item.name}`} item={item} copying={copying === item.name} onCopy={(value) => void copyYAML(value)} onEdit={openEdit} onDelete={(value) => void remove(value)} />

  return <div className="grid gap-4">
    {currentLoadError ? <div role="alert" className="rounded-lg border border-destructive/30 bg-destructive/5 px-4 py-3"><p className="text-sm font-medium text-destructive">{t("user.runnerSpecsLoadErrorTitle")}</p><p className="mt-1 text-xs text-muted-foreground">{currentLoadError}</p><Button type="button" variant="link" size="sm" className="mt-1 h-auto px-0" onClick={() => void load()}>{t("user.retry")}</Button></div> : null}

    {view === "custom" ? <Card data-runner-spec-section="custom" className="min-w-0 rounded-lg">
      <CardHeader className="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
        <div className="flex min-w-0 items-start gap-3">
          <div className="rounded-md bg-primary/10 p-2 text-primary"><Boxes className="h-4 w-4" aria-hidden="true" /></div>
          <div>
            <CardTitle>{t("user.customRunnerSpecs")}</CardTitle>
            <p className="mt-1 text-sm leading-5 text-muted-foreground">{t("user.runnerSpecsScopeDescription", { scope: displayScopeName })}</p>
          </div>
        </div>
        <Button type="button" size="sm" disabled={!canCreate || saving} title={needsSandboxConfig ? t("user.runnerSpecsConfigureSandbox") : undefined} onClick={() => void openCreate()}><Plus className="h-4 w-4" />{t("user.createCustomRunnerSpec")}</Button>
      </CardHeader>
      <CardContent>
        {scopeLoading ? <div className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" />{t("common.loading")}</div> : null}
        {!scopeLoading && !currentLoadError && customItems.length === 0 ? <div className="rounded-md border border-dashed bg-muted/20 px-4 py-6 text-center"><p className="text-sm font-medium">{t("user.noCustomRunnerSpecs")}</p><p className="mt-1 text-xs text-muted-foreground">{canCreate ? t("user.noCustomRunnerSpecsDescription") : t("user.runnerSpecsConfigureSandbox")}</p>{sandboxServiceHref && !canCreate ? <Button type="button" variant="link" size="sm" asChild><a href={sandboxServiceHref}>{t("user.configureSandboxService")}</a></Button> : null}</div> : null}
        <div className="grid gap-3">{customItems.map(renderItem)}</div>
      </CardContent>
    </Card> : null}

    {view === "platform" ? <Card data-runner-spec-section="platform" className="min-w-0 rounded-lg shadow-none">
      <CardContent>
        {scopeLoading ? <div className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" />{t("common.loading")}</div> : null}
        {!scopeLoading && !currentLoadError && platformItems.length === 0 ? <p className="text-sm text-muted-foreground">{t("user.noPlatformRunnerSpecs")}</p> : null}
        <div className="grid gap-3">{platformItems.map(renderItem)}</div>
      </CardContent>
    </Card> : null}

    <Dialog open={scopeLoaded && dialog !== null} onOpenChange={(open) => { if (!open) closeDialog() }}><DialogContent className="max-h-[calc(100vh-2rem)] overflow-y-auto sm:max-w-xl"><DialogHeader className="gap-1.5 pr-8"><DialogTitle>{dialog === "create" ? t("user.createCustomRunnerSpec") : t("common.edit")}</DialogTitle><DialogDescription className="leading-5">{t(runnerSpecDialogDescriptionKey(dialog || "create"))}</DialogDescription></DialogHeader><RunnerSpecForm mode={dialog || "create"} form={form} templates={templates} saving={saving} allowRunnerGroup={Boolean(installationID)} sandboxTemplatesHref={sandboxTemplatesHref} onChange={(next) => setForm((current) => ({ ...current, ...next }))} onSubmit={submit} onClose={closeDialog} /></DialogContent></Dialog>
  </div>
}
