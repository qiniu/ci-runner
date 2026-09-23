import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { Window } from "happy-dom"
import { act, createElement } from "react"
import { createRoot } from "react-dom/client"
import { renderToStaticMarkup } from "react-dom/server"

import {
  GitHubAppAccountDetail,
  GitHubAppAccountsList,
  GitHubAppAccountsSection,
} from "./github-app-accounts-section"

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

const installation = {
  id: 987,
  account_id: 9001,
  account_type: "organization",
  account_login: "octo-org",
  account_name: "Octo Organization",
  account_avatar: "https://avatars.example/o.png",
}

describe("GitHubAppAccountsSection", () => {
  test("renders installed accounts as accessible detail links", () => {
    const html = renderToStaticMarkup(createElement(GitHubAppAccountsList, {
      installations: [installation],
      query: "",
      loading: false,
      error: "",
      onQueryChange: () => {},
      onRefresh: () => {},
      onOpen: () => {},
    }))

    expect(html).toContain("GitHub App accounts")
    expect(html).toContain("octo-org")
    expect(html).toContain("Octo Organization")
    expect(html).toContain("Organization")
    expect(html).toContain("Installation #987")
    expect(html).toContain('href="/admin/github_accounts/987"')
    expect(html).toContain('aria-label="Refresh GitHub App accounts"')
  })

  test("renders the installation repository detail", () => {
    const html = renderToStaticMarkup(createElement(GitHubAppAccountDetail, {
      detail: {
        installation,
        repositories: ["octo-org/runner", "octo-org/api"],
      },
      loading: false,
      error: "",
      onBack: () => {},
      onRefresh: () => {},
    }))

    expect(html).toContain("Back to GitHub App accounts")
    expect(html).toContain("2 repositories")
    expect(html).toContain("octo-org/runner")
    expect(html).toContain('href="https://github.com/octo-org/runner"')
    expect(html).toContain('aria-label="Refresh repositories"')
  })

  test("renders intentional empty states", () => {
    const listHTML = renderToStaticMarkup(createElement(GitHubAppAccountsList, {
      installations: [],
      query: "",
      loading: false,
      error: "",
      onQueryChange: () => {},
      onRefresh: () => {},
      onOpen: () => {},
    }))
    const detailHTML = renderToStaticMarkup(createElement(GitHubAppAccountDetail, {
      detail: { installation, repositories: [] },
      loading: false,
      error: "",
      onBack: () => {},
      onRefresh: () => {},
    }))

    expect(listHTML).toContain("No GitHub App installations")
    expect(detailHTML).toContain("No repositories authorized")
  })

  test("does not present a failed list request as an empty installation catalog", async () => {
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })

    await act(async () => root.render(createElement(GitHubAppAccountsSection, {
      installationID: 0,
      request: async () => { throw new Error("upstream unavailable") },
      onOpen() {},
      onBack() {},
    })))
    await act(async () => {
      await Promise.resolve()
    })

    expect(container.querySelector('[role="alert"]')).not.toBeNull()
    expect(container.textContent).toContain("Failed to load GitHub App accounts")
    expect(container.textContent).not.toContain("No GitHub App installations")
  })

  test("ignores a delayed repository response after navigating to another installation", async () => {
    const pending = new Map()
    const request = (url) => new Promise((resolve) => pending.set(url, resolve))
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })
    const props = {
      request,
      onOpen() {},
      onBack() {},
    }

    await act(async () => root.render(createElement(GitHubAppAccountsSection, { ...props, installationID: 1 })))
    expect(pending.has("/admin/api/github-app/installations/1/repositories")).toBe(true)

    await act(async () => root.render(createElement(GitHubAppAccountsSection, { ...props, installationID: 2 })))
    expect(pending.has("/admin/api/github-app/installations/2/repositories")).toBe(true)

    await act(async () => {
      pending.get("/admin/api/github-app/installations/2/repositories")({
        installation: { ...installation, id: 2, account_login: "second-org" },
        repositories: ["second-org/api"],
      })
      await Promise.resolve()
    })
    expect(container.textContent).toContain("second-org")
    expect(container.textContent).toContain("second-org/api")

    await act(async () => {
      pending.get("/admin/api/github-app/installations/1/repositories")({
        installation: { ...installation, id: 1, account_login: "first-org" },
        repositories: ["first-org/runner"],
      })
      await Promise.resolve()
    })
    expect(container.textContent).toContain("second-org")
    expect(container.textContent).not.toContain("first-org")
  })
})
