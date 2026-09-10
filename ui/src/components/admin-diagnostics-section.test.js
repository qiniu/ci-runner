import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { Window } from "happy-dom"
import { act, createElement } from "react"
import { createRoot } from "react-dom/client"
import { renderToStaticMarkup } from "react-dom/server"

import i18n from "../i18n"
import { DiagnosticsSection, RunnerdRuntimeDiagnostics, RunnerRequestDiagnosisResult } from "./admin-sections"

const window = new Window({ url: "http://localhost/" })
const domGlobals = { window, document: window.document, navigator: window.navigator, HTMLElement: window.HTMLElement, SVGElement: window.SVGElement, Node: window.Node, DocumentFragment: window.DocumentFragment, Event: window.Event, MouseEvent: window.MouseEvent, KeyboardEvent: window.KeyboardEvent, getComputedStyle: window.getComputedStyle.bind(window), requestAnimationFrame: window.requestAnimationFrame.bind(window), cancelAnimationFrame: window.cancelAnimationFrame.bind(window), IS_REACT_ACT_ENVIRONMENT: true }
const originalGlobalDescriptors = new Map(Object.keys(domGlobals).map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(domGlobals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const mountedRoots = []

afterEach(async () => {
  for (const { root, container } of mountedRoots.splice(0)) {
    await act(async () => root.unmount())
    container.remove()
  }
})

afterAll(() => {
  for (const [key, descriptor] of originalGlobalDescriptors) {
    if (descriptor) Object.defineProperty(globalThis, key, descriptor)
    else Reflect.deleteProperty(globalThis, key)
  }
  window.close()
})

const diagnostics = {
  pprof: [],
  state: { backend: "sqlite", database: "var/runnerd.db" },
  github: { auth_mode: "app", api_base_url: "https://api.github.com" },
  recent_failures: [{
    id: "101445685709",
    status: "completed",
    runner_name: "e2b-101445685709",
    repository_full_name: "xgo-dev/llgo",
    failure_reason: "runner communication lost",
    updated_at: "2026-09-06T07:25:09Z",
    created_at: "2026-09-06T07:04:57Z",
  }],
}

async function renderDiagnostics(request = async () => ({}), props = {}) {
  const container = document.createElement("div")
  document.body.append(container)
  const root = createRoot(container)
  mountedRoots.push({ root, container })
  await act(async () => root.render(createElement(DiagnosticsSection, { diagnostics, request, ...props })))
  return container
}

async function renderRuntimeDiagnostics(request = async () => ({})) {
  const container = document.createElement("div")
  document.body.append(container)
  const root = createRoot(container)
  mountedRoots.push({ root, container })
  await act(async () => root.render(createElement(RunnerdRuntimeDiagnostics, { diagnostics, request })))
  return container
}

async function click(element) {
  await act(async () => {
    element.focus()
    element.dispatchEvent(new MouseEvent("mousedown", { bubbles: true, cancelable: true, button: 0 }))
    element.click()
  })
  await act(async () => {
    await Promise.resolve()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

describe("admin diagnostics", () => {
  test("prompts for the user-visible Runner Name while keeping request ID compatibility", async () => {
    await i18n.changeLanguage("zh")
    try {
      const html = renderToStaticMarkup(createElement(DiagnosticsSection, {
        diagnostics: null,
        request: async () => ({}),
      }))
      expect(html).toContain("Runner Name / Request ID")
      expect(html).toContain('placeholder="e2b-101445685709"')
      expect(html).toContain("GitHub Job 页面显示的 Runner Name")
    } finally {
      await i18n.changeLanguage("en")
    }
  })

  test("separates request diagnosis from runnerd runtime diagnostics", async () => {
    await i18n.changeLanguage("en")
    const html = renderToStaticMarkup(createElement(DiagnosticsSection, {
      diagnostics,
      request: async () => ({}),
    }))

    expect(html).toContain("Request diagnosis")
    expect(html).toContain("runnerd runtime")
    expect(html).not.toContain("Diagnostics summary")
    expect(html).not.toContain("No /debug/vars data available")
  })

  test("keeps diagnosis focused instead of duplicating runner request browsing", async () => {
    const html = renderToStaticMarkup(createElement(DiagnosticsSection, {
      diagnostics,
      request: async () => ({}),
    }))

    expect(html).not.toContain("Recent failures")
    expect(html).not.toContain("data-diagnostic-runner")
  })

  test("runs the diagnosis from a shareable runner deep link", async () => {
    const requestedURLs = []
    const request = async (url) => {
      requestedURLs.push(url)
      return {
        state: diagnostics.recent_failures[0],
        github_job: { lookup_status: "unavailable" },
        findings: [],
        events: [],
        events_truncated: false,
      }
    }

    const container = await renderDiagnostics(request, { initialRequestIdentifier: "e2b-101445685709" })

    expect(requestedURLs).toEqual(["/diagnostics/runner-requests/e2b-101445685709"])
    expect(container.querySelector("input")?.value).toBe("e2b-101445685709")
  })

  test("loads expvar only after the runtime action is requested", async () => {
    const requestedURLs = []
    const request = async (url) => {
      requestedURLs.push(url)
      return { runner_requests_total: 42 }
    }
    const container = await renderRuntimeDiagnostics(request)
    expect(requestedURLs).toEqual([])
    expect(container.textContent).toContain("Load expvar")

    const loadButton = Array.from(container.querySelectorAll("button")).find((element) => element.textContent?.includes("Load expvar"))
    expect(loadButton).toBeDefined()
    await click(loadButton)

    expect(requestedURLs).toEqual(["/diagnostics/vars"])
    expect(container.textContent).toContain('"runner_requests_total": 42')
  })

  test("stretches diagnostic findings across the available width", () => {
    const html = renderToStaticMarkup(createElement(RunnerRequestDiagnosisResult, {
      diagnosis: {
        state: diagnostics.recent_failures[0],
        github_job: { lookup_status: "unavailable" },
        findings: [{ code: "no_anomaly_detected", severity: "ok" }],
        events: [],
        events_truncated: false,
      },
    }))
    const container = document.createElement("div")
    container.innerHTML = html
    const finding = Array.from(container.querySelectorAll("div")).find((element) => element.textContent === "No anomaly was detected from the available evidence")
    const findingsGrid = finding?.closest(".grid")

    expect(findingsGrid?.classList.contains("grid-cols-1")).toBe(true)
    expect(findingsGrid?.classList.contains("lg:grid-cols-2")).toBe(false)
  })

  test("renders an unknown finding as neutral instead of claiming no anomaly", () => {
    const html = renderToStaticMarkup(createElement(RunnerRequestDiagnosisResult, {
      diagnosis: {
        state: diagnostics.recent_failures[0],
        github_job: { lookup_status: "not_applicable" },
        findings: [{ code: "future_diagnostic_signal", severity: "future" }],
        events: [],
        events_truncated: false,
      },
    }))
    const container = document.createElement("div")
    container.innerHTML = html
    const findingText = Array.from(container.querySelectorAll("div.text-sm")).find((element) => element.textContent === "Unknown diagnostic finding: future_diagnostic_signal")
    const finding = findingText?.closest(".flex")

    expect(finding).toBeDefined()
    expect(finding?.className).toContain("bg-muted/30")
    expect(html).not.toContain("No anomaly was detected from the available evidence")
    expect(html).not.toContain("bg-emerald-500/5")
  })

  test("renders findings and a chronological event timeline", async () => {
    await i18n.changeLanguage("zh")
    try {
      const html = renderToStaticMarkup(createElement(RunnerRequestDiagnosisResult, {
        diagnosis: {
          state: {
            id: "101445685709",
            status: "completed",
            runner_name: "e2b-101445685709",
            repository_full_name: "xgo-dev/llgo",
            runner_spec_name: "qiniu-ubuntu-24.04",
            sandbox_id: "sandbox-llgo",
            head_branch: "codex/test-sync-concurrent-wait-20260905",
            retry_count: 2,
            error: "runner communication lost",
            updated_at: "2026-09-06T07:25:09Z",
            created_at: "2026-09-06T07:04:57Z",
          },
          github_job: {
            lookup_status: "ok",
            id: 101445685709,
            conclusion: "failure",
            status: "completed",
          },
          findings: [
            { code: "github_job_failed", severity: "critical", detail: "failure" },
            { code: "runner_termination_unobserved", severity: "critical", detail: "2026-09-06T07:05:06Z" },
          ],
          events: [
            {
              id: 4143071,
              event_type: "control_log",
              message: "runner accepted a job\n",
              created_at: "2026-09-06T07:05:06Z",
            },
          ],
          events_truncated: false,
        },
      }))
      expect(html).toContain("GitHub Job 已失败")
      expect(html).toContain("Runner 终止过程未被 runnerd 观察到")
      expect(html).toContain("runner accepted a job")
      expect(html).toContain("xgo-dev/llgo")
      expect(html).toContain("请求上下文")
      expect(html).toContain("qiniu-ubuntu-24.04")
      expect(html).toContain("sandbox-llgo")
      expect(html).toContain("codex/test-sync-concurrent-wait-20260905")
      expect(html).toContain("runner communication lost")
    } finally {
      await i18n.changeLanguage("en")
    }
  })
})
