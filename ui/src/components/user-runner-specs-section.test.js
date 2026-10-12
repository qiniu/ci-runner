import { afterAll, afterEach, describe, expect, test } from "bun:test"
import { Window } from "happy-dom"
import { act, createElement, useState } from "react"
import { createRoot } from "react-dom/client"
import { renderToStaticMarkup } from "react-dom/server"
import { toast } from "sonner"

import { GitHubRunnerRegistration } from "./github-runner-registration"
import { load as loadYAML } from "js-yaml"
import { RunnerSpecForm, RunnerSpecItem, UserRunnerSpecsSection } from "./user-runner-specs-section"
import { newRunnerSpecFormState, runnerSpecCatalogRegion, runnerSpecDialogDescriptionKey, runnerSpecOverridesGlobal, runnerSpecWorkflowYAML } from "./user-runner-specs-utils"

const window = new Window({ url: "http://localhost/" })
const domGlobals = { window, document: window.document, navigator: window.navigator, HTMLElement: window.HTMLElement, SVGElement: window.SVGElement, Node: window.Node, DocumentFragment: window.DocumentFragment, Event: window.Event, MouseEvent: window.MouseEvent, getComputedStyle: window.getComputedStyle.bind(window), requestAnimationFrame: window.requestAnimationFrame.bind(window), cancelAnimationFrame: window.cancelAnimationFrame.bind(window), IS_REACT_ACT_ENVIRONMENT: true }
const originalGlobalDescriptors = new Map(Object.keys(domGlobals).map((key) => [key, Object.getOwnPropertyDescriptor(globalThis, key)]))
for (const [key, value] of Object.entries(domGlobals)) Object.defineProperty(globalThis, key, { configurable: true, writable: true, value })
const mountedRoots = []

