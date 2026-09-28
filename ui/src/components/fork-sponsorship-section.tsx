import { Plus, RefreshCw, Save, Trash2 } from "lucide-react"
import { type FormEvent, useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import type { ForkSponsorshipMode, ForkSponsorshipPolicy } from "@/admin-types"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Switch } from "@/components/ui/switch"

type Request = (url: string, options?: RequestInit) => Promise<unknown>

const defaultMode: ForkSponsorshipMode = "approval_required"

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
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [sourceRepository, setSourceRepository] = useState("")
  const query = `?installation_id=${installationID}`
	const currentQuery = useRef(query)
	const loadGeneration = useRef(0)
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

  useEffect(() => { void load() }, [load])

  const create = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (saving || !sourceRepository.trim()) return
		const operationQuery = query
    setSaving(true)
    try {
      await request(`/user/fork-sponsorship-policies${query}`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ source_repository_full_name: sourceRepository.trim(), mode: defaultMode, enabled: false, max_concurrency: 1 }),
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
              <Input id={`fork-source-${installationID}`} value={sourceRepository} onChange={(event) => setSourceRepository(event.target.value)} placeholder={t("user.sourceRepositoryPlaceholder")} autoComplete="off" />
            </div>
            <Button type="submit" disabled={saving || !sourceRepository.trim()}>
              <Plus className="h-4 w-4" />
              {t("user.addPolicy")}
            </Button>
            <Button type="button" variant="outline" size="icon" onClick={() => void load()} disabled={loading} title={t("common.refresh")}>
              <RefreshCw className={loading ? "h-4 w-4 animate-spin" : "h-4 w-4"} />
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
      <div className="grid gap-4 sm:grid-cols-[minmax(0,1fr)_160px_auto] sm:items-end">
        <div className="space-y-2">
          <Label>{t("user.eligibilityMode")}</Label>
          <Select value={mode} onValueChange={(value) => setMode(value as ForkSponsorshipMode)}>
            <SelectTrigger><SelectValue /></SelectTrigger>
            <SelectContent>
              <SelectItem value="approval_required">{t("user.approvalRequired")}</SelectItem>
              <SelectItem value="write_permission">{t("user.writePermission")}</SelectItem>
              <SelectItem value="organization_member">{t("user.organizationMember")}</SelectItem>
            </SelectContent>
          </Select>
        </div>
        <div className="space-y-2">
          <Label htmlFor={`fork-limit-${item.source_repository_id}`}>{t("user.maxConcurrency")}</Label>
          <Input id={`fork-limit-${item.source_repository_id}`} type="number" min="1" step="1" value={maxConcurrency} onChange={(event) => setMaxConcurrency(event.target.value)} />
        </div>
        <div className="flex h-10 items-center gap-2">
          <Switch id={`fork-enabled-${item.source_repository_id}`} checked={enabled} onCheckedChange={setEnabled} />
          <Label htmlFor={`fork-enabled-${item.source_repository_id}`}>{t("common.enabled")}</Label>
        </div>
      </div>
      <div className="space-y-3">
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
      </div>
    </div>
  )
}
