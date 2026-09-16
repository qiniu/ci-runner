import { expect, test, type Page } from "@playwright/test"

import type { RunnerJobGroup, RunnerState } from "../src/admin-types"
import { getLocalAuthSessionRoute } from "./production-smoke-support"

const postRenderObservationMs = 1_000

test("boots the public landing page from the production bundle", async ({ page }) => {
  const diagnostics = observeBrowserDiagnostics(page)

  await routeLocalAnonymousSession(page)

  const response = await page.goto("/", { waitUntil: "networkidle" })

  expect(response?.ok()).toBe(true)
  await expect(
    page.getByRole("heading", { name: "GitHub Actions, powered by Qiniu Sandbox" }),
  ).toBeVisible()
  await expect(page.locator("#root")).not.toBeEmpty()
  await page.waitForTimeout(postRenderObservationMs)
  diagnostics.expectClean()
})

test("serves the hosted guide as a responsive public production route", async ({ page }) => {
  const diagnostics = observeBrowserDiagnostics(page)

  await routeLocalAnonymousSession(page)
  await page.setViewportSize({ width: 390, height: 844 })
  const response = await page.goto("/docs/getting-started/hosted", { waitUntil: "networkidle" })

  expect(response?.ok()).toBe(true)
  await expect(
    page.getByRole("heading", { name: "Get started with the hosted service", exact: true }),
  ).toBeVisible()
  await expect(page.getByRole("link", { name: "Copy a complete workflow" })).toHaveAttribute(
    "href",
    "/docs/guides/workflow",
  )
  await expect(page.locator("#root")).not.toBeEmpty()

  const viewport = await page.evaluate(() => ({
    documentWidth: document.documentElement.scrollWidth,
    viewportWidth: window.innerWidth,
  }))
  expect(viewport.documentWidth).toBeLessThanOrEqual(viewport.viewportWidth + 1)

  await page.waitForTimeout(postRenderObservationMs)
  diagnostics.expectClean()
})

test("serves the custom template guide from the production bundle", async ({ page }) => {
  const diagnostics = observeBrowserDiagnostics(page)

  await routeLocalAnonymousSession(page)
  await page.setViewportSize({ width: 390, height: 844 })
  const response = await page.goto("/docs/guides/custom-templates", { waitUntil: "networkidle" })

  expect(response?.ok()).toBe(true)
  await expect(
    page.getByRole("heading", { name: "Build and use a custom runner template", exact: true }),
  ).toBeVisible()
  await expect(page).toHaveTitle("Build and use a custom runner template · Qiniu CI Runner")
  await expect(page.locator('meta[name="description"]')).toHaveAttribute(
    "content",
    "Build a private Qiniu Sandbox template for your own tools, connect it to a custom Runner Spec, and select it from a GitHub Actions workflow.",
  )
  await expect(
    page.locator("pre code").filter({ hasText: "qshell sandbox template build --wait" }),
  ).toBeVisible()
  await expect(page.getByText("Status: ready", { exact: true }).first()).toBeVisible()

  const viewport = await page.evaluate(() => ({
    documentWidth: document.documentElement.scrollWidth,
    viewportWidth: window.innerWidth,
  }))
  expect(viewport.documentWidth).toBeLessThanOrEqual(viewport.viewportWidth + 1)

  await page.waitForTimeout(postRenderObservationMs)
  diagnostics.expectClean()
})

