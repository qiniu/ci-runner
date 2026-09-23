import { describe, expect, test } from "bun:test"
import { createElement } from "react"
import { renderToStaticMarkup } from "react-dom/server"

import {
  GitHubAppAccountDetail,
  GitHubAppAccountsList,
} from "./github-app-accounts-section"

const installation = {
  id: 987,
  account_id: 9001,
  account_type: "organization",
  account_login: "octo-org",
  account_name: "Octo Organization",
  account_avatar: "https://avatars.example/o.png",
}

describe("GitHubAppAccountsSection", () => {
  test("renders installed accounts as accessible detail links", () => {
    const html = renderToStaticMarkup(createElement(GitHubAppAccountsList, {
      installations: [installation],
      query: "",
      loading: false,
      error: "",
      onQueryChange: () => {},
      onRefresh: () => {},
      onOpen: () => {},
    }))

    expect(html).toContain("GitHub App accounts")
    expect(html).toContain("octo-org")
    expect(html).toContain("Octo Organization")
    expect(html).toContain("Organization")
    expect(html).toContain("Installation #987")
    expect(html).toContain('href="/admin/github_accounts/987"')
    expect(html).toContain('aria-label="Refresh GitHub App accounts"')
  })

  test("renders the installation repository detail", () => {
    const html = renderToStaticMarkup(createElement(GitHubAppAccountDetail, {
      detail: {
        installation,
        repositories: ["octo-org/runner", "octo-org/api"],
      },
      loading: false,
      error: "",
      onBack: () => {},
      onRefresh: () => {},
    }))

    expect(html).toContain("Back to GitHub App accounts")
    expect(html).toContain("2 repositories")
    expect(html).toContain("octo-org/runner")
    expect(html).toContain('href="https://github.com/octo-org/runner"')
    expect(html).toContain('aria-label="Refresh repositories"')
  })

  test("renders intentional empty states", () => {
    const listHTML = renderToStaticMarkup(createElement(GitHubAppAccountsList, {
      installations: [],
      query: "",
      loading: false,
      error: "",
      onQueryChange: () => {},
      onRefresh: () => {},
      onOpen: () => {},
    }))
    const detailHTML = renderToStaticMarkup(createElement(GitHubAppAccountDetail, {
      detail: { installation, repositories: [] },
      loading: false,
      error: "",
      onBack: () => {},
      onRefresh: () => {},
    }))

    expect(listHTML).toContain("No GitHub App installations")
    expect(detailHTML).toContain("No repositories authorized")
  })
})
