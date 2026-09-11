import { lazy, Suspense, type ReactNode, useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { toast } from "sonner"

import { AppSidebar } from "@/components/app-sidebar"
import { AccountsSection } from "@/components/accounts-section"
import { AuditSection, DiagnosticsSection, MatchSection, OverviewSection, RunnerRequestSection } from "@/components/admin-sections"
import { AccessDeniedPage, NotFoundPage, SessionErrorPage, SessionLoadingPage, SignInPage } from "@/components/auth-pages"
import { LandingPage } from "@/components/landing-page"
import { RunnerJobDetail } from "@/components/runner-job-detail"
import { RunnerRequestsSection } from "@/components/runner-requests-section"
import { RunnerSpecsSection } from "@/components/runner-specs-section"
import { SandboxServiceDefaultSection } from "@/components/sandbox-service-default-section"
import { SandboxRegionsContext, fetchSandboxRegions, type SandboxRegion } from "@/components/sandbox-catalog-utils"
import { SiteHeader } from "@/components/site-header"
import { UserDashboard } from "@/components/user-dashboard"
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { SidebarInset, SidebarProvider } from "@/components/ui/sidebar"
import { Toaster } from "@/components/ui/sonner"
import {
  adminSections,
  runnerRequestIdentifierFromAdminPath,
  sectionFromPath,
  type AdminSection,
  type AuditEvent,
  type AuthSession,
  type AuthorizedRepositories,
  type DiagnosticsSummary,
  type GitHubAppConfig,
  type GitHubInstallation,
  type ProductTourOnboarding,
  type RunnerSpec,
  type RunnerSpecMatch,
  type RunnerDisplayStatus,
  type RunnerState,
  type SyncedGitHubInstallations,
  type UserPreferences,
} from "@/admin-types"
import {
  adminDataResources,
  adminPollingResources,
  appRouteAccess,
  authRouteViewState,
  createLatestUserLoadGate,
  loadOptionalUserResource,
  shouldPollAdminSection,
  shouldPollUserRoute,
  userDataResources,
  userGitHubAppPath,
  userPollingResources,
  userRunnerHistoryWindow,
  userRunnerRequestLimit,
  userRunnerRequestsPath,
  type AdminDataResource,
  type AuthSessionCheckStatus,
} from "@/app-load-policy"
import { useRunnerCatalog } from "@/hooks/use-runner-catalog"
import { runnerDisplayStatus, runnerMetrics } from "@/admin-format"
import appI18n from "@/i18n"
import {
  repositoryAccountLogin,
  repositoryPath,
  repositoryPreferenceScope,
  selectRepositoryInstallation,
  settingsPreferenceInstallationID,
} from "@/repository-readiness"
import { productTourVersion, shouldCompleteProductTour } from "@/user-onboarding"
import {
  createGitHubReauthenticationGate,
  requiresGitHubReauthentication,
  type RequestError,
} from "@/user-auth-errors"

const DocsPage = lazy(() =>
  import("@/components/docs-page").then(({ DocsPage: Component }) => ({ default: Component }))
)

type AccountSettingsTab = "repositories" | "preferences" | "sandbox-templates" | "sandbox-instances" | "runner-specs"
type AccountSettingsRoute = {
  accountLogin?: string
  tab: AccountSettingsTab
}

type UserRunnerPage = {
  items: RunnerState[]
  total: number
}

const adminResourcePaths: Record<AdminDataResource, string> = {
  runner_requests: "/runner_requests?limit=100&offset=0",
  runner_specs: "/runner_specs",
  audit_events: "/audit-events",
}

function adminResourcePath(resource: AdminDataResource): string {
  return adminResourcePaths[resource]
}

function updateAdminResource(
  resource: AdminDataResource,
  data: unknown,
  setters: {
    setRunners: (value: RunnerState[]) => void
    setRunnerSpecs: (value: RunnerSpec[]) => void
    setAuditEvents: (value: AuditEvent[]) => void
  }
) {
  const items = Array.isArray(data) ? data : []
  switch (resource) {
    case "runner_requests":
      setters.setRunners(items as RunnerState[])
      break
    case "runner_specs":
      setters.setRunnerSpecs(items as RunnerSpec[])
      break
    case "audit_events":
      setters.setAuditEvents(items as AuditEvent[])
      break
  }
}

function App() {
  const { t } = useTranslation()
  const [authSession, setAuthSession] = useState<AuthSession>({ authenticated: false, oauth_enabled: false })
  const [authSessionStatus, setAuthSessionStatus] = useState<AuthSessionCheckStatus>("checking")
  const [locationPath, setLocationPath] = useState(() => window.location.pathname)
  const [locationSearch, setLocationSearch] = useState(() => window.location.search)
  const [section, setSectionState] = useState<AdminSection>(() => sectionFromPath())
  const [runners, setRunners] = useState<RunnerState[]>([])
  const [runnerSpecs, setRunnerSpecs] = useState<RunnerSpec[]>([])
  const [loading, setLoading] = useState(false)
  const [createID, setCreateID] = useState("")
  const [createRepository, setCreateRepository] = useState("")
  const [createRunnerSpec, setCreateRunnerSpec] = useState("")
  const [createLabels, setCreateLabels] = useState("self-hosted,e2b")
  const [createRunnerOpen, setCreateRunnerOpen] = useState(false)
  const [runnerStatusFilter, setRunnerStatusFilter] = useState<RunnerDisplayStatus | "all">("all")
  const [runnerRepositoryFilter, setRunnerRepositoryFilter] = useState("all")
  const [runnerSpecFilter, setRunnerSpecFilter] = useState("all")
  const [matchRepository, setMatchRepository] = useState("")
  const [matchLabels, setMatchLabels] = useState("self-hosted,e2b")
  const [matchResult, setMatchResult] = useState<RunnerSpecMatch | null>(null)
  const [diagnostics, setDiagnostics] = useState<DiagnosticsSummary | null>(null)
  const [auditEvents, setAuditEvents] = useState<AuditEvent[]>([])
  const [userRunners, setUserRunners] = useState<RunnerState[]>([])
  const [userRunnerTotal, setUserRunnerTotal] = useState(0)
  const [loadingUserRunnerHistory, setLoadingUserRunnerHistory] = useState(false)
  const [githubApp, setGitHubApp] = useState<GitHubAppConfig | null>(null)
  const [userPreferences, setUserPreferences] = useState<UserPreferences | null>(null)
  const [userPreferencesScope, setUserPreferencesScope] = useState("")
  const [productTourOnboarding, setProductTourOnboarding] = useState<ProductTourOnboarding | null>(null)
  const [authorizedRepositories, setAuthorizedRepositories] = useState<Record<number, string[]>>({})
  const [repositoryErrors, setRepositoryErrors] = useState<Record<number, string>>({})
  const [loadingRepositoriesFor, setLoadingRepositoriesFor] = useState<number | null>(null)
  const [syncingGitHubInstallations, setSyncingGitHubInstallations] = useState(false)
  const [userSelectedKey, setUserSelectedKey] = useState(() => userJobsGroupKeyFromLocation(window.location.pathname, window.location.search))
  const [beginGitHubReauthentication] = useState(createGitHubReauthenticationGate)
  const userLoadGate = useRef(createLatestUserLoadGate()).current
  const runnerRequestLookupGeneration = useRef(0)
  const [sandboxRegions, setSandboxRegions] = useState<SandboxRegion[]>([])
  const runnerRequestIdentifier = runnerRequestIdentifierFromAdminPath(locationPath)

  useEffect(() => {
    void fetchSandboxRegions().then((regions) => {
      if (regions && regions.length > 0) setSandboxRegions(regions)
    })
  }, [])

  const setSection = useCallback((next: string) => {
    const section = adminSections.includes(next as AdminSection) ? (next as AdminSection) : "overview"
    setSectionState(section)
    const nextPath = section === "overview" ? "/admin/" : `/admin/${section}`
    if (window.location.pathname !== nextPath) {
      window.history.pushState(null, "", nextPath)
      setLocationPath(nextPath)
      setLocationSearch("")
    }
  }, [])

  const openRunnerRequest = useCallback((identifier: string) => {
    runnerRequestLookupGeneration.current += 1
    const nextPath = `/admin/runner_requests/${encodeURIComponent(identifier)}`
    if (window.location.pathname + window.location.search !== nextPath) {
      window.history.pushState(null, "", nextPath)
    }
    setSectionState("runner_requests")
    setLocationPath(window.location.pathname)
    setLocationSearch(window.location.search)
  }, [])

  const canonicalizeRunnerRequestID = useCallback((id: string) => {
    const nextPath = `/admin/runner_requests/${encodeURIComponent(id)}`
    if (window.location.pathname + window.location.search === nextPath) return
    window.history.replaceState(null, "", nextPath)
  }, [])

  const setUserPage = useCallback((next: "home" | "repositories" | "runner-specs" | "settings") => {
    const nextPath = next === "settings" ? "/account/preferences" : next === "repositories" ? "/repositories" : next === "runner-specs" ? "/runner-specs" : userJobsPath(userSelectedKey)
    if (window.location.pathname + window.location.search !== nextPath) {
      window.history.pushState(null, "", nextPath)
    }
    setLocationPath(window.location.pathname)
    setLocationSearch(window.location.search)
  }, [userSelectedKey])

  const setRepositoryAccount = useCallback(
    (accountLogin: string | undefined) => {
      const nextPath = repositoryPath(accountLogin, authSession.login)
      if (window.location.pathname + window.location.search !== nextPath) {
        window.history.pushState(null, "", nextPath)
      }
      setLocationPath(window.location.pathname)
      setLocationSearch(window.location.search)
    },
    [authSession.login],
  )

  const openProductTourStart = useCallback(() => {
    setUserSelectedKey("")
    if (window.location.pathname !== "/jobs" || window.location.search) {
      window.history.pushState(null, "", "/jobs")
    }
    setLocationPath("/jobs")
    setLocationSearch("")
  }, [])

  const setAccountSettingsRoute = useCallback(
    (accountLogin: string | undefined, tab: AccountSettingsTab) => {
      const nextPath = accountSettingsPath(accountLogin, authSession.login, tab)
      if (window.location.pathname !== nextPath) {
        window.history.pushState(null, "", nextPath)
      }
      setLocationPath(window.location.pathname)
      setLocationSearch(window.location.search)
    },
    [authSession.login]
  )

  const setUserJobsSelection = useCallback((key: string) => {
    setUserSelectedKey(key)
    const nextPath = userJobsPath(key)
    if (window.location.pathname + window.location.search !== nextPath) {
      window.history.pushState(null, "", nextPath)
    }
    setLocationPath(window.location.pathname)
    setLocationSearch(window.location.search)
  }, [])

  const runnerRepositories = useMemo(
    () =>
      Array.from(new Set(runners.map((runner) => runner.repository_full_name).filter(Boolean) as string[])).sort(),
    [runners]
  )

  const runnerSpecNames = useMemo(
    () =>
      Array.from(
        new Set(
          [
            ...runnerSpecs.map((runnerSpec) => runnerSpec.name),
            ...runners.map((runner) => runner.runner_spec_name || ""),
          ].filter(Boolean)
        )
      ).sort(),
    [runnerSpecs, runners]
  )

  const filteredRunners = useMemo(
    () =>
      runners.filter((runner) => {
        if (runnerStatusFilter !== "all" && runnerDisplayStatus(runner) !== runnerStatusFilter) return false
        if (runnerRepositoryFilter !== "all" && runner.repository_full_name !== runnerRepositoryFilter) return false
        if (runnerSpecFilter !== "all" && runner.runner_spec_name !== runnerSpecFilter) return false
        return true
      }),
    [runnerRepositoryFilter, runnerSpecFilter, runnerStatusFilter, runners]
  )

  const hasAccess = authSession.authenticated && authSession.role === "admin"
  const routeAccess = appRouteAccess(locationPath)
  const isAdminRoute = routeAccess === "admin"
  const userJobID = userJobIDFromPath(locationPath)
  const userSelectedJobID = userJobIDFromSearch(locationSearch)
  const accountSettingsRoute = parseAccountSettingsRoute(locationPath, authSession.login)
  const accountSettingsLogin = accountSettingsRoute?.accountLogin
  const accountSettingsTab = accountSettingsRoute?.tab
  const selectedRepositoryAccountLogin = repositoryAccountLogin(locationPath, authSession.login)
  const selectedRepositoryInstallation = selectRepositoryInstallation(
    githubApp?.installations ?? [],
    selectedRepositoryAccountLogin,
    authSession.login,
  )
  const selectedRepositoryPreferenceScope = repositoryPreferenceScope(
    selectedRepositoryInstallation,
    authSession.login,
  )
  const userPage = locationPath === "/runner-specs"
    ? "runner-specs"
    : selectedRepositoryAccountLogin
      ? "repositories"
      : accountSettingsRoute
        ? "settings"
        : "home"

  const metrics = useMemo(
    () => runnerMetrics(runners, runnerSpecs.length, t),
    [runnerSpecs.length, runners, t],
  )

  const requestResponse = useCallback(
    async (url: string, options: RequestInit = {}) => {
      const headers = new Headers(options.headers)
      const response = await fetch(url, { ...options, headers, credentials: "same-origin" })
      if (response.status === 401) {
        try {
          const sessionResponse = await fetch("/auth/session", { credentials: "same-origin" })
          if (sessionResponse.ok) {
            setAuthSession((await sessionResponse.json()) as AuthSession)
          }
        } catch {
          setAuthSession((current) => ({ ...current, authenticated: false, login: undefined, role: undefined, avatar_url: undefined, expires_at: undefined }))
        }
        throw new Error("Session expired or access is not allowed")
      }
      if (!response.ok) {
        const text = await response.text()
        let message = text
        let code = ""
        try {
          const parsed = JSON.parse(text) as { code?: string; error?: string }
          code = parsed.code || ""
          message = parsed.error || text
        } catch {
          // Keep the raw response body for non-JSON errors.
        }
        const error = new Error(message || `${response.status} ${response.statusText}`) as RequestError
        error.code = code
        throw error
      }
      return response
    },
    []
  )

  const request = useCallback(
    async (url: string, options: RequestInit = {}) => {
      const response = await requestResponse(url, options)
      const contentType = response.headers.get("content-type") || ""
      if (contentType.includes("application/json")) return response.json()
      return response.text()
    },
    [requestResponse]
  )

  const lookupRunnerRequest = useCallback(async (identifier: string) => {
    const generation = ++runnerRequestLookupGeneration.current
    try {
      const runner = await request(
        `/runner_requests_lookup/${encodeURIComponent(identifier.trim())}`
      ) as RunnerState
      if (generation !== runnerRequestLookupGeneration.current) return
      openRunnerRequest(runner.id)
    } catch (error) {
      if (generation === runnerRequestLookupGeneration.current) {
        toast.error(error instanceof Error ? error.message : appI18n.t("admin.runnerDiagnosisFailed"))
      }
    }
  }, [openRunnerRequest, request])

  const requestUserRunnerPage = useCallback(
    async (limit: number, offset: number): Promise<UserRunnerPage> => {
      const response = await requestResponse(userRunnerRequestsPath(limit, offset))
      const data = await response.json()
      const items = Array.isArray(data) ? (data as RunnerState[]) : []
      const totalHeader = response.headers.get("X-Total-Count")
      const parsedTotal = totalHeader === null ? Number.NaN : Number(totalHeader)
      return {
        items,
        total: Number.isSafeInteger(parsedTotal) && parsedTotal >= 0 ? parsedTotal : items.length,
      }
    },
    [requestResponse]
  )

  const parseLabels = (value: string) =>
    value
      .split(",")
      .map((label) => label.trim())
      .filter(Boolean)

  const refreshGitHubOAuthLogin = useCallback(() => {
    const returnTo = window.location.pathname + window.location.search
    window.location.href = `/auth/github/login?return_to=${encodeURIComponent(returnTo || "/")}`
  }, [])

  const loadAll = useCallback(async (polling = false) => {
    if (!hasAccess || !isAdminRoute || runnerRequestIdentifier) return
    const resources = polling ? adminPollingResources(section) : adminDataResources(section)
    if (resources.length === 0) return
    setLoading(true)
    try {
      const entries = await Promise.all(
        resources.map(async (resource) => [resource, await request(adminResourcePath(resource))] as const)
      )
      for (const [resource, data] of entries) {
        updateAdminResource(resource, data, {
          setRunners,
          setRunnerSpecs,
          setAuditEvents,
        })
      }
    } catch (error) {
      toast.error(error instanceof Error ? error.message : appI18n.t("app.controlPlaneLoadFailed"))
    } finally {
      setLoading(false)
    }
  }, [hasAccess, isAdminRoute, request, runnerRequestIdentifier, section])

  const loadUserAll = useCallback(async (polling = false) => {
    const loadID = userLoadGate.begin(`${authSession.login ?? ""}:${locationPath}`)
    if (!authSession.authenticated || (hasAccess && isAdminRoute)) return
    const resources = polling ? userPollingResources(locationPath) : userDataResources(locationPath)
    if (resources.length === 0) {
      setLoading(false)
      return
    }
    setLoading(true)
    try {
      const [appData, runnerPage, onboardingData] = await Promise.all([
        resources.includes("github_app")
          ? request(userGitHubAppPath(locationPath))
          : Promise.resolve(null),
        resources.includes("runner_requests")
          ? requestUserRunnerPage(userRunnerRequestLimit(locationPath, polling), 0)
          : Promise.resolve(null),
        resources.includes("onboarding")
          ? loadOptionalUserResource(request("/user/onboarding/product-tour"))
          : Promise.resolve(null),
      ])
      const nextApp = appData as GitHubAppConfig | null
      let nextPreferences: UserPreferences | null = null
      let nextPreferencesScope = ""
      if (resources.includes("preferences") && nextApp) {
        const nextRoute = parseAccountSettingsRoute(locationPath, authSession.login)
        const repositoryLogin = repositoryAccountLogin(locationPath, authSession.login)
        const repositoryInstallation = repositoryLogin
          ? selectRepositoryInstallation(
              nextApp.installations,
              repositoryLogin,
              authSession.login,
            )
          : undefined
        const installationID = repositoryLogin
          ? repositoryInstallation?.account_login?.toLowerCase() ===
            authSession.login?.toLowerCase()
            ? undefined
            : repositoryInstallation?.id
          : settingsPreferenceInstallationID(
              nextApp.installations,
              nextRoute?.accountLogin,
              authSession.login,
            )
        const preferencesPath = userPreferencesPath(installationID)
        const preferencesData = await request(preferencesPath)
        nextPreferences = preferencesData as UserPreferences
        nextPreferencesScope = installationID ? `github_installation:${installationID}` : "account"
      }
      if (!userLoadGate.isCurrent(loadID)) return
      if (nextApp) setGitHubApp(nextApp)
      if (runnerPage) {
        setUserRunnerTotal(runnerPage.total)
        setUserRunners((current) => polling ? mergeUserRunnerPages(runnerPage.items, current) : runnerPage.items)
        if (runnerPage.total === 0) setUserSelectedKey("")
      }
      if (onboardingData) setProductTourOnboarding(onboardingData as ProductTourOnboarding)
      if (nextPreferences) setUserPreferences(nextPreferences)
      if (nextPreferencesScope) setUserPreferencesScope(nextPreferencesScope)
    } catch (error) {
      if (!userLoadGate.isCurrent(loadID)) return
      if (requiresGitHubReauthentication(error)) {
        if (beginGitHubReauthentication()) {
          toast.message(appI18n.t("app.refreshingGitHubSignIn"))
          refreshGitHubOAuthLogin()
        }
        return
      }
      toast.error(error instanceof Error ? error.message : appI18n.t("app.workspaceLoadFailed"))
    } finally {
      if (userLoadGate.isCurrent(loadID)) setLoading(false)
    }
  }, [authSession.authenticated, authSession.login, beginGitHubReauthentication, hasAccess, isAdminRoute, locationPath, refreshGitHubOAuthLogin, request, requestUserRunnerPage, userLoadGate])

  const saveProductTourOnboarding = useCallback(async (state: ProductTourOnboarding) => {
    const saved = (await request("/user/onboarding/product-tour", {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(state),
    })) as ProductTourOnboarding
    setProductTourOnboarding(saved)
  }, [request])

  const loadUserRunnerHistory = useCallback(async () => {
    if (!authSession.authenticated || loadingUserRunnerHistory) return
    setLoadingUserRunnerHistory(true)
    try {
      const page = await requestUserRunnerPage(userRunnerHistoryWindow, 0)
      setUserRunnerTotal(page.total)
      setUserRunners(page.items)
    } catch (error) {
      if (requiresGitHubReauthentication(error)) {
        if (beginGitHubReauthentication()) {
          toast.message(appI18n.t("app.refreshingGitHubSignIn"))
          refreshGitHubOAuthLogin()
        }
        return
      }
      toast.error(error instanceof Error ? error.message : appI18n.t("app.olderJobsLoadFailed"))
    } finally {
      setLoadingUserRunnerHistory(false)
    }
  }, [authSession.authenticated, beginGitHubReauthentication, loadingUserRunnerHistory, refreshGitHubOAuthLogin, requestUserRunnerPage])

  const syncGitHubAppSetupFromURL = useCallback(async () => {
    if (
      !authSession.authenticated ||
      (hasAccess && isAdminRoute) ||
      (!isAccountSettingsPath(locationPath) && locationPath !== "/repositories")
    ) return
    const params = new URLSearchParams(window.location.search)
    const installationID = Number(params.get("installation_id") || "")
    if (!Number.isSafeInteger(installationID) || installationID <= 0) return
    const setupState = params.get("state") || ""
    setLoading(true)
    try {
      const installation = (await request("/user/github-app/installations", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ installation_id: installationID, setup_state: setupState }),
      })) as GitHubInstallation
      toast.success(appI18n.t("app.accountSynced"))
      const nextPath = repositoryPath(installation.account_login, authSession.login)
      window.history.replaceState(null, "", nextPath)
      setLocationPath(window.location.pathname)
      setLocationSearch(window.location.search)
      await loadUserAll()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : appI18n.t("app.accountSyncFailed"))
    } finally {
      setLoading(false)
    }
  }, [authSession.authenticated, authSession.login, hasAccess, isAdminRoute, loadUserAll, locationPath, request])

  const loadAuthorizedRepositories = useCallback(async (id: number) => {
    setRepositoryErrors((current) => {
      if (!(id in current)) return current
      const next = { ...current }
      delete next[id]
      return next
    })
    setLoadingRepositoriesFor(id)
    try {
      const data = (await request(
        `/user/github-app/installations/${encodeURIComponent(String(id))}/repositories`
      )) as AuthorizedRepositories
      setAuthorizedRepositories((current) => ({ ...current, [id]: data.repositories || [] }))
    } catch (error) {
      const message = error instanceof Error ? error.message : appI18n.t("app.repositoriesLoadFailed")
      setRepositoryErrors((current) => ({ ...current, [id]: message }))
      toast.error(message)
    } finally {
      setLoadingRepositoriesFor(null)
    }
  }, [request])

  const syncGitHubInstallations = useCallback(async () => {
    if (syncingGitHubInstallations) return
    setLoading(true)
    setSyncingGitHubInstallations(true)
    try {
      const data = (await request("/user/github-app/installations/sync", { method: "POST" })) as SyncedGitHubInstallations
      const count = data.installations?.length ?? 0
      toast.success(appI18n.t("app.syncedGitHubAccounts", { count }))
      await loadUserAll()
    } catch (error) {
      const requestError = error as RequestError
      const message = error instanceof Error ? error.message : appI18n.t("app.accountsSyncFailed")
      if (requiresGitHubReauthentication(requestError) || message === "sign in with GitHub again before syncing installations") {
        if (beginGitHubReauthentication()) {
          toast.message(appI18n.t("app.refreshingGitHubSignIn"))
          refreshGitHubOAuthLogin()
        }
        return
      }
      toast.error(message)
    } finally {
      setSyncingGitHubInstallations(false)
      setLoading(false)
    }
  }, [beginGitHubReauthentication, loadUserAll, refreshGitHubOAuthLogin, request, syncingGitHubInstallations])

  const saveSandboxConfig = useCallback(async (
    apiURL: string,
    apiKey: string,
    installationID?: number,
    mode: "custom" | "inherit" = "custom",
    replaceInheritedSource = false,
  ) => {
    const preferences = (await request(userPreferencesPath(installationID, "/user/preferences/sandbox"), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ mode, api_url: apiURL, api_key: apiKey, replace_inherited_source: replaceInheritedSource }),
    })) as UserPreferences
    setUserPreferences(preferences)
    setUserPreferencesScope(installationID ? `github_installation:${installationID}` : "account")
    toast.success(appI18n.t("app.sandboxSaved"))
  }, [request])

  const saveCacheConfig = useCallback(async (input: { bucket: string; prefix: string; access_key_id: string; secret_access_key: string }, installationID?: number) => {
    const preferences = (await request(userPreferencesPath(installationID, "/user/preferences/cache"), {
      method: "PUT",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(input),
    })) as UserPreferences
    setUserPreferences(preferences)
    setUserPreferencesScope(installationID ? `github_installation:${installationID}` : "account")
    toast.success(t("user.cacheS3SettingsSaved"))
  }, [request])

  const deleteCacheConfig = useCallback(async (installationID?: number) => {
    const preferences = (await request(userPreferencesPath(installationID, "/user/preferences/cache"), { method: "DELETE" })) as UserPreferences
    setUserPreferences(preferences)
    setUserPreferencesScope(installationID ? `github_installation:${installationID}` : "account")
    toast.success(t("user.cacheS3SettingsRemoved"))
  }, [request])

  const deleteSandboxAPIKey = useCallback(async (installationID?: number) => {
    const preferences = (await request(userPreferencesPath(installationID, "/user/preferences/sandbox-api-key"), {
      method: "DELETE",
    })) as UserPreferences
    setUserPreferences(preferences)
    setUserPreferencesScope(installationID ? `github_installation:${installationID}` : "account")
    toast.success(appI18n.t("app.apiKeyRemoved"))
  }, [request])

  const {
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
  } = useRunnerCatalog({
    request,
    loadAll,
    setSection,
    parseLabels,
  })

  const loadAuthSession = useCallback(async () => {
    setAuthSessionStatus("checking")
    try {
      const response = await fetch("/auth/session", { credentials: "same-origin" })
      if (!response.ok) throw new Error(`session check failed with status ${response.status}`)
      setAuthSession((await response.json()) as AuthSession)
      setAuthSessionStatus("ready")
    } catch {
      setAuthSessionStatus("error")
    }
  }, [])

  useEffect(() => {
    void loadAuthSession()
  }, [loadAuthSession])

  useEffect(() => {
    const handlePopState = () => {
      setLocationPath(window.location.pathname)
      setLocationSearch(window.location.search)
      setUserSelectedKey(userJobsGroupKeyFromLocation(window.location.pathname, window.location.search))
      setSectionState(sectionFromPath())
    }
    window.addEventListener("popstate", handlePopState)
    return () => window.removeEventListener("popstate", handlePopState)
  }, [])

  useEffect(() => {
    if (locationPath !== "/accounts" && locationPath !== "/settings") return
    const nextPath = `/account/preferences${window.location.search}`
    window.history.replaceState(null, "", nextPath)
    setLocationPath("/account/preferences")
    setLocationSearch(window.location.search)
  }, [locationPath])

  useEffect(() => {
    if (!hasAccess || locationPath !== "/admin/diagnostics") return
    const identifier = diagnosticRunnerFromSearch(locationSearch)
    if (!identifier) return
    let cancelled = false
    void request(`/runner_requests_lookup/${encodeURIComponent(identifier)}`)
      .then((value) => {
        if (cancelled) return
        const runner = value as RunnerState
        const nextPath = `/admin/runner_requests/${encodeURIComponent(runner.id)}`
        window.history.replaceState(null, "", nextPath)
        setSectionState("runner_requests")
        setLocationPath(window.location.pathname)
        setLocationSearch(window.location.search)
      })
      .catch((error) => {
        if (!cancelled) {
          toast.error(error instanceof Error ? error.message : appI18n.t("admin.runnerDiagnosisFailed"))
        }
      })
    return () => {
      cancelled = true
    }
  }, [hasAccess, locationPath, locationSearch, request])

  useEffect(() => {
    if (locationPath !== "/account/sandbox" && !/^\/organizations\/[^/]+\/sandbox$/.test(locationPath)) return
    const nextPath = locationPath.replace(/\/sandbox$/, "/sandbox-templates")
    window.history.replaceState(null, "", nextPath)
    setLocationPath(nextPath)
  }, [locationPath])

  useEffect(() => {
    if (!isUserJobsRoute(locationPath)) return
    const key = userJobsGroupKeyFromLocation(locationPath, locationSearch)
    setUserSelectedKey(key)
    const canonicalPath = userJobsPath(key)
    const currentLocation = `${locationPath}${locationSearch}`
    const nextPath = withPreservedJobSearch(canonicalPath, locationSearch)
    if (key && currentLocation !== nextPath) {
      window.history.replaceState(null, "", nextPath)
      setLocationPath(window.location.pathname)
      setLocationSearch(window.location.search)
    }
  }, [locationPath, locationSearch])

  useEffect(() => {
    if (!hasAccess || !isAdminRoute) return
    void loadAll()
    if (runnerRequestIdentifier || !shouldPollAdminSection(section)) return
    const timer = window.setInterval(() => void loadAll(true), 5000)
    return () => window.clearInterval(timer)
  }, [hasAccess, isAdminRoute, loadAll, runnerRequestIdentifier, section])

  useEffect(() => {
    if (!authSession.authenticated || (hasAccess && isAdminRoute)) return
    void loadUserAll()
    if (!shouldPollUserRoute(locationPath)) return
    const timer = window.setInterval(() => void loadUserAll(true), 5000)
    return () => window.clearInterval(timer)
  }, [authSession.authenticated, hasAccess, isAdminRoute, loadUserAll, locationPath])

  useEffect(() => {
    const showsRepositoryReadiness = Boolean(
      selectedRepositoryAccountLogin &&
      userPreferencesScope === selectedRepositoryPreferenceScope,
    )
    const showsCurrentAccountSettings =
      accountSettingsTab === "preferences" &&
      accountSettingsLogin === authSession.login &&
      userPreferencesScope === "account"
    if (
      (!showsRepositoryReadiness && !showsCurrentAccountSettings) ||
      !shouldCompleteProductTour(productTourOnboarding, userPreferences)
    ) {
      return
    }
    void saveProductTourOnboarding({
      version: productTourVersion,
      status: "completed",
      tour_seen: true,
    }).catch((error) => {
      toast.error(
        error instanceof Error
          ? appI18n.t("app.tourProgressUpdateFailedWithReason", { reason: error.message })
          : appI18n.t("app.tourProgressUpdateFailed"),
      )
    })
  }, [
    accountSettingsLogin,
    accountSettingsTab,
    authSession.login,
    productTourOnboarding,
    saveProductTourOnboarding,
    selectedRepositoryAccountLogin,
    selectedRepositoryPreferenceScope,
    userPreferences,
    userPreferencesScope,
  ])

  useEffect(() => {
    void syncGitHubAppSetupFromURL()
  }, [syncGitHubAppSetupFromURL])

  useEffect(() => {
    if (section !== "diagnostics" || !hasAccess || diagnosticRunnerFromSearch(locationSearch)) return
    void (async () => {
      try {
        const summary = await request("/diagnostics/pprof")
        setDiagnostics(summary as DiagnosticsSummary)
      } catch (error) {
        toast.error(error instanceof Error ? error.message : appI18n.t("app.diagnosticsLoadFailed"))
      }
    })()
  }, [hasAccess, locationSearch, request, section])

  const signOut = () => {
    void fetch("/auth/logout", { method: "POST", credentials: "same-origin" }).finally(() => {
      setAuthSession((current) => ({ ...current, authenticated: false, login: undefined, role: undefined, avatar_url: undefined, expires_at: undefined }))
    })
    setRunners([])
    setRunnerSpecs([])
    setAuditEvents([])
    setUserRunners([])
    setUserRunnerTotal(0)
    setGitHubApp(null)
    setAuthorizedRepositories({})
    setRepositoryErrors({})
    setLoadingRepositoriesFor(null)
    setUserSelectedKey("")
  }

  const resetCreateRunnerForm = () => {
    setCreateID("")
    setCreateRepository("")
    setCreateRunnerSpec("")
    setCreateLabels("self-hosted,e2b")
  }

  const createRunner = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    if (!hasAccess) {
      toast.error(t("app.adminRequired"))
      return
    }
    const body: {
      id?: string
      repository_full_name?: string
      runner_spec_name?: string
      labels?: string[]
    } = {}
    const repository = createRepository.trim()
    if (!repository || repository.includes("*")) {
      toast.error(t("app.repositoryFormatRequired"))
      return
    }
    if (createID.trim()) body.id = createID.trim()
    body.repository_full_name = repository
    if (createRunnerSpec.trim()) body.runner_spec_name = createRunnerSpec.trim()
    const labels = parseLabels(createLabels)
    if (labels.length > 0) body.labels = labels
    try {
      const runner = (await request("/runner_requests", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      })) as RunnerState
      resetCreateRunnerForm()
      setCreateRunnerOpen(false)
      toast.success(t("app.runnerQueued", { id: runner.id }))
      await loadAll()
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("app.createRunnerFailed"))
    }
  }

  const stopRunner = async (id: string) => {
    try {
      const runner = (await request(`/runner_requests/${encodeURIComponent(id)}`, {
        method: "DELETE",
      })) as RunnerState
      toast.success(t("app.runnerCompleted", { id: runner.id }))
      await loadAll()
      return true
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("app.stopRunnerFailed"))
      return false
    }
  }

  const retryRunner = async (id: string) => {
    try {
      const runner = (await request(`/runner_requests/${encodeURIComponent(id)}/retry`, {
        method: "POST",
      })) as RunnerState
      toast.success(t("app.runnerRequeued", { id: runner.id }))
      await loadAll()
      return true
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("app.retryRunnerFailed"))
      return false
    }
  }

  const runMatchTest = async (event: React.FormEvent<HTMLFormElement>) => {
    event.preventDefault()
    try {
      const result = (await request("/runner_specs/match", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          repository_full_name: matchRepository.trim(),
          labels: parseLabels(matchLabels),
        }),
      })) as RunnerSpecMatch
      setMatchResult(result)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : t("app.matchFailed"))
    }
  }

  const copyRunnerID = async (id: string) => {
    await navigator.clipboard.writeText(id)
    toast.success(t("app.runnerIDCopied"))
  }

  const openUserJob = (id: string) => {
    const groupPath = userSelectedKey ? userJobsPath(userSelectedKey) : ""
    const nextPath = groupPath && groupPath !== "/jobs" ? withSearchParam(groupPath, "job", id) : `/jobs/${encodeURIComponent(id)}`
    window.history.pushState(null, "", nextPath)
    setLocationPath(window.location.pathname)
    setLocationSearch(window.location.search)
  }

  const backToUserJobs = () => {
    const nextPath = userJobsPath(userSelectedKey)
    window.history.pushState(null, "", nextPath)
    setLocationPath(window.location.pathname)
    setLocationSearch(window.location.search)
  }

  const loadUserJobGroup = useCallback((key: string) => {
    const path = userJobGroupAPIPath(key)
    return path ? request(path) : Promise.resolve(null)
  }, [request])

  if (routeAccess === "public") {
    return (
      <>
        {locationPath === "/" ? (
          <LandingPage />
        ) : (
          <Suspense
            fallback={
              <div
                className="min-h-screen bg-[#f7fbfd] dark:bg-[#07171e]"
                aria-busy="true"
              />
            }
          >
            <DocsPage path={locationPath} />
          </Suspense>
        )}
        <Toaster richColors />
      </>
    )
  }

  if (routeAccess === "not-found") {
    return <NotFoundPage />
  }

  const authViewState = authRouteViewState(authSessionStatus, authSession.authenticated)

  if (authViewState === "loading") {
    return <SessionLoadingPage />
  }

  if (authViewState === "error") {
    return <SessionErrorPage onRetry={() => void loadAuthSession()} />
  }

  if (authViewState === "sign-in") {
    return (
      <SignInPage
        oauthEnabled={authSession.oauth_enabled}
        returnTo={`${locationPath}${locationSearch}`}
      />
    )
  }

  if (isAdminRoute && !hasAccess) {
    return <AccessDeniedPage login={authSession.login} onSignOut={signOut} />
  }

  if (!isAdminRoute) {
    if (userJobID) {
      return (
        <>
          <UserJobRedirect
            id={userJobID}
            request={request}
            onResolved={(key) => {
              setUserSelectedKey(key)
              const nextPath = withSearchParam(userJobsPath(key), "job", userJobID)
              window.history.replaceState(null, "", nextPath)
              setLocationPath(window.location.pathname)
              setLocationSearch(window.location.search)
            }}
            fallback={
              <RunnerJobDetail
                id={userJobID}
                apiBase="/user/runner_requests"
                onBack={backToUserJobs}
                onOpenJob={openUserJob}
                request={request}
              />
            }
          />
          <Toaster richColors />
        </>
      )
    }
    return (
      <SandboxRegionsContext.Provider value={sandboxRegions}>
      <>
        <UserDashboard
          authSession={authSession}
          githubApp={githubApp}
          locationPath={locationPath}
          productTourOnboarding={productTourOnboarding}
          userPreferences={userPreferences}
          userPreferencesScope={userPreferencesScope}
          runners={userRunners}
          runnerTotal={userRunnerTotal}
          loadingRunnerHistory={loadingUserRunnerHistory}
          selectedKey={userSelectedKey}
          selectedJobID={userSelectedJobID}
          page={userPage}
          selectedRepositoryAccountLogin={selectedRepositoryAccountLogin}
          accountSettingsRoute={accountSettingsRoute || defaultAccountSettingsRoute(authSession.login)}
          authorizedRepositories={authorizedRepositories}
          repositoryErrors={repositoryErrors}
          loadingRepositoriesFor={loadingRepositoriesFor}
          syncingGitHubInstallations={syncingGitHubInstallations}
          onLoadAuthorizedRepositories={(id) => void loadAuthorizedRepositories(id)}
          onSyncGitHubInstallations={() => void syncGitHubInstallations()}
          onNavigateProductTourStart={openProductTourStart}
          onSaveProductTourOnboarding={saveProductTourOnboarding}
          onSaveSandboxConfig={saveSandboxConfig}
          onDeleteSandboxAPIKey={deleteSandboxAPIKey}
          onSaveCacheConfig={saveCacheConfig}
          onDeleteCacheConfig={deleteCacheConfig}
          onNavigate={setUserPage}
          onNavigateRepositoryAccount={setRepositoryAccount}
          onNavigateAccountSettings={setAccountSettingsRoute}
          onOpenJob={openUserJob}
          onLoadJobGroup={loadUserJobGroup}
          onLoadRunnerHistory={() => void loadUserRunnerHistory()}
          request={request}
          onSelectKey={setUserJobsSelection}
          onSignOut={signOut}
        />
        <Toaster richColors />
      </>
      </SandboxRegionsContext.Provider>
    )
  }

  return (
    <SandboxRegionsContext.Provider value={sandboxRegions}>
    <SidebarProvider>
      <AppSidebar
        section={section}
        onSectionChange={setSection}
      />
      <SidebarInset className="min-h-0 overflow-hidden">
        <SiteHeader authSession={authSession} onSignOut={signOut} />
        <main className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto p-4 lg:gap-6 lg:p-6">
          {section === "overview" || (section === "runner_requests" && !runnerRequestIdentifier) ? (
            <div className="grid gap-4 md:grid-cols-2 xl:grid-cols-4">
              {metrics.map((metric) => (
                <Card key={metric.id} className="gap-3 py-5">
                  <CardHeader className="px-5">
                    <CardDescription>{metric.label}</CardDescription>
                    <CardTitle className="text-3xl">{metric.value}</CardTitle>
                  </CardHeader>
                  <CardContent className="px-5 text-xs text-muted-foreground">
                    {metric.description}
                  </CardContent>
                </Card>
              ))}
            </div>
          ) : null}

          {section === "overview" ? (
            <OverviewSection
              runners={runners}
              runnerSpecs={runnerSpecs}
              onEditRunnerSpec={loadRunnerSpecIntoForm}
            />
          ) : null}

          {section === "accounts" ? <AccountsSection request={request} /> : null}

          {section === "runner_requests" && runnerRequestIdentifier ? (
            <RunnerRequestSection
              identifier={runnerRequestIdentifier}
              request={request}
              onBackToRunnerRequests={() => setSection("runner_requests")}
              onResolvedRequestID={canonicalizeRunnerRequestID}
              onCopyRunnerID={(id) => void copyRunnerID(id)}
              onRetryRunner={retryRunner}
              onStopRunner={stopRunner}
            />
          ) : null}

          {section === "runner_requests" && !runnerRequestIdentifier ? (
            <RunnerRequestsSection
              hasAccess={hasAccess}
              loading={loading}
              runners={runners}
              filteredRunners={filteredRunners}
              createID={createID}
              createRepository={createRepository}
              createRunnerSpec={createRunnerSpec}
              createLabels={createLabels}
              createRunnerOpen={createRunnerOpen}
              runnerStatusFilter={runnerStatusFilter}
              runnerRepositoryFilter={runnerRepositoryFilter}
              runnerSpecFilter={runnerSpecFilter}
              runnerRepositories={runnerRepositories}
              runnerSpecNames={runnerSpecNames}
              onRefresh={() => void loadAll()}
              onResetCreateRunnerForm={resetCreateRunnerForm}
              onCreateRunnerOpenChange={setCreateRunnerOpen}
              onCreateRunnerSubmit={createRunner}
              onCreateIDChange={setCreateID}
              onCreateRepositoryChange={setCreateRepository}
              onCreateRunnerSpecChange={setCreateRunnerSpec}
              onCreateLabelsChange={setCreateLabels}
              onStatusFilterChange={setRunnerStatusFilter}
              onRepositoryFilterChange={setRunnerRepositoryFilter}
              onRunnerSpecFilterChange={setRunnerSpecFilter}
              onLookupRunnerRequest={(identifier) => void lookupRunnerRequest(identifier)}
              onOpenRunnerRequest={openRunnerRequest}
              onRetryRunner={(id) => void retryRunner(id)}
              onStopRunner={(id) => void stopRunner(id)}
            />
          ) : null}

          {section === "runner_specs" ? (
            <RunnerSpecsSection
              savingRunnerSpec={savingRunnerSpec}
              loading={loading}
              runnerSpecs={runnerSpecs}
              runnerSpecOpen={runnerSpecOpen}
              editingRunnerSpec={editingRunnerSpec}
              runnerSpecForm={runnerSpecForm}
              onRefresh={() => void loadAll()}
              onResetRunnerSpecForm={resetRunnerSpecForm}
              onRunnerSpecOpenChange={setRunnerSpecOpen}
              onRunnerSpecFormChange={setRunnerSpecForm}
              onSubmitRunnerSpec={saveRunnerSpec}
              onEditRunnerSpec={loadRunnerSpecIntoForm}
              onDeleteRunnerSpec={(name) => void deleteRunnerSpec(name)}
            />
          ) : null}

          {section === "sandbox_service" ? (
            <SandboxServiceDefaultSection request={request} />
          ) : null}

          {section === "match" ? (
            <MatchSection
              matchRepository={matchRepository}
              matchLabels={matchLabels}
              matchResult={matchResult}
              onRepositoryChange={setMatchRepository}
              onLabelsChange={setMatchLabels}
              onSubmit={runMatchTest}
            />
          ) : null}

          {section === "audit" ? <AuditSection auditEvents={auditEvents} /> : null}

          {section === "diagnostics" ? (
            <DiagnosticsSection
              diagnostics={diagnostics}
              request={request}
            />
          ) : null}
        </main>
      </SidebarInset>
      <Toaster richColors />
    </SidebarProvider>
    </SandboxRegionsContext.Provider>
  )
}