test("keeps the Jobs list independently scrollable beside the Web Console", async ({ page }) => {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")

  const diagnostics = observeBrowserDiagnostics(page)
  const runners = fixtureRunners(40)
  const selected = runners[0]
  const selectedGroup: RunnerJobGroup = {
    key: `branch:${selected.repository_full_name}:${selected.head_branch}:${selected.head_sha}`,
    group: "branch",
    repository: selected.repository_full_name || "fixture/repository-0",
    title: selected.head_branch || "fixture-branch-0",
    subtitle: selected.head_sha || "0".repeat(40),
    updated_at: selected.updated_at,
    jobs: [selected],
    current_jobs: [selected],
    previous_jobs: [],
    workflow_run_ids: [selected.workflow_run_id || 1],
    head_sha: selected.head_sha,
    head_branch: selected.head_branch,
  }

  await page.setViewportSize({ width: 1440, height: 900 })
  await page.route("**/auth/session", async (route) => {
    await route.fulfill({
      json: {
        authenticated: true,
        oauth_enabled: true,
        login: "fixture-user",
        role: "user",
      },
    })
  })
  await page.route("**/user/**", async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === "/user/github-app") {
      await route.fulfill({
        json: {
          setup_url: "/github-app/setup",
          installations: [{
            id: 1,
            account_id: 1,
            installation_id: 101,
            account_login: "fixture-user",
            repositories: runners.map((runner) => runner.repository_full_name),
            created_at: "2026-08-12T00:00:00Z",
            updated_at: "2026-08-12T00:00:00Z",
          }],
        },
      })
      return
    }
    if (url.pathname === "/user/runner_requests") {
      await route.fulfill({
        headers: { "X-Total-Count": String(runners.length) },
        json: runners,
      })
      return
    }
    if (url.pathname === "/user/onboarding/product-tour") {
      await route.fulfill({
        json: { version: 1, status: "completed", tour_seen: true },
      })
      return
    }
    if (url.pathname.startsWith("/user/github/branches/")) {
      await route.fulfill({ json: selectedGroup })
      return
    }
    if (url.pathname.endsWith("/github-log")) {
      await route.fulfill({
        contentType: "text/plain",
        body: Array.from({ length: 200 }, (_, index) => `fixture log line ${index + 1}`).join("\n"),
      })
      return
    }
    if (url.pathname.includes("/logs/")) {
      await route.fulfill({ contentType: "text/plain", body: "fixture runner log\n" })
      return
    }
    await route.fulfill({ status: 404, body: "fixture route not found" })
  })

  const response = await page.goto("/jobs", { waitUntil: "networkidle" })
  expect(response?.ok()).toBe(true)

  const jobList = page.locator("main aside .overflow-y-auto")
  const webConsoleTab = page.getByRole("tab", { name: /Web Console|Web 控制台/ })
  await expect(jobList).toBeVisible()
  await expect(webConsoleTab).toBeVisible()
  await webConsoleTab.click()

  const webConsole = page.getByRole("tabpanel", { name: /Web Console|Web 控制台/ })
  await expect(webConsole).toBeVisible()
  const consoleBefore = await webConsole.boundingBox()
  expect(consoleBefore).not.toBeNull()

  const layoutBefore = await page.evaluate(() => ({
    documentHeight: document.documentElement.scrollHeight,
    viewportHeight: window.innerHeight,
  }))
  expect(layoutBefore.documentHeight).toBeLessThanOrEqual(layoutBefore.viewportHeight + 1)

  const listBefore = await jobList.evaluate((element) => ({
    clientHeight: element.clientHeight,
    scrollHeight: element.scrollHeight,
  }))
  expect(listBefore.scrollHeight).toBeGreaterThan(listBefore.clientHeight)

  await jobList.hover()
  await page.mouse.wheel(0, 600)
  await expect.poll(() => jobList.evaluate((element) => element.scrollTop)).toBeGreaterThan(0)
  expect(await page.evaluate(() => window.scrollY)).toBe(0)

  const consoleAfter = await webConsole.boundingBox()
  expect(consoleAfter).not.toBeNull()
  expect(consoleAfter?.y).toBeCloseTo(consoleBefore?.y || 0, 0)

  await page.setViewportSize({ width: 1024, height: 700 })
  const narrowLayout = await page.evaluate(() => {
    const main = document.querySelector("main")
    return {
      documentHeight: document.documentElement.scrollHeight,
      mainOverflowY: main ? getComputedStyle(main).overflowY : "missing",
      viewportHeight: window.innerHeight,
    }
  })
  expect(narrowLayout.documentHeight).toBeGreaterThan(narrowLayout.viewportHeight)
  expect(narrowLayout.mainOverflowY).not.toBe("hidden")

  await page.waitForTimeout(postRenderObservationMs)
  diagnostics.expectClean()
})

