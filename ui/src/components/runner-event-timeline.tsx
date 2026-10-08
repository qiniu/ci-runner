import { useCallback, useEffect, useRef, useState } from "react"
import { useTranslation } from "react-i18next"
import { Loader2, RefreshCw } from "lucide-react"

import type { RunnerDiagnosticEvent, RunnerEventPage } from "@/admin-types"
import { formatTime } from "@/admin-format"
import { Button } from "@/components/ui/button"
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs"

type RunnerEventTimelineProps = {
  endpoint: string
  request?: (url: string, options?: RequestInit) => Promise<unknown>
  initialEvents?: RunnerDiagnosticEvent[]
  refreshRevision?: number
  autoRefresh?: boolean
  pollIntervalMs?: number
}

const emptyEvents: RunnerDiagnosticEvent[] = []

export function RunnerEventTimeline(props: RunnerEventTimelineProps) {
  return <RunnerEventTimelineContent key={props.endpoint} {...props} />
}

function parseRunnerEventPage(data: unknown, failureMessage: string): RunnerEventPage {
  const page = data as RunnerEventPage | null
  if (!page || !Array.isArray(page.events) || typeof page.has_more !== "boolean") {
    throw new Error(failureMessage)
  }
  return page
}

function mergeRunnerEvents(...pages: RunnerDiagnosticEvent[][]): RunnerDiagnosticEvent[] {
  const byID = new Map<number, RunnerDiagnosticEvent>()
  for (const page of pages) {
    for (const event of page) byID.set(event.id, event)
  }
  return Array.from(byID.values()).sort((left, right) => left.id - right.id)
}

