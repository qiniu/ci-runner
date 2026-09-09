import { ArrowLeft, ArrowRight, BookOpen, ChevronRight, ExternalLink, Moon, Sun } from "lucide-react"
import { useTheme } from "next-themes"
import { useEffect, type ReactNode } from "react"
import ReactMarkdown, { defaultUrlTransform, type Components } from "react-markdown"
import { useTranslation } from "react-i18next"
import remarkGfm from "remark-gfm"

import { LanguageSwitcher } from "@/components/language-switcher"
import { QiniuRunnerLogo } from "@/components/qiniu-runner-logo"
import { siteDocumentForPath, siteDocuments, type SiteDocument, type SiteDocumentGroup } from "@/site-docs"

const groupOrder: SiteDocumentGroup[] = ["overview", "getting-started", "guides", "reference"]

export function DocsPage({ path }: { path: string }) {
  const { i18n, t } = useTranslation()
  const documents = siteDocuments(i18n.resolvedLanguage || "en")
  const article = siteDocumentForPath(path, i18n.resolvedLanguage || "en") ?? documents[0]
  const headings = documentHeadings(article.markdown)
  const documentIndex = documents.findIndex((candidate) => candidate.path === article.path)
  const previous = documentIndex > 0 ? documents[documentIndex - 1] : null
  const next = documentIndex + 1 < documents.length ? documents[documentIndex + 1] : null

  useDocumentMetadata(article.title, article.summary)

  return (
    <div className="site-docs min-h-screen bg-[#f7fbfd] text-[#10242f] transition-colors dark:bg-[#061119] dark:text-[#edf8fc]">
      <a
        href="#docs-content"
        className="fixed left-4 top-4 z-[70] -translate-y-24 rounded-md bg-[#07131b] px-4 py-2 text-sm font-semibold text-white transition-transform focus:translate-y-0"
      >
        {t("docs.skipToContent")}
      </a>

      <header className="sticky top-0 z-50 border-b border-[#d9e7ed] bg-[#f7fbfd]/95 backdrop-blur-xl dark:border-white/10 dark:bg-[#061119]/95">
        <div className="mx-auto flex h-17 max-w-[1680px] items-center justify-between gap-4 px-5 sm:px-8 lg:px-10">
          <div className="flex min-w-0 items-center gap-4">
            <a href="/" aria-label={t("common.productHome")} className="shrink-0 rounded-md focus-visible:outline-2 focus-visible:outline-offset-4 focus-visible:outline-[#00aae7]">
              <QiniuRunnerLogo />
            </a>
            <span className="hidden h-7 w-px bg-[#c9dce5] sm:block dark:bg-white/15" aria-hidden="true" />
            <a href="/docs" className="hidden items-center gap-2 text-sm font-semibold text-[#24414f] sm:inline-flex dark:text-[#c5d7e1]">
              <BookOpen className="h-4 w-4 text-[#0088c2] dark:text-[#7ddcff]" />
              {t("docs.documentation")}
            </a>
          </div>
          <div className="flex items-center gap-1.5 sm:gap-2">
            <DocsThemeToggle />
            <LanguageSwitcher className="hover:bg-[#e8f5fa] dark:text-white dark:hover:bg-white/10 dark:hover:text-white" />
            <a
              href="/jobs"
              aria-label={t("docs.openJobs")}
              className="inline-flex h-10 items-center gap-2 rounded-md bg-[#07131b] px-3.5 text-sm font-semibold text-white transition-colors hover:bg-[#0d2a3a] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#00aae7] dark:bg-[#00aae7] dark:text-[#041018] dark:hover:bg-[#27c5f5] sm:px-4"
            >
              <span className="hidden sm:inline">{t("docs.openJobs")}</span>
              <ArrowRight className="h-4 w-4" />
            </a>
          </div>
        </div>
      </header>

      <div className="border-b border-[#d9e7ed] bg-white px-5 py-3 lg:hidden dark:border-white/10 dark:bg-[#0a1922]">
        <p className="mb-2 font-mono text-[10px] font-semibold uppercase tracking-[0.16em] text-[#006b91] dark:text-[#7ddcff]">
          {t("docs.browseGuides")}
        </p>
        <DocsNavigation documents={documents} currentPath={article.path} compact />
      </div>

      <div className="mx-auto grid max-w-[1680px] lg:grid-cols-[16.5rem_minmax(0,1fr)] xl:grid-cols-[16.5rem_minmax(0,1fr)_13.5rem]">
        <aside className="hidden border-r border-[#d9e7ed] lg:block dark:border-white/10">
          <div className="sticky top-17 max-h-[calc(100vh-4.25rem)] overflow-y-auto px-6 py-10">
            <p className="mb-6 font-mono text-[10px] font-semibold uppercase tracking-[0.18em] text-[#006b91] dark:text-[#7ddcff]">
              {t("docs.browseGuides")}
            </p>
            <DocsNavigation documents={documents} currentPath={article.path} />
          </div>
        </aside>

        <main id="docs-content" tabIndex={-1} className="min-w-0 px-5 py-10 sm:px-8 sm:py-14 lg:px-12 xl:px-16">
          <div className="mx-auto max-w-[1080px]">
            <div className="mb-9 flex items-center gap-2 font-mono text-[10px] font-semibold uppercase tracking-[0.16em] text-[#006b91] dark:text-[#7ddcff]">
              <span>{t(groupTranslationKey(article.group))}</span>
              <ChevronRight className="h-3 w-3" />
              <span className="truncate text-[#5b7480] dark:text-[#92abb7]">{article.title}</span>
            </div>

            <ReactMarkdown
              remarkPlugins={[remarkGfm]}
              components={markdownComponents}
              urlTransform={docsURLTransform}
            >
              {article.markdown}
            </ReactMarkdown>

            <nav aria-label={t("docs.navigation")} className="mt-16 grid gap-3 border-t border-[#d9e7ed] pt-8 sm:grid-cols-2 dark:border-white/10">
              {previous ? <DocumentPager document={previous} direction="previous" /> : <span />}
              {next ? <DocumentPager document={next} direction="next" /> : null}
            </nav>
          </div>
        </main>

        <aside className="hidden border-l border-[#d9e7ed] xl:block dark:border-white/10">
          <nav aria-label={t("docs.onThisPage")} className="sticky top-17 max-h-[calc(100vh-4.25rem)] overflow-y-auto px-6 py-10">
            <p className="mb-5 font-mono text-[10px] font-semibold uppercase tracking-[0.18em] text-[#006b91] dark:text-[#7ddcff]">
              {t("docs.onThisPage")}
            </p>
            <ol className="space-y-3 border-l border-[#c8dce5] pl-4 text-[13px] leading-5 text-[#5b7480] dark:border-white/15 dark:text-[#92abb7]">
              {headings.map((heading) => (
                <li key={heading.id}>
                  <a className="transition-colors hover:text-[#006b91] dark:hover:text-[#7ddcff]" href={`#${heading.id}`}>
                    {heading.label}
                  </a>
                </li>
              ))}
            </ol>
          </nav>
        </aside>
      </div>
    </div>
  )
}

