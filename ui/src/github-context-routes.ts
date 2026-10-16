import { readableQuery } from "@/url-query"
import { type DeploymentListStatus } from "@/deployment-status"

// Jobs and deployments share repository/source identity. Deployment IDs are path segments.
export type GitHubContext = {
  kind: "repository" | "pulls" | "branches" | "tags" | "runs"
  repository: string
  reference: string
  name?: string
}

function decode(value: string): string | null {
  try {
    const result = decodeURIComponent(value)
    return result && ![...result].some((character) => character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127) ? result : null
  } catch { return null }
}

const routeKinds = { pull_requests: "pulls", runs: "runs", pr: "pulls", branch: "branches", tag: "tags", run: "runs" } as const

function validRepository(owner: string | null, repository: string | null): boolean {
  return Boolean(owner && repository && !/[/\\]/.test(owner + repository) && ![owner, repository].some((part) => part === "." || part === ".."))
}

export function githubContextFromPath(path: string, search = ""): GitHubContext | null {
  const query = new URLSearchParams(search)
  if (["branch", "tag", "commit"].some((key) => query.getAll(key).length > 1)) return null
  if (query.has("branch") && query.has("tag")) return null
  for (const key of ["branch", "tag", "commit"]) {
    const value = query.get(key)
    if (value !== null && (!value || [...value].some((character) => character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127))) return null
  }
  const collection = path.match(/^\/github\/([^/]+)\/([^/]+)\/(jobs|deployments)(?:\/([a-zA-Z0-9_-]+))?$/)
  if (collection) {
    if (collection[3] === "jobs" && collection[4]) return null
    if (collection[3] === "deployments" && query.has("deployment")) return null
    const owner = decode(collection[1]), repository = decode(collection[2])
    if (!validRepository(owner, repository) || (collection[3] === "jobs" && query.has("tag"))) return null
    const name = query.get("branch") ?? query.get("tag")
    return { kind: query.has("branch") ? "branches" : query.has("tag") ? "tags" : "repository", repository: `${owner}/${repository}`, reference: query.get("commit") || "", ...(name ? { name } : {}) }
  }
  const current = path.match(/^\/github\/([^/]+)\/([^/]+)\/(pull_requests|runs|pr|branch|tag|run)\/([^/]+)\/(jobs|deployments)(?:\/([a-zA-Z0-9_-]+))?$/)
  const legacy = current ? null : path.match(/^\/github\/(pulls|branches|tags|runs)\/([^/]+)\/([^/]+)\/([^/]+)\/(jobs|deployments)$/)
  // Older Jobs paths placed the full branch before the SHA, sometimes unescaped.
  const oldBranch = current || legacy ? null : path.match(/^\/github\/branches\/([^/]+)\/([^/]+)\/(.+)\/([^/]+)\/jobs$/)
  if (oldBranch) {
    const owner = decode(oldBranch[1]), repository = decode(oldBranch[2]), name = decode(oldBranch[3]), reference = decode(oldBranch[4])
    return validRepository(owner, repository) && name && reference ? { kind: "branches", repository: `${owner}/${repository}`, name, reference } : null
  }
  if (!current && !legacy) return null
  const kind = current ? routeKinds[current[3] as keyof typeof routeKinds] : legacy![1] as GitHubContext["kind"]
  const view = (current ?? legacy)![5]
  if (view === "jobs" && current?.[6]) return null
  // Deploy is unpublished: retired source paths and query IDs need no compatibility.
  if (view === "deployments" && (!current || current[3] !== "pull_requests" || query.has("deployment"))) return null
  if ((kind === "runs" && view === "deployments") || (kind === "tags" && view === "jobs")) return null
  const owner = decode(current ? current[1] : legacy![2])
  const repository = decode(current ? current[2] : legacy![3])
  const value = decode((current ?? legacy)![4])
  if (!validRepository(owner, repository) || !value) return null
  if ((kind === "pulls" || kind === "runs") && (!/^[1-9]\d*$/.test(value) || query.has("branch") || query.has("tag"))) return null
  if (kind === "branches" || kind === "tags") {
    const name = current ? value : query.get(kind === "tags" ? "tag" : "branch") || undefined
    const reference = current ? query.get("commit") || "" : value
    return { kind, repository: `${owner}/${repository}`, reference, ...(name ? { name } : {}) }
  }
  return { kind, repository: `${owner}/${repository}`, reference: value }
}

export function githubContextPath(context: GitHubContext, view: "jobs" | "deployments", selection: Record<string, string | number | undefined> = {}): string {
  const repository = context.repository.split("/").map(encodeURIComponent).join("/")
  const query = new URLSearchParams()
  let prefix = `/github/${repository}`
  if (context.kind === "pulls" || context.kind === "runs") {
    prefix += `/${context.kind === "pulls" ? "pull_requests" : "runs"}/${encodeURIComponent(context.reference)}`
  } else {
    if (context.name) query.set(context.kind === "tags" ? "tag" : "branch", context.name)
    if (context.reference && !(view === "deployments" && selection.deployment !== undefined)) query.set("commit", context.reference)
  }
  for (const [key, value] of Object.entries(selection)) if (value !== undefined && !(view === "deployments" && key === "deployment")) query.set(key, String(value))
  const detail = view === "deployments" && selection.deployment !== undefined ? `/${encodeURIComponent(selection.deployment)}` : ""
  return `${prefix}/${view}${detail}${query.size ? `?${readableQuery(query)}` : ""}`
}

