import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { Window } from "happy-dom"
import { act, createElement } from "react"
import { createRoot } from "react-dom/client"
import { renderToStaticMarkup } from "react-dom/server"

import * as catalogUtils from "./sandbox-catalog-utils"
import { SandboxRegionsContext } from "./sandbox-catalog-utils"

const { formatOptionalTime, findSandboxRegionByAPIURL } = catalogUtils

const window = new Window({ url: "http://localhost/" })
const domGlobals = {
  window,
  document: window.document,
  navigator: window.navigator,
  HTMLElement: window.HTMLElement,
  HTMLFormElement: window.HTMLFormElement,
  SVGElement: window.SVGElement,
  Node: window.Node,
  DocumentFragment: window.DocumentFragment,
  Event: window.Event,
  MouseEvent: window.MouseEvent,
  getComputedStyle: window.getComputedStyle.bind(window),
  requestAnimationFrame: window.requestAnimationFrame.bind(window),
  cancelAnimationFrame: window.cancelAnimationFrame.bind(window),
  IS_REACT_ACT_ENVIRONMENT: true,
}
const originalGlobalDescriptors = new Map(Object.keys(domGlobals).map((key) => [
  key,
  Object.getOwnPropertyDescriptor(globalThis, key),
]))
for (const [key, value] of Object.entries(domGlobals)) {
  Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
}

const { SandboxTemplateCatalog, SandboxTemplatesSection } = await import("./sandbox-catalog-sections")

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

async function mountSandboxTemplates(request) {
  const container = document.createElement("div")
  document.body.append(container)
  const root = createRoot(container)
  mountedRoots.push({ root, container })
  const regions = [
    { id: "us-south-1", label: "United States \u00b7 Dallas 1", apiURL: "https://us-south-1-sandbox.qiniuapi.com" },
  ]
  await act(async () => {
    root.render(
      createElement(SandboxRegionsContext.Provider, { value: regions },
        createElement(SandboxTemplatesSection, { request, installationID: 42 }),
      ),
    )
  })
  return {
    container,
    async rerender(nextRequest) {
      await act(async () => {
        root.render(
          createElement(SandboxRegionsContext.Provider, { value: regions },
            createElement(SandboxTemplatesSection, { request: nextRequest, installationID: 42 }),
          ),
        )
      })
    },
  }
}

function cardByTitle(container, title) {
  const titleElement = [...container.querySelectorAll('[data-slot="card-title"]')]
    .find((element) => element.textContent === title)
  if (!titleElement) throw new Error(`card not found: ${title}`)
  return titleElement.closest('[data-slot="card"]')
}

async function click(element) {
  await act(async () => {
    element.dispatchEvent(new MouseEvent("click", { bubbles: true }))
  })
}

async function settle() {
  await act(async () => {
    await Promise.resolve()
    await Promise.resolve()
  })
}

function deferred() {
  let resolve
  let reject
  const promise = new Promise((resolvePromise, rejectPromise) => {
    resolve = resolvePromise
    reject = rejectPromise
  })
  return { promise, resolve, reject }
}

const publicTemplate = (name) => ({
  default_template_name: name,
  runner_spec_names: [`${name}-spec`],
  workflow_labels: [["qiniu", `${name}-label`]],
})

const scopedTemplate = (id) => ({
  template_id: id,
  aliases: [`${id}-alias`],
  build_status: "ready",
  cpu_count: 4,
  memory_mb: 8192,
  disk_size_mb: 40960,
  public: false,
  spawn_count: 3,
  updated_at: "2026-08-13T00:00:00Z",
})

describe("sandbox regions", () => {
  test("matches a known region by exact API URL", () => {
    const regions = [
      {
        id: "us-south-1",
        label: "United States · Dallas 1",
        apiURL: "https://us-south-1-sandbox.qiniuapi.com",
      },
      {
        id: "cn-yangzhou-1",
        label: "China · Yangzhou 1",
        apiURL: "https://cn-yangzhou-1-sandbox.qiniuapi.com",
      },
    ]
    expect(findSandboxRegionByAPIURL(regions, "https://us-south-1-sandbox.qiniuapi.com")?.id).toBe("us-south-1")
    expect(findSandboxRegionByAPIURL(regions, "https://cn-yangzhou-1-sandbox.qiniuapi.com/")?.id).toBe("cn-yangzhou-1")
    expect(findSandboxRegionByAPIURL(regions, "https://unknown.example.com")).toBeUndefined()
  })
})

