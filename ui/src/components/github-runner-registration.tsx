import { useEffect, useState } from "react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

type RunnerGroup = { id: number; name: string; restricted_to_workflows: boolean }
type GroupList = { organization: string; groups: RunnerGroup[] }
type RequestFn = (url: string, options?: RequestInit) => Promise<unknown>

export function RunnerRegistrationTarget({ group = "", organization }: { group?: string; organization?: string }) {
  const { t } = useTranslation()
  return <span>{group.trim()
    ? organization
      ? t("user.organizationRegistration", { organization, group: group.trim() })
      : t("user.ownerOrganizationRegistration", { group: group.trim() })
    : t("user.repositoryRegistration")}</span>
}

export function GitHubRunnerRegistration({ group, onChange, id, organization, platform = false, request, installationID }: {
  group: string
  onChange: (group: string) => void
  id: string
  organization?: string
  platform?: boolean
  request?: RequestFn
  installationID?: number
}) {
  const { t } = useTranslation()
  const [enabled, setEnabled] = useState(Boolean(group.trim()))
  const [revision, setRevision] = useState(0)
  const [result, setResult] = useState<{ installationID: number; revision: number; data?: GroupList; error?: string } | null>(null)
  const expanded = enabled || Boolean(group.trim())
  const useCatalog = Boolean(request && installationID)
  const current = result?.installationID === installationID && result?.revision === revision ? result : null
  const data = current?.data
  const loading = useCatalog && expanded && !current
  useEffect(() => {
    if (!expanded || !request || !installationID) return
    let active = true
    void request(`/user/github-runner-groups?installation_id=${installationID}`).then((response) => {
      const data = response as GroupList
      if (!data || !Array.isArray(data.groups) || !data.organization) throw new Error("invalid group list")
      if (active) setResult({ installationID, revision, data })
    }).catch((error: unknown) => {
      const code = (error as { code?: string } | null)?.code
      const message = code === "runner_group_permission_denied" ? t("user.runnerGroupsPermissionDenied")
        : code === "runner_group_rate_limited" ? t("user.runnerGroupsRateLimited")
        : code === "runner_group_timeout" ? t("user.runnerGroupsTimeout")
        : t("user.runnerGroupsUnavailable")
      if (active) setResult({ installationID, revision, error: message })
    })
    return () => { active = false }
  }, [expanded, request, installationID, revision, t])
  const missing = Boolean(group && data && !data.groups.some((item) => item.name === group))
  return (
    <div className="grid min-w-0 gap-2 rounded-md border bg-muted/20 p-3">
      <p className="text-sm font-medium">{t("user.registrationTarget")}</p>
      <p className="break-words text-xs text-muted-foreground">
        <RunnerRegistrationTarget group={group} organization={data?.organization || organization} />
      </p>
      <label className="flex items-center gap-2 text-sm">
        <input type="checkbox" checked={expanded} aria-controls={`${id}-options`} onChange={(event) => {
          setEnabled(event.target.checked)
          if (event.target.checked) setRevision((value) => value + 1)
          else onChange("")
        }} />
        {t("user.useOrganizationRunnerGroup")}
      </label>
      <p className="text-xs leading-5 text-muted-foreground">{t("user.runnerGroupHelp")}</p>
      {expanded ? <div id={`${id}-options`} className="grid min-w-0 gap-2">
        <Label htmlFor={id}>{t("user.runnerGroup")}</Label>
        {useCatalog ? <>
          <select id={id} value={group} required aria-busy={loading} className="h-9 w-full min-w-0 rounded-md border bg-background px-3 text-sm" onChange={(event) => onChange(event.target.value)}>
            <option value="">{t("user.selectRunnerGroup")}</option>
            {group && (!data || missing) ? <option value={group}>{data ? t("user.runnerGroupUnavailableOption", { group }) : group}</option> : null}
            {data?.groups.map((item) => <option key={item.id} value={item.name}>{item.name}</option>)}
          </select>
          {loading ? <p role="status" className="text-xs text-muted-foreground">{t("user.runnerGroupsLoading")}</p> : null}
          {current?.error ? <p role="alert" className="text-xs text-destructive">{current.error}</p> : null}
          {data && data.groups.length === 0 ? <p className="text-xs text-muted-foreground">{t("user.runnerGroupsEmpty")}</p> : null}
          {data?.groups.find((item) => item.name === group)?.restricted_to_workflows ? <p className="text-xs text-muted-foreground">{t("user.runnerGroupWorkflowRestricted")}</p> : null}
          <div className="flex flex-wrap items-center gap-3">
            <Button type="button" variant="outline" size="sm" disabled={loading} onClick={() => setRevision((value) => value + 1)}>{t("user.refreshRunnerGroups")}</Button>
            {(data?.organization || organization) ? <a className="text-xs text-primary hover:underline" href={`https://github.com/organizations/${encodeURIComponent(data?.organization || organization || "")}/settings/actions/runner-groups`} target="_blank" rel="noreferrer">{t("user.manageRunnerGroups")}</a> : null}
          </div>
        </> : <Input id={id} value={group} required pattern={".*\\S.*"} onChange={(event) => onChange(event.target.value)} />}
        <p className="text-xs leading-5 text-muted-foreground">{t(platform ? "user.runnerGroupAdminHelp" : "user.runnerGroupWorkflowHelp")}</p>
      </div> : null}
    </div>
  )
}