function diagnosticRunnerFromSearch(search: string) {
  return new URLSearchParams(search).get("runner")?.trim() || ""
}

function UserJobRedirect({
  id,
  request,
  onResolved,
  fallback,
}: {
  id: string
  request: (url: string, options?: RequestInit) => Promise<unknown>
  onResolved: (key: string) => void
  fallback: ReactNode
}) {
  const { t } = useTranslation()
  const [failedID, setFailedID] = useState("")

  useEffect(() => {
    let cancelled = false
    void request(`/user/runner_requests/${encodeURIComponent(id)}/group`)
      .then((group) => {
        if (cancelled) return
        const key = isRunnerJobGroupResponse(group) ? group.key : ""
        if (key) {
          onResolved(key)
          return
        }
        setFailedID(id)
      })
      .catch(() => {
        if (!cancelled) setFailedID(id)
      })
    return () => {
      cancelled = true
    }
  }, [id, onResolved, request])

  if (failedID === id) return <>{fallback}</>
  return (
    <div className="flex min-h-screen items-center justify-center bg-background text-sm text-muted-foreground">
      {t("user.openingBuildContext")}
    </div>
  )
}

function defaultAccountSettingsRoute(currentLogin?: string): AccountSettingsRoute {
  return { accountLogin: currentLogin, tab: "preferences" }
}

