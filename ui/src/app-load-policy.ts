import { adminSections, type AdminSection } from "@/admin-types"
import { isSiteDocumentPath } from "@/site-doc-routes"

export const userRunnerInitialPageSize = 100
export const userRunnerHistoryWindow = 500

export type AdminDataResource =
  | "runner_requests"
  | "runner_specs"
  | "audit_events"

export type UserDataResource = "github_app" | "runner_requests" | "preferences" | "onboarding"
export type AppRouteAccess = "public" | "user" | "admin" | "not-found"
export type AuthSessionCheckStatus = "checking" | "ready" | "error"
export type AuthRouteViewState = "loading" | "authenticated" | "sign-in" | "error"

export function createLatestUserLoadGate() {
  let generation = 0
  let scope = ""
  return {
    begin(nextScope: string) {
      if (nextScope !== scope) {
        generation += 1
        scope = nextScope
      }
      return generation
    },
    isCurrent(candidate: number | undefined) {
      return candidate === generation
    },
  }
}

const adminResourcesBySection: Record<AdminSection, readonly AdminDataResource[]> = {
  overview: ["runner_requests", "runner_specs"],
  accounts: [],
  runner_requests: ["runner_requests", "runner_specs"],
  runner_specs: ["runner_specs"],
  sandbox_service: [],
  match: [],
  audit: ["audit_events"],
  diagnostics: [],
}

export function adminDataResources(section: AdminSection): AdminDataResource[] {
  return [...adminResourcesBySection[section]]
}

export function shouldPollAdminSection(section: AdminSection): boolean {
  return section === "overview" || section === "runner_requests"
}

export function adminPollingResources(section: AdminSection): AdminDataResource[] {
  return shouldPollAdminSection(section) ? ["runner_requests"] : []
}

export function userDataResources(path: string): UserDataResource[] {
  if (isUserJobsRoute(path)) return ["github_app", "runner_requests", "onboarding"]
  if (path === "/repositories") return ["github_app", "preferences", "onboarding"]
  if (path === "/runner-specs") return []
  if (isAccountSettingsRoute(path)) return ["github_app", "preferences", "onboarding"]
  return []
}

export function userGitHubAppPath(path: string): string {
  return isSandboxSettingsRoute(path)
    ? "/user/github-app?include=settings"
    : "/user/github-app"
}

export function shouldPollUserRoute(path: string): boolean {
  return isUserJobsRoute(path)
}

export function userPollingResources(path: string): UserDataResource[] {
  return shouldPollUserRoute(path) ? ["runner_requests"] : []
}

export async function loadOptionalUserResource<T>(resource: Promise<T>): Promise<T | null> {
  try {
    return await resource
  } catch {
    return null
  }
}

export function userRunnerRequestLimit(path: string, polling: boolean): number {
  if (polling || path === "/jobs") return userRunnerInitialPageSize
  return isUserJobsRoute(path) ? userRunnerHistoryWindow : userRunnerInitialPageSize
}

export function userRunnerRequestsPath(limit: number, offset: number): string {
  return `/user/runner_requests?limit=${limit}&offset=${offset}`
}

export function appRouteAccess(path: string): AppRouteAccess {
  if (path === "/" || isSiteDocumentPath(path)) return "public"
  if (isAdminRoute(path)) return "admin"
  if (isUserRoute(path)) return "user"
  return "not-found"
}

export function authRouteViewState(
  status: AuthSessionCheckStatus,
  authenticated: boolean,
): AuthRouteViewState {
  if (status === "checking") return "loading"
  if (status === "error") return "error"
  return authenticated ? "authenticated" : "sign-in"
}

export function signInURL(path: string, search = ""): string {
  const safePath = path.startsWith("/") && !path.startsWith("//") ? path : "/jobs"
  const safeSearch = search.startsWith("?") ? search : ""
  return `/auth/github/login?return_to=${encodeURIComponent(`${safePath}${safeSearch}`)}`
}

export function isUserJobsRoute(path: string): boolean {
  return (
    path === "/jobs" ||
    /^\/github\/(pulls|runs)\/[^/]+\/[^/]+\/[^/]+\/jobs$/.test(path) ||
    /^\/github\/branches\/[^/]+\/[^/]+\/.+\/jobs$/.test(path) ||
    /^\/jobs\/(pulls|runs)\/[^/]+\/[^/]+\/\d+$/.test(path) ||
    /^\/jobs\/branches\/[^/]+\/[^/]+\/.+\/[^/]+$/.test(path) ||
    /^\/jobs\/manual\/[^/]+\/[^/]+\/[^/]+$/.test(path)
  )
}

export function isAccountSettingsRoute(path: string): boolean {
  return (
    path === "/settings" ||
    path === "/accounts" ||
    /^\/account\/(repositories|preferences|sandbox|sandbox-templates|sandbox-instances|runner-specs)$/.test(path) ||
    /^\/organizations\/[^/]+\/(repositories|preferences|sandbox|sandbox-templates|sandbox-instances|runner-specs)$/.test(path)
  )
}

function isSandboxSettingsRoute(path: string): boolean {
  return (
    path === "/settings" ||
    path === "/accounts" ||
    /^\/account\/(preferences|sandbox|sandbox-templates|sandbox-instances|runner-specs)$/.test(path) ||
    /^\/organizations\/[^/]+\/(preferences|sandbox|sandbox-templates|sandbox-instances|runner-specs)$/.test(path)
  )
}

function isAdminRoute(path: string): boolean {
  if (path === "/admin" || path === "/admin/") return true
  const match = path.match(/^\/admin\/([^/]+)$/)
  return Boolean(match && adminSections.includes(match[1] as AdminSection))
}

function isUserRoute(path: string): boolean {
  return (
    isUserJobsRoute(path) ||
    /^\/jobs\/[^/]+$/.test(path) ||
    path === "/repositories" ||
    path === "/runner-specs" ||
    isAccountSettingsRoute(path)
  )
}
