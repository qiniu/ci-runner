import { expect, test, type Page, type Route } from "@playwright/test"

const duration = { seconds: 3600, samples: 1, eligible: 2 }
const totals = {
  requests: 4,
  started: 3,
  rejected: 1,
  terminal: 3,
  active_accounts: 1,
  active_repositories: 1,
  mapped_accounts: 3,
  known_specs: 3,
  creating_times: 3,
  stopping_times: 1,
  terminal_times: 2,
  job_results: 1,
  job_result_eligible: 3,
  runtime: duration,
  startup: duration,
  creation: duration,
  cleanup: duration,
  completion: duration,
}
const query = { from: "2026-11-01", to: "2026-11-21", timezone: "Asia/Singapore", grain: "day" }
const search = new URLSearchParams(query).toString()
const report = {
  query,
  snapshot: "fixture-snapshot",
  observed_at: "2026-11-20T10:00:00Z",
  expires_at: "2026-11-20T10:05:00Z",
  window: {
    start: "2026-11-01T00:00:00+08:00",
    end: "2026-11-20T18:00:00+08:00",
    previous_start: "2026-10-12T00:00:00+08:00",
    previous_end: "2026-10-31T18:00:00+08:00",
  },
  totals,
  previous: { ...totals, requests: 0 },
  trend: [
    { ...totals, date: "2026-11-01" },
    { ...totals, requests: 0, date: "2026-11-02" },
  ],
}
const longName = "fixture-organization-with-a-very-long-name/repository-with-a-very-long-name"
const runner = {
  id: "request-1",
  runner_name: "request-1",
  status: "completed",
  repository_full_name: longName,
  runner_spec_name: "standard",
  created_at: report.observed_at,
  updated_at: report.observed_at,
}

async function setup(page: Page, language = "en") {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixtures only")
  await page.addInitScript(language => localStorage.setItem("runnerd:language", language), language)
  await page.route("**/auth/session", (route) =>
    route.fulfill({ json: { authenticated: true, role: "admin", login: "fixture-admin", oauth_enabled: true } }),
  )
  await page.route("**/reports/overview?*", (route) => route.fulfill({ json: report }))
  await page.route("**/reports/active?*", (route) =>
    route.fulfill({
      json: { counts: { queued: 2, creating: 1, running: 3, stopping: 0 }, observed_at: report.observed_at },
    }),
  )
  await page.route("**/reports/*/groups?*", (route) =>
    route.fulfill({
      json: { items: [{ ...totals, key: longName, label: longName }], total: 1, offset: 0, snapshot: report.snapshot },
    }),
  )
  await page.route("**/reports/*/requests?*", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: runner.id,
            queued_at: report.observed_at,
            repository: longName,
            owner_label: "fixture",
            spec_label: "standard · global",
            status: "completed",
            job_result: "failure",
            runtime_seconds: 3600,
          },
        ],
        total: 1,
        offset: 0,
        snapshot: report.snapshot,
      },
    }),
  )
  await page.route("**/runner_requests/request-1/diagnostics", (route) =>
    route.fulfill({ json: { state: runner, github_job: { lookup_status: "unavailable" }, findings: [], events: [] } }),
  )
  await page.route("**/runner_requests/request-1/events*", (route) =>
    route.fulfill({ json: { events: [], has_more: false } }),
  )
}

test("report filters, snapshot drilldown and return survive reload", async ({ page }) => {
  await setup(page)
  await page.goto(`/admin/reports?${search}`)
  await expect(page.getByRole("heading", { name: "Usage reports" })).toBeVisible()
  await expect(page.getByText("New · vs. previous period")).toBeVisible()
  await expect(page.getByLabel("Start date", { exact: true })).toHaveValue(query.from)
  await page.getByRole("button", { name: longName, exact: true }).first().click()
  await expect(page).toHaveURL(/snapshot=fixture-snapshot/)
  await expect(page).toHaveURL(/dimension=spec/)
  await page.getByRole("link", { name: "request-1", exact: true }).click()
  await expect(page.getByRole("button", { name: "Back to reports" })).toBeVisible()
  await page.getByRole("button", { name: "Back to reports" }).click()
  await expect(page).toHaveURL(/dimension=spec/)
  await expect(page.getByRole("link", { name: "request-1", exact: true })).toBeVisible()
  await page.reload()
  await expect(page.getByLabel("Timezone", { exact: true })).toHaveValue(query.timezone)
  await expect(page.getByRole("link", { name: "request-1", exact: true })).toBeVisible()
})

