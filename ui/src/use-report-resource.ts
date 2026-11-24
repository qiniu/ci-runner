import { useCallback, useEffect, useState } from "react"

export type ReportFetch = (url: string, options?: RequestInit) => Promise<unknown>
export type Resource<T> = { url: string; revision?: number; data?: T; error?: string }
export function useReportResource<T>(
  url: string,
  request: ReportFetch,
  revision = 0,
  pinnedUrl?: (resource: Resource<T>) => string | undefined,
) {
  const [state, setState] = useState<Resource<T>>({ url: "" })
  // Writing the displayed snapshot to the URL should not reload it. A page reload
  // starts with empty state and fetches the pinned URL, including expiry checks.
  const fetchUrl = state.revision === revision && url === pinnedUrl?.(state) ? state.url : url
  const [attempt, setAttempt] = useState(0)
  const retry = useCallback(() => setAttempt((v) => v + 1), [])
  useEffect(() => {
    const controller = new AbortController()
    let current = true
    if (fetchUrl)
      void request(fetchUrl, { signal: controller.signal })
        .then((data) => {
          if (current) setState({ url: fetchUrl, revision, data: data as T })
        })
        .catch((error) => {
          if (current)
            setState({
              url: fetchUrl,
              revision,
              error: error instanceof Error ? error.message : String(error),
            })
        })
    return () => {
      current = false
      controller.abort()
    }
  }, [fetchUrl, request, revision, attempt])
  return { ...(state.url === fetchUrl && state.revision === revision ? state : { url }), retry }
}