afterEach(async () => {
  for (const item of toast.getToasts()) toast.dismiss(item.id)
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

function deferred() {
  let resolve
  const promise = new Promise((resolvePromise) => { resolve = resolvePromise })
  return { promise, resolve }
}

async function settle() {
  await act(async () => { await Promise.resolve(); await Promise.resolve() })
}

const managed = {
  name: "ubuntu-24.04",
  source: "platform_public",
  published: true,
  workflow_labels: ["qiniu", "ubuntu-24.04"],
  template_id: "managed-template",
  default_template_name: "ubuntu-24.04-x64",
  enabled: true,
  max_concurrency: 8,
  overrides_global: false,
  updated_at: "2026-08-28T00:00:00Z",
}

const custom = {
  ...managed,
  name: "gpu",
  source: "scoped_custom",
  workflow_labels: ["qiniu", "gpu"],
  template_id: "gpu-template",
  default_template_name: "",
  max_concurrency: 10,
  overrides_global: true,
}

const disabledManaged = {
  ...managed,
  name: "disabled-ubuntu",
  enabled: false,
}

describe("UserRunnerSpecsSection", () => {

  test("quotes groups and preserves label routing in YAML", () => {
    const group = 'build: #gpu "team"\nnext'
    const labels = ["qiniu", "gpu: fast"]
    expect(loadYAML(runnerSpecWorkflowYAML(labels, group))).toEqual({ "runs-on": { group, labels } })
    expect(runnerSpecWorkflowYAML(labels, "  ")).toBe(runnerSpecWorkflowYAML(labels))
  })

  test("loads groups only when enabled, links management, and keeps selection in the preview", async () => {
    const calls = []
    const request = async (url) => {
      calls.push(url)
      return { organization: "octo-org", groups: [{ id: 1, name: "Build", restricted_to_workflows: true }] }
    }
    function Form() {
      const [form, setForm] = useState({ ...newRunnerSpecFormState(), labels: "qiniu, gpu" })
      return createElement(RunnerSpecForm, { mode: "create", form, templates: [], saving: false, allowRunnerGroup: true, installationID: 987, request, onChange: (next) => setForm((old) => ({ ...old, ...next })), onSubmit() {}, onClose() {} })
    }
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })
    await act(async () => root.render(createElement(Form)))
    expect(calls).toHaveLength(0)
    expect(container.textContent).toContain("Repository that triggered the job")
    const toggle = [...container.querySelectorAll('input[type="checkbox"]')].find((input) => input.parentElement.textContent.includes("Use an organization Runner group"))
    await act(async () => toggle.click())
    await settle()
    expect(calls).toEqual(["/user/github-runner-groups?installation_id=987"])
    const select = container.querySelector("#user-runner-group")
    expect(select.required).toBe(true)
    expect(select.checkValidity()).toBe(false)
    await act(async () => { select.value = "Build"; select.dispatchEvent(new Event("change", { bubbles: true })) })
    expect(container.textContent).toContain('group: "Build"')
    expect(container.textContent).toContain('labels: ["qiniu", "gpu"]')
    expect(container.textContent).toContain("Organization octo-org · group Build")
    expect(container.textContent).toContain("This group restricts workflows")
    expect(container.querySelector('a[href="https://github.com/organizations/octo-org/settings/actions/runner-groups"]')).not.toBeNull()
    await act(async () => toggle.click())
    expect(container.querySelector("#user-runner-group")).toBeNull()
    expect(container.textContent).not.toContain('group: "Build"')
    expect(container.textContent).toContain('runs-on: ["qiniu", "gpu"]')
  })

  test("ignores a late group list after changing organizations", async () => {
    const old = deferred()
    const request = (url) => url.endsWith("=1") ? old.promise : Promise.resolve({ organization: "second", groups: [{ id: 2, name: "Second" }] })
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })
    const props = { group: "Saved", id: "group", onChange() {}, request }
    await act(async () => root.render(createElement(GitHubRunnerRegistration, { ...props, installationID: 1 })))
    await act(async () => root.render(createElement(GitHubRunnerRegistration, { ...props, installationID: 2 })))
    await settle()
    await act(async () => old.resolve({ organization: "first", groups: [{ id: 1, name: "Old private group" }] }))
    await settle()
    expect(container.textContent).toContain("Second")
    expect(container.textContent).not.toContain("Old private group")
    expect(container.textContent).toContain("Saved (not in the current list)")
  })

  test("preserves an existing group on permission failure and supports retry", async () => {
    let calls = 0
    const request = async () => {
      if (++calls === 1) throw Object.assign(new Error("raw provider details"), { code: "runner_group_permission_denied" })
      return { organization: "org", groups: [{ id: 1, name: "Saved" }] }
    }
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })
    await act(async () => root.render(createElement(GitHubRunnerRegistration, { group: "Saved", id: "group", onChange() {}, request, installationID: 1 })))
    await settle()
    expect(container.querySelector('[role="alert"]').textContent).toContain("Self-hosted runners permission")
    expect(container.textContent).not.toContain("raw provider details")
    expect(container.querySelector("select").value).toBe("Saved")
    await act(async () => [...container.querySelectorAll("button")].find((button) => button.textContent === "Refresh Runner groups").click())
    await settle()
    expect(container.querySelector('[role="alert"]')).toBeNull()
    expect(container.querySelector("select").value).toBe("Saved")
  })

  test("copy YAML includes the configured organization group", async () => {
    let copied = ""
    const previous = Object.getOwnPropertyDescriptor(navigator, "clipboard")
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: async (text) => { copied = text } } })
    try {
      const request = async () => ({ items: [{ ...custom, runner_group: "Build" }], sandbox_source: "github_installation" })
      const container = document.createElement("div")
      document.body.append(container)
      const root = createRoot(container)
      mountedRoots.push({ root, container })
      await act(async () => root.render(createElement(UserRunnerSpecsSection, { request, installationID: 987 })))
      await settle()
      await act(async () => container.querySelector('button[aria-label="Copy runs-on YAML"]').click())
      expect(loadYAML(copied)).toEqual({ "runs-on": { group: "Build", labels: custom.workflow_labels } })
    } finally {
      if (previous) Object.defineProperty(navigator, "clipboard", previous)
      else Reflect.deleteProperty(navigator, "clipboard")
    }
  })

  test("uses the configured Sandbox region for the scoped catalog", () => {
    expect(runnerSpecCatalogRegion("cn-yangzhou-1")).toBe("cn-yangzhou-1")
    expect(runnerSpecCatalogRegion("unknown-region")).toBe("us-south-1")
  })

  test("quotes workflow labels in copied YAML", () => {
    expect(runnerSpecWorkflowYAML(["foo: bar", "a,b", "#gpu"])).toBe('runs-on: ["foo: bar", "a,b", "#gpu"]')
  })

  test("keeps custom status badges while leaving platform runner specs unbadged", () => {
    const html = renderToStaticMarkup(createElement("div", null,
      createElement(RunnerSpecItem, { item: managed, copying: false, onCopy() {}, onEdit() {}, onDelete() {} }),
      createElement(RunnerSpecItem, { item: custom, copying: false, onCopy() {}, onEdit() {}, onDelete() {} }),
    ))

    expect(html).toContain("ubuntu-24.04-x64")
    expect(html).not.toContain("Platform managed")
    expect(html).toContain("Your custom spec")
    expect(html).not.toContain(">managed<")
    expect(html).not.toContain(">scoped_custom<")
    expect(html).toContain(`aria-label="Edit ${custom.name}"`)
    expect(html).toContain(`aria-label="Delete ${custom.name}"`)
    expect(html).not.toContain(`aria-label="Edit platform control ${managed.name}"`)
    expect(html).not.toContain(`aria-label="Reset platform control ${managed.name}"`)
  })

  test("renders only scoped custom specs on account settings", async () => {
    const request = async (url) => {
      if (url === "/user/runner-specs") {
        return {
          items: [managed, custom],
          sandbox_source: "account",
          sandbox_region: "us-south-1",
        }
      }
      throw new Error(`unexpected request: ${url}`)
    }
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })

    await act(async () => root.render(createElement(UserRunnerSpecsSection, {
      request,
      scopeName: "miclle",
      sandboxTemplatesHref: "/account/sandbox-templates",
    })))
    await settle()

    const customSection = container.querySelector('[data-runner-spec-section="custom"]')
    expect(customSection).not.toBeNull()
    expect(container.querySelector('[data-runner-spec-section="platform"]')).toBeNull()
    if (!customSection) return
    const customHeadings = Array.from(container.querySelectorAll('h2, [data-slot="card-title"]')).filter((element) => element.textContent === "Custom Runner Specs")
    expect(customHeadings).toHaveLength(1)
    expect(customSection.textContent).toContain("Custom Runner Specs")
    expect(customSection.textContent).toContain(custom.name)
    expect(customSection.textContent).toContain("Only repositories owned by miclle can use custom runner specs created here.")
    expect([...customSection.querySelectorAll("button")].some((button) => button.textContent === "Refresh")).toBe(false)
    expect(container.textContent).not.toContain(managed.name)
    expect(container.querySelector('a[href="/runner-specs"]')).toBeNull()
  })

  test("renders the platform catalog without scope controls", async () => {
    const request = async (url) => {
      if (url === "/user/runner-specs") {
        return {
          items: [managed, { ...managed, name: "public-fixed-id", source: "platform_custom", default_template_name: "", template_id: "" }, disabledManaged, custom, { ...managed, name: "private-platform", source: "platform_custom", published: false }],
          sandbox_source: "github_installation",
          sandbox_region: "us-south-1",
        }
      }
      throw new Error(`unexpected request: ${url}`)
    }
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })

    await act(async () => root.render(createElement(UserRunnerSpecsSection, {
      request,
      view: "platform",
    })))
    await settle()

    expect(container.querySelector('[data-runner-spec-section="custom"]')).toBeNull()
    const platformSection = container.querySelector('[data-runner-spec-section="platform"]')
    expect(platformSection?.textContent).toContain(managed.name)
    expect(platformSection?.textContent).not.toContain(disabledManaged.name)
    expect(container.textContent).not.toContain(custom.name)
    expect(container.textContent).not.toContain("private-platform")
    expect(container.textContent).toContain("public-fixed-id")
    expect(platformSection?.textContent).not.toContain("Enabled")
    expect(platformSection?.textContent).not.toContain("Platform managed")
    expect(platformSection?.querySelectorAll('[data-slot="badge"]')).toHaveLength(0)
    expect(container.textContent).not.toContain("Availability and concurrency changes here apply only")
    expect(container.textContent).not.toContain("Effective max concurrency")
    expect(container.textContent).not.toContain("Scope max concurrency")
    expect([...container.querySelectorAll("button")].some((button) => button.textContent === "Refresh")).toBe(false)
    expect(container.querySelector(`[aria-label="Edit platform control ${managed.name}"]`)).toBeNull()
  })

  test("keeps the custom creation path visible when only platform specs exist", async () => {
    const request = async (url) => {
      if (url === "/user/runner-specs?installation_id=42") {
        return {
          items: [managed],
          sandbox_source: "github_installation",
          sandbox_region: "us-south-1",
        }
      }
      throw new Error(`unexpected request: ${url}`)
    }
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })

    await act(async () => root.render(createElement(UserRunnerSpecsSection, {
      request,
      installationID: 42,
      scopeName: "qiniu",
      sandboxTemplatesHref: "/organizations/qiniu/sandbox-templates",
    })))
    await settle()

    expect(container.textContent).toContain("No custom runner specs yet")
    expect(container.textContent).toContain("Only repositories owned by qiniu can use custom runner specs created here.")
    expect(container.textContent).toContain("Create custom runner spec")
    expect(container.textContent).not.toContain(managed.name)
  })

  test("starts a new custom runner spec with a bounded concurrency of 10", () => {
    expect(newRunnerSpecFormState().maxConcurrency).toBe("10")
  })

  test("describes custom dialogs for assistive technology", () => {
    expect(runnerSpecDialogDescriptionKey("create")).toBe("user.customRunnerSpecDialogDescription")
    expect(runnerSpecDialogDescriptionKey("edit")).toBe("user.customRunnerSpecDialogDescription")
  })

  test("links missing scoped credentials to Sandbox service settings", async () => {
    const request = async (url) => {
      if (url === "/user/runner-specs") {
        return {
          items: [managed],
          sandbox_source: "none",
          sandbox_region: "us-south-1",
        }
      }
      throw new Error(`unexpected request: ${url}`)
    }
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })

    await act(async () => root.render(createElement(UserRunnerSpecsSection, {
      request,
      scopeName: "miclle",
      sandboxServiceHref: "/account/preferences",
      sandboxTemplatesHref: "/account/sandbox-templates",
    })))
    await settle()

    const configureLink = [...container.querySelectorAll("a")].find((link) => link.textContent === "Configure Sandbox service")
    expect(configureLink?.getAttribute("href")).toBe("/account/preferences")
  })

  test("explains workflow labels and identifies scoped templates in the create form", () => {
    const html = renderToStaticMarkup(createElement(RunnerSpecForm, {
      mode: "create",
      form: { name: "", labels: "qiniu, gpu", templateID: "gpu-template", runnerGroup: "org-runners", maxConcurrency: "0", enabled: true },
      templates: [{ template_id: "gpu-template", aliases: ["GPU builder"], build_status: "ready" }],
      allowRunnerGroup: true,
      sandboxTemplatesHref: "/organizations/qiniu/sandbox-templates",
      saving: false,
      onChange() {},
      onSubmit() {},
      onClose() {},
    }))

    expect(html).toContain("runs-on labels")
    expect(html).toContain("GPU builder · gpu-template")
    expect(html).toContain('group: &quot;org-runners&quot;')
    expect(html).toContain('labels: [&quot;qiniu&quot;, &quot;gpu&quot;]')
    expect(html).toContain('href="/organizations/qiniu/sandbox-templates"')
    expect(html).toContain("org-runners")
  })

  test("makes creation intent and capacity semantics explicit in the form", () => {
    const html = renderToStaticMarkup(createElement(RunnerSpecForm, {
      mode: "create",
      form: { name: "", labels: "", templateID: "", runnerGroup: "", maxConcurrency: "0", enabled: true },
      templates: [],
      saving: false,
      onChange() {},
      onSubmit() {},
      onClose() {},
    }))

    expect(html).toContain('placeholder="gpu-build"')
    expect(html).toContain('placeholder="self-hosted, qiniu, gpu"')
    expect(html).toContain("Use 0 for unlimited concurrency.")
    expect(html).toContain("Allow new jobs to match this runner spec.")
    expect(html).toMatch(/<button[^>]*type="submit"[^>]*>Create custom runner spec<\/button>/)
  })

  test("keeps the form fields visible and disables submit while saving", () => {
    const html = renderToStaticMarkup(createElement(RunnerSpecForm, {
      mode: "create",
      form: { name: "gpu", labels: "qiniu,gpu", templateID: "gpu-template", runnerGroup: "", maxConcurrency: "2", enabled: true },
      templates: [],
      saving: true,
      onChange() {},
      onSubmit() {},
      onClose() {},
    }))

    expect(html).toContain('value="gpu"')
    expect(html).toContain('value="qiniu,gpu"')
    expect(html).toContain('value="gpu-template"')
    expect(html).toMatch(/<button[^>]*type="submit"[^>]*disabled/)
  })

  test("detects only exact platform label overrides", () => {
    expect(runnerSpecOverridesGlobal(["linux", "qiniu"], [managed])).toBe(false)
    expect(runnerSpecOverridesGlobal(["ubuntu-24.04", "qiniu"], [managed])).toBe(true)
    expect(runnerSpecOverridesGlobal(["Ubuntu-24.04", "QINIU"], [managed])).toBe(true)
  })

  test("hides the previous scope while the next scope is loading", async () => {
    const nextScope = deferred()
    const previousScopeItem = { ...custom, name: "private-gpu", template_id: "private-template", runner_group: "private-group" }
    const request = (url) => {
      if (url === "/user/runner-specs?installation_id=1") return Promise.resolve({ items: [previousScopeItem], sandbox_source: "github_installation" })
      if (url === "/user/runner-specs?installation_id=2") return nextScope.promise
      throw new Error(`unexpected request: ${url}`)
    }
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })

    await act(async () => root.render(createElement(UserRunnerSpecsSection, { request, installationID: 1, scopeName: "scope-one" })))
    await settle()
    expect(container.textContent).toContain("private-gpu")
    expect(container.textContent).toContain("private-template")

    await act(async () => root.render(createElement(UserRunnerSpecsSection, { request, installationID: 2, scopeName: "scope-two" })))
    await settle()

    expect(container.textContent).not.toContain("private-gpu")
    expect(container.textContent).not.toContain("private-template")
    expect(container.textContent).not.toContain("private-group")
    expect([...container.querySelectorAll("button")].find((button) => button.textContent === "Create custom runner spec")?.disabled).toBe(true)

    await act(async () => nextScope.resolve({ items: [], sandbox_source: "github_installation" }))
    await settle()
  })

  test("shows a persistent load error instead of misdiagnosing missing credentials", async () => {
    const request = async () => { throw new Error("catalog unavailable") }
    const container = document.createElement("div")
    document.body.append(container)
    const root = createRoot(container)
    mountedRoots.push({ root, container })

    await act(async () => root.render(createElement(UserRunnerSpecsSection, {
      request,
      scopeName: "miclle",
      sandboxServiceHref: "/account/preferences",
    })))
    await settle()

    const alert = container.querySelector('[role="alert"]')
    expect(alert?.textContent).toContain("Could not load runner specs")
    expect(alert?.textContent).toContain("catalog unavailable")
    expect(container.textContent).not.toContain("Configure Sandbox service")
    expect([...container.querySelectorAll("button")].find((button) => button.textContent === "Create custom runner spec")?.getAttribute("title")).toBeNull()
  })
})
