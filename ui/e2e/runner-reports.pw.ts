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

test("all distributions and both metrics work with a process-local snapshot", async ({ page }) => {
  await setup(page)
  let overviewReads = 0
  let groupReads = 0
  const distributions = Object.fromEntries(["spec", "owner", "repository"].map((dimension) => [
    dimension,
    {
      total: 1,
      requests: [{ ...totals, key: dimension, label: dimension + " requests" }],
      runtime: [{ ...totals, key: dimension, label: dimension + " runtime" }],
    },
  ]))
  await page.route("**/reports/overview?*", (route) => {
    overviewReads++
    return route.fulfill({ json: { ...report, distributions } })
  })
  await page.route("**/reports/*/groups?*", (route) => {
    groupReads++
    return route.fulfill({ status: 410, json: { error: "report snapshot expired" } })
  })
  await page.goto("/admin/reports")
  for (const dimension of Object.keys(distributions)) {
    await expect(page.getByRole("button", { name: "Filter by " + dimension + " requests", exact: true })).toBeVisible()
  }
  await page.getByRole("combobox", { name: "Distribution metric" }).click()
  await page.getByRole("option", { name: "Recorded runtime (h)", exact: true }).click()
  for (const dimension of Object.keys(distributions)) {
    await expect(page.getByRole("button", { name: "Filter by " + dimension + " runtime", exact: true })).toBeVisible()
  }
  await expect(page.getByRole("img", { name: /chart$/ })).toHaveCount(3)
  expect(overviewReads).toBe(1)
  expect(groupReads).toBe(0)
})

