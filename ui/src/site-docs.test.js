import { describe, expect, test } from "bun:test"

import {
  isSiteDocumentPath,
  siteDocumentForPath,
  siteDocumentRoutes,
  siteDocuments,
} from "./site-docs"

describe("public site documentation catalog", () => {
  test("publishes a fixed set of exact public routes", () => {
    expect(siteDocumentRoutes).toEqual([
      "/docs",
      "/docs/getting-started/hosted",
      "/docs/getting-started/deploy",
      "/docs/guides/workflow",
      "/docs/guides/custom-templates",
      "/docs/troubleshooting",
      "/docs/reference/runner-labels",
    ])
    expect(isSiteDocumentPath("/docs/getting-started/hosted")).toBe(true)
    expect(isSiteDocumentPath("/docs/not-a-guide")).toBe(false)
    expect(isSiteDocumentPath("/docs/getting-started/hosted/extra")).toBe(false)
  })

  test("keeps the selected article while changing language", () => {
    expect(siteDocumentForPath("/docs/guides/workflow", "en")?.title).toBe("Run your first workflow")
    expect(siteDocumentForPath("/docs/guides/workflow", "zh-CN")?.title).toBe("运行第一个工作流")
    expect(siteDocumentForPath("/docs/guides/custom-templates", "en")?.title).toBe("Build and use a custom runner template")
    expect(siteDocumentForPath("/docs/guides/custom-templates", "zh-CN")?.title).toBe("构建并使用自定义 Runner 模板")
    expect(siteDocumentForPath("/docs/not-a-guide", "en")).toBeNull()
  })

  test("derives navigation metadata from every Markdown source", () => {
    const english = siteDocuments("en")
    const chinese = siteDocuments("zh")

    expect(english).toHaveLength(7)
    expect(chinese).toHaveLength(7)
    expect(english.map((document) => document.path)).toEqual(chinese.map((document) => document.path))
    expect(english.every((document) => document.title && document.summary && document.markdown.startsWith("# "))).toBe(true)
    expect(chinese.every((document) => document.title && document.summary && document.markdown.startsWith("# "))).toBe(true)
    english.forEach((document, index) => {
      expect(document.markdown.match(/^## /gm)?.length).toBe(
        chinese[index].markdown.match(/^## /gm)?.length,
      )
    })
    expect([...english, ...chinese].some((document) => document.markdown.includes("qiniu-sandbox"))).toBe(false)
  })

  test("documents the custom template build and runtime contracts", () => {
    for (const language of ["en", "zh"]) {
      const markdown = siteDocumentForPath("/docs/guides/custom-templates", language)?.markdown

      expect(markdown).toContain("qshell sandbox template build --wait")
      expect(markdown).toContain("Status: ready")
      expect(markdown).toContain("/opt/actions-runner/config.sh")
      expect(markdown).toContain("command -v bash base64 install cp mkdir id")
      expect(markdown).toContain("sudo -E -u runner")
      expect(markdown).toContain("enabled")
      expect(markdown).toContain("required labels")
    }
  })

  test("documents managed and operator-configured runner labels and resource sizes", () => {
    for (const language of ["en", "zh"]) {
      const markdown = siteDocumentForPath("/docs/reference/runner-labels", language)?.markdown

      for (const label of [
        "ubuntu-slim-large",
        "ubuntu-22.04-large",
        "ubuntu-24.04-large",
        "ubuntu-26.04-large",
        "ubuntu-latest-large",
      ]) {
        expect(markdown).toContain(`[qiniu, ${label}]`)
      }
      expect(markdown).toContain("8 vCPU")
      expect(markdown).toContain("8 GiB")
      expect(markdown).toContain("20 GiB")
      expect(markdown).toContain("80 GiB")
      expect(markdown).toContain("Sandbox provider")
      expect(markdown).toContain(language === "en" ? "Operator-configured" : "Operator 配置")
      expect(markdown).toContain(language === "en" ? "custom Runner Spec" : "自定义 Runner Spec")
    }
  })
})
