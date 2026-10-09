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
    if (url.pathname.endsWith("/events")) {
      await route.fulfill({ json: fixtureRunnerEvents() })
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

test("reveals Jobs with Runner logs and a GitHub link before secondary metadata", async ({ page }) => {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")

  const diagnostics = observeBrowserDiagnostics(page)
  const runners = fixtureRunners(1)
  let releaseJobs = () => {}
  let releaseGitHubApp = () => {}
  const jobsGate = new Promise<void>((resolve) => { releaseJobs = resolve })
  const githubAppGate = new Promise<void>((resolve) => { releaseGitHubApp = resolve })
  let runnerLogRequests = 0
  let groupRequests = 0
  const githubLogRequests: string[] = []
  page.on("request", (request) => {
    if (new URL(request.url()).pathname.endsWith("/github-log")) githubLogRequests.push(request.url())
  })

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
    } else if (url.pathname.endsWith("/events")) {
      runnerLogRequests += 1
      await route.fulfill({ json: fixtureRunnerEvents() })
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
    await expect(page.getByRole("tab", { name: /Runner logs|Runner 日志/i })).toHaveAttribute("aria-selected", "true")
    await expect(page.getByRole("tab", { name: /GitHub logs|GitHub 日志/i })).toHaveCount(0)
    await expect(page.getByText("fixture Runner log")).toBeVisible()
    const timeline = page.getByRole("region", { name: /Run history|运行记录/, exact: true })
    await expect(timeline.getByRole("tab", { name: /All|全部/, exact: true })).toHaveAttribute("aria-selected", "true")
    await expect(timeline.locator("[data-runner-event-id]")).toHaveCount(3)
    await expect(timeline.getByText("fixture Runner stdout", { exact: true })).toBeVisible()
    await expect(timeline.getByText("fixture Runner stderr", { exact: true })).toBeVisible()
    await expect(timeline.getByText("control · runner_hook", { exact: true })).toBeVisible()
    await timeline.getByRole("tab", { name: "stderr", exact: true }).click()
    await expect(timeline.locator("[data-runner-event-id]")).toHaveCount(1)
    await expect(timeline.getByText("fixture Runner stderr", { exact: true })).toBeVisible()
    await timeline.getByRole("tab", { name: /All|全部/, exact: true }).click()
    await expect(timeline.locator("[data-runner-event-id]")).toHaveCount(3)
    const githubLink = page.locator(`a[href="${runners[0].github_job_url}"]`)
    await expect(githubLink).toHaveCount(1)
    await expect(githubLink).toHaveText(runners[0].assigned_job_name!)
    await expect(githubLink).toHaveAttribute("href", runners[0].github_job_url!)
    await expect(githubLink).toHaveAttribute("target", "_blank")
    await expect(githubLink).toHaveAttribute("title", /View job on GitHub|在 GitHub 查看 Job/)
    const workflowLink = page.getByRole("link", { name: runners[0].workflow_name!, exact: true })
    await expect(workflowLink).toHaveAttribute("href", runners[0].github_job_url!.split("/job/")[0])
    await expect(workflowLink).toHaveAttribute("title", /View workflow run on GitHub|在 GitHub 查看 Workflow Run/)
    await expect.poll(() => groupRequests).toBe(1)
    await page.screenshot({ path: test.info().outputPath("job-github-link.png"), fullPage: true })
    expect(runnerLogRequests).toBe(1)
    await page.getByRole("button", { name: /fixture\/repository-0/ }).click()
    expect(groupRequests).toBe(1)
    expect(githubLogRequests).toEqual([])
    diagnostics.expectClean()
  } finally {
    releaseJobs()
    releaseGitHubApp()
  }
})

