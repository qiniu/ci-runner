export const siteDocumentRoutes = [
  "/docs",
  "/docs/getting-started/hosted",
  "/docs/getting-started/deploy",
  "/docs/guides/workflow",
  "/docs/guides/cache",
  "/docs/guides/custom-templates",
  "/docs/troubleshooting",
  "/docs/reference/runner-labels",
] as const

const siteDocumentRouteSet = new Set<string>(siteDocumentRoutes)

export function isSiteDocumentPath(path: string): boolean {
  return siteDocumentRouteSet.has(path)
}
