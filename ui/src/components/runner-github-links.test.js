import { describe, expect, test } from "bun:test"

import { githubJobLink, workflowRunURL } from "./runner-github-links"

describe("GitHub Job navigation", () => {
  test("prefers the original Job URL over the Run or assigned Job IDs", () => {
    const job = {
      github_job_url: "https://github.com/o/r/actions/runs/42/job/100",
      workflow_run_id: 42,
      assigned_job_id: 200,
    }
    expect(githubJobLink(job)).toEqual({ url: job.github_job_url, labelKey: "user.openGitHubJob" })
    expect(workflowRunURL(job)).toBe("https://github.com/o/r/actions/runs/42")
  })

  test("falls back to the Workflow Run when the Job URL is missing", () => {
    expect(githubJobLink({ repository_full_name: "o/r", workflow_run_id: 42 })).toEqual({
      url: "https://github.com/o/r/actions/runs/42",
      labelKey: "user.openGitHubRun",
    })
  })

  test("preserves the origin of an available Run URL", () => {
    expect(workflowRunURL({ github_job_url: "https://github.example/o/r/actions/runs/42/job/100", workflow_run_id: 42 }))
      .toBe("https://github.example/o/r/actions/runs/42")
  })

  test("does not infer another Run URL when the supplied Job URL has no matching marker", () => {
    const job = {
      github_job_url: "https://github.example/o/r/actions/runs/43/job/100",
      repository_full_name: "o/r",
      workflow_run_id: 42,
    }
    expect(workflowRunURL(job)).toBe("")
    expect(githubJobLink(job)).toEqual({ url: job.github_job_url, labelKey: "user.openGitHubJob" })
  })

  test("omits navigation without enough GitHub context", () => {
    for (const job of [{ id: "manual-1" }, { repository_full_name: "o/r" }, { workflow_run_id: 42 }, { repository_full_name: "invalid", workflow_run_id: 42 }]) {
      expect(githubJobLink(job)).toBeNull()
    }
  })
})
