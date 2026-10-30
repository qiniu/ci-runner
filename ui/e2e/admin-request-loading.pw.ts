import { expect, test, type Page, type Route } from "@playwright/test"

function rows(prefix: string, count = 100) {
  return Array.from({ length: count }, (_, i) => ({
    id: `${prefix}-${i}`, runner_name: `${prefix}-${i}`, status: "running",
    repository_full_name: "fixture/repository", runner_spec_name: "fixture-spec",
    created_at: "2026-10-30T00:00:00Z", updated_at: "2026-10-30T00:00:00Z",
  }))
}

async function setup(page: Page) {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")
  await page.addInitScript(() => localStorage.setItem("runnerd:language", "en"))
  await page.clock.install()
  await page.route("**/auth/session", (route) => route.fulfill({ json: {
    authenticated: true, role: "admin", login: "fixture-admin", oauth_enabled: true,
  } }))
  await page.route("**/runner_specs", (route) => route.fulfill({ json: [] }))
}

test("admin list accepts slow responses independently of statistics and retains rows on refresh failure", async ({ page }) => {
  await setup(page)
  const lists: Route[] = [], metrics: Route[] = []
  await page.route("**/runner_requests?*", (route) => { lists.push(route) })
  await page.route("**/runner_request_metrics", (route) => { metrics.push(route) })
  await page.goto("/admin/runner_requests")
  await expect.poll(() => lists.length).toBe(1)
  await expect(page.getByRole("cell", { name: "Loading...", exact: true })).toBeVisible()
  await expect(page.getByText("No runner requests found", { exact: true })).toHaveCount(0)
  for (let tick = 0; tick < 3; tick++) await page.clock.fastForward(5000)
  expect(lists).toHaveLength(1)
  await lists[0].fulfill({ json: { items: rows("slow"), next_cursor: "older", has_more: true } })
  await expect(page.getByRole("link", { name: "slow-0", exact: true })).toBeVisible()
  await expect(page.getByRole("button", { name: "Refresh", exact: true })).toBeEnabled()
  // Statistics are still pending, but neither list rendering nor manual refresh waits for them.
  await page.getByRole("button", { name: "Refresh", exact: true }).click()
  await expect.poll(() => lists.length).toBe(2)
  await expect(page.getByRole("link", { name: "slow-0", exact: true })).toBeVisible()
  await lists[1].fulfill({ status: 500, json: { error: "list unavailable" } })
  await expect(page.getByRole("alert")).toContainText("list unavailable")
  await expect(page.getByRole("link", { name: "slow-0", exact: true })).toBeVisible()
  // Manual refresh cancels the previous statistics subscriber and starts another.
  await metrics.at(-1)!.fulfill({ status: 500, json: { error: "statistics unavailable" } })
  await expect(page.getByText("Failed to load Runner request metrics", { exact: true })).toBeVisible()
})

test("admin filter changes ignore an older slow response", async ({ page }) => {
  await setup(page)
  const lists: Route[] = []
  await page.route("**/runner_request_metrics", (route) => route.fulfill({ json: {} }))
  await page.route("**/runner_requests?*", (route) => { lists.push(route) })
  await page.goto("/admin/runner_requests")
  await expect.poll(() => lists.length).toBe(1)
  await page.getByRole("combobox").first().click()
  await page.getByRole("option", { name: "Running", exact: true }).click()
  await expect.poll(() => lists.length).toBe(2)
  expect(new URL(lists[1].request().url()).searchParams.get("status")).toBe("running")
  await lists[1].fulfill({ json: { items: rows("filtered"), next_cursor: null, has_more: false } })
  await expect(page.getByRole("link", { name: "filtered-0", exact: true })).toBeVisible()
  await lists[0].fulfill({ json: { items: rows("obsolete"), next_cursor: null, has_more: false } })
  await expect(page.getByRole("link", { name: "obsolete-0", exact: true })).toHaveCount(0)
  await expect(page.getByRole("link", { name: "filtered-0", exact: true })).toBeVisible()
})

test("admin unexpanded list refreshes only its latest page as new requests arrive", async ({ page }) => {
  await setup(page)
  const cursors: Array<string | null> = []
  await page.route("**/runner_request_metrics", (route) => route.fulfill({ json: {} }))
  await page.route("**/runner_requests?*", (route) => {
    cursors.push(new URL(route.request().url()).searchParams.get("cursor"))
    return route.fulfill({ json: { items: rows(`latest-${cursors.length}`), next_cursor: `older-${cursors.length}`, has_more: true } })
  })
  await page.goto("/admin/runner_requests")
  await expect(page.getByRole("link", { name: "latest-1-0", exact: true })).toBeVisible()
  for (let refresh = 2; refresh <= 4; refresh++) {
    await page.clock.fastForward(10000)
    await expect(page.getByRole("link", { name: `latest-${refresh}-0`, exact: true })).toBeVisible()
  }
  expect(cursors).toEqual([null, null, null, null])
  await expect(page.getByRole("link", { name: "latest-1-0", exact: true })).toHaveCount(0)
  await expect(page.getByText("100 loaded", { exact: true })).toBeAttached()
})

