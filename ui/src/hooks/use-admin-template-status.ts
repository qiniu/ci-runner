import { useEffect, useMemo, useState } from "react"

export type TemplateDetails = {
  template_id: string
  build_status?: string
  names: string[] | null
  cpu_count: number
  memory_mb: number
  disk_size_mb: number
  envd_version: string
  created_at: string
  updated_at: string
}

export type TemplateStatus = {
  public?: boolean
  runnable?: boolean
  template?: TemplateDetails
  error?: string
}

type Request = (url: string, options?: RequestInit) => Promise<unknown>
type Entry = {
  promise: Promise<TemplateStatus>
  controller: AbortController
  subscribers: number
  pending: boolean
  expiresAt: number
}

// One loader belongs to one mounted admin list. It never shares metadata across
// sessions, and reference kind is part of identity to preserve existing bindings.
export function createTemplateLookup(request: Request) {
  const entries = new Map<string, Entry>()
  const queue: (() => Promise<void>)[] = []
  let active = 0
  const pump = () => {
    while (active < 3 && queue.length) {
      const run = queue.shift()!
      active++
      void run().finally(() => {
        active--
        pump()
      })
    }
  }

  return {
    invalidate() {
      for (const [key, entry] of entries) {
        if (!entry.pending) entries.delete(key)
      }
    },
    load(
      query: string,
      signal: AbortSignal,
      retry = false,
    ): Promise<TemplateStatus> {
      if (signal.aborted) return Promise.reject(signal.reason)
      for (const [key, entry] of entries) {
        if (
          !entry.pending &&
          ((retry && key === query) || entry.expiresAt <= Date.now())
        )
          entries.delete(key)
      }
      let entry = entries.get(query)
      if (!entry) {
        const controller = new AbortController()
        let run!: () => Promise<void>
        const promise = new Promise<TemplateStatus>((resolve, reject) => {
          run = async () => {
            try {
              controller.signal.throwIfAborted()
              const result = (await request(query, {
                signal: controller.signal,
              })) as TemplateStatus
              controller.signal.throwIfAborted()
              entry!.pending = false
              entry!.expiresAt = Date.now() + 30_000
              resolve(result)
            } catch (error) {
              entry!.pending = false
              if (entries.get(query) === entry) entries.delete(query)
              reject(error)
            }
          }
        })
        entry = {
          promise,
          controller,
          subscribers: 0,
          pending: true,
          expiresAt: 0,
        }
        entries.set(query, entry)
        queue.push(run)
      }
      const current = entry
      current.subscribers++
      const result = new Promise<TemplateStatus>((resolve, reject) => {
        let released = false
        const release = () => {
          if (released) return
          released = true
          signal.removeEventListener("abort", abort)
          current.subscribers--
          if (current.pending && !current.subscribers) {
            if (entries.get(query) === current) entries.delete(query)
            current.controller.abort()
          }
        }
        const abort = () => {
          release()
          reject(signal.reason)
        }
        signal.addEventListener("abort", abort, { once: true })
        current.promise.then(
          (status) => {
            release()
            resolve(status)
          },
          (error) => {
            release()
            reject(error)
          },
        )
      })
      pump()
      return result
    },
  }
}

export type TemplateLookup = ReturnType<typeof createTemplateLookup>

export function useAdminTemplateStatus(
  request: Request,
  template: string,
  referenceType: string,
  sharedLookup?: TemplateLookup,
  refreshVersion = 0,
  delay = 0,
) {
  const lookup = useMemo(
    () => sharedLookup || createTemplateLookup(request),
    [request, sharedLookup],
  )
  const query = `/runner_specs/templates/status?template=${encodeURIComponent(template)}&reference_type=${referenceType}`
  const [attempt, setAttempt] = useState(0)
  const [result, setResult] = useState<{
    key: string
    lookup: TemplateLookup
    status: TemplateStatus
  } | null>(null)
  const key = JSON.stringify([query, attempt, refreshVersion])
  useEffect(() => {
    if (!template) return
    const controller = new AbortController()
    const timer = setTimeout(() => {
      void lookup.load(query, controller.signal, attempt > 0).then(
        (status) => {
          if (!controller.signal.aborted) setResult({ key, lookup, status })
        },
        (error: unknown) => {
          if (!controller.signal.aborted)
            setResult({
              key,
              lookup,
              status: {
                error:
                  (error as { code?: string } | null)?.code || "unavailable",
              },
            })
        },
      )
    }, delay)
    return () => {
      clearTimeout(timer)
      controller.abort()
    }
  }, [lookup, query, template, attempt, key, delay])
  return {
    status:
      result?.key === key && result.lookup === lookup ? result.status : null,
    retry: () => setAttempt((current) => current + 1),
  }
}
