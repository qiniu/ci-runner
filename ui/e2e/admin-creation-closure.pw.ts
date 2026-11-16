import { expect, test, type Page, type Route } from "@playwright/test"

function diagnosis(id = "unknown-request") {
  return {
    creation_attempt_id: `attempt-${id}`,
    unknown_creation_resolution_available: true,
    state: {
      id, version: 7, status: "stopping", runner_name: `e2b-${id}`,
      repository_full_name: "fixture/repository-with-a-long-name-for-closure-review",
      workflow_job_id: 123, created_at: "2026-11-13T00:00:00Z", updated_at: "2026-11-13T00:00:00Z",
      stopping_at: "2026-11-13T00:00:00Z",
    },
    github_job: { lookup_status: "retained", source: "retained", status: "completed", conclusion: "success" },
    findings: [{ code: "sandbox_creation_unresolved", severity: "warning" }],
    events: [], events_truncated: false,
  }
}

async function setup(page: Page, language = "en") {
  test.skip(Boolean(process.env.RUNNERD_UI_SMOKE_BASE_URL), "local fixture coverage only")
  await page.addInitScript((language) => localStorage.setItem("runnerd:language", language), language)
  await page.clock.install()
  await page.route("**/auth/session", (route) => route.fulfill({ json: {
    authenticated: true, role: "admin", login: "fixture-admin", oauth_enabled: true,
  } }))
  await page.route("**/runner_requests/*/events*", (route) => route.fulfill({ json: { events: [], has_more: false } }))
}

for (const language of ["en", "zh"]) {
  test(`closes the confirmed attempt with a fresh version and refreshes diagnostics (${language})`, async ({ page }, testInfo) => {
    await setup(page, language)
    await page.setViewportSize(language === "zh" ? { width: 375, height: 812 } : { width: 1280, height: 900 })
    const current = diagnosis()
    let events: { id: number; event_type: string; stage: string; message: string; created_at: string }[] = []
    await page.route("**/runner_requests/unknown-request/events*", (route) => route.fulfill({ json: { events, has_more: false } }))
    await page.route("**/runner_requests/unknown-request/diagnostics", (route) => route.fulfill({ json: current }))
    const submissions: Route[] = []
    await page.route("**/runner_requests/unknown-request/resolve-unknown-creation", (route) => { submissions.push(route) })
    await page.goto("/admin/runner_requests/unknown-request")
    const triggerName = language === "zh" ? "接受未知结果并结案" : "Close with unknown outcome"
    const submitName = language === "zh" ? "确认接受未知并结案" : "Accept unknown and close"
    const reasonName = language === "zh" ? "结案理由" : "Closure reason"
    const trigger = page.getByRole("button", { name: triggerName, exact: true })
    await trigger.click()
    const dialog = page.getByRole("dialog")
    const reason = dialog.getByRole("textbox", { name: reasonName })
    const submit = dialog.getByRole("button", { name: submitName, exact: true })
    await expect(reason).toBeFocused()
    await expect(submit).toBeDisabled()
    await reason.fill("short")
    await dialog.getByRole("switch").check()
    await expect(submit).toBeDisabled()
    await reason.fill("中".repeat(342)) // 1026 UTF-8 bytes despite only 342 characters.
    await expect(submit).toBeDisabled()
    await reason.fill("中".repeat(341))
    await expect(submit).toBeEnabled()
    await page.keyboard.press("Escape")
    await expect(dialog).not.toBeVisible()
    await expect(trigger).toBeFocused()
    await trigger.click()
    await expect(reason).toHaveValue("")
    await expect(dialog.getByRole("switch")).not.toBeChecked()
    const evidence = language === "zh"
      ? "事件 #643：管理员接受历史创建结果未知，停止后续核对并释放配额；仍存在或迟到创建的资源可能不再被处理。"
      : "Incident #643: administrator accepts the unresolved historical creation and possible orphan resources, ending reconciliation and releasing capacity."
    await reason.fill(`  ${evidence}  `)
    await dialog.getByRole("switch").check()
    await expect(submit).toBeEnabled()
    await expect.poll(() => dialog.evaluate((node) => node.scrollWidth <= node.clientWidth)).toBe(true)
    await page.screenshot({ path: testInfo.outputPath(`closure-dialog-${language}.png`), fullPage: true })
    current.state.version = 8
    await submit.click()
    await expect.poll(() => submissions.length).toBe(1)
    expect(submissions[0].request().postDataJSON()).toEqual({
      version: 8, attempt_id: "attempt-unknown-request", evidence, accept_unknown_result: true,
    })
    await page.keyboard.press("Enter")
    await page.keyboard.press("Escape")
    await expect(dialog).toBeVisible()
    await expect(dialog.getByRole("textbox")).toBeDisabled()
    expect(submissions).toHaveLength(1)
    await page.screenshot({ path: testInfo.outputPath(`closure-pending-${language}.png`), fullPage: true })
    current.state.status = "completed"
    current.state.version = 10
    current.unknown_creation_resolution_available = false
    current.findings = [{ code: "sandbox_creation_closed_unknown", severity: "warning" }]
    events = [{ id: 1, event_type: "control_log", stage: "sandbox_creation_resolution", message: "Administrator accepted the unknown creation outcome; see audit.", created_at: "2026-11-16T00:00:00Z" }]
    await submissions[0].fulfill({ json: { id: current.state.id, status: "completed", resolution: "sandbox_creation_closed_unknown" } })
    await expect(dialog).not.toBeVisible()
    await expect(trigger).toHaveCount(0)
    await expect(page.getByText(language === "zh" ? /资源不存在仍未经证实/ : /Resource absence remains unverified/)).toBeVisible()
    await expect(page.getByText(events[0].message, { exact: true })).toBeVisible()
    await page.screenshot({ path: testInfo.outputPath(`closure-completed-${language}.png`), fullPage: true })
  })
}

