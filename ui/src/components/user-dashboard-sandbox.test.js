import { describe, expect, mock, test } from "bun:test"
import { createElement } from "react"
import { renderToStaticMarkup } from "react-dom/server"

import i18n from "../i18n"

mock.module("xterm", () => ({
  Terminal: class Terminal {},
}))
mock.module("xterm-addon-fit", () => ({
  FitAddon: class FitAddon {},
}))

const { UserDashboard } = await import("./user-dashboard")

function renderDashboard(overrides = {}) {
  const props = {
    authSession: {
      authenticated: true,
      oauth_enabled: true,
      login: "miclle",
      role: "user",
    },
    githubApp: {
      setup_url: "/github-app/setup",
      installations: [{
        id: 1,
        account_id: 1,
        installation_id: 987,
        account_type: "user",
        account_login: "miclle",
        repositories: [],
        created_at: "2026-07-29T00:00:00Z",
        updated_at: "2026-07-29T00:00:00Z",
      }],
    },
    locationPath: "/account/preferences",
    productTourOnboarding: null,
    userPreferences: {
      sandbox: {
        mode: "custom",
        resolved_source: "none",
        api_url: "",
        api_key: { configured: false },
      },
    },
    userPreferencesScope: "account",
    runners: [],
    runnerTotal: 0,
    loadingRunnerHistory: false,
    selectedKey: "",
    selectedJobID: "",
    page: "settings",
    selectedRepositoryAccountLogin: "miclle",
    accountSettingsRoute: { tab: "preferences" },
    authorizedRepositories: { 1: ["miclle/qiniu-ci-runner"] },
    repositoryErrors: {},
    loadingRepositoriesFor: null,
    syncingGitHubInstallations: false,
    onLoadAuthorizedRepositories: () => {},
    onSyncGitHubInstallations: () => {},
    onNavigateProductTourStart: () => {},
    onSaveProductTourOnboarding: async () => {},
    onSaveSandboxConfig: async () => {},
    onDeleteSandboxAPIKey: async () => {},
    onNavigate: () => {},
    onNavigateRepositoryAccount: () => {},
    onNavigateAccountSettings: () => {},
    onOpenJob: () => {},
    onLoadJobGroup: async () => null,
    onLoadRunnerHistory: () => {},
    request: async () => [],
    onSelectKey: () => {},
    onSignOut: () => {},
    ...overrides,
  }
  return renderToStaticMarkup(createElement(UserDashboard, props))
}

