import {
  AlertCircle,
  Boxes,
  BookOpen,
  CalendarDays,
  Check,
  ExternalLink,
  Github,
  KeyRound,
  Loader2,
  Play,
  RefreshCw,
  ShieldCheck,
  SquareTerminal,
  Workflow,
  X,
} from "lucide-react"
import { type CSSProperties, type FormEvent, type MouseEvent, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"

import type { AuthSession, GitHubAppConfig, ProductTourOnboarding, RunnerJobGroup, RunnerState, UserPreferences } from "@/admin-types"
import { logNames } from "@/admin-types"
import { formatRunnerDuration, formatTime, runnerStatusLabel } from "@/admin-format"
import { localizedLogTextForView, type LocalizedLogMessageKey, type LocalizedLogText } from "@/app-log-state"
import appI18n, { type AppTFunction } from "@/i18n"
import { userRunnerHistoryWindow } from "@/app-load-policy"
import { AccountMenu } from "@/components/account-menu"
import { githubLogFailureState } from "@/components/github-log-utils"
import { RepositoryReadinessPage } from "@/components/repository-readiness-page"
import { UserOnboardingTour } from "@/components/user-onboarding-tour"
import {
  manageableSettingsInstallations,
  repositoryPreferenceScope,
  selectRepositoryInstallation,
  settingsPreferenceInstallationID,
  settingsScopeAccessState,
} from "@/repository-readiness"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { shouldShowSandboxSetupTask } from "@/user-onboarding"
import { SandboxesSection, SandboxTemplatesSection } from "@/components/sandbox-catalog-sections"
import { UserRunnerSpecsSection } from "@/components/user-runner-specs-section"
import { findSandboxRegionByAPIURL, useSandboxRegions } from "@/components/sandbox-catalog-utils"
import { Switch } from "@/components/ui/switch"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { useSandboxTerminal } from "@/hooks/use-sandbox-terminal"
import { cn } from "@/lib/utils"
import { toast } from "sonner"

type BuildGroupKind = "pull_request" | "branch" | "workflow_run" | "manual"
type RunnerStatusSummary = "completed" | "active" | "failed"

type BuildGroup = {
  key: string
  kind: BuildGroupKind
  repository: string
  title: string
  subtitle: string
  updatedAt: string
  jobs: RunnerState[]
  workflowRunIDs: number[]
  headSHA?: string
  headBranch?: string
  pullRequestNumber?: number
}

type UserPage = "home" | "repositories" | "runner-specs" | "settings"
type AccountSettingsTab = "repositories" | "preferences" | "sandbox-templates" | "sandbox-instances" | "runner-specs"
type AccountSettingsRoute = {
  accountLogin?: string
  tab: AccountSettingsTab
}

type GitHubLogState =
  | { kind: "log"; text: LocalizedLogText }
  | { kind: "unavailable"; detail: string }

const jobLogTabsListClassName = "h-auto w-full justify-start gap-6 rounded-none border-b bg-transparent p-0 text-muted-foreground"
const jobLogTabsTriggerClassName = "h-10 flex-none rounded-none border-x-0 border-t-0 border-b-2 border-transparent bg-transparent px-0 py-2 text-sm font-medium shadow-none hover:text-foreground data-[state=active]:border-primary data-[state=active]:bg-transparent data-[state=active]:text-foreground data-[state=active]:shadow-none dark:data-[state=active]:bg-transparent"

export function UserDashboard({
  authSession,
  githubApp,
  locationPath,
  productTourOnboarding,
  userPreferences,
  userPreferencesScope,
  runners,
  runnerTotal,
  loadingRunnerHistory,
  selectedKey,
  selectedJobID,
  page,
  selectedRepositoryAccountLogin,
  accountSettingsRoute,
  authorizedRepositories,
  repositoryErrors,
  loadingRepositoriesFor,
  syncingGitHubInstallations,
  onLoadAuthorizedRepositories,
  onSyncGitHubInstallations,
  onNavigateProductTourStart,
  onSaveProductTourOnboarding,
  onSaveSandboxConfig,
  onDeleteSandboxAPIKey,
  onSaveCacheConfig,
  onDeleteCacheConfig,
  onNavigate,
  onNavigateRepositoryAccount,
  onNavigateAccountSettings,
  onOpenJob,
  onLoadJobGroup,
  onLoadRunnerHistory,
  request,
  onSelectKey,
  onSignOut,
}: {
  authSession: AuthSession
  githubApp: GitHubAppConfig | null
  locationPath: string
  productTourOnboarding: ProductTourOnboarding | null
  userPreferences: UserPreferences | null
  userPreferencesScope: string
  runners: RunnerState[]
  runnerTotal: number
  loadingRunnerHistory: boolean
  selectedKey: string
  selectedJobID: string
  page: UserPage
  selectedRepositoryAccountLogin: string | null
  accountSettingsRoute: AccountSettingsRoute
  authorizedRepositories: Record<number, string[]>
  repositoryErrors: Record<number, string>
  loadingRepositoriesFor: number | null
  syncingGitHubInstallations: boolean
  onLoadAuthorizedRepositories: (id: number) => void
  onSyncGitHubInstallations: () => void
  onNavigateProductTourStart: () => void
  onSaveProductTourOnboarding: (state: ProductTourOnboarding) => Promise<void>
  onSaveSandboxConfig: (apiURL: string, apiKey: string, installationID?: number, mode?: "custom" | "inherit", replaceInheritedSource?: boolean) => Promise<void>
  onDeleteSandboxAPIKey: (installationID?: number) => Promise<void>
  onSaveCacheConfig: (input: { bucket: string; prefix: string; access_key_id: string; secret_access_key: string }, installationID?: number) => Promise<void>
  onDeleteCacheConfig: (installationID?: number) => Promise<void>
  onNavigate: (page: UserPage) => void
  onNavigateRepositoryAccount: (accountLogin: string | undefined) => void
  onNavigateAccountSettings: (accountLogin: string | undefined, tab: AccountSettingsTab) => void
  onOpenJob: (id: string) => void
  onLoadJobGroup: (key: string) => Promise<unknown>
  onLoadRunnerHistory: () => void
  request: (url: string, options?: RequestInit) => Promise<unknown>
  onSelectKey: (key: string) => void
  onSignOut: () => void
}) {
  const { t } = useTranslation()
  const groups = useMemo(() => groupRunnersByBuildContext(runners, t), [runners, t])
  const selected = groups.find((group) => group.key === selectedKey) || (selectedKey ? undefined : groups[0])
  const [loadedJobGroup, setLoadedJobGroup] = useState<{ key: string; group: RunnerJobGroup } | null>(null)
  const selectedJobGroup = loadedJobGroup && selected && loadedJobGroup.key === selected.key ? loadedJobGroup.group : null
  const installations = useMemo(
    () => orderInstallationsByCurrentAccount(githubApp?.installations ?? [], authSession.login),
    [authSession.login, githubApp?.installations]
  )
  const canSyncGitHubInstallations = Boolean(githubApp?.install_url || githubApp?.app_slug)
  const hasInstallations = installations.length > 0
  const repositoryInstallation = selectRepositoryInstallation(
    installations,
    selectedRepositoryAccountLogin,
    authSession.login,
  )
  const selectedRepositoryPreferenceScope = repositoryPreferenceScope(
    repositoryInstallation,
    authSession.login,
  )
  const repositoryPreferences =
    userPreferencesScope === selectedRepositoryPreferenceScope ? userPreferences : null
  const settingsPreferenceInstallation = settingsPreferenceInstallationID(
    installations,
    accountSettingsRoute.accountLogin,
    authSession.login,
  )
  const selectedSettingsPreferenceScope = settingsPreferenceInstallation
    ? `github_installation:${settingsPreferenceInstallation}`
    : "account"
  const settingsPreferences =
    userPreferencesScope === selectedSettingsPreferenceScope ? userPreferences : null
  const [productTourReplayRequest, setProductTourReplayRequest] = useState(0)
  const openRepositories = useCallback(
    () => onNavigate("repositories"),
    [onNavigate],
  )
  const navItemClass = (active: boolean) =>
    cn(
      "inline-flex h-9 items-center gap-2 rounded-md px-3 text-sm font-medium transition-colors",
      active ? "bg-accent text-accent-foreground" : "text-muted-foreground hover:bg-accent hover:text-accent-foreground"
    )
  const goToPage = (event: MouseEvent<HTMLAnchorElement>, next: UserPage) => {
    event.preventDefault()
    onNavigate(next)
  }

  useEffect(() => {
    let cancelled = false
    if (!selected?.key) return
    void onLoadJobGroup(selected.key)
      .then((group) => {
        if (!cancelled) {
          setLoadedJobGroup(isRunnerJobGroup(group) ? { key: selected.key, group } : null)
        }
      })
      .catch(() => {
        if (!cancelled) setLoadedJobGroup(null)
      })
    return () => {
      cancelled = true
    }
  }, [onLoadJobGroup, selected?.jobs.length, selected?.key, selected?.updatedAt])

  return (
    <main
      className={cn(
        "flex min-h-screen flex-col bg-background text-foreground",
        page === "home" && "xl:h-dvh xl:min-h-0 xl:overflow-hidden",
      )}
    >
      <UserOnboardingTour
        key={productTourReplayRequest}
        locationPath={locationPath}
        onboarding={productTourOnboarding}
        onNavigateRepositories={openRepositories}
        onStatusChange={onSaveProductTourOnboarding}
        replay={productTourReplayRequest > 0}
      />
      <header
        className="flex h-14 shrink-0 items-center gap-3 border-b px-4 lg:px-6"
        data-onboarding="product-shell"
      >
        <div className="flex h-9 w-9 items-center justify-center rounded-lg bg-foreground text-background">
          <Play className="h-5 w-5" />
        </div>
        <div>
          <div className="text-sm font-semibold tracking-wide">Qiniu Runner</div>
        </div>
        <nav className="ml-3 hidden items-center gap-1 md:flex" aria-label={t("user.workspace")}>
          <a
            href="/jobs"
            className={navItemClass(page === "home")}
            data-onboarding="jobs-nav"
            onClick={(event) => goToPage(event, "home")}
          >
            <Workflow className="h-4 w-4" />
            {t("user.jobs")}
          </a>
          <a
            href="/repositories"
            className={navItemClass(page === "repositories")}
            data-onboarding="repositories-nav"
            onClick={(event) => goToPage(event, "repositories")}
          >
            <BookOpen className="h-4 w-4" />
            {t("user.repositories")}
          </a>
          <a
            href="/runner-specs"
            className={navItemClass(page === "runner-specs")}
            onClick={(event) => goToPage(event, "runner-specs")}
          >
            <Boxes className="h-4 w-4" />
            {t("user.runnerSpecs")}
          </a>
        </nav>
        <div className="ml-auto flex items-center gap-2">
          <AccountMenu
            authSession={authSession}
            onReplayProductTour={() => {
              onNavigateProductTourStart()
              setProductTourReplayRequest((current) => current + 1)
            }}
            onSignOut={onSignOut}
          />
        </div>
      </header>

      <nav className="flex items-center gap-1 border-b px-4 py-2 md:hidden" aria-label={t("user.workspace")}>
        <a
          href="/jobs"
          className={navItemClass(page === "home")}
          data-onboarding="jobs-nav"
          onClick={(event) => goToPage(event, "home")}
        >
          <Workflow className="h-4 w-4" />
          {t("user.jobs")}
        </a>
        <a
          href="/repositories"
          className={navItemClass(page === "repositories")}
          data-onboarding="repositories-nav"
          onClick={(event) => goToPage(event, "repositories")}
        >
          <BookOpen className="h-4 w-4" />
          {t("user.repositories")}
        </a>
        <a
          href="/runner-specs"
          className={navItemClass(page === "runner-specs")}
          onClick={(event) => goToPage(event, "runner-specs")}
        >
          <Boxes className="h-4 w-4" />
          {t("user.runnerSpecs")}
        </a>
      </nav>

      {page === "repositories" ? (
        <RepositoryReadinessPage
          githubApp={githubApp ? { ...githubApp, installations } : null}
          currentLogin={authSession.login}
          selectedAccountLogin={selectedRepositoryAccountLogin}
          preferences={repositoryPreferences}
          preferencesLoading={userPreferencesScope !== selectedRepositoryPreferenceScope}
          authorizedRepositories={authorizedRepositories}
          repositoryErrors={repositoryErrors}
          loadingRepositoriesFor={loadingRepositoriesFor}
          syncingGitHubInstallations={syncingGitHubInstallations}
          onLoadAuthorizedRepositories={onLoadAuthorizedRepositories}
          onSyncGitHubInstallations={onSyncGitHubInstallations}
          onSelectAccount={onNavigateRepositoryAccount}
        />
      ) : page === "runner-specs" ? (
        <PlatformRunnerSpecsPage
          request={request}
        />
      ) : page === "settings" ? (
        <AccountsPage
          userPreferences={settingsPreferences}
          installations={installations}
          settingsManageabilityLoaded={githubApp?.settings_manageability === true}
          route={accountSettingsRoute}
          showProductTourSetup={shouldShowSandboxSetupTask(productTourOnboarding)}
          onSaveSandboxConfig={onSaveSandboxConfig}
          onDeleteSandboxAPIKey={onDeleteSandboxAPIKey}
          onSaveCacheConfig={onSaveCacheConfig}
          onDeleteCacheConfig={onDeleteCacheConfig}
          currentLogin={authSession.login}
          onNavigateAccountSettings={onNavigateAccountSettings}
          request={request}
        />
      ) : (
        <PullRequestsPage
          groups={groups}
          hasInstallations={hasInstallations}
          selected={selected}
          selectedJobGroup={selectedJobGroup}
          selectedJobID={selectedJobID}
          onSelectKey={onSelectKey}
          canSyncGitHubInstallations={canSyncGitHubInstallations}
          syncingGitHubInstallations={syncingGitHubInstallations}
          onSyncGitHubInstallations={onSyncGitHubInstallations}
          onOpenJob={onOpenJob}
          request={request}
          runnerCount={runners.length}
          runnerTotal={runnerTotal}
          loadingRunnerHistory={loadingRunnerHistory}
          onLoadRunnerHistory={onLoadRunnerHistory}
        />
      )}
    </main>
  )
}

function PlatformRunnerSpecsPage({
  request,
}: {
  request: (url: string, options?: RequestInit) => Promise<unknown>
}) {
  const { t } = useTranslation()

  return <div className="min-h-0 flex-1 overflow-y-auto">
    <section className="border-b bg-muted/35 px-4 py-4 lg:px-6">
      <h1 className="text-xl font-semibold">{t("user.platformRunnerSpecsPageTitle")}</h1>
      <p className="mt-1 text-sm text-muted-foreground">{t("user.platformRunnerSpecsPageDescription")}</p>
    </section>
    <section className="mx-auto grid w-full max-w-6xl gap-4 p-4 lg:p-6">
      <UserRunnerSpecsSection
        request={request}
        view="platform"
      />
    </section>
  </div>
}

function SyncGitHubInstallationsButton({
  isSyncing,
  label,
  loadingLabel,
  onSync,
  variant,
}: {
  isSyncing: boolean
  label: string
  loadingLabel: string
  onSync: () => void
  variant?: "default" | "outline"
}) {
  return (
    <Button
      type="button"
      variant={variant}
      disabled={isSyncing}
      className={cn(label.length > 16 ? "min-w-[13.5rem]" : "min-w-[8.5rem]")}
      onClick={onSync}
    >
      {isSyncing ? (
        <Loader2 className="h-4 w-4 animate-spin" />
      ) : (
        <Github className="h-4 w-4" />
      )}
      {isSyncing ? loadingLabel : label}
    </Button>
  )
}

function AccountsPage({
  userPreferences,
  installations,
  settingsManageabilityLoaded,
  route,
  showProductTourSetup,
  onSaveSandboxConfig,
  onDeleteSandboxAPIKey,
  onSaveCacheConfig,
  onDeleteCacheConfig,
  currentLogin,
  onNavigateAccountSettings,
  request,
}: {
  userPreferences: UserPreferences | null
  installations: NonNullable<GitHubAppConfig["installations"]>
  settingsManageabilityLoaded: boolean
  route: AccountSettingsRoute
  showProductTourSetup: boolean
  onSaveSandboxConfig: (apiURL: string, apiKey: string, installationID?: number, mode?: "custom" | "inherit", replaceInheritedSource?: boolean) => Promise<void>
  onDeleteSandboxAPIKey: (installationID?: number) => Promise<void>
  onSaveCacheConfig: (input: { bucket: string; prefix: string; access_key_id: string; secret_access_key: string }, installationID?: number) => Promise<void>
  onDeleteCacheConfig: (installationID?: number) => Promise<void>
  currentLogin?: string
  onNavigateAccountSettings: (accountLogin: string | undefined, tab: AccountSettingsTab) => void
  request: (url: string, options?: RequestInit) => Promise<unknown>
}) {
  const { t } = useTranslation()
  const manageableInstallations = manageableSettingsInstallations(installations, currentLogin)
  const normalizedCurrentLogin = currentLogin?.trim().toLowerCase()
  const currentAccountInstallation = manageableInstallations.find(
    (installation) =>
      installation.account_login?.trim().toLowerCase() === normalizedCurrentLogin,
  )
  const settingsInstallations = currentAccountInstallation || !currentLogin
    ? manageableInstallations
    : [
        {
          id: 0,
          account_id: 0,
          installation_id: 0,
          account_login: currentLogin,
          manageable: true,
          repositories: [],
          created_at: "",
          updated_at: "",
        },
        ...manageableInstallations,
      ]
  const scopeAccess = settingsScopeAccessState(
    installations,
    route.accountLogin,
    currentLogin,
    settingsManageabilityLoaded,
  )
  const normalizedRouteLogin = route.accountLogin?.trim().toLowerCase()
  const selected = settingsInstallations.find(
    (installation) =>
      installation.account_login?.trim().toLowerCase() === normalizedRouteLogin,
  )
  const preferenceInstallationID = settingsPreferenceInstallationID(
    settingsInstallations,
    route.accountLogin,
    currentLogin,
  )

  useEffect(() => {
    if (scopeAccess !== "forbidden") return
    const login = route.accountLogin?.trim()
    toast.error(
      login
        ? appI18n.t("user.sandboxPermissionDeniedFor", { login })
        : appI18n.t("user.sandboxPermissionDenied"),
    )
    onNavigateAccountSettings(currentLogin, "preferences")
  }, [
    currentLogin,
    onNavigateAccountSettings,
    route.accountLogin,
    scopeAccess,
  ])

  return (
    <>
      <section
        className="border-b bg-muted/35 px-4 py-4 lg:px-6"
        data-onboarding="settings-tabs"
      >
        <div>
          <h1 className="text-xl font-semibold">{t("user.settings")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {t("user.settingsDescription")}
          </p>
        </div>
      </section>

      <div className="grid min-h-0 flex-1 lg:grid-cols-[320px_minmax(0,1fr)]">
        <aside className="min-h-0 border-r bg-muted/20">
          <div className="flex h-full flex-col">
            <div className="min-h-0 flex-1 overflow-y-auto">
              {settingsInstallations.length ? (
                settingsInstallations.map((installation) => (
                  <button
                    key={installation.id}
                    type="button"
                    className={cn(
                      "flex h-14 w-full items-center gap-3 border-b px-4 text-left transition-colors hover:bg-accent",
                      selected?.id === installation.id ? "bg-accent" : ""
                    )}
                    onClick={() => {
                      onNavigateAccountSettings(installation.account_login, route.tab)
                    }}
                  >
                    <AccountAvatar installation={installation} size="sm" />
                    <div className="min-w-0 flex-1">
                      <div className="truncate text-sm font-semibold">{installation.account_login || "GitHub App"}</div>
                    </div>
                  </button>
                ))
              ) : (
                <div className="p-4 text-sm text-muted-foreground">
                  {t("user.installOrSync")}
                </div>
              )}
            </div>
          </div>
        </aside>

        <section className="min-h-0 overflow-y-auto p-4 lg:p-6">
          {scopeAccess === "loading" ? (
            <div
              className="flex items-center gap-2 rounded-lg border bg-muted/30 p-6 text-sm text-muted-foreground"
              aria-live="polite"
            >
              <Loader2 className="h-4 w-4 animate-spin" />
              {t("user.checkingPermissions")}
            </div>
          ) : scopeAccess === "forbidden" ? (
            <div
              className="flex items-center gap-2 rounded-lg border bg-muted/30 p-6 text-sm text-muted-foreground"
              aria-live="polite"
            >
              <Loader2 className="h-4 w-4 animate-spin" />
              {t("user.openingSettings")}
            </div>
          ) : selected ? (
            <div className="space-y-4">
              <div className="flex items-center gap-3">
                <AccountAvatar installation={selected} size="lg" />
                <div className="min-w-0">
                  <h2 className="truncate text-2xl font-semibold">{accountDisplayName(selected)}</h2>
                  <div className="truncate text-sm text-muted-foreground">{selected.account_login || "GitHub"}</div>
                </div>
              </div>

              <Tabs
                value={route.tab}
                onValueChange={(value) => onNavigateAccountSettings(selected.account_login, value as AccountSettingsTab)}
                className="gap-4"
              >
                <TabsList className="h-auto w-full justify-start rounded-none border-b bg-transparent p-0">
                  <TabsTrigger
                    value="preferences"
                    className="h-10 flex-none rounded-none border-0 border-b-2 border-transparent bg-transparent px-0 py-2 shadow-none data-[state=active]:border-primary data-[state=active]:bg-transparent data-[state=active]:text-primary data-[state=active]:shadow-none"
                  >
                    {t("user.sandboxService")}
                  </TabsTrigger>
                  <TabsTrigger
                    value="runner-specs"
                    className="ml-8 h-10 flex-none rounded-none border-0 border-b-2 border-transparent bg-transparent px-0 py-2 shadow-none data-[state=active]:border-primary data-[state=active]:bg-transparent data-[state=active]:text-primary data-[state=active]:shadow-none"
                  >
                    {t("user.customRunnerSpecs")}
                  </TabsTrigger>
                  <TabsTrigger
                    value="sandbox-templates"
                    className="ml-8 h-10 flex-none rounded-none border-0 border-b-2 border-transparent bg-transparent px-0 py-2 shadow-none data-[state=active]:border-primary data-[state=active]:bg-transparent data-[state=active]:text-primary data-[state=active]:shadow-none"
                  >
                    {t("user.sandboxTemplates")}
                  </TabsTrigger>
                  <TabsTrigger
                    value="sandbox-instances"
                    className="ml-8 h-10 flex-none rounded-none border-0 border-b-2 border-transparent bg-transparent px-0 py-2 shadow-none data-[state=active]:border-primary data-[state=active]:bg-transparent data-[state=active]:text-primary data-[state=active]:shadow-none"
                  >
                    {t("user.sandboxInstances")}
                  </TabsTrigger>
                </TabsList>

                <TabsContent value="preferences">
                  <SandboxAPIKeyCard
                    preferences={userPreferences}
                    allowInheritance={Boolean(preferenceInstallationID)}
                    showOnboardingSetup={showProductTourSetup && !preferenceInstallationID}
                    onSave={(apiURL, apiKey, mode, replaceInheritedSource) => onSaveSandboxConfig(apiURL, apiKey, preferenceInstallationID, mode, replaceInheritedSource)}
                    onDelete={() => onDeleteSandboxAPIKey(preferenceInstallationID)}
                  />
                  <CacheS3Card preferences={userPreferences} installationID={preferenceInstallationID} onSave={onSaveCacheConfig} onDelete={onDeleteCacheConfig} />
                </TabsContent>
                <TabsContent value="runner-specs">
                  <UserRunnerSpecsSection
                    request={request}
                    installationID={preferenceInstallationID}
                    scopeName={selected.account_login || currentLogin}
                    sandboxServiceHref={preferenceInstallationID && selected.account_login
                      ? `/organizations/${encodeURIComponent(selected.account_login)}/preferences`
                      : "/account/preferences"}
                    sandboxTemplatesHref={preferenceInstallationID && selected.account_login
                      ? `/organizations/${encodeURIComponent(selected.account_login)}/sandbox-templates`
                      : "/account/sandbox-templates"}
                  />
                </TabsContent>
                <TabsContent value="sandbox-templates">
                  <SandboxTemplatesSection
                    key={`templates-${preferenceInstallationID ?? "account"}`}
                    request={request}
                    installationID={preferenceInstallationID}
                  />
                </TabsContent>
                <TabsContent value="sandbox-instances">
                  <SandboxesSection
                    key={`instances-${preferenceInstallationID ?? "account"}`}
                    request={request}
                    installationID={preferenceInstallationID}
                  />
                </TabsContent>
              </Tabs>
            </div>
          ) : (
            route.tab === "preferences" ? (
              <div className="space-y-4">
                <div>
                  <h2 className="text-2xl font-semibold">{t("user.accountPreferences")}</h2>
                  <div className="mt-1 text-sm text-muted-foreground">{t("user.accountPreferencesDescription")}</div>
                </div>
                <SandboxAPIKeyCard
                  preferences={userPreferences}
                  showOnboardingSetup={showProductTourSetup}
                  onSave={(apiURL, apiKey, mode) => onSaveSandboxConfig(apiURL, apiKey, undefined, mode)}
                  onDelete={onDeleteSandboxAPIKey}
                />
                <CacheS3Card preferences={userPreferences} onSave={onSaveCacheConfig} onDelete={onDeleteCacheConfig} />
              </div>
            ) : route.tab === "runner-specs" ? (
              <div className="space-y-4"><UserRunnerSpecsSection request={request} scopeName={currentLogin} sandboxServiceHref="/account/preferences" sandboxTemplatesHref="/account/sandbox-templates" /></div>
            ) : route.tab === "sandbox-templates" ? (
              <div className="space-y-4">
                <div>
                  <h2 className="text-2xl font-semibold">{t("user.sandboxTemplates")}</h2>
                  <div className="mt-1 text-sm text-muted-foreground">{t("user.templatesDescription")}</div>
                </div>
                <SandboxTemplatesSection request={request} />
              </div>
            ) : route.tab === "sandbox-instances" ? (
              <div className="space-y-4">
                <div>
                  <h2 className="text-2xl font-semibold">{t("user.sandboxInstances")}</h2>
                  <div className="mt-1 text-sm text-muted-foreground">{t("user.instancesDescription")}</div>
                </div>
                <SandboxesSection request={request} />
              </div>
            ) : (
              <div className="rounded-lg border bg-muted/30 p-6">
                <div className="flex flex-wrap items-center justify-between gap-4">
                  <div className="min-w-0">
                    <h2 className="text-base font-semibold">{t("user.repositorySetupMoved")}</h2>
                    <p className="mt-1 text-sm text-muted-foreground">
                      {t("user.repositorySetupMovedDescription")}
                    </p>
                  </div>
                  <Button type="button" asChild>
                    <a href="/repositories">{t("user.openRepositories")}</a>
                  </Button>
                </div>
              </div>
            )
          )}
        </section>
      </div>
    </>
  )
}

function CacheS3Card({
  preferences,
  installationID,
  onSave,
  onDelete,
}: {
  preferences: UserPreferences | null
  installationID?: number
  onSave: (input: { bucket: string; prefix: string; access_key_id: string; secret_access_key: string }, installationID?: number) => Promise<void>
  onDelete: (installationID?: number) => Promise<void>
}) {
  const { t } = useTranslation()
  const s3Region = preferences?.cache?.region ?? ""
  const s3Endpoint = preferences?.cache?.endpoint ?? ""

  const [bucket, setBucket] = useState(preferences?.cache?.bucket ?? "")
  const [prefix, setPrefix] = useState(preferences?.cache?.prefix ?? "")
  const [accessKeyID, setAccessKeyID] = useState("")
  const [secretAccessKey, setSecretAccessKey] = useState("")
  const [saving, setSaving] = useState(false)
  const [removing, setRemoving] = useState(false)
  const [error, setError] = useState("")
  const configured = Boolean(preferences?.cache?.configured)

  useEffect(() => {
    setBucket(preferences?.cache?.bucket ?? "")
    setPrefix(preferences?.cache?.prefix ?? "")
    setAccessKeyID("")
    setSecretAccessKey("")
  }, [preferences?.cache?.bucket, preferences?.cache?.prefix, installationID])

  const hasRegion = Boolean(s3Region)
  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!hasRegion) {
      setError(t("user.cacheS3RegionRequired"))
      return
    }
    if (!bucket.trim() || (!configured && (!accessKeyID.trim() || !secretAccessKey.trim()))) {
      setError(t("user.cacheS3BucketRequired"))
      return
    }
    setSaving(true)
    setError("")
    try {
      await onSave({ bucket, prefix, access_key_id: accessKeyID, secret_access_key: secretAccessKey }, installationID)
      setAccessKeyID("")
      setSecretAccessKey("")
    } catch (cause) {
      setError(cause instanceof Error ? cause.message : t("user.cacheS3SaveFailed"))
    } finally {
      setSaving(false)
    }
  }
  return (
    <Card className="rounded-lg">
      <form onSubmit={submit}>
        <CardHeader className="gap-2 pb-3"><CardTitle className="text-base">{t("user.cacheS3")}</CardTitle><CardDescription>{t("user.cacheS3Description")}</CardDescription></CardHeader>
        <CardContent className="space-y-4">
          {hasRegion ? (
            <div className="rounded-md border bg-muted/40 px-3 py-2 text-sm text-muted-foreground">
              <span className="font-medium text-foreground">{t("user.cacheS3Region")}</span> {s3Region}{" "}
              <span className="font-medium text-foreground">{t("user.cacheS3Endpoint")}</span> {s3Endpoint}
            </div>
          ) : (
            <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{t("user.cacheS3NoEndpoint")}</div>
          )}
          <div className="grid gap-4 md:grid-cols-2">
            <div className="grid gap-2">
              <Label htmlFor="cache-bucket">{t("user.cacheS3Bucket")}</Label>
              <Input id="cache-bucket" value={bucket} onChange={(event) => setBucket(event.target.value)} placeholder={t("user.cacheS3BucketPlaceholder")} disabled={saving || removing} aria-describedby="cache-bucket-hint" />
              <p id="cache-bucket-hint" className="text-xs text-muted-foreground">{t("user.cacheS3BucketHint")}</p>
            </div>
            <div className="grid gap-2"><Label htmlFor="cache-prefix">{t("user.cacheS3Prefix")}</Label><Input id="cache-prefix" value={prefix} onChange={(event) => setPrefix(event.target.value)} placeholder={t("user.cacheS3PrefixPlaceholder")} disabled={saving || removing} /></div>
            <div className="grid gap-2"><Label htmlFor="cache-ak">{t("user.cacheS3AccessKeyID")}</Label><Input id="cache-ak" type="password" value={accessKeyID} onChange={(event) => setAccessKeyID(event.target.value)} placeholder={configured ? t("user.cacheS3AccessKeyIDPlaceholderNew") : t("user.cacheS3AccessKeyIDPlaceholder")} disabled={saving || removing} /></div>
            <div className="grid gap-2"><Label htmlFor="cache-sk">{t("user.cacheS3SecretAccessKey")}</Label><Input id="cache-sk" type="password" value={secretAccessKey} onChange={(event) => setSecretAccessKey(event.target.value)} placeholder={configured ? t("user.cacheS3SecretAccessKeyPlaceholderNew") : t("user.cacheS3SecretAccessKeyPlaceholder")} disabled={saving || removing} /></div>
          </div>
          <div className="flex items-center gap-2"><Button type="submit" disabled={saving || removing || !hasRegion}>{saving ? t("user.cacheS3Validating") : configured ? t("user.cacheS3SaveChanges") : t("user.cacheS3Save")}</Button>{configured ? <Button type="button" variant="outline" disabled={saving || removing} onClick={async () => { setRemoving(true); setError(""); try { await onDelete(installationID) } catch (cause) { setError(cause instanceof Error ? cause.message : t("user.cacheS3RemoveFailed")) } finally { setRemoving(false) } }}>{t("user.cacheS3Remove")}</Button> : null}</div>
          <div className="text-sm text-muted-foreground">{configured ? `${t("user.cacheS3Configured")}${preferences?.cache?.updated_at ? ` · ${formatTime(preferences.cache.updated_at)}` : ""}` : t("user.cacheS3NotConfigured")}</div>
          {error ? <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div> : null}
        </CardContent>
      </form>
    </Card>
  )
}

