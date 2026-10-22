import { expect, test } from "@playwright/test"
import { mkdir, writeFile } from "node:fs/promises"
import { join } from "node:path"

import { startTerminalFaultHarness } from "./terminal-fault-support"

type TerminalEvent = { kind: string; at: number; id: string }
type TerminalEvidence = { events: TerminalEvent[]; output: string }
declare global {
  interface Window { terminalFaultEvidence: TerminalEvidence }
}

let harness: Awaited<ReturnType<typeof startTerminalFaultHarness>>

test.beforeAll(async ({ browserName }, testInfo) => {
  test.setTimeout(120_000)
  expect(browserName).toBe("chromium")
  await mkdir(testInfo.project.outputDir, { recursive: true })
  harness = await startTerminalFaultHarness(join(testInfo.project.outputDir, `terminal-harness-${testInfo.workerIndex}.log`))
})

test.afterAll(async () => {
  if (harness) {
    await harness.close()
  }
})

for (const outageMs of [5000, 15000, 25000]) {
  test(`terminal transport outage ${outageMs / 1000}s preserves recovery boundaries`, async ({ page }, testInfo) => {
    const timing: Record<string, unknown> = { requestedOutageMs: outageMs }
    try {
      const before = await harness.metrics()
      const deletes: { at: number; duringOutage: boolean }[] = []
      page.on("request", request => {
        if (request.method() === "DELETE" && request.url().includes("/terminal/")) {
          deletes.push({ at: performance.now(), duringOutage: harness.isFaulted() })
        }
      })
      await page.addInitScript(() => {
        window.terminalFaultEvidence = { events: [], output: "" }
        // Observe native events without changing retry, readyState or timers.
        const NativeEventSource = window.EventSource
        window.EventSource = class extends NativeEventSource {
          constructor(url: string | URL, options?: EventSourceInit) {
            super(url, options)
            for (const kind of ["open", "error", "message", "terminal-heartbeat", "terminal-closed"]) {
              this.addEventListener(kind, event => {
                const message = event instanceof MessageEvent ? event : undefined
                window.terminalFaultEvidence.events.push({ kind, at: performance.now(), id: message?.lastEventId ?? "" })
                if (kind === "message" && message) window.terminalFaultEvidence.output += JSON.parse(message.data)
              })
            }
          }
        }
      })
      await page.goto(harness.url + "/github/fixture/project/deployments/terminal-fault?branch=main")
      await page.getByRole("tab", { name: "Terminal", exact: true }).click()
      const created = page.waitForResponse(response => response.request().method() === "POST" && response.url().endsWith("/terminal"))
      await page.getByRole("button", { name: "Connect", exact: true }).click()
      const session = await (await created).json() as { session_id: string; pid: number }
      await expect(page.getByRole("button", { name: "Disconnect", exact: true })).toBeVisible()
      const input = page.locator(".xterm-helper-textarea")
      // Disable shell job control so every command remains in the PTY group that
      // the fixture tears down. Numbered output stays well below the replay tail.
      await input.pressSequentially('set +m; i=1; while [ "$i" -le 60 ]; do printf "__SEQ_%03d PID=%s\\n" "$i" "$$"; i=$((i+1)); sleep 0.5; done; printf "__END PID=%s\\n" "$$"')
      await input.press("Enter")
      await expect.poll(() => page.evaluate(() => window.terminalFaultEvidence.output)).toContain("__SEQ_006 PID=")
      const earlyCursor = await page.evaluate(() => window.terminalFaultEvidence.events.filter(event => event.kind === "message" || event.kind === "terminal-heartbeat").at(-1)!.id)
      if (outageMs === 5000) {
        // Force the scheduling race: another frame arrives after this early
        // observation but before injection. It must advance the resume cursor.
        await expect.poll(() => page.evaluate(() => window.terminalFaultEvidence.events.filter(event => event.kind === "message" || event.kind === "terminal-heartbeat").at(-1)!.id)).not.toBe(earlyCursor)
      }
      const outage = harness.inject(outageMs)
      await expect.poll(() => page.evaluate(() => window.terminalFaultEvidence.events.some(event => event.kind === "error"))).toBe(true)
      const disconnected = await page.evaluate(() => {
        const events = window.terminalFaultEvidence.events
        const errorIndex = events.findIndex(event => event.kind === "error")
        const previous = events.slice(0, errorIndex).findLast(event => event.kind === "message" || event.kind === "terminal-heartbeat")!
        return { at: events[errorIndex].at, cursor: previous.id }
      })
      const firstError = disconnected.at
      const prior = disconnected.cursor
      let closedAt = 0
      const waitForClosure = async () => {
        if (outageMs < 20_000) return
        await expect(page.getByRole("alert")).toContainText("Terminal stream disconnected", { timeout: 25_000 })
        closedAt = await page.evaluate(() => performance.now())
      }
      const [restored] = await Promise.all([outage.restored, waitForClosure()])
      Object.assign(timing, restored, { shellPID: session.pid, earlyCursor, disconnectCursor: prior })
      expect(Number(timing.actualOutageMs)).toBeGreaterThanOrEqual(outageMs)
      // gstack-timing-ok: real-time fault injection measures the production recovery window.
      expect(Number(timing.actualOutageMs)).toBeLessThan(outageMs + 1000)

      if (outageMs < 20_000) {
        await expect.poll(() => page.evaluate(first => window.terminalFaultEvidence.events.some(event => event.at > first && (event.kind === "message" || event.kind === "terminal-heartbeat")), firstError)).toBe(true)
        const recovered = await page.evaluate(first => window.terminalFaultEvidence.events.find(event => event.at > first && (event.kind === "message" || event.kind === "terminal-heartbeat"))!.at, firstError)
        timing.recoveryMs = recovered - firstError
        // gstack-timing-ok: recovery must deliver data/heartbeat before the actual 20-second deadline.
        expect(recovered - firstError).toBeLessThan(20_000)
        await expect.poll(() => page.evaluate(() => window.terminalFaultEvidence.output), { timeout: 35_000 }).toContain(`__END PID=${session.pid}`)
        const output = await page.evaluate(() => window.terminalFaultEvidence.output)
        const sequences = [...output.matchAll(/__SEQ_(\d{3}) PID=(\d+)/g)].map(match => ({ n: Number(match[1]), pid: Number(match[2]) }))
        expect(sequences).toEqual(Array.from({ length: 60 }, (_, i) => ({ n: i + 1, pid: session.pid })))
        await input.pressSequentially('printf "__AFTER PID=%s\\n" "$$"')
        await input.press("Enter")
        await expect(page.locator(".xterm-rows")).toContainText(`__AFTER PID=${session.pid}`)
        const current = await harness.metrics()
        expect(current.creates - before.creates).toBe(1)
        expect(current.deletes - before.deletes).toBe(0)
        expect(current.records[session.session_id]).toMatchObject({ pid: session.pid, alive: true })
        const resumedStreams = current.streams.filter(stream => stream.session_id === session.session_id).slice(1)
        expect(resumedStreams.length).toBeGreaterThanOrEqual(1)
        expect(resumedStreams[0].last_event_id).toBe(prior)
        await page.screenshot({ path: testInfo.outputPath("recovered.png") })
        await page.getByRole("button", { name: "Disconnect", exact: true }).click()
        await expect.poll(async () => (await harness.metrics()).records[session.session_id].alive).toBe(false)
        timing.sequenceCount = sequences.length
        timing.reconnectCursor = resumedStreams[0].last_event_id
      } else {
        timing.clientClosedMs = closedAt - firstError
        // gstack-timing-ok: a real 20-second timeout must neither close early nor reset on retries.
        expect(closedAt - firstError).toBeGreaterThanOrEqual(19_500)
        // gstack-timing-ok: allow UI assertion scheduling while enforcing the production deadline.
        expect(closedAt - firstError).toBeLessThan(23_000)
        await expect(page.getByRole("button", { name: "Connect", exact: true })).toBeVisible()
        expect(deletes).toHaveLength(1)
        expect(deletes[0].duringOutage).toBe(true)
        expect((await harness.metrics()).records[session.session_id].alive).toBe(true)
        await expect.poll(async () => (await harness.metrics()).records[session.session_id].alive, { timeout: 15_000 }).toBe(false)
        const current = await harness.metrics()
        const exitMs = Date.parse(current.records[session.session_id].exited_at) - outage.startedAt
        timing.serverShellExitedMs = exitMs
        // gstack-timing-ok: DELETE cannot reach runnerd, so the real 30-second idle reaper owns cleanup.
        expect(exitMs).toBeGreaterThanOrEqual(29_000)
        // gstack-timing-ok: bounded scheduler allowance around the actual idle cleanup deadline.
        expect(exitMs).toBeLessThan(34_000)
        expect(current.creates - before.creates).toBe(1)
        expect(current.deletes - before.deletes).toBe(0)
        const events = await page.evaluate(() => window.terminalFaultEvidence.events)
        expect(events.filter(event => event.kind === "open" && event.at > firstError)).toHaveLength(0)
        await page.screenshot({ path: testInfo.outputPath("timeout.png") })
      }
    } finally {
      const evidence = {
        "timings.json": timing,
        "events.json": page.isClosed() ? null : await page.evaluate(() => window.terminalFaultEvidence),
        "metrics.json": await harness.metrics(),
      }
      for (const [name, data] of Object.entries(evidence)) {
        const path = testInfo.outputPath(name)
        await writeFile(path, JSON.stringify(data, null, 2))
        await testInfo.attach(name, { path, contentType: "application/json" })
      }
    }
  })
}
