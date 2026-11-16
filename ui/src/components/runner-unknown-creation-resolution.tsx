import { useEffect, useId, useRef, useState, type FormEvent } from "react"
import { useTranslation } from "react-i18next"
import { Loader2 } from "lucide-react"
import { toast } from "sonner"

import type { RunnerRequestDiagnosis } from "@/admin-types"
import { Button } from "@/components/ui/button"
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle, DialogTrigger } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Switch } from "@/components/ui/switch"

export function RunnerUnknownCreationResolution({ diagnosis, request, onResolved, disabled = false }: {
  diagnosis: RunnerRequestDiagnosis
  request: (url: string, options?: RequestInit) => Promise<unknown>
  onResolved: () => Promise<void>
  disabled?: boolean
}) {
  const { t } = useTranslation()
  const fieldID = useId()
  const [open, setOpen] = useState(false)
  const [reason, setReason] = useState("")
  const [accepted, setAccepted] = useState(false)
  const [pending, setPending] = useState(false)
  const [error, setError] = useState("")
  const submitting = useRef(false)
  const mounted = useRef(false)
  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false }
  }, [])

  const available = diagnosis.unknown_creation_resolution_available === true &&
    Boolean(diagnosis.creation_attempt_id) && Number.isSafeInteger(diagnosis.state.version) &&
    (diagnosis.state.version ?? 0) > 0
  const reasonBytes = new TextEncoder().encode(reason.trim()).length
  const validReason = reasonBytes >= 20 && reasonBytes <= 1024

  function changeOpen(next: boolean) {
    if (submitting.current) return
    setOpen(next)
    setReason("")
    setAccepted(false)
    setError("")
  }

  async function submit(event: FormEvent) {
    event.preventDefault()
    if (submitting.current || disabled || !available || !accepted || !validReason || error) return
    submitting.current = true
    setPending(true)
    const id = diagnosis.state.id
    const attempt = diagnosis.creation_attempt_id
    try {
      // Polling can advance the version while the administrator reads the dialog.
      // Refresh it, but never transfer confirmation to a different attempt.
      const current = await request(`/runner_requests/${encodeURIComponent(id)}/diagnostics`, { cache: "no-store" }) as RunnerRequestDiagnosis
      if (!mounted.current) return
      if (current.state.id !== id || current.creation_attempt_id !== attempt ||
          !current.unknown_creation_resolution_available || !Number.isSafeInteger(current.state.version) ||
          (current.state.version ?? 0) <= 0) {
        throw new Error(t("admin.closeUnknownChanged"))
      }
      await request(`/runner_requests/${encodeURIComponent(id)}/resolve-unknown-creation`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ version: current.state.version, attempt_id: attempt, evidence: reason.trim(), accept_unknown_result: true }),
      })
      if (!mounted.current) return
      setOpen(false)
      setAccepted(false)
      toast.success(t("admin.closeUnknownSuccess"))
      await onResolved()
    } catch (failure) {
      if (mounted.current) {
        setError(failure instanceof Error ? failure.message : t("admin.closeUnknownFailed"))
        setAccepted(false)
      }
    } finally {
      submitting.current = false
      if (mounted.current) setPending(false)
    }
  }

  return (
    <Dialog open={open} onOpenChange={changeOpen}>
      {available ? (
        <DialogTrigger asChild>
          <Button type="button" size="sm" variant="outline" disabled={disabled}>{t("admin.closeUnknown")}</Button>
        </DialogTrigger>
      ) : null}
      <DialogContent className="max-h-[calc(100dvh-2rem)] overflow-y-auto" showCloseButton={!pending}
        onEscapeKeyDown={(event) => { if (pending) event.preventDefault() }}
        onInteractOutside={(event) => { if (pending) event.preventDefault() }}>
        <form className="grid min-w-0 gap-4" onSubmit={(event) => void submit(event)}>
          <DialogHeader>
            <DialogTitle>{t("admin.closeUnknown")}</DialogTitle>
            <DialogDescription>{t("admin.closeUnknownDescription")}</DialogDescription>
          </DialogHeader>
          <p className="break-all text-sm">{diagnosis.state.id} · {diagnosis.state.repository_full_name} · {diagnosis.state.runner_name}</p>
          <div className="grid gap-2">
            <Label htmlFor={`${fieldID}-reason`}>{t("admin.closeUnknownReason")}</Label>
            <Input id={`${fieldID}-reason`} value={reason} onChange={(event) => setReason(event.target.value)}
              disabled={pending} maxLength={1024} autoComplete="off" aria-describedby={`${fieldID}-help`} />
            <p id={`${fieldID}-help`} className="text-sm text-muted-foreground">{t("admin.closeUnknownReasonHelp", { count: reasonBytes })}</p>
          </div>
          <div className="flex items-start gap-3">
            <Switch id={`${fieldID}-accept`} checked={accepted} onCheckedChange={setAccepted} disabled={pending || Boolean(error)} />
            <Label htmlFor={`${fieldID}-accept`} className="block text-sm leading-relaxed">{t("admin.closeUnknownAccept")}</Label>
          </div>
          {!available && !error ? <p role="alert" className="text-sm text-destructive">{t("admin.closeUnknownChanged")}</p> : null}
          {error ? <div role="alert" className="space-y-2 break-words text-sm text-destructive"><p>{error}</p><p>{t("admin.closeUnknownNoRetry")}</p></div> : null}
          <DialogFooter>
            <Button type="button" variant="outline" disabled={pending} onClick={() => changeOpen(false)}>{t("common.cancel")}</Button>
            <Button type="submit" variant="destructive" disabled={pending || disabled || !available || !accepted || !validReason || Boolean(error)}>
              {pending ? <Loader2 className="animate-spin" /> : null}
              {t(pending ? "admin.closeUnknownPending" : "admin.closeUnknownConfirm")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
