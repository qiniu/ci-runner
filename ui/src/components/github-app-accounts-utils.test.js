import { describe, expect, test } from "bun:test"

import { filterGitHubInstallations } from "./github-app-accounts-utils"

const installations = [
  {
    id: 987,
    account_id: 9001,
    account_type: "organization",
    account_login: "octo-org",
    account_name: "Octo Organization",
    account_avatar: "https://avatars.example/o.png",
  },
  {
    id: 654,
    account_id: 9002,
    account_type: "user",
    account_login: "hubot",
    account_name: "Automation User",
  },
]

describe("GitHub App account utilities", () => {
  test.each([
    ["OCTO", [987]],
    ["automation", [654]],
    ["organization", [987]],
    ["9002", [654]],
    ["987", [987]],
  ])("filters installations by %s", (query, expected) => {
    expect(filterGitHubInstallations(installations, query).map((item) => item.id)).toEqual(expected)
  })

  test("returns all installations for a blank query", () => {
    expect(filterGitHubInstallations(installations, "   ")).toEqual(installations)
  })
})
