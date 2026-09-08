import { describe, expect, test } from "bun:test"
import { createElement } from "react"
import { renderToStaticMarkup } from "react-dom/server"

import * as RunnerSpecsModule from "./runner-specs-section"
import * as RunnerCatalogModule from "../hooks/use-runner-catalog"

const managedSpec = {
  name: "ubuntu-24.04",
  labels: ["self-hosted", "qiniu", "ubuntu-24.04"],
  required_labels: ["qiniu", "ubuntu-24.04"],
  template_id: "region-resolved-template-id",
  default_template_name: "ubuntu-24.04-x64",
  managed_by: "qiniu/ci-runner",
  catalog_revision: 3,
  runner_group: "",
  max_concurrency: 8,
  min_idle: 1,
  priority: 10,
  enabled: true,
  created_at: "2026-07-27T00:00:00Z",
  updated_at: "2026-07-27T00:00:00Z",
}

const managedForm = {
  name: managedSpec.name,
  labels: managedSpec.labels.join(","),
  required_labels: managedSpec.required_labels.join(","),
  template_id: managedSpec.template_id,
  runner_group: "",
  max_concurrency: String(managedSpec.max_concurrency),
  min_idle: String(managedSpec.min_idle),
  priority: String(managedSpec.priority),
  enabled: managedSpec.enabled,
}

function sectionProps(overrides = {}) {
  return {
    loading: false,
    runnerSpecs: [managedSpec],
    runnerSpecOpen: false,
    editingRunnerSpec: null,
    runnerSpecForm: managedForm,
    onRefresh: () => {},
    onResetRunnerSpecForm: () => {},
    onRunnerSpecOpenChange: () => {},
    onRunnerSpecFormChange: () => {},
    onSubmitRunnerSpec: () => {},
    onEditRunnerSpec: () => {},
    onDeleteRunnerSpec: () => {},
    ...overrides,
  }
}

function inputTag(html, id) {
  return html.match(new RegExp(`<input[^>]*id="${id}"[^>]*>`))?.[0] || ""
}

function isDisabledInput(html, id) {
  return /\sdisabled(?:=""|(?=[\s/>]))/.test(inputTag(html, id))
}

function collectText(node) {
  if (typeof node === "string" || typeof node === "number") return String(node)
  if (Array.isArray(node)) return node.map(collectText).join("")
  if (!node || typeof node !== "object") return ""
  return collectText(node.props?.children)
}

describe("RunnerSpecsSection", () => {
  test("renders managed specs with catalog identity and without a delete action", () => {
    const html = renderToStaticMarkup(
      createElement(RunnerSpecsModule.RunnerSpecsSection, sectionProps()),
    )

    expect(html).toContain(">Managed<")
    expect(html).toContain("Default template")
    expect(html).toContain(managedSpec.default_template_name)
    expect(html).not.toContain(managedSpec.template_id)
    expect(html).not.toContain("Internal runner groups")
    expect(html).not.toContain("Globally available")
    expect(html).toContain(`aria-label="Edit ${managedSpec.name}"`)
    expect(html).toContain(">Edit</button>")
    expect(html).not.toContain(">Delete<")
  })

  test("renders a managed edit dialog with only operator controls enabled", () => {
    expect(typeof RunnerSpecsModule.RunnerSpecDialogForm).toBe("function")
    if (typeof RunnerSpecsModule.RunnerSpecDialogForm !== "function") return

    const html = renderToStaticMarkup(
      createElement(RunnerSpecsModule.RunnerSpecDialogForm, {
        editingRunnerSpec: managedSpec,
        runnerSpecForm: managedForm,
        onRunnerSpecFormChange: () => {},
        onRunnerSpecOpenChange: () => {},
        onSubmitRunnerSpec: () => {},
      }),
    )

    expect(html).toContain(managedSpec.default_template_name)
    expect(html).not.toContain(managedSpec.template_id)
    for (const id of [
      "runner-spec-name",
      "runner-spec-labels",
      "runner-spec-required-labels",
      "runner-spec-default-template",
      "runner-spec-github-group",
      "runner-spec-priority",
    ]) {
      expect(isDisabledInput(html, id)).toBe(true)
    }
    for (const id of [
      "runner-spec-max-concurrency",
      "runner-spec-min-idle",
      "runner-spec-enabled",
    ]) {
      expect(isDisabledInput(html, id)).toBe(false)
    }
  })

  test("discloses the platform-wide audience for admin custom specs", () => {
    const customSpec = {
      ...managedSpec,
      name: "large-custom",
      managed_by: "",
      default_template_name: "",
      template_id: "custom-template-id",
    }
    const commonProps = {
      runnerSpecForm: { ...managedForm, name: customSpec.name, template_id: customSpec.template_id },
      onRunnerSpecFormChange: () => {},
      onRunnerSpecOpenChange: () => {},
      onSubmitRunnerSpec: () => {},
    }
    const createHTML = renderToStaticMarkup(createElement(RunnerSpecsModule.RunnerSpecDialogForm, {
      ...commonProps,
      editingRunnerSpec: null,
    }))
    const editHTML = renderToStaticMarkup(createElement(RunnerSpecsModule.RunnerSpecDialogForm, {
      ...commonProps,
      editingRunnerSpec: customSpec,
    }))
    const managedHTML = renderToStaticMarkup(createElement(RunnerSpecsModule.RunnerSpecDialogForm, {
      ...commonProps,
      editingRunnerSpec: managedSpec,
    }))

    for (const html of [createHTML, editHTML]) {
      expect(html).toContain("Platform shared")
      expect(html).toContain("available to every account and organization")
    }
    expect(managedHTML).not.toContain("available to every account and organization")
  })

  test("derives the dialog title from editing identity instead of a populated create name", () => {
    const createSection = RunnerSpecsModule.RunnerSpecsSection(
      sectionProps({
        editingRunnerSpec: null,
        runnerSpecForm: { ...managedForm, name: "draft-custom-spec" },
      }),
    )
    const editSection = RunnerSpecsModule.RunnerSpecsSection(
      sectionProps({
        editingRunnerSpec: managedSpec,
        runnerSpecForm: managedForm,
      }),
    )

    expect(collectText(createSection)).toContain("Create platform runner spec")
    expect(collectText(createSection)).not.toContain("Edit runner spec")
    expect(collectText(editSection)).toContain("Edit runner spec")
  })
})