test("reveals Jobs before secondary metadata and loads Runner logs only when selected", async ({ page }) => {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")

  const diagnostics = observeBrowserDiagnostics(page)
  const runners = fixtureRunners(1)
  let releaseJobs = () => {}
  let releaseGitHubApp = () => {}
  const jobsGate = new Promise<void>((resolve) => { releaseJobs = resolve })
  const githubAppGate = new Promise<void>((resolve) => { releaseGitHubApp = resolve })
  let runnerLogRequests = 0
  let groupRequests = 0

  await page.route("**/auth/session", (route) => route.fulfill({
    json: { authenticated: true, oauth_enabled: true, login: "fixture-user", role: "user" },
  }))
  await page.route("**/user/**", async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === "/user/runner_requests") {
      await jobsGate
      await route.fulfill({ headers: { "X-Total-Count": "1" }, json: runners })
    } else if (url.pathname === "/user/github-app") {
      await githubAppGate
      await route.fulfill({ json: { setup_url: "/github-app/setup", installations: [] } })
    } else if (url.pathname === "/user/onboarding/product-tour") {
      await route.fulfill({ json: { version: 1, status: "completed", tour_seen: true } })
    } else if (url.pathname.startsWith("/user/github/branches/")) {
      groupRequests += 1
      const selected = runners[0]
      await route.fulfill({ json: {
        key: `branch:${selected.repository_full_name}:${selected.head_branch}:${selected.head_sha}`,
        group: "branch", repository: selected.repository_full_name,
        title: selected.head_branch, subtitle: selected.head_sha, updated_at: selected.updated_at,
        jobs: [selected], current_jobs: [selected], previous_jobs: [],
        workflow_run_ids: [selected.workflow_run_id], head_sha: selected.head_sha, head_branch: selected.head_branch,
      } })
    } else if (url.pathname.endsWith("/github-log")) {
      await route.fulfill({ body: "fixture GitHub log", contentType: "text/plain" })
    } else if (url.pathname.includes("/logs/")) {
      runnerLogRequests += 1
      await route.fulfill({ body: "fixture Runner log", contentType: "text/plain" })
    } else {
      await route.fulfill({ status: 404, body: "fixture route not found" })
    }
  })

  try {
    const response = await page.goto("/jobs", { waitUntil: "domcontentloaded" })
    expect(response?.ok()).toBe(true)
    await expect(page.getByRole("status", { name: /Loading jobs|正在加载任务/ })).toBeVisible()

    releaseJobs()
    await expect(page.getByRole("button", { name: /fixture\/repository-0/ })).toBeVisible()
    await expect(page.getByText("fixture GitHub log")).toBeVisible()
    expect(runnerLogRequests).toBe(0)
    await expect.poll(() => groupRequests).toBe(1)
    await page.getByRole("tab", { name: /Runner logs|Runner 日志/i }).click()
    await expect(page.getByText("fixture Runner log")).toBeVisible()
    expect(runnerLogRequests).toBe(1)
    await page.getByRole("button", { name: /fixture\/repository-0/ }).click()
    expect(groupRequests).toBe(1)
    diagnostics.expectClean()
  } finally {
    releaseJobs()
    releaseGitHubApp()
  }
})

test("reloads Jobs after returning from another page while an older list request is pending", async ({ page }) => {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")

  const runners = fixtureRunners(1)
  let releaseFirstJobs = () => {}
  const firstJobsGate = new Promise<void>((resolve) => { releaseFirstJobs = resolve })
  let jobsRequests = 0

  await page.route("**/auth/session", (route) => route.fulfill({
    json: { authenticated: true, oauth_enabled: true, login: "fixture-user", role: "user" },
  }))
  await page.route("**/user/**", async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === "/user/runner_requests") {
      jobsRequests += 1
      if (jobsRequests === 1) await firstJobsGate
      await route.fulfill({ headers: { "X-Total-Count": "1" }, json: runners })
    } else if (url.pathname === "/user/github-app") {
      await route.fulfill({ json: { setup_url: "/github-app/setup", installations: [] } })
    } else if (url.pathname === "/user/runner-specs") {
      await route.fulfill({ json: { items: [], sandbox_source: "none" } })
    } else if (url.pathname === "/user/onboarding/product-tour") {
      await route.fulfill({ json: { version: 1, status: "completed", tour_seen: true } })
    } else if (url.pathname.endsWith("/github-log")) {
      await route.fulfill({ body: "fixture GitHub log", contentType: "text/plain" })
    } else {
      await route.fulfill({ status: 404, body: "fixture route not found" })
    }
  })

  try {
    await page.goto("/jobs", { waitUntil: "domcontentloaded" })
    await expect(page.getByRole("status", { name: /Loading jobs|正在加载任务/ })).toBeVisible()
    await expect.poll(() => jobsRequests).toBe(1)
    await page.locator('nav a[href="/runner-specs"]').first().click()
    await page.locator('nav a[href="/jobs"]').first().click()
    await expect(page.getByRole("button", { name: /fixture\/repository-0/ })).toBeVisible({ timeout: 3_000 })
  } finally {
    releaseFirstJobs()
  }
})

