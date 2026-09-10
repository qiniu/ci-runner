import { describe, expect, test } from "bun:test"
import { createElement } from "react"
import { renderToStaticMarkup } from "react-dom/server"

import i18n from "../i18n"
import { RunnerRequestsSection } from "./runner-requests-section"

function renderSelectedRunner() {
  return renderToStaticMarkup(
    createElement(RunnerRequestsSection, {
      hasAccess: false,
      loading: false,
      runners: [],
      filteredRunners: [],
      selected: {
        id: "runner-1",
        status: "completed",
        runner_name: "e2b-runner-1",
        completed_at: "2026-08-10T08:09:10Z",
      },
      selectedID: "runner-1",
      selectedLog: "control.log",
      logText: "",
      createID: "",
      createRepository: "",
      createRunnerSpec: "",
      createLabels: "",
      createRunnerOpen: false,
      runnerStatusFilter: "all",
      runnerRepositoryFilter: "all",
      runnerSpecFilter: "all",
      runnerRepositories: [],
      runnerSpecNames: [],
      onRefresh: () => {},
      onResetCreateRunnerForm: () => {},
      onCreateRunnerOpenChange: () => {},
      onCreateRunnerSubmit: () => {},
      onCreateIDChange: () => {},
      onCreateRepositoryChange: () => {},
      onCreateRunnerSpecChange: () => {},
      onCreateLabelsChange: () => {},
      onStatusFilterChange: () => {},
      onRepositoryFilterChange: () => {},
      onRunnerSpecFilterChange: () => {},
      onSelectRunner: () => {},
      onRetryRunner: () => {},
      onStopRunner: () => {},
      onCopySelectedID: () => {},
      onLoadLog: () => {},
      onSelectedLogChange: () => {},
    }),
  )
}

function renderUnmatchedRunner() {
  const runner = {
    id: "runner-unmatched",
    status: "failed",
    failure_stage: "admission",
    failure_reason: "profile_labels_not_matched",
    runner_name: "e2b-runner-unmatched",
    repository_full_name: "owner/repo",
    updated_at: "2026-08-25T01:02:03Z",
    created_at: "2026-08-25T01:02:03Z",
  }
  return renderToStaticMarkup(
    createElement(RunnerRequestsSection, {
      hasAccess: true,
      loading: false,
      runners: [runner],
      filteredRunners: [runner],
      selected: runner,
      selectedID: runner.id,
      selectedLog: "control.log",
      logText: "",
      createID: "",
      createRepository: "",
      createRunnerSpec: "",
      createLabels: "",
      createRunnerOpen: false,
      runnerStatusFilter: "unmatched",
      runnerRepositoryFilter: "all",
      runnerSpecFilter: "all",
      runnerRepositories: ["owner/repo"],
      runnerSpecNames: [],
      onRefresh: () => {},
      onResetCreateRunnerForm: () => {},
      onCreateRunnerOpenChange: () => {},
      onCreateRunnerSubmit: () => {},
      onCreateIDChange: () => {},
      onCreateRepositoryChange: () => {},
      onCreateRunnerSpecChange: () => {},
      onCreateLabelsChange: () => {},
      onStatusFilterChange: () => {},
      onRepositoryFilterChange: () => {},
      onRunnerSpecFilterChange: () => {},
      onSelectRunner: () => {},
      onRetryRunner: () => {},
      onStopRunner: () => {},
      onCopySelectedID: () => {},
      onLoadLog: () => {},
      onSelectedLogChange: () => {},
    }),
  )
}

describe("RunnerRequestsSection", () => {
  test("labels completed_at as a completed time", async () => {
    await i18n.changeLanguage("zh")
    try {
      const html = renderSelectedRunner()
      expect(html).toContain("完成时间")
      expect(html).not.toContain("已完成2026")
    } finally {
      await i18n.changeLanguage("en")
    }
  })

  test("links the selected runner to its shareable diagnosis", async () => {
    await i18n.changeLanguage("en")
    const html = renderSelectedRunner()

    expect(html).toContain('href="/admin/diagnostics?runner=e2b-runner-1"')
    expect(html).toContain("Run diagnosis")
  })

  test("shows admission label rejection as unmatched without retry actions", async () => {
    await i18n.changeLanguage("zh")
    try {
      const html = renderUnmatchedRunner()
      expect(html).toContain("未匹配")
      expect(html).not.toContain("重试请求")
      expect(html).not.toContain("</svg>重试</button>")
    } finally {
      await i18n.changeLanguage("en")
    }
  })
})
