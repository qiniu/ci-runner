import { Check, ChevronsUpDown, LoaderCircle, Plus, RefreshCw, Save, Search, Trash2 } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import type { ForkSponsorshipMode, ForkSponsorshipPolicy, ForkSponsorshipRepository } from "@/admin-types"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover"
import { Switch } from "@/components/ui/switch"
import { cn } from "@/lib/utils"

type Request = (url: string, options?: RequestInit) => Promise<unknown>

const defaultMode: ForkSponsorshipMode = "approval_required"

const eligibilityModes: Array<{
  value: ForkSponsorshipMode
  label: "user.approvalRequired" | "user.writePermission" | "user.organizationMember"
  description: "user.approvalRequiredDescription" | "user.writePermissionDescription" | "user.organizationMemberDescription"
}> = [
  { value: "approval_required", label: "user.approvalRequired", description: "user.approvalRequiredDescription" },
  { value: "write_permission", label: "user.writePermission", description: "user.writePermissionDescription" },
  { value: "organization_member", label: "user.organizationMember", description: "user.organizationMemberDescription" },
]

function policyPayload(policy: Pick<ForkSponsorshipPolicy, "source_repository_full_name" | "mode" | "enabled" | "max_concurrency">) {
  return {
    source_repository_full_name: policy.source_repository_full_name,
    mode: policy.mode,
    enabled: policy.enabled,
    max_concurrency: policy.max_concurrency,
  }
}

