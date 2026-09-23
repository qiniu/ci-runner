import { useCallback, useEffect, useMemo, useRef, useState, type MouseEvent } from "react"
import { ArrowLeft, Building2, ChevronRight, ExternalLink, Github, LoaderCircle, RefreshCw, Search, UserRound } from "lucide-react"
import { useTranslation } from "react-i18next"

import type {
  AdminGitHubInstallation,
  AdminGitHubInstallationDetail,
  AdminGitHubInstallationsResponse,
} from "@/admin-types"
import "@/i18n"
import type { AppTFunction } from "@/i18n"
import { filterGitHubInstallations, githubAccountInitial, githubRepositoryURL } from "@/components/github-app-accounts-utils"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table"

type RequestFunction = (url: string, options?: RequestInit) => Promise<unknown>

type GitHubAppAccountsSectionProps = {
  installationID: number
  request: RequestFunction
  onOpen: (installationID: number) => void
  onBack: () => void
}

type ListProps = {
  installations: AdminGitHubInstallation[]
  query: string
  loading: boolean
  error: string
  onQueryChange: (query: string) => void
  onRefresh: () => void
  onOpen: (installationID: number) => void
}

type DetailProps = {
  detail: AdminGitHubInstallationDetail | null
  loading: boolean
  error: string
  onBack: () => void
  onRefresh: () => void
}

export function GitHubAppAccountsSection({
  installationID,
  request,
  onOpen,
  onBack,
}: GitHubAppAccountsSectionProps) {
  const { t } = useTranslation()
  const [installations, setInstallations] = useState<AdminGitHubInstallation[]>([])
  const [detail, setDetail] = useState<AdminGitHubInstallationDetail | null>(null)
  const [loadedInstallationID, setLoadedInstallationID] = useState(0)
  const [query, setQuery] = useState("")
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState("")
  const loadVersion = useRef(0)

  const load = useCallback(async () => {
    const version = ++loadVersion.current
    setLoading(true)
    setError("")
    const path = installationID > 0
      ? `/admin/api/github-app/installations/${installationID}/repositories`
      : "/admin/api/github-app/installations"
    try {
      const data = await request(path)
      if (version !== loadVersion.current) return
      if (installationID > 0) {
        const next = data as AdminGitHubInstallationDetail
        if (!next?.installation || !Array.isArray(next.repositories)) throw new Error("invalid installation detail")
        setDetail(next)
        setLoadedInstallationID(installationID)
        return
      }
      const next = data as AdminGitHubInstallationsResponse
      if (!next || !Array.isArray(next.installations)) throw new Error("invalid installation list")
      setInstallations(next.installations)
    } catch {
      if (version === loadVersion.current) {
        setError(installationID > 0 ? t("admin.loadGitHubRepositoriesFailed") : t("admin.loadGitHubAppAccountsFailed"))
      }
    } finally {
      if (version === loadVersion.current) setLoading(false)
    }
  }, [installationID, request, t])

  useEffect(() => {
    void load()
    return () => {
      loadVersion.current += 1
    }
  }, [load])

  const filteredInstallations = useMemo(
    () => filterGitHubInstallations(installations, query),
    [installations, query],
  )
  const refresh = () => void load()

  if (installationID > 0) {
    return (
      <GitHubAppAccountDetail
        detail={loadedInstallationID === installationID ? detail : null}
        loading={loading}
        error={error}
        onBack={onBack}
        onRefresh={refresh}
      />
    )
  }
  return (
    <GitHubAppAccountsList
      installations={filteredInstallations}
      query={query}
      loading={loading}
      error={error}
      onQueryChange={setQuery}
      onRefresh={refresh}
      onOpen={onOpen}
    />
  )
}

