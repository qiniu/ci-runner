import { useCallback, useEffect, useRef, useState } from "react"
import { Terminal } from "xterm"
import { FitAddon } from "xterm-addon-fit"
import "xterm/css/xterm.css"

export type SandboxTerminalSession = {
  session_id: string
  pid: number
  sandbox_id: string
}

type UseSandboxTerminalOptions = {
  endpoint: string
  available: boolean
  request: (url: string, options?: RequestInit) => Promise<unknown>
  connectingMessage: string
  streamDisconnectedMessage: string
  connectErrorMessage: string
}

export function useSandboxTerminal({
  endpoint,
  available,
  request,
  connectingMessage,
  streamDisconnectedMessage,
  connectErrorMessage,
}: UseSandboxTerminalOptions) {
  const [terminalSession, setTerminalSession] = useState<SandboxTerminalSession | null>(null)
  const [terminalError, setTerminalError] = useState("")
  const [terminalConnecting, setTerminalConnecting] = useState(false)
  const terminalEl = useRef<HTMLDivElement | null>(null)
  const terminalRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const eventSourceRef = useRef<EventSource | null>(null)
  const terminalDataDisposableRef = useRef<{ dispose: () => void } | null>(null)
  const terminalSessionRef = useRef<SandboxTerminalSession | null>(null)
  const resizeTimeoutRef = useRef<number | null>(null)

  const closeTerminalSession = useCallback(
    (session = terminalSessionRef.current) => {
      eventSourceRef.current?.close()
      eventSourceRef.current = null
      terminalDataDisposableRef.current?.dispose()
      terminalDataDisposableRef.current = null
      if (resizeTimeoutRef.current) {
        window.clearTimeout(resizeTimeoutRef.current)
        resizeTimeoutRef.current = null
      }
      terminalRef.current?.dispose()
      terminalRef.current = null
      fitRef.current = null
      terminalSessionRef.current = null
      setTerminalSession(null)
      if (session) {
        void fetch(`${endpoint}/terminal/${encodeURIComponent(session.session_id)}`, {
          method: "DELETE",
          credentials: "same-origin",
          keepalive: true,
        }).catch(() => undefined)
      }
    },
    [endpoint],
  )

  useEffect(() => {
    setTerminalError("")
    closeTerminalSession()
    return () => {
      closeTerminalSession()
    }
  }, [closeTerminalSession])

  const connectTerminal = useCallback(async () => {
    if (!available || terminalConnecting || terminalSession) return
    setTerminalConnecting(true)
    setTerminalError("")
    try {
      // Canvas renderers need the resolved stack, not a CSS var() expression.
      const fontFamily = terminalEl.current
        ? getComputedStyle(terminalEl.current).getPropertyValue("--font-mono").trim() || "monospace"
        : "monospace"
      const term = new Terminal({
        cursorBlink: true,
        convertEol: true,
        fontFamily,
        fontSize: 13,
        theme: {
          background: "#111318",
          foreground: "#e6edf3",
          cursor: "#36d399",
          selectionBackground: "#334155",
        },
      })
      const fit = new FitAddon()
      term.loadAddon(fit)
      terminalRef.current = term
      fitRef.current = fit
      if (terminalEl.current) {
        await document.fonts.load(`13px ${fontFamily}`).catch(() => undefined)
        if (terminalRef.current !== term) return
        term.open(terminalEl.current)
        fit.fit()
      }
      term.writeln(connectingMessage)
      const cols = term.cols || 100
      const rows = term.rows || 28
      const session = (await request(`${endpoint}/terminal`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ cols, rows }),
      })) as SandboxTerminalSession
      if (terminalRef.current !== term) {
        void fetch(`${endpoint}/terminal/${encodeURIComponent(session.session_id)}`, {
          method: "DELETE",
          credentials: "same-origin",
          keepalive: true,
        }).catch(() => undefined)
        return
      }
      setTerminalSession(session)
      terminalSessionRef.current = session
      term.writeln(`Connected to ${session.sandbox_id} pid=${session.pid}`)
      const events = new EventSource(`${endpoint}/terminal/${encodeURIComponent(session.session_id)}/events`, {
        withCredentials: true,
      })
      eventSourceRef.current = events
      events.onmessage = (event) => {
        try {
          term.write(JSON.parse(event.data) as string)
        } catch {
          term.write(event.data)
        }
      }
      events.onerror = () => {
        setTerminalError(streamDisconnectedMessage)
        closeTerminalSession(session)
      }
      let inputClosed = false
      let inputSending = false
      const inputQueue: string[] = []
      const sendNextInput = async () => {
        if (inputClosed || inputSending || inputQueue.length === 0) return
        inputSending = true
        const data = inputQueue.join("")
        inputQueue.length = 0
        try {
          await request(`${endpoint}/terminal/${encodeURIComponent(session.session_id)}/input`, {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ data }),
          })
        } catch (error) {
          if (!inputClosed) {
            setTerminalError(error instanceof Error ? error.message : "Failed to send input")
          }
        } finally {
          inputSending = false
          void sendNextInput()
        }
      }
      const inputDisposable = term.onData((data) => {
        inputQueue.push(data)
        void sendNextInput()
      })
      terminalDataDisposableRef.current = {
        dispose: () => {
          inputClosed = true
          inputQueue.length = 0
          inputDisposable.dispose()
        },
      }
    } catch (error) {
      setTerminalError(error instanceof Error ? error.message : connectErrorMessage)
      closeTerminalSession()
    } finally {
      setTerminalConnecting(false)
    }
  }, [
    available,
    closeTerminalSession,
    connectErrorMessage,
    connectingMessage,
    endpoint,
    request,
    streamDisconnectedMessage,
    terminalConnecting,
    terminalSession,
  ])

  const resizeTerminal = useCallback(() => {
    const session = terminalSession
    const term = terminalRef.current
    const fit = fitRef.current
    if (!session || !term || !fit) return
    fit.fit()
    if (resizeTimeoutRef.current) {
      window.clearTimeout(resizeTimeoutRef.current)
    }
    resizeTimeoutRef.current = window.setTimeout(() => {
      resizeTimeoutRef.current = null
      void request(`${endpoint}/terminal/${encodeURIComponent(session.session_id)}/resize`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ cols: term.cols, rows: term.rows }),
      }).catch(() => undefined)
    }, 250)
  }, [endpoint, request, terminalSession])

  useEffect(() => {
    if (!terminalSession) return
    const observer = new ResizeObserver(resizeTerminal)
    if (terminalEl.current) observer.observe(terminalEl.current)
    return () => observer.disconnect()
  }, [resizeTerminal, terminalSession])

  return {
    terminalEl,
    terminalSession,
    terminalError,
    terminalConnecting,
    connectTerminal,
    closeTerminalSession,
  }
}