type DeploymentSource = {
  id: string
  repository_full_name: string
  source?: string
  ref?: string
  commit_sha: string
  revision_number?: number
  pull_request_number?: number
}
type RevisionSource = { ref?: string; commit_sha: string; number: number }

export function deploymentContext(deployment: DeploymentSource, revision?: RevisionSource | null): GitHubContext | null {
  if (deployment.pull_request_number) return { kind: "pulls", repository: deployment.repository_full_name, reference: String(deployment.pull_request_number) }
  const ref = revision?.ref ?? deployment.ref ?? ""
  const kind = ref.startsWith("refs/tags/") || deployment.source === "tag" ? "tags" : ref.startsWith("refs/heads/") || deployment.source === "branch" ? "branches" : null
  if (!kind || !deployment.commit_sha) return null
  return { kind, repository: deployment.repository_full_name, reference: "", name: ref.replace(/^refs\/(heads|tags)\//, "") }
}

export function deploymentPath(deployment: DeploymentSource, revision?: RevisionSource | null, explicitRevision?: number): string {
  const context = deploymentContext(deployment, revision)
  const number = explicitRevision ?? revision?.number
  if (!context) return `/deployments/${encodeURIComponent(deployment.id)}${number ? `?revision=${number}` : ""}`
  return githubContextPath(context, "deployments", { deployment: deployment.id, revision: number })
}

export function deploymentListPath(context: GitHubContext | null, status: DeploymentListStatus = "running"): string {
  const selection = status === "running" ? {} : { status }
  return context ? githubContextPath(context, "deployments", selection) : `/deployments${status === "running" ? "" : `?${new URLSearchParams({ status })}`}`
}

export function deploymentListAPIPath(context: GitHubContext | null, status: DeploymentListStatus = "running"): string {
  const query = new URLSearchParams()
  if (context) query.set("repository_full_name", context.repository)
  if (context?.kind === "pulls") query.set("pull_request_number", context.reference)
  else if (context) {
    if (context.reference) query.set("commit_sha", context.reference)
    if (context.name) query.set("ref", `refs/${context.kind === "tags" ? "tags" : "heads"}/${context.name}`)
  }
  query.set("status", status)
  return `/user/deployments${query.size ? `?${query}` : ""}`
}

export type DeploymentPrefill = { repository: string; source: "branch" | "tag" | "pull_request"; ref: string; commitSHA: string }

export function deploymentCreatePath(context: GitHubContext | null, commitSHA = ""): string {
  if (!context || context.kind === "runs") return "/deployments/new"
  const ref = context.kind === "pulls" ? context.reference : context.name
  if (!ref) return `/deployments/new?${readableQuery(new URLSearchParams({ repository: context.repository }))}`
  const query = new URLSearchParams({ repository: context.repository, source: context.kind === "pulls" ? "pull_request" : context.kind === "tags" ? "tag" : "branch", ref })
  const sha = commitSHA || (context.kind !== "pulls" ? context.reference : "")
  if (sha) query.set("commit_sha", sha)
  return `/deployments/new?${readableQuery(query)}`
}

export function deploymentPrefillFromSearch(search: string): DeploymentPrefill | null {
  const query = new URLSearchParams(search)
  const repository = query.get("repository") || ""
  const source = query.get("source") || "branch"
  const ref = query.get("ref") || ""
  const commitSHA = query.get("commit_sha") || ""
  if (!/^[^/\\?#%\s]+\/[^/\\?#%\s]+$/.test(repository) || !["branch", "tag", "pull_request"].includes(source || "") || (query.has("source") && !ref) || [...ref].some((character) => character.charCodeAt(0) < 32 || character.charCodeAt(0) === 127)) return null
  if (source === "pull_request" && !/^[1-9]\d*$/.test(ref)) return null
  if (commitSHA && !/^[a-f0-9]{40}$/.test(commitSHA)) return null
  return { repository, source: source as DeploymentPrefill["source"], ref, commitSHA }
}

export function jobDeploymentContext(job: { repository_full_name?: string; pull_request_number?: number; head_branch?: string; head_sha?: string }): GitHubContext | null {
  if (!job.repository_full_name || !/^[a-f0-9]{40}$/.test(job.head_sha || "")) return null
  if (job.pull_request_number && job.pull_request_number > 0) return { kind: "pulls", repository: job.repository_full_name, reference: String(job.pull_request_number) }
  if (job.head_branch) return { kind: "branches", repository: job.repository_full_name, name: job.head_branch, reference: job.head_sha! }
  return null
}

// Restrict the visible Jobs groups without treating URL context as authorization.
// RunnerState currently has no reliable Tag evidence; Tag filters are deployment-only.
export function githubContextMatchesJobGroup(context: GitHubContext | null, group: { repository: string; headBranch?: string; headSHA?: string; pullRequestNumber?: number; workflowRunIDs?: number[] }): boolean {
  if (!context) return true
  if (group.repository.toLowerCase() !== context.repository.toLowerCase()) return false
  if (context.kind === "pulls") return group.pullRequestNumber === Number(context.reference)
  if (context.kind === "runs") return Boolean(group.workflowRunIDs?.includes(Number(context.reference)))
  if (context.kind === "tags") return false
  if (context.kind === "branches" && (group.pullRequestNumber || (context.name && group.headBranch !== context.name))) return false
  return !context.reference || group.headSHA === context.reference
}
