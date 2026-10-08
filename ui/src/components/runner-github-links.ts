import type { RunnerState } from "@/admin-types"

export function workflowRunURL(job: RunnerState): string {
  if (!job.workflow_run_id) return ""
  const marker = `/actions/runs/${job.workflow_run_id}`
  const jobURL = job.github_job_url || ""
  const index = jobURL.indexOf(marker)
  if (index >= 0) return jobURL.slice(0, index + marker.length)
  const repository = job.repository_full_name?.split("/")
  if (repository?.length !== 2 || repository.some((part) => !part)) return ""
  return `https://github.com/${repository.map(encodeURIComponent).join("/")}${marker}`
}

export function githubJobLink(job: RunnerState): {
  url: string
  labelKey: "user.openGitHubJob" | "user.openGitHubRun"
} | null {
  if (job.github_job_url) return { url: job.github_job_url, labelKey: "user.openGitHubJob" }
  const url = workflowRunURL(job)
  return url ? { url, labelKey: "user.openGitHubRun" } : null
}
