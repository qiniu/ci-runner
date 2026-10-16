import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { act, createElement } from "react"
import { Window } from "happy-dom"
import { createRoot } from "react-dom/client"
import { renderToStaticMarkup } from "react-dom/server"

import "../i18n"
import { UserDeploymentsSection } from "./user-deployments-section"
import { UserDeploymentCreateSection } from "./user-deployment-create-section"

function renderPage(overrides = {}) {
  return renderToStaticMarkup(createElement(UserDeploymentCreateSection, {
    githubApp: {
      setup_url: "/github-app/setup",
      installations: [{
        id: 12,
        account_id: 4,
        installation_id: 9001,
        account_login: "miclle",
        account_type: "User",
        repositories: ["miclle/demo"],
        manageable: true,
        created_at: "2026-10-01T00:00:00Z",
        updated_at: "2026-10-01T00:00:00Z",
      }],
    },
    currentLogin: "miclle",
    loadingGitHubApp: false,
    request: async () => ({ items: [] }),
    onOpenDeployment: () => {},
    ...overrides,
  }))
}

describe("UserDeploymentCreateSection", () => {
  test("renders the deployment workflow and keeps repository data in the form", () => {
    const html = renderPage()

    expect(html).toContain("Create deployment")
    expect(html.match(/role="combobox"/g)).toHaveLength(5)
    expect(html).toContain(".qiniu/deploy.yaml")
    expect(html).toContain("Version source")
    expect(html).toContain("Select branch")
  })

  test("does not expose credentials in the deployment surface", () => {
    const html = renderPage()

    expect(html).not.toContain("api_key")
    expect(html).not.toContain("Sandbox API key")
    expect(html).not.toContain("secret_access_key")
  })
})

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
	window.history.replaceState(null, "", "/")
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

const installation = (id, login = "miclle") => ({ id, account_login: login, account_type: "User", repositories: [`${login}/demo`], manageable: true })
const deployment = (id, status = "running") => ({ id, repository_full_name: "miclle/demo", commit_sha: "a".repeat(40), config_path: ".qiniu/deploy.yaml", status, template_id: "public-base", template_name: "base", template_cpu: 4, template_memory_mb: 8192, template_disk_mb: 40960, created_at: "2026-10-13T00:00:00Z", expires_at: "2026-10-13T01:00:00Z" })
function navigationPage(items) {
  const groups = new Map()
  for (const item of items) {
    const group = groups.get(item.repository_full_name) || { repository_full_name: item.repository_full_name, total: 0, active_count: 0, active_deployments: [] }
    group.total++
    if (["queued", "planning", "creating", "running", "stopping"].includes(item.status)) { group.active_count++; group.active_deployments.push(item) }
    groups.set(item.repository_full_name, group)
  }
  return { items: [...groups.values()], has_more: false }
}
const event = (id, message) => ({ id, event_type: "output", stage: "installing", stream: "stdout", message, created_at: "2026-10-13T00:00:00Z" })
async function mountPage(props, { chooseRepository = true, page = "create" } = {}) {
  const container = document.createElement("div")
  document.body.append(container)
  const root = createRoot(container)
  mountedRoots.push({ root, container })
  const base = { githubApp: { installations: [installation(12)] }, currentLogin: "miclle", loadingGitHubApp: false, onOpenDeployment: () => {}, ...props }
  await act(async () => root.render(createElement(page === "create" ? UserDeploymentCreateSection : UserDeploymentsSection, base)))
  await settle()
  const repository = container.querySelector('select[name="deployment-repository"]')
  if (chooseRepository && repository) {
    await selectOption(container, "#deployment-repository", [...repository.options].find((option) => option.value)?.value ?? "")
    await settle()
  }
  return { root, container, props: base }
}
function button(container, name) { return [...container.querySelectorAll("button")].find((item) => item.textContent.trim() === name) }
async function selectRevisionURL(number) {
  await act(async () => {
    const url = new URL(window.location.href)
    url.searchParams.set("revision", String(number))
    window.history.pushState(null, "", url)
    window.dispatchEvent(new window.PopStateEvent("popstate"))
  })
  await settle()
}
async function selectOption(container, id, value) {
  await act(async () => {
    // Exercise Radix's native form bridge here; production smoke covers the popup.
    const select = container.querySelector(`select[name="${id.slice(1)}"]`)
    select.value = value
    select.dispatchEvent(new Event("change", { bubbles: true }))
  })
}