export function ForkSponsorshipSection({ request, installationID }: { request: Request; installationID: number }) {
  const { t } = useTranslation()
  const [items, setItems] = useState<ForkSponsorshipPolicy[]>([])
  const [repositories, setRepositories] = useState<ForkSponsorshipRepository[]>([])
  const [loading, setLoading] = useState(true)
  const [repositoriesLoading, setRepositoriesLoading] = useState(true)
  const [repositoriesError, setRepositoriesError] = useState("")
  const [saving, setSaving] = useState(false)
  const [sourceRepository, setSourceRepository] = useState("")
  const [repositoryOpen, setRepositoryOpen] = useState(false)
  const [repositoryQuery, setRepositoryQuery] = useState("")
  const query = `?installation_id=${installationID}`
  const currentQuery = useRef(query)
  const loadGeneration = useRef(0)
  const repositoryLoadGeneration = useRef(0)
  currentQuery.current = query

  const load = useCallback(async () => {
    const generation = ++loadGeneration.current
    setLoading(true)
    try {
      const response = await request(`/user/fork-sponsorship-policies${query}`) as { items?: ForkSponsorshipPolicy[] }
      if (generation === loadGeneration.current && currentQuery.current === query) setItems(response.items || [])
    } catch (error) {
      if (generation === loadGeneration.current && currentQuery.current === query) toast.error(error instanceof Error ? error.message : t("user.forkSponsorshipLoadFailed"))
    } finally {
      if (generation === loadGeneration.current && currentQuery.current === query) setLoading(false)
    }
  }, [query, request, t])

  const loadRepositories = useCallback(async () => {
    const generation = ++repositoryLoadGeneration.current
    setRepositoriesLoading(true)
    setRepositoriesError("")
    try {
      const response = await request(`/user/fork-sponsorship-repositories${query}`) as { items?: ForkSponsorshipRepository[] }
      if (generation === repositoryLoadGeneration.current && currentQuery.current === query) setRepositories(response.items || [])
    } catch (error) {
      if (generation === repositoryLoadGeneration.current && currentQuery.current === query) {
        setRepositoriesError(error instanceof Error ? error.message : t("user.sourceRepositoriesLoadFailed"))
      }
    } finally {
      if (generation === repositoryLoadGeneration.current && currentQuery.current === query) setRepositoriesLoading(false)
    }
  }, [query, request, t])

  useEffect(() => {
    void load()
    void loadRepositories()
  }, [load, loadRepositories])

  useEffect(() => {
    if (sourceRepository && items.some((item) => item.source_repository_full_name === sourceRepository)) {
      setSourceRepository("")
    }
  }, [items, sourceRepository])

  const configuredRepositoryIDs = new Set(items.map((item) => item.source_repository_id))
  const availableRepositories = repositories.filter((repository) => !configuredRepositoryIDs.has(repository.id))
  const normalizedRepositoryQuery = repositoryQuery.trim().toLocaleLowerCase()
  const filteredRepositories = availableRepositories.filter((repository) => (
    !normalizedRepositoryQuery
    || repository.name.toLocaleLowerCase().includes(normalizedRepositoryQuery)
    || repository.full_name.toLocaleLowerCase().includes(normalizedRepositoryQuery)
  ))
  const selectedRepository = repositories.find((repository) => repository.full_name === sourceRepository)

  const create = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (saving || !sourceRepository) return
    const operationQuery = query
    setSaving(true)
    try {
      await request(`/user/fork-sponsorship-policies${query}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ source_repository_full_name: sourceRepository, mode: defaultMode, enabled: false, max_concurrency: 1 }),
      })
      if (currentQuery.current !== operationQuery) return
      setSourceRepository("")
      toast.success(t("user.forkSponsorshipSaved"))
      await load()
    } catch (error) {
      if (currentQuery.current === operationQuery) toast.error(error instanceof Error ? error.message : t("user.forkSponsorshipSaveFailed"))
    } finally {
      if (currentQuery.current === operationQuery) setSaving(false)
    }
  }

  return (
    <div className="space-y-4">
      <Card>
        <CardHeader>
          <CardTitle>{t("user.forkSponsorship")}</CardTitle>
          <CardDescription>{t("user.forkSponsorshipDescription")}</CardDescription>
        </CardHeader>
        <CardContent>
          <form className="flex flex-col gap-3 sm:flex-row sm:items-end" onSubmit={create}>
            <div className="min-w-0 flex-1 space-y-2">
              <Label htmlFor={`fork-source-${installationID}`}>{t("user.sourceRepository")}</Label>
              <Popover
                open={repositoryOpen}
                onOpenChange={(open) => {
                  setRepositoryOpen(open)
                  if (open) setRepositoryQuery("")
                }}
              >
                <PopoverTrigger asChild>
                  <Button
                    id={`fork-source-${installationID}`}
                    type="button"
                    variant="outline"
                    role="combobox"
                    aria-expanded={repositoryOpen}
                    aria-label={t("user.sourceRepository")}
                    className="w-full justify-between px-3 font-normal"
                    disabled={repositoriesLoading && repositories.length === 0}
                  >
                    <span className={cn("truncate", !selectedRepository && "text-muted-foreground")}>
                      {selectedRepository?.name || t("user.sourceRepositoryPlaceholder")}
                    </span>
                    {repositoriesLoading ? <LoaderCircle className="size-4 animate-spin opacity-50" /> : <ChevronsUpDown className="size-4 opacity-50" />}
                  </Button>
                </PopoverTrigger>
                <PopoverContent align="start" className="w-[var(--radix-popover-trigger-width)] p-0">
                  <div className="border-b p-2">
                    <div className="relative">
                      <Search className="pointer-events-none absolute top-1/2 left-2.5 size-4 -translate-y-1/2 text-muted-foreground" />
                      <Input
                        data-testid="fork-source-repository-search"
                        value={repositoryQuery}
                        onChange={(event) => setRepositoryQuery(event.target.value)}
                        placeholder={t("user.searchSourceRepositories")}
                        className="pl-8"
                        autoComplete="off"
                      />
                    </div>
                  </div>
                  <div className="max-h-64 overflow-y-auto p-1" role="listbox" aria-label={t("user.sourceRepository")}>
                    {filteredRepositories.map((repository) => (
                      <button
                        type="button"
                        role="option"
                        aria-selected={sourceRepository === repository.full_name}
                        key={repository.id}
                        className="flex w-full items-start gap-2 rounded-sm px-2 py-2 text-left hover:bg-accent focus-visible:bg-accent focus-visible:outline-none"
                        onClick={() => {
                          setSourceRepository(repository.full_name)
                          setRepositoryOpen(false)
                        }}
                      >
                        <Check className={cn("mt-0.5 size-4 shrink-0", sourceRepository === repository.full_name ? "opacity-100" : "opacity-0")} />
                        <span className="min-w-0">
                          <span className="block truncate text-sm font-medium">{repository.name}</span>
                          <span className="block truncate text-xs text-muted-foreground">{repository.full_name}</span>
                        </span>
                      </button>
                    ))}
                    {!repositoriesLoading && filteredRepositories.length === 0 ? (
                      <div className="px-2 py-4 text-center text-sm text-muted-foreground">
                        {normalizedRepositoryQuery ? t("user.noMatchingSourceRepositories") : t("user.noAvailableSourceRepositories")}
                      </div>
                    ) : null}
                  </div>
                </PopoverContent>
              </Popover>
              <p className={cn("text-xs", repositoriesError ? "text-destructive" : "text-muted-foreground")}>
                {repositoriesError || t("user.sourceRepositoryDescription")}
              </p>
            </div>
            <Button type="submit" disabled={saving || !sourceRepository}>
              <Plus className="h-4 w-4" />
              {t("user.addPolicy")}
            </Button>
            <Button type="button" variant="outline" size="icon" onClick={() => { void load(); void loadRepositories() }} disabled={loading || repositoriesLoading} title={t("common.refresh")}>
              <RefreshCw className={loading || repositoriesLoading ? "h-4 w-4 animate-spin" : "h-4 w-4"} />
            </Button>
          </form>
        </CardContent>
      </Card>

      {!loading && items.length === 0 ? (
        <div className="border-t py-8 text-sm text-muted-foreground">{t("user.noForkSponsorshipPolicies")}</div>
      ) : items.map((item) => (
        <ForkSponsorshipPolicyEditor key={`${installationID}:${item.source_repository_id}`} item={item} query={query} request={request} onChanged={load} />
      ))}
    </div>
  )
}

function ForkSponsorshipPolicyEditor({ item, query, request, onChanged }: { item: ForkSponsorshipPolicy; query: string; request: Request; onChanged: () => Promise<void> }) {
  const { t } = useTranslation()
  const [mode, setMode] = useState<ForkSponsorshipMode>(item.mode)
  const [enabled, setEnabled] = useState(item.enabled)
  const [maxConcurrency, setMaxConcurrency] = useState(String(item.max_concurrency))
  const [forkRepository, setForkRepository] = useState("")
  const [saving, setSaving] = useState(false)

  useEffect(() => {
    setMode(item.mode)
    setEnabled(item.enabled)
    setMaxConcurrency(String(item.max_concurrency))
  }, [item.mode, item.enabled, item.max_concurrency])

  const save = async () => {
    const limit = Number(maxConcurrency)
    if (!Number.isInteger(limit) || limit <= 0) return
    setSaving(true)
    try {
      await request(`/user/fork-sponsorship-policies/${item.source_repository_id}${query}`, {
        method: "PUT",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(policyPayload({ ...item, mode, enabled, max_concurrency: limit })),
      })
      toast.success(t("user.forkSponsorshipSaved"))
      await onChanged()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("user.forkSponsorshipSaveFailed"))
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    if (!window.confirm(t("user.confirmDeleteForkSponsorship", { repository: item.source_repository_full_name }))) return
    setSaving(true)
    try {
      await request(`/user/fork-sponsorship-policies/${item.source_repository_id}${query}`, { method: "DELETE" })
      toast.success(t("user.forkSponsorshipDeleted"))
      await onChanged()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("user.forkSponsorshipDeleteFailed"))
    } finally {
      setSaving(false)
    }
  }

  const addApproval = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!forkRepository.trim()) return
    setSaving(true)
    try {
      await request(`/user/fork-sponsorship-policies/${item.source_repository_id}/approvals${query}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ fork_repository_full_name: forkRepository.trim() }),
      })
      setForkRepository("")
      toast.success(t("user.forkApprovalSaved"))
      await onChanged()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("user.forkApprovalSaveFailed"))
    } finally {
      setSaving(false)
    }
  }

  const deleteApproval = async (forkRepositoryID: number) => {
    setSaving(true)
    try {
      await request(`/user/fork-sponsorship-policies/${item.source_repository_id}/approvals/${forkRepositoryID}${query}`, { method: "DELETE" })
      await onChanged()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("user.forkApprovalDeleteFailed"))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="space-y-4 border-t pt-4">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <div className="min-w-0">
          <h3 className="truncate text-base font-semibold">{item.source_repository_full_name}</h3>
          <p className="text-xs text-muted-foreground">{t("user.repositoryID", { id: item.source_repository_id })}</p>
        </div>
        <div className="flex items-center gap-2">
          <Button type="button" size="sm" onClick={() => void save()} disabled={saving}>
            <Save className="h-4 w-4" />
            {t("common.save")}
          </Button>
          <Button type="button" size="icon" variant="ghost" onClick={() => void remove()} disabled={saving} title={t("common.delete")}>
            <Trash2 className="h-4 w-4" />
          </Button>
        </div>
      </div>
      <fieldset className="space-y-2">
        <legend className="text-sm font-medium">{t("user.eligibilityMode")}</legend>
        <div className="grid gap-2 lg:grid-cols-3">
          {eligibilityModes.map((option) => (
            <label
              key={option.value}
              className={cn(
                "flex cursor-pointer items-start gap-3 rounded-md border p-3 transition-colors hover:bg-muted/50",
                mode === option.value && "border-primary bg-primary/5",
                saving && "cursor-not-allowed opacity-60",
              )}
            >
              <input
                type="radio"
                name={`fork-mode-${item.source_repository_id}`}
                value={option.value}
                checked={mode === option.value}
                disabled={saving}
                onChange={() => setMode(option.value)}
                className="mt-0.5 size-4 shrink-0 accent-primary"
              />
              <span className="min-w-0">
                <span className="block text-sm font-medium">{t(option.label)}</span>
                <span className="mt-0.5 block text-xs leading-relaxed text-muted-foreground">{t(option.description)}</span>
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      <div className="grid gap-4 sm:grid-cols-[160px_auto] sm:items-end">
        <div className="space-y-2">
          <Label htmlFor={`fork-limit-${item.source_repository_id}`}>{t("user.maxConcurrency")}</Label>
          <Input id={`fork-limit-${item.source_repository_id}`} type="number" min="1" step="1" value={maxConcurrency} onChange={(event) => setMaxConcurrency(event.target.value)} />
        </div>
        <div className="flex h-10 items-center gap-2">
          <Switch id={`fork-enabled-${item.source_repository_id}`} checked={enabled} onCheckedChange={setEnabled} />
          <Label htmlFor={`fork-enabled-${item.source_repository_id}`}>{t("common.enabled")}</Label>
        </div>
      </div>
      {mode === "approval_required" && <div className="space-y-3">
        <div>
          <h4 className="text-sm font-semibold">{t("user.approvedForks")}</h4>
          <p className="text-xs text-muted-foreground">{t("user.approvedForksDescription")}</p>
        </div>
        <form className="flex flex-col gap-2 sm:flex-row" onSubmit={addApproval}>
          <Input value={forkRepository} onChange={(event) => setForkRepository(event.target.value)} placeholder={t("user.forkRepositoryPlaceholder")} autoComplete="off" />
          <Button type="submit" variant="outline" disabled={saving || !forkRepository.trim()}>
            <Plus className="h-4 w-4" />
            {t("user.approveFork")}
          </Button>
        </form>
        {item.approvals.length > 0 && (
          <div className="divide-y border-y">
            {item.approvals.map((approval) => (
              <div key={approval.fork_repository_id} className="flex min-h-11 items-center justify-between gap-3 py-2">
                <div className="min-w-0">
                  <div className="truncate text-sm font-medium">{approval.fork_repository_full_name}</div>
                  <div className="truncate text-xs text-muted-foreground">{approval.fork_owner_login}</div>
                </div>
                <Button type="button" variant="ghost" size="icon" disabled={saving} onClick={() => void deleteApproval(approval.fork_repository_id)} title={t("common.delete")}>
                  <Trash2 className="h-4 w-4" />
                </Button>
              </div>
            ))}
          </div>
        )}
      </div>}
    </div>
  )
}