test("spec charts and table ignore legacy request drilldown links", async ({ page }) => {
  await setup(page)
  const calls: string[] = []
  page.on("request", request => calls.push(request.url()))
  const group = { ...totals, key: "standard", label: "qiniu-ubuntu-24.04 · global" }
  const ranking = { total: 1, requests: [group], runtime: [group] }
  const scope = { ...query, repository: "team/repo" }
  const repos = [
    { key:"org/main", label:"org/main", requests:1, runtime:{seconds:1800,samples:1,eligible:1} },
    { key:"org/ci", label:"org/ci", requests:1, runtime:{seconds:5400,samples:1,eligible:1} },
  ]
  const items = [
          {
            key: "standard",
            label: group.label,
            name: "standard",
            source: "global",
            current: true,
            enabled: true,
            published: true,
            requests: 2,
            started: 2,
            repositories: 1,
            runtime: { seconds: 7200, samples: 2, eligible: 2 },
            creation: { seconds: 120, samples: 2, eligible: 2 },
            average_runtime_seconds: 3600,
            p95_runtime_seconds: 3600,
            average_creation_seconds: 60,
            p95_creation_seconds: 60,
            job_results: { success: 2 },
            repository_mix: { requests: repos, runtime: [...repos].reverse() },
            owner_mix: { requests: repos, runtime: [...repos].reverse() },
            spec_trend: [{ ...repos[0], key: "2026-11-01", started: 1 }],
          },
          {
            key: "unused",
            label: "unused · global",
            name: "unused",
            source: "global",
            current: true,
            enabled: false,
            published: false,
            requests: 0,
            started: 0,
            repositories: 0,
            runtime: { seconds: 0, samples: 0, eligible: 0 },
            creation: { seconds: 0, samples: 0, eligible: 0 },
          },
        ]
  await page.route("**/reports/spec-usage?*", route => route.fulfill({json:{
    ...report, query:scope, distributions:{spec:ranking,owner:ranking,repository:ranking},
    spec_dashboard:{ summary:{ ...items[0], requests:4, job_results:{success:2, cancelled:2} }, total:2, catalog:2, used_current:1, historical_groups:0, items },
  }}))
  await page.goto("/admin/reports?view=specs&requests_offset=0")
  const requestToggle = page.getByRole("button", { name: /^(View|Hide) requests$/ })
  const tab = page.getByRole("tab", { name: "Specs", exact: true })
  const charts = page.getByRole("button", { name: "Charts", exact: true })
  const table = page.getByRole("button", { name: "Table", exact: true })
  const filter = page.getByRole("button", { name: "Filter by spec", exact: true })
  const trend = page.getByRole("heading", { name: "Trend", exact: true })
  const choose = async (name: string) => {
    await filter.click()
    await page.getByRole("dialog").getByRole("button", { name, exact: true }).click()
  }
  await expect(tab).toHaveAttribute("aria-selected", "true")
  await expect(charts).toHaveAttribute("aria-pressed", "true")
  await expect(page.getByText(/^(Cohort:|Previous period:|Observed )/)).toHaveCount(0)
  await expect(filter).toHaveText("All specs")
  await expect(requestToggle).toHaveCount(0)
  await expect(trend).toHaveCount(1)
  await expect(page.getByText("cancelled · 2 · 50%")).toBeVisible()
  await expect(page.getByText("Current catalog specs", { exact: true })).toHaveCount(0)
  await expect(page.getByText("No requests in this cohort.")).toHaveCount(0)
  await expect(page.getByRole("table")).toHaveCount(0)
  expect(calls.filter(url => /\/(specs|requests)\?/.test(url))).toEqual([])

  const donut = page.getByRole("img", { name: "Runner spec · Recorded runtime (h) chart" })
  await expect(donut.locator("svg")).toContainText("Total runtime")
  await expect(donut.locator("svg")).toContainText("1 h · 100.0%")
  const bar = page.getByRole("img", { name: "Runner spec · Requests chart" })
  const axisText = () => bar.locator("svg text").allTextContents().then(parts => parts.join("").replace(/\s+/g, ""))
  await expect.poll(axisText).toContain(group.label.replace(/\s+/g, ""))
  const legends = page.getByRole("list", { name: "Runner spec chart values" })
  const box = await donut.boundingBox()
  const barBox = await bar.boundingBox()
  const legendBox = await legends.first().boundingBox()
  if (!box || !barBox || !legendBox) throw new Error("Missing spec charts or legend")
  expect(box.y).toBeGreaterThanOrEqual(barBox.y + barBox.height)
  expect(legendBox.x).toBeGreaterThanOrEqual(barBox.x + barBox.width)
  await donut.click({ position: { x: box.width / 2 + box.height * 0.32, y: box.height / 2 } })
  await expect(filter).toHaveText(group.label)
  expect(Object.fromEntries(new URL(page.url()).searchParams)).toMatchObject({
    ...scope, snapshot: report.snapshot, focus_spec: group.key,
  })
  await expect(trend).toHaveCount(1)
  await expect(page.getByRole("list", {name:"Request share by repository"})).toContainText("50%")
  await expect(page.getByRole("list", {name:"Runtime share by repository"})).toContainText("75%")
  await expect(page.getByText("cancelled · 2 · 50%")).toHaveCount(0)
  await choose("unused · global")
  await expect(page.getByText("No requests in this cohort.")).toBeVisible()
  await expect(trend).toHaveCount(0)
  await choose(group.label)
  await table.click()
  await expect(table).toHaveAttribute("aria-pressed", "true")
  await expect(page.getByRole("table").getByRole("row")).toHaveCount(2)
  await expect(page.getByRole("table")).toContainText(group.label)
  await expect(trend).toHaveCount(0)
  expect(calls.filter(url => url.includes("/reports/spec-usage?"))).toHaveLength(1)
  const [restored] = await Promise.all([
    page.waitForRequest(req => req.url().includes("/reports/spec-usage?")),
    page.reload(),
  ])
  expect(Object.fromEntries(new URL(restored.url()).searchParams)).toMatchObject({
    ...scope, snapshot: report.snapshot,
  })
  await expect(page.getByRole("table")).toContainText(group.label)
  await expect(requestToggle).toHaveCount(0)
  await page.setViewportSize({ width: 390, height: 844 })
  await charts.click()
  await expect(filter).toHaveText(group.label)
  await expect(trend).toHaveCount(1)
  await choose("All specs")
  await expect(page.getByText("cancelled · 2 · 50%")).toBeVisible()
  await expect(charts).toHaveAttribute("aria-pressed", "true")
  await expect.poll(axisText).toContain(group.label.replace(/\s+/g, ""))
  await expect(donut.locator("svg")).toContainText("100.0%")
  const mobileBar = await bar.boundingBox()
  const mobileLegend = await legends.first().boundingBox()
  if (!mobileBar || !mobileLegend) throw new Error("Missing mobile chart or legend")
  expect(mobileLegend.y).toBeGreaterThanOrEqual(mobileBar.y + mobileBar.height)
  expect(calls.filter(url => /\/reports\/[^/]+\/requests(?:\?|$)/.test(url))).toEqual([])
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true)
})