describe("deployment interaction", () => {
  test("uses saved revision commits for links and copying without putting them in the URL", async () => {
    const sha = "b".repeat(40)
    const revision = { ...deployment("d1-r1", "completed"), number: 1, ref: "refs/heads/example/vite", commit_sha: sha, source_repository_full_name: "contributor/fork" }
    const endpoint = "https://deployment-preview.example.test"
    const saved = { ...deployment("d1", "completed"), endpoint, endpoint_access: "public", source: "branch", ref: "example/vite", revision_number: 2, revisions: [revision], selected_revision: revision }
    window.history.replaceState(null, "", `/github/miclle/demo/deployments/d1?branch=example/vite&commit=${"c".repeat(40)}&revision=1`)
    const request = async (url) => url.includes("/events") ? { events: [], has_more: false } : url.includes("/resources") ? {} : saved
    let copied
    const previous = Object.getOwnPropertyDescriptor(navigator, "clipboard")
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async (text) => { copied = text } } })
    try {
      const { container } = await mountPage({ request, deploymentID: "d1" }, { page: "deployments" })
      expect(window.location.pathname).toBe("/github/miclle/demo/deployments/d1")
      expect(window.location.search).toBe("?branch=example/vite&revision=1")
      const link = container.querySelector(`a[href="https://github.com/contributor/fork/commit/${sha}"]`)
      expect(link.textContent).toBe("bbbbbbbb")
      expect(link.title).toBe(sha)
      await act(async () => container.querySelectorAll('button[aria-label="Copy full commit SHA"]').item(1).click())
      expect(copied).toBe(sha)
      await act(async () => container.querySelector('button[aria-label="Copy access URL"]').click())
      expect(copied).toBe(endpoint)
      const openEndpoint = container.querySelector('a[aria-label="Open access URL in a new window"]')
      expect(openEndpoint.href).toBe(`${endpoint}/`)
      expect(openEndpoint.target).toBe("_blank")
      expect(openEndpoint.rel).toBe("noopener noreferrer")
      expect(container.textContent).toContain("This address is public. Protect sensitive data in your application.")
      expect(container.querySelector('[data-slot="card-title"] a')?.getAttribute("href")).toBe("https://github.com/miclle/demo/tree/example/vite")
    } finally {
      if (previous) Object.defineProperty(navigator, "clipboard", previous)
      else Reflect.deleteProperty(navigator, "clipboard")
    }
  })

  test("shows the persisted GitHub pull request title beside the source and status", async () => {
    const saved = { ...deployment("pr-title", "running"), source: "pull_request", ref: "7", pull_request_number: 7, pull_request_title: "Preview deployment", revision_number: 1 }
    const request = async (url) => url.includes("/events") ? { events: [], has_more: false } : url.includes("/resources") ? {} : saved
    const { container } = await mountPage({ request, deploymentID: saved.id }, { page: "deployments" })
    expect(container.textContent).toContain("Pull request #7 · Preview deployment")
    const title = container.querySelector('[data-slot="card-title"]')
    expect(title?.textContent).toContain("Pull request #7 · Preview deployment")
    expect(title?.nextElementSibling?.textContent).toBe("Running")
    expect(title?.querySelector("a")?.getAttribute("href")).toBe("https://github.com/miclle/demo/pull/7")
  })

  test("renders legacy PR details and logs before optional GitHub title metadata", async () => {
    let finishTitle
    const pendingTitle = new Promise((resolve) => { finishTitle = resolve })
    const saved = { ...deployment("legacy-pr", "completed"), source: "pull_request", ref: "7", pull_request_number: 7, revision_number: 2, revisions: [{ ...deployment("legacy-pr-r1", "completed"), ref: "refs/pull/7/head", number: 1, pull_request_title: "Historical title" }] }
    const request = async (url) => url.endsWith("/pull-request") ? pendingTitle : url.includes("/events") ? { events: [event(1, "local saved logs")], has_more: false } : url.includes("/resources") ? {} : saved
    const { container } = await mountPage({ request, deploymentID: saved.id }, { page: "deployments" })
    expect(container.textContent).toContain("local saved logs")
    const title = () => container.querySelector('[data-slot="card-title"]')
    expect(title().textContent).toBe("Pull request #7")
    await act(async () => finishTitle({ pull_request_number: 7, pull_request_title: "Add real GitHub title" }))
    expect(title().textContent).toBe("Pull request #7 · Add real GitHub title")
    saved.pull_request_title = "Add real GitHub title"
    await selectRevisionURL(1)
    expect(title().textContent).not.toContain("Historical title")
  })

  test.each(["unavailable", "empty"])("retries %s PR title metadata on explicit page refresh", async (failure) => {
    const saved = { ...deployment("retry-pr-title", "completed"), source: "pull_request", ref: "7", pull_request_number: 7, revision_number: 1 }
    let titleReads = 0
    const request = async (url) => {
      if (url.startsWith("/user/deployment-repositories")) return navigationPage([saved])
      if (url.endsWith("/pull-request")) {
        titleReads++
        if (titleReads === 1) {
          if (failure === "unavailable") throw new Error("temporary metadata outage")
          return { pull_request_number: 7, pull_request_title: "" }
        }
        saved.pull_request_title = "Recovered PR title"
        return { pull_request_number: 7, pull_request_title: saved.pull_request_title }
      }
      if (url.includes("/events")) return { events: [event(1, "local logs stay visible")], has_more: false }
      if (url.includes("/resources")) return {}
      return { ...saved }
    }
    const { container } = await mountPage({ request, deploymentID: saved.id }, { page: "deployments" })
    expect(titleReads).toBe(1)
    expect(container.textContent).toContain("local logs stay visible")
    await act(async () => button(container, "Refresh").click())
    await settle()
    expect(titleReads).toBe(2)
    expect(container.querySelector('[data-slot="card-title"]').textContent).toBe("Pull request #7 · Recovered PR title")
    expect(container.textContent).toContain("local logs stay visible")
  })

  test("does not carry a recovered PR title into a later revision", async () => {
    let revision = 1
    let finishTitle
    const pendingTitle = new Promise((resolve) => { finishTitle = resolve })
    const request = async (url) => {
      if (url.startsWith("/user/deployment-repositories")) return { items: [] }
      if (url.endsWith("/pull-request")) return pendingTitle
      if (url.includes("/events")) return { events: [], has_more: false }
      if (url.includes("/resources")) return {}
      return { ...deployment("next-pr-title", "completed"), source: "pull_request", ref: "7", pull_request_number: 7, pull_request_title: revision === 1 ? "Old revision title" : undefined, revision_number: revision }
    }
    const { container } = await mountPage({ request, deploymentID: "next-pr-title" }, { page: "deployments" })
    const title = () => container.querySelector('[data-slot="card-title"]').textContent
    expect(title()).toContain("Old revision title")
    revision = 2
    await act(async () => button(container, "Refresh").click())
    await settle()
    expect(title()).toBe("Pull request #7")
    await act(async () => finishTitle({ pull_request_number: 7, pull_request_title: "New revision title" }))
    expect(title()).toBe("Pull request #7 · New revision title")
  })

  test.each([
    ["branch", "feature/a#b?c", "https://github.com/miclle/demo/tree/feature/a%23b%3Fc"],
    ["tag", "release/v1", "https://github.com/miclle/demo/tree/refs/tags/release/v1"],
  ])("links the %s heading to the exact GitHub ref", async (source, ref, href) => {
    const saved = { ...deployment("source-link", "completed"), source, ref }
    const request = async (url) => url.includes("/events") ? { events: [], has_more: false } : url.includes("/resources") ? {} : saved
    const { container } = await mountPage({ request, deploymentID: saved.id }, { page: "deployments" })
    const link = container.querySelector('[data-slot="card-title"] a')
    expect(link.getAttribute("href")).toBe(href)
    expect(link.getAttribute("rel")).toBe("noopener noreferrer")
    expect(link.getAttribute("target")).toBe("_blank")
  })
	 test("switches immutable revisions, rejects delayed logs, and restores the URL selection", async () => {
		let finishLatest
		const latestOutput = new Promise((resolve) => { finishLatest = resolve })
		const revisions = [2, 1].map((number) => ({ ...deployment(`versioned-r${number}`, "completed"), number, ref: "refs/heads/main" }))
		const requests = []
		const request = async (url) => {
			requests.push(url)
			if (url.startsWith("/user/deployments/versioned") && !url.includes("/events")) return { ...deployment("versioned", "completed"), revision_number: 2, revisions }
			if (url.includes("/revisions/2/events")) return latestOutput
			if (url.includes("/revisions/1/events")) return { events: [event(1, "first version retained")], has_more: false }
			throw new Error(`unexpected request ${url}`)
		}
		const { container } = await mountPage({ request, deploymentID: "versioned" }, { page: "deployments" })
		expect(requests.filter((url) => url.endsWith("/events"))).toEqual(["/user/deployments/versioned/revisions/2/events"])
		await selectRevisionURL(1)
		expect(new URLSearchParams(window.location.search).get("revision")).toBe("1")
		expect(container.textContent).toContain("first version retained")
		await act(async () => finishLatest({ events: [event(10, "delayed second version")], has_more: false }))
		expect(container.textContent).not.toContain("delayed second version")
		expect(container.querySelector('[data-testid="deployment-logs"]').querySelector("time").textContent).toMatch(/\d{2}:\d{2}:\d{2}\.\d{3}/)
		await act(async () => button(container, "Refresh").click())
		expect(container.querySelector('[data-testid="deployment-historical-notice"]')?.textContent).toContain("Viewing historical revision #1")
		const restored = await mountPage({ request, deploymentID: "versioned" }, { page: "deployments" })
		expect(restored.container.textContent).toContain("first version retained")
		expect(restored.container.textContent).not.toContain("delayed second version")
	})
  test("keeps the current overview and resources while browsing history, and returns to current logs", async () => {
    const revisions = [2, 1].map((number) => ({ ...deployment(`history-r${number}`, number === 2 ? "running" : "completed"), number, ref: "refs/heads/main", template_name: number === 2 ? "base" : "old-template" }))
    const current = { ...deployment("history", "completed"), source: "branch", ref: "main", revision_number: 2, sandbox_id: "current-sandbox", revisions }
    const calls = []
    const request = async (url) => {
      calls.push(url)
      if (url.startsWith("/user/deployment-repositories")) return navigationPage([current, { ...deployment("other", "queued"), source: "pull_request", pull_request_number: 7 }])
      if (url === "/user/deployments") return { items: [current] }
      if (url.includes("/events")) return { events: [event(url.includes("/revisions/1/") ? 1 : 2, url.includes("/revisions/1/") ? "historical log" : "current log")], has_more: false }
      if (url.includes("/resources")) return { template_name: "current-template", sandbox_id: "current-sandbox" }
      return current
    }
    const { container } = await mountPage({ request, deploymentID: "history" }, { page: "deployments" })
    const navigation = container.querySelector('[data-testid="deployment-navigation"]')
    expect(navigation.querySelector('a[aria-current="page"]').href).toContain("/deployments/history?branch=main")
    expect(navigation.textContent).toContain("PR #7")
    expect(container.querySelector('[data-testid="deployment-history"]').hidden).toBe(true)
    expect([...container.querySelectorAll('[role="tab"]')].map((item) => item.textContent)).toEqual(["Deployment logs", "Terminal", "Deployment history"])
    await selectRevisionURL(1)
    expect(container.textContent).toContain("Viewing historical revision #1")
    expect(container.textContent).toContain("historical log")
    expect(container.textContent).toContain("current-template")
    expect(container.textContent).not.toContain("old-template")
    expect(calls.filter((url) => url.includes("/resources"))).toEqual(["/user/deployments/history/resources?revision=2"])
    expect(container.textContent).toContain("Current deployment · Revision #2")
    await act(async () => button(container, "Return to current revision").click())
    expect(new URLSearchParams(window.location.search).has("revision")).toBe(false)
    expect(container.querySelector('[data-testid="deployment-historical-notice"]')).toBeNull()
    expect(container.textContent).toContain("current log")
  })

  test("renders a direct detail before sidebar loading finishes and discards a previous account's delayed list", async () => {
    let finishList
    const delayedList = new Promise((resolve) => { finishList = resolve })
    const request = async (url) => url.startsWith("/user/deployment-repositories") ? delayedList : url.includes("/events") ? { events: [event(1, "ready log")], has_more: false } : url.includes("/resources") ? {} : deployment("direct", "completed")
    const mounted = await mountPage({ request, deploymentID: "direct" }, { page: "deployments" })
    expect(mounted.container.textContent).toContain("ready log")
    expect(mounted.container.querySelector('a[aria-current="page"]')).not.toBeNull()
    const nextRequest = async (url) => url.startsWith("/user/deployment-repositories") ? { items: [] } : url.includes("/events") ? { events: [], has_more: false } : url.includes("/resources") ? {} : { ...deployment("direct", "completed"), repository_full_name: "next/demo" }
    await act(async () => mounted.root.render(createElement(UserDeploymentsSection, { ...mounted.props, currentLogin: "next", request: nextRequest })))
    await settle()
    await act(async () => finishList(navigationPage([{ ...deployment("private"), repository_full_name: "previous/private" }])))
    expect(mounted.container.querySelector('[data-testid="deployment-navigation"]').textContent).not.toContain("previous/private")
    expect(mounted.container.querySelector('[data-testid="deployment-navigation"]').textContent).toContain("next/demo")
  })

  test("shows only the list and a creation link without loading form catalogs", async () => {
    const requested = []
    const request = async (url) => {
      requested.push(url)
      if (url.startsWith("/user/deployment-repositories")) return navigationPage([deployment("existing", "running")])
      if (url !== "/user/deployments?status=running") throw new Error(`list requested a form dependency: ${url}`)
      return { items: [deployment("existing", "running")] }
    }
    const { container } = await mountPage({ request }, { page: "deployments" })
    expect(requested).toEqual(["/user/deployment-repositories?limit=100&offset=0", "/user/deployments?status=running"])
    expect(container.querySelector("form")).toBeNull()
    expect(container.querySelector('a[href="/deployments/new"]').textContent).toContain("Create deployment")
    expect(container.querySelector('a[href="/deployments/existing"]').textContent).toContain("miclle/demo")
    expect(container.querySelector('[data-testid="deployment-logs"]')).toBeNull()
  })

  test("navigates deployment links without a reload while preserving native modified clicks", async () => {
    window.history.replaceState(null, "", "/deployments")
    const { container } = await mountPage({ request: async (url) => url.startsWith("/user/deployment-repositories") ? navigationPage([deployment("existing", "completed")]) : ({ items: [deployment("existing", "completed")] }) }, { page: "deployments" })
    const createLink = container.querySelector('a[href="/deployments/new"]')
    const locations = []
    const onHistory = () => locations.push(window.location.pathname)
    window.addEventListener("popstate", onHistory)
    try {
      for (const options of [{ ctrlKey: true }, { metaKey: true }, { shiftKey: true }, { altKey: true }, { button: 1 }]) {
        const click = new MouseEvent("click", { bubbles: true, cancelable: true, ...options })
        await act(async () => createLink.dispatchEvent(click))
        expect(click.defaultPrevented).toBe(false)
      }
      expect(locations).toEqual([])
      window.history.replaceState(null, "", "/deployments")
      for (const link of [createLink, container.querySelector('a[href="/deployments/existing"]')]) {
        const click = new MouseEvent("click", { bubbles: true, cancelable: true })
        await act(async () => link.dispatchEvent(click))
        expect(click.defaultPrevented).toBe(true)
      }
      expect(locations).toEqual(["/deployments/new", "/deployments/existing"])
    } finally {
      window.removeEventListener("popstate", onHistory)
    }
  })

  test("groups navigation by repository, distinguishes active deployments, and pins a selected terminal deployment", async () => {
    let status = "running"
    const old = { ...deployment("stopped-preview", "completed"), ref: "refs/heads/preview" }
    const running = { ...deployment("running-preview", "running"), ref: "refs/heads/preview" }
    const other = { ...deployment("other-active", "creating"), repository_full_name: "another/project", ref: "refs/pull/17/head", pull_request_title: "Add preview deployment" }
    const request = async (url) => {
      if (url.startsWith("/user/deployment-repositories")) return navigationPage([{ ...running, status }, old, other])
      if (url.includes("/events")) return { events: [], has_more: false }
      if (url.includes("/resources")) return {}
      return { ...running, status }
    }
    const { container } = await mountPage({ request, deploymentID: running.id }, { page: "deployments" })
    const navigation = container.querySelector('[data-testid="deployment-navigation"]')
    expect(navigation.querySelectorAll('[data-testid="deployment-repository"]')).toHaveLength(2)
    expect(navigation.querySelector(`a[aria-current="page"]`).textContent).toContain(running.id.slice(0, 10))
    expect(navigation.textContent).not.toContain("stopped-preview")
    const repositoryLink = navigation.querySelector('a[href="/github/miclle/demo/deployments"]')
    expect(repositoryLink.textContent).toBe("miclle/demo")
    const toggle = [...navigation.querySelectorAll("button")].find((item) => item.getAttribute("aria-label")?.includes("another/project"))
    await act(async () => toggle.click())
    expect(navigation.textContent).toContain("PR #17")
    expect(navigation.textContent).toContain("PR #17 · Add preview deployment")
    status = "completed"
    await act(async () => button(container, "Refresh").click())
    expect(navigation.querySelector('a[aria-current="page"]').textContent).toContain("Stopped")
    expect(navigation.textContent).not.toContain("stopped-preview")
    expect(navigation.querySelector('a[href="/github/miclle/demo/deployments"]')).not.toBeNull()
    const destinations = []
    const observe = () => destinations.push(window.location.pathname + window.location.search)
    window.addEventListener("popstate", observe)
    try {
      await act(async () => container.querySelector('button[aria-label="Back to deployments"]').click())
      expect(destinations).toContain("/github/miclle/demo/deployments")
    } finally { window.removeEventListener("popstate", observe) }
  })

  test("summarizes all active deployments without assigning one child's state to its repository", async () => {
    const records = [
      ["queued", ["queued", "queued"], "Queued"],
      ["creating", ["creating", "creating"], "Deploying"],
      ["running", ["running"], "Deploying"],
      ["mixed", ["running", "queued"], "Active deployments"],
      ["stopping", ["stopping"], "Active deployments"],
      ["archived", ["completed", "failed"], null],
    ]
    const items = records.flatMap(([name, statuses]) => statuses.map((status, index) => ({ ...deployment(`${name}-${index}`, status), repository_full_name: `miclle/${name}` })))
    const { container } = await mountPage({ request: async (url) => url.startsWith("/user/deployment-repositories") ? navigationPage(items) : { items: [] } }, { page: "deployments" })
    const navigation = container.querySelector('[data-testid="deployment-navigation"]')
    for (const [name, statuses, label] of records) {
      const repository = navigation.querySelector(`a[href="/github/miclle/${name}/deployments"]`).closest('[data-testid="deployment-repository"]')
      const badge = repository.querySelector('[data-slot="badge"]')
      expect(badge?.textContent ?? null).toBe(label)
      expect(repository.textContent).toContain(`${statuses.filter((status) => !["completed", "failed"].includes(status)).length} active`)
    }
  })

  test("resolves a direct detail's repository beyond loaded navigation pages", async () => {
    const selected = { ...deployment("deep-selected", "completed"), repository_full_name: "z/selected" }
    const calls = []
    const request = async (url) => {
      calls.push(url)
      if (url.startsWith("/user/deployment-repositories?repository_full_name=")) return navigationPage([selected, { ...selected, id: "other-live", status: "running" }])
      if (url.startsWith("/user/deployment-repositories")) return { ...navigationPage([{ ...selected, id: "a-item", repository_full_name: "a/project" }]), has_more: true }
      if (url.includes("/events")) return { events: [], has_more: false }
      if (url.includes("/resources")) return {}
      return selected
    }
    const { container } = await mountPage({ request, deploymentID: selected.id }, { page: "deployments" })
    const navigation = container.querySelector('[data-testid="deployment-navigation"]')
    expect(calls).toContain("/user/deployment-repositories?repository_full_name=z%2Fselected")
    expect(navigation.querySelector('[data-testid="deployment-repository"]').textContent).toContain("z/selected")
    expect(navigation.querySelector('a[aria-current="page"]').textContent).toContain("deep-selec")
    expect(navigation.textContent).toContain("other-live")
    expect(navigation.textContent).toContain("1 active")
  })

  test("loads independent history and repository pages and retains loaded history after refresh", async () => {
    const first = Array.from({ length: 100 }, (_, index) => deployment(`past-${index}`, "completed"))
    const last = { ...deployment("old-active", "creating"), repository_full_name: "z/older", ref: "refs/heads/main" }
    const requested = []
    const request = async (url) => {
      requested.push(url)
      if (url.startsWith("/user/deployment-repositories")) {
        return { ...navigationPage(url.includes("offset=100") ? [last] : first), has_more: !url.includes("offset=100") }
      }
      return { items: url.includes("offset=100") ? [last] : first, total: 101 }
    }
    const { container } = await mountPage({ request }, { page: "deployments" })
    expect(container.querySelectorAll('[data-testid="deployment-list"] > a')).toHaveLength(100)
    await act(async () => button(container, "Load more repositories").click())
    expect(container.querySelector('[data-testid="deployment-navigation"]').textContent).toContain("z/older")
    expect(container.querySelectorAll('[data-testid="deployment-list"] > a')).toHaveLength(100)
    await act(async () => button(container, "Load more deployments").click())
    expect(container.querySelectorAll('[data-testid="deployment-list"] > a')).toHaveLength(101)
    await act(async () => button(container, "Refresh").click())
    expect(container.querySelectorAll('[data-testid="deployment-list"] > a')).toHaveLength(101)
    expect(requested).toContain("/user/deployments?status=running&limit=100&offset=100")
  })

  test("filters through URL state and server counts, resets paging, and discards the previous status page", async () => {
    window.history.replaceState(null, "", "/github/miclle/demo/deployments?branch=feature/preview&status=stopped")
    let finishOld
    let stopped = false
    let navigationCalls = 0
    const oldPage = new Promise((resolve) => { finishOld = resolve })
    const requests = []
    const request = async (url) => {
      requests.push(url)
      if (url.startsWith("/user/deployment-repositories")) { navigationCalls++; return navigationPage([deployment("live-preview")]) }
      const query = new URL(url, window.location.href).searchParams
      const counts = { all: 107, running: stopped ? 0 : 2, processing: 3, stopped: stopped ? 104 : 102, failed: 0 }
      if (query.get("offset") === "100") return oldPage
      const status = query.get("status")
      const items = status === "stopped" ? Array.from({ length: 100 }, (_, index) => deployment(`past-${index}`, "completed")) : status === "running" && !stopped ? [deployment("live-one"), deployment("live-two")] : []
      return { items, total: counts[status], status_counts: counts }
    }
    const { container } = await mountPage({ request }, { page: "deployments" })
    const filters = container.querySelector('[data-testid="deployment-status-filter"]')
    const filter = (label) => [...filters.querySelectorAll('button')].find((item) => item.textContent.startsWith(label))
    expect(filter("Stopped").getAttribute("aria-checked")).toBe("true")
    expect(filter("All")).toBeUndefined()
    expect(filter("Stopped").textContent).toBe("Stopped102")
    await act(async () => button(container, "Load more deployments").click())
    await act(async () => filter("Running").click())
    expect(filter("Stopped").getAttribute("aria-checked")).toBe("false")
    await settle()
    expect(new URLSearchParams(window.location.search).get("branch")).toBe("feature/preview")
    expect(new URLSearchParams(window.location.search).get("status")).toBeNull()
    expect(filter("Running").getAttribute("aria-checked")).toBe("true")
    expect(container.querySelectorAll('[data-testid="deployment-list"] > a')).toHaveLength(2)
    const selectedURL = window.location.href
    await act(async () => filter("Running").click())
    expect(window.location.href).toBe(selectedURL)
    expect(filter("Running").getAttribute("aria-checked")).toBe("true")
    expect(navigationCalls).toBe(1)
    await act(async () => finishOld({ items: [deployment("stale-page", "completed")], total: 999, status_counts: { all: 999 } }))
    expect(filters.textContent).not.toContain("999")
    expect(container.querySelector('[data-testid="deployment-list"]').textContent).not.toContain("stale-page")
    expect(button(container, "Load more deployments")).toBeUndefined()
    stopped = true
    await act(async () => button(container, "Refresh").click())
    expect(filter("Running").textContent).toBe("Running0")
    expect(container.textContent).toContain("No deployments match these filters")
    expect(container.querySelector('[data-testid="deployment-list"]')).toBeNull()
    expect(requests.some((url) => url.includes("status=running") && url.includes("ref=refs%2Fheads%2Ffeature%2Fpreview"))).toBe(true)
  })

  test("discards an older repository's pending history page on navigation", async () => {
    let finishOld
    const pending = new Promise((resolve) => { finishOld = resolve })
    const request = async (url) => {
      if (url.startsWith("/user/deployment-repositories")) return { items: [], has_more: false }
      if (url.includes("offset=100")) return pending
      const repository = url.includes("new%2Frepo") ? "new/repo" : "miclle/demo"
      return { items: Array.from({ length: repository === "new/repo" ? 1 : 100 }, (_, index) => ({ ...deployment(`item-${index}`, "completed"), repository_full_name: repository })), total: repository === "new/repo" ? 1 : 101 }
    }
    const mounted = await mountPage({ request, locationPath: "/github/miclle/demo/deployments" }, { page: "deployments" })
    await act(async () => button(mounted.container, "Load more deployments").click())
    await act(async () => mounted.root.render(createElement(UserDeploymentsSection, { ...mounted.props, locationPath: "/github/new/repo/deployments" })))
    await settle()
    await act(async () => finishOld({ items: [deployment("stale-private", "completed")], total: 101 }))
    expect(mounted.container.querySelector('[data-testid="deployment-list"]').textContent).toContain("new/repo")
    expect(mounted.container.querySelector('[data-testid="deployment-list"]').textContent).not.toContain("stale-private")
    expect(button(mounted.container, "Load more deployments")).toBeUndefined()
  })

  test("unlocks paging when the request context changes during a pending history page", async () => {
    let finishOld
    const pending = new Promise((resolve) => { finishOld = resolve })
    const first = Array.from({ length: 100 }, (_, index) => deployment(`previous-${index}`, "completed"))
    const request = async (url) => url.startsWith("/user/deployment-repositories") ? { items: [] } : url.includes("offset=100") ? pending : { items: first, total: 101 }
    const mounted = await mountPage({ request }, { page: "deployments" })
    await act(async () => button(mounted.container, "Load more deployments").click())
    const nextRequest = async (url) => url.startsWith("/user/deployment-repositories") ? { items: [] } : { items: [deployment("new-context", "completed")], total: 1 }
    await act(async () => mounted.root.render(createElement(UserDeploymentsSection, { ...mounted.props, request: nextRequest })))
    await settle()
    await act(async () => finishOld({ items: [deployment("stale-page", "completed")], total: 101 }))
    expect(mounted.container.querySelectorAll('[data-testid="deployment-list"] > a')).toHaveLength(1)
    expect(mounted.container.querySelector('[data-testid="deployment-list"]').textContent).toContain("new-contex")
    expect(mounted.container.querySelector('[data-testid="deployment-list"]').textContent).not.toContain("stale-page")
    expect(button(mounted.container, "Load more deployments")).toBeUndefined()
  })

  test("loads only creation dependencies and provides a link back to the list", async () => {
    const requested = []
    const request = async (url) => {
      requested.push(url)
      if (url.includes("deployment-templates")) return { templates: [{ template_id: "public-base" }] }
      if (url.includes("/repositories")) return { repositories: ["miclle/demo"] }
      throw new Error(`unexpected creation request: ${url}`)
    }
    const { container } = await mountPage({ request }, { chooseRepository: false })
    expect(requested).not.toContain("/user/deployments")
    expect(container.querySelector("form")).not.toBeNull()
    expect(container.querySelector('a[href="/deployments"]').textContent).toBe("Back to deployments")
    expect(container.querySelector('[data-testid="deployment-list"]')).toBeNull()
    expect(container.querySelector('[data-testid="deployment-logs"]')).toBeNull()
  })

  test("waits for an explicit repository choice before discovering versions", async () => {
    const requests = []
    const request = async (url) => {
      requests.push(url)
      if (url.includes("deployment-refs")) return { refs: ["main"], has_more: false }
      if (url.includes("deployment-templates")) return { templates: [{ template_id: "public-base" }] }
      if (url.includes("/repositories")) return { repositories: ["miclle/demo"] }
      return { items: [] }
    }
    const { container } = await mountPage({ request }, { chooseRepository: false })
    expect(container.querySelector("#deployment-repository").hasAttribute("data-placeholder")).toBe(true)
    expect(requests.some((url) => url.includes("deployment-refs"))).toBe(false)
    expect(container.querySelector('button[type="submit"]').disabled).toBe(true)
    await selectOption(container, "#deployment-repository", "miclle/demo")
    expect(container.querySelector('select[name="deployment-ref"]').value).toBe("main")
    expect(container.querySelector('button[type="submit"]').disabled).toBe(false)
  })

  test("shows an actionable App permission error when version discovery is forbidden", async () => {
    const request = async (url) => {
      if (url.includes("deployment-refs")) throw Object.assign(new Error("provider detail"), { code: "deployment_repository_contents_required" })
      if (url.includes("deployment-templates")) return { templates: [{ template_id: "public-base" }] }
      if (url.includes("/repositories")) return { repositories: ["miclle/demo"] }
      return { items: [] }
    }
    const { container } = await mountPage({ request })
    expect(container.textContent).toContain("Approve its Contents: Read-only permission")
    expect(container.textContent).not.toContain("provider detail")
    expect(container.querySelector('button[type="submit"]').disabled).toBe(true)
  })

  test("submits the selected template and opens the created deployment's canonical detail", async () => {
    let submitted, opened
    const request = async (url, options) => {
      if (options?.method === "POST") { submitted = JSON.parse(options.body); return deployment("created", "queued") }
      if (url.includes("deployment-refs")) return { refs: ["main", "example/vite"], has_more: false }
      if (url.includes("deployment-templates")) return { templates: [{ template_id: "public-base", names: ["base"] }, { template_id: "custom", aliases: ["My template"] }] }
      if (url.includes("/repositories")) return { repositories: ["miclle/demo"] }
      return { items: [] }
    }
    const { container } = await mountPage({ request, onOpenDeployment: (id) => { opened = id } })
    const select = container.querySelector('select[name="deployment-template"]')
    expect(select.value).toBe("public-base")
    await act(async () => { select.value = "custom"; select.dispatchEvent(new Event("change", { bubbles: true })) })
    await act(async () => container.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })))
    expect(submitted.template_id).toBe("custom")
    expect(submitted.template_name).toBe("My template")
    expect(submitted.source).toBe("branch")
    expect(submitted.ref).toBe("main")
    expect(submitted.repository_full_name).toBe("miclle/demo")
    expect(opened).toBe("created")
  })

  test("ignores the previous scope's delayed template response and prevents stale submission", async () => {
    let resolveFirst
    const pending = new Promise((resolve) => { resolveFirst = resolve })
    const request = async (url) => {
      if (url.includes("deployment-refs")) return { refs: ["main"], has_more: false }
      if (url.includes("deployment-templates")) return url.includes("installation_id=12&") ? pending : { templates: [{ template_id: "scope-13", names: ["base"] }] }
      if (url.includes("/repositories")) return { repositories: url.includes("/13/") ? ["other/demo"] : ["miclle/demo"] }
      return { items: [] }
    }
    const { container } = await mountPage({ request, githubApp: { installations: [installation(12), installation(13, "other")] } })
    expect(container.querySelector('button[type="submit"]').disabled).toBe(true)
    const scope = container.querySelector('select[name="deployment-installation"]')
    await act(async () => { scope.value = "13"; scope.dispatchEvent(new Event("change", { bubbles: true })) })
    await settle()
    await selectOption(container, "#deployment-repository", "other/demo")
    await settle()
    await act(async () => resolveFirst({ templates: [{ template_id: "stale-private", names: ["old"] }] }))
    expect(container.querySelector('select[name="deployment-template"]').value).toBe("scope-13")
    expect(container.textContent).not.toContain("stale-private")
  })

  test("allows a repository member to select an organization and deploy its public template", async () => {
    let submitted
    const requests = []
    const request = async (url, options) => {
      requests.push(url)
      if (options?.method === "POST") { submitted = JSON.parse(options.body); return deployment("member-created", "queued") }
      if (url.includes("deployment-templates")) return { templates: [{ template_id: "public-base", names: ["base"] }] }
      if (url.includes("deployment-refs")) return { refs: ["main"] }
      if (url.includes("/repositories")) return { repositories: ["acme/demo"] }
      return { items: [] }
    }
    const { container } = await mountPage({ request, githubApp: { installations: [{ ...installation(12, "acme"), account_type: "Organization", manageable: false }] } })
    expect(container.querySelector('select[name="deployment-installation"]').value).toBe("12")
    expect(requests.some((url) => url.includes("deployment-templates?") && url.includes("repository_full_name=acme%2Fdemo"))).toBe(true)
    expect(container.querySelector('button[type="submit"]').disabled).toBe(false)
    await act(async () => container.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })))
    expect(submitted.repository_full_name).toBe("acme/demo")
    expect(submitted.template_id).toBe("public-base")
  })

  test("discards delayed templates from another repository in the same installation", async () => {
    let resolveFirst
    const pending = new Promise((resolve) => { resolveFirst = resolve })
    const request = async (url) => {
      if (url.includes("deployment-templates")) return url.includes("miclle%2Fdemo") ? pending : { templates: [{ template_id: "current-public" }] }
      if (url.includes("deployment-refs")) return { refs: ["main"] }
      if (url.includes("/repositories")) return { repositories: ["miclle/demo", "miclle/other"] }
      return { items: [] }
    }
    const { container } = await mountPage({ request })
    await selectOption(container, "#deployment-repository", "miclle/other")
    await settle()
    await act(async () => resolveFirst({ templates: [{ template_id: "stale-private" }] }))
    expect(container.querySelector('select[name="deployment-template"]').value).toBe("current-public")
    expect(container.textContent).not.toContain("stale-private")
  })

  test("switches to Tag, ignores a delayed branch response, and submits the selected tag", async () => {
    let resolveBranches, submitted
    const branches = new Promise((resolve) => { resolveBranches = resolve })
    const request = async (url, options) => {
      if (options?.method === "POST") { submitted = JSON.parse(options.body); return deployment("tag-deployment", "queued") }
      if (url.includes("deployment-refs")) return url.includes("source=branch") ? branches : { refs: ["v1.0"], has_more: false }
      if (url.includes("deployment-templates")) return { templates: [{ template_id: "public-base", names: ["base"] }] }
      if (url.includes("/repositories")) return { repositories: ["miclle/demo"] }
      return { items: [] }
    }
    const { container } = await mountPage({ request })
    expect(container.querySelector('button[type="submit"]').disabled).toBe(true)
    await selectOption(container, "#deployment-source", "tag")
    await settle()
    expect(container.querySelector('label[for="deployment-ref"]').textContent).toBe("Select Tag")
    expect(container.querySelector('select[name="deployment-ref"]').value).toBe("v1.0")
    await act(async () => resolveBranches({ refs: ["main", "stale-branch"], has_more: false }))
    expect(container.querySelector('select[name="deployment-ref"]').value).toBe("v1.0")
    expect(container.textContent).not.toContain("stale-branch")
    await act(async () => container.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })))
    expect(submitted.source).toBe("tag")
    expect(submitted.ref).toBe("v1.0")
  })

  test("creates a PR deployment without losing the selected template", async () => {
    let submitted
    const request = async (url, options) => {
      if (options?.method === "POST") { submitted = JSON.parse(options.body); return { ...deployment("pr-preview"), source: "pull_request", pull_request_number: 7 } }
      if (url.includes("deployment-refs")) return { refs: url.includes("source=pull_request") ? ["7", "8"] : ["main"], has_more: false }
      if (url.includes("deployment-templates")) return { templates: [{ template_id: "base-template", names: ["base"] }] }
      if (url.includes("/repositories")) return { repositories: ["miclle/demo"] }
      throw new Error(`unexpected request ${url}`)
    }
    const opened = []
    const { container } = await mountPage({ request, onOpenDeployment: (id) => opened.push(id) })
    await selectOption(container, "#deployment-source", "pull_request")
    await settle()
    expect(container.querySelector('label[for="deployment-ref"]').textContent).toBe("Select PR")
    expect(container.querySelector('select[name="deployment-ref"]').selectedOptions[0].textContent).toBe("#7")
    await act(async () => container.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })))
    expect(submitted.source).toBe("pull_request")
    expect(submitted.ref).toBe("7")
    expect(submitted.template_id).toBe("base-template")
    expect(opened).toEqual(["pr-preview"])
  })

  test("blocks creation for an empty Tag list and retries a failed version discovery", async () => {
    let tagRequests = 0, submissions = 0
    const request = async (url, options) => {
      if (options?.method === "POST") { submissions += 1; return deployment("unexpected") }
      if (url.includes("deployment-refs")) {
        if (!url.includes("source=tag")) return { refs: ["main"], has_more: false }
        tagRequests += 1
        if (tagRequests === 1) return { refs: [], has_more: false }
        if (tagRequests === 2) throw new Error("GitHub temporarily unavailable")
        return { refs: ["v2.0"], has_more: false }
      }
      if (url.includes("deployment-templates")) return { templates: [{ template_id: "public-base", names: ["base"] }] }
      if (url.includes("/repositories")) return { repositories: ["miclle/demo"] }
      return { items: [] }
    }
    const { container } = await mountPage({ request })
    await selectOption(container, "#deployment-source", "tag")
    await settle()
    expect(container.textContent).toContain("No branches or tags available")
    expect(container.querySelector('button[type="submit"]').disabled).toBe(true)
    await act(async () => container.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })))
    expect(submissions).toBe(0)
    await act(async () => button(container, "Refresh versions").click())
    expect(container.textContent).toContain("GitHub temporarily unavailable")
    expect(container.querySelector('button[type="submit"]').disabled).toBe(true)
    await act(async () => button(container, "Refresh versions").click())
    expect(container.querySelector('select[name="deployment-ref"]').value).toBe("v2.0")
    expect(container.querySelector('button[type="submit"]').disabled).toBe(false)
    expect(container.textContent).not.toContain("GitHub temporarily unavailable")
  })

  test("appends another version page without losing the selected branch", async () => {
    const request = async (url) => {
      if (url.includes("deployment-refs")) return url.includes("page=2") ? { refs: ["main", "example/vite"], has_more: false } : { refs: ["main", "feature/first"], has_more: true }
      if (url.includes("deployment-templates")) return { templates: [{ template_id: "public-base", names: ["base"] }] }
      if (url.includes("/repositories")) return { repositories: ["miclle/demo"] }
      return { items: [] }
    }
    const { container } = await mountPage({ request })
    await selectOption(container, "#deployment-ref", "feature/first")
    await act(async () => button(container, "Load more versions").click())
    const select = container.querySelector('select[name="deployment-ref"]')
    expect(select.value).toBe("feature/first")
    expect([...select.options].map((option) => option.value)).toEqual(["main", "feature/first", "example/vite"])
    expect(button(container, "Load more versions")).toBeUndefined()
  })

  test("loads a direct detail URL, merges cursor pages, and retains logs after a refresh error", async () => {
    let failRefresh = true
    const requested = []
    const request = async (url) => {
      requested.push(url)
      if (url.startsWith("/user/deployment-repositories")) return { items: [] }
      if (url === "/user/deployments/deep-link") return deployment("deep-link", "completed")
      if (url.endsWith("?before_id=205")) return { events: [event(203, "earlier install output"), event(204, "older output")], has_more: false }
      if (url.endsWith("?after_id=206")) {
        if (failRefresh) throw new Error("offline")
        return { events: [event(207, "application starting")], has_more: true }
      }
      if (url.endsWith("?after_id=207")) return { events: [event(208, "deployment ready")], has_more: false }
      if (url.endsWith("/events")) return { events: [event(205, "install command output"), event(206, "build completed")], has_more: true }
      throw new Error(`unexpected request ${url}`)
    }
    const { container } = await mountPage({ request, deploymentID: "deep-link" }, { page: "deployments" })
    expect(requested).toContain("/user/deployments/deep-link")
    expect(requested).toContain("/user/deployment-repositories?limit=100&offset=0")
    expect(container.textContent).toContain("base")
    expect(container.textContent).toContain("4 vCPU")
    expect(container.textContent).toContain("8192 MB")
    expect(container.textContent).toContain("40960 MB")
    expect(container.querySelector("form")).toBeNull()
    await act(async () => button(container, "Load earlier logs").click())
    await act(async () => [...container.querySelectorAll("button")].filter((item) => item.textContent.trim() === "Refresh").at(-1).click())
    expect(container.textContent).toContain("Unable to load deployment logs: offline")
    expect(container.textContent).toContain("earlier install output")
    failRefresh = false
    await act(async () => [...container.querySelectorAll("button")].filter((item) => item.textContent.trim() === "Refresh").at(-1).click())
    expect(container.textContent).toContain("deployment ready")
    expect(container.textContent).toContain("earlier install output")
    expect(container.querySelector('[data-testid="deployment-logs"]').querySelectorAll("pre").length).toBe(6)
  })
})