test("loads older jobs for the default visible group beyond the initial Jobs page", async ({ page }) => {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")

  const runners = fixtureRunners(100)
  const selected = { ...runners[0], pull_request_number: 42 }
  runners[0] = selected
  const previous: RunnerState = {
    ...selected,
    id: "fixture-previous-job",
    status: "completed",
    workflow_job_id: 30_000,
    workflow_run_id: 40_000,
    workflow_name: "Historical workflow",
    head_sha: "f".repeat(40),
    created_at: "2026-08-11T00:00:00Z",
    updated_at: "2026-08-11T00:00:00Z",
    completed_at: "2026-08-11T00:00:00Z",
  }

  await page.route("**/auth/session", (route) => route.fulfill({
    json: { authenticated: true, oauth_enabled: true, login: "fixture-user", role: "user" },
  }))
  await page.route("**/user/**", async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === "/user/runner_requests") {
      await route.fulfill({ headers: { "X-Total-Count": "101" }, json: runners })
    } else if (url.pathname === "/user/github-app") {
      await route.fulfill({ json: { setup_url: "/github-app/setup", installations: [] } })
    } else if (url.pathname === "/user/onboarding/product-tour") {
      await route.fulfill({ json: { version: 1, status: "completed", tour_seen: true } })
    } else if (url.pathname.startsWith("/user/github/pulls/")) {
      await route.fulfill({ json: {
        key: `pr:${selected.repository_full_name}:42`,
        group: "pull_request", repository: selected.repository_full_name,
        title: "PR #42", subtitle: selected.head_branch, updated_at: selected.updated_at,
        jobs: [selected, previous], current_jobs: [selected], previous_jobs: [previous],
        workflow_run_ids: [selected.workflow_run_id, previous.workflow_run_id],
        head_sha: selected.head_sha, head_branch: selected.head_branch,
        pull_request_number: 42,
      } })
    } else if (url.pathname.endsWith("/github-log")) {
      await route.fulfill({ body: "fixture GitHub log", contentType: "text/plain" })
    } else {
      await route.fulfill({ status: 404, body: "fixture route not found" })
    }
  })

  await page.goto("/jobs", { waitUntil: "domcontentloaded" })
  await expect(page.getByRole("button", { name: /fixture\/repository-0/ })).toBeVisible()
  await expect(page.getByRole("button", { name: "Historical workflow" })).toBeVisible()
})

test("does not show the previous account preferences during an in-place session change", async ({ page }) => {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")

  let sessionChecks = 0
  let bobPreferencesStarted = false
  let releaseBobPreferences = () => {}
  const bobPreferencesGate = new Promise<void>((resolve) => { releaseBobPreferences = resolve })
  const preferences = (bucket: string) => ({
    cache: { configured: true, region: "fixture-region", endpoint: "https://fixture.example", bucket, prefix: "fixture/" },
    sandbox: { mode: "custom", resolved_source: "none", api_url: "", api_key: { configured: false } },
  })

  await page.route("**/auth/session", (route) => route.fulfill({
    json: { authenticated: true, oauth_enabled: true, login: ++sessionChecks === 1 ? "alice" : "bob", role: "user" },
  }))
  await page.route("**/sandbox/regions", (route) => route.fulfill({ json: [] }))
  await page.route("**/user/**", async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === "/user/github-app") {
      await route.fulfill({ json: { setup_url: "/github-app/setup", settings_manageability: true, installations: [] } })
    } else if (url.pathname === "/user/preferences/cache" && route.request().method() === "DELETE") {
      await route.fulfill({ status: 401 })
    } else if (url.pathname === "/user/preferences") {
      if (sessionChecks > 1) {
        bobPreferencesStarted = true
        await bobPreferencesGate
      }
      await route.fulfill({ json: preferences(sessionChecks > 1 ? "bob-bucket" : "alice-bucket") })
    } else if (url.pathname === "/user/onboarding/product-tour") {
      await route.fulfill({ json: { version: 1, status: "completed", tour_seen: true } })
    } else {
      await route.fulfill({ status: 404, body: "fixture route not found" })
    }
  })

  try {
    await page.goto("/account/preferences", { waitUntil: "domcontentloaded" })
    await expect(page.locator("#cache-bucket")).toHaveValue("alice-bucket")
    await page.locator("form").filter({ has: page.locator("#cache-bucket") }).getByRole("button", { name: "Remove" }).click()
    await expect.poll(() => bobPreferencesStarted).toBe(true)
    await expect(page.locator("#cache-bucket")).toHaveValue("")
  } finally {
    releaseBobPreferences()
  }
})

