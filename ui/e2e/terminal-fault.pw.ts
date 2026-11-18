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
  const received: { type: string; session_id?: string; data?: string }[][] = []
  const sent: string[][] = []
  page.on("websocket", socket => {
    const inputFrames: string[] = []
    const outputFrames: (typeof received)[number] = []
    sent.push(inputFrames)
    received.push(outputFrames)
    socket.on("framesent", frame => {
      const data = JSON.parse(String(frame.payload))
      if (data.type === "input") inputFrames.push(data.data)
    })
    socket.on("framereceived", frame => {
      try { outputFrames.push(JSON.parse(String(frame.payload))) } catch { /* ignore control frames */ }
    })
  })
  await page.getByRole("button", { name: "Connect", exact: true }).click()
  await expect(page.getByRole("button", { name: "Disconnect", exact: true })).toBeVisible()
  const output = (connection: number) => (received[connection] ?? []).filter(f => f.type === "output").map(f => Buffer.from(f.data!, "base64").toString()).join("")
  // A WebSocket ready frame only means the PTY exists. On macOS the interactive
  // shell can discard pending input while configuring the tty for its first prompt.
  await expect.poll(() => output(0)).toContain("__PTY_READY__")
  const input = page.locator(".xterm-helper-textarea")
  await input.fill("export TEST_TERMINAL_VALUE=retained; printf '__VALUE=%s__\\n' \"$TEST_TERMINAL_VALUE\"")
  await input.press("Enter")
  await expect.poll(() => output(0)).toContain("__VALUE=retained__")
  const oldID = received[0].find(f => f.type === "ready")!.session_id!
  const fault = harness.inject(1500)
  await expect(page.getByRole("alert")).toContainText("disconnected")
  await fault.restored
  await expect.poll(async () => (await harness.metrics()).records[oldID]?.alive).toBe(false)
  expect((await harness.metrics()).creates).toBe(1)
  await page.getByRole("button", { name: "Connect", exact: true }).click()
  await expect(page.getByRole("button", { name: "Disconnect", exact: true })).toBeVisible()
  await expect.poll(() => output(1)).toContain("__PTY_READY__")
  const newID = received[1].find(f => f.type === "ready")!.session_id!
  expect(newID).not.toBe(oldID)
  const command = "printf '__NEW=%s__\\n' \"${TEST_TERMINAL_VALUE-unset}\""
  await input.fill(command)
  await input.press("Enter")
  await expect.poll(() => sent[1]?.join("")).toBe(`${command}\r`)
  await expect.poll(() => output(1)).toContain("__NEW=unset__")
  await page.getByRole("button", { name: "Disconnect", exact: true }).click()
  await expect.poll(async () => (await harness.metrics()).records[newID]?.alive).toBe(false)
  expect((await harness.metrics()).creates).toBe(2)
})