function userPreferencesPath(installationID?: number, base = "/user/preferences") {
  if (!installationID) return base
  return `${base}?installation_id=${encodeURIComponent(String(installationID))}`
}

function isAccountSettingsPath(path: string): boolean {
  return (
    path === "/settings" ||
    path === "/accounts" ||
    path === "/account/repositories" ||
    path === "/account/preferences" ||
    path === "/account/sandbox" ||
    path === "/account/sandbox-templates" ||
    path === "/account/sandbox-instances" ||
    path === "/account/runner-specs" ||
    /^\/organizations\/[^/]+\/(repositories|preferences|sandbox|sandbox-templates|sandbox-instances|runner-specs)$/.test(path)
  )
}

function parseAccountSettingsRoute(path: string, currentLogin?: string): AccountSettingsRoute | null {
  if (path === "/settings" || path === "/accounts") return defaultAccountSettingsRoute(currentLogin)
  if (path === "/account/repositories") return { accountLogin: currentLogin, tab: "repositories" }
  if (path === "/account/preferences") return { accountLogin: currentLogin, tab: "preferences" }
  if (path === "/account/sandbox" || path === "/account/sandbox-templates") return { accountLogin: currentLogin, tab: "sandbox-templates" }
  if (path === "/account/sandbox-instances") return { accountLogin: currentLogin, tab: "sandbox-instances" }
  if (path === "/account/runner-specs") return { accountLogin: currentLogin, tab: "runner-specs" }

  const organizationMatch = path.match(/^\/organizations\/([^/]+)\/(repositories|preferences|sandbox|sandbox-templates|sandbox-instances|runner-specs)$/)
  if (!organizationMatch) return null
  const accountLogin = safeDecodePathSegment(organizationMatch[1])
  if (!accountLogin) return null

  return {
    accountLogin,
    tab: (organizationMatch[2] === "sandbox" ? "sandbox-templates" : organizationMatch[2]) as AccountSettingsTab,
  }
}