test("reports a preferences failure without treating GitHub accounts as failed", async ({ page }) => {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")

  await page.route("**/auth/session", (route) => route.fulfill({
    json: { authenticated: true, oauth_enabled: true, login: "alice", role: "user" },
  }))
  await page.route("**/sandbox/regions", (route) => route.fulfill({ json: [] }))
  await page.route("**/user/**", async (route) => {
    const url = new URL(route.request().url())
    if (url.pathname === "/user/github-app") {
      await route.fulfill({ json: { setup_url: "/github-app/setup", settings_manageability: true, installations: [] } })
    } else if (url.pathname === "/user/preferences") {
      await route.fulfill({ status: 500, body: "" })
    } else if (url.pathname === "/user/onboarding/product-tour") {
      await route.fulfill({ json: { version: 1, status: "completed", tour_seen: true } })
    } else {
      await route.fulfill({ status: 404, body: "fixture route not found" })
    }
  })

  await page.goto("/account/preferences", { waitUntil: "domcontentloaded" })
  await expect(page.getByText("Could not load preferences. Try again.")).toBeVisible()
  await expect(page.getByRole("heading", { name: "alice", exact: true })).toBeVisible()
})

async function routeLocalAnonymousSession(page: Page) {
  const authSessionRoute = getLocalAuthSessionRoute(process.env.RUNNERD_UI_SMOKE_BASE_URL)
  if (!authSessionRoute) return

  await page.route(authSessionRoute.pattern, async (route) => {
    await route.fulfill({ json: authSessionRoute.json })
  })
}

function observeBrowserDiagnostics(page: Page) {
  const consoleErrors: string[] = []
  const pageErrors: string[] = []
  const failedAssets: string[] = []

  page.on("console", (message) => {
    if (message.type() === "error") {
      consoleErrors.push(message.text())
    }
  })
  page.on("pageerror", (error) => {
    pageErrors.push(error.message)
  })
  page.on("requestfailed", (request) => {
    if (["script", "stylesheet"].includes(request.resourceType())) {
      failedAssets.push(`${request.method()} ${request.url()}: ${request.failure()?.errorText ?? "failed"}`)
    }
  })
  page.on("response", (response) => {
    if (
      response.status() >= 400 &&
      ["script", "stylesheet"].includes(response.request().resourceType())
    ) {
      failedAssets.push(`${response.status()} ${response.url()}`)
    }
  })

  return {
    expectClean() {
      expect(pageErrors, "page errors").toEqual([])
      expect(consoleErrors, "console errors").toEqual([])
      expect(failedAssets, "failed script or stylesheet requests").toEqual([])
    },
  }
}

function fixtureRunners(count: number): RunnerState[] {
  return Array.from({ length: count }, (_, index) => {
    const createdAt = new Date(Date.UTC(2026, 7, 12, 0, 0, count - index)).toISOString()
    const id = `fixture-job-${index}`
    return {
      id,
      status: index === 0 ? "running" : "completed",
      repository_full_name: `fixture/repository-${index}`,
      requested_labels: ["self-hosted", "e2b"],
      runner_spec_name: "fixture-spec",
      runner_name: `fixture-runner-${index}`,
      sandbox_id: index === 0 ? "fixture-sandbox" : undefined,
      workflow_job_id: 10_000 + index,
      workflow_run_id: 20_000 + index,
      workflow_name: "Fixture workflow",
      head_branch: `fixture-branch-${index}`,
      head_sha: index.toString(16).padStart(40, "0"),
      assigned_job_name: `Fixture job ${index}`,
      github_job_url: `https://github.com/fixture/repository-${index}/actions/runs/${20_000 + index}/job/${10_000 + index}`,
      created_at: createdAt,
      updated_at: createdAt,
      running_at: index === 0 ? createdAt : undefined,
      completed_at: index === 0 ? undefined : createdAt,
    }
  })
}
