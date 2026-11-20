import { describe, expect, test } from "bun:test"
import { reportOffset, reportPreset, reportQueryParams, reportReturnSearch } from "./runner-reports"

describe("report URL boundaries", () => {
  test("query excludes UI state and preserves identity and snapshot", () => {
    expect(reportQueryParams("?from=2026-01-01&owner=github%3A10&snapshot=s&offset=20&dimension=spec").toString()).toBe(
      "from=2026-01-01&owner=github%3A10&snapshot=s",
    )
    for (const value of ["-1", "100001", "Infinity", "2.5", "bad"]) expect(reportOffset(value)).toBe(0)
    expect(reportOffset("20")).toBe(20)
  })
  test("only exact report paths are accepted as return destinations", () => {
    const wrap = (value) => `?${new URLSearchParams({ report_return: value })}`
    expect(reportReturnSearch(wrap("/admin/reports?snapshot=s&dimension=owner"))).toBe("?snapshot=s&dimension=owner")
    for (const value of [
      "https://evil.test/admin/reports?x=1",
      "//evil.test/admin/reports?x=1",
      "/admin/reports/other?x=1",
      "/admin/reports?x=1#bad",
      "/admin/runner_requests",
    ])
      expect(reportReturnSearch(wrap(value))).toBeNull()
  })
  test("presets use local calendar dates across DST and UTC midnight", () => {
    expect(reportPreset(7, "America/New_York", new Date("2026-11-02T02:00:00Z"))).toEqual({
      from: "2026-10-26",
      to: "2026-11-02",
    })
    expect(reportPreset(30, "Asia/Singapore", new Date("2026-11-20T23:00:00Z"))).toEqual({
      from: "2026-10-23",
      to: "2026-11-22",
    })
  })
})