describe("Job deployment prefill", () => {
  test("preserves a historical PR and pinned SHA through delayed account catalogs", async () => {
    const sha = "b".repeat(40)
    const locationSearch = `?repository=team%2Fdemo&source=pull_request&ref=7&commit_sha=${sha}`
    let submitted
    const request = async (url, options) => {
      if (url.endsWith("/repositories")) return { repositories: ["team/demo"] }
      if (url.startsWith("/user/deployment-refs")) return { refs: ["8"], has_more: false }
      if (url.startsWith("/user/deployment-templates")) return { templates: [{ template_id: "base" }] }
      if (url === "/user/deployments" && options?.method === "POST") { submitted = JSON.parse(options.body); return deployment("pinned") }
      throw new Error(url)
    }
    const mounted = await mountPage({ request, locationSearch, githubApp: null, loadingGitHubApp: true }, { chooseRepository: false })
    await act(async () => mounted.root.render(createElement(UserDeploymentCreateSection, { ...mounted.props, githubApp: { installations: [installation(12), installation(22, "team")] }, loadingGitHubApp: false })))
    await settle()
    expect(mounted.container.querySelector('select[name="deployment-installation"]').value).toBe("22")
    expect(mounted.container.querySelector('select[name="deployment-repository"]').value).toBe("team/demo")
    expect(mounted.container.querySelector('select[name="deployment-source"]').value).toBe("pull_request")
    expect(mounted.container.querySelector('select[name="deployment-ref"]').value).toBe("7")
    await act(async () => mounted.container.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })))
    expect(submitted).toMatchObject({ installation_id: 22, repository_full_name: "team/demo", source: "pull_request", ref: "7", commit_sha: sha })
    await act(async () => button(mounted.container, "Use latest source commit").click())
    await act(async () => mounted.container.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })))
    expect(submitted.ref).toBe("7")
    expect(submitted.commit_sha).toBeUndefined()
  })

  test("source changes discard the old pin and reject delayed ref results", async () => {
    let finishRefs
    const delayed = new Promise((resolve) => { finishRefs = resolve })
    let submitted
    const request = async (url, options) => {
      if (url.endsWith("/repositories")) return { repositories: ["miclle/demo"] }
      if (url.startsWith("/user/deployment-refs")) return new URL(url, "https://example.com").searchParams.get("source") === "pull_request" ? delayed : { refs: ["main"], has_more: false }
      if (url.startsWith("/user/deployment-templates")) return { templates: [{ template_id: "base" }] }
      if (url === "/user/deployments") { submitted = JSON.parse(options.body); return deployment("new-branch") }
      throw new Error(url)
    }
    const { container } = await mountPage({ request, locationSearch: `?repository=miclle%2Fdemo&source=pull_request&ref=7&commit_sha=${"a".repeat(40)}` }, { chooseRepository: false })
    await selectOption(container, "#deployment-source", "branch")
    await settle()
    await act(async () => finishRefs({ refs: ["7", "8"] }))
    expect(container.querySelector('select[name="deployment-ref"]').value).toBe("main")
    expect(container.textContent).not.toContain("Pinned commit")
    await act(async () => container.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })))
    expect(submitted).toMatchObject({ source: "branch", ref: "main" })
    expect(submitted.commit_sha).toBeUndefined()
  })

  test("an inaccessible prefilled repository cannot be submitted", async () => {
    let posted = false
    const request = async (url, options) => {
      if (options?.method === "POST") posted = true
      if (url.endsWith("/repositories")) return { repositories: ["miclle/demo"] }
      if (url.startsWith("/user/deployment-templates")) return { templates: [{ template_id: "base" }] }
      return {}
    }
    const { container } = await mountPage({ request, locationSearch: `?repository=private%2Fother&source=branch&ref=main&commit_sha=${"a".repeat(40)}` }, { chooseRepository: false })
    expect(container.textContent).toContain("This repository is not available")
    expect(container.querySelector('button[type="submit"]').disabled).toBe(true)
    await act(async () => container.querySelector("form").dispatchEvent(new Event("submit", { bubbles: true, cancelable: true })))
    expect(posted).toBe(false)
  })
})