test("retains check failures without retries and rejects a changed attempt before POST", async ({ page }) => {
  await setup(page)
  const current = diagnosis()
  let posts = 0
  await page.route("**/runner_requests/unknown-request/diagnostics", (route) => route.fulfill({ json: current }))
  await page.route("**/runner_requests/unknown-request/resolve-unknown-creation", (route) => {
    posts++
    return route.fulfill({ status: 409, json: { error: "original GitHub Job completion is not verified" } })
  })
  await page.goto("/admin/runner_requests/unknown-request")
  await page.getByRole("button", { name: "Close with unknown outcome", exact: true }).click()
  const dialog = page.getByRole("dialog")
  await dialog.getByRole("textbox").fill("Incident #643: explicitly accept the historical unknown outcome")
  await dialog.getByRole("switch").check()
  await dialog.getByRole("button", { name: "Accept unknown and close", exact: true }).click()
  await expect(dialog.getByRole("alert")).toContainText("original GitHub Job completion is not verified")
  await expect(dialog.getByRole("alert")).toContainText("No automatic retry")
  await expect(dialog.getByRole("switch")).not.toBeChecked()
  await page.clock.fastForward(6000)
  expect(posts).toBe(1)
  await expect(dialog.getByRole("button", { name: "Accept unknown and close", exact: true })).toBeDisabled()
  await dialog.getByRole("button", { name: "Cancel", exact: true }).click()
  await page.getByRole("button", { name: "Close with unknown outcome", exact: true }).click()
  await dialog.getByRole("textbox").fill("Incident #643: explicitly accept the historical unknown outcome")
  await dialog.getByRole("switch").check()
  current.creation_attempt_id = "replacement-attempt"
  await dialog.getByRole("button", { name: "Accept unknown and close", exact: true }).click()
  await expect(dialog.getByRole("alert")).toContainText("request or creation attempt changed")
  expect(posts).toBe(1)
})

test("navigation during the fresh-state read cannot submit the previous request", async ({ page }) => {
  await setup(page)
  let reads = 0, posts = 0
  const pending: Route[] = []
  await page.route("**/runner_requests/unknown-request/diagnostics", (route) => {
    reads++
    if (reads === 1) return route.fulfill({ json: diagnosis() })
    pending.push(route)
  })
  await page.route("**/runner_requests/other-request/diagnostics", (route) => route.fulfill({ json: diagnosis("other-request") }))
  await page.route("**/runner_requests/*/resolve-unknown-creation", (route) => { posts++; return route.fulfill({ json: {} }) })
  await page.goto("/admin/runner_requests/unknown-request")
  await page.getByRole("button", { name: "Close with unknown outcome", exact: true }).click()
  await page.getByRole("dialog").getByRole("textbox").fill("Incident #643: accept the unknown historical outcome")
  await page.getByRole("dialog").getByRole("switch").check()
  await page.getByRole("button", { name: "Accept unknown and close", exact: true }).click()
  await expect.poll(() => pending.length).toBe(1)
  await page.evaluate(() => {
    history.pushState({}, "", "/admin/runner_requests/other-request")
    window.dispatchEvent(new PopStateEvent("popstate"))
  })
  await expect(page.getByText("other-request", { exact: true }).first()).toBeVisible()
  await pending[0].fulfill({ json: diagnosis() })
  await expect(page.getByRole("dialog")).not.toBeVisible()
  expect(posts).toBe(0)
  await page.getByRole("button", { name: "Close with unknown outcome", exact: true }).click()
  await expect(page.getByRole("dialog").getByRole("textbox")).toHaveValue("")
  await expect(page.getByRole("dialog").getByRole("switch")).not.toBeChecked()
})