test("admin polling bounds a missing boundary and preserves a continuation cursor", async ({ page }) => {
  await setup(page)
  const cursors: Array<string | null> = []
  let metricsCalls = 0
  await page.route("**/runner_request_metrics", (route) => { metricsCalls++; return route.fulfill({ json: {} }) })
  await page.route("**/runner_requests?*", (route) => {
    const cursor = new URL(route.request().url()).searchParams.get("cursor")
    cursors.push(cursor)
    return route.fulfill({ json: { items: rows(`page-${cursors.length}`), next_cursor: `cursor-${cursors.length}`, has_more: true } })
  })
  await page.goto("/admin/runner_requests")
  await expect(page.getByRole("link", { name: "page-1-0", exact: true })).toBeVisible()
  await page.getByRole("button", { name: "Load more requests", exact: true }).scrollIntoViewIfNeeded()
  await expect(page.getByRole("link", { name: "page-2-0", exact: true })).toBeAttached()
  await page.getByRole("link", { name: "page-1-0", exact: true }).scrollIntoViewIfNeeded()
  await page.clock.fastForward(10000)
  await expect(page.getByRole("status")).toContainText("500 requests")
  expect(cursors).toEqual([null, "cursor-1", null, "cursor-3", "cursor-4", "cursor-5", "cursor-6"])
  expect(metricsCalls).toBe(1)
  await page.getByRole("button", { name: "Load more requests", exact: true }).scrollIntoViewIfNeeded()
  await expect.poll(() => cursors.length).toBe(8)
  expect(cursors[7]).toBe("cursor-7")
  await expect(page.getByRole("link", { name: "page-8-0", exact: true })).toBeAttached()
  await page.clock.fastForward(10000)
  expect(cursors).toHaveLength(8)
  await expect(page.getByRole("status")).toContainText("Automatic list refresh is paused")
  await expect(page.getByRole("link", { name: "page-8-0", exact: true })).toBeAttached()
})

test("admin history paging finishes without being invalidated by polling", async ({ page }) => {
  await setup(page)
  const requests: Route[] = []
  await page.route("**/runner_request_metrics", (route) => route.fulfill({ json: {} }))
  await page.route("**/runner_requests?*", (route) => {
    requests.push(route)
    if (requests.length === 1) return route.fulfill({ json: { items: rows("head"), next_cursor: "older", has_more: true } })
  })
  await page.goto("/admin/runner_requests")
  await expect(page.getByRole("link", { name: "head-0", exact: true })).toBeVisible()
  await page.getByRole("button", { name: "Load more requests", exact: true }).scrollIntoViewIfNeeded()
  await expect.poll(() => requests.length).toBe(2)
  expect(new URL(requests[1].request().url()).searchParams.get("cursor")).toBe("older")
  for (let tick = 0; tick < 3; tick++) await page.clock.fastForward(5000)
  expect(requests).toHaveLength(2)
  await requests[1].fulfill({ json: { items: rows("history"), next_cursor: null, has_more: false } })
  await expect(page.getByRole("link", { name: "history-0", exact: true })).toBeAttached()
  await expect(page.getByText("200 loaded", { exact: true })).toBeVisible()
})

test("admin initial timeout shows a recoverable error rather than an empty result", async ({ page }) => {
  await setup(page)
  const requests: Route[] = []
  await page.route("**/runner_request_metrics", (route) => route.fulfill({ json: {} }))
  await page.route("**/runner_requests?*", (route) => { requests.push(route) })
  await page.goto("/admin/runner_requests")
  await expect.poll(() => requests.length).toBe(1)
  await page.clock.fastForward(31000)
  await expect(page.getByRole("alert")).toContainText("Failed to load control plane data")
  await expect(page.getByText("No runner requests found", { exact: true })).toHaveCount(0)
  await page.getByRole("button", { name: "Refresh", exact: true }).click()
  await expect.poll(() => requests.length).toBe(2)
  await requests[1].fulfill({ json: { items: rows("recovered"), next_cursor: null, has_more: false } })
  await expect(page.getByRole("link", { name: "recovered-0", exact: true })).toBeVisible()
  await expect(page.getByRole("alert")).toHaveCount(0)
})

test("admin session recheck times out without signing out and allows retry", async ({ page }) => {
  await setup(page)
  let sessionCalls = 0, listCalls = 0
  let sessionRecheck: Route | undefined
  await page.route("**/auth/session", (route) => {
    sessionCalls++
    if (sessionCalls === 1) return route.fulfill({ json: {
      authenticated: true, role: "admin", login: "fixture-admin", oauth_enabled: true,
    } })
    sessionRecheck = route
  })
  await page.route("**/runner_request_metrics", (route) => route.fulfill({ json: {} }))
  await page.route("**/runner_requests?*", (route) => {
    listCalls++
    return listCalls === 1
      ? route.fulfill({ status: 401 })
      : route.fulfill({ json: { items: rows("retried"), next_cursor: null, has_more: false } })
  })
  await page.goto("/admin/runner_requests")
  await expect.poll(() => sessionCalls).toBe(2)
  await page.clock.fastForward(31000)
  await expect(page.getByRole("alert")).toContainText("Failed to load control plane data")
  await expect(page.getByRole("button", { name: "Refresh", exact: true })).toBeEnabled()
  await page.getByRole("button", { name: "Refresh", exact: true }).click()
  await expect(page.getByRole("link", { name: "retried-0", exact: true })).toBeVisible()
  // A late response from the cancelled check must not sign out the recovered page.
  await sessionRecheck!.fulfill({ json: { authenticated: false } })
  await expect(page.getByRole("link", { name: "retried-0", exact: true })).toBeVisible()
  await expect(page.getByRole("alert")).toHaveCount(0)
  expect(listCalls).toBe(2)
})
