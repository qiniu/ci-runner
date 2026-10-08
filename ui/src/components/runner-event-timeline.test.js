import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { Window } from "happy-dom"
import { act, createElement } from "react"
import { createRoot } from "react-dom/client"

import i18n from "../i18n"
import { RunnerEventTimeline } from "./runner-event-timeline"

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

function event(id, type = "control_log", message = `event ${id}\n`) {
  return { id, event_type: type, stage: "runner_hook", message, created_at: `2026-09-29T03:51:0${id}.123Z` }
}

async function mount(props) {
  await i18n.changeLanguage("en")
  const container = document.createElement("div")
  document.body.append(container)
  const root = createRoot(container)
  mountedRoots.push({ root, container })
  const render = async (next) => { await act(async () => root.render(createElement(RunnerEventTimeline, next))) }
  await render(props)
  return { container, render }
}

function button(container, label) {
  return Array.from(container.querySelectorAll("button")).find((node) => node.textContent === label)
}

async function click(node) {
  expect(node).toBeDefined()
  await act(async () => {
    node.focus()
    node.dispatchEvent(new window.MouseEvent("mousedown", { bubbles: true, cancelable: true, button: 0 }))
    node.click()
  })
}

function ids(container) {
  return Array.from(container.querySelectorAll("[data-runner-event-id]")).map((node) => Number(node.dataset.runnerEventId))
}

describe("shared Runner event timeline", () => {
  test("merges older history and every new page without duplicates or lost cursors", async () => {
    const endpoint = "/user/runner_requests/job-1/events"
    const urls = []
    let resolveEarlier
    const request = async (url) => {
      urls.push(url)
      if (url === endpoint) return { events: [event(3, "stdout_log"), event(4, "stderr_log")], has_more: true }
      if (url.endsWith("before_id=3")) return new Promise((resolve) => { resolveEarlier = resolve })
      if (url.endsWith("after_id=4")) return { events: [event(5)], has_more: true }
      if (url.endsWith("after_id=5")) return { events: [event(6)], has_more: false }
      return { events: [], has_more: false }
    }
    const { container } = await mount({ endpoint, request })
    expect(ids(container)).toEqual([3, 4])
    await click(button(container, "Load earlier records"))
    await click(button(container, "Refresh"))
    expect(ids(container)).toEqual([3, 4, 5, 6])
    await act(async () => resolveEarlier({ events: [event(1), event(2), event(3, "stdout_log")], has_more: false }))
    expect(ids(container)).toEqual([1, 2, 3, 4, 5, 6])
    expect(button(container, "Load earlier records")).toBeUndefined()
    await click(button(container, "Refresh"))
    expect(urls).toEqual([endpoint, `${endpoint}?before_id=3`, `${endpoint}?after_id=4`, `${endpoint}?after_id=5`, `${endpoint}?after_id=6`])
    await click(button(container, "stderr"))
    expect(ids(container)).toEqual([4])
    expect(container.querySelector("time").textContent).toContain("123")
    expect(container.textContent).toContain("stderr · runner_hook")
    await click(button(container, "All"))
    expect(ids(container)).toHaveLength(6)
    expect(urls).toHaveLength(5)
  })

  test("keeps raw messages and rejects pending old responses after switching Jobs", async () => {
    let resolveFirst
    let resolveEarlier
    const request = async (url) => {
      if (url === "/first/events") return new Promise((resolve) => { resolveFirst = resolve })
      if (url === "/second/events") return { events: [event(3)], has_more: true }
      if (url.includes("before_id")) return new Promise((resolve) => { resolveEarlier = resolve })
      return { events: [event(4, "stdout_log", "##[group]raw\n\n##[endgroup]\n")], has_more: false }
    }
    const { container, render } = await mount({ endpoint: "/first/events", request })
    await render({ endpoint: "/second/events", request })
    await click(button(container, "Load earlier records"))
    await render({ endpoint: "/third/events", request })
    await act(async () => {
      resolveFirst({ events: [event(1)], has_more: true })
      resolveEarlier({ events: [event(2)], has_more: false })
    })
    expect(ids(container)).toEqual([4])
    expect(container.querySelector("pre").textContent).toBe("##[group]raw\n\n##[endgroup]")
    expect(button(container, "Load earlier records")).toBeUndefined()
  })

  test("reports an invalid page instead of claiming the history is empty", async () => {
    let calls = 0
    const request = async () => ++calls === 1 ? "<html>outdated server</html>" : { events: [event(1)], has_more: false }
    const { container } = await mount({ endpoint: "/user/runner_requests/job/events", request })
    expect(container.textContent).toContain("Failed to load Run history.")
    expect(container.textContent).not.toContain("No persisted Run history.")
    await click(button(container, "Refresh"))
    expect(ids(container)).toEqual([1])
    expect(container.textContent).not.toContain("Failed to load Run history.")
  })

  test("recovers incremental polling from failure and stops polling when inactive", async () => {
    const endpoint = "/user/runner_requests/live/events"
    let calls = 0
    let completed = false
    const request = async () => {
      calls += 1
      if (completed) return { events: [event(3)], has_more: false }
      if (calls === 1) return { events: [event(1)], has_more: false }
      if (calls === 2) throw new Error("temporary failure")
      return { events: [event(2)], has_more: false }
    }
    const props = { endpoint, request, autoRefresh: true, pollIntervalMs: 5 }
    const { container, render } = await mount(props)
    for (let attempt = 0; attempt < 50 && !ids(container).includes(2); attempt += 1) {
      await act(async () => { await new Promise((resolve) => setTimeout(resolve, 5)) })
    }
    expect(calls).toBeGreaterThanOrEqual(3)
    expect(ids(container)).toEqual([1, 2])
    expect(container.textContent).not.toContain("temporary failure")
    completed = true
    await render({ ...props, autoRefresh: false })
    expect(ids(container)).toEqual([1, 2, 3])
    const stoppedCalls = calls
    await act(async () => { await new Promise((resolve) => setTimeout(resolve, 15)) })
    expect(calls).toBe(stoppedCalls)
  })
})