function safeDecodePathSegment(value: string): string | null {
  try {
    return decodeURIComponent(value)
  } catch {
    return null
  }
}

function userJobIDFromPath(path: string) {
  const match = path.match(/^\/jobs\/([^/]+)$/)
  return match ? safeDecodePathSegment(match[1]) || "" : ""
}

function userJobIDFromSearch(search: string) {
  return new URLSearchParams(search).get("job") || ""
}

function isRunnerJobGroupResponse(value: unknown): value is { key: string } {
  return Boolean(value && typeof value === "object" && typeof (value as { key?: unknown }).key === "string")
}

function isUserJobsRoute(path: string) {
  return path === "/jobs" || Boolean(userJobsGroupKeyFromPath(path))
}

function userJobsGroupKeyFromLocation(path: string, search: string) {
  const pathKey = userJobsGroupKeyFromPath(path, search)
  if (pathKey) return pathKey
  if (path !== "/jobs") return ""
  return new URLSearchParams(search).get("group") || ""
}

function userJobsGroupKeyFromPath(path: string, search = "") {
  const pullRequestMatch = path.match(/^\/github\/pulls\/([^/]+)\/([^/]+)\/(\d+)\/jobs$/)
  if (pullRequestMatch) {
    return `pr:${decodeRepositoryPath(pullRequestMatch[1], pullRequestMatch[2])}:${pullRequestMatch[3]}`
  }

  const legacyPullRequestMatch = path.match(/^\/jobs\/pulls\/([^/]+)\/([^/]+)\/(\d+)$/)
  if (legacyPullRequestMatch) {
    return `pr:${decodeRepositoryPath(legacyPullRequestMatch[1], legacyPullRequestMatch[2])}:${legacyPullRequestMatch[3]}`
  }

  const runMatch = path.match(/^\/github\/runs\/([^/]+)\/([^/]+)\/(\d+)\/jobs$/)
  if (runMatch) {
    return `run:${decodeRepositoryPath(runMatch[1], runMatch[2])}:${runMatch[3]}`
  }

  const legacyRunMatch = path.match(/^\/jobs\/runs\/([^/]+)\/([^/]+)\/(\d+)$/)
  if (legacyRunMatch) {
    return `run:${decodeRepositoryPath(legacyRunMatch[1], legacyRunMatch[2])}:${legacyRunMatch[3]}`
  }

  const branchMatch = path.match(/^\/github\/branches\/([^/]+)\/([^/]+)\/(.+)\/([^/]+)\/jobs$/)
  if (branchMatch) {
    const repository = decodeRepositoryPath(branchMatch[1], branchMatch[2])
    const branch = safeDecodePathSegment(branchMatch[3])
    const sha = safeDecodePathSegment(branchMatch[4])
    if (!branch || !sha) return ""
    return `branch:${repository}:${branch}:${sha}`
  }

  const branchQueryMatch = path.match(/^\/github\/branches\/([^/]+)\/([^/]+)\/([^/]+)\/jobs$/)
  if (branchQueryMatch) {
    const repository = decodeRepositoryPath(branchQueryMatch[1], branchQueryMatch[2])
    const sha = safeDecodePathSegment(branchQueryMatch[3])
    const branch = new URLSearchParams(search).get("branch") || ""
    if (!branch || !sha) return ""
    return `branch:${repository}:${branch}:${sha}`
  }

  const legacyBranchMatch = path.match(/^\/jobs\/branches\/([^/]+)\/([^/]+)\/(.+)\/([^/]+)$/)
  if (legacyBranchMatch) {
    const repository = decodeRepositoryPath(legacyBranchMatch[1], legacyBranchMatch[2])
    const branch = safeDecodePathSegment(legacyBranchMatch[3])
    const sha = safeDecodePathSegment(legacyBranchMatch[4])
    if (!branch || !sha) return ""
    return `branch:${repository}:${branch}:${sha}`
  }

  const manualMatch = path.match(/^\/jobs\/manual\/([^/]+)\/([^/]+)\/([^/]+)$/)
  if (manualMatch) {
    const repository = decodeRepositoryPath(manualMatch[1], manualMatch[2])
    const id = safeDecodePathSegment(manualMatch[3])
    if (!id) return ""
    return `manual:${repository}:${id}`
  }

  return ""
}

