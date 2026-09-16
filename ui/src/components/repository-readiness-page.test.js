import { describe, expect, test } from "bun:test"
import { createElement } from "react"
import { renderToStaticMarkup } from "react-dom/server"

import "../i18n"
import { RepositoryReadinessPage } from "./repository-readiness-page"

function renderPage(overrides = {}) {
  const props = {
    githubApp: {
      app_slug: "qiniu-runner",
      install_url: "/github-app/install",
      setup_url: "/github-app/setup",
      installations: [{
        id: 1,
        account_id: 1,
        installation_id: 987,
        account_login: "miclle",
        account_name: "Miclle",
        repositories: [],
        created_at: "2026-07-29T00:00:00Z",
        updated_at: "2026-07-29T00:00:00Z",
      }],
    },
    currentLogin: "miclle",
    selectedAccountLogin: "miclle",
    preferences: {
      sandbox: {
        mode: "custom",
        resolved_source: "admin_default",
        api_url: "",
        api_key: { configured: false },
      },
    },
    preferencesLoading: false,
    authorizedRepositories: { 1: ["miclle/qiniu-ci-runner"] },
    repositoryErrors: {},
    loadingRepositoriesFor: null,
    syncingGitHubInstallations: false,
    onLoadAuthorizedRepositories: () => {},
    onSyncGitHubInstallations: () => {},
    onSelectAccount: () => {},
    sandboxConfiguration: createElement(
      "form",
      null,
      createElement("label", null, "Region"),
      createElement("label", null, "API Key"),
      createElement("button", null, "Save settings"),
    ),
    ...overrides,
  }
  return renderToStaticMarkup(createElement(RepositoryReadinessPage, props))
}