export function GitHubAppAccountsList({
  installations,
  query,
  loading,
  error,
  onQueryChange,
  onRefresh,
  onOpen,
}: ListProps) {
  const { t } = useTranslation()
  return (
    <section className="space-y-4" aria-labelledby="github-app-accounts-title">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <h1 id="github-app-accounts-title" className="text-xl font-semibold">{t("admin.githubAppAccountsTitle")}</h1>
          <p className="mt-1 max-w-3xl text-sm text-muted-foreground">{t("admin.githubAppAccountsDescription")}</p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label={t("admin.refreshGitHubAppAccounts")}
          title={t("admin.refreshGitHubAppAccounts")}
          onClick={onRefresh}
          disabled={loading}
        >
          <RefreshCw className={loading ? "animate-spin" : ""} />
        </Button>
      </div>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="relative w-full max-w-md">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            value={query}
            onChange={(event) => onQueryChange(event.target.value)}
            className="pl-9"
            aria-label={t("admin.searchGitHubAppAccounts")}
            placeholder={t("admin.searchGitHubAppAccountsPlaceholder")}
          />
        </div>
        <p className="text-sm text-muted-foreground">{t("admin.githubAppAccountResults", { count: installations.length })}</p>
      </div>

      {error ? <LoadError message={error} onRetry={onRefresh} /> : null}

      <div className="overflow-hidden rounded-md border bg-background">
        <Table className="table-fixed md:table-auto">
          <TableHeader>
            <TableRow className="bg-muted/40 hover:bg-muted/40">
              <TableHead>{t("admin.installedAccount")}</TableHead>
              <TableHead className="hidden md:table-cell">{t("admin.accountType")}</TableHead>
              <TableHead className="hidden lg:table-cell">{t("admin.githubAccountIdentifier")}</TableHead>
              <TableHead className="hidden md:table-cell">{t("admin.installationID")}</TableHead>
              <TableHead className="w-10"><span className="sr-only">{t("common.details")}</span></TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {loading && installations.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="h-40 text-center text-muted-foreground">
                  <LoaderCircle className="mx-auto mb-2 size-5 animate-spin" />
                  {t("admin.loadingGitHubAppAccounts")}
                </TableCell>
              </TableRow>
            ) : !error && installations.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5} className="h-40 text-center">
                  <Github className="mx-auto mb-3 size-6 text-muted-foreground" />
                  <p className="font-medium">{t("admin.noGitHubAppInstallations")}</p>
                  <p className="mt-1 text-sm text-muted-foreground">{t("admin.noGitHubAppInstallationsDescription")}</p>
                </TableCell>
              </TableRow>
            ) : installations.map((installation) => (
              <TableRow key={installation.id}>
                <TableCell>
                  <a
                    href={`/admin/github_accounts/${installation.id}`}
                    className="flex min-w-0 items-center gap-3 rounded-sm outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    onClick={(event) => handleInternalLink(event, () => onOpen(installation.id))}
                  >
                    <GitHubAccountAvatar installation={installation} />
                    <span className="min-w-0">
                      <span className="block truncate font-medium">{installation.account_login}</span>
                      <span className="block truncate text-xs text-muted-foreground">
                        {installation.account_name || t("admin.unnamedGitHubAccount")}
                      </span>
                      <span className="mt-1 flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground md:hidden">
                        <span>{accountTypeLabel(installation.account_type, t)}</span>
                        <span>{t("admin.installationNumber", { id: installation.id })}</span>
                      </span>
                    </span>
                  </a>
                </TableCell>
                <TableCell className="hidden md:table-cell">
                  <Badge variant="outline" className="gap-1.5">
                    {installation.account_type === "organization" ? <Building2 /> : <UserRound />}
                    {accountTypeLabel(installation.account_type, t)}
                  </Badge>
                </TableCell>
                <TableCell className="hidden font-mono text-xs lg:table-cell">{installation.account_id}</TableCell>
                <TableCell className="hidden text-muted-foreground md:table-cell">{t("admin.installationNumber", { id: installation.id })}</TableCell>
                <TableCell>
                  <a
                    href={`/admin/github_accounts/${installation.id}`}
                    className="inline-flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                    aria-label={t("admin.openGitHubAppAccount", { account: installation.account_login })}
                    onClick={(event) => handleInternalLink(event, () => onOpen(installation.id))}
                  >
                    <ChevronRight className="size-4" />
                  </a>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </div>
    </section>
  )
}

