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
async function setup(page: Page, language = "en") {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixtures only")
  await page.addInitScript((language) => localStorage.setItem("runnerd:language", language), language)
  await page.route("**/auth/session", (route) =>
    route.fulfill({
      json: { authenticated: true, role: "admin", login: "fixture-admin", oauth_enabled: true },
    }),
  )
  await page.route("**/reports/overview?*", (route) => route.fulfill({ json: report }))
  await page.route("**/reports/*/groups?*", (route) =>
    route.fulfill({
      json: {
        items: [{ ...totals, key: longName, label: longName }],
        total: 1,
        offset: 0,
        snapshot: report.snapshot,
      },
    }),
  )
}

test("chart filtering and snapshot metric survive reload without fetching request details", async ({
  page,
}) => {
  await setup(page)
  const detailRequests: string[] = []
  page.on("request", (request) => {
    if (/\/reports\/(?:active|[^/]+\/requests)/.test(request.url())) detailRequests.push(request.url())
  })
  await page.goto(`/admin/reports?${search}&offset=20&dimension=spec`)
  await expect(page.getByRole("heading", { name: "Usage reports" })).toBeVisible()
  await expect(page.getByText("New · vs. previous period")).toBeVisible()
  await expect(page.getByText("Data coverage and valid samples", { exact: true })).toHaveCount(0)
  await expect(page.getByRole("heading", { name: "Current backlog", exact: true })).toHaveCount(0)
  await expect(page.getByText("Based on 1 valid record", { exact: true })).toBeVisible()
  await page.getByRole("button", { name: "Runtime calculation", exact: true }).focus()
  await expect(page.getByRole("tooltip")).toContainText("Runtime includes valid terminal requests only")
  await page.keyboard.press("Escape")
  await expect(page.getByRole("img", { name: /chart$/ })).toHaveCount(3)
  await expect(page.getByRole("heading", { name: "Request drilldown" })).toHaveCount(0)
  await page.getByRole("combobox", { name: "Distribution metric" }).click()
  await page.getByRole("option", { name: "Recorded runtime (h)", exact: true }).click()
  await expect(page).toHaveURL(/snapshot=fixture-snapshot/)
  await expect(page).toHaveURL(/sort=runtime/)
  await page.reload()
  await expect(page.getByRole("combobox", { name: "Distribution metric" })).toHaveText("Recorded runtime (h)")
  await expect(page.getByLabel("Timezone", { exact: true })).toHaveValue(query.timezone)
  await page
    .getByRole("button", { name: `Filter by ${longName}`, exact: true })
    .first()
    .click()
  await expect(page).toHaveURL(/spec=/)
  await expect(page).toHaveURL(/sort=runtime/)
  await expect(page).not.toHaveURL(/snapshot=/)
  expect(detailRequests).toEqual([])
})

