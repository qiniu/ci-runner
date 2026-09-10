import { describe, expect, test } from "bun:test"
import * as appPolicy from "./app-load-policy"
import * as adminTypes from "./admin-types"

import {
  adminDataResources,
  adminPollingResources,
  shouldPollAdminSection,
  shouldPollUserRoute,
  userDataResources,
  userPollingResources,
  userRunnerRequestLimit,
  userRunnerRequestsPath,
} from "./app-load-policy"

describe("app load policy", () => {
  test.each([
    ["overview", ["runner_requests", "runner_specs"]],
    ["runner_requests", ["runner_requests", "runner_specs"]],
    ["runner_specs", ["runner_specs"]],
    ["audit", ["audit_events"]],
    ["accounts", []],
    ["sandbox_service", []],
    ["match", []],
    ["diagnostics", []],
  ])("loads only data used by the %s admin section", (section, expected) => {
    expect(adminDataResources(section)).toEqual(expected)
  })

  test("polls only dynamic admin request surfaces", () => {
    expect(shouldPollAdminSection("overview")).toBe(true)
    expect(shouldPollAdminSection("runner_requests")).toBe(true)
    expect(shouldPollAdminSection("runner_specs")).toBe(false)
    expect(shouldPollAdminSection("audit")).toBe(false)
    expect(adminPollingResources("overview")).toEqual(["runner_requests"])
    expect(adminPollingResources("runner_requests")).toEqual(["runner_requests"])
    expect(adminPollingResources("runner_specs")).toEqual([])
  })

  test.each([
    ["/", []],
    ["/jobs", ["github_app", "runner_requests", "onboarding"]],
    ["/github/pulls/octo/repo/12/jobs", ["github_app", "runner_requests", "onboarding"]],
    ["/github/runs/octo/repo/34/jobs", ["github_app", "runner_requests", "onboarding"]],
    ["/github/branches/octo/repo/deadbeef/jobs", ["github_app", "runner_requests", "onboarding"]],
    ["/jobs/manual/octo/repo/manual-1", ["github_app", "runner_requests", "onboarding"]],
    ["/repositories", ["github_app", "preferences", "onboarding"]],
    ["/runner-specs", []],
    ["/account/repositories", ["github_app", "preferences", "onboarding"]],
    ["/organizations/octo/repositories", ["github_app", "preferences", "onboarding"]],
    ["/account/preferences", ["github_app", "preferences", "onboarding"]],
    ["/organizations/octo/sandbox-templates", ["github_app", "preferences", "onboarding"]],
    ["/account/runner-specs", ["github_app", "preferences", "onboarding"]],
    ["/organizations/octo/runner-specs", ["github_app", "preferences", "onboarding"]],
    ["/jobs/job-1", []],
    ["/admin/", []],
  ])("loads only data used by user route %s", (path, expected) => {
    expect(userDataResources(path)).toEqual(expected)
  })

  test.each([
    ["/jobs", "/user/github-app"],
    ["/repositories", "/user/github-app"],
    ["/account/preferences", "/user/github-app?include=settings"],
    ["/organizations/qbox/sandbox-templates", "/user/github-app?include=settings"],
  ])("loads Settings manageability only for Settings route %s", (path, expected) => {
    expect(appPolicy.userGitHubAppPath?.(path)).toBe(expected)
  })

  test("polls only user job-list routes", () => {
    expect(shouldPollUserRoute("/")).toBe(false)
    expect(shouldPollUserRoute("/jobs")).toBe(true)
    expect(shouldPollUserRoute("/github/pulls/octo/repo/12/jobs")).toBe(true)
    expect(shouldPollUserRoute("/repositories")).toBe(false)
    expect(shouldPollUserRoute("/account/preferences")).toBe(false)
    expect(shouldPollUserRoute("/jobs/job-1")).toBe(false)
    expect(userPollingResources("/")).toEqual([])
    expect(userPollingResources("/jobs")).toEqual(["runner_requests"])
    expect(userPollingResources("/repositories")).toEqual([])
  })

  test("keeps the jobs home light while making stable job routes resolve the bounded history", () => {
    expect(userRunnerRequestLimit("/jobs", false)).toBe(100)
    expect(userRunnerRequestLimit("/github/pulls/octo/repo/12/jobs", false)).toBe(500)
    expect(userRunnerRequestLimit("/github/pulls/octo/repo/12/jobs", true)).toBe(100)
    expect(userRunnerRequestsPath(500, 0)).toBe("/user/runner_requests?limit=500&offset=0")
  })

  test("classifies public, protected, admin, and unknown routes explicitly", () => {
    expect(appPolicy.appRouteAccess?.("/")).toBe("public")
    expect(appPolicy.appRouteAccess?.("/docs")).toBe("public")
    expect(appPolicy.appRouteAccess?.("/docs/getting-started/hosted")).toBe("public")
    expect(appPolicy.appRouteAccess?.("/docs/getting-started/deploy")).toBe("public")
    expect(appPolicy.appRouteAccess?.("/docs/guides/workflow")).toBe("public")
    expect(appPolicy.appRouteAccess?.("/docs/guides/cache")).toBe("public")
    expect(appPolicy.appRouteAccess?.("/docs/guides/custom-templates")).toBe("public")
    expect(appPolicy.appRouteAccess?.("/docs/troubleshooting")).toBe("public")
    expect(appPolicy.appRouteAccess?.("/docs/reference/runner-labels")).toBe("public")
    expect(appPolicy.appRouteAccess?.("/jobs")).toBe("user")
    expect(appPolicy.appRouteAccess?.("/jobs/job-1")).toBe("user")
    expect(appPolicy.appRouteAccess?.("/github/pulls/octo/repo/12/jobs")).toBe("user")
    expect(appPolicy.appRouteAccess?.("/runner-specs")).toBe("user")
    expect(appPolicy.appRouteAccess?.("/account/preferences")).toBe("user")
    expect(appPolicy.appRouteAccess?.("/account/runner-specs")).toBe("user")
    expect(appPolicy.appRouteAccess?.("/organizations/octo/runner-specs")).toBe("user")
    expect(appPolicy.appRouteAccess?.("/account/runner-types")).toBe("not-found")
    expect(appPolicy.appRouteAccess?.("/organizations/octo/runner-types")).toBe("not-found")
    expect(appPolicy.appRouteAccess?.("/admin/runner_specs")).toBe("admin")
    expect(appPolicy.appRouteAccess?.("/admin/runner_requests/101445685709")).toBe("admin")
    expect(appPolicy.appRouteAccess?.("/admin/not-a-section")).toBe("not-found")
    expect(appPolicy.appRouteAccess?.("/docs/not-a-guide")).toBe("not-found")
    expect(appPolicy.appRouteAccess?.("/not-a-route")).toBe("not-found")
  })

  test("resolves only a single encoded runner request path segment", () => {
    expect(adminTypes.runnerRequestIdentifierFromAdminPath?.("/admin/runner_requests/e2b-101445685709")).toBe("e2b-101445685709")
    expect(adminTypes.runnerRequestIdentifierFromAdminPath?.("/admin/runner_requests/request%20one")).toBe("request one")
    expect(adminTypes.runnerRequestIdentifierFromAdminPath?.("/admin/runner_requests/request%2Fextra")).toBe("")
    expect(adminTypes.runnerRequestIdentifierFromAdminPath?.("/admin/runner_requests/%20")).toBe("")
    expect(adminTypes.runnerRequestIdentifierFromAdminPath?.("/admin/runner_requests/request/extra")).toBe("")
  })

  test("rejects incomplete legacy Jobs routes", () => {
    expect(appPolicy.appRouteAccess?.("/jobs/pulls/octo")).toBe("not-found")
    expect(appPolicy.appRouteAccess?.("/jobs/runs/octo/repo")).toBe("not-found")
    expect(appPolicy.appRouteAccess?.("/jobs/branches/octo/repo/main")).toBe("not-found")
    expect(appPolicy.appRouteAccess?.("/jobs/manual/octo/repo")).toBe("not-found")
  })

  test("builds a same-origin sign-in URL that returns to the protected destination", () => {
    expect(appPolicy.signInURL?.("/jobs/job-1", "?tab=logs")).toBe(
      "/auth/github/login?return_to=%2Fjobs%2Fjob-1%3Ftab%3Dlogs",
    )
  })

  test("does not treat an unchecked session as signed out", () => {
    expect(appPolicy.authRouteViewState?.("checking", false)).toBe("loading")
    expect(appPolicy.authRouteViewState?.("ready", true)).toBe("authenticated")
    expect(appPolicy.authRouteViewState?.("ready", false)).toBe("sign-in")
    expect(appPolicy.authRouteViewState?.("error", false)).toBe("error")
  })

  test("loads optional user resources without failing the primary workspace request", async () => {
    await expect(appPolicy.loadOptionalUserResource?.(Promise.resolve({ status: "pending" }))).resolves.toEqual({
      status: "pending",
    })
    await expect(appPolicy.loadOptionalUserResource?.(Promise.reject(new Error("onboarding unavailable")))).resolves.toBeNull()
  })

  test("accepts results only from the latest user workspace load", () => {
    const gate = appPolicy.createLatestUserLoadGate?.()
    const accountLoad = gate?.begin("miclle:/account/preferences")
    const organizationLoad = gate?.begin("miclle:/organizations/qiniu/preferences")

    expect(gate?.isCurrent(accountLoad)).toBe(false)
    expect(gate?.isCurrent(organizationLoad)).toBe(true)
  })

  test("keeps initial and polling loads current within the same workspace scope", () => {
    const gate = appPolicy.createLatestUserLoadGate?.()
    const initialLoad = gate?.begin("miclle:/jobs")
    const pollingLoad = gate?.begin("miclle:/jobs")

    expect(gate?.isCurrent(initialLoad)).toBe(true)
    expect(gate?.isCurrent(pollingLoad)).toBe(true)
  })
})
