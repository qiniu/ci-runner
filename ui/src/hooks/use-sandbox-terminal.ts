import { useCallback, useEffect, useRef, useState } from "react"
import { Terminal } from "@xterm/xterm"
import { FitAddon } from "@xterm/addon-fit"
import "@xterm/xterm/css/xterm.css"

export type SandboxTerminalSession = { session_id: string; pid: number; sandbox_id: string }
type UseSandboxTerminalOptions = {
  endpoint: string
  available: boolean
  request: (url: string, options?: RequestInit) => Promise<unknown>
  connectingMessage: string
  streamDisconnectedMessage: string
  connectErrorMessage: string
}

// Every connection owns a new shell. No session resume, command retries or
// follow-up HTTP requests that would need to locate a particular runnerd node.
export function useSandboxTerminal({ endpoint, available, request, connectingMessage, streamDisconnectedMessage, connectErrorMessage }: UseSandboxTerminalOptions) {
  const [terminalSession, setTerminalSession] = useState<SandboxTerminalSession | null>(null)
  const [terminalError, setTerminalError] = useState("")
  const [terminalConnecting, setTerminalConnecting] = useState(false)
  const terminalEl = useRef<HTMLDivElement | null>(null)
  const terminalRef = useRef<Terminal | null>(null)
  const fitRef = useRef<FitAddon | null>(null)
  const socketRef = useRef<WebSocket | null>(null)
  const timerRef = useRef<number | null>(null)
  const resizeRef = useRef<number | null>(null)
  const generation = useRef(0)

  const closeTerminalSession = useCallback(() => {
    generation.current++
    const socket = socketRef.current
    socketRef.current = null
    socket?.close()
    if (timerRef.current !== null) window.clearTimeout(timerRef.current)
    if (resizeRef.current !== null) window.clearTimeout(resizeRef.current)
    timerRef.current = resizeRef.current = null
    terminalRef.current?.dispose()
    terminalRef.current = null
    fitRef.current = null
    setTerminalSession(null)
    setTerminalConnecting(false)
  }, [])

  useEffect(() => {
    // The endpoint/lifecycle owns this external socket; tear it down before
    // another resource can render or send input through it.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    closeTerminalSession()
    return closeTerminalSession
  }, [endpoint, available, closeTerminalSession])

  const connectTerminal = useCallback(async () => {
    if (!available || terminalRef.current) return
    const attempt = ++generation.current
    setTerminalConnecting(true)
    setTerminalError("")
    const fail = (message: string) => {
      if (generation.current !== attempt) return
      setTerminalError(message)
      closeTerminalSession()
    }
    try {
      const fontFamily = terminalEl.current
        ? getComputedStyle(terminalEl.current).getPropertyValue("--font-mono").trim() || "monospace"
        : "monospace"
      const term = new Terminal({ cursorBlink: true, convertEol: true, fontFamily, fontSize: 13,
        theme: { background: "#111318", foreground: "#e6edf3", cursor: "#36d399", selectionBackground: "#334155" } })
      const fit = new FitAddon()
      term.loadAddon(fit)
      terminalRef.current = term
      fitRef.current = fit
      await document.fonts.load(`13px ${fontFamily}`).catch(() => undefined)
      if (generation.current !== attempt) return
      if (terminalEl.current) { term.open(terminalEl.current); fit.fit() }
      term.writeln(connectingMessage)
      const url = new URL(`${endpoint}/terminal`, window.location.href)
      url.protocol = url.protocol === "https:" ? "wss:" : "ws:"
      url.searchParams.set("cols", String(Math.min(500, term.cols || 100)))
      url.searchParams.set("rows", String(Math.min(500, term.rows || 28)))
      await request(`${endpoint}/terminal`)
      if (generation.current !== attempt) return
      const socket = new WebSocket(url)
      socketRef.current = socket
      let ready = false
      timerRef.current = window.setTimeout(() => fail(connectErrorMessage), 25_000)
      socket.onmessage = (event) => {
        if (socketRef.current !== socket) return
        try {
          const frame = JSON.parse(event.data as string)
          if (frame.type === "ready" && !ready && typeof frame.session_id === "string") {
            ready = true
            if (timerRef.current !== null) window.clearTimeout(timerRef.current)
            timerRef.current = null
            setTerminalSession({ session_id: frame.session_id, pid: frame.pid, sandbox_id: frame.sandbox_id })
            setTerminalConnecting(false)
            term.focus()
          } else if (frame.type === "output" && ready && typeof frame.data === "string") {
            // Decode bytes, not UTF-8 fragments: xterm retains split codepoints.
            term.write(Uint8Array.from(atob(frame.data), (c) => c.charCodeAt(0)))
          } else { fail(connectErrorMessage) }
        } catch (error) { fail(error instanceof Error ? error.message : connectErrorMessage) }
      }
      socket.onerror = () => fail(connectErrorMessage)
      socket.onclose = () => fail(ready ? streamDisconnectedMessage : connectErrorMessage)
      term.onData((data) => {
        if (!ready || socketRef.current !== socket) return
        // Never retain an input queue for a future connection or retry an
        // ambiguous operation. Bound both paste size and browser backpressure.
        if (new TextEncoder().encode(data).byteLength > 4096 || socket.bufferedAmount > 16384 || socket.readyState !== WebSocket.OPEN) {
          fail(streamDisconnectedMessage)
          return
        }
        socket.send(JSON.stringify({ type: "input", data }))
      })
    } catch (error) { fail(error instanceof Error ? error.message : connectErrorMessage) }
  }, [available, closeTerminalSession, connectErrorMessage, connectingMessage, endpoint, request, streamDisconnectedMessage])

  const resizeTerminal = useCallback(() => {
    const term = terminalRef.current
    const socket = socketRef.current
    if (!terminalSession || !term || !fitRef.current || !socket) return
    fitRef.current.fit()
    if (resizeRef.current !== null) window.clearTimeout(resizeRef.current)
    resizeRef.current = window.setTimeout(() => {
      resizeRef.current = null
      if (socketRef.current === socket && socket.readyState === WebSocket.OPEN) {
        socket.send(JSON.stringify({ type: "resize", cols: Math.max(1, Math.min(500, term.cols)), rows: Math.max(1, Math.min(500, term.rows)) }))
      }
    }, 250)
  }, [terminalSession])
  useEffect(() => {
    if (!terminalSession) return
    const observer = new ResizeObserver(resizeTerminal)
    if (terminalEl.current) observer.observe(terminalEl.current)
    return () => observer.disconnect()
  }, [resizeTerminal, terminalSession])
  return { terminalEl, terminalSession, terminalError, terminalConnecting, connectTerminal, closeTerminalSession }
}