function useDocumentMetadata(title: string, summary: string) {
  useEffect(() => {
    const page = globalThis.document
    const previousTitle = page.title
    const existingDescription = page.querySelector<HTMLMetaElement>('meta[name="description"]')
    const previousDescription = existingDescription?.content
    const description = existingDescription ?? page.createElement("meta")

    if (!existingDescription) {
      description.name = "description"
      page.head.append(description)
    }

    page.title = `${title} · Qiniu CI Runner`
    description.content = summary

    return () => {
      page.title = previousTitle
      if (existingDescription) {
        description.content = previousDescription ?? ""
      } else {
        description.remove()
      }
    }
  }, [summary, title])
}

function DocsNavigation({
  documents,
  currentPath,
  compact = false,
}: {
  documents: SiteDocument[]
  currentPath: string
  compact?: boolean
}) {
  const { t } = useTranslation()

  if (compact) {
    return (
      <nav aria-label={t("docs.navigation")} className="flex gap-2 overflow-x-auto pb-1">
        {documents.map((document) => (
          <a
            key={document.path}
            href={document.path}
            aria-current={document.path === currentPath ? "page" : undefined}
            className={`shrink-0 rounded-full border px-3 py-1.5 text-xs font-medium transition-colors ${
              document.path === currentPath
                ? "border-[#00aae7] bg-[#e9f8fd] text-[#005f80] dark:bg-[#00aae7]/15 dark:text-[#7ddcff]"
                : "border-[#d2e2e9] bg-white text-[#4d6875] hover:border-[#8dcfe5] dark:border-white/10 dark:bg-white/5 dark:text-[#a9bfcb]"
            }`}
          >
            {document.title}
          </a>
        ))}
      </nav>
    )
  }

  return (
    <nav aria-label={t("docs.navigation")} className="space-y-7">
      {groupOrder.map((group) => {
        const groupedDocuments = documents.filter((document) => document.group === group)
        return (
          <section key={group}>
            <h2 className="mb-2 text-xs font-semibold text-[#7b909a] dark:text-[#78919d]">{t(groupTranslationKey(group))}</h2>
            <ul className="space-y-1">
              {groupedDocuments.map((document) => (
                <li key={document.path}>
                  <a
                    href={document.path}
                    aria-current={document.path === currentPath ? "page" : undefined}
                    className={`block rounded-md border-l-2 px-3 py-2 text-[13px] leading-5 transition-colors ${
                      document.path === currentPath
                        ? "border-[#00aae7] bg-[#eaf8fd] font-semibold text-[#005f80] dark:bg-[#00aae7]/10 dark:text-[#7ddcff]"
                        : "border-transparent text-[#4d6875] hover:bg-white hover:text-[#006b91] dark:text-[#a9bfcb] dark:hover:bg-white/5 dark:hover:text-[#edf8fc]"
                    }`}
                  >
                    {document.title}
                  </a>
                </li>
              ))}
            </ul>
          </section>
        )
      })}
    </nav>
  )
}

