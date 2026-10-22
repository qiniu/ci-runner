import { expect, spyOn, test } from "bun:test"

import { scheduleAtMonotonicDeadline } from "./terminal-fault-support"

test("keeps traffic blocked until the monotonic deadline after an early timer", () => {
  let now = 0
  let pending = () => {}
  let restorations = 0
  const delays: number[] = []
  const clock = spyOn(performance, "now").mockImplementation(() => now)
  const schedule = spyOn(globalThis, "setTimeout").mockImplementation(((callback: () => void, delay: number) => {
    pending = callback
    delays.push(delay)
    return delays.length as unknown as ReturnType<typeof setTimeout>
  }) as typeof setTimeout)
  const cancelTimer = spyOn(globalThis, "clearTimeout").mockImplementation(() => {})
  try {
    const cancel = scheduleAtMonotonicDeadline(5000, () => { restorations++ })
    expect(delays).toEqual([5000])
    now = 4999.28
    pending()
    expect(restorations).toBe(0)
    expect(delays).toEqual([5000, 1])
    now = 5000
    pending()
    expect(restorations).toBe(1)
    cancel()
    expect(cancelTimer).toHaveBeenCalledWith(2)
  } finally {
    clock.mockRestore()
    schedule.mockRestore()
    cancelTimer.mockRestore()
  }
})
