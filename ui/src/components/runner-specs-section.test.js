import { describe, expect, test } from "bun:test"
import { createElement } from "react"
import { renderToStaticMarkup } from "react-dom/server"
import { RunnerSpecDialogForm, RunnerSpecsSection } from "./runner-specs-section"
import { submitRunnerSpecChanges } from "../hooks/use-runner-catalog"

const spec = {
  name: "ubuntu-24.04", labels: ["self-hosted", "qiniu", "ubuntu-24.04"], required_labels: ["qiniu", "ubuntu-24.04"],
  template_id: "", default_template_name: "ubuntu-24.04-x64", template_source: "public", published: true,
  runner_update_policy: "preinstalled", require_docker: true, fork_sponsorship: true,
  runner_group: "", managed_by: "qiniu/ci-runner", max_concurrency: 8, min_idle: 1, priority: 10, enabled: true,
  created_at: "2026-07-27T00:00:00Z", updated_at: "2026-07-27T00:00:00Z",
}
const form = { ...spec, labels: spec.labels.join(","), required_labels: spec.required_labels.join(","), max_concurrency: "8", min_idle: "1", priority: "10" }
const noop = () => {}
const formProps = { editingRunnerSpec: spec, runnerSpecForm: form, onRunnerSpecFormChange: noop, onRunnerSpecOpenChange: noop, onSubmitRunnerSpec: noop }
const sectionProps = { loading: false, runnerSpecs: [spec], runnerSpecOpen: false, ...formProps, onRefresh: noop, onResetRunnerSpecForm: noop, onEditRunnerSpec: noop, onDeleteRunnerSpec: noop }
const render = (Component, props) => renderToStaticMarkup(createElement(Component, props))
const input = (html, id) => html.match(new RegExp(`<input[^>]*id="${id}"[^>]*>`))?.[0] || ""
const disabled = (html, id) => /\sdisabled(?:=""|(?=[\s/>]))/.test(input(html, id))

 describe("Admin Runner Specs", () => {
  test("legacy ownership does not prevent editing or deleting a public spec", () => {
    const html = render(RunnerSpecsSection, sectionProps)
    expect(html).toContain(">Published<")
    expect(html).not.toContain(">Managed<")
    expect(html).toContain(spec.default_template_name)
    expect(html).toContain(">Delete</button>")
    const edit = render(RunnerSpecDialogForm, formProps)
    expect(disabled(edit, "runner-spec-name")).toBe(true)
    for (const id of ["labels", "required-labels", "default-template", "github-group", "priority", "max-concurrency", "min-idle", "enabled", "published", "fork_sponsorship"]) {
      expect(disabled(edit, `runner-spec-${id}`)).toBe(false)
    }
    expect(edit).toContain("Only published public-template specs")
  })
  test("private specs use physical IDs and cannot publish or sponsor forks", () => {
    const html = render(RunnerSpecDialogForm, { ...formProps, editingRunnerSpec: null, runnerSpecForm: { ...form, template_source: "private", template_id: "private-id", published: false, fork_sponsorship: false } })
    expect(input(html, "runner-spec-template-id")).toContain("private-id")
    expect(input(html, "runner-spec-default-template")).toBe("")
    expect(disabled(html, "runner-spec-published")).toBe(true)
    expect(disabled(html, "runner-spec-fork_sponsorship")).toBe(true)
    expect(html).toContain('/admin/sandbox_service')
  })
  test("pending saves disable the fieldset and cancel button", () => {
    const html = render(RunnerSpecDialogForm, { ...formProps, savingRunnerSpec: true })
    expect(html).toMatch(/<fieldset[^>]*disabled/)
    expect(html).toMatch(/<button[^>]*disabled[^>]*>Cancel/)
    expect(html).toContain("Saving")
  })
  test("public PATCH sends editable routing, publication and independent execution policies", async () => {
    const requests = []
    await submitRunnerSpecChanges({ request: async (url, options) => { requests.push({url, options}) }, editingRunnerSpec: spec,
      runnerSpecForm: { ...form, template_id: "must-not-send", labels: "qiniu,ubuntu-24.04,new-label", priority: "20", require_docker: false, fork_sponsorship: false },
      parseLabels: (value) => value.split(",") })
    expect(requests[0].url).toBe("/runner_specs/ubuntu-24.04")
    expect(requests[0].options.method).toBe("PATCH")
    expect(JSON.parse(requests[0].options.body)).toEqual({ expected_updated_at: spec.updated_at, labels: ["qiniu","ubuntu-24.04","new-label"], required_labels: spec.required_labels,
      template_source: "public", template_id: "", default_template_name: spec.default_template_name, published: true, runner_update_policy: "preinstalled", require_docker: false, fork_sponsorship: false,
      runner_group: "", max_concurrency: 8, min_idle: 1, priority: 20, enabled: true })
  })
  test("private create omits stale public binding and never publishes it", async () => {
    let payload
    await submitRunnerSpecChanges({ request: async (url, options) => { expect(url).toBe("/runner_specs"); payload = JSON.parse(options.body) }, editingRunnerSpec: null,
      runnerSpecForm: { ...form, name: " private-spec ", template_source: "private", template_id: " private-id ", published: true, fork_sponsorship: true, runner_update_policy: "official" }, parseLabels: (value) => value.split(",") })
    expect(payload.name).toBe("private-spec")
    expect(payload.template_id).toBe("private-id")
    expect(payload.default_template_name).toBe("")
    expect(payload.published).toBe(false)
    expect(payload.fork_sponsorship).toBe(false)
    expect(payload).not.toHaveProperty("managed_by")
  })
  test("validation errors propagate so the caller can keep the form open", async () => {
    await expect(submitRunnerSpecChanges({request: async () => { throw new Error("public template invalid") }, editingRunnerSpec: null, runnerSpecForm: form, parseLabels: (v) => v.split(",") })).rejects.toThrow("public template invalid")
  })
})
