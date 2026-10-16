import type { ComponentProps } from "react"

// App listens to popstate for its existing History API routes.
export function AppLink({ onClick, ...props }: ComponentProps<"a">) {
  return <a {...props} onClick={(event) => {
    onClick?.(event)
    const anchor = event.currentTarget
    if (event.defaultPrevented || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey ||
      !anchor.hasAttribute("href") || anchor.hasAttribute("download") || (anchor.target && anchor.target !== "_self")) return
    const destination = new URL(anchor.href)
    if (destination.origin !== window.location.origin) return
    event.preventDefault()
    if (destination.href === window.location.href) return
    window.history.pushState(null, "", destination)
    window.dispatchEvent(new window.PopStateEvent("popstate"))
  }} />
}