test("obsolete reports cannot replace new filters", async ({ page }) => {
  await setup(page)
  const pending: Route[] = []
  await page.route("**/reports/overview?*", (route) => {
    pending.push(route)
  })
  await page.goto(`/admin/reports?${search}`)
  await expect.poll(() => pending.length).toBe(1)
  await expect(page.getByRole("heading", { name: "Usage reports", exact: true })).toBeVisible()
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

test("desktop and narrow reports, popup, empty and error states remain usable", async ({
  page,
}, testInfo) => {
  await setup(page)
  const chartTotals = {
    ...totals,
    requests: 1200,
    runtime: { seconds: 720000, samples: 1000, eligible: 1100 },
  }
  await page.route("**/reports/overview?*", (route) =>
    route.fulfill({
      json: {
        ...report,
        totals: chartTotals,
        trend: [42, 67, 55, 89, 120, 106, 78, 95, 145, 112, 150, 100, 75, 90].map((requests, index) => ({
          ...totals,
          requests,
          runtime: {
            seconds:
              index === 4
                ? 0
                : [1260, 2880, 2520, 720, 0, 3240, 5400, 6120, 4320, 3600, 5400, 7200, 5760, 6840][index],
            samples: index === 4 ? 0 : 1,
            eligible: 1,
          },
          date: `2026-11-${String(index + 1).padStart(2, "0")}`,
        })),
      },
    }),
  )
  await page.route("**/reports/*/groups?*", (route) => {
    const dimension = new URL(route.request().url()).searchParams.get("dimension")
    const names =
      dimension === "spec"
        ? [
            "ubuntu-24.04 · global",
            "ubuntu-22.04 · global",
            "windows-2025 · global",
            "large · account:7",
            "Unknown",
          ]
        : dimension === "owner"
          ? [
              "platform-team",
              "release-engineering",
              "application-team",
              "tools-team",
              "external-contributors",
            ]
          : ["platform/api", "platform/runner", "apps/web", longName, "tools/release"]
    return route.fulfill({
      json: {
        items: [600, 240, 120, 80, 60].map((requests, index) => ({
          ...totals,
          requests,
          key: String(index),
          label: names[index],
        })),
        total: 8,
        offset: 0,
        snapshot: report.snapshot,
      },
    })
  })
  await page.setViewportSize({ width: 1603, height: 1565 })
  await page.goto(`/admin/reports?${search}`)
  await expect(page.getByRole("img", { name: /chart$/ })).toHaveCount(3)
  const applyBottom = await page.getByRole("button", { name: "Apply filters" })
    .evaluate((button) => button.getBoundingClientRect().bottom)
  for (const bottom of await page.locator("form input:visible, form button[role=combobox]")
    .evaluateAll((fields) => fields.map((field) => field.getBoundingClientRect().bottom))) {
    expect(Math.abs(bottom - applyBottom)).toBeLessThanOrEqual(1)
  }
  await page
    .locator("section[aria-labelledby=reports-title]")
    .screenshot({ path: testInfo.outputPath("reports-desktop.png"), animations: "disabled" })
  await page.getByRole("heading", { name: "Usage distribution" }).scrollIntoViewIfNeeded()
  await page.screenshot({
    path: testInfo.outputPath("reports-desktop-distribution.png"),
    animations: "disabled",
  })
  const donut = page.getByRole("img", { name: "Runner spec · Requests chart", exact: true })
  await expect(donut.locator("svg path").first()).toBeVisible()
  const donutSize = await donut.boundingBox()
  await donut.hover({
    position: { x: donutSize!.width / 2 + donutSize!.height * 0.32, y: donutSize!.height / 2 },
  })
  await expect(donut.locator("svg")).toContainText("ubuntu-24.04 · global")
  await page.screenshot({ path: testInfo.outputPath("reports-chart-tooltip.png"), animations: "disabled" })
  await page
    .getByRole("img", { name: "Repository · Requests chart", exact: true })
    .locator("..")
    .screenshot({ path: testInfo.outputPath("reports-repository-bars.png"), animations: "disabled" })
  await page.getByRole("heading", { name: "Trend", exact: true }).scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath("reports-desktop-trend.png"), animations: "disabled" })
  await page.getByRole("heading", { name: "Usage reports" }).scrollIntoViewIfNeeded()
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(page.getByRole("heading", { name: "Usage reports" })).toBeVisible()
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.getByRole("combobox", { name: "Time grain", exact: true }).click()
  await expect(page.getByRole("option", { name: "Week", exact: true })).toBeVisible()
  await page.screenshot({
    path: testInfo.outputPath("reports-narrow-select.png"),
    fullPage: true,
    animations: "disabled",
  })
  await page.keyboard.press("ArrowDown")
  await page.keyboard.press("Enter")
  await page.screenshot({ path: testInfo.outputPath("reports-narrow.png"), fullPage: true })
  await page.getByRole("button", { name: "Runtime calculation", exact: true }).scrollIntoViewIfNeeded()
  await page.mouse.move(0, 0)
  await page.getByRole("button", { name: "Runtime calculation", exact: true }).focus()
  await expect(page.getByRole("tooltip")).toBeVisible()
  await page.screenshot({
    path: testInfo.outputPath("reports-runtime-help-narrow.png"),
    animations: "disabled",
  })
  await page.keyboard.press("Escape")
  await page.getByRole("heading", { name: "Usage distribution" }).scrollIntoViewIfNeeded()
  await page.screenshot({
    path: testInfo.outputPath("reports-narrow-distribution.png"),
    animations: "disabled",
  })
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
  await page.evaluate(() => localStorage.setItem("theme", "dark"))
  await page.reload()
  await page.getByRole("heading", { name: "Usage distribution" }).scrollIntoViewIfNeeded()
  await expect(
    page.getByRole("img", { name: "Runner spec · Requests chart", exact: true }).locator("svg path").first(),
  ).toBeVisible()
  await page
    .getByRole("img", { name: "Runner spec · Requests chart", exact: true })
    .locator("..")
    .screenshot({ path: testInfo.outputPath("reports-narrow-dark.png"), animations: "disabled" })
  await page.route("**/reports/*/groups?*", (route) =>
    new URL(route.request().url()).searchParams.get("dimension") === "spec"
      ? route.fulfill({ json: { items: [], total: 0, offset: 0, snapshot: report.snapshot } })
      : route.fulfill({ status: 410, json: { error: "report snapshot expired; refresh the report" } }),
  )
  await page.reload()
  await expect(page.getByText("No matching requests.", { exact: true })).toBeVisible()
  await expect(page.getByRole("alert")).toHaveCount(2)
  await expect(page.getByRole("img", { name: "Trend", exact: true }).locator("svg")).toBeVisible()
  await page.getByRole("heading", { name: "Usage distribution" }).scrollIntoViewIfNeeded()
  await page.screenshot({ path: testInfo.outputPath("reports-error-empty.png"), fullPage: true })
  await page.route("**/reports/overview?*", (route) =>
    route.fulfill({ status: 410, json: { error: "report snapshot expired; refresh the report" } }),
  )
  await page.reload()
  await expect(page.getByRole("alert")).toHaveCount(1)
  await expect(page.getByRole("heading", { name: "Usage reports", exact: true })).toBeVisible()
})

