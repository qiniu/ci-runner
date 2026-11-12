import { expect, test } from "@playwright/test"
import { mkdir } from "node:fs/promises"
import { join } from "node:path"
import { startTerminalFaultHarness } from "./terminal-fault-support"

let harness: Awaited<ReturnType<typeof startTerminalFaultHarness>>
test.beforeAll(async ({ browserName }, info) => {
  expect(browserName).toBe("chromium")
  test.setTimeout(120_000)
  await mkdir(info.project.outputDir, { recursive: true })
  harness = await startTerminalFaultHarness(join(info.project.outputDir, "terminal-websocket.log"))
})
test.afterAll(async () => { if (harness) await harness.close() })

test("disconnect ends the shell; explicit reopen creates a fresh session without input replay", async ({ page }) => {
  await page.goto(harness.url + "/github/fixture/project/deployments/terminal-fault?branch=main")
  await page.getByRole("tab", { name: "Terminal", exact: true }).click()
  const received: { type: string; session_id?: string; data?: string }[] = []
  page.on("websocket", socket => socket.on("framereceived", frame => {
    try { received.push(JSON.parse(String(frame.payload))) } catch { /* ignore control frames */ }
  }))
  await page.getByRole("button", { name: "Connect", exact: true }).click()
  await expect(page.getByRole("button", { name: "Disconnect", exact: true })).toBeVisible()
  const input = page.locator(".xterm-helper-textarea")
  await input.fill("export TEST_TERMINAL_VALUE=retained; printf '__VALUE=%s__\\n' \"$TEST_TERMINAL_VALUE\"")
  await input.press("Enter")
  const output = () => received.filter(f => f.type === "output").map(f => Buffer.from(f.data!, "base64").toString()).join("")
  await expect.poll(output).toContain("__VALUE=retained__")
  const oldID = received.find(f => f.type === "ready")!.session_id!
  const fault = harness.inject(1500)
  await expect(page.getByRole("alert")).toContainText("disconnected")
  await fault.restored
  await expect.poll(async () => (await harness.metrics()).records[oldID]?.alive).toBe(false)
  expect((await harness.metrics()).creates).toBe(1)
  await page.getByRole("button", { name: "Connect", exact: true }).click()
  await expect(page.getByRole("button", { name: "Disconnect", exact: true })).toBeVisible()
  await expect.poll(() => received.filter(f => f.type === "ready").length).toBe(2)
  const newID = received.filter(f => f.type === "ready")[1].session_id!
  expect(newID).not.toBe(oldID)
  await input.fill("printf '__NEW=%s__\\n' \"${TEST_TERMINAL_VALUE-unset}\"")
  await input.press("Enter")
  await expect.poll(output).toContain("__NEW=unset__")
  await page.getByRole("button", { name: "Disconnect", exact: true }).click()
  await expect.poll(async () => (await harness.metrics()).records[newID]?.alive).toBe(false)
  expect((await harness.metrics()).creates).toBe(2)
})
