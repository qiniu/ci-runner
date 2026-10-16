import { deploymentPrefillFromSearch } from "@/github-context-routes"
import { Loader2, Plus, RefreshCw } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import type { GitHubAppConfig } from "@/admin-types"
import { AppLink } from "@/components/app-link"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import type { Deployment } from "@/components/user-deployments-section"
import type { RequestError } from "@/user-auth-errors"
import { toast } from "sonner"

type Request = (url: string, options?: RequestInit) => Promise<unknown>
type DeploymentTemplate = { template_id: string; names?: string[]; aliases?: string[]; cpu_count?: number; memory_mb?: number; disk_size_mb?: number }

export function UserDeploymentCreateSection({ githubApp, currentLogin, loadingGitHubApp, request, onOpenDeployment, locationSearch = "" }: {
  locationSearch?: string
  githubApp: GitHubAppConfig | null
  currentLogin?: string
  loadingGitHubApp: boolean
  request: Request
  onOpenDeployment: (id: string | null) => void
}) {
  const { t } = useTranslation()
  const prefill = useMemo(() => deploymentPrefillFromSearch(locationSearch), [locationSearch])
  const [prefillActive, setPrefillActive] = useState(Boolean(prefill))
  const [commitSHA, setCommitSHA] = useState(prefill?.commitSHA ?? "")
  const prefillInstallation = githubApp?.installations.find((item) => (item.repositories?.includes(prefill?.repository ?? "") || item.account_login?.toLowerCase() === prefill?.repository.split("/")[0]?.toLowerCase()))
  const initialInstallation = prefill ? prefillInstallation : githubApp?.installations.find((item) => item.account_login?.toLowerCase() === currentLogin?.toLowerCase())
    ?? githubApp?.installations[0]
  const [repositories, setRepositories] = useState<string[]>(initialInstallation?.repositories ?? [])
  const [loadingRepositories, setLoadingRepositories] = useState(false)
  const [installationID, setInstallationID] = useState(initialInstallation ? String(initialInstallation.id) : "")
  const [repository, setRepository] = useState(prefill?.repository ?? "")
  const [ref, setRef] = useState(prefill?.ref ?? "main")
  const [source, setSource] = useState<"branch" | "tag" | "pull_request">(prefill?.source ?? "branch")
  const [referenceCatalog, setReferenceCatalog] = useState<{ key: string; refs: string[]; page: number; hasMore: boolean } | null>(null)
  const [loadingReferences, setLoadingReferences] = useState(false)
  const [referenceError, setReferenceError] = useState<string | null>(null)
  const [referenceReload, setReferenceReload] = useState(0)
  const referenceEpoch = useRef(0)
  const [repositoryScopeID, setRepositoryScopeID] = useState(initialInstallation ? String(initialInstallation.id) : "")
  const [configPath, setConfigPath] = useState(".qiniu/deploy.yaml")
  const [ttlSeconds, setTTLSeconds] = useState("3600")
  const [submitting, setSubmitting] = useState(false)
  const [templateCatalog, setTemplateCatalog] = useState<{ key: string; templates: DeploymentTemplate[]; region?: string } | null>(null)
  const [templateID, setTemplateID] = useState("")
  const [loadingTemplates, setLoadingTemplates] = useState(false)
  const [templateError, setTemplateError] = useState<string | null>(null)
  const [templateReload, setTemplateReload] = useState(0)
  const availableInstallations = useMemo(
    () => githubApp?.installations ?? [],
    [githubApp?.installations],
  )
  const selectedInstallation = availableInstallations.find((item) => String(item.id) === installationID)
  const templateKey = `${currentLogin}:${installationID}:${repository}`
  const templates = templateCatalog?.key === templateKey ? templateCatalog.templates : []
  const selectedTemplate = templates.find((item) => item.template_id === templateID)
  const referenceKey = `${currentLogin}:${installationID}:${repository}:${source}`
  const catalogReferences = referenceCatalog?.key === referenceKey ? referenceCatalog.refs : []
  const prefilledRef = prefillActive && prefill?.repository === repository && prefill.source === source ? prefill.ref : ""
  const references = prefilledRef && !catalogReferences.includes(prefilledRef) ? [prefilledRef, ...catalogReferences] : catalogReferences
  const selectedRef = references.includes(ref) ? ref : ""
  const currentRepositories = repositoryScopeID === installationID ? repositories : selectedInstallation?.repositories ?? []
  const referenceErrorMessage = useCallback((cause: unknown) => {
    if (cause instanceof Error && (cause as RequestError).code === "deployment_repository_contents_required") return t("user.deploymentContentsRequired")
    return cause instanceof Error ? cause.message : t("user.deploymentReferencesFailed")
  }, [t])

  useEffect(() => {
    const first = (prefillActive ? prefillInstallation : undefined) ?? availableInstallations.find((item) => item.account_login?.toLowerCase() === currentLogin?.toLowerCase()) ?? availableInstallations[0]
    if ((prefillActive && prefillInstallation && String(prefillInstallation.id) !== installationID) || !installationID || !availableInstallations.some((item) => String(item.id) === installationID)) {
      setInstallationID(first ? String(first.id) : "")
    }
  }, [installationID, availableInstallations, currentLogin, prefillActive, prefillInstallation])

  useEffect(() => {
    if (!selectedInstallation) {
      setRepositories([])
      setLoadingRepositories(false)
      return
    }
    let cancelled = false
    setLoadingRepositories(true)
    setRepositoryScopeID(String(selectedInstallation.id))
    const initial = selectedInstallation.repositories ?? []
    setRepositories(initial)
    request(`/user/github-app/installations/${encodeURIComponent(String(selectedInstallation.id))}/repositories`)
      .then((data) => {
        if (cancelled) return
        const next = (data as { repositories?: unknown }).repositories
        if (!Array.isArray(next)) return
        const values = next.filter((item): item is string => typeof item === "string")
        setRepositories(values)
        setRepository((current) => values.includes(current) ? current : "")
      })
      .catch(() => {
        // The installation payload is still a useful fallback when the detail endpoint is unavailable.
      })
      .finally(() => {
        if (!cancelled) setLoadingRepositories(false)
      })
    return () => { cancelled = true }
  }, [request, selectedInstallation, currentLogin])

  const referencePath = useCallback((page: number) => `/user/deployment-refs?${new URLSearchParams({ installation_id: installationID, repository_full_name: repository, source, page: String(page) })}`, [installationID, repository, source])
  const repositoryIsCurrent = currentRepositories.includes(repository)
  useEffect(() => {
    const epoch = ++referenceEpoch.current
    setReferenceCatalog(null)
    if (prefilledRef) setRef(prefilledRef)
    setReferenceError(null)
    const enabled = Boolean(installationID && repository && repositoryIsCurrent)
    setLoadingReferences(enabled)
    if (!enabled) return
    request(referencePath(1))
      .then((response) => {
        if (epoch !== referenceEpoch.current) return
        const data = response as { refs?: string[]; has_more?: boolean }
        const next = Array.isArray(data.refs) ? data.refs : []
        setReferenceCatalog({ key: referenceKey, refs: next, page: 1, hasMore: Boolean(data.has_more) })
        setRef((current) => prefilledRef || (next.includes(current) ? current : next.includes("main") ? "main" : next[0] ?? ""))
      })
      .catch((cause) => { if (epoch === referenceEpoch.current) setReferenceError(referenceErrorMessage(cause)) })
      .finally(() => { if (epoch === referenceEpoch.current) setLoadingReferences(false) })
    return () => { referenceEpoch.current += 1 }
  }, [request, referencePath, referenceKey, repositoryIsCurrent, installationID, repository, referenceReload, referenceErrorMessage, prefilledRef])

  async function loadMoreReferences() {
    if (loadingReferences || referenceCatalog?.key !== referenceKey || !referenceCatalog.hasMore) return
    const epoch = referenceEpoch.current
    setLoadingReferences(true)
    setReferenceError(null)
    try {
      const page = referenceCatalog.page + 1
      const data = await request(referencePath(page)) as { refs?: string[]; has_more?: boolean }
      if (epoch !== referenceEpoch.current) return
      setReferenceCatalog({ key: referenceKey, refs: [...new Set([...referenceCatalog.refs, ...(data.refs ?? [])])], page, hasMore: Boolean(data.has_more) })
    } catch (cause) {
      if (epoch === referenceEpoch.current) setReferenceError(referenceErrorMessage(cause))
    } finally {
      if (epoch === referenceEpoch.current) setLoadingReferences(false)
    }
  }

  useEffect(() => {
    let cancelled = false
    setTemplateCatalog(null)
    setTemplateID("")
    setTemplateError(null)
    setLoadingTemplates(Boolean(installationID && repository))
    if (!installationID || !repository) return
    request(`/user/deployment-templates?${new URLSearchParams({ installation_id: installationID, repository_full_name: repository })}`)
      .then((response) => {
        if (cancelled) return
        const data = response as { templates?: DeploymentTemplate[]; default_template_id?: string; region?: string }
        const next = Array.isArray(data.templates) ? data.templates : []
        setTemplateCatalog({ key: templateKey, templates: next, region: data.region })
        const preferred = next.find((item) => item.template_id === data.default_template_id)
          ?? next.find((item) => item.names?.includes("base") || item.aliases?.includes("base"))
          ?? next[0]
        setTemplateID(preferred?.template_id ?? "")
      })
      .catch((cause) => {
        if (!cancelled) setTemplateError(cause instanceof Error ? cause.message : t("user.deploymentTemplatesFailed"))
      })
      .finally(() => { if (!cancelled) setLoadingTemplates(false) })
    return () => { cancelled = true }
  }, [request, installationID, repository, templateKey, currentLogin, templateReload, t])

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (!installationID || !repositoryIsCurrent || !repository || !selectedRef || loadingReferences || !selectedTemplate || loadingTemplates || submitting) return
    setSubmitting(true)
    try {
      const created = await request("/user/deployments", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "Idempotency-Key": typeof crypto?.randomUUID === "function" ? crypto.randomUUID() : `${Date.now()}-${Math.random()}`,
        },
        body: JSON.stringify({
          installation_id: Number(installationID),
          repository_full_name: repository,
          ref: selectedRef,
          source,
          ...(commitSHA ? { commit_sha: commitSHA } : {}),
          config_path: configPath.trim(),
          ttl_seconds: Math.max(300, Math.min(86400, Number(ttlSeconds) || 3600)),
          template_id: selectedTemplate.template_id,
          template_name: templateName(selectedTemplate),
          template_cpu: selectedTemplate.cpu_count ?? 0,
          template_memory_mb: selectedTemplate.memory_mb ?? 0,
          template_disk_mb: selectedTemplate.disk_size_mb ?? 0,
        }),
      }) as Deployment
      onOpenDeployment(created.id)
      toast.success(t("user.deploymentCreated"))
    } catch (cause) {
      toast.error(cause instanceof Error ? cause.message : t("user.deploymentCreateFailed"))
    } finally {
      setSubmitting(false)
    }
  }

  function changeSourceSelection(change: () => void) {
    setPrefillActive(false)
    setCommitSHA("")
    change()
  }

  return (
    <div className="min-h-0 flex-1 overflow-y-auto">
      <section className="mx-auto w-full max-w-2xl space-y-4 p-4 lg:p-6">
        <Button asChild variant="outline" className="w-fit"><AppLink href="/deployments">{t("user.deploymentBack")}</AppLink></Button>
        <div>
          <h1 className="text-2xl font-semibold tracking-tight">{t("user.createDeployment")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">{t("user.createDeploymentDescription")}</p>
        </div>
        <Card>
          <CardContent className="pt-6">
            {loadingGitHubApp ? <div className="flex items-center gap-2 text-sm text-muted-foreground"><Loader2 className="h-4 w-4 animate-spin" />{t("user.loadingAccounts")}</div> : (
              <form className="space-y-4" onSubmit={submit}>
                <div className="space-y-2">
                  <Label htmlFor="deployment-installation">{t("user.deploymentScope")}</Label>
                  <Select name="deployment-installation" value={installationID} onValueChange={(value) => { if (value) changeSourceSelection(() => { setInstallationID(value); setRepository("") }) }} disabled={availableInstallations.length === 0}>
                    <SelectTrigger id="deployment-installation" className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      {availableInstallations.map((installation) => <SelectItem key={installation.id} value={String(installation.id)}>{installation.account_login || installation.account_name || installation.installation_id}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="deployment-repository">{t("common.repository")}</Label>
                  <Select name="deployment-repository" value={repository} onValueChange={(value) => { if (value) changeSourceSelection(() => setRepository(value)) }} disabled={loadingRepositories || repositories.length === 0}>
                    <SelectTrigger id="deployment-repository" className="w-full"><SelectValue placeholder={loadingRepositories ? t("repositories.loadingRepositories") : repositories.length === 0 ? t("user.noDeploymentRepositories") : t("user.deploymentSelectRepository")} /></SelectTrigger>
                    <SelectContent>
                      {repositories.map((item) => <SelectItem key={item} value={item}>{item}</SelectItem>)}
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="deployment-source">{t("user.deploymentVersionSource")}</Label>
                  <Select name="deployment-source" value={source} onValueChange={(value) => { if (value) changeSourceSelection(() => setSource(value as "branch" | "tag" | "pull_request")) }}>
                    <SelectTrigger id="deployment-source" className="w-full"><SelectValue /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="branch">{t("user.deploymentBranch")}</SelectItem>
                      <SelectItem value="tag">{t("user.deploymentTag")}</SelectItem>
                      <SelectItem value="pull_request">{t("user.deploymentPullRequest")}</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                {source === "pull_request" ? <p className="text-sm text-muted-foreground">{t("user.deploymentPullRequestHint")}</p> : null}
                <div className="space-y-2">
                  <Label htmlFor="deployment-ref">{t(source === "pull_request" ? "user.deploymentSelectPullRequest" : source === "branch" ? "user.deploymentSelectBranch" : "user.deploymentSelectTag")}</Label>
                  <Select name="deployment-ref" value={selectedRef} onValueChange={(value) => { if (value) changeSourceSelection(() => setRef(value)) }} disabled={loadingReferences || references.length === 0}>
                    <SelectTrigger id="deployment-ref" className="w-full"><SelectValue placeholder={loadingReferences ? t("user.deploymentReferencesLoading") : t("user.deploymentReferencesEmpty")} /></SelectTrigger>
                    <SelectContent>
                      {references.map((name) => <SelectItem key={name} value={name}>{source === "pull_request" ? `#${name}` : name}</SelectItem>)}
                    </SelectContent>
                  </Select>
                  {referenceError ? <p role="alert" className="text-sm text-destructive">{referenceError}</p> : null}
                  <div className="flex gap-2"><Button type="button" variant="ghost" size="sm" disabled={loadingReferences} onClick={() => setReferenceReload((value) => value + 1)}><RefreshCw className="h-3.5 w-3.5" />{t("user.deploymentReferencesRefresh")}</Button>{referenceCatalog?.key === referenceKey && referenceCatalog.hasMore ? <Button type="button" variant="ghost" size="sm" disabled={loadingReferences} onClick={() => void loadMoreReferences()}>{t("user.deploymentReferencesMore")}</Button> : null}</div>
                </div>
                {commitSHA ? <div className="space-y-1 rounded-md bg-muted/50 p-3 text-sm"><p>{t("user.deploymentPinnedCommit")}</p><code className="block break-all font-mono text-xs">{commitSHA}</code><p className="text-xs text-muted-foreground">{t("user.deploymentPinnedCommitHint")}</p><Button type="button" variant="ghost" size="sm" onClick={() => setCommitSHA("")}>{t("user.deploymentUseLatest")}</Button></div> : null}
                {prefillActive && !loadingGitHubApp && !loadingRepositories && !repositoryIsCurrent ? <p role="alert" className="text-sm text-destructive">{t("user.deploymentPrefillUnavailable")}</p> : null}
                <div className="space-y-2"><Label htmlFor="deployment-ttl">{t("user.deploymentTTL")}</Label><Input id="deployment-ttl" type="number" min={300} max={86400} step={60} value={ttlSeconds} onChange={(event) => setTTLSeconds(event.target.value)} /></div>
                <div className="space-y-2">
                  <Label htmlFor="deployment-template">{t("user.deploymentTemplate")}</Label>
                  <Select name="deployment-template" value={selectedTemplate?.template_id ?? ""} onValueChange={(value) => { if (value) setTemplateID(value) }} disabled={loadingTemplates || templates.length === 0}>
                    <SelectTrigger id="deployment-template" className="w-full"><SelectValue placeholder={loadingTemplates ? t("user.deploymentTemplatesLoading") : t("user.deploymentTemplatesEmpty")} /></SelectTrigger>
                    <SelectContent>
                      {templates.map((item) => <SelectItem key={item.template_id} value={item.template_id}>{templateName(item)} · {item.template_id}</SelectItem>)}
                    </SelectContent>
                  </Select>
                  {templateCatalog?.key === templateKey && templateCatalog.region ? <p className="text-xs text-muted-foreground">{templateCatalog.region}</p> : null}
                  {templateError ? <div role="alert" className="space-y-1 text-sm"><p className="text-destructive">{templateError}</p>{selectedInstallation?.manageable !== false ? <AppLink className="text-primary underline" href={selectedInstallation?.account_type?.toLowerCase() === "organization" ? `/organizations/${encodeURIComponent(selectedInstallation.account_login ?? "")}/preferences` : "/account/preferences"}>{t("user.deploymentPreferences")}</AppLink> : null}</div> : null}
                  <Button type="button" variant="ghost" size="sm" disabled={loadingTemplates} onClick={() => setTemplateReload((value) => value + 1)}><RefreshCw className="h-3.5 w-3.5" />{t("user.deploymentTemplatesRefresh")}</Button>
                </div>
                <div className="space-y-2"><Label htmlFor="deployment-config">{t("user.deploymentConfigPath")}</Label><Input id="deployment-config" value={configPath} onChange={(event) => setConfigPath(event.target.value)} /></div>
                <Button type="submit" className="w-full" disabled={submitting || !repositoryIsCurrent || !repository || !installationID || loadingReferences || !selectedRef || loadingTemplates || !selectedTemplate}>{submitting ? <Loader2 className="h-4 w-4 animate-spin" /> : <Plus className="h-4 w-4" />}{submitting ? t("user.creatingDeployment") : t("user.createDeployment")}</Button>
              </form>
            )}
          </CardContent>
        </Card>
      </section>
    </div>
  )
}

function templateName(template: DeploymentTemplate) {
  return template.aliases?.find((alias) => alias !== template.template_id) ?? template.names?.[0] ?? template.template_id
}
