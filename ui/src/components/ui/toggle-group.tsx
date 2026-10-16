import * as React from "react"
import * as ToggleGroupPrimitive from "@radix-ui/react-toggle-group"

import { buttonVariants } from "@/components/ui/button"
import { cn } from "@/lib/utils"

function ToggleGroup({ className, ...props }: React.ComponentProps<typeof ToggleGroupPrimitive.Root>) {
  return <ToggleGroupPrimitive.Root data-slot="toggle-group" className={cn("inline-flex w-fit max-w-full flex-wrap items-center gap-1 rounded-lg bg-muted p-[3px]", className)} {...props} />
}

function ToggleGroupItem({ className, ...props }: React.ComponentProps<typeof ToggleGroupPrimitive.Item>) {
  return <ToggleGroupPrimitive.Item data-slot="toggle-group-item" className={cn(
    buttonVariants({ variant: "ghost", size: "sm" }),
    "h-7 text-xs text-foreground data-[state=on]:bg-background data-[state=on]:shadow-sm data-[state=on]:hover:bg-background dark:data-[state=on]:bg-input/30",
    className,
  )} {...props} />
}

export { ToggleGroup, ToggleGroupItem }