function userJobsPath(groupKey: string) {
  if (!groupKey) return "/jobs"
  const pullRequestMatch = groupKey.match(/^pr:(.+):(\d+)$/)
  if (pullRequestMatch) return `/github/pulls/${encodeRepositoryPath(pullRequestMatch[1])}/${pullRequestMatch[2]}/jobs`

  const runMatch = groupKey.match(/^run:(.+):(\d+)$/)
  if (runMatch) return `/github/runs/${encodeRepositoryPath(runMatch[1])}/${runMatch[2]}/jobs`

  const branchMatch = groupKey.match(/^branch:([^:]+):(.+):([^:]+)$/)
  if (branchMatch) {
    return withSearchParam(`/github/branches/${encodeRepositoryPath(branchMatch[1])}/${encodeURIComponent(branchMatch[3])}/jobs`, "branch", branchMatch[2])
  }

  const manualMatch = groupKey.match(/^manual:(.+):([^:]+)$/)
  if (manualMatch) return `/jobs/manual/${encodeRepositoryPath(manualMatch[1])}/${encodeURIComponent(manualMatch[2])}`

  return `/jobs?group=${encodeURIComponent(groupKey)}`
}

function userJobGroupAPIPath(groupKey: string) {
  if (!groupKey) return ""
  const pullRequestMatch = groupKey.match(/^pr:(.+):(\d+)$/)
  if (pullRequestMatch) return `/user/github/pulls/${encodeRepositoryPath(pullRequestMatch[1])}/${pullRequestMatch[2]}/jobs`

  const runMatch = groupKey.match(/^run:(.+):(\d+)$/)
  if (runMatch) return `/user/github/runs/${encodeRepositoryPath(runMatch[1])}/${runMatch[2]}/jobs`

  const branchMatch = groupKey.match(/^branch:([^:]+):(.+):([^:]+)$/)
  if (branchMatch) {
    return withSearchParam(`/user/github/branches/${encodeRepositoryPath(branchMatch[1])}/${encodeURIComponent(branchMatch[3])}/jobs`, "branch", branchMatch[2])
  }

  return ""
}