test("backlog loads independently and obsolete reports cannot replace new filters", async ({ page }) => {
  await setup(page)
  const pending: Route[] = []
  await page.route("**/reports/overview?*", (route) => {
    pending.push(route)
  })
  await page.goto(`/admin/reports?${search}`)
  await expect.poll(() => pending.length).toBe(1)
  await expect(page.getByText("Running", { exact: true })).toBeVisible()
  await expect(page.getByRole("heading", { name: "Trend", exact: true })).toHaveCount(0)
  await page.getByLabel("Repository", { exact: true }).fill("new/repo")
  await page.getByRole("button", { name: "Apply filters" }).click()
  await expect.poll(() => pending.length).toBe(2)
  await pending[1].fulfill({
    json: { ...report, query: { ...query, repository: "new/repo" }, totals: { ...totals, requests: 987 } },
  })
  await expect(page.getByText("987", { exact: true })).toBeVisible()
  await pending[0].fulfill({ json: report })
  await expect(page.getByText("987", { exact: true })).toBeVisible()
})

test("desktop and narrow reports, popup, empty and error states remain usable", async ({ page }, testInfo) => {
  await setup(page)
  await page.setViewportSize({ width: 1440, height: 1000 })
  await page.goto(`/admin/reports?${search}`)
  await expect(page.getByRole("link", { name: "request-1", exact: true })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath("reports-desktop.png"), fullPage: true })
  await page.getByRole("heading", { name: "Usage distribution" }).scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath("reports-desktop-distribution.png"), animations: "disabled" })
  await page.getByRole("heading", { name: "Trend", exact: true }).scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath("reports-desktop-trend.png"), animations: "disabled" })
  await page.getByRole("heading", { name: "Usage reports" }).scrollIntoViewIfNeeded()
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByRole("heading", { name: "Usage reports" })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.getByRole("combobox", { name: "Time grain", exact: true }).click()
  await expect(page.getByRole("option", { name: "Week", exact: true })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath("reports-narrow-select.png"), fullPage: true, animations: "disabled" })
  await page.keyboard.press("ArrowDown")
  await page.keyboard.press("Enter")
  await page.screenshot({ path: testInfo.outputPath("reports-narrow.png"), fullPage: true })
  await page.getByRole("heading", { name: "Usage distribution" }).scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath("reports-narrow-distribution.png"), animations: "disabled" })
  await page.route("**/reports/*/requests?*", (route) =>
    route.fulfill({ json: { items: [], total: 0, offset: 0, snapshot: report.snapshot } }),
  )
  await page.route("**/reports/*/groups?*", (route) =>
    route.fulfill({ status: 410, json: { error: "report snapshot expired; refresh the report" } }),
  )
  await page.reload()
  await expect(page.getByText("No matching requests.", { exact: true })).toBeVisible()
  await expect(page.getByRole("alert")).toHaveCount(3)
  await page.getByRole("heading", { name: "Usage distribution" }).scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath("reports-error-empty.png"), fullPage: true })
  await page.route("**/reports/overview?*", (route) =>
    route.fulfill({ status: 410, json: { error: "report snapshot expired; refresh the report" } }),
  )
  await page.reload()
  await expect(page.getByRole("alert")).toHaveCount(1)
  await expect(page.getByText("Running", { exact: true })).toBeVisible()
})

test("timezone-only URLs keep normalized dates when applying filters", async ({ page }) => {
  await setup(page)
  await page.clock.setFixedTime(new Date("2026-11-20T02:00:00Z"))
  const normalized = { from: "2026-10-21", to: "2026-11-20", timezone: "America/New_York", grain: "day" }
  const queries: URLSearchParams[] = []
  await page.route("**/reports/overview?*", route => {
    queries.push(new URL(route.request().url()).searchParams)
    return route.fulfill({ json: { ...report, query: normalized } })
  })
  await page.goto("/admin/reports?timezone=America%2FNew_York")
  await expect(page.getByLabel("Start date", { exact: true })).toHaveValue(normalized.from)
  await expect(page.getByLabel("End date (exclusive)", { exact: true })).toHaveValue(normalized.to)
  await page.getByRole("button", { name: "Apply filters", exact: true }).click()
  await expect.poll(() => queries.at(-1)?.get("to")).toBe(normalized.to)
  expect(queries.at(-1)?.get("from")).toBe(normalized.from)
})

test("Chinese reports localize statuses and both observation labels independently of browser locale", async ({ page }) => {
  await setup(page, "zh")
  await page.route("**/reports/*/requests?*", route => route.fulfill({ json: {
    items: ["completed", "failed", "legacy_state"].map(status => ({ id: `request-${status}`, repository: longName, spec_label: "standard", status, job_result: "unknown", runtime_seconds: null })),
    total: 3, offset: 0, snapshot: report.snapshot,
  } }))
  await page.goto(`/admin/reports?${search}`)
  await expect(page.getByRole("heading", { name: "用量报表", exact: true })).toBeVisible()
  await expect(page.getByRole("cell", { name: "已完成", exact: true })).toBeVisible()
  await expect(page.getByRole("cell", { name: "失败", exact: true })).toBeVisible()
  await expect(page.getByRole("cell", { name: "legacy_state", exact: true })).toBeVisible()
  const expected = await page.evaluate(timestamp => new Date(timestamp).toLocaleString("zh"), report.observed_at)
  await expect(page.getByText(`统计时点：${expected}`, { exact: true })).toHaveCount(2)
})