test("refreshing a paged specs table returns to the first page of the new snapshot", async ({ page }) => {
  await setup(page)
  const oldSnapshot = "old-catalog"
  const catalog = Array.from({ length: 25 }, (_, i) => ({
    key: `spec-${i}`, label: `Spec ${i}`, current: true, enabled: true,
    requests: 0, started: 0, repositories: 0, runtime: { samples: 0, eligible: 0, seconds: 0 },
  }))
  await page.route("**/reports/spec-usage?*", route => {
    const old = new URL(route.request().url()).searchParams.get("snapshot") === oldSnapshot
    return route.fulfill({ json: {
      ...report, snapshot: old ? oldSnapshot : "new-catalog",
      spec_dashboard: { total: old ? 25 : 5, items: catalog.slice(0, old ? 20 : 5) },
    } })
  })
  await page.route(`**/reports/${oldSnapshot}/specs?*`, route =>
    route.fulfill({ json: { total: 25, items: catalog.slice(20) } }))
  await page.goto(`/admin/reports?${search}&view=specs&spec_mode=table&snapshot=${oldSnapshot}&spec_offset=20`)
  const previous = page.getByRole("button", { name: "Previous", exact: true })
  await expect(previous).toBeEnabled()
  await expect(page.getByRole("table")).toContainText("Spec 20")
  await page.getByRole("button", { name: "Refresh", exact: true }).click()
  await expect(page.getByText("5 specs", { exact: true })).toBeVisible()
  await expect(previous).toBeDisabled()
  await expect(page.getByRole("table")).toContainText("Spec 0")
  await expect(page).not.toHaveURL(/spec_offset=|snapshot=/)
})

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

test("form filters preserve the distribution metric and discard the old snapshot", async ({ page }) => {
  await setup(page)
  await page.route("**/reports/overview?*", (route) => {
    const params = new URL(route.request().url()).searchParams
    params.delete("snapshot")
    return route.fulfill({ json: { ...report, query: { ...query, ...Object.fromEntries(params) } } })
  })
  await page.clock.setFixedTime(new Date("2026-11-20T10:00:00Z"))
  await page.goto(`/admin/reports?${search}&sort=runtime&snapshot=fixture-snapshot`)
  const metric = page.getByRole("combobox", { name: "Distribution metric" })
  await expect(metric).toHaveText("Recorded runtime (h)")
  await page.getByLabel("Repository", { exact: true }).fill("team/repo")
  await page.getByRole("button", { name: "Apply filters" }).click()
  await expect(page).toHaveURL(/repository=team%2Frepo/)
  await expect(page).not.toHaveURL(/snapshot=/)
  await expect(metric).toHaveText("Recorded runtime (h)")
  await page.getByRole("button", { name: "Last 7 days" }).click()
  await expect(page.getByLabel("Start date", { exact: true })).not.toHaveValue(query.from)
  await expect(metric).toHaveText("Recorded runtime (h)")
  const applied = new URL(page.url()).searchParams
  await page.route("**/reports/spec-usage?*", (route) => {
    const params = new URL(route.request().url()).searchParams
    return route.fulfill({ json: { ...report, query: Object.fromEntries(params) } })
  })
  const repository = page.getByLabel("Repository", { exact: true })
  const specs = page.getByRole("tab", { name: "Specs", exact: true })
  expect(await repository.evaluate(input =>
    Boolean(input.compareDocumentPosition(document.querySelector('[role="tablist"]')!) & Node.DOCUMENT_POSITION_FOLLOWING)
  )).toBe(true)
  await repository.fill("draft/repo")
  const [specRequest] = await Promise.all([
    page.waitForRequest(request => request.url().includes("/reports/spec-usage?")),
    page.getByRole("tab", { name: "Overview", exact: true }).press("ArrowRight"),
  ])
  await expect(specs).toBeFocused()
  await expect(page.getByRole("tabpanel", { name: "Specs" })).toBeVisible()
  const specQuery = new URL(specRequest.url()).searchParams
  for (const key of ["from", "to", "timezone", "grain", "repository"]) {
    expect(specQuery.get(key)).toBe(applied.get(key))
  }
  await expect(page.getByRole("button", { name: "Charts", exact: true })).toBeVisible()
  await expect(repository).toHaveValue("draft/repo")
  await specs.press("ArrowLeft")
  await expect(page.getByRole("tab", { name: "Overview", exact: true })).toBeFocused()
  await expect(metric).toHaveText("Recorded runtime (h)")
  await expect(repository).toHaveValue("draft/repo")
  await page.getByRole("button", { name: "Clear dimension filters" }).click()
  await expect(page).not.toHaveURL(/repository=/)
  await page.reload()
  await expect(metric).toHaveText("Recorded runtime (h)")
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
  await page.getByRole("combobox", { name: "Distribution metric" }).click()
  await page.getByRole("option", { name: "Recorded runtime (h)", exact: true }).click()
  await expect(page).toHaveURL(/snapshot=fixture-snapshot/)
  await expect(page.getByRole("combobox", { name: "Distribution metric" })).toHaveText("Recorded runtime (h)")
  await expect(legend).toHaveText("Started requests")
  await expect(chart.locator("svg")).not.toContainText("Hours")
  await page.getByRole("combobox", { name: "Metric", exact: true }).click()
  await page.getByRole("option", { name: "Recorded runtime (h)", exact: true }).click()
  await expect(legend).toHaveText("Recorded runtime (h)")
  await expect(chart.locator("svg")).toContainText("Hours")
})
