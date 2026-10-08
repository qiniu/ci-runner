import type { AppTFunction } from "@/i18n"

export type LocalizedLogMessageKey =
  | "app.loadFailed"
  | "common.loading"
  | "user.loadingRunnerLog"
  | "user.runnerLogEmpty"
  | "user.runnerLogFailed"

export type LocalizedLogText =
  | { kind: "text"; text: string }
  | { kind: "message"; key: LocalizedLogMessageKey }

export function localizedLogTextForView(logText: LocalizedLogText, t: AppTFunction) {
  return logText.kind === "message" ? t(logText.key) : logText.text
}

export function runnerLogTextForView(selectedID: string, logText: LocalizedLogText, t: AppTFunction) {
  if (!selectedID) return t("app.noRunnerSelected")
  return localizedLogTextForView(logText, t)
}