describe("RepositoryReadinessPage", () => {
  test("opens every external repository action in a new tab", () => {
    const installURL = "https://github.com/apps/qiniu-runner/installations/new"
    const selectedHTML = renderPage({
      githubApp: {
        app_slug: "qiniu-runner",
        install_url: installURL,
        setup_url: "/github-app/setup",
        installations: [{
          id: 1,
          account_id: 1,
          installation_id: 987,
          account_login: "miclle",
          account_name: "Miclle",
          repositories: [],
          created_at: "2026-07-29T00:00:00Z",
          updated_at: "2026-07-29T00:00:00Z",
        }],
      },
    })
    const emptyHTML = renderPage({
      githubApp: {
        app_slug: "qiniu-runner",
        install_url: installURL,
        setup_url: "/github-app/setup",
        installations: [],
      },
    })

    for (const html of [selectedHTML, emptyHTML]) {
      const externalLinks = html.match(/<a\b[^>]*href="https:\/\/[^"]+"[^>]*>/g) ?? []
      expect(externalLinks).toHaveLength(2)
      for (const link of externalLinks) {
        expect(link).toContain('target="_blank"')
        expect(link).toContain('rel="noopener noreferrer"')
      }
    }
  })

  test("keeps a ready platform Sandbox source as compact readiness information", () => {
    const html = renderPage()

    expect(html).toContain("Runner readiness")
    expect(html).toContain("Repository access")
    expect(html).toContain("Sandbox service")
    expect(html).toContain(">Ready<")
    expect(html).toContain("Provided by the platform")
    expect(html).toContain("Authorized repositories")
    expect(html).toContain("miclle/qiniu-ci-runner")
    expect(html).toContain("No jobs yet")
    expect(html).toContain("Sandbox settings")
    expect(html).toContain('href="/account/preferences"')
    expect(html).not.toContain(">Change<")
    expect(html).not.toContain("Local repositories appear here")
  })

  test("links missing personal Sandbox setup to Settings without embedding the editor", () => {
    const html = renderPage({
      preferences: {
        sandbox: {
          mode: "custom",
          resolved_source: "none",
          api_url: "",
          api_key: { configured: false },
        },
      },
    })

    expect(html).toContain("Setup required")
    expect(html).toContain("Configure Sandbox")
    expect(html).toContain('href="/account/preferences"')
    expect(html).not.toContain(">Region<")
    expect(html).not.toContain(">API Key<")
    expect(html).not.toContain("Save settings")
  })

  test("asks an organization owner to configure Sandbox for repository-only access", () => {
    const html = renderPage({
      selectedAccountLogin: "qiniu",
      githubApp: {
        app_slug: "qiniu-runner",
        install_url: "/github-app/install",
        setup_url: "/github-app/setup",
        installations: [{
          id: 2,
          account_id: 1,
          installation_id: 988,
          account_type: "organization",
          account_login: "qiniu",
          account_name: "Qiniu",
          repositories: [],
          created_at: "2026-07-29T00:00:00Z",
          updated_at: "2026-07-29T00:00:00Z",
        }],
      },
      authorizedRepositories: { 2: ["qiniu/go-sdk"] },
      preferences: {
        sandbox: {
          mode: "custom",
          resolved_source: "none",
          manageable: false,
          api_url: "",
          api_key: { configured: false },
        },
      },
    })

    expect(html).toContain(">Unavailable<")
    expect(html).toContain("The qiniu organization has not configured a Sandbox service")
    expect(html).toContain("Ask an organization owner to configure Sandbox")
    expect(html).toContain("View on GitHub")
    expect(html).not.toContain("Manage on GitHub")
    expect(html).not.toContain("Configure Sandbox")
    expect(html).not.toContain(">Region<")
    expect(html).not.toContain(">API Key<")
  })

  test("does not offer Sandbox settings for a ready repository-only organization", () => {
    const html = renderPage({
      selectedAccountLogin: "qiniu",
      githubApp: {
        app_slug: "qiniu-runner",
        install_url: "/github-app/install",
        setup_url: "/github-app/setup",
        installations: [{
          id: 2,
          account_id: 1,
          installation_id: 988,
          account_type: "organization",
          account_login: "qiniu",
          account_name: "Qiniu",
          repositories: [],
          created_at: "2026-07-29T00:00:00Z",
          updated_at: "2026-07-29T00:00:00Z",
        }],
      },
      authorizedRepositories: { 2: ["qiniu/go-sdk"] },
      preferences: {
        sandbox: {
          mode: "custom",
          resolved_source: "custom",
          manageable: false,
          api_url: "https://sandbox.qiniu.com",
          api_key: { configured: true },
        },
      },
    })

    expect(html).toContain(">Ready<")
    expect(html).toContain("Provided by the qiniu organization")
    expect(html).not.toContain("Sandbox settings")
    expect(html).not.toContain('href="/organizations/qiniu/preferences"')
  })

  test("links manageable organization setup to the organization Settings route", () => {
    const html = renderPage({
      selectedAccountLogin: "qiniu",
      githubApp: {
        app_slug: "qiniu-runner",
        install_url: "/github-app/install",
        setup_url: "/github-app/setup",
        installations: [{
          id: 2,
          account_id: 1,
          installation_id: 988,
          account_type: "organization",
          account_login: "qiniu",
          account_name: "Qiniu",
          repositories: [],
          created_at: "2026-07-29T00:00:00Z",
          updated_at: "2026-07-29T00:00:00Z",
        }],
      },
      authorizedRepositories: { 2: ["qiniu/go-sdk"] },
      preferences: {
        sandbox: {
          mode: "custom",
          resolved_source: "none",
          manageable: true,
          api_url: "",
          api_key: { configured: false },
        },
      },
    })

    expect(html).toContain("Configure Sandbox")
    expect(html).toContain('href="/organizations/qiniu/preferences"')
    expect(html).not.toContain(">Region<")
    expect(html).not.toContain(">API Key<")
  })

  test("shows a retry state when authorized repositories fail to load", () => {
    const html = renderPage({
      authorizedRepositories: {},
      repositoryErrors: { 1: "GitHub denied repository access" },
    })

    expect(html).toContain("Unable to load repositories")
    expect(html).toContain("GitHub denied repository access")
    expect(html).toContain(">Retry<")
    expect(html).not.toContain("Loading repositories from GitHub")
  })
})
