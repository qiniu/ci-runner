import { describe, expect, test } from "bun:test"

import i18n from "../i18n"
import { runnerJobDetailRows } from "./runner-job-detail-rows"
import { runnerRegistrationTargetLabel } from "./runner-registration-utils"
import { RunnerRegistrationTarget } from "./github-runner-registration"
import { createElement } from "react"
import { renderToStaticMarkup } from "react-dom/server"

describe("runnerJobDetailRows", () => {
  test("shares registration labels across forms and Jobs in both languages", () => {
    for (const language of ["en", "zh"]) {
      const t = i18n.getFixedT(language)
      for (const [group, organization, en, zh] of [
        ["", "octo-org", "Repository that triggered the job", "触发 Job 的仓库"],
        ["  ", "octo-org", "Repository that triggered the job", "触发 Job 的仓库"],
        [" Build ", "octo-org", "Organization octo-org · group Build", "组织 octo-org · 组 Build"],
        ["Build", undefined, "Repository owner’s organization · group Build", "仓库所属组织 · 组 Build"],
      ]) {
        const expected = language === "en" ? en : zh
        expect(runnerRegistrationTargetLabel(t, group, organization)).toBe(expected)
        const row = runnerJobDetailRows({ id: "group-label", status: "running", runner_group: group, repository_full_name: organization ? `${organization}/repo` : undefined }, t).find((item) => item.id === "runner_group")
        expect(row.value).toBe(expected)
      }
    }
    const html = renderToStaticMarkup(createElement(RunnerRegistrationTarget, { group: " Build ", organization: "octo-org" }))
    expect(html).toContain(runnerRegistrationTargetLabel(i18n.t.bind(i18n), " Build ", "octo-org"))
  })
  test("keeps failed time and failure reason as distinct stable fields", () => {
    const failedAt = "2026-08-10T08:09:10Z"
    const rows = runnerJobDetailRows(
      {
        id: "job-1",
        status: "failed",
        failed_at: failedAt,
        failure_reason: "runner exited",
      },
      i18n.getFixedT("en"),
      "en",
    )

    expect(new Set(rows.map((row) => row.id)).size).toBe(rows.length)
    expect(rows.find((row) => row.id === "failed_at")).toEqual({
      id: "failed_at",
      label: "Failed at",
      value: new Date(failedAt).toLocaleString("en"),
    })
    expect(rows.find((row) => row.id === "failure_reason")).toEqual({
      id: "failure_reason",
      label: "Failure",
      value: "runner exited",
    })
  })
})
