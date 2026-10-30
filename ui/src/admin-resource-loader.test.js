import { describe, expect, test, spyOn } from "bun:test"
import { createAdminResourceLoader } from "./admin-resource-loader"

function pending() {
  let resolve, reject
  const promise = new Promise((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}

function observer(load) {
  const values = [], errors = [], loading = []
  return { values, errors, loading, options: {
    polling: false, load, accept: (value) => values.push(value), fail: (error) => errors.push(error), loading: (value) => loading.push(value),
  } }
}

describe("admin resource loading", () => {
  test("accepts a slow response after eight poll ticks without overlapping requests", async () => {
    const loader = createAdminResourceLoader()
    const response = pending()
    let calls = 0
    const result = observer(() => { calls++; return response.promise })
    const first = loader.run("runner_requests", result.options)
    for (let tick = 0; tick < 8; tick++) {
      expect(loader.run("runner_requests", { ...result.options, polling: true })).toBe(first)
    }
    response.resolve(["request"])
    await first
    expect(calls).toBe(1)
    expect(result.values).toEqual([["request"]])
    expect(result.loading).toEqual([true, false])
  })

  test("renders list success independently of slow or failed statistics", async () => {
    const loader = createAdminResourceLoader()
    const metric = pending()
    const list = observer(async () => ["request"])
    const statistics = observer(() => metric.promise)
    const statLoad = loader.run("runner_request_metrics", statistics.options)
    await loader.run("runner_requests", list.options)
    expect(list.values).toEqual([["request"]])
    expect(statistics.loading).toEqual([true])
    metric.reject(new Error("statistics unavailable"))
    await statLoad
    expect(list.errors).toEqual([])
    expect(statistics.errors).toHaveLength(1)
  })

  test("paces polls from completion and lets manual refresh bypass the delay", async () => {
    const clock = spyOn(Date, "now").mockReturnValue(1000)
    try {
      const loader = createAdminResourceLoader()
      const list = observer(async () => [])
      const metrics = observer(async () => ({}))
      await loader.run("runner_requests", list.options)
      await loader.run("runner_request_metrics", metrics.options)
      clock.mockReturnValue(6000)
      await loader.run("runner_requests", { ...list.options, polling: true })
      await loader.run("runner_request_metrics", { ...metrics.options, polling: true })
      expect(list.values).toHaveLength(2)
      expect(metrics.values).toHaveLength(1)
      await loader.run("runner_requests", { ...list.options, polling: true })
      expect(list.values).toHaveLength(2)
      await loader.run("runner_requests", list.options)
      expect(list.values).toHaveLength(3)
      clock.mockReturnValue(31000)
      await loader.run("runner_request_metrics", { ...metrics.options, polling: true })
      expect(metrics.values).toHaveLength(2)
    } finally { clock.mockRestore() }
  })

  test("aborts old scope requests and never lets their completion clear a newer load", async () => {
    const loader = createAdminResourceLoader()
    const old = pending(), current = pending()
    let signal
    const previous = observer((value) => { signal = value; return old.promise })
    const next = observer(() => current.promise)
    const one = loader.run("runner_requests", previous.options)
    await Promise.resolve()
    loader.reset()
    expect(signal.aborted).toBe(true)
    const two = loader.run("runner_requests", next.options)
    old.resolve(["old-scope"])
    await one
    expect(previous.values).toEqual([])
    expect(next.loading).toEqual([true])
    expect(loader.run("runner_requests", next.options)).toBe(two)
    current.resolve(["current-scope"])
    await two
    expect(next.values).toEqual([["current-scope"]])
  })
})