test("uses Runner logs and GitHub navigation in the standalone Job fallback", async ({ page }) => {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")
  const diagnostics = observeBrowserDiagnostics(page)
  let selected = fixtureRunners(1)[0]
  const requests: string[] = []
  let jobReads = 0
  await page.route("**/auth/session", (route) => route.fulfill({
    json: { authenticated: true, oauth_enabled: true, login: "fixture-user", role: "user" },
  }))
  await page.route("**/user/**", async (route) => {
    const path = new URL(route.request().url()).pathname
    requests.push(path)
    if (path === `/user/runner_requests/${selected.id}`) {
      jobReads += 1
      if (jobReads >= 3) selected = { ...selected, status: "completed" }
      await route.fulfill({ json: selected })
    } else if (path.endsWith("/group")) {
      await route.fulfill({ json: {
        key: "", group: "manual", repository: selected.repository_full_name,
        jobs: [selected], current_jobs: [selected], previous_jobs: [], workflow_run_ids: [],
      } })
    } else if (path.endsWith("/events")) {
      const eventPage = fixtureRunnerEvents()
      if (selected.status === "completed") eventPage.events.push({
        id: 4, event_type: "control_log", stage: "runner_cleanup", message: "fixture final cleanup", created_at: "2026-09-29T03:51:03.999Z",
      })
      await route.fulfill({ json: eventPage })
    } else if (path === "/user/github-app") {
      await route.fulfill({ json: { setup_url: "/github-app/setup", installations: [] } })
    } else if (path === "/user/onboarding/product-tour") {
      await route.fulfill({ json: { version: 1, status: "completed", tour_seen: true } })
    } else {
      await route.fulfill({ status: 404, body: "fixture route not found" })
    }
  })
  const original = selected
  for (const variant of ["job", "run", "none"]) {
    selected = { ...original,
      github_job_url: variant === "job" ? original.github_job_url : undefined,
      workflow_run_id: variant === "none" ? undefined : original.workflow_run_id,
    }
    await page.goto(`/jobs/${selected.id}`, { waitUntil: "networkidle" })
    await expect(page.getByRole("tab", { name: /Runner logs|Runner 日志/i })).toHaveAttribute("aria-selected", "true")
    await expect(page.getByText("fixture Runner log")).toBeVisible()
    await expect(page.getByRole("tab", { name: /GitHub logs|GitHub 日志/i })).toHaveCount(0)
    const link = page.getByRole("link", { name: /View job on GitHub|View workflow run on GitHub|在 GitHub 查看/ })
    if (variant === "none") {
      await expect(link).toHaveCount(0)
    } else {
      await expect(link).toHaveAttribute("href", variant === "job" ? original.github_job_url! : "https://github.com/fixture/repository-0/actions/runs/20000")
      await expect(link).toHaveAttribute("target", "_blank")
    }
    if (variant === "job") {
      await expect(page.locator("header").getByText(/Completed|已完成/, { exact: true })).toBeVisible({ timeout: 15000 })
      await expect(page.getByText("fixture final cleanup", { exact: true })).toBeVisible()
      await expect(page.locator("[data-runner-event-id]")).toHaveCount(4)
    }
    diagnostics.expectClean()
  }
  expect(requests.filter((path) => path.endsWith("/github-log"))).toEqual([])
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
    } else if (url.pathname.endsWith("/events")) {
      await route.fulfill({ json: fixtureRunnerEvents() })
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
    } else if (url.pathname.endsWith("/events")) {
      await route.fulfill({ json: fixtureRunnerEvents() })
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

test("filters the platform spec directory to enabled published public entries", async ({ page }) => {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")
  const diagnostics = observeBrowserDiagnostics(page)
  await page.route("**/auth/session", (route) => route.fulfill({ json: { authenticated: true, oauth_enabled: true, login: "fixture-user", role: "user" } }))
  await page.route("**/user/**", (route) => {
    if (new URL(route.request().url()).pathname === "/user/runner-specs") {
      const base = { workflow_labels: ["qiniu", "ubuntu"], enabled: true, max_concurrency: 4, overrides_global: false, updated_at: "2026-10-01T00:00:00Z" }
      return route.fulfill({ json: { items: [
        { ...base, name: "visible-public", source: "platform_public", published: true, default_template_name: "public-template" },
        { ...base, name: "unpublished-public", source: "platform_public", published: false },
        { ...base, name: "disabled-public", source: "platform_public", published: true, enabled: false },
        { ...base, name: "private-business", source: "platform_custom", published: true, template_id: "private-id" },
        { ...base, name: "scope-custom", source: "scoped_custom", template_id: "scope-id" },
      ] } })
    }
    return route.fulfill({ json: { version: 1, status: "completed", tour_seen: true } })
  })
  await page.route("**/sandbox/regions", (route) => route.fulfill({ json: [] }))
  await page.goto("/runner-specs", { waitUntil: "networkidle" })
  await expect(page.getByText("visible-public", { exact: true })).toBeVisible()
  for (const name of ["unpublished-public", "disabled-public", "private-business", "scope-custom", "private-id", "scope-id"]) {
    await expect(page.getByText(name, { exact: true })).toHaveCount(0)
  }
  diagnostics.expectClean()
})

test("edits former managed specs and clears publication when switching to private binding", async ({ page }) => {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")
  const diagnostics = observeBrowserDiagnostics(page)
  let profile = { name: "old-managed", labels: ["qiniu", "ubuntu"], required_labels: ["qiniu", "ubuntu"], template_source: "public", default_template_name: "public-template", template_id: "", published: true, managed_by: "qiniu/ci-runner", enabled: true, max_concurrency: 7, min_idle: 0, priority: 1, runner_group: "", updated_at: "2026-10-01T00:00:00Z" }
  let saved: Record<string, unknown> | undefined
  await page.route("**/auth/session", (route) => route.fulfill({ json: { authenticated: true, oauth_enabled: true, login: "fixture-admin", role: "admin" } }))
  await page.route("**/runner_specs**", (route) => {
    const url = new URL(route.request().url())
    if (url.pathname.startsWith("/admin/")) return route.continue()
    if (route.request().method() === "PATCH") {
      saved = route.request().postDataJSON()
      profile = { ...profile, ...saved }
      return route.fulfill({ json: profile })
    }
    return route.fulfill({ json: [profile] })
  })
  await page.goto("/admin/runner_specs", { waitUntil: "networkidle" })
  await page.setViewportSize({ width: 1485, height: 1000 })
  await page.getByRole("button", { name: "Edit old-managed", exact: true }).click()
  await expect(page.locator("#runner-spec-labels")).toBeEnabled()
  await expect(page.locator("#runner-spec-default-template")).toBeEnabled()
  await expect(page.locator("#runner-spec-update-policy, #runner-spec-require_docker, #runner-spec-fork_sponsorship")).toHaveCount(0)
  const longLabels = `self-hosted,linux,x64,qiniu,ubuntu-24.04,${"long-label-".repeat(12)}`
  const longTemplate = `github-runner-ubuntu-24-04-${"extended-".repeat(12)}`
  await page.locator("#runner-spec-labels").fill(longLabels)
  await page.locator("#runner-spec-default-template").fill(longTemplate)
  const assertWrappedFields = async () => {
    await expect.poll(() => page.locator("#runner-spec-labels, #runner-spec-default-template").evaluateAll((fields) => fields.every((field) => field.scrollWidth <= field.clientWidth + 1 && field.scrollHeight <= field.clientHeight + 1))).toBe(true)
    expect(await page.getByRole("dialog").evaluate((dialog) => dialog.scrollWidth <= dialog.clientWidth + 1)).toBe(true)
  }
  await assertWrappedFields()
  expect(await page.getByRole("dialog").evaluate((dialog) => dialog.getBoundingClientRect().width)).toBeGreaterThan(700)
  await page.setViewportSize({ width: 390, height: 844 })
  await assertWrappedFields()
  await expect(page.locator("#runner-spec-labels")).toHaveValue(longLabels)
  await expect(page.locator("#runner-spec-default-template")).toHaveValue(longTemplate)
  await page.locator("#runner-spec-labels").fill("qiniu,ubuntu,new-label")
  await page.locator("#runner-spec-template-source").selectOption("private")
  await expect(page.locator("#runner-spec-published")).not.toBeChecked()
  await expect(page.locator("#runner-spec-published")).toBeDisabled()
  await page.locator("#runner-spec-template-id").fill("private-template")
  await page.getByRole("button", { name: "Save runner spec", exact: true }).click()
  await expect(page.getByRole("dialog")).toHaveCount(0)
  expect(saved).toMatchObject({ template_source: "private", template_id: "private-template", default_template_name: "", published: false, expected_updated_at: "2026-10-01T00:00:00Z", labels: ["qiniu", "ubuntu", "new-label"] })
  await expect(page.getByText("private-template", { exact: true })).toBeVisible()
  diagnostics.expectClean()
})

test("requires confirmation before deleting an admin Runner Spec", async ({ page }) => {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "Local fixture-backed admin coverage")
  const diagnostics = observeBrowserDiagnostics(page)
  const base = { labels: ["qiniu", "ubuntu"], required_labels: ["qiniu", "ubuntu"], template_source: "public", default_template_name: "public-template", template_id: "", published: true, enabled: true, max_concurrency: 7, min_idle: 0, priority: 1, runner_group: "", updated_at: "2026-10-01T00:00:00Z" }
  let profiles = [{ ...base, name: "delete-me" }, { ...base, name: "keep-me" }]
  const deletions: string[] = []
  await page.route("**/auth/session", (route) => route.fulfill({ json: { authenticated: true, oauth_enabled: true, login: "fixture-admin", role: "admin" } }))
  await page.route("**/runner_specs**", (route) => {
    const url = new URL(route.request().url())
    if (url.pathname.startsWith("/admin/")) return route.continue()
    if (route.request().method() === "DELETE") {
      deletions.push(url.pathname)
      profiles = profiles.filter((profile) => profile.name !== "delete-me")
      return route.fulfill({ json: {} })
    }
    return route.fulfill({ json: profiles })
  })
  await page.goto("/admin/runner_specs", { waitUntil: "networkidle" })
  const remove = page.getByRole("button", { name: "Delete delete-me", exact: true })
  await expect(remove).toHaveText("")
  await expect(page.getByRole("button", { name: "Edit delete-me", exact: true })).toHaveText("")
  await remove.click()
  const confirmation = page.getByRole("dialog", { name: "Delete Runner Spec", exact: true })
  await expect(confirmation).toContainText('Delete Runner Spec "delete-me"?')
  expect(deletions).toEqual([])
  await confirmation.getByRole("button", { name: "Cancel", exact: true }).click()
  await expect(confirmation).toHaveCount(0)
  expect(deletions).toEqual([])
  await remove.click()
  await page.keyboard.press("Escape")
  await expect(confirmation).toHaveCount(0)
  expect(deletions).toEqual([])
  await remove.click()
  await confirmation.getByRole("button", { name: "Delete", exact: true }).click()
  await expect(remove).toHaveCount(0)
  await expect(confirmation).toHaveCount(0)
  expect(deletions).toEqual(["/runner_specs/delete-me"])
  await expect(page.getByRole("button", { name: "Delete keep-me", exact: true })).toBeVisible()
  diagnostics.expectClean()
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

function fixtureRunnerEvents() {
  return {
    events: [
      { id: 1, event_type: "control_log", stage: "runner_hook", message: "fixture Runner log", created_at: "2026-09-29T03:51:00.123Z" },
      { id: 2, event_type: "stdout_log", message: "fixture Runner stdout", created_at: "2026-09-29T03:51:01.456Z" },
      { id: 3, event_type: "stderr_log", message: "fixture Runner stderr", created_at: "2026-09-29T03:51:02.789Z" },
    ],
    has_more: false,
  }
}