export function GitHubAppAccountDetail({ detail, loading, error, onBack, onRefresh }: DetailProps) {
  const { t } = useTranslation()
  const installation = detail?.installation
  const repositories = detail?.repositories ?? []
  return (
    <section className="space-y-4" aria-labelledby="github-app-account-title">
      <div className="flex items-center justify-between gap-3">
        <Button variant="ghost" asChild className="-ml-3">
          <a href="/admin/github_accounts" onClick={(event) => handleInternalLink(event, onBack)}>
            <ArrowLeft />
            {t("admin.backToGitHubAppAccounts")}
          </a>
        </Button>
        <Button
          type="button"
          variant="outline"
          size="icon"
          aria-label={t("admin.refreshGitHubRepositories")}
          title={t("admin.refreshGitHubRepositories")}
          onClick={onRefresh}
          disabled={loading}
        >
          <RefreshCw className={loading ? "animate-spin" : ""} />
        </Button>
      </div>

      {error ? <LoadError message={error} onRetry={onRefresh} /> : null}

      {loading && !installation ? (
        <div className="flex h-44 items-center justify-center gap-2 border-y text-sm text-muted-foreground">
          <LoaderCircle className="size-5 animate-spin" />
          {t("admin.loadingGitHubRepositories")}
        </div>
      ) : installation ? (
        <>
          <div className="flex flex-wrap items-center gap-4 border-y py-5">
            <GitHubAccountAvatar installation={installation} size="large" />
            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2">
                <h1 id="github-app-account-title" className="text-xl font-semibold">{installation.account_login}</h1>
                <Badge variant="outline">{accountTypeLabel(installation.account_type, t)}</Badge>
              </div>
              <p className="mt-1 text-sm text-muted-foreground">{installation.account_name || t("admin.unnamedGitHubAccount")}</p>
              <p className="mt-2 font-mono text-xs text-muted-foreground">
                {t("admin.installationNumber", { id: installation.id })} · {t("admin.githubAccountNumber", { id: installation.account_id })}
              </p>
            </div>
          </div>

          <div className="flex flex-col items-start gap-2 sm:flex-row sm:items-end sm:justify-between sm:gap-4">
            <div>
              <h2 className="font-semibold">{t("admin.authorizedRepositories")}</h2>
              <p className="mt-1 text-sm text-muted-foreground">{t("admin.authorizedRepositoriesDescription")}</p>
            </div>
            <span className="text-sm font-medium">{t("admin.repositoryCount", { count: repositories.length })}</span>
          </div>

          <div className="overflow-hidden rounded-md border bg-background">
            <Table className="table-fixed">
              <TableHeader>
                <TableRow className="bg-muted/40 hover:bg-muted/40">
                  <TableHead>{t("common.repository")}</TableHead>
                  <TableHead className="w-10"><span className="sr-only">{t("common.actions")}</span></TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {repositories.length === 0 ? (
                  <TableRow>
                    <TableCell colSpan={2} className="h-36 text-center">
                      <p className="font-medium">{t("admin.noAuthorizedRepositories")}</p>
                      <p className="mt-1 text-sm text-muted-foreground">{t("admin.noAuthorizedRepositoriesDescription")}</p>
                    </TableCell>
                  </TableRow>
                ) : repositories.map((repository) => (
                  <TableRow key={repository}>
                    <TableCell className="break-words whitespace-normal font-medium">{repository}</TableCell>
                    <TableCell>
                      <a
                        href={githubRepositoryURL(repository)}
                        target="_blank"
                        rel="noreferrer"
                        className="inline-flex size-8 items-center justify-center rounded-md text-muted-foreground hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        aria-label={t("admin.openRepository", { repository })}
                        title={t("admin.openRepository", { repository })}
                      >
                        <ExternalLink className="size-4" />
                      </a>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </div>
        </>
      ) : null}
    </section>
  )
}

function GitHubAccountAvatar({ installation, size = "default" }: { installation: AdminGitHubInstallation; size?: "default" | "large" }) {
  const { t } = useTranslation()
  const [failed, setFailed] = useState(false)
  const sizeClass = size === "large" ? "size-14 text-lg" : "size-10 text-sm"
  return (
    <span className={`relative inline-flex shrink-0 items-center justify-center overflow-hidden rounded-full border bg-muted font-semibold ${sizeClass}`}>
      <span aria-hidden="true">{githubAccountInitial(installation)}</span>
      {installation.account_avatar && !failed ? (
        <img
          src={installation.account_avatar}
          alt={t("common.namedAvatar", { name: installation.account_login })}
          className="absolute inset-0 size-full object-cover"
          onError={() => setFailed(true)}
        />
      ) : null}
    </span>
  )
}

function LoadError({ message, onRetry }: { message: string; onRetry: () => void }) {
  const { t } = useTranslation()
  return (
    <div role="alert" className="flex flex-wrap items-center justify-between gap-3 border-l-2 border-destructive bg-destructive/5 px-4 py-3 text-sm">
      <span>{message}</span>
      <Button type="button" variant="outline" size="sm" onClick={onRetry}>{t("auth.tryAgain")}</Button>
    </div>
  )
}

function accountTypeLabel(accountType: string, t: AppTFunction): string {
  if (accountType === "organization") return t("admin.githubOrganization")
  if (accountType === "user") return t("admin.githubUser")
  return accountType || t("common.unknown")
}

function handleInternalLink(event: MouseEvent<HTMLAnchorElement>, navigate: () => void) {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  navigate()
}
