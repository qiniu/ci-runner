import { describe, expect, test } from "bun:test"
import { createElement } from "react"
import { renderToStaticMarkup } from "react-dom/server"

import i18n from "../i18n"
import { stickyTableHeaderOffset } from "./runner-request-table"
import { RunnerRequestsSection } from "./runner-requests-section"

function renderRunnerRequests(runner, runnerStatusFilter = "all") {
  return renderToStaticMarkup(createElement(RunnerRequestsSection, {
    hasAccess: true,
    loading: false,
    runners: [runner],
    filteredRunners: [runner],
    createID: "",
    createRepository: "",
    createRunnerSpec: "",
    createLabels: "",
    createRunnerOpen: false,
    runnerStatusFilter,
    runnerRepositoryFilter: "all",
    runnerSpecFilter: "all",
    runnerRepositories: runner.repository_full_name ? [runner.repository_full_name] : [],
    runnerSpecNames: runner.runner_spec_name ? [runner.runner_spec_name] : [],
    onRefresh() {},
    onResetCreateRunnerForm() {},
    onCreateRunnerOpenChange() {},
    onCreateRunnerSubmit() {},
    onCreateIDChange() {},
    onCreateRepositoryChange() {},
    onCreateRunnerSpecChange() {},
    onCreateLabelsChange() {},
    onStatusFilterChange() {},
    onRepositoryFilterChange() {},
    onRunnerSpecFilterChange() {},
    onLookupRunnerRequest() {},
    onOpenRunnerRequest() {},
    onRetryRunner() {},
    onStopRunner() {},
  }))
}

