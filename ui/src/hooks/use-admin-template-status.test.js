import { describe, expect, test, spyOn } from "bun:test"
import { createTemplateLookup } from "./use-admin-template-status"
const signal = () => new AbortController().signal
const info = { public: true, runnable: true }
describe("admin template lookup", () => {
  test("coalesces identical lookups, caches for 30 seconds and keeps reference types separate", async () => {
    const clock = spyOn(Date, "now").mockReturnValue(1000)
    try {
      const queries = []
      const lookup = createTemplateLookup(async (query) => {
        queries.push(query)
        return info
      })
      await Promise.all([
        lookup.load("?template=a&type=name", signal()),
        lookup.load("?template=a&type=name", signal()),
      ])
      await lookup.load("?template=a&type=name", signal())
      expect(queries).toHaveLength(1)
      await lookup.load("?template=a&type=id", signal())
      expect(queries).toHaveLength(2)
      clock.mockReturnValue(31001)
      await lookup.load("?template=a&type=name", signal())
      expect(queries).toHaveLength(3)
      lookup.invalidate()
      await lookup.load("?template=a&type=name", signal())
      expect(queries).toHaveLength(4)
    } finally {
      clock.mockRestore()
    }
  })
  test("bounds provider requests to three and never caches failures", async () => {
    let active = 0
    let maximum = 0
    let calls = 0
    const lookup = createTemplateLookup(async () => {
      calls++
      maximum = Math.max(maximum, ++active)
      await Bun.sleep(5)
      active--
      if (calls === 1) throw new Error("unavailable")
      return info
    })
    await expect(lookup.load("failed", signal())).rejects.toThrow("unavailable")
    await expect(lookup.load("failed", signal())).resolves.toEqual(info)
    await Promise.all(
      Array.from({ length: 9 }, (_, index) =>
        lookup.load(String(index), signal()),
      ),
    )
    expect(maximum).toBe(3)
    expect(calls).toBe(11)
  })
  test("cancels a shared request only after its final subscriber leaves", async () => {
    let requestSignal
    const lookup = createTemplateLookup((_query, options) => {
      requestSignal = options.signal
      return new Promise((_resolve, reject) =>
        requestSignal.addEventListener(
          "abort",
          () => reject(requestSignal.reason),
          { once: true },
        ),
      )
    })
    const first = new AbortController()
    const second = new AbortController()
    const one = lookup.load("same", first.signal).catch((error) => error)
    const two = lookup.load("same", second.signal).catch((error) => error)
    first.abort()
    await one
    expect(requestSignal.aborted).toBe(false)
    second.abort()
    await two
    expect(requestSignal.aborted).toBe(true)
  })
  test("does not start abandoned queued requests", async () => {
    const started = []
    const releases = []
    const lookup = createTemplateLookup(async (query) => {
      started.push(query)
      await new Promise((resolve) => releases.push(resolve))
      return info
    })
    const running = ["a", "b", "c"].map((query) => lookup.load(query, signal()))
    const controller = new AbortController()
    const queued = lookup
      .load("abandoned", controller.signal)
      .catch((error) => error)
    controller.abort()
    releases.forEach((release) => release())
    await Promise.all([...running, queued])
    expect(started).toEqual(["a", "b", "c"])
  })
})