function DocumentPager({ document, direction }: { document: SiteDocument; direction: "previous" | "next" }) {
  const { t } = useTranslation()
  const isNext = direction === "next"
  return (
    <a
      href={document.path}
      className={`group flex min-h-24 flex-col rounded-lg border border-[#d2e2e9] bg-white p-4 transition-all hover:border-[#8dcfe5] hover:shadow-[0_12px_30px_rgba(7,49,73,0.08)] dark:border-white/10 dark:bg-white/[0.035] dark:hover:border-[#27c5f5]/35 ${isNext ? "items-end text-right" : "items-start"}`}
    >
      <span className="flex items-center gap-1.5 font-mono text-[10px] font-semibold uppercase tracking-[0.14em] text-[#6b838e] dark:text-[#78919d]">
        {isNext ? null : <ArrowLeft className="h-3.5 w-3.5" />}
        {t(isNext ? "docs.next" : "docs.previous")}
        {isNext ? <ArrowRight className="h-3.5 w-3.5" /> : null}
      </span>
      <span className="mt-3 text-sm font-semibold text-[#183440] transition-colors group-hover:text-[#006b91] dark:text-[#dcecf3] dark:group-hover:text-[#7ddcff]">
        {document.title}
      </span>
    </a>
  )
}

function DocsThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme()
  const { t } = useTranslation()
  const nextTheme = resolvedTheme === "dark" ? "light" : "dark"
  const label = nextTheme === "dark" ? t("docs.switchThemeDark") : t("docs.switchThemeLight")
  return (
    <button
      type="button"
      data-theme-toggle="docs"
      aria-label={label}
      title={label}
      onClick={() => setTheme(nextTheme)}
      className="inline-flex h-10 w-10 items-center justify-center rounded-md text-[#24414f] transition-colors hover:bg-[#e8f5fa] focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-[#00aae7] dark:text-white dark:hover:bg-white/10"
    >
      <Moon className="h-4 w-4 dark:hidden" />
      <Sun className="hidden h-4 w-4 dark:block" />
    </button>
  )
}

