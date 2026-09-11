import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { Window } from "happy-dom"
import { act, createElement } from "react"
import { createRoot } from "react-dom/client"
import { renderToStaticMarkup } from "react-dom/server"

import i18n from "../i18n"
import { DiagnosticsSection, RunnerdRuntimeDiagnostics, RunnerRequestDiagnosisResult, RunnerRequestSection } from "./admin-sections"

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

const runnerState = {
  id: "101445685709",
  status: "completed",
  runner_name: "e2b-101445685709",
  repository_full_name: "xgo-dev/llgo",
  failure_reason: "runner communication lost",
  updated_at: "2026-09-06T07:25:09Z",
  created_at: "2026-09-06T07:04:57Z",
}

const diagnostics = {
  pprof: [],
  state: { backend: "sqlite", database: "var/runnerd.db" },
  github: { auth_mode: "app", api_base_url: "https://api.github.com" },
}

async function renderDiagnostics(request = async () => ({}), props = {}) {
  const container = document.createElement("div")
  document.body.append(container)
  const root = createRoot(container)
  mountedRoots.push({ root, container })
  await act(async () => root.render(createElement(DiagnosticsSection, { diagnostics, request, ...props })))
  return container
}

async function renderRunnerRequest(request = async () => ({}), props = {}) {
  const container = document.createElement("div")
  document.body.append(container)
  const root = createRoot(container)
  mountedRoots.push({ root, container })
  await act(async () => root.render(createElement(RunnerRequestSection, {
    identifier: "101445685709",
    request,
    onBackToRunnerRequests() {},
    ...props,
  })))
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
  test("keeps the diagnostics page focused on runnerd runtime diagnostics", async () => {
    await i18n.changeLanguage("en")
    const html = renderToStaticMarkup(createElement(DiagnosticsSection, {
      diagnostics,
      request: async () => ({}),
    }))

    expect(html).toContain("Diagnostics summary")
    expect(html).toContain("Load expvar")
    expect(html).not.toContain("Request diagnosis")
    expect(html).not.toContain("Runner Name / Request ID")
  })

  test("keeps diagnosis focused instead of duplicating runner request browsing", async () => {
    const html = renderToStaticMarkup(createElement(DiagnosticsSection, {
      diagnostics,
      request: async () => ({}),
    }))

    expect(html).not.toContain("Recent failures")
    expect(html).not.toContain("data-diagnostic-runner")
  })

  test("loads the canonical request resource by internal ID", async () => {
    const requestedURLs = []
    const resolvedIDs = []
    const request = async (url) => {
      requestedURLs.push(url)
      return {
        state: runnerState,
        github_job: { lookup_status: "unavailable" },
        findings: [],
        events: [],
        events_truncated: false,
      }
    }

    const container = await renderRunnerRequest(request, {
      onResolvedRequestID: (id) => resolvedIDs.push(id),
    })

    expect(requestedURLs).toEqual([
      "/runner_requests/101445685709/diagnostics",
      "/runner_requests/101445685709/events",
    ])
    expect(resolvedIDs).toEqual(["101445685709"])
    expect(container.textContent).toContain("101445685709")
  })

  test("preserves the request card's natural height inside the admin flex scroller", () => {
    const html = renderToStaticMarkup(createElement(RunnerRequestSection, {
      identifier: "101445685709",
      request: async () => ({}),
      onBackToRunnerRequests() {},
    }))
    const container = document.createElement("div")
    container.innerHTML = html

    expect(container.querySelector('[data-slot="card"]')?.classList.contains("shrink-0")).toBe(true)
  })

  test("filters the loaded chronological event stream without refetching", async () => {
    const requestedURLs = []
    const request = async (url) => {
      requestedURLs.push(url)
      if (url === "/runner_requests/101445685709/diagnostics") {
        return {
          state: runnerState,
          github_job: { lookup_status: "unavailable" },
          findings: [],
          events: [{
            id: 1,
            event_type: "control_log",
            message: "runner accepted a job\n",
            created_at: "2026-09-06T07:05:06Z",
          }],
          events_truncated: false,
        }
      }
      if (url === "/runner_requests/101445685709/events") {
        return {
          events: [
            { id: 3, event_type: "stdout_log", message: "second output line\n", created_at: "2026-09-06T07:05:03Z" },
            { id: 4, event_type: "stderr_log", message: "setup warning\n", created_at: "2026-09-06T07:05:04Z" },
            { id: 5, event_type: "control_log", message: "runner accepted a job\n", created_at: "2026-09-06T07:05:05Z" },
          ],
          has_more: true,
        }
      }
      if (url === "/runner_requests/101445685709/events?before_id=3") {
        return {
          events: [
            { id: 1, event_type: "control_log", message: "runner request created\n", created_at: "2026-09-06T07:05:01Z" },
            { id: 2, event_type: "stdout_log", message: "first output line\n", created_at: "2026-09-06T07:05:02Z" },
          ],
          has_more: false,
        }
      }
      throw new Error(`unexpected request: ${url}`)
    }

    const container = await renderRunnerRequest(request)
    await act(async () => new Promise((resolve) => setTimeout(resolve, 0)))
    expect(requestedURLs).toEqual([
      "/runner_requests/101445685709/diagnostics",
      "/runner_requests/101445685709/events",
    ])
    expect(container.textContent).toContain("runner accepted a job")
    expect(container.textContent).not.toContain("Runner logs")
    const eventTabs = Array.from(container.querySelectorAll('[role="tab"]'))
    expect(eventTabs.map((element) => element.textContent)).toEqual(["All", "control", "stdout", "stderr"])
    expect(eventTabs[0]?.getAttribute("aria-selected")).toBe("true")
    expect(container.textContent).toContain("second output line")
    expect(container.textContent).toContain("setup warning")

    await click(eventTabs[2])
    expect(container.textContent).toContain("second output line")
    expect(container.textContent).not.toContain("setup warning")
    expect(container.textContent).not.toContain("runner accepted a job")
    expect(requestedURLs).toEqual([
      "/runner_requests/101445685709/diagnostics",
      "/runner_requests/101445685709/events",
    ])

    await click(eventTabs[0])
    expect(container.textContent).toContain("runner accepted a job")
    expect(container.textContent).toContain("setup warning")

    const loadEarlierButton = Array.from(container.querySelectorAll("button"))
      .find((element) => element.textContent?.includes("Load earlier records"))
    expect(loadEarlierButton).toBeDefined()
    await click(loadEarlierButton)

    expect(requestedURLs).toEqual([
      "/runner_requests/101445685709/diagnostics",
      "/runner_requests/101445685709/events",
      "/runner_requests/101445685709/events?before_id=3",
    ])
    expect(container.textContent).toContain("runner request created")

    const outputRows = Array.from(container.querySelectorAll("pre"))
      .filter((element) => element.textContent?.includes("output line") || element.textContent?.includes("setup warning"))
    expect(outputRows.map((element) => element.textContent)).toEqual([
      "first output line",
      "second output line",
      "setup warning",
    ])
    expect(outputRows.every((element) => element.parentElement?.classList.contains("grid"))).toBe(true)
    expect(container.textContent).not.toContain("2 lines")
    expect(container.textContent?.match(/first output line/g)).toHaveLength(1)
  })

  test("keeps request details on the page scroll surface without nested vertical scrolling", () => {
    const html = renderToStaticMarkup(createElement(RunnerRequestDiagnosisResult, {
      diagnosis: {
        state: runnerState,
        github_job: { lookup_status: "unavailable" },
        findings: [],
        events: [{
          id: 4143071,
          event_type: "control_log",
          message: "runner accepted a job\n",
          created_at: "2026-09-06T07:05:06Z",
        }],
        events_truncated: false,
      },
      request: async () => "runner accepted a job",
    }))
    const container = document.createElement("div")
    container.innerHTML = html

    expect(container.querySelectorAll(".overflow-auto")).toHaveLength(0)
    expect(container.querySelectorAll('[class*="max-h-"]')).toHaveLength(0)
  })

  test("uses two request-context columns only when the page is wide enough", () => {
    const html = renderToStaticMarkup(createElement(RunnerRequestDiagnosisResult, {
      diagnosis: {
        state: runnerState,
        github_job: { lookup_status: "unavailable" },
        findings: [],
        events: [],
        events_truncated: false,
      },
    }))
    const container = document.createElement("div")
    container.innerHTML = html
    const idLabel = Array.from(container.querySelectorAll(".text-muted-foreground"))
      .find((element) => element.textContent === "ID")
    const detailsGrid = idLabel?.parentElement?.parentElement

    expect(detailsGrid?.classList.contains("grid")).toBe(true)
    expect(detailsGrid?.classList.contains("grid-cols-1")).toBe(true)
    expect(detailsGrid?.classList.contains("xl:grid-cols-2")).toBe(true)
  })

  test("refreshes the diagnosis after retrying a failed request", async () => {
    let status = "failed"
    const request = async () => ({
      state: {
        ...runnerState,
        status,
      },
      github_job: { lookup_status: "unavailable" },
      findings: [],
      events: [],
      events_truncated: false,
    })
    const onRetryRunner = async () => {
      status = "queued"
      return true
    }

    const container = await renderRunnerRequest(request, {
      onRetryRunner,
    })
    expect(container.textContent).toContain("Failed")

    const retryButton = Array.from(container.querySelectorAll("button"))
      .find((element) => element.textContent?.includes("Retry"))
    expect(retryButton).toBeDefined()
    await click(retryButton)

    expect(container.textContent).toContain("Queued")
    expect(container.textContent).not.toContain("Failed")
  })

  test("polls every new event page without reopening exhausted history", async () => {
    let diagnosisRequestCount = 0
    const eventURLs = []
    const request = async (url) => {
      if (url.includes("/events")) {
        eventURLs.push(url)
        if (url.endsWith("/events")) {
          return {
            events: [
              { id: 1, event_type: "control_log", message: "runner started\n", created_at: "2026-09-06T07:05:01Z" },
              { id: 2, event_type: "stdout_log", message: "first output\n", created_at: "2026-09-06T07:05:02Z" },
            ],
            has_more: false,
          }
        }
        if (url.endsWith("/events?after_id=2")) {
          return {
            events: [
              { id: 3, event_type: "stdout_log", message: "second output\n", created_at: "2026-09-06T07:05:03Z" },
              { id: 4, event_type: "stderr_log", message: "warning output\n", created_at: "2026-09-06T07:05:04Z" },
            ],
            has_more: true,
          }
        }
        if (url.endsWith("/events?after_id=4")) {
          return {
            events: [
              { id: 5, event_type: "control_log", message: "runner completed\n", created_at: "2026-09-06T07:05:05Z" },
            ],
            has_more: false,
          }
        }
        return {
          events: [],
          has_more: false,
        }
      }
      diagnosisRequestCount += 1
      return {
        state: {
          ...runnerState,
          status: diagnosisRequestCount === 1 ? "running" : "completed",
        },
        github_job: { lookup_status: "unavailable" },
        findings: [],
        events: [],
        events_truncated: false,
      }
    }

    const container = await renderRunnerRequest(request, { pollIntervalMs: 5 })

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 20))
    })

    expect(diagnosisRequestCount).toBe(2)
    expect(eventURLs).toEqual([
      "/runner_requests/101445685709/events",
      "/runner_requests/101445685709/events?after_id=2",
      "/runner_requests/101445685709/events?after_id=4",
    ])
    expect(container.textContent).toContain("Completed")
    expect(container.textContent).not.toContain("Running")
    expect(container.textContent).toContain("first output")
    expect(container.textContent).toContain("second output")
    expect(container.textContent).toContain("warning output")
    expect(container.textContent).toContain("runner completed")
    expect(container.textContent).not.toContain("Load earlier records")
  })

  test("ignores a stale diagnosis after navigating to another request", async () => {
    let resolveFirstRequest
    const resolvedIDs = []
    const request = (url) => {
      if (url.includes("first-runner")) {
        return new Promise((resolve) => {
          resolveFirstRequest = resolve
        })
      }
      return Promise.resolve({
        state: {
          ...runnerState,
          id: "second-request",
          runner_name: "second-runner",
        },
        github_job: { lookup_status: "unavailable" },
        findings: [],
        events: [],
        events_truncated: false,
      })
    }
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })
    const props = {
      request,
      onBackToRunnerRequests() {},
      onResolvedRequestID: (id) => resolvedIDs.push(id),
    }

    await act(async () => root.render(createElement(RunnerRequestSection, {
      ...props,
      identifier: "first-runner",
    })))
    await act(async () => root.render(createElement(RunnerRequestSection, {
      ...props,
      identifier: "second-runner",
    })))

    expect(container.textContent).toContain("second-request")
    expect(resolvedIDs).toEqual(["second-request"])

    await act(async () => {
      resolveFirstRequest({
        state: {
          ...runnerState,
          id: "first-request",
          runner_name: "first-runner",
        },
        github_job: { lookup_status: "unavailable" },
        findings: [],
        events: [],
        events_truncated: false,
      })
      await Promise.resolve()
    })

    expect(container.textContent).toContain("second-request")
    expect(container.textContent).not.toContain("first-request")
    expect(resolvedIDs).toEqual(["second-request"])
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
        state: runnerState,
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
        state: runnerState,
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
            completed_at: "2026-09-06T07:25:09Z",
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
      expect(html).toContain("完成时间")
      expect(html).not.toContain("已完成2026")
    } finally {
      await i18n.changeLanguage("en")
    }
  })
})
