import { describe, expect, test } from "bun:test"
import { createElement } from "react"
import { renderToStaticMarkup } from "react-dom/server"

import i18n from "../i18n"
import { DocsPage } from "./docs-page"

describe("DocsPage", () => {
  test("renders the hosted guide in Chinese with public navigation", async () => {
    await i18n.changeLanguage("zh")
    try {
      const html = renderToStaticMarkup(
        createElement(DocsPage, { path: "/docs/getting-started/hosted" }),
      )

      expect(html).toContain("开始使用托管服务")
      expect(html).toContain('aria-label="文档导航"')
      expect(html).toContain('href="/docs/guides/workflow"')
      expect(html).toContain('href="/jobs"')
      expect(html).toContain('aria-label="打开 Jobs"')
      expect(html).toContain("qiniu, ubuntu-24.04")
      expect(html).toContain('id="1-使用-github-登录"')
    } finally {
      await i18n.changeLanguage("en")
    }
  })

  test("renders a table of contents from the current Markdown headings", () => {
    const html = renderToStaticMarkup(
      createElement(DocsPage, { path: "/docs/troubleshooting" }),
    )

    expect(html).toContain('aria-label="On this page"')
    expect(html).toContain('href="#job-stays-queued"')
    expect(html).toContain('id="job-stays-queued"')
    expect(html).toContain('href="/docs/reference/runner-labels"')
    expect(html).toContain('href="/docs/guides/custom-templates"')
    expect(html).toContain('href="/docs/guides/cache"')
  })

  test("keeps source links external while guide links stay on the site", () => {
    const html = renderToStaticMarkup(
      createElement(DocsPage, { path: "/docs/getting-started/deploy" }),
    )

    expect(html).toContain('href="https://app-6a6b0d723d3a24e095531129.app.qiniucc.com/"')
    expect(html).toContain('target="_blank"')
    expect(html).toContain('rel="noreferrer"')
    expect(html).toContain('href="/docs/getting-started/hosted"')
  })
})
