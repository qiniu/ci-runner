import { type FormEvent, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { type RunnerSpec } from "@/admin-types"
import { type RunnerSpecFormState } from "@/components/runner-specs-section"

type RequestFn = (url: string, options?: RequestInit) => Promise<unknown>

export async function submitRunnerSpecChanges({
  request,
  editingRunnerSpec,
  runnerSpecForm,
  parseLabels,
}: {
  request: RequestFn
  editingRunnerSpec: RunnerSpec | null
  runnerSpecForm: RunnerSpecFormState
  parseLabels: (value: string) => string[]
}) {
  const name = editingRunnerSpec?.name || runnerSpecForm.name.trim()
  const publicTemplate = runnerSpecForm.template_source === "public"
  const payload = {
    ...(editingRunnerSpec ? { expected_updated_at: editingRunnerSpec.updated_at } : { name }),
    labels: parseLabels(runnerSpecForm.labels), required_labels: parseLabels(runnerSpecForm.required_labels),
    template_source: runnerSpecForm.template_source || "private",
    template_id: publicTemplate ? "" : runnerSpecForm.template_id.trim(),
    default_template_name: publicTemplate ? (runnerSpecForm.default_template_name || "").trim() : "",
    published: publicTemplate && Boolean(runnerSpecForm.published),
    runner_update_policy: runnerSpecForm.runner_update_policy || "official",
    require_docker: Boolean(runnerSpecForm.require_docker),
    fork_sponsorship: publicTemplate && Boolean(runnerSpecForm.fork_sponsorship),
    runner_group: runnerSpecForm.runner_group.trim(),
    max_concurrency: Number(runnerSpecForm.max_concurrency) || 0,
    min_idle: Number(runnerSpecForm.min_idle) || 0, priority: Number(runnerSpecForm.priority) || 0,
    enabled: runnerSpecForm.enabled,
  }
  await request(editingRunnerSpec ? `/runner_specs/${encodeURIComponent(name)}` : "/runner_specs", {
    method: editingRunnerSpec ? "PATCH" : "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  })
  return name
}

export function useRunnerCatalog({
  request,
  loadAll,
  setSection,
  parseLabels,
}: {
  request: RequestFn
  loadAll: () => Promise<void>
  setSection: (next: "runner_specs") => void
  parseLabels: (value: string) => string[]
}) {
  const { t } = useTranslation()
  const [runnerSpecOpen, setRunnerSpecOpen] = useState(false)
  const [savingRunnerSpec, setSavingRunnerSpec] = useState(false)
  const savingRunnerSpecRef = useRef(false)
  const [editingRunnerSpec, setEditingRunnerSpec] = useState<RunnerSpec | null>(null)
  const [runnerSpecForm, setRunnerSpecForm] = useState<RunnerSpecFormState>({
    template_source: "private", default_template_name: "", published: false,
    runner_update_policy: "official", require_docker: false, fork_sponsorship: false,
    name: "",
    labels: "self-hosted,e2b",
    required_labels: "",
    template_id: "",
    runner_group: "",
    max_concurrency: "10",
    min_idle: "0",
    priority: "0",
    enabled: true,
  })

  const resetRunnerSpecForm = () => {
    setEditingRunnerSpec(null)
    setRunnerSpecForm({
      template_source: "private", default_template_name: "", published: false,
    runner_update_policy: "official", require_docker: false, fork_sponsorship: false,
    name: "",
      labels: "self-hosted,e2b",
      required_labels: "",
      template_id: "",
      runner_group: "",
      max_concurrency: "10",
      min_idle: "0",
      priority: "0",
      enabled: true,
    })
  }

  const saveRunnerSpec = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (savingRunnerSpecRef.current) return
    savingRunnerSpecRef.current = true
    setSavingRunnerSpec(true)
    try {
      const name = await submitRunnerSpecChanges({
        request,
        editingRunnerSpec,
        runnerSpecForm,
        parseLabels,
      })
      toast.success(t("admin.runnerSpecSaved", { name }))
      setRunnerSpecOpen(false)
      await loadAll()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("admin.saveRunnerSpecFailed"))
    } finally {
      savingRunnerSpecRef.current = false
      setSavingRunnerSpec(false)
    }
  }

  const loadRunnerSpecIntoForm = (runnerSpec: RunnerSpec) => {
    setSection("runner_specs")
    setEditingRunnerSpec(runnerSpec)
    setRunnerSpecForm({
      template_source: runnerSpec.template_source, default_template_name: runnerSpec.default_template_name || "", published: runnerSpec.published,
      runner_update_policy: runnerSpec.runner_update_policy, require_docker: runnerSpec.require_docker, fork_sponsorship: runnerSpec.fork_sponsorship,
      name: runnerSpec.name,
      labels: runnerSpec.labels.join(","),
      required_labels: runnerSpec.required_labels.join(","),
      template_id: runnerSpec.template_id,
      runner_group: runnerSpec.runner_group || "",
      max_concurrency: String(runnerSpec.max_concurrency),
      min_idle: String(runnerSpec.min_idle),
      priority: String(runnerSpec.priority),
      enabled: runnerSpec.enabled,
    })
    setRunnerSpecOpen(true)
  }

  const deleteRunnerSpec = async (name: string) => {
    try {
      await request(`/runner_specs/${encodeURIComponent(name)}`, { method: "DELETE" })
      toast.success(t("admin.runnerSpecDeleted", { name }))
      if (runnerSpecForm.name === name) resetRunnerSpecForm()
      await loadAll()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("admin.deleteRunnerSpecFailed"))
    }
  }

  return {
    runnerSpecOpen,
    savingRunnerSpec,
    editingRunnerSpec,
    runnerSpecForm,
    setRunnerSpecOpen,
    setRunnerSpecForm,
    resetRunnerSpecForm,
    saveRunnerSpec,
    loadRunnerSpecIntoForm,
    deleteRunnerSpec,
  }
}