describe("formatOptionalTime", () => {
  test("formats timestamps with the selected application locale", () => {
    const timestamp = "2026-08-10T08:09:10Z"
    const date = new Date(timestamp)

    expect(formatOptionalTime(timestamp, "en")).toBe(date.toLocaleString("en"))
    expect(formatOptionalTime(timestamp, "zh")).toBe(date.toLocaleString("zh"))
  })

  test("renders invalid timestamps as unavailable", () => {
    expect(formatOptionalTime("not-a-date")).toBe("—")
  })

  test("renders empty and zero timestamps as unavailable", () => {
    expect(formatOptionalTime("")).toBe("—")
    expect(formatOptionalTime("0001-01-01T00:00:00Z")).toBe("—")
  })
})

describe("sandbox catalog loaders", () => {
  test("loads instances without fetching templates", async () => {
    const paths = []
    const request = async (path) => {
      paths.push(path)
      return []
    }

    expect(typeof catalogUtils.loadSandboxInstances).toBe("function")
    await catalogUtils.loadSandboxInstances(request, "us-south-1", 42, "template-1")

    expect(paths).toEqual([
      "/user/sandbox/instances?region=us-south-1&installation_id=42&template_id=template-1",
    ])
  })
})

describe("sandbox template catalog", () => {
  test("keeps public managed templates visible when the scoped provider catalog fails", () => {
    const html = renderToStaticMarkup(createElement(SandboxTemplateCatalog, {
      publicTemplates: [{
        default_template_name: "github-runner-ubuntu-24-04",
        runner_spec_names: ["qiniu-ubuntu-24.04", "qiniu-ubuntu-latest"],
        workflow_labels: [["qiniu", "ubuntu-24.04"], ["qiniu", "ubuntu-latest"]],
      }],
      publicLoading: false,
      publicError: "",
      scopedTemplates: [],
      scopedLoading: false,
      scopedError: "Sandbox credentials are not configured",
    }))

    expect(html).toContain("Managed runner templates")
    expect(html).toContain("github-runner-ubuntu-24-04")
    expect(html).toContain("qiniu-ubuntu-latest")
    expect(html).toContain("qiniu, ubuntu-latest")
    expect(html).toContain("Provider templates")
    expect(html).toContain("Sandbox credentials are not configured")
  })

  test("loads both catalogs initially and exposes an independent public refresh", async () => {
    const paths = []
    const { container } = await mountSandboxTemplates(async (path) => {
      paths.push(path)
      return []
    })

    expect(paths).toEqual([
      "/api/public/runner-templates",
      "/user/sandbox/templates?region=us-south-1&installation_id=42",
    ])

    const publicCard = cardByTitle(container, "Managed runner templates")
    const publicRefresh = publicCard.querySelector('button[aria-label="Refresh"]')
    expect(publicRefresh).not.toBeNull()

    await click(publicRefresh)
    expect(paths.slice(2)).toEqual(["/api/public/runner-templates"])
  })

  test("provider refresh does not reload the public catalog", async () => {
    const paths = []
    const { container } = await mountSandboxTemplates(async (path) => {
      paths.push(path)
      return []
    })
    const providerCard = cardByTitle(container, "Provider templates")
    const providerRefresh = providerCard.querySelector('button[aria-label="Refresh"]')

    await click(providerRefresh)

    expect(paths.slice(2)).toEqual([
      "/user/sandbox/templates?region=us-south-1&installation_id=42",
    ])
  })

  test("keeps provider refresh available while only the public catalog is loading", async () => {
    const pendingPublic = deferred()
    const { container } = await mountSandboxTemplates(async (path) => path === "/api/public/runner-templates"
      ? pendingPublic.promise
      : [])
    await settle()

    const publicCard = cardByTitle(container, "Managed runner templates")
    const providerCard = cardByTitle(container, "Provider templates")
    expect(publicCard.querySelector('button[aria-label="Refresh"]').disabled).toBe(true)
    expect(providerCard.querySelector('button[aria-label="Refresh"]').disabled).toBe(false)

    pendingPublic.resolve([])
    await settle()
  })

  test("recovers a public failure without reloading visible provider templates", async () => {
    const paths = []
    let publicAttempts = 0
    const { container } = await mountSandboxTemplates(async (path) => {
      paths.push(path)
      if (path === "/api/public/runner-templates") {
        publicAttempts += 1
        if (publicAttempts === 1) throw new Error("public catalog unavailable")
        return [publicTemplate("public-recovered")]
      }
      return [scopedTemplate("provider-stable")]
    })
    await settle()

    expect(container.textContent).toContain("public catalog unavailable")
    expect(container.textContent).toContain("provider-stable-alias")

    const publicCard = cardByTitle(container, "Managed runner templates")
    await click(publicCard.querySelector('button[aria-label="Refresh"]'))
    await settle()

    expect(container.textContent).toContain("public-recovered")
    expect(container.textContent).not.toContain("public catalog unavailable")
    expect(container.textContent).toContain("provider-stable-alias")
    expect(paths).toEqual([
      "/api/public/runner-templates",
      "/user/sandbox/templates?region=us-south-1&installation_id=42",
      "/api/public/runner-templates",
    ])
  })

  test("recovers a provider failure without reloading visible public templates", async () => {
    const paths = []
    let scopedAttempts = 0
    const { container } = await mountSandboxTemplates(async (path) => {
      paths.push(path)
      if (path === "/api/public/runner-templates") return [publicTemplate("public-stable")]
      scopedAttempts += 1
      if (scopedAttempts === 1) throw new Error("provider catalog unavailable")
      return [scopedTemplate("provider-recovered")]
    })
    await settle()

    expect(container.textContent).toContain("public-stable")
    expect(container.textContent).toContain("provider catalog unavailable")

    const providerCard = cardByTitle(container, "Provider templates")
    await click(providerCard.querySelector('button[aria-label="Refresh"]'))
    await settle()

    expect(container.textContent).toContain("public-stable")
    expect(container.textContent).toContain("provider-recovered-alias")
    expect(container.textContent).not.toContain("provider catalog unavailable")
    expect(paths).toEqual([
      "/api/public/runner-templates",
      "/user/sandbox/templates?region=us-south-1&installation_id=42",
      "/user/sandbox/templates?region=us-south-1&installation_id=42",
    ])
  })

  test("does not let stale catalog responses overwrite a newer request scope", async () => {
    const stalePublic = deferred()
    const staleScoped = deferred()
    const initialRequest = (path) => path === "/api/public/runner-templates" ? stalePublic.promise : staleScoped.promise
    const { container, rerender } = await mountSandboxTemplates(initialRequest)

    await rerender(async (path) => path === "/api/public/runner-templates"
      ? [publicTemplate("public-current")]
      : [scopedTemplate("provider-current")])
    await settle()

    expect(container.textContent).toContain("public-current")
    expect(container.textContent).toContain("provider-current-alias")

    stalePublic.resolve([publicTemplate("public-stale")])
    staleScoped.resolve([scopedTemplate("provider-stale")])
    await settle()

    expect(container.textContent).toContain("public-current")
    expect(container.textContent).toContain("provider-current-alias")
    expect(container.textContent).not.toContain("public-stale")
    expect(container.textContent).not.toContain("provider-stale-alias")
  })
})

describe("sandbox instances view state", () => {
  test("keeps the instances table available when only the template filter fails", () => {
    expect(
      catalogUtils.sandboxInstancesViewState({
        templatesLoading: false,
        instancesLoading: false,
        templatesError: "template catalog unavailable",
        instancesError: "",
      }),
    ).toEqual({
      loading: false,
      error: "",
      filterDisabled: true,
    })
  })
})
