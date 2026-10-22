import { spawn } from "node:child_process"
import { once } from "node:events"
import { createServer, request, type ServerResponse, type ClientRequest } from "node:http"
import { mkdtemp, readFile, rm, writeFile } from "node:fs/promises"
import { tmpdir } from "node:os"
import { join } from "node:path"
import { setTimeout as delay } from "node:timers/promises"
import { fileURLToPath } from "node:url"

export type TerminalMetrics = {
  creates: number
  deletes: number
  records: Record<string, { pid: number; alive: boolean; exited_at: string }>
  streams: { session_id: string; last_event_id: string }[]
}

// Timers can fire before a performance.now() deadline. Keep traffic blocked
// until the monotonic clock actually reaches it, including after rescheduling.
export function scheduleAtMonotonicDeadline(deadline: number, callback: () => void) {
  const check = () => {
    const remaining = deadline - performance.now()
    if (remaining > 0) timer = setTimeout(check, Math.ceil(remaining))
    else callback()
  }
  let timer = setTimeout(check, Math.max(0, Math.ceil(deadline - performance.now())))
  return () => clearTimeout(timer)
}

// All traffic is loopback-only. A real proxy drops the active SSE connection and
// rejects subsequent requests using native EventSource retries and browser timers.
export async function startTerminalFaultHarness(logPath: string) {
  const temporary = await mkdtemp(join(tmpdir(), "runnerd-terminal-fault-"))
  const readyFile = join(temporary, "ready")
  const child = spawn("go", ["test", "-race", "-tags=terminalfault", "./internal/server", "-run=^TestTerminalFaultHarness$", "-count=1", "-timeout=6m", "-v"], {
    cwd: fileURLToPath(new URL("../../", import.meta.url)),
    env: { ...process.env, RUNNERD_TERMINAL_FAULT_READY_FILE: readyFile },
    detached: true,
    stdio: ["ignore", "pipe", "pipe"],
  })
  let log = ""
  child.stdout.on("data", data => { log += String(data) })
  child.stderr.on("data", data => { log += String(data) })
  let spawnError: Error | undefined
  child.on("error", error => { spawnError = error })
  const exited = once(child, "exit").then(([code]) => code as number | null).catch(error => { spawnError = error; return -1 })
  let backend = ""
  let faulted = false
  let blockedRequests = 0
  let cancelRestoration: (() => void) | undefined
  const active = new Set<{ response: ServerResponse; upstream: ClientRequest }>()
  const proxy = createServer((incoming, response) => {
    if (faulted) {
      blockedRequests++
      incoming.socket.destroy()
      return
    }
    const upstream = request(backend + incoming.url, { method: incoming.method, headers: incoming.headers }, result => {
      response.writeHead(result.statusCode ?? 502, result.headers)
      result.pipe(response)
    })
    const entry = { response, upstream }
    active.add(entry)
    response.on("close", () => { active.delete(entry); upstream.destroy() })
    upstream.on("error", () => response.destroy())
    incoming.pipe(upstream)
  })

  async function close() {
    cancelRestoration?.()
    faulted = false
    proxy.closeAllConnections()
    await new Promise<void>(resolve => proxy.close(() => resolve()))
    try {
      if (backend && child.exitCode === null) {
        await fetch(backend + "/__stop", { signal: AbortSignal.timeout(2000) })
      }
      const code = await Promise.race([exited, delay(10_000, undefined, { ref: false }).then(() => "timeout")])
      if (code !== 0) throw new Error(`terminal harness exited with ${code}\n${log}`)
    } finally {
      // If startup/teardown failed, terminate the test process group too. Python
      // handles TERM and reaps its separate PTY shell group.
      if (child.exitCode === null) {
        try { process.kill(-child.pid!, "SIGTERM") } catch { /* process already exited */ }
        await Promise.race([exited, delay(5000, undefined, { ref: false })])
      }
      await writeFile(logPath, log)
      await rm(temporary, { recursive: true, force: true })
    }
  }

  try {
    const deadline = performance.now() + 110_000
    while (!backend) {
      if (spawnError) throw spawnError
      if (child.exitCode !== null) throw new Error(`terminal fixture exited during startup\n${log}`)
      try { backend = await readFile(readyFile, "utf8") } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error
      }
      if (performance.now() > deadline) throw new Error(`terminal fixture startup timed out\n${log}`)
      if (!backend) await delay(100)
    }
    if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(backend)) throw new Error("fixture must listen on loopback")
    await new Promise<void>(resolve => proxy.listen(0, "127.0.0.1", resolve))
  } catch (error) {
    await close().catch(() => undefined)
    throw error
  }
  const address = proxy.address()
  if (!address || typeof address === "string") throw new Error("missing proxy address")
  return {
    url: `http://127.0.0.1:${address.port}`,
    isFaulted: () => faulted,
    metrics: async (): Promise<TerminalMetrics> => {
      const response = await fetch(backend + "/__metrics", { signal: AbortSignal.timeout(3000) })
      if (!response.ok) throw new Error(`fixture metrics returned ${response.status}`)
      return response.json()
    },
    inject(durationMs: number) {
      if (faulted) throw new Error("an outage is already active")
      faulted = true
      blockedRequests = 0
      const started = performance.now()
      const startedAt = Date.now()
      for (const entry of active) { entry.upstream.destroy(); entry.response.destroy() }
      const restored = new Promise<{ actualOutageMs: number; blockedRequests: number }>(resolve => {
        cancelRestoration = scheduleAtMonotonicDeadline(started + durationMs, () => {
          faulted = false
          resolve({ actualOutageMs: performance.now() - started, blockedRequests })
        })
      })
      return { startedAt, restored }
    },
    close,
  }
}