test("timezone-only URLs keep normalized dates when applying filters", async ({ page }) => {
  await setup(page)
  await page.clock.setFixedTime(new Date("2026-11-20T02:00:00Z"))
  const normalized = { from: "2026-10-21", to: "2026-11-20", timezone: "America/New_York", grain: "day" }
  const queries: URLSearchParams[] = []
  await page.route("**/reports/overview?*", (route) => {
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

test("Chinese charts label missing spec names and preserve localized observation times", async ({ page }) => {
  await setup(page, "zh")
  await page.route("**/reports/*/groups?*", (route) =>
    route.fulfill({
      json: {
        items: [{ ...totals, key: "unknown-spec", label: "" }],
        total: 1,
        offset: 0,
        snapshot: report.snapshot,
      },
    }),
  )
  await page.goto(`/admin/reports?${search}`)
  await expect(page.getByRole("heading", { name: "用量报表", exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "按 未知 筛选", exact: true }).first()).toBeVisible()
  const expected = await page.evaluate(
    (timestamp) => new Date(timestamp).toLocaleString("zh"),
    report.observed_at,
  )
  await expect(page.getByText(`统计时点：${expected}`, { exact: true })).toHaveCount(1)
})

test("charts show full-cohort Other and distinguish missing runtime from zero", async ({ page }) => {
  await setup(page)
  await page.route("**/reports/*/groups?*", (route) =>
    route.fulfill({
      json: {
        items: [
          {
            ...totals,
            requests: 1,
            runtime: { seconds: 0, samples: 0, eligible: 1 },
            key: "unknown",
            label: "Unknown",
          },
        ],
        total: 6,
        offset: 0,
        snapshot: report.snapshot,
      },
    }),
  )
  await page.goto(`/admin/reports?${search}`)
  const legend = page.getByRole("list", { name: "Runner spec chart values", exact: true })
  await expect(legend.getByRole("listitem").filter({ hasText: "Other" })).toContainText("3 · 75.0%")
  await page.getByRole("combobox", { name: "Distribution metric" }).click()
  await page.getByRole("option", { name: "Recorded runtime (h)", exact: true }).click()
  await expect(legend.getByRole("listitem").filter({ hasText: "Unknown" })).toContainText("— · —")
  await expect(legend.getByRole("listitem").filter({ hasText: "Other" })).toContainText("1 · 100.0%")
  await page.route("**/reports/overview?*", (route) =>
    route.fulfill({
      json: {
        ...report,
        totals: { ...totals, runtime: { seconds: 0, samples: 0, eligible: 2 } },
      },
    }),
  )
  await page.reload()
  await expect(
    page.getByText("No valid runtime samples. Missing duration is not zero usage.", { exact: true }),
  ).toHaveCount(3)
  await page.route("**/reports/overview?*", (route) =>
    route.fulfill({
      json: {
        ...report,
        totals: { ...totals, runtime: { seconds: 0, samples: 1, eligible: 2 } },
      },
    }),
  )
  await page.reload()
  await expect(
    page.getByText("Recorded runtime is zero; there is no duration share to plot.", { exact: true }),
  ).toHaveCount(3)
})

test("trend defaults to request counts and runtime on separate axes and retains single metrics", async ({
  page,
}) => {
  await setup(page)
  await page.route("**/reports/overview?*", (route) =>
    route.fulfill({
      json: {
        ...report,
        totals: { ...totals, requests: 12, runtime: { seconds: 3600, samples: 2, eligible: 3 } },
        trend: [
          { ...totals, date: "2026-11-01", runtime: { seconds: 0, samples: 1, eligible: 1 } },
          { ...totals, date: "2026-11-02", runtime: { seconds: 0, samples: 0, eligible: 1 } },
          { ...totals, date: "2026-11-03", runtime: { seconds: 3600, samples: 1, eligible: 1 } },
        ],
      },
    }),
  )
  await page.goto(`/admin/reports?${search}`)
  const chart = page.getByRole("img", { name: "Trend", exact: true })
  await expect(page.getByRole("combobox", { name: "Metric", exact: true })).toHaveText("Requests + runtime")
  await expect(chart.locator("svg")).toContainText("Hours")
  await expect(chart.locator("svg")).toContainText("Requests")
  const legend = page.getByLabel("Trend legend", { exact: true })
  await expect(legend).toContainText("Requests")
  await expect(legend).toContainText("Recorded runtime (h)")
  await page.getByText("View trend data table", { exact: true }).click()
  await expect(page.getByRole("row").filter({ hasText: "2026-11-01" }).getByRole("cell").last()).toHaveText(
    "0",
  )
  await expect(page.getByRole("row").filter({ hasText: "2026-11-02" }).getByRole("cell").last()).toHaveText(
    "—",
  )
  await expect(page.getByRole("row").filter({ hasText: "2026-11-03" }).getByRole("cell").last()).toHaveText(
    "1",
  )
  await page.getByRole("combobox", { name: "Metric", exact: true }).click()
  await page.getByRole("option", { name: "Started requests", exact: true }).click()
  await expect(legend).toHaveText("Started requests")
  await expect(chart.locator("svg")).not.toContainText("Hours")
  await page.getByRole("combobox", { name: "Metric", exact: true }).click()
  await page.getByRole("option", { name: "Recorded runtime (h)", exact: true }).click()
  await expect(legend).toHaveText("Recorded runtime (h)")
  await expect(chart.locator("svg")).toContainText("Hours")
})