function SandboxAPIKeyCard({
  preferences,
  allowInheritance = false,
  showOnboardingSetup = false,
  onSave,
  onDelete,
}: {
  preferences: UserPreferences | null
  allowInheritance?: boolean
  showOnboardingSetup?: boolean
  onSave: (apiURL: string, apiKey: string, mode?: "custom" | "inherit", replaceInheritedSource?: boolean) => Promise<void>
  onDelete: () => Promise<void>
}) {
  const sandboxRegions = useSandboxRegions()
  const { t, i18n } = useTranslation()
  const [apiURL, setAPIURL] = useState("")
  const [apiKey, setAPIKey] = useState("")
  const [credentialMode, setCredentialMode] = useState<"custom" | "inherit">("custom")
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [removeConfirmOpen, setRemoveConfirmOpen] = useState(false)
  const [replaceSourceConfirmOpen, setReplaceSourceConfirmOpen] = useState(false)
  const [error, setError] = useState("")
  const configured = preferences?.sandbox?.api_key?.configured ?? false
  const customConfigured = configured && !preferences?.sandbox?.inherited
  const inherited = credentialMode === "inherit" && Boolean(preferences?.sandbox?.inherited)
  const sourceIsCurrentAccount = Boolean(preferences?.sandbox?.source_is_current_account)
  const sourceAccountLogin = preferences?.sandbox?.source_account_login?.trim()
  const sourceAvailable = Boolean(preferences?.sandbox?.source_available)
  const usingAdminDefault = preferences?.sandbox?.resolved_source === "admin_default"
  const effectiveReady = Boolean(
    preferences && preferences.sandbox.resolved_source !== "none",
  )
  const updatedAt = preferences?.sandbox?.api_key?.updated_at
  const savedAPIURL = preferences?.sandbox?.api_url ?? ""
  const effectiveAPIURL = apiURL
  const selectedRegion = findSandboxRegionByAPIURL(sandboxRegions, effectiveAPIURL)
  const organizationManaged = preferences?.sandbox?.manageable === false

  useEffect(() => {
    setAPIURL(findSandboxRegionByAPIURL(sandboxRegions, savedAPIURL)?.apiURL ?? "")
  }, [sandboxRegions, savedAPIURL])

  useEffect(() => {
    setCredentialMode(allowInheritance && preferences?.sandbox?.mode === "inherit" ? "inherit" : "custom")
  }, [allowInheritance, preferences?.sandbox?.mode])

  const submit = async (event: FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    const nextAPIURL = effectiveAPIURL.trim()
    const nextAPIKey = apiKey.trim()
    if (credentialMode === "inherit") {
      setSaving(true)
      setError("")
      try {
        await onSave("", "", "inherit")
        setAPIKey("")
      } catch (error) {
        setError(error instanceof Error ? error.message : t("user.sandboxSaveFailed"))
      } finally {
        setSaving(false)
      }
      return
    }
    if (!nextAPIURL) {
      setError(t("user.sandboxRegionRequired"))
      return
    }
    if (!customConfigured && !nextAPIKey) {
      setError(t("user.sandboxAPIKeyRequired"))
      return
    }
    setSaving(true)
    setError("")
    try {
      await onSave(nextAPIURL, nextAPIKey, "custom")
      setAPIKey("")
    } catch (error) {
      setError(error instanceof Error ? error.message : t("user.sandboxSaveFailed"))
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    setDeleting(true)
    setError("")
    try {
      await onDelete()
      setAPIKey("")
      setRemoveConfirmOpen(false)
    } catch (error) {
      setError(error instanceof Error ? error.message : t("user.sandboxRemoveFailed"))
    } finally {
      setDeleting(false)
    }
  }

  const replaceInheritedSource = async () => {
    setSaving(true)
    setError("")
    try {
      await onSave("", "", "inherit", true)
      setAPIKey("")
      setReplaceSourceConfirmOpen(false)
    } catch (error) {
      setError(error instanceof Error ? error.message : t("user.sandboxUseAccountFailed"))
    } finally {
      setSaving(false)
    }
  }

  if (organizationManaged) {
    return (
      <Card className="rounded-lg" data-onboarding="sandbox-service-form">
        <CardHeader className="gap-3 pb-3">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0">
              <CardTitle className="flex items-center gap-2 text-base">
                <KeyRound className="h-4 w-4 shrink-0" />
                <span>{t("user.sandboxService")}</span>
              </CardTitle>
              <CardDescription className="mt-1">
                {t("user.organizationManagedDescription")}
              </CardDescription>
            </div>
            <Badge variant="outline">{t("user.organizationManaged")}</Badge>
          </div>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            {effectiveReady
              ? t("user.organizationReady")
              : t("user.organizationMissing")}
          </p>
        </CardContent>
      </Card>
    )
  }

  return (
    <Card className="rounded-lg" data-onboarding="sandbox-service-form">
      <form onSubmit={submit}>
        <CardHeader className="gap-3 pb-3">
          <div className="flex flex-wrap items-start justify-between gap-3">
            <div className="min-w-0">
              <CardTitle className="flex items-center gap-2 text-base">
                <KeyRound className="h-4 w-4 shrink-0" />
                <span>{t("user.sandboxService")}</span>
              </CardTitle>
              <CardDescription className="mt-1">
                {t("user.sandboxConfigDescription")}
              </CardDescription>
            </div>
            <Badge variant={effectiveReady ? "success" : "warning"}>
              {effectiveReady ? t("repositories.ready") : t("user.actionRequired")}
            </Badge>
          </div>
        </CardHeader>
        <CardContent className="space-y-4">
          {showOnboardingSetup && !effectiveReady ? (
            <div className="flex flex-col gap-3 rounded-lg border border-primary/25 bg-primary/[0.04] p-4 sm:flex-row sm:items-start sm:justify-between">
              <div className="min-w-0">
                <div className="flex flex-wrap items-center gap-2">
                  <Badge variant="outline">{t("user.setupStep")}</Badge>
                  <span className="text-sm font-semibold">{t("user.connectSandbox")}</span>
                </div>
                <p className="mt-2 max-w-2xl text-sm leading-6 text-muted-foreground">
                  {t("user.connectSandboxDescription")}
                </p>
              </div>
              <div className="flex shrink-0 flex-wrap gap-2">
                <Button type="button" variant="outline" size="sm" asChild>
                  <a href="https://developer.qiniu.com/las/13283/sandbox-quickstart" target="_blank" rel="noreferrer">
                    {t("user.quickstart")}
                    <ExternalLink className="h-3.5 w-3.5" />
                  </a>
                </Button>
                <Button type="button" size="sm" asChild>
                  <a href="https://portal.qiniu.com/developer/user/api-key" target="_blank" rel="noreferrer">
                    {t("user.getAPIKey")}
                    <ExternalLink className="h-3.5 w-3.5" />
                  </a>
                </Button>
              </div>
            </div>
          ) : null}

          {allowInheritance ? (
            <div className="flex flex-wrap items-center gap-3 rounded-md border bg-muted/20 px-3 py-3">
              <Switch
                id="sandbox-use-account-default"
                checked={credentialMode === "inherit"}
                onCheckedChange={(checked) => {
                  setCredentialMode(checked ? "inherit" : "custom")
                  if (!checked && preferences?.sandbox?.inherited) {
                    setAPIURL("")
                    setAPIKey("")
                  }
                  setError("")
                }}
                disabled={saving || deleting}
              />
              <Label htmlFor="sandbox-use-account-default" className="cursor-pointer text-sm font-medium">
                {t("user.useAccountDefault")}
              </Label>
              <span className="text-sm text-muted-foreground">
                {credentialMode !== "inherit"
                  ? t("user.organizationOwnSettings")
                  : !preferences?.sandbox?.inherited
                    ? t("user.accountDefaultAfterSave")
                  : !sourceAvailable
                    ? sourceAccountLogin
                      ? t("user.inheritedCredentialsUnavailableBy", { login: sourceAccountLogin })
                      : t("user.inheritedCredentialsUnavailable")
                  : sourceIsCurrentAccount
                    ? t("user.usingAccountCredentials")
                    : sourceAccountLogin
                      ? t("user.usingCredentialsBy", { login: sourceAccountLogin })
                      : t("user.usingOtherOwnerCredentials")}
              </span>
            </div>
          ) : null}

          <div className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_auto] xl:items-end">
            <div className="grid min-w-0 gap-2">
              <Label htmlFor="sandbox-api-region">{t("common.region")}</Label>
              <Select
                value={selectedRegion?.id ?? ""}
                onValueChange={(regionID) => {
                  const region = sandboxRegions.find((region) => region.id === regionID)
                  setAPIURL(region?.apiURL ?? "")
                }}
                disabled={saving || deleting || credentialMode === "inherit"}
              >
                <SelectTrigger id="sandbox-api-region" className="w-full">
                  {selectedRegion ? (
                    <span className="truncate">{selectedRegion.label}</span>
                  ) : (
                    <SelectValue placeholder={t("user.selectRegion")} />
                  )}
                </SelectTrigger>
                <SelectContent>
                  {sandboxRegions.map((region) => (
                    <SelectItem key={region.id} value={region.id} textValue={region.label}>
                      <span>{region.label}</span>
                      <span className="text-muted-foreground">{region.id}</span>
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="grid min-w-0 gap-2">
              <Label htmlFor="sandbox-api-key">{t("common.apiKey")}</Label>
              <Input
                id="sandbox-api-key"
                type="password"
                value={apiKey}
                onChange={(event) => setAPIKey(event.target.value)}
                autoComplete="off"
                disabled={saving || deleting || credentialMode === "inherit"}
                placeholder={customConfigured ? t("user.apiKeyReplacementPlaceholder") : t("user.apiKeyPlaceholder")}
              />
            </div>

            <div className="flex flex-wrap items-center gap-2">
              {!inherited ? (
                <Button type="submit" disabled={saving || deleting || (credentialMode === "custom" && (!effectiveAPIURL.trim() || (!customConfigured && !apiKey.trim())))}>
                  <ShieldCheck className="h-4 w-4" />
                  {saving ? t("user.saving") : configured ? t("user.saveChanges") : t("user.saveSettings")}
                </Button>
              ) : null}
              {inherited && !sourceIsCurrentAccount ? (
                <Button
                  type="button"
                  variant="outline"
                  onClick={() => {
                    setError("")
                    setReplaceSourceConfirmOpen(true)
                  }}
                  disabled={saving || deleting}
                >
                  <KeyRound className="h-4 w-4" />
                  {t("user.useMyAccountCredentials")}
                </Button>
              ) : null}
              {customConfigured && credentialMode === "custom" ? (
                <Button type="button" variant="outline" onClick={() => setRemoveConfirmOpen(true)} disabled={deleting || saving}>
                  <X className="h-4 w-4" />
                  {deleting ? t("user.removing") : t("user.remove")}
                </Button>
              ) : null}
            </div>
          </div>

          <div className="text-sm text-muted-foreground">
            {configured && updatedAt ? t("user.updatedAt", { time: formatTime(updatedAt, i18n.resolvedLanguage) }) : t("user.noAPIKeySaved")}
          </div>

          {usingAdminDefault ? (
            <div className="rounded-md border bg-muted/20 px-3 py-2 text-sm text-muted-foreground">
              {t("user.platformDefault")}
            </div>
          ) : null}

          {error ? <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div> : null}
        </CardContent>
      </form>
      <Dialog open={removeConfirmOpen} onOpenChange={(open) => !deleting && setRemoveConfirmOpen(open)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("user.removeAPIKeyTitle")}</DialogTitle>
            <DialogDescription>
              {t("user.removeAPIKeyDescription")}
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline" disabled={deleting}>
                {t("common.cancel")}
              </Button>
            </DialogClose>
            <Button type="button" variant="destructive" onClick={remove} disabled={deleting}>
              <X className="h-4 w-4" />
              {deleting ? t("user.removing") : t("user.removeAPIKey")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      <Dialog open={replaceSourceConfirmOpen} onOpenChange={(open) => !saving && setReplaceSourceConfirmOpen(open)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("user.useAccountCredentialsTitle")}</DialogTitle>
            <DialogDescription>
              {sourceAccountLogin
                ? t("user.replaceCredentialsDescriptionBy", { login: sourceAccountLogin })
                : t("user.replaceCredentialsDescription")}
            </DialogDescription>
          </DialogHeader>
          {error ? <div className="rounded-md border border-destructive/30 bg-destructive/10 px-3 py-2 text-sm text-destructive">{error}</div> : null}
          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline" disabled={saving}>
                {t("common.cancel")}
              </Button>
            </DialogClose>
            <Button type="button" onClick={replaceInheritedSource} disabled={saving}>
              <KeyRound className="h-4 w-4" />
              {saving ? t("user.switching") : t("user.useMyCredentials")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
  )
}

function PullRequestsPage({
  groups,
  hasInstallations,
  selected,
  selectedJobGroup,
  selectedJobID,
  onSelectKey,
  canSyncGitHubInstallations,
  syncingGitHubInstallations,
  onSyncGitHubInstallations,
  onOpenJob,
  request,
  runnerCount,
  runnerTotal,
  loadingRunnerHistory,
  onLoadRunnerHistory,
}: {
  groups: BuildGroup[]
  hasInstallations: boolean
  selected: BuildGroup | undefined
  selectedJobGroup: RunnerJobGroup | null
  selectedJobID: string
  onSelectKey: (key: string) => void
  canSyncGitHubInstallations: boolean
  syncingGitHubInstallations: boolean
  onSyncGitHubInstallations: () => void
  onOpenJob: (id: string) => void
  request: (url: string, options?: RequestInit) => Promise<unknown>
  runnerCount: number
  runnerTotal: number
  loadingRunnerHistory: boolean
  onLoadRunnerHistory: () => void
}) {
  const { t, i18n } = useTranslation()
  const currentJobs = selectedJobGroup?.current_jobs || (selected ? currentBuildJobs(selected) : [])
  const previousJobs = selectedJobGroup?.previous_jobs || (selected ? previousBuildJobs(selected, currentJobs) : [])
  const allJobs = [...currentJobs, ...previousJobs]
  const selectedJob = allJobs.find((job) => job.id === selectedJobID) || allJobs[0] || null
  const effectiveSelectedJobID = selectedJob?.id || ""
  const workflows = workflowGroups(allJobs, t)
  const selectedStatus = selected ? buildGroupStatus(selected) : null
  const visibleHistoryTotal = Math.min(runnerTotal, userRunnerHistoryWindow)
  const olderRunnerCount = Math.max(0, visibleHistoryTotal - runnerCount)

  return (
    <>
      <section className="border-b bg-muted/35 px-4 py-4 lg:px-6">
        <div>
          <h1 className="text-xl font-semibold">{t("user.jobs")}</h1>
          <p className="mt-1 text-sm text-muted-foreground">
            {t("user.jobsDescription")}
          </p>
        </div>
      </section>

      <div className="grid min-h-0 flex-1 xl:grid-cols-[360px_minmax(0,1fr)] xl:overflow-hidden">
        <aside className="min-h-0 border-r bg-muted/20">
          <div className="flex h-full flex-col">
            <div className="min-h-0 flex-1 overflow-y-auto">
              {groups.length ? (
                <>
                  {groups.map((group) => {
                  const isSelected = selected?.key === group.key
                  const showSubmenu = isSelected && allJobs.length > 1
                  return (
                    <div key={group.key} className="border-b">
                      <BuildGroupListItem
                        group={group}
                        selected={isSelected}
                        onSelect={() => onSelectKey(group.key)}
                      />
                      {showSubmenu ? (
                        <div className="border-t border-border/40 bg-background/70 py-1">
                          <WorkflowJobExplorer
                            workflows={workflows}
                            selectedJobID={effectiveSelectedJobID}
                            onOpenJob={onOpenJob}
                          />
                        </div>
                      ) : null}
                    </div>
                  )
                  })}
                  {olderRunnerCount > 0 ? (
                    <div className="border-b p-3">
                      <Button
                        className="w-full"
                        disabled={loadingRunnerHistory}
                        onClick={onLoadRunnerHistory}
                        type="button"
                        variant="outline"
                      >
                        {loadingRunnerHistory ? t("user.loadingOlderJobs") : t("user.loadOlderJobs", { count: olderRunnerCount })}
                      </Button>
                    </div>
                  ) : null}
                </>
              ) : (
                <div className="p-4 text-sm text-muted-foreground">
                  {hasInstallations ? (
                    t("user.noJobsYet")
                  ) : (
                    t("user.syncToTrackJobs")
                  )}
                </div>
              )}
            </div>
          </div>
        </aside>

        <section className="min-h-0 overflow-y-auto">
          {selected ? (
            <div className="flex min-h-full flex-col">
              <div className="border-b px-4 py-4 lg:px-6">
                <h2 className="truncate text-2xl font-semibold">{selected.repository}</h2>
              </div>
              <div className="flex min-h-0 flex-1 flex-col gap-4 p-4 lg:p-6">
                <div className="shrink-0 border-b pb-4">
                  <h3 className="flex flex-wrap items-center gap-x-3 gap-y-1 text-2xl font-semibold">
                    <span>{pullRequestHeading(selected, selectedJobGroup)}</span>
                    {selectedStatus ? <BuildGroupStatusBadge group={selected} status={selectedStatus} /> : null}
                  </h3>
                  <div className="mt-3 space-y-4 text-sm">
                    <section>
                      <div className="grid gap-3 sm:grid-cols-3">
                        <JobField label={t("user.branch")} value={selectedJobGroup?.head_branch || selected.headBranch || selected.subtitle || t("common.unknown")} />
                        <JobField label={t("user.commit")} value={shortSHA(selectedJobGroup?.head_sha || selected.headSHA) || t("common.unknown")} />
                        <JobField label={t("user.lastUpdated")} value={formatTime(selectedJobGroup?.updated_at || selected.updatedAt, i18n.resolvedLanguage)} />
                      </div>
                    </section>
                    <section>
                      <div className="grid gap-3 sm:grid-cols-3">
                        {selectedJob ? (
                          <>
                            <JobField
                              label={t("user.jobName")}
                              value={selectedJob.github_job_url ? (
                                <a className={cn("inline-flex max-w-full min-w-0 items-center gap-1 hover:underline", jobStatusTextClass(selectedJob.status))} href={selectedJob.github_job_url} target="_blank" rel="noreferrer">
                                  <span className="truncate">{runnerJobTitle(selectedJob)}</span>
                                  <ExternalLink className="h-3.5 w-3.5 shrink-0" />
                                </a>
                              ) : (
                                <span className={cn("block truncate", jobStatusTextClass(selectedJob.status))}>{runnerJobTitle(selectedJob)}</span>
                              )}
                            />
                            <JobField
                              label={t("user.workflow")}
                              value={workflowRunURL(selectedJob) ? (
                                <a className="inline-flex max-w-full min-w-0 items-center gap-1 text-primary hover:underline" href={workflowRunURL(selectedJob)} target="_blank" rel="noreferrer">
                                  <span className="truncate">{selectedJob.workflow_name || t("user.workflow")}</span>
                                  <ExternalLink className="h-3.5 w-3.5 shrink-0" />
                                </a>
                              ) : (
                                <span className="block truncate">{selectedJob.workflow_name || t("user.workflow")}</span>
                              )}
                            />
                            <JobField label={t("user.duration")} value={formatRunnerDuration(selectedJob) || "-"} />
                          </>
                        ) : (
                          <div className="text-muted-foreground">{t("user.selectJob")}</div>
                        )}
                      </div>
                    </section>
                  </div>
                </div>
                {selectedJob ? <RunnerJobLogPanel job={selectedJob} request={request} /> : (
                  <div className="rounded-lg border bg-muted/30 p-6 text-sm text-muted-foreground">
                    {t("user.selectJob")}
                  </div>
                )}
              </div>
            </div>
          ) : (
            <div className="p-4 lg:p-6">
              {groups.length ? (
                <div className="rounded-lg border bg-muted/30 p-6 text-sm text-muted-foreground">
                  {t("user.groupNotFound")}
                </div>
              ) : hasInstallations ? (
                <div className="rounded-lg border bg-muted/30 p-6 text-sm text-muted-foreground">
                  {t("user.noRunnerJobs")}
                </div>
              ) : (
                <div className="rounded-lg border bg-muted/30 p-6">
                  <div className="flex flex-wrap items-center justify-between gap-4">
                    <div className="min-w-0">
                      <h2 className="text-base font-semibold">{t("user.syncExistingAccounts")}</h2>
                      <p className="mt-1 text-sm text-muted-foreground">
                        {canSyncGitHubInstallations
                          ? t("user.syncHelp")
                          : t("user.setupAuth")}
                      </p>
                    </div>
                    {canSyncGitHubInstallations ? (
                      <SyncGitHubInstallationsButton
                        isSyncing={syncingGitHubInstallations}
                        label={t("user.syncAccounts")}
                        loadingLabel={t("user.syncing")}
                        onSync={onSyncGitHubInstallations}
                      />
                    ) : null}
                  </div>
                </div>
              )}
            </div>
          )}
        </section>
      </div>
    </>
  )
}

function JobField({ label, value, onOpen }: { label: string; value: ReactNode; onOpen?: () => void }) {
  return (
    <div className="grid grid-cols-[88px_minmax(0,1fr)] items-baseline gap-2">
      <div className="text-xs text-muted-foreground">{label}</div>
      {onOpen ? (
        <button type="button" className="min-w-0 break-words text-left font-medium hover:text-primary hover:underline" onClick={onOpen}>
          {value}
        </button>
      ) : (
        <div className="min-w-0 break-words font-medium">{value}</div>
      )}
    </div>
  )
}

function WorkflowJobExplorer({
  workflows,
  selectedJobID,
  onOpenJob,
}: {
  workflows: ReturnType<typeof workflowGroups>
  selectedJobID: string
  onOpenJob: (id: string) => void
}) {
  return (
    <div className="grid gap-0">
      {workflows.map((workflow) => (
        workflow.jobs.length === 1 ? (
          <WorkflowRunListItem
            key={workflow.id}
            workflow={workflow}
            selectedJobID={selectedJobID}
            onOpenJob={onOpenJob}
          />
        ) : (
          <section key={workflow.id} className="grid gap-0">
            <div className="grid gap-0">
              {workflow.jobs.map((job) => (
                <RunnerJobListItem key={job.id} job={job} selected={job.id === selectedJobID} onOpen={() => onOpenJob(job.id)} />
              ))}
            </div>
          </section>
        )
      ))}
    </div>
  )
}

function WorkflowRunListItem({
  workflow,
  selectedJobID,
  onOpenJob,
}: {
  workflow: ReturnType<typeof workflowGroups>[number]
  selectedJobID: string
  onOpenJob: (id: string) => void
}) {
  const job = workflow.jobs[0]
  const selected = job.id === selectedJobID
  const status = workflowStatus(workflow.jobs)

  return (
    <button
      type="button"
      onClick={() => onOpenJob(job.id)}
      className={cn(
        "grid w-full grid-cols-[32px_minmax(0,1fr)_auto] items-center gap-2 px-4 py-1.5 text-left text-sm transition-colors",
        selected ? "bg-primary/10 text-primary shadow-[inset_3px_0_0_hsl(var(--primary))]" : "hover:bg-muted/80"
      )}
    >
      <span className={cn("flex justify-center", buildGroupStatusClasses(status).icon)}>{jobStatusMark(job.status)}</span>
      <span className="min-w-0">
        <span className="block truncate font-medium">{workflow.name}</span>
      </span>
      <span className="shrink-0 text-xs text-muted-foreground">{formatRunnerDuration(job)}</span>
    </button>
  )
}

function RunnerJobListItem({ job, selected, onOpen }: { job: RunnerState; selected: boolean; onOpen: () => void }) {
  return (
    <button
      type="button"
      onClick={onOpen}
      className={cn(
        "grid w-full grid-cols-[32px_minmax(0,1fr)_auto] items-center gap-2 px-4 py-1.5 text-left text-sm transition-colors",
        selected ? "bg-primary/10 text-primary shadow-[inset_3px_0_0_hsl(var(--primary))]" : "hover:bg-muted/80"
      )}
    >
      <span className={cn("flex justify-center", buildGroupStatusClasses(jobStatusSummary(job.status)).icon)}>{jobStatusMark(job.status)}</span>
      <span className="min-w-0">
        <span className="block truncate font-medium">{runnerJobTitle(job)}</span>
      </span>
      <span className="shrink-0 text-xs text-muted-foreground">{formatRunnerDuration(job)}</span>
    </button>
  )
}

function RunnerJobLogPanel({
  job,
  request,
}: {
  job: RunnerState
  request: (url: string, options?: RequestInit) => Promise<unknown>
}) {
  const { t, i18n } = useTranslation()
  const [selectedLog, setSelectedLog] = useState<(typeof logNames)[number]>("control.log")
  const [runnerLogText, setRunnerLogText] = useState<LocalizedLogText>({ kind: "message", key: "user.loadingRunnerLog" })
  const [githubLog, setGithubLog] = useState<GitHubLogState>({ kind: "log", text: { kind: "message", key: "user.loadingGitHubLog" } })
  const [githubLogLoading, setGithubLogLoading] = useState(false)
  const endpoint = `/user/runner_requests/${encodeURIComponent(job.id)}`
  const endpointRef = useRef(endpoint)
  const terminalAvailable = isTerminalAvailable(job)
  const { terminalEl, terminalSession, terminalError, terminalConnecting, connectTerminal } = useSandboxTerminal({
    endpoint,
    available: terminalAvailable,
    request,
    connectingMessage: t("user.connectingConsole"),
    streamDisconnectedMessage: t("user.consoleDisconnected"),
    connectErrorMessage: t("user.consoleConnectFailed"),
  })

  useEffect(() => {
    endpointRef.current = endpoint
  }, [endpoint])

  useEffect(() => {
    let active = true
    queueMicrotask(() => {
      if (active) {
        setRunnerLogText({ kind: "message", key: "user.loadingRunnerLog" })
      }
    })
    void request(`${endpoint}/logs/${encodeURIComponent(selectedLog)}`)
      .then((text) => {
        if (active) {
          setRunnerLogText(logResponseText(text, "user.runnerLogEmpty"))
        }
      })
      .catch((error) => {
        if (active) {
          setRunnerLogText(error instanceof Error
            ? { kind: "text", text: error.message }
            : { kind: "message", key: "user.runnerLogFailed" })
        }
      })
    return () => {
      active = false
    }
  }, [endpoint, request, selectedLog])

  useEffect(() => {
    let active = true
    queueMicrotask(() => {
      if (active) {
        setGithubLogLoading(true)
        setGithubLog({ kind: "log", text: { kind: "message", key: "user.loadingGitHubLog" } })
      }
    })
    void request(`${endpoint}/github-log`)
      .then((text) => {
        if (active) {
          setGithubLog(githubLogResponseState(text, "user.githubLogEmpty"))
        }
      })
      .catch((error) => {
        if (active) {
          setGithubLog(githubLogErrorState(error))
        }
      })
      .finally(() => {
        if (active) {
          setGithubLogLoading(false)
        }
      })
    return () => {
      active = false
    }
  }, [endpoint, request])

  const refreshGithubLog = () => {
    const refreshEndpoint = endpoint
    setGithubLogLoading(true)
    setGithubLog({ kind: "log", text: { kind: "message", key: "user.loadingGitHubLog" } })
    void request(`${refreshEndpoint}/github-log`)
      .then((text) => {
        if (endpointRef.current === refreshEndpoint) {
          setGithubLog(githubLogResponseState(text, "user.githubLogEmpty"))
        }
      })
      .catch((error) => {
        if (endpointRef.current === refreshEndpoint) {
          setGithubLog(githubLogErrorState(error))
        }
      })
      .finally(() => {
        if (endpointRef.current === refreshEndpoint) {
          setGithubLogLoading(false)
        }
      })
  }

  const githubLogActions = (
    <Button
      type="button"
      variant="outline"
      size="sm"
      className="border-white/15 bg-white/5 text-slate-100 hover:bg-white/10 hover:text-white"
      onClick={refreshGithubLog}
      disabled={githubLogLoading}
    >
      <RefreshCw className={cn(githubLogLoading && "animate-spin")} />
      {t("user.refresh")}
    </Button>
  )

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <Tabs defaultValue="github-logs" className="flex min-h-0 flex-1 flex-col gap-0">
        <TabsList className={jobLogTabsListClassName}>
          <TabsTrigger className={jobLogTabsTriggerClassName} value="github-logs">{t("user.githubLogs")}</TabsTrigger>
          <TabsTrigger className={jobLogTabsTriggerClassName} value="runner-logs">{t("user.runnerLogs")}</TabsTrigger>
          <TabsTrigger className={jobLogTabsTriggerClassName} value="web-console">{t("user.webConsole")}</TabsTrigger>
          <TabsTrigger className={jobLogTabsTriggerClassName} value="details">{t("user.details")}</TabsTrigger>
        </TabsList>
        <TabsContent value="github-logs" className="m-0 pt-2">
          {githubLog.kind === "unavailable" ? (
            <GitHubLogsUnavailable detail={githubLog.detail} actions={githubLogActions} />
          ) : (
            <LogOutput
              text={localizedLogTextForView(githubLog.text, t)}
              description={t("user.githubLogSource")}
              actions={githubLogActions}
            />
          )}
        </TabsContent>
        <TabsContent value="runner-logs" className="m-0 pt-2">
          <LogOutput
            text={localizedLogTextForView(runnerLogText, t)}
            description={t("user.runnerLogDescription", { log: selectedLog.replace(".log", "") })}
            leading={(
              <div className="flex items-center gap-1 rounded-md border border-white/10 bg-white/5 p-1" aria-label={t("user.runnerLogStream")}>
                {logNames.map((name) => {
                  const value = name.replace(".log", "")
                  return (
                    <button
                      key={name}
                      type="button"
                      className={cn(
                        "rounded px-2 py-1 text-xs font-medium text-slate-300 transition-colors hover:bg-white/10 hover:text-white",
                        selectedLog === name && "bg-emerald-400/15 text-emerald-100"
                      )}
                      aria-pressed={selectedLog === name}
                      onClick={() => setSelectedLog(name)}
                    >
                      {value}
                    </button>
                  )
                })}
              </div>
            )}
          />
        </TabsContent>
        <TabsContent value="web-console" forceMount className="m-0 flex min-h-0 flex-1 flex-col overflow-hidden pt-2 data-[state=inactive]:hidden">
          <div className="flex min-h-0 flex-1 flex-col overflow-hidden border-y border-emerald-500/15 bg-[#111318] text-slate-100 shadow-[inset_3px_0_0_theme(colors.emerald.500/0.35)]">
            <div className="flex flex-wrap items-center justify-between gap-3 border-b border-white/10 bg-slate-900/95 px-4 py-3">
              <div className="min-w-0">
                <div className="truncate text-xs text-slate-400">{job.sandbox_id || t("user.noActiveSandbox")}</div>
              </div>
              <Button
                type="button"
                size="sm"
                className="border-white/15 bg-white/5 text-slate-100 hover:bg-white/10 hover:text-white disabled:opacity-60"
                variant="outline"
                onClick={() => void connectTerminal()}
                disabled={!terminalAvailable || terminalConnecting || Boolean(terminalSession)}
              >
                <SquareTerminal />
                {terminalSession ? t("common.connected") : terminalConnecting ? t("common.connecting") : t("common.connect")}
              </Button>
            </div>
            {terminalError ? <WebConsoleError message={terminalError} /> : null}
            {terminalAvailable ? (
              <div className="relative min-h-0 flex-1 p-2">
                <div ref={terminalEl} className="h-full min-h-0 overflow-hidden rounded-md" />
                {!terminalSession ? (
                  <div className="absolute inset-2 flex items-center justify-center rounded-md bg-[#111318] text-sm text-slate-300">
                    {t("user.connectInteractiveShell")}
                  </div>
                ) : null}
              </div>
            ) : (
              <WebConsoleUnavailable job={job} />
            )}
          </div>
        </TabsContent>
        <TabsContent value="details" className="m-0 py-5">
          <div className="grid gap-2 text-sm">
            <JobField label={t("common.status")} value={runnerStatusLabel(job.status)} />
            <JobField label={t("common.runnerSpec")} value={job.runner_spec_name || t("user.matchedByLabels")} />
            <JobField label={t("user.workflowRun")} value={workflowRunValue(job, t)} />
            <JobField label={t("user.queued")} value={formatTime(job.created_at, i18n.resolvedLanguage)} />
            <JobField label={t("common.started")} value={job.running_at ? formatTime(job.running_at, i18n.resolvedLanguage) : "-"} />
            <JobField label={t("user.finished")} value={job.completed_at || job.failed_at ? formatTime(job.completed_at || job.failed_at, i18n.resolvedLanguage) : "-"} />
            <JobField label={t("user.commit")} value={job.head_sha || "-"} />
          </div>
        </TabsContent>
      </Tabs>
    </div>
  )
}

function logResponseText(text: unknown, emptyMessageKey: LocalizedLogMessageKey): LocalizedLogText {
  if (typeof text === "string") {
    return text
      ? { kind: "text", text }
      : { kind: "message", key: emptyMessageKey }
  }
  return { kind: "text", text: JSON.stringify(text, null, 2) }
}

function githubLogResponseState(text: unknown, emptyMessageKey: LocalizedLogMessageKey): GitHubLogState {
  return { kind: "log", text: logResponseText(text, emptyMessageKey) }
}

function githubLogErrorState(error: unknown): GitHubLogState {
  const failure = githubLogFailureState(error)
  return failure.kind === "text" && isGitHubLogUnavailable(failure.text)
    ? { kind: "unavailable", detail: failure.text }
    : { kind: "log", text: failure }
}

function isGitHubLogUnavailable(text: string) {
  const value = text.toLowerCase()
  return (
    value.includes("status 404") ||
    value.includes("blobnotfound") ||
    value.includes("the specified blob does not exist")
  )
}

function GitHubLogsUnavailable({ detail, actions }: { detail: string; actions: ReactNode }) {
  const { t } = useTranslation()
  return (
    <div className="overflow-hidden border-y border-emerald-500/15 bg-slate-950 text-slate-100 shadow-[inset_3px_0_0_theme(colors.emerald.500/0.35)]">
      <div className="flex flex-wrap items-center justify-between gap-3 border-b border-white/10 bg-slate-900/95 px-4 py-3">
        <div className="text-xs text-slate-400">{t("user.githubLogSource")}</div>
        <div className="flex items-center gap-2">{actions}</div>
      </div>
      <div className="flex min-h-[260px] items-center justify-center px-4 py-12">
        <div className="max-w-xl text-center">
          <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-md border border-white/10 bg-white/5 text-amber-200">
            <AlertCircle className="h-5 w-5" />
          </div>
          <h3 className="mt-4 text-sm font-semibold text-slate-100">{t("user.githubLogsUnavailable")}</h3>
          <p className="mt-2 text-sm leading-6 text-slate-400">
            {t("user.githubLogsUnavailableDescription")}
          </p>
          <details className="mt-5 rounded-md border border-white/10 bg-white/[0.03] text-left">
            <summary className="cursor-pointer px-3 py-2 text-xs font-medium text-slate-300 hover:text-slate-100">
              {t("user.showTechnicalDetails")}
            </summary>
            <pre className="max-h-48 overflow-auto border-t border-white/10 px-3 py-3 font-mono text-xs leading-relaxed whitespace-pre-wrap break-words text-slate-400">
              {detail}
            </pre>
          </details>
        </div>
      </div>
    </div>
  )
}

function WebConsoleError({ message }: { message: string }) {
  const { t } = useTranslation()
  return (
    <div className="border-b border-red-400/15 bg-red-500/10 px-4 py-3 text-sm text-red-100">
      <div className="flex min-w-0 items-start gap-2">
        <AlertCircle className="mt-0.5 h-4 w-4 shrink-0 text-red-200" />
        <div className="min-w-0">
          <div className="font-medium">{t("user.webConsoleFailed")}</div>
          <div className="mt-1 break-words font-mono text-xs leading-relaxed text-red-100/80">{message}</div>
        </div>
      </div>
    </div>
  )
}

function WebConsoleUnavailable({ job }: { job: RunnerState }) {
  const { t } = useTranslation()
  const reason = job.sandbox_id
    ? t("user.consoleStateUnavailable")
    : t("user.consoleCleanedUp")
  return (
    <div className="flex min-h-[320px] items-center justify-center px-4 py-12">
      <div className="max-w-md text-center">
        <div className="mx-auto flex h-12 w-12 items-center justify-center rounded-md border border-white/10 bg-white/5 text-emerald-200">
          <SquareTerminal className="h-5 w-5" />
        </div>
        <h3 className="mt-4 text-sm font-semibold text-slate-100">{t("user.webConsoleUnavailable")}</h3>
        <p className="mt-2 text-sm leading-6 text-slate-400">
          {t("user.consoleAvailabilityDescription", { reason })}
        </p>
        <div className="mt-4 inline-flex items-center gap-2 rounded-md border border-white/10 bg-white/5 px-3 py-1.5 text-xs text-slate-300">
          <span className="text-slate-500">{t("user.status")}</span>
          <span className="font-medium text-slate-100">{runnerStatusLabel(job.status)}</span>
        </div>
      </div>
    </div>
  )
}

function LogOutput({
  text,
  description,
  actions,
  leading,
}: {
  text: string
  description: string
  actions?: ReactNode
  leading?: ReactNode
}) {
  const { t } = useTranslation()
  const logRef = useRef<HTMLDivElement | null>(null)
  const [collapseState, setCollapseState] = useState<{ text: string; groups: Set<number> }>(() => ({ text, groups: new Set() }))
  const collapsedGroups = useMemo(() => (collapseState.text === text ? collapseState.groups : new Set<number>()), [collapseState, text])
  const lines = useMemo(() => text.split(/\r?\n/), [text])
  const largeLog = lines.length > 20000
  const logLines = useMemo(() => (largeLog ? [] : parseLogLines(lines, collapsedGroups)), [lines, collapsedGroups, largeLog])
  const numberWidth = `${Math.max(2, String(lines.length).length)}ch`

  const scrollToBottom = () => {
    logRef.current?.scrollIntoView({ behavior: "smooth", block: "end" })
  }

  const toggleGroup = (groupID: number) => {
    setCollapseState((current) => {
      const next = new Set(current.text === text ? current.groups : [])
      if (next.has(groupID)) {
        next.delete(groupID)
      } else {
        next.add(groupID)
      }
      return { text, groups: next }
    })
  }

  return (
    <div className="overflow-hidden border-y border-emerald-500/15 bg-slate-950 text-slate-100 shadow-[inset_3px_0_0_theme(colors.emerald.500/0.35)]">
      <div className="sticky top-0 z-10 flex flex-wrap items-center justify-between gap-3 border-b border-white/10 bg-slate-900/95 px-4 py-3 backdrop-blur">
        <div className="flex min-w-0 flex-wrap items-center gap-3">
          {leading}
          <div className="min-w-0 text-xs text-slate-400">{description}</div>
        </div>
        <div className="flex items-center gap-2">
          <Button
            type="button"
            variant="outline"
            size="sm"
            className="border-white/15 bg-white/5 text-slate-100 hover:bg-white/10 hover:text-white"
            onClick={scrollToBottom}
          >
            {t("user.scrollToBottom")}
          </Button>
          {actions}
        </div>
      </div>
      <div ref={logRef} className="py-3 font-mono text-xs leading-relaxed">
        {largeLog ? (
          <pre className="max-h-[70vh] overflow-auto whitespace-pre-wrap break-words px-4 text-slate-200">{text}</pre>
        ) : logLines.map((logLine) => {
          const rowStyle = { "--line-number-width": numberWidth } as CSSProperties
          const rowClassName = "grid grid-cols-[12px_var(--line-number-width)_minmax(0,1fr)] gap-1 px-4"
          if (logLine.groupID !== undefined && logLine.kind === "group-start") {
            return (
              <button
                key={`${logLine.index}-${logLine.text.slice(0, 16)}`}
                type="button"
                className={cn(rowClassName, "group text-left")}
                style={rowStyle}
                onClick={() => toggleGroup(logLine.groupID ?? logLine.index)}
                aria-expanded={!collapsedGroups.has(logLine.groupID ?? logLine.index)}
              >
                <span className="flex h-[1.625em] select-none items-center justify-center text-slate-300 group-hover:text-emerald-200">
                  <Play
                    className={cn(
                      "h-3 w-3 max-w-none fill-current stroke-current",
                      !collapsedGroups.has(logLine.groupID ?? logLine.index) && "rotate-90"
                    )}
                  />
                </span>
                <span className="select-none text-right text-slate-500">{logLine.index + 1}</span>
                <span className={cn("min-w-0 whitespace-pre-wrap break-words text-left text-slate-200 group-hover:text-emerald-200", logLineClass(logLine.text))}>{logLine.displayText || " "}</span>
              </button>
            )}
          return (
            <div key={`${logLine.index}-${logLine.text.slice(0, 16)}`} className={rowClassName} style={rowStyle}>
              <span />
              <span className="select-none text-right text-slate-500">{logLine.index + 1}</span>
              <span className={cn("min-w-0 whitespace-pre-wrap break-words text-slate-200", logLineClass(logLine.text))}>{logLine.displayText || " "}</span>
            </div>
          )
        })}
      </div>
    </div>
  )
}

function logLineClass(line: string) {
  if (line.includes("##[group]") || line.includes("##[endgroup]")) return "font-semibold text-emerald-300"
  if (line.trimStart().startsWith("$ ")) return "font-semibold text-cyan-200"
  return ""
}

type ParsedLogLine = {
  index: number
  text: string
  displayText: string
  kind: "line" | "group-start" | "group-end"
  groupID?: number
}

function parseLogLines(lines: string[], collapsedGroups: Set<number>): ParsedLogLine[] {
  const visible: ParsedLogLine[] = []
  const stack: number[] = []

  lines.forEach((text, index) => {
    const hiddenByParent = stack.some((groupID) => collapsedGroups.has(groupID))

    if (text.includes("##[group]")) {
      if (!hiddenByParent) {
        visible.push({ index, text, displayText: text.replace("##[group]", ""), kind: "group-start", groupID: index })
      }
      stack.push(index)
      return
    }

    if (text.includes("##[endgroup]")) {
      stack.pop()
      return
    }

    if (!hiddenByParent) {
      visible.push({ index, text, displayText: text, kind: "line" })
    }
  })

  return visible
}

function workflowRunValue(job: RunnerState, t: AppTFunction) {
  const runID = job.workflow_run_id ? String(job.workflow_run_id) : t("common.unknown")
  const jobID = job.workflow_job_id ? String(job.workflow_job_id) : job.id
  const runURL = workflowRunURL(job)
  const runValue = runURL ? (
    <a className="text-primary hover:underline" href={runURL} target="_blank" rel="noreferrer">
      {runID}
    </a>
  ) : (
    runID
  )
  if (!job.github_job_url || !jobID) return runValue
  return (
    <span className="inline-flex items-center gap-1">
      {runValue}
      <span className="text-muted-foreground">/</span>
      <a className="inline-flex items-center gap-1 text-primary hover:underline" href={job.github_job_url} target="_blank" rel="noreferrer">
        {jobID}
        <ExternalLink className="h-3.5 w-3.5" />
      </a>
    </span>
  )
}

function workflowRunURL(job: RunnerState) {
  if (!job.github_job_url || !job.workflow_run_id) return ""
  const marker = `/actions/runs/${job.workflow_run_id}`
  const index = job.github_job_url.indexOf(marker)
  if (index < 0) return ""
  return job.github_job_url.slice(0, index + marker.length)
}

function pullRequestHeading(group: BuildGroup, jobGroup: RunnerJobGroup | null) {
  const title = jobGroup?.pull_request_title?.trim()
  const label = jobGroup?.title || group.title
  return title ? `${label}: ${title}` : label
}

function workflowGroups(jobs: RunnerState[], t: AppTFunction) {
  const groups = new Map<number | string, { id: number | string; name: string; jobs: RunnerState[] }>()
  for (const job of jobs) {
    const id = job.workflow_run_id || job.id
    const group = groups.get(id)
    if (group) {
      group.jobs.push(job)
      continue
    }
    groups.set(id, {
      id,
      name: job.workflow_name || t("user.workflowRun"),
      jobs: [job],
    })
  }
  return Array.from(groups.values())
}

function workflowStatus(jobs: RunnerState[]) {
  if (jobs.some((job) => job.status === "failed")) return "failed"
  if (jobs.some((job) => ["queued", "creating", "running", "stopping"].includes(job.status))) return "active"
  return "completed"
}

function BuildGroupListItem({
  group,
  selected,
  onSelect,
}: {
  group: BuildGroup
  selected: boolean
  onSelect: () => void
}) {
  const { t, i18n } = useTranslation()
  const status = buildGroupStatus(group)
  const statusClasses = buildGroupStatusClasses(status)
  const reference = buildGroupReference(group)

  return (
    <button
      type="button"
      onClick={onSelect}
      className={cn(
        "group relative flex w-full gap-2 bg-background/60 py-4 pl-3 pr-4 text-left transition-colors hover:bg-accent/70",
        selected ? "bg-accent" : ""
      )}
    >
      <span className={cn("absolute inset-y-0 left-0 w-1", statusClasses.bar)} aria-hidden="true" />
      <span className={cn("mt-1 flex h-5 w-5 shrink-0 items-center justify-center", statusClasses.icon)}>
        <BuildGroupStatusIcon status={status} />
      </span>
      <span className="min-w-0 flex-1">
        <span className="flex items-start justify-between gap-3">
          <span className="min-w-0 flex-1">
            <span className={cn("block truncate text-sm font-semibold leading-5", statusClasses.title)}>
              {group.repository}
            </span>
          </span>
          <span className="flex shrink-0 items-baseline gap-1 font-mono text-sm leading-5">
            <span className={statusClasses.title}>#</span>
            <span className={cn("text-sm font-semibold", statusClasses.title)}>{reference}</span>
          </span>
        </span>
        <span className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
          <span className="inline-flex items-center gap-1">
            <Workflow className="h-3.5 w-3.5" />
            {t("user.jobCount", { count: group.jobs.length })}
          </span>
          <span className="inline-flex items-center gap-1">
            <Play className="h-3.5 w-3.5" />
            {t("user.runCount", { count: group.workflowRunIDs.length || 1 })}
          </span>
          <span className="inline-flex items-center gap-1">
            <CalendarDays className="h-3.5 w-3.5" />
            {formatTime(group.updatedAt, i18n.resolvedLanguage)}
          </span>
        </span>
      </span>
    </button>
  )
}

function BuildGroupStatusIcon({ status }: { status: RunnerStatusSummary }) {
  const className = "h-4 w-4"
  if (status === "failed") return <X className={className} />
  if (status === "active") return <Loader2 className={cn(className, "animate-spin")} />
  return <Check className={className} />
}

function BuildGroupStatusBadge({ group, status }: { group: BuildGroup; status: RunnerStatusSummary }) {
  const { t } = useTranslation()
  if (status === "failed") {
    return (
      <Badge variant="danger" className="self-center">
        <X />
        {buildGroupStatusLabel(group, "failed", t)}
      </Badge>
    )
  }
  if (status === "active") {
    return (
      <Badge variant="warning" className="self-center">
        <Loader2 className="animate-spin" />
        {buildGroupStatusLabel(group, "running", t)}
      </Badge>
    )
  }
  return (
    <Badge variant="success" className="self-center">
      <Check />
      {buildGroupStatusLabel(group, "passed", t)}
    </Badge>
  )
}

function buildGroupStatusLabel(group: BuildGroup, statusText: "failed" | "running" | "passed", t: AppTFunction) {
  const statusKeys = {
    failed: "user.statusFailed",
    running: "user.statusRunning",
    passed: "user.statusPassed",
  } as const
  const status = t(statusKeys[statusText])
  switch (group.kind) {
    case "pull_request":
      return t("user.pullRequestStatus", { status })
    case "workflow_run":
      return t("user.workflowRunStatus", { status })
    case "branch":
      return t("user.branchStatus", { status })
    default:
      return t("user.jobStatus", { status })
  }
}

function buildGroupStatus(group: BuildGroup): RunnerStatusSummary {
  const jobs = currentBuildJobs(group)
  if (jobs.some((job) => job.status === "failed")) return "failed"
  if (jobs.some((job) => job.status === "queued" || job.status === "creating" || job.status === "running" || job.status === "stopping")) {
    return "active"
  }
  return "completed"
}

function jobStatusSummary(status: RunnerState["status"]): RunnerStatusSummary {
  if (status === "failed") return "failed"
  if (status === "queued" || status === "creating" || status === "running" || status === "stopping") return "active"
  return "completed"
}

function jobStatusTextClass(status: RunnerState["status"]) {
  return buildGroupStatusClasses(jobStatusSummary(status)).title
}

function jobStatusMark(status: RunnerState["status"]) {
  const className = "h-4 w-4"
  switch (status) {
    case "failed":
      return <X className={className} />
    case "queued":
    case "creating":
    case "running":
    case "stopping":
      return <Loader2 className={cn(className, "animate-spin")} />
    default:
      return <Check className={className} />
  }
}

function buildGroupStatusClasses(status: RunnerStatusSummary) {
  switch (status) {
    case "failed":
      return {
        bar: "bg-destructive",
        icon: "text-destructive",
        title: "text-destructive",
      }
    case "active":
      return {
        bar: "bg-yellow-500",
        icon: "text-yellow-700 dark:text-yellow-400",
        title: "text-yellow-700 dark:text-yellow-400",
      }
    default:
      return {
        bar: "bg-emerald-500",
        icon: "text-emerald-500",
        title: "text-emerald-500",
      }
  }
}

function buildGroupReference(group: BuildGroup) {
  if (group.pullRequestNumber) return String(group.pullRequestNumber)
  if (group.headSHA) return shortSHA(group.headSHA)
  if (group.workflowRunIDs[0]) return String(group.workflowRunIDs[0])
  return String(group.jobs.length)
}

function runnerJobTitle(job: RunnerState) {
  if (job.assigned_job_name && job.assigned_job_name !== "__runner_job_started__") {
    return job.assigned_job_name
  }
  return job.workflow_name || job.runner_name
}

function isTerminalAvailable(job: RunnerState) {
  return Boolean(job.sandbox_id && ["creating", "running", "stopping"].includes(job.status))
}

function groupRunnersByBuildContext(runners: RunnerState[], t: AppTFunction): BuildGroup[] {
  const visibleRunners = runners.filter(isUserVisibleRunnerJob)
  const prByRepositoryAndSHA = new Map<string, number>()
  for (const runner of runners) {
    if (runner.repository_full_name && runner.head_sha && runner.pull_request_number) {
      prByRepositoryAndSHA.set(`${runner.repository_full_name}:${runner.head_sha}`, runner.pull_request_number)
    }
  }

  const groups = new Map<string, BuildGroup>()
  for (const runner of visibleRunners) {
    const repository = runner.repository_full_name || "unknown/repository"
    const inferredPR = runner.pull_request_number || (runner.head_sha ? prByRepositoryAndSHA.get(`${repository}:${runner.head_sha}`) : undefined)
    const group = buildGroupSeed(runner, repository, inferredPR, t)
    const key = group.key
    const current = groups.get(key)
    if (current) {
      current.jobs.push(runner)
      if (runner.updated_at > current.updatedAt) {
        current.updatedAt = runner.updated_at
        current.subtitle = group.subtitle
        current.headSHA = runner.head_sha || current.headSHA
        current.headBranch = runner.head_branch || current.headBranch
      }
      if (runner.head_sha && !current.headSHA) current.headSHA = runner.head_sha
      if (runner.head_branch && !current.headBranch) current.headBranch = runner.head_branch
      if (runner.workflow_run_id && !current.workflowRunIDs.includes(runner.workflow_run_id)) {
        current.workflowRunIDs.push(runner.workflow_run_id)
        current.workflowRunIDs.sort((a, b) => b - a)
      }
      continue
    }
    groups.set(key, group)
  }
  return Array.from(groups.values())
    .map((group) => ({ ...group, jobs: orderJobs(group.jobs) }))
    .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt))
}

function isUserVisibleRunnerJob(job: RunnerState) {
  return !(job.failure_stage === "admission" && job.failure_reason === "profile_labels_not_matched")
}

function isRunnerJobGroup(value: unknown): value is RunnerJobGroup {
  if (!value || typeof value !== "object") return false
  const candidate = value as Partial<RunnerJobGroup>
  return Array.isArray(candidate.jobs) && Array.isArray(candidate.current_jobs) && Array.isArray(candidate.previous_jobs)
}

function buildGroupSeed(runner: RunnerState, repository: string, pullRequestNumber: number | undefined, t: AppTFunction): BuildGroup {
  const workflowRunIDs = runner.workflow_run_id ? [runner.workflow_run_id] : []
  if (pullRequestNumber) {
    return {
      key: `pr:${repository}:${pullRequestNumber}`,
      kind: "pull_request",
      repository,
      title: `PR #${pullRequestNumber}`,
      subtitle: runner.head_branch || shortSHA(runner.head_sha) || runner.workflow_name || t("user.pullRequestChecks"),
      updatedAt: runner.updated_at,
      jobs: [runner],
      workflowRunIDs,
      headSHA: runner.head_sha,
      headBranch: runner.head_branch,
      pullRequestNumber,
    }
  }
  if (runner.head_sha) {
    const branch = runner.head_branch || "detached"
    return {
      key: `branch:${repository}:${branch}:${runner.head_sha}`,
      kind: "branch",
      repository,
      title: runner.head_branch || t("user.commitTitle", { sha: shortSHA(runner.head_sha) }),
      subtitle: shortSHA(runner.head_sha) || runner.workflow_name || t("user.branchChecks"),
      updatedAt: runner.updated_at,
      jobs: [runner],
      workflowRunIDs,
      headSHA: runner.head_sha,
      headBranch: runner.head_branch,
    }
  }
  if (runner.workflow_run_id) {
    return {
      key: `run:${repository}:${runner.workflow_run_id}`,
      kind: "workflow_run",
      repository,
      title: runner.workflow_name || t("user.workflowRun"),
      subtitle: t("user.runTitle", { id: runner.workflow_run_id }),
      updatedAt: runner.updated_at,
      jobs: [runner],
      workflowRunIDs,
    }
  }
  return {
    key: `manual:${repository}:${runner.id}`,
    kind: "manual",
    repository,
    title: t("user.manualRunner"),
    subtitle: runner.runner_spec_name || runner.runner_name || runner.id,
    updatedAt: runner.updated_at,
    jobs: [runner],
    workflowRunIDs,
  }
}

function orderJobs(jobs: RunnerState[]) {
  return [...jobs].sort((a, b) => {
    const runOrder = (b.workflow_run_id || 0) - (a.workflow_run_id || 0)
    if (runOrder !== 0) return runOrder
    return b.updated_at.localeCompare(a.updated_at)
  })
}

function currentBuildJobs(group: BuildGroup) {
  if (group.headSHA) {
    const current = group.jobs.filter((job) => job.head_sha === group.headSHA)
    if (current.length > 0) return current
  }
  const latestRunID = group.workflowRunIDs[0]
  if (latestRunID) {
    const current = group.jobs.filter((job) => job.workflow_run_id === latestRunID)
    if (current.length > 0) return current
  }
  return group.jobs
}

function previousBuildJobs(group: BuildGroup, currentJobs: RunnerState[]) {
  const currentIDs = new Set(currentJobs.map((job) => job.id))
  return group.jobs.filter((job) => !currentIDs.has(job.id))
}

function shortSHA(value?: string) {
  if (!value) return ""
  return value.length > 7 ? value.slice(0, 7) : value
}

function orderInstallationsByCurrentAccount(
  installations: NonNullable<GitHubAppConfig["installations"]>,
  currentLogin?: string
) {
  const login = (currentLogin || "").toLowerCase()
  return [...installations].sort((a, b) => {
    const aLogin = a.account_login || ""
    const bLogin = b.account_login || ""
    const aIsCurrent = aLogin.toLowerCase() === login
    const bIsCurrent = bLogin.toLowerCase() === login
    if (aIsCurrent !== bIsCurrent) return aIsCurrent ? -1 : 1
    return aLogin.localeCompare(bLogin)
  })
}

function AccountAvatar({
  installation,
  size,
}: {
  installation: NonNullable<GitHubAppConfig["installations"]>[number]
  size: "sm" | "lg"
}) {
  const { t } = useTranslation()
  const className =
    size === "lg"
      ? "flex h-12 w-12 shrink-0 items-center justify-center overflow-hidden rounded-md bg-foreground text-sm font-semibold text-background"
      : "flex h-8 w-8 shrink-0 items-center justify-center overflow-hidden rounded-md bg-foreground text-xs font-semibold text-background"
  const label = accountInitials(installation)
  const avatarURL = accountAvatarURL(installation)
  if (avatarURL) {
    return (
      <img
        className={className}
        src={avatarURL}
        alt={t("common.namedAvatar", { name: installation.account_login || "GitHub" })}
      />
    )
  }
  return <div className={className}>{label}</div>
}

function accountDisplayName(installation: NonNullable<GitHubAppConfig["installations"]>[number]) {
  return installation.account_name || installation.account_login || "GitHub App"
}

function accountInitials(installation: NonNullable<GitHubAppConfig["installations"]>[number]) {
  return accountDisplayName(installation).slice(0, 2).toUpperCase()
}

function accountAvatarURL(installation: NonNullable<GitHubAppConfig["installations"]>[number]) {
  if (installation.account_avatar) return installation.account_avatar
  if (!installation.account_login) return ""
  return `https://github.com/${encodeURIComponent(installation.account_login)}.png?size=96`
}