const markdownComponents: Components = {
  h1: ({ children }) => (
    <h1 className="max-w-[720px] text-4xl font-semibold leading-[1.05] tracking-[-0.05em] text-[#07131b] sm:text-5xl dark:text-white">
      {children}
    </h1>
  ),
  h2: ({ children }) => {
    const label = reactNodeText(children)
    return (
      <h2 id={headingID(label)} className="scroll-mt-24 pt-11 text-2xl font-semibold tracking-[-0.035em] text-[#10242f] sm:text-3xl dark:text-[#edf8fc]">
        {children}
      </h2>
    )
  },
  h3: ({ children }) => <h3 className="pt-7 text-xl font-semibold tracking-[-0.02em] text-[#173642] dark:text-[#dcecf3]">{children}</h3>,
  p: ({ children }) => <p className="mt-5 text-[15px] leading-8 text-[#4d6875] sm:text-base dark:text-[#a9bfcb]">{children}</p>,
  ul: ({ children }) => <ul className="mt-5 list-disc space-y-2.5 pl-6 text-[15px] leading-7 text-[#4d6875] marker:text-[#00aae7] sm:text-base dark:text-[#a9bfcb]">{children}</ul>,
  ol: ({ children }) => <ol className="mt-5 list-decimal space-y-2.5 pl-6 text-[15px] leading-7 text-[#4d6875] marker:font-semibold marker:text-[#006b91] sm:text-base dark:text-[#a9bfcb] dark:marker:text-[#7ddcff]">{children}</ol>,
  li: ({ children }) => <li className="pl-1">{children}</li>,
  a: ({ href = "", children }) => {
    const external = /^https?:\/\//.test(href)
    return (
      <a
        href={href}
        target={external ? "_blank" : undefined}
        rel={external ? "noreferrer" : undefined}
        className="font-medium text-[#006b91] underline decoration-[#8dcfe5] underline-offset-4 transition-colors hover:text-[#004e6b] dark:text-[#7ddcff] dark:decoration-[#27c5f5]/45 dark:hover:text-white"
      >
        {children}
        {external ? <ExternalLink className="ml-1 inline h-3.5 w-3.5" aria-hidden="true" /> : null}
      </a>
    )
  },
  strong: ({ children }) => <strong className="font-semibold text-[#173642] dark:text-[#dcecf3]">{children}</strong>,
  code: ({ className, children }) => {
    const block = Boolean(className)
    return block ? (
      <code className={`${className ?? ""} font-mono text-[13px] leading-6 text-[#c9f1ff]`}>{children}</code>
    ) : (
      <code className="rounded bg-[#e8f3f7] px-1.5 py-0.5 font-mono text-[0.88em] text-[#005f80] dark:bg-white/10 dark:text-[#9de7ff]">{children}</code>
    )
  },
  pre: ({ children }) => (
    <div className="mt-6 overflow-hidden rounded-xl border border-[#12394b] bg-[#071923] shadow-[0_14px_35px_rgba(7,25,35,0.12)]">
      <div className="flex h-9 items-center gap-1.5 border-b border-white/10 px-4" aria-hidden="true">
        <span className="h-2 w-2 rounded-full bg-[#ff6b6b]" />
        <span className="h-2 w-2 rounded-full bg-[#ffc44d]" />
        <span className="h-2 w-2 rounded-full bg-[#2bd67b]" />
      </div>
      <pre className="overflow-x-auto p-5">{children}</pre>
    </div>
  ),
  blockquote: ({ children }) => <blockquote className="mt-6 border-l-3 border-[#00aae7] bg-[#edf9fd] px-5 py-1 dark:bg-[#00aae7]/10">{children}</blockquote>,
  table: ({ children }) => (
    <div className="mt-6 overflow-x-auto rounded-lg border border-[#d2e2e9] dark:border-white/10">
      <table className="w-full min-w-[620px] border-collapse text-left text-sm">{children}</table>
    </div>
  ),
  thead: ({ children }) => <thead className="bg-[#eaf6fa] text-[#173642] dark:bg-white/8 dark:text-[#dcecf3]">{children}</thead>,
  tbody: ({ children }) => <tbody className="[&>tr:last-child>td]:border-b-0">{children}</tbody>,
  th: ({ children }) => <th className="border-b border-[#d2e2e9] px-4 py-3 font-semibold dark:border-white/10">{children}</th>,
  td: ({ children }) => <td className="border-b border-[#e2edf1] px-4 py-3.5 align-top leading-6 text-[#4d6875] dark:border-white/8 dark:text-[#a9bfcb]">{children}</td>,
  hr: () => <hr className="my-10 border-[#d2e2e9] dark:border-white/10" />,
}

function documentHeadings(markdown: string): Array<{ id: string; label: string }> {
  return markdown
    .split("\n")
    .map((line) => line.match(/^##\s+(.+)$/)?.[1]?.trim() ?? "")
    .filter(Boolean)
    .map((label) => ({ id: headingID(label), label }))
}

function headingID(label: string): string {
  return label
    .trim()
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, "-")
    .replace(/^-+|-+$/g, "")
}

function reactNodeText(node: ReactNode): string {
  if (typeof node === "string" || typeof node === "number") return String(node)
  if (Array.isArray(node)) return node.map(reactNodeText).join("")
  return ""
}

function docsURLTransform(url: string): string {
  if (url.startsWith("/") || url.startsWith("#")) return url
  return defaultUrlTransform(url)
}

function groupTranslationKey(group: SiteDocumentGroup):
  | "docs.groups.overview"
  | "docs.groups.gettingStarted"
  | "docs.groups.guides"
  | "docs.groups.reference" {
  if (group === "overview") return "docs.groups.overview"
  if (group === "getting-started") return "docs.groups.gettingStarted"
  if (group === "guides") return "docs.groups.guides"
  return "docs.groups.reference"
}
