import type { ReactNode } from "react"

import { cn } from "@/lib/utils"

export function DetailField({ label, value, mono = false }: {
  label: string
  value: ReactNode
  mono?: boolean
}) {
  const valueClassName = cn("min-w-0 break-words font-medium", mono && "break-all font-mono")
  return (
    <div className="grid min-w-0 grid-cols-[88px_minmax(0,1fr)] items-baseline gap-2">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={valueClassName}>{value}</div>
    </div>
  )
}
