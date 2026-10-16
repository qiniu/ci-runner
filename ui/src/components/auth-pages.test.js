import { describe, expect, test } from "bun:test"
import { createElement } from "react"
import { renderToStaticMarkup } from "react-dom/server"

import * as authPages from "./auth-pages"
import i18n from "../i18n"

const { AccessDeniedPage, NotFoundPage, SessionLoadingPage, SignInPage } = authPages

describe("authentication route pages", () => {
  test("renders protected-route guidance in Chinese", async () => {
    await i18n.changeLanguage("zh")
    try {
      const html = renderToStaticMarkup(
        createElement(SignInPage, {
          oauthEnabled: true,
          returnTo: "/repositories",
        }),
      )

      expect(html).toContain("登录后继续")
      expect(html).toContain("使用 GitHub 继续")
      expect(html).toContain('href="/docs/getting-started/hosted"')
      expect(html).toContain("先阅读托管版快速开始")
      expect(html).toContain("返回首页")
    } finally {
      await i18n.changeLanguage("en")
    }
  })

  test("preserves the protected destination through GitHub sign-in", () => {
    const html = renderToStaticMarkup(
      createElement(SignInPage, {
        oauthEnabled: true,
        returnTo: "/github/pulls/octo/repo/12/jobs?job=runner-1",
      }),
    )

    expect(html).toContain("Sign in to continue")
    expect(html).toContain("Pull request jobs")
    expect(html).not.toContain("lucide-lock-keyhole")
    expect(html).toContain(
      'href="/auth/github/login?return_to=%2Fgithub%2Fpulls%2Focto%2Frepo%2F12%2Fjobs%3Fjob%3Drunner-1"',
    )
    expect(html).toContain('href="/"')
  })

  test("explains when sign-in is unavailable", () => {
    const html = renderToStaticMarkup(
      createElement(SignInPage, {
        oauthEnabled: false,
        returnTo: "/jobs",
      }),
    )

    expect(html).toContain("Sign-in unavailable")
    expect(html).toContain("GitHub OAuth is not configured")
    expect(html).not.toContain("/auth/github/login?")
  })

  test("gives authenticated users a way out of an unauthorized admin route", () => {
    const html = renderToStaticMarkup(
      createElement(AccessDeniedPage, {
        login: "octocat",
        onSignOut: () => {},
      }),
    )

    expect(html).toContain("Access denied")
    expect(html).toContain("octocat")
    expect(html).toContain("Open Jobs")
    expect(html).toContain("Sign out")
  })

  test("renders distinct loading and not-found states", () => {
    expect(renderToStaticMarkup(createElement(SessionLoadingPage))).toContain("Checking your session")
    expect(renderToStaticMarkup(createElement(NotFoundPage))).toContain("Page not found")
  })

  test("offers retry when the session check fails", () => {
    expect(typeof authPages.SessionErrorPage).toBe("function")
    if (!authPages.SessionErrorPage) return

    const html = renderToStaticMarkup(
      createElement(authPages.SessionErrorPage, {
        onRetry: () => {},
      }),
    )

    expect(html).toContain("Unable to check your session")
    expect(html).toContain("Try again")
  })
})

test("repository-first Job and deployment routes preserve OAuth destinations", () => {
  for (const [returnTo, label] of [
    ["/github/octo/repo/pr/12/jobs?job=old-job", "Pull request jobs"],
    ["/github/octo/repo/pull_requests/12/jobs?job=old-job", "Pull request jobs"],
    ["/github/octo/repo/jobs?branch=feature/preview&commit=abc", "Branch jobs"],
    ["/github/octo/repo/deployments?tag=release/v1", "Deployments"],
    ["/github/octo/repo/branch/feature%2Fpreview/jobs?commit=abc", "Branch jobs"],
    ["/github/octo/repo/pull_requests/12/deployments/d1?revision=2", "Deployments"],
  ]) {
    const html = renderToStaticMarkup(createElement(SignInPage, { oauthEnabled: true, returnTo }))
    expect(html).toContain(label)
    expect(html).toContain(`return_to=${encodeURIComponent(returnTo)}`)
  }
})