describe("runner spec submission", () => {
  test("disables the form while template validation is pending", () => {
    const html = renderToStaticMarkup(createElement(RunnerSpecsModule.RunnerSpecDialogForm, {
      editingRunnerSpec: null, runnerSpecForm: managedForm, savingRunnerSpec: true,
      onRunnerSpecFormChange() {}, onRunnerSpecOpenChange() {}, onSubmitRunnerSpec() {},
    }))
    expect(html).toContain('aria-busy="true"')
    expect(html).toMatch(/<fieldset[^>]*disabled/)
    expect(html).toMatch(/<button[^>]*type="submit"[^>]*disabled/)
    expect(html).toContain("Saving")
    expect(html).toContain('href="/admin/sandbox_service"')
  })

  test("submits a managed PATCH with only operator controls and no group updates", async () => {
    expect(typeof RunnerCatalogModule.submitRunnerSpecChanges).toBe("function")
    if (typeof RunnerCatalogModule.submitRunnerSpecChanges !== "function") return

    const requests = []
    await RunnerCatalogModule.submitRunnerSpecChanges({
      request: async (url, options) => {
        requests.push({ url, options })
        return {}
      },
      editingRunnerSpec: managedSpec,
      runnerSpecForm: {
        ...managedForm,
        max_concurrency: "12",
        min_idle: "2",
        enabled: false,
      },
      parseLabels: (value) => value.split(",").filter(Boolean),
    })

    expect(requests).toEqual([
      {
        url: "/runner_specs/ubuntu-24.04",
        options: {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            max_concurrency: 12,
            min_idle: 2,
            enabled: false,
          }),
        },
      },
    ])
  })

  test("submits a custom create payload with required labels and routing fields", async () => {
    const requests = []
    await RunnerCatalogModule.submitRunnerSpecChanges({
      request: async (url, options) => {
        requests.push({ url, options })
        return {}
      },
      editingRunnerSpec: null,
      runnerSpecForm: {
        name: "large-custom",
        labels: "self-hosted, gpu, linux",
        required_labels: "gpu, linux",
        template_id: "custom-template-id",
        runner_group: "qiniu-runners",
        max_concurrency: "6",
        min_idle: "1",
        priority: "20",
        enabled: true,
      },
      parseLabels: (value) => value.split(",").map((label) => label.trim()).filter(Boolean),
    })

    expect(requests).toEqual([
      {
        url: "/runner_specs",
        options: {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            name: "large-custom",
            labels: ["self-hosted", "gpu", "linux"],
            required_labels: ["gpu", "linux"],
            template_id: "custom-template-id",
            runner_group: "qiniu-runners",
            max_concurrency: 6,
            min_idle: 1,
            priority: 20,
            enabled: true,
          }),
        },
      },
    ])
  })

  test("submits a custom PATCH with required labels and mutable catalog fields", async () => {
    const customSpec = {
      ...managedSpec,
      name: "large-custom",
      required_labels: ["gpu"],
      default_template_name: "",
      managed_by: "",
      catalog_revision: 0,
      template_id: "custom-template-id",
    }
    const requests = []
    await RunnerCatalogModule.submitRunnerSpecChanges({
      request: async (url, options) => {
        requests.push({ url, options })
        return {}
      },
      editingRunnerSpec: customSpec,
      runnerSpecForm: {
        ...managedForm,
        name: customSpec.name,
        labels: "self-hosted, gpu, linux",
        required_labels: "gpu, linux",
        template_id: "new-template-id",
        runner_group: "qiniu-runners",
        max_concurrency: "9",
        min_idle: "3",
        priority: "30",
        enabled: false,
      },
      parseLabels: (value) => value.split(",").map((label) => label.trim()).filter(Boolean),
    })

    expect(requests).toEqual([
      {
        url: "/runner_specs/large-custom",
        options: {
          method: "PATCH",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({
            labels: ["self-hosted", "gpu", "linux"],
            required_labels: ["gpu", "linux"],
            template_id: "new-template-id",
            runner_group: "qiniu-runners",
            max_concurrency: 9,
            min_idle: 3,
            priority: 30,
            enabled: false,
          }),
        },
      },
    ])
  })
})
