import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { Window } from "happy-dom"
import { act, createElement } from "react"
import { createRoot } from "react-dom/client"

import "../i18n"
import { ForkSponsorshipSection } from "./fork-sponsorship-section"

const window = new Window({ url: "http://localhost/" })
const domGlobals = { window, document: window.document, navigator: window.navigator, HTMLElement: window.HTMLElement, SVGElement: window.SVGElement, Node: window.Node, DocumentFragment: window.DocumentFragment, Event: window.Event, MouseEvent: window.MouseEvent, getComputedStyle: window.getComputedStyle.bind(window), requestAnimationFrame: window.requestAnimationFrame.bind(window), cancelAnimationFrame: window.cancelAnimationFrame.bind(window), IS_REACT_ACT_ENVIRONMENT: true }
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

async function settle() {
  await act(async () => { await Promise.resolve(); await Promise.resolve() })
}

describe("ForkSponsorshipSection", () => {
  test("loads organization policies and exact approvals from the selected installation", async () => {
    const requested = []
    const request = async (url) => {
      requested.push(url)
      return {
        items: [{
          sponsor_installation_id: 989,
          source_repository_id: 300,
          source_repository_full_name: "qiniu/project",
          mode: "approval_required",
          enabled: true,
          max_concurrency: 2,
          approvals: [{
            sponsor_installation_id: 989,
            source_repository_id: 300,
            fork_repository_id: 400,
            fork_repository_full_name: "miclle/project",
            fork_owner_id: 500,
            fork_owner_login: "miclle",
          }],
        }],
      }
    }
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })

    await act(async () => root.render(createElement(ForkSponsorshipSection, { request, installationID: 989 })))
    await settle()

    expect(requested).toEqual(["/user/fork-sponsorship-policies?installation_id=989"])
    expect(container.textContent).toContain("qiniu/project")
    expect(container.textContent).toContain("miclle/project")
    expect(container.textContent).toContain("Exact approvals are required")
  })
})
