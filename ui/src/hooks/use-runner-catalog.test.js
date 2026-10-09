import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { Window } from "happy-dom"
import { act, createElement } from "react"
import { createRoot } from "react-dom/client"
import { toast } from "sonner"

import "../i18n"
import { useRunnerCatalog } from "./use-runner-catalog"

const window = new Window({ url: "http://localhost/" })
const domGlobals = {
  window,
  document: window.document,
  navigator: window.navigator,
  HTMLElement: window.HTMLElement,
  Node: window.Node,
  Event: window.Event,
  IS_REACT_ACT_ENVIRONMENT: true,
}
const originalGlobalDescriptors = new Map(Object.keys(domGlobals).map((key) => [
  key,
  Object.getOwnPropertyDescriptor(globalThis, key),
]))
for (const [key, value] of Object.entries(domGlobals)) {
  Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
}

const mountedRoots = []

afterEach(async () => {
  toast.dismiss()
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

function mountRunnerCatalog(request, loadAll) {
  let currentCatalog
  function Harness() {
    currentCatalog = useRunnerCatalog({
      request,
      loadAll,
      setSection: () => {},
      parseLabels: (value) => value.split(",").map((label) => label.trim()).filter(Boolean),
    })
    return createElement("div", { "data-open": String(currentCatalog.runnerSpecOpen) })
  }

  const container = document.createElement("div")
  document.body.append(container)
  const root = createRoot(container)
  mountedRoots.push({ root, container })
  act(() => root.render(createElement(Harness)))
  return () => currentCatalog
}

describe("useRunnerCatalog", () => {
  test("keeps the dialog open and shows only the backend error when save is rejected", async () => {
    const validationMessage = "profile name must not contain '/' or be '.' or '..'"
    let loadCount = 0
    const getCatalog = mountRunnerCatalog(
      async () => {
        throw new Error(validationMessage)
      },
      async () => {
        loadCount += 1
      },
    )
    await act(async () => {
      const catalog = getCatalog()
      catalog.setRunnerSpecForm({
        name: "owner/spec",
        labels: "self-hosted,path-name",
        required_labels: "path-name",
        template: "path-name-template",
        published: false,
        runner_group: "",
        max_concurrency: "10",
        min_idle: "0",
        priority: "0",
        enabled: true,
      })
      catalog.setRunnerSpecOpen(true)
    })

    await act(async () => {
      await getCatalog().saveRunnerSpec({ preventDefault: () => {} })
    })

    expect(getCatalog().runnerSpecOpen).toBe(true)
    expect(loadCount).toBe(0)
    const activeToasts = toast.getToasts()
    expect(activeToasts).toHaveLength(1)
    expect(activeToasts[0].type).toBe("error")
    expect(activeToasts[0].title).toBe(validationMessage)
    expect(activeToasts.some((item) => item.type === "success")).toBe(false)
  })
  test("blocks duplicate saves while validation is pending and allows retry after rejection", async () => {
    let rejectRequest
    let requests = 0
    const getCatalog = mountRunnerCatalog(() => {
      requests += 1
      if (requests === 1) return new Promise((_, reject) => { rejectRequest = reject })
      return Promise.resolve({ template_source: "private", template_id: "fixed-id", default_template_name: "" })
    }, async () => {})
    act(() => { getCatalog().setRunnerSpecForm((form) => ({ ...form, name: "new-spec", template: "fixed-id" })); getCatalog().setRunnerSpecOpen(true) })
    let pending
    act(() => {
      pending = getCatalog().saveRunnerSpec({ preventDefault() {} })
      void getCatalog().saveRunnerSpec({ preventDefault() {} })
    })
    expect(getCatalog().savingRunnerSpec).toBe(true)
    expect(requests).toBe(1)
    await act(async () => {
      rejectRequest(new Error("Template has no usable default build"))
      await pending
    })
    expect(getCatalog().savingRunnerSpec).toBe(false)
    expect(getCatalog().runnerSpecOpen).toBe(true)
    await act(async () => { await getCatalog().saveRunnerSpec({ preventDefault() {} }) })
    expect(requests).toBe(3)
    expect(getCatalog().savingRunnerSpec).toBe(false)
    expect(getCatalog().runnerSpecOpen).toBe(false)
  })

})
