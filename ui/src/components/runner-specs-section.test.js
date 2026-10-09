import { describe, expect, test } from "bun:test"
import { createElement } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { RunnerSpecDialogForm, RunnerSpecsSection } from "./runner-specs-section"
import { AdminTemplateInfo } from "./admin-template-info"
import { submitRunnerSpecChanges } from "../hooks/use-runner-catalog"

const spec = {
  name: "ubuntu-24.04", labels: ["self-hosted", "qiniu", "ubuntu-24.04"], required_labels: ["qiniu", "ubuntu-24.04"],
  template_id: "", default_template_name: "ubuntu-24.04-x64", template_source: "public", published: true,
  runner_group: "", managed_by: "qiniu/ci-runner", max_concurrency: 8, min_idle: 1, priority: 10, enabled: true,
  created_at: "2026-07-27T00:00:00Z", updated_at: "2026-07-27T00:00:00Z",
}
const form = { ...spec, template: spec.default_template_name, labels: spec.labels.join(","), required_labels: spec.required_labels.join(","), max_concurrency: "8", min_idle: "1", priority: "10" }
const noop = () => {}
const formProps = { request: async () => ({ public: true, runnable: true }), editingRunnerSpec: spec, runnerSpecForm: form, onRunnerSpecFormChange: noop, onRunnerSpecOpenChange: noop, onSubmitRunnerSpec: noop }
const sectionProps = { loading: false, runnerSpecs: [spec], runnerSpecOpen: false, ...formProps, onRefresh: noop, onResetRunnerSpecForm: noop, onEditRunnerSpec: noop, onDeleteRunnerSpec: noop }
const render = (Component, props) => renderToStaticMarkup(createElement(Component, props))
const input = (html, id) => html.match(new RegExp(`<(?:input|textarea)[^>]*id="${id}"[^>]*>(?:[^<]*</textarea>)?`))?.[0] || ""
const disabled = (html, id) => /\sdisabled(?:=""|(?=[\s/>]))/.test(input(html, id))

 describe("Admin Runner Specs", () => {
  test("legacy ownership does not prevent editing or deleting a public spec", () => {
    const html = render(RunnerSpecsSection, sectionProps)
    expect(html).toContain('aria-label="ubuntu-24.04"')
    expect(html).toContain("Catalog display")
    expect(html).not.toContain("<table")
    for (const label of ["Required labels", "Max concurrency", "Min idle", "Priority", "GitHub runner group"]) expect(html).toContain(label)
    expect(html).not.toContain(">Managed<")
    expect(html).toContain(spec.default_template_name)
    expect(html).toContain('aria-label="Delete ubuntu-24.04"')
    const edit = render(RunnerSpecDialogForm, formProps)
    expect(disabled(edit, "runner-spec-name")).toBe(true)
    for (const id of ["labels", "required-labels", "template", "github-group", "priority", "max-concurrency", "min-idle", "enabled"]) {
      expect(disabled(edit, `runner-spec-${id}`)).toBe(false)
    }
    expect(edit).toContain("Only enabled public-template specs with catalog display turned on")
    for (const id of ["update-policy", "require_docker", "fork_sponsorship"]) {
      expect(edit).not.toContain(`id="runner-spec-${id}"`)
    }
  })
  test("template field accepts IDs without a binding selector; display waits for verification", () => {
    const html = render(RunnerSpecDialogForm, { ...formProps, editingRunnerSpec: null, runnerSpecForm: { ...form, template: "public-physical-id", published: false } })
    expect(input(html, "runner-spec-template")).toContain("public-physical-id")
    expect(html).not.toContain('id="runner-spec-template-source"')
    expect(disabled(html, "runner-spec-published")).toBe(true)
    expect(html).toContain("Checking template visibility")
  })
  test("pending saves disable the fieldset and cancel button", () => {
    const html = render(RunnerSpecDialogForm, { ...formProps, savingRunnerSpec: true })
    expect(html).toMatch(/<fieldset[^>]*disabled/)
    expect(html).toMatch(/<button[^>]*disabled[^>]*>Cancel/)
    expect(html).toContain("Saving")
  })
  test("public PATCH sends routing and catalog settings only", async () => {
    const requests = []
    await submitRunnerSpecChanges({ request: async (url, options) => { requests.push({url, options}) }, editingRunnerSpec: spec,
      runnerSpecForm: { ...form, template_id: "must-not-send", labels: "qiniu,ubuntu-24.04,new-label", priority: "20", },
      parseLabels: (value) => value.split(",") })
    expect(requests[0].url).toBe("/runner_specs/ubuntu-24.04")
    expect(requests[0].options.method).toBe("PATCH")
    expect(JSON.parse(requests[0].options.body)).toEqual({ expected_updated_at: spec.updated_at, labels: ["qiniu","ubuntu-24.04","new-label"], required_labels: spec.required_labels,
      template_source: "public", template_id: "", default_template_name: spec.default_template_name, published: true, runner_group: "", max_concurrency: 8, min_idle: 1, priority: 20, enabled: true })
  })
  test("new public ID references preserve ID execution and allow publication", async () => {
    let payload
    await submitRunnerSpecChanges({ request: async (url, options) => {
      if (url.startsWith("/runner_specs/templates/status?")) return { template_source: "private", template_id: "public-id", default_template_name: "", public: true, runnable: true }
      expect(url).toBe("/runner_specs"); payload = JSON.parse(options.body)
    }, editingRunnerSpec: null, runnerSpecForm: { ...form, name: " large-spec ", template: " public-id ", published: true }, parseLabels: (value) => value.split(",") })
    expect(payload.name).toBe("large-spec")
    expect(payload.template_id).toBe("public-id")
    expect(payload.default_template_name).toBe("")
    expect(payload.template_source).toBe("private")
    expect(payload.published).toBe(true)
    expect(payload).not.toHaveProperty("managed_by")
  })
  test("unchanged physical IDs can be edited without metadata access", async () => {
    const requests = []
    await submitRunnerSpecChanges({ request: async (url, options) => { requests.push({url, payload: JSON.parse(options.body)}) },
      editingRunnerSpec: { ...spec, template_source: "private", template_id: "fixed-id", default_template_name: "" },
      runnerSpecForm: { ...form, template: "fixed-id", published: false }, parseLabels: (value) => value.split(",") })
    expect(requests).toHaveLength(1)
    expect(requests[0].payload.template_source).toBe("private")
    expect(requests[0].payload.template_id).toBe("fixed-id")
    expect(requests[0].payload.default_template_name).toBe("")
  })
  test("validation errors propagate so the caller can keep the form open", async () => {
    await expect(submitRunnerSpecChanges({request: async () => { throw new Error("public template invalid") }, editingRunnerSpec: null, runnerSpecForm: form, parseLabels: (v) => v.split(",") })).rejects.toThrow("public template invalid")
  })
})

test("latest build failure is distinct from missing default build", () => {
  const props = {onRetry: noop, showSuccessMessage: false, status: {public: true, runnable: true, template: {template_id: "tpl", names: ["team/name"], build_status: "failed"}}}
  const html = render(AdminTemplateInfo, props)
  expect(html).toContain("The latest template build failed, but a usable default build is still available.")
  expect(html).not.toContain("No usable default build")
  const missing = render(AdminTemplateInfo, {...props, status: {...props.status, runnable: false}})
  expect(missing).toContain("No usable default build is available yet")
  expect(missing).not.toContain("a usable default build is still available")
})