describe("RunnerRequestsSection", () => {
  test("offers exact lookup by Runner Name or Request ID", async () => {
    await i18n.changeLanguage("en")
    const html = renderRunnerRequests({
      id: "101445685709",
      status: "completed",
      runner_name: "e2b-101445685709",
      updated_at: "2026-09-06T07:25:09Z",
      created_at: "2026-09-06T07:04:57Z",
    })

    expect(html).toContain("Runner Name / Request ID")
    expect(html).toContain('placeholder="e2b-101445685709"')
    expect(html).toContain("Open request")
  })

  test("places filters left of exact lookup in one responsive toolbar", async () => {
    await i18n.changeLanguage("en")
    const html = renderRunnerRequests({
      id: "101445685709",
      status: "completed",
      runner_name: "e2b-101445685709",
      repository_full_name: "xgo-dev/llgo",
      runner_spec_name: "qiniu-ubuntu-24.04",
      updated_at: "2026-09-06T07:25:09Z",
      created_at: "2026-09-06T07:04:57Z",
    })

    expect(html).toContain('data-testid="runner-request-toolbar"')
    expect(html.indexOf('role="combobox"')).toBeLessThan(html.indexOf('id="runner-request-lookup"'))
    expect(html).toContain("xl:grid-cols-[minmax(140px,180px)_minmax(180px,240px)_minmax(180px,240px)_minmax(360px,1fr)]")
  })

  test("links the visible Runner Name to the canonical request resource", async () => {
    await i18n.changeLanguage("en")
    const html = renderRunnerRequests({
      id: "101445685709",
      status: "completed",
      runner_name: "e2b-101445685709",
      repository_full_name: "xgo-dev/llgo",
      runner_spec_name: "qiniu-ubuntu-24.04",
      updated_at: "2026-09-06T07:25:09Z",
      created_at: "2026-09-06T07:04:57Z",
    })

    expect(html).toContain('href="/admin/runner_requests/101445685709"')
    expect(html).toContain("e2b-101445685709</a>")
    expect(html).not.toContain("Request details")
  })

  test("shows requested labels, the GitHub job ID link, and created time", async () => {
    await i18n.changeLanguage("en")
    const html = renderRunnerRequests({
      id: "101445685709",
      status: "completed",
      runner_name: "e2b-101445685709",
      repository_full_name: "xgo-dev/llgo",
      requested_labels: ["qiniu", "ubuntu-24.04"],
      workflow_job_id: 101445685709,
      github_job_url: "https://github.com/xgo-dev/llgo/actions/runs/34014141709/job/101445685709",
      updated_at: "2026-09-06T07:25:09Z",
      created_at: "2026-09-06T07:04:57Z",
    })

    expect(html).toContain("Requested labels")
    expect(html).toContain("qiniu, ubuntu-24.04")
    expect(html).toContain("Created")
    expect(html).toContain('href="https://github.com/xgo-dev/llgo/actions/runs/34014141709/job/101445685709"')
    expect(html).toContain(">101445685709</a>")
  })

  test("keeps row values on one line and omits the redundant request ID under Runner", async () => {
    await i18n.changeLanguage("en")
    const html = renderRunnerRequests({
      id: "101445685709",
      status: "completed",
      runner_name: "e2b-101445685709",
      repository_full_name: "xgo-dev/llgo",
      requested_labels: ["qiniu", "ubuntu-24.04"],
      updated_at: "2026-09-06T07:25:09Z",
      created_at: "2026-09-06T07:04:57Z",
    })

    expect(html).not.toContain("whitespace-normal")
    expect(html).not.toContain('<div class="truncate text-xs text-muted-foreground">101445685709</div>')
    expect(html).toContain(">e2b-101445685709</a>")
  })

  test("renders the request count after the table", async () => {
    await i18n.changeLanguage("en")
    const html = renderRunnerRequests({
      id: "101445685709",
      status: "completed",
      runner_name: "e2b-101445685709",
      updated_at: "2026-09-06T07:25:09Z",
      created_at: "2026-09-06T07:04:57Z",
    })

    expect(html.indexOf("Showing 1 of 1 runner requests.")).toBeGreaterThan(html.indexOf("</table>"))
  })

  test("lets the request table grow with its content", async () => {
    await i18n.changeLanguage("en")
    const html = renderRunnerRequests({
      id: "101445685709",
      status: "completed",
      runner_name: "e2b-101445685709",
      updated_at: "2026-09-06T07:25:09Z",
      created_at: "2026-09-06T07:04:57Z",
    })

    expect(html).not.toContain("max-h-[calc(100vh-18rem)]")
  })

  test("keeps every request field visible and scrolls horizontally when needed", async () => {
    await i18n.changeLanguage("en")
    const html = renderRunnerRequests({
      id: "101445685709",
      status: "completed",
      runner_name: "e2b-101445685709",
      repository_full_name: "xgo-dev/llgo",
      runner_spec_name: "qiniu-ubuntu-24.04",
      requested_labels: ["qiniu", "ubuntu-24.04"],
      workflow_job_id: 101445685709,
      github_job_url: "https://github.com/xgo-dev/llgo/actions/runs/34014141709/job/101445685709",
      sandbox_id: "ijvttd1cb5dmx85cnuwub",
      updated_at: "2026-09-06T07:25:09Z",
      created_at: "2026-09-06T07:04:57Z",
    })
    expect(html).toContain('data-slot="table-container" class="relative w-full overflow-x-auto"')
    expect(html).toContain("min-w-max")
    expect(html).not.toMatch(/<th[^>]*class="[^"]*\bhidden\b/)
    expect(html).not.toMatch(/<td[^>]*class="[^"]*\bhidden\b/)
    expect(html).not.toMatch(/<td[^>]*class="[^"]*\btruncate\b/)
  })

  test("keeps the table header within the visible table while the page scrolls", () => {
    expect(stickyTableHeaderOffset({ scrollportTop: 56, tableTop: 372, tableHeight: 1_800, headerHeight: 40 })).toBe(0)
    expect(stickyTableHeaderOffset({ scrollportTop: 56, tableTop: -444, tableHeight: 1_800, headerHeight: 40 })).toBe(500)
    expect(stickyTableHeaderOffset({ scrollportTop: 56, tableTop: -1_900, tableHeight: 1_800, headerHeight: 40 })).toBe(1_760)
  })

  test("shows admission label rejection as unmatched without retry actions", async () => {
    await i18n.changeLanguage("zh")
    try {
      const html = renderRunnerRequests({
        id: "runner-unmatched",
        status: "failed",
        failure_stage: "admission",
        failure_reason: "profile_labels_not_matched",
        runner_name: "e2b-runner-unmatched",
        repository_full_name: "owner/repo",
        updated_at: "2026-08-25T01:02:03Z",
        created_at: "2026-08-25T01:02:03Z",
      }, "unmatched")

      expect(html).toContain("未匹配")
      expect(html).not.toContain("重试请求")
      expect(html).not.toContain("</svg>重试</button>")
    } finally {
      await i18n.changeLanguage("en")
    }
  })
})
