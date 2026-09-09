import enDeploy from "@/content/site-docs/en/getting-started-deploy.md?raw"
import enCustomTemplates from "@/content/site-docs/en/custom-templates.md?raw"
import enHosted from "@/content/site-docs/en/getting-started-hosted.md?raw"
import enIndex from "@/content/site-docs/en/index.md?raw"
import enLabels from "@/content/site-docs/en/runner-labels.md?raw"
import enTroubleshooting from "@/content/site-docs/en/troubleshooting.md?raw"
import enCache from "@/content/site-docs/en/cache.md?raw"
import enWorkflow from "@/content/site-docs/en/workflow.md?raw"
import zhDeploy from "@/content/site-docs/zh/getting-started-deploy.md?raw"
import zhCustomTemplates from "@/content/site-docs/zh/custom-templates.md?raw"
import zhHosted from "@/content/site-docs/zh/getting-started-hosted.md?raw"
import zhIndex from "@/content/site-docs/zh/index.md?raw"
import zhLabels from "@/content/site-docs/zh/runner-labels.md?raw"
import zhTroubleshooting from "@/content/site-docs/zh/troubleshooting.md?raw"
import zhCache from "@/content/site-docs/zh/cache.md?raw"
import zhWorkflow from "@/content/site-docs/zh/workflow.md?raw"
import { siteDocumentRoutes } from "@/site-doc-routes"

export { isSiteDocumentPath, siteDocumentRoutes } from "@/site-doc-routes"

export type SiteDocumentGroup = "overview" | "getting-started" | "guides" | "reference"
export type SiteDocumentLocale = "en" | "zh"

export type SiteDocument = {
  path: string
  group: SiteDocumentGroup
  title: string
  summary: string
  markdown: string
}

type SiteDocumentDefinition = {
  path: string
  group: SiteDocumentGroup
  markdown: Record<SiteDocumentLocale, string>
}

const definitions: SiteDocumentDefinition[] = [
  { path: siteDocumentRoutes[0], group: "overview", markdown: { en: enIndex, zh: zhIndex } },
  {
    path: siteDocumentRoutes[1],
    group: "getting-started",
    markdown: { en: enHosted, zh: zhHosted },
  },
  {
    path: siteDocumentRoutes[2],
    group: "getting-started",
    markdown: { en: enDeploy, zh: zhDeploy },
  },
  { path: siteDocumentRoutes[3], group: "guides", markdown: { en: enWorkflow, zh: zhWorkflow } },
  { path: siteDocumentRoutes[4], group: "guides", markdown: { en: enCache, zh: zhCache } },
  {
    path: siteDocumentRoutes[5],
    group: "guides",
    markdown: { en: enCustomTemplates, zh: zhCustomTemplates },
  },
  {
    path: siteDocumentRoutes[6],
    group: "guides",
    markdown: { en: enTroubleshooting, zh: zhTroubleshooting },
  },
  {
    path: siteDocumentRoutes[7],
    group: "reference",
    markdown: { en: enLabels, zh: zhLabels },
  },
]

export function siteDocuments(language: string): SiteDocument[] {
  const locale = siteDocumentLocale(language)
  return definitions.map((definition) => {
    const markdown = definition.markdown[locale]
    const { title, summary } = siteDocumentMetadata(markdown)
    return {
      path: definition.path,
      group: definition.group,
      title,
      summary,
      markdown,
    }
  })
}

export function siteDocumentForPath(path: string, language: string): SiteDocument | null {
  return siteDocuments(language).find((document) => document.path === path) ?? null
}

function siteDocumentLocale(language: string): SiteDocumentLocale {
  return language.toLowerCase().startsWith("zh") ? "zh" : "en"
}

function siteDocumentMetadata(markdown: string): { title: string; summary: string } {
  const lines = markdown.trim().split("\n")
  const title = lines.find((line) => line.startsWith("# "))?.slice(2).trim() ?? ""
  const summary = lines.find((line) => {
    const trimmed = line.trim()
    return Boolean(trimmed && !trimmed.startsWith("#") && !trimmed.startsWith("[") && !trimmed.startsWith("|"))
  })?.trim() ?? ""
  return { title, summary }
}
