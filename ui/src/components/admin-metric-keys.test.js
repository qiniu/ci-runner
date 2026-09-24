import { describe, expect, test } from "bun:test"

import * as AdminFormatModule from "../admin-format"
import i18n from "../i18n"

describe("translated admin metrics", () => {
  test("treats profile label admission rejection as unmatched rather than failed", () => {
    const unmatched = {
      id: "unmatched",
      status: "failed",
      failure_stage: "admission",
      failure_reason: "profile_labels_not_matched",
    }
    const failed = { id: "failed", status: "failed", failure_stage: "sandbox_create" }

    expect(AdminFormatModule.runnerDisplayStatus(unmatched)).toBe("unmatched")
    expect(AdminFormatModule.runnerDisplayStatus(failed)).toBe("failed")

    const metrics = AdminFormatModule.runnerMetrics({
      queued: 0,
      creating: 0,
      running: 0,
      stopping: 0,
      completed: 0,
      failed: 1,
      unmatched: 1,
      runner_specs: 2,
    }, i18n.getFixedT("en"))
    expect(metrics.find((metric) => metric.id === "failed")?.value).toBe(1)
  })

  test("keep stable IDs while labels change with language", () => {
    expect(typeof AdminFormatModule.runnerMetrics).toBe("function")
    expect(typeof AdminFormatModule.accountMetrics).toBe("function")
    if (
      typeof AdminFormatModule.runnerMetrics !== "function"
      || typeof AdminFormatModule.accountMetrics !== "function"
    ) return

    const en = i18n.getFixedT("en")
    const zh = i18n.getFixedT("zh")
    const stats = {
      total_accounts: 4,
      admin_accounts: 1,
      user_accounts: 3,
      oauth_identities: 4,
    }

    const englishAccountMetrics = AdminFormatModule.accountMetrics(stats, en)
    const chineseAccountMetrics = AdminFormatModule.accountMetrics(stats, zh)
    expect(englishAccountMetrics.map((metric) => metric.id)).toEqual([
      "accounts",
      "administrators",
      "users",
      "identities",
    ])
    expect(chineseAccountMetrics.map((metric) => metric.id)).toEqual(
      englishAccountMetrics.map((metric) => metric.id),
    )
    expect(chineseAccountMetrics.map((metric) => metric.label)).not.toEqual(
      englishAccountMetrics.map((metric) => metric.label),
    )

    const englishRunnerMetrics = AdminFormatModule.runnerMetrics({
      queued: 1,
      creating: 2,
      running: 3,
      stopping: 4,
      completed: 5,
      failed: 6,
      unmatched: 7,
      runner_specs: 8,
    }, en)
    const chineseRunnerMetrics = AdminFormatModule.runnerMetrics({
      queued: 1,
      creating: 2,
      running: 3,
      stopping: 4,
      completed: 5,
      failed: 6,
      unmatched: 7,
      runner_specs: 8,
    }, zh)
    expect(englishRunnerMetrics.map((metric) => metric.id)).toEqual([
      "queued",
      "creating",
      "running",
      "stopping",
      "completed",
      "failed",
    ])
    expect(chineseRunnerMetrics.map((metric) => metric.id)).toEqual(
      englishRunnerMetrics.map((metric) => metric.id),
    )
    expect(chineseRunnerMetrics.map((metric) => metric.label)).not.toEqual(
      englishRunnerMetrics.map((metric) => metric.label),
    )
  })
})