function RunnerEventTimelineContent({
  endpoint,
  request,
  initialEvents = emptyEvents,
  refreshRevision = 0,
  autoRefresh = false,
  pollIntervalMs = 5000,
}: RunnerEventTimelineProps) {
  const { t, i18n } = useTranslation()
  const [pollRevision, setPollRevision] = useState(0)
  const [manualRevision, setManualRevision] = useState(0)
  const endRef = useRef<HTMLDivElement>(null)
  const [events, setEvents] = useState(initialEvents)
  const [eventFilter, setEventFilter] = useState<"all" | "control_log" | "stdout_log" | "stderr_log">("all")
  const [hasMore, setHasMore] = useState(false)
  const [loadingLatest, setLoadingLatest] = useState(false)
  const [loadingEarlier, setLoadingEarlier] = useState(false)
  const [loadError, setLoadError] = useState("")
  const latestRequestGeneration = useRef(0)
  const earlierRequestGeneration = useRef(0)
  const initialized = useRef(false)
  const latestEventID = useRef(0)

  useEffect(() => {
    if (!request) {
      setEvents(initialEvents)
      latestEventID.current = initialEvents[initialEvents.length - 1]?.id ?? 0
      return
    }
    const generation = ++latestRequestGeneration.current
    setLoadingLatest(true)
    setLoadError("")
    const loadLatest = async () => {
      try {
        if (!initialized.current) {
          const page = parseRunnerEventPage(await request(endpoint, { cache: "no-store" }), t("runnerEvents.loadFailed"))
          if (generation !== latestRequestGeneration.current) return
          const pageEvents = Array.isArray(page.events) ? page.events : []
          initialized.current = true
          latestEventID.current = pageEvents[pageEvents.length - 1]?.id ?? 0
          setEvents(pageEvents)
          setHasMore(Boolean(page.has_more))
          return
        }

        let cursor = latestEventID.current
        let newEvents: RunnerDiagnosticEvent[] = []
        for (;;) {
          const page = parseRunnerEventPage(await request(`${endpoint}?after_id=${cursor}`, { cache: "no-store" }), t("runnerEvents.loadFailed"))
          if (generation !== latestRequestGeneration.current) return
          const pageEvents = Array.isArray(page.events) ? page.events : []
          newEvents = mergeRunnerEvents(newEvents, pageEvents)
          const nextCursor = pageEvents[pageEvents.length - 1]?.id
          if (!page.has_more || nextCursor === undefined || nextCursor <= cursor) break
          cursor = nextCursor
        }
        latestEventID.current = newEvents[newEvents.length - 1]?.id ?? latestEventID.current
        if (newEvents.length) setEvents((current) => mergeRunnerEvents(current, newEvents))
      } catch (error) {
        if (generation !== latestRequestGeneration.current) return
        setLoadError(error instanceof Error ? error.message : t("runnerEvents.loadFailed"))
      } finally {
        if (generation === latestRequestGeneration.current) setLoadingLatest(false)
      }
    }
    void loadLatest()
    return () => {
      if (latestRequestGeneration.current === generation) latestRequestGeneration.current += 1
    }
  }, [initialEvents, refreshRevision, manualRevision, pollRevision, autoRefresh, request, endpoint, t])

  useEffect(() => {
    if (!request || !autoRefresh || loadingLatest) return
    const timer = window.setTimeout(() => setPollRevision((revision) => revision + 1), pollIntervalMs)
    return () => window.clearTimeout(timer)
  }, [request, autoRefresh, loadingLatest, pollRevision, pollIntervalMs])

  useEffect(() => () => {
    earlierRequestGeneration.current += 1
  }, [])

  const loadEarlier = useCallback(async () => {
    const beforeID = events[0]?.id
    if (!request || !beforeID || loadingEarlier) return
    const generation = ++earlierRequestGeneration.current
    setLoadingEarlier(true)
    setLoadError("")
    try {
      const page = parseRunnerEventPage(await request(`${endpoint}?before_id=${beforeID}`, { cache: "no-store" }), t("runnerEvents.loadFailed"))
      if (generation !== earlierRequestGeneration.current) return
      setEvents((current) => mergeRunnerEvents(page.events ?? [], current))
      setHasMore(Boolean(page.has_more))
    } catch (error) {
      if (generation !== earlierRequestGeneration.current) return
      setLoadError(error instanceof Error ? error.message : t("runnerEvents.loadFailed"))
    } finally {
      if (generation === earlierRequestGeneration.current) setLoadingEarlier(false)
    }
  }, [events, loadingEarlier, request, endpoint, t])

  const visibleEvents = eventFilter === "all"
    ? events
    : events.filter((event) => event.event_type === eventFilter)

  return (
    <section className="min-w-0 space-y-2" aria-label={t("runnerEvents.title")}>
      <div className="flex flex-col justify-between gap-3 sm:flex-row sm:items-end">
        <div>
          <div className="text-xs font-semibold uppercase tracking-[0.14em] text-muted-foreground">
            {t("runnerEvents.title")}
          </div>
          <div className="mt-1 text-xs text-muted-foreground">{t("runnerEvents.description")}</div>
        </div>
        <div className="flex flex-wrap items-center gap-3">
          <Tabs
            value={eventFilter}
            onValueChange={(value) => setEventFilter(value as typeof eventFilter)}
            className="gap-0"
          >
            <TabsList className="h-8 rounded-md" aria-label={t("runnerEvents.filterLabel")}>
              <TabsTrigger value="all" className="px-2.5 text-xs">{t("runnerEvents.all")}</TabsTrigger>
              <TabsTrigger value="control_log" className="px-2.5 font-mono text-xs">{t("runnerEvents.control")}</TabsTrigger>
              <TabsTrigger value="stdout_log" className="px-2.5 font-mono text-xs">{t("runnerEvents.stdout")}</TabsTrigger>
              <TabsTrigger value="stderr_log" className="px-2.5 font-mono text-xs">{t("runnerEvents.stderr")}</TabsTrigger>
            </TabsList>
          </Tabs>
          {request ? (
            <Button type="button" variant="outline" size="sm" disabled={loadingLatest} onClick={() => setManualRevision((revision) => revision + 1)}>
              <RefreshCw className={loadingLatest ? "animate-spin" : ""} />
              {t("common.refresh")}
            </Button>
          ) : null}
          <Button type="button" variant="outline" size="sm" onClick={() => endRef.current?.scrollIntoView({ behavior: "smooth", block: "end" })}>
            {t("user.scrollToBottom")}
          </Button>
          <span className="text-xs tabular-nums text-muted-foreground">
            {loadingLatest ? <Loader2 className="mr-1 inline size-3 animate-spin" /> : null}
            {t("runnerEvents.count", { count: events.length })}
          </span>
        </div>
      </div>

      {loadError ? (
        <div className="rounded-md border border-destructive/30 bg-destructive/5 px-3 py-2 text-xs text-destructive">{loadError}</div>
      ) : null}
      {hasMore && request ? (
        <div className="flex justify-center">
          <Button type="button" variant="outline" size="sm" disabled={loadingEarlier} onClick={() => void loadEarlier()}>
            {loadingEarlier ? <Loader2 className="animate-spin" /> : null}
            {t("runnerEvents.loadEarlier")}
          </Button>
        </div>
      ) : null}
      <div className="rounded-lg border bg-muted/20 px-4 py-2" aria-busy={loadingLatest}>
        {visibleEvents.length ? visibleEvents.map((event) => (
          <div key={event.id} data-runner-event-id={event.id} className="relative grid gap-1 border-l py-0.5 pl-5 sm:grid-cols-[190px_90px_minmax(0,1fr)] sm:gap-3">
            <span className={`absolute -left-1 top-2 size-2 rounded-full ring-4 ring-background ${event.event_type === "stderr_log" ? "bg-amber-500" : event.event_type === "stdout_log" ? "bg-slate-400" : "bg-sky-500"}`} />
            <time className="font-mono text-[11px] text-muted-foreground">
              {formatTime(event.created_at, i18n.resolvedLanguage, { fractionalSecondDigits: 3 })}
            </time>
            <span className="font-mono text-[11px] text-muted-foreground">
              {event.event_type.replace("_log", "")}{event.stage ? ` · ${event.stage}` : ""}
            </span>
            <pre className="min-w-0 whitespace-pre-wrap break-words font-mono text-xs leading-normal">{event.message.trimEnd()}</pre>
          </div>
        )) : loadError && !events.length ? null : (
          <div className="py-8 text-center text-sm text-muted-foreground">
            {events.length ? t("runnerEvents.emptyFilter", { type: eventFilter.replace("_log", "") }) : loadingLatest ? t("common.loading") : t("runnerEvents.empty")}
          </div>
        )}
      </div>
      <div ref={endRef} />
    </section>
  )
}
