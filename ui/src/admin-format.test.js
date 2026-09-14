import { describe, expect, test } from "bun:test"

import { formatRunnerCleanupDuration, formatTime } from "./admin-format"

describe("formatTime", () => {
  test("formats timestamps with the selected application locale", () => {
    const timestamp = "2026-08-10T08:09:10Z"
    const date = new Date(timestamp)

    expect(formatTime(timestamp, "en")).toBe(date.toLocaleString("en"))
    expect(formatTime(timestamp, "zh")).toBe(date.toLocaleString("zh"))
  })

  test("shows milliseconds when diagnostics need precise event ordering", () => {
    expect(formatTime("2026-08-10T08:09:10.987", "zh", { fractionalSecondDigits: 3 }))
      .toBe("2026/8/10 08:09:10.987")
  })

  test("treats Go zero time as an absent timestamp", () => {
    expect(formatTime("0001-01-01T00:00:00Z", "en")).toBe("-")
  })
})

describe("formatRunnerCleanupDuration", () => {
  test("uses the terminal timestamp selected by persisted status", () => {
    const runner = {
      status: "completed",
      stopping_at: "2026-08-10T08:09:10.250Z",
      completed_at: "2026-08-10T08:10:25.500Z",
      failed_at: "2026-08-10T09:00:00Z",
    }

    expect(formatRunnerCleanupDuration(runner, "in progress")).toBe("1m 15.25s")
    expect(formatRunnerCleanupDuration({ ...runner, status: "failed" }, "in progress")).toBe("50m 49.75s")
  })

  test("distinguishes active cleanup from unavailable or invalid timing", () => {
    expect(formatRunnerCleanupDuration({
      status: "stopping",
      stopping_at: "2026-08-10T08:09:10Z",
    }, "in progress")).toBe("in progress")
    expect(formatRunnerCleanupDuration({ status: "running" }, "in progress")).toBe("-")
    for (const status of ["stopping", "completed", "failed"]) {
      expect(formatRunnerCleanupDuration({
        status,
        stopping_at: "0001-01-01T00:00:00Z",
        completed_at: "2026-08-10T08:09:10Z",
        failed_at: "2026-08-10T08:09:10Z",
      }, "in progress")).toBe("-")
    }
    expect(formatRunnerCleanupDuration({
      status: "completed",
      stopping_at: "2026-08-10T08:10:25Z",
      completed_at: "2026-08-10T08:09:10Z",
    }, "in progress")).toBe("-")
  })
})