describe("Sandbox service Settings", () => {
  test("keeps platform Runner Specs at the top level and account specs in Settings", () => {
    const platformHTML = renderDashboard({
      locationPath: "/runner-specs",
      page: "runner-specs",
      githubApp: {
        settings_manageability: true,
        setup_url: "/github-app/setup",
        installations: [
          {
            id: 1,
            account_id: 1,
            installation_id: 987,
            account_type: "user",
            account_login: "miclle",
            manageable: true,
            repositories: [],
            created_at: "2026-07-29T00:00:00Z",
            updated_at: "2026-07-29T00:00:00Z",
          },
          {
            id: 3,
            account_id: 1,
            installation_id: 989,
            account_type: "organization",
            account_login: "qiniu",
            manageable: true,
            repositories: [],
            created_at: "2026-07-29T00:00:00Z",
            updated_at: "2026-07-29T00:00:00Z",
          },
        ],
      },
    })

    expect(platformHTML).toContain('href="/runner-specs"')
    expect(platformHTML).toContain("Platform Runner Specs")
    expect(platformHTML).toContain("Browse platform-managed Runner Specs and copy their workflow labels.")
    expect(platformHTML).not.toContain("Choose which account or organization should receive availability and concurrency overrides.")
    expect(platformHTML).not.toContain('id="platform-runner-spec-scope"')

    const settingsHTML = renderDashboard({
      locationPath: "/account/runner-specs",
      accountSettingsRoute: { accountLogin: "miclle", tab: "runner-specs" },
    })
    expect(settingsHTML).toContain("Custom Runner Specs")
    expect(settingsHTML).not.toContain("Choose which account or organization should receive availability and concurrency overrides.")
  })

  test("distinguishes custom Runner Specs in account Settings from the platform catalog", () => {
    const html = renderDashboard({
      accountSettingsRoute: { accountLogin: "miclle", tab: "preferences" },
    })

    expect(html).toContain(">Custom Runner Specs</button>")
    expect(html).not.toContain(">Runner Specs</button>")
    expect(html).not.toContain("Runner Types")
  })

  test("keeps the supported region selector in Settings without a custom endpoint control", () => {
    const html = renderDashboard()

    expect(html).toContain("Select Sandbox region")
    expect(html).not.toContain("Custom endpoint")
    expect(html).not.toContain("https://sandbox.example.test")
  })

  test("shows only personal and manageable organization scopes", () => {
    const html = renderDashboard({
      githubApp: {
        setup_url: "/github-app/setup",
        installations: [
          {
          id: 1,
          account_id: 1,
          installation_id: 987,
          account_type: "user",
          account_login: "miclle",
          manageable: true,
          repositories: [],
          created_at: "2026-07-29T00:00:00Z",
          updated_at: "2026-07-29T00:00:00Z",
          },
          {
            id: 2,
            account_id: 1,
            installation_id: 988,
            account_type: "organization",
            account_login: "qbox",
            manageable: false,
            repositories: [],
            created_at: "2026-07-29T00:00:00Z",
            updated_at: "2026-07-29T00:00:00Z",
          },
          {
            id: 3,
            account_id: 1,
            installation_id: 989,
            account_type: "organization",
            account_login: "qiniu",
            manageable: true,
            repositories: [],
            created_at: "2026-07-29T00:00:00Z",
            updated_at: "2026-07-29T00:00:00Z",
          },
        ],
      },
    })

    expect(html).toContain(">miclle<")
    expect(html).toContain(">qiniu<")
    expect(html).not.toContain(">qbox<")
  })

  test("keeps the personal Settings scope without a personal GitHub App installation", () => {
    const html = renderDashboard({
      githubApp: {
        setup_url: "/github-app/setup",
        installations: [
          {
            id: 2,
            account_id: 1,
            installation_id: 988,
            account_type: "organization",
            account_login: "qbox",
            manageable: false,
            repositories: [],
            created_at: "2026-07-29T00:00:00Z",
            updated_at: "2026-07-29T00:00:00Z",
          },
          {
            id: 3,
            account_id: 1,
            installation_id: 989,
            account_type: "organization",
            account_login: "qiniu",
            manageable: true,
            repositories: [],
            created_at: "2026-07-29T00:00:00Z",
            updated_at: "2026-07-29T00:00:00Z",
          },
        ],
      },
    })

    expect(html).toContain(">miclle<")
    expect(html).toContain(">qiniu<")
    expect(html).not.toContain(">qbox<")
  })

  test("keeps an organization Settings route pending until manageability is loaded", () => {
    const html = renderDashboard({
      accountSettingsRoute: {
        accountLogin: "qiniu",
        tab: "preferences",
      },
      githubApp: {
        setup_url: "/github-app/setup",
        installations: [{
          id: 3,
          account_id: 1,
          installation_id: 989,
          account_type: "organization",
          account_login: "qiniu",
          repositories: [],
          created_at: "2026-07-29T00:00:00Z",
          updated_at: "2026-07-29T00:00:00Z",
        }],
      },
    })

    expect(html).toContain("Checking organization permissions...")
    expect(html).not.toContain("Opening your account Settings...")
  })

  test("does not render preferences loaded for a different Settings scope", () => {
    const html = renderDashboard({
      accountSettingsRoute: {
        accountLogin: "qiniu",
        tab: "preferences",
      },
      githubApp: {
        settings_manageability: true,
        setup_url: "/github-app/setup",
        installations: [{
          id: 3,
          account_id: 1,
          installation_id: 989,
          account_type: "organization",
          account_login: "qiniu",
          manageable: true,
          repositories: [],
          created_at: "2026-07-29T00:00:00Z",
          updated_at: "2026-07-29T00:00:00Z",
        }],
      },
      userPreferences: {
        sandbox: {
          mode: "custom",
          resolved_source: "admin_default",
          api_url: "https://us-south-1-sandbox.qiniuapi.com",
          api_key: { configured: true },
          manageable: false,
        },
      },
      userPreferencesScope: "github_installation:988",
    })

    expect(html).not.toContain("Organization managed")
    expect(html).not.toContain("The organization has an effective Sandbox service")
    expect(html).toContain("Action required")
  })
})

describe("Jobs grouping", () => {
  test("keeps a detached branch group selected across languages", async () => {
    const headSHA = "deadbeefcafebabe"
    const selectedKey = `branch:miclle/qiniu-ci-runner:detached:${headSHA}`
    const runners = [{
      id: "detached-job",
      status: "completed",
      repository_full_name: "miclle/qiniu-ci-runner",
      head_sha: headSHA,
      assigned_job_name: "detached build",
      workflow_name: "CI",
      created_at: "2026-08-10T00:00:00Z",
      updated_at: "2026-08-10T00:01:00Z",
      completed_at: "2026-08-10T00:01:00Z",
    }]

    try {
      for (const language of ["en", "zh"]) {
        await i18n.changeLanguage(language)
        const html = renderDashboard({
          locationPath: "/jobs",
          page: "home",
          runners,
          runnerTotal: 1,
          selectedKey,
        })

        expect(html).toContain('<h2 class="truncate text-2xl font-semibold">miclle/qiniu-ci-runner</h2>')
        expect(html).not.toContain(i18n.t("user.groupNotFound"))
      }
    } finally {
      await i18n.changeLanguage("en")
    }
  })
})