function encodeRepositoryPath(repository: string) {
  return repository.split("/").map((segment) => encodeURIComponent(segment)).join("/")
}

function withSearchParam(path: string, key: string, value: string) {
  const [pathname, search = ""] = path.split("?")
  const params = new URLSearchParams(search)
  params.set(key, value)
  const query = params.toString()
  return query ? `${pathname}?${query}` : pathname
}

function withPreservedJobSearch(path: string, search: string) {
  const job = new URLSearchParams(search).get("job")
  return job ? withSearchParam(path, "job", job) : path
}

function mergeUserRunnerPages(primary: RunnerState[], existing: RunnerState[]): RunnerState[] {
  const merged: RunnerState[] = []
  const seen = new Set<string>()
  for (const runner of [...primary, ...existing]) {
    if (seen.has(runner.id)) continue
    seen.add(runner.id)
    merged.push(runner)
  }
  return merged
}

function decodeRepositoryPath(ownerSegment: string, repoSegment: string) {
  const owner = safeDecodePathSegment(ownerSegment)
  const repo = safeDecodePathSegment(repoSegment)
  if (!owner || !repo) return "unknown/repository"
  return `${owner}/${repo}`
}

function accountSettingsPath(
  accountLogin: string | undefined,
  currentLogin: string | undefined,
  tab: AccountSettingsTab
): string {
  const segment = tab === "preferences" ? "preferences" : tab === "sandbox-templates" ? "sandbox-templates" : tab === "sandbox-instances" ? "sandbox-instances" : tab === "runner-specs" ? "runner-specs" : "repositories"
  const login = accountLogin?.trim()
  if (!login || login === currentLogin) return `/account/${segment}`
  return `/organizations/${encodeURIComponent(login)}/${segment}`
}

export default App
