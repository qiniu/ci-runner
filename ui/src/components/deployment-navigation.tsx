import { useEffect, useState } from "react"
import { ChevronRight, Loader2, Plus } from "lucide-react"
import { useTranslation } from "react-i18next"

import { AppLink } from "@/components/app-link"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "@/components/ui/collapsible"
import { deploymentCreatePath, deploymentPath, githubContextPath } from "@/github-context-routes"
import { cn } from "@/lib/utils"
import type { Deployment } from "@/components/user-deployments-section"
import { DeploymentStatus } from "@/components/deployment-status"
import { activeDeploymentStatuses } from "@/deployment-status"

type NavigationItem = Pick<Deployment, "id" | "repository_full_name" | "ref" | "commit_sha" | "status" | "updated_at" | "source" | "pull_request_number" | "pull_request_title">
type Repository = { repository_full_name: string; total?: number; active_count?: number; active_deployments: NavigationItem[] }
type RepositoryPage = { items?: Repository[]; has_more?: boolean }
type Request = (url: string, options?: RequestInit) => Promise<unknown>
const pageSize = 100

export function DeploymentNavigation({ request, selected, selectedID, repository, refreshVersion }: {
  request: Request
  selected: Deployment | null
  selectedID?: string
  repository?: string
  refreshVersion: number
}) {
  const { t } = useTranslation()
  const [repositories, setRepositories] = useState<Repository[]>([])
  const [pageCount, setPageCount] = useState(1)
  const [hasMore, setHasMore] = useState(false)
  const [loading, setLoading] = useState(true)
  const [failed, setFailed] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const [expanded, setExpanded] = useState<Record<string, boolean>>({})
  const selectedRepository = selected?.repository_full_name || repository

  useEffect(() => {
    if (selectedRepository) setExpanded((current) => ({ ...current, [selectedRepository.toLowerCase()]: true }))
  }, [selectedRepository, selectedID])

  useEffect(() => {
    let cancelled = false
    let busy = false
    const load = async () => {
      if (busy) return
      busy = true
      try {
        const pages = await Promise.all(Array.from({ length: pageCount }, (_, index) => request(`/user/deployment-repositories?limit=${pageSize}&offset=${index * pageSize}`) as Promise<RepositoryPage>))
        if (cancelled) return
        const items = pages.flatMap((page) => page.items ?? [])
        if (selectedRepository && !items.some((item) => item.repository_full_name.toLowerCase() === selectedRepository.toLowerCase())) {
          const page = await request(`/user/deployment-repositories?repository_full_name=${encodeURIComponent(selectedRepository)}`) as RepositoryPage
          items.unshift(...(page.items ?? []))
        }
        if (!cancelled) {
          setRepositories([...new Map(items.map((item) => [item.repository_full_name.toLowerCase(), item])).values()])
          setHasMore(Boolean(pages[pages.length - 1]?.has_more))
          setFailed(false)
        }
      } catch {
        if (!cancelled) setFailed(true)
      } finally {
        busy = false
        if (!cancelled) setLoading(false)
      }
    }
    setLoading(true)
    void load()
    const timer = window.setInterval(() => { void load() }, 5000)
    return () => { cancelled = true; window.clearInterval(timer) }
  }, [request, selectedRepository, pageCount, refreshVersion, refresh])

  const visible = [...repositories]
  if (selected && !visible.some((item) => item.repository_full_name.toLowerCase() === selected.repository_full_name.toLowerCase())) {
    // A direct detail must remain navigable while the independent catalog loads.
    visible.unshift({ repository_full_name: selected.repository_full_name, active_deployments: [] })
  }
  const createContext = selectedRepository ? { kind: "repository" as const, repository: selectedRepository, reference: "" } : null
  return <aside className="flex min-h-0 min-w-0 flex-col border-b bg-muted/20 xl:border-b-0 xl:border-r" data-testid="deployment-navigation">
    {selectedID ? <div className="flex h-10 shrink-0 items-center justify-end border-b px-4">
      <Button asChild variant="ghost" size="icon" className="size-7"><AppLink href={deploymentCreatePath(createContext)} aria-label={t("user.createDeployment")}><Plus className="size-4" /></AppLink></Button>
    </div> : null}
    {failed ? <div className="flex flex-wrap items-center gap-2 border-b p-3 text-xs text-muted-foreground" role="alert"><span>{t("user.deploymentLoadFailed")}</span><Button type="button" variant="outline" size="sm" onClick={() => setRefresh((value) => value + 1)}>{t("user.tryAgain")}</Button></div> : null}
    <nav aria-label={t("user.deployments")} className="max-h-60 overflow-y-auto xl:min-h-0 xl:max-h-none xl:flex-1">
      <AppLink href="/deployments" aria-current={!selectedID && !repository ? "page" : undefined} className={cn("block border-b px-4 py-3 text-sm hover:bg-accent/70", !selectedID && !repository && "bg-primary/5 font-medium")}>{t("user.deploymentAll")}</AppLink>
      {visible.map((item) => {
        const name = item.repository_full_name
        const key = name.toLowerCase()
        const isSelected = key === selectedRepository?.toLowerCase()
        const pinned = isSelected && selected ? selected : null
        const active = item.active_deployments.filter((deployment) => deployment.id !== pinned?.id)
        if (pinned && activeDeploymentStatuses.has(pinned.status)) active.unshift(pinned)
        const children = pinned && !activeDeploymentStatuses.has(pinned.status) ? [pinned, ...active] : active
        const statuses = new Set(active.map((deployment) => deployment.status))
        const aggregateStatus = statuses.size !== 1 ? "active" : statuses.has("queued") ? "queued" : statuses.has("stopping") ? "active" : "deploying"
        return <Collapsible key={key} open={expanded[key] ?? isSelected} onOpenChange={(open) => setExpanded((current) => ({ ...current, [key]: open }))} className="border-b" data-testid="deployment-repository">
          <div className={cn("flex items-start gap-1 border-l-2 py-2 pl-2 pr-2", isSelected ? "border-l-primary bg-primary/5" : "border-l-transparent")}>
            <div className="min-w-0 flex-1">
              <div className="flex min-h-7 items-center gap-2">
                <AppLink href={githubContextPath({ kind: "repository", repository: name, reference: "" }, "deployments")} aria-current={isSelected && !selectedID ? "page" : undefined} title={name} className="min-w-0 flex-1 truncate text-sm font-semibold hover:underline">{name}</AppLink>
                {active.length ? <Badge variant="secondary">{aggregateStatus === "queued" ? t("common.statusQueued") : aggregateStatus === "deploying" ? t("user.deploymentRepositoryDeploying") : t("user.deploymentRepositoryActive")}</Badge> : null}
              </div>
              {item.active_count !== undefined ? <p className="text-xs text-muted-foreground">{t("user.deploymentActiveCount", { count: item.active_count })}</p> : null}
            </div>
            {children.length ? <CollapsibleTrigger asChild><Button type="button" variant="ghost" size="icon" className="size-7 shrink-0" aria-label={t("user.deploymentToggleRepository", { repository: name })}><ChevronRight className={cn("size-4 transition-transform", (expanded[key] ?? isSelected) && "rotate-90")} /></Button></CollapsibleTrigger> : null}
          </div>
          <CollapsibleContent className="border-t border-border/40 bg-background/70 py-1">
            {children.length ? children.map((deployment) => {
              const pull = deployment.pull_request_number || Number(deployment.ref?.match(/^refs\/pull\/(\d+)\/head$/)?.[1])
              const reference = pull
                ? `${t("user.deploymentPullRequestNumber", { number: pull })}${deployment.pull_request_title?.trim() ? ` · ${deployment.pull_request_title.trim()}` : ""}`
                : `${t(deployment.source === "tag" || deployment.ref?.startsWith("refs/tags/") ? "user.deploymentTag" : "user.deploymentBranch")} ${deployment.ref?.replace(/^refs\/(heads|tags)\//, "") || "—"}`
              return <AppLink key={deployment.id} href={deploymentPath({ ...deployment, pull_request_number: pull || undefined })} aria-current={deployment.id === selectedID ? "page" : undefined} className={cn("block min-w-0 border-l-2 py-2 pl-10 pr-4 transition-colors hover:bg-accent/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring", deployment.id === selectedID ? "border-l-primary bg-primary/5" : "border-l-transparent")}>
                <div className="flex items-center justify-between gap-2"><span className="min-w-0 line-clamp-2 break-words text-sm" title={reference}>{reference}</span><DeploymentStatus status={deployment.status} /></div>
                <code className="mt-1 block truncate text-xs text-muted-foreground" title={deployment.id}>{deployment.id.slice(0, 10)}</code>
              </AppLink>
            }) : null}
          </CollapsibleContent>
        </Collapsible>
      })}
      {loading && !visible.length ? <p className="flex items-center gap-2 p-4 text-sm text-muted-foreground"><Loader2 className="size-4 animate-spin" />{t("user.loadingDeployments")}</p> : !visible.length ? <p className="p-4 text-xs text-muted-foreground">{t("user.noDeployments")}</p> : null}
      {hasMore ? <div className="p-3"><Button type="button" variant="outline" size="sm" className="w-full" disabled={loading} onClick={() => setPageCount((count) => count + 1)}>{t("user.deploymentMoreRepositories")}</Button></div> : null}
    </nav>
  </aside>
}
