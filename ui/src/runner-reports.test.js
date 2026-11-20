import { describe, expect, test } from "bun:test"
import { reportDistribution, reportPreset, reportQueryParams, reportReturnSearch } from "./runner-reports"

describe("report URL boundaries", () => {
  test("query excludes UI state and preserves identity and snapshot", () => {
    expect(
      reportQueryParams("?from=2026-01-01&owner=github%3A10&snapshot=s&offset=20&dimension=spec").toString(),
    ).toBe("from=2026-01-01&owner=github%3A10&snapshot=s")
  })
  test("only exact report paths are accepted as return destinations", () => {
    const wrap = (value) => `?${new URLSearchParams({ report_return: value })}`
    expect(reportReturnSearch(wrap("/admin/reports?snapshot=s&dimension=owner"))).toBe(
      "?snapshot=s&dimension=owner",
    )
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

describe("chart distributions", () => {
  const group = (key, requests, seconds, samples) => ({
    key,
    label: key,
    requests,
    runtime: { seconds, samples },
  })
  test("Other preserves the full cohort denominator for counts and runtime", () => {
    const page = { items: [group("a", 40, 3600, 2), group("b", 20, 1800, 1)], total: 4 }
    const totals = { requests: 100, runtime: { seconds: 7200, samples: 4 } }
    expect(reportDistribution(page, totals, "requests").map((row) => row.value)).toEqual([40, 20, 40])
    expect(reportDistribution(page, totals, "runtime").map((row) => row.value)).toEqual([1, 0.5, 0.5])
  })
  test("zero-duration samples stay distinct from missing samples, including Other", () => {
    const page = { items: [group("zero", 1, 0, 1), group("missing", 1, 0, 0)], total: 3 }
    expect(
      reportDistribution(page, { requests: 3, runtime: { seconds: 0, samples: 1 } }, "runtime").map(
        (row) => row.value,
      ),
    ).toEqual([0, null, null])
    expect(
      reportDistribution(page, { requests: 3, runtime: { seconds: 0, samples: 2 } }, "runtime").map(
        (row) => row.value,
      ),
    ).toEqual([0, null, 0])
    expect(
      reportDistribution(
        { ...page, total: 2 },
        { requests: 2, runtime: { seconds: 0, samples: 1 } },
        "requests",
      ),
    ).toHaveLength(2)
  })
})
