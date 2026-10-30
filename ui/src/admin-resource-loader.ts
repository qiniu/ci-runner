import type { AdminDataResource } from "@/app-load-policy"

export const adminRequestTimeoutMs = 30_000
export const adminMetricsPollIntervalMs = 30_000

// Each resource owns its request and completion clock. A slow statistic must
// neither invalidate a successful list response nor block the next list poll.
export function createAdminResourceLoader() {
  const active = new Map<AdminDataResource, { controller: AbortController; promise: Promise<void>; stop: () => void }>()
  const completed = new Map<AdminDataResource, number>()

  return {
    isLoading(resource: AdminDataResource) { return active.has(resource) },
    run<T>(resource: AdminDataResource, options: {
      polling: boolean
      load: (signal: AbortSignal) => Promise<T>
      accept: (value: T) => void
      fail: (error: unknown) => void
      loading: (value: boolean) => void
    }): Promise<void> {
      const existing = active.get(resource)
      if (existing) return existing.promise
      const interval = resource === "runner_request_metrics" ? adminMetricsPollIntervalMs : 5_000
      const lastCompleted = completed.get(resource)
      if (options.polling && lastCompleted !== undefined && Date.now() - lastCompleted < interval) {
        return Promise.resolve()
      }
      const controller = new AbortController()
      const timer = setTimeout(() => controller.abort(new DOMException("Request timed out", "TimeoutError")), adminRequestTimeoutMs)
      const entry = { controller, promise: Promise.resolve(), stop: () => { clearTimeout(timer); options.loading(false) } }
      active.set(resource, entry)
      options.loading(true)
      entry.promise = Promise.resolve().then(() => options.load(controller.signal))
        .then((value) => { if (active.get(resource) === entry) options.accept(value) })
        .catch((error: unknown) => { if (active.get(resource) === entry) options.fail(error) })
        .finally(() => {
          clearTimeout(timer)
          if (active.get(resource) !== entry) return
          active.delete(resource)
          completed.set(resource, Date.now())
          options.loading(false)
        })
      return entry.promise
    },
    reset() {
      const pending = [...active.values()]
      active.clear()
      completed.clear()
      for (const entry of pending) {
        entry.controller.abort()
        entry.stop()
      }
    },
  }
}
