import { Loader2, SquareTerminal } from "lucide-react"
import { useTranslation } from "react-i18next"

import { Button } from "@/components/ui/button"
import { useSandboxTerminal } from "@/hooks/use-sandbox-terminal"

type Props = {
  endpoint: string
  request: (url: string, options?: RequestInit) => Promise<unknown>
}

export default function DeploymentTerminal({ endpoint, request }: Props) {
  const { t } = useTranslation()
  const { terminalEl, terminalSession, terminalConnecting, terminalError, connectTerminal, closeTerminalSession } = useSandboxTerminal({
    endpoint,
    available: true,
    request,
    connectingMessage: t("user.connectingTerminal"),
    streamDisconnectedMessage: t("user.terminalDisconnected"),
    connectErrorMessage: t("user.terminalConnectFailed"),
  })

  return <section className="overflow-hidden bg-[#111318] text-slate-100">
    <div className="border-b border-white/10 bg-slate-900/95 px-4 py-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <h3 className="text-sm font-semibold">{t("common.terminal")}</h3>
        <Button type="button" size="sm" variant="outline" className="border-white/15 bg-white/5 text-slate-100 hover:bg-white/10 hover:text-white disabled:opacity-60" disabled={terminalConnecting} onClick={() => terminalSession ? closeTerminalSession() : void connectTerminal()}>
          {terminalConnecting ? <Loader2 className="h-4 w-4 animate-spin" /> : <SquareTerminal className="h-4 w-4" />}
          {terminalSession ? t("user.deploymentTerminalDisconnect") : terminalConnecting ? t("common.connecting") : t("common.connect")}
        </Button>
      </div>
      <p className="mt-1 text-xs text-slate-400">{t("user.deploymentTerminalDescription")}</p>
    </div>
    <div>
      <div className="relative p-2">
        <div ref={terminalEl} className="deployment-console h-[400px] min-w-0 overflow-hidden" />
        {!terminalSession && !terminalConnecting ? <p className="pointer-events-none absolute inset-0 flex items-center justify-center px-4 text-center text-sm text-slate-400">{t("user.connectInteractiveShell")}</p> : null}
      </div>
      {terminalError ? <p role="alert" className="border-t border-red-400/15 bg-red-500/10 px-4 py-3 text-sm text-red-100">{terminalError}</p> : null}
    </div>
  </section>
}
