import { describe, expect, test } from "bun:test"
import { githubContextMatchesJobGroup, deploymentCreatePath, deploymentPrefillFromSearch, jobDeploymentContext, deploymentContext, deploymentListAPIPath, deploymentListPath, deploymentPath, githubContextFromPath, githubContextPath } from "./github-context-routes"
import { deploymentListStatuses, deploymentListStatusFromSearch } from "./deployment-status"
import { appRouteAccess, deploymentIDFromPath, signInURL, userDataResources } from "./app-load-policy"

describe("GitHub source context routes", () => {
  test("Jobs and deployments round trip the same source with slash and Unicode names", () => {
    for (const kind of ["branches", "tags", "pulls"]) {
      const source = { kind, repository: "qiniu/deployment", reference: kind === "pulls" ? "125" : "a".repeat(40), ...(kind === "pulls" ? {} : { name: "feature/中文 #1" }) }
      for (const view of kind === "tags" ? ["deployments"] : ["jobs", "deployments"]) {
        const path = githubContextPath(source, view, view === "jobs" ? { job: "j1" } : { deployment: "d1", revision: 2 })
        const url = new URL(path, "https://example.com")
        expect(githubContextFromPath(url.pathname, url.search)).toEqual(view === "deployments" && kind !== "pulls" ? { ...source, reference: "" } : source)
        if (view === "deployments") {
          expect(appRouteAccess(url.pathname)).toBe("user")
          expect(deploymentIDFromPath(url.pathname)).toBe("d1")
          expect(userDataResources(url.pathname)).toEqual([])
          expect(decodeURIComponent(signInURL(url.pathname, url.search))).toContain(path)
        }
      }
    }
  })
  test("canonicalizes by saved revision evidence and retains legacy untyped refs", () => {
    const deployment = { id: "d1", repository_full_name: "qiniu/deployment", source: "branch", ref: "main", commit_sha: "b".repeat(40) }
    const revision = { number: 1, ref: "refs/heads/example/vite", commit_sha: "a".repeat(40) }
    expect(deploymentPath(deployment, revision)).toBe("/github/qiniu/deployment/deployments/d1?branch=example/vite&revision=1")
    expect(deploymentPath({ ...deployment, source: undefined, ref: "legacy" })).toBe("/deployments/d1")
    expect(deploymentListAPIPath(deploymentContext(deployment, revision))).toContain("ref=refs%2Fheads%2Fexample%2Fvite&status=running")
    expect(deploymentPath({ ...deployment, pull_request_number: 125 }, revision)).toBe("/github/qiniu/deployment/pull_requests/125/deployments/d1?revision=1")
  })
  test("rejects malformed source paths and keeps creation separate", () => {
    for (const path of ["/github/pulls/o/r/0/deployments", "/github/pulls/o/r/no/deployments", "/github/branches/o%2Fr/r/a/deployments", "/github/tags/o/r/%zz/deployments", "/github/pulls/o/r/1/deployments/more"]) expect(githubContextFromPath(path)).toBeNull()
    expect(deploymentIDFromPath("/deployments/new")).toBeUndefined()
    expect(deploymentIDFromPath("/deployments/old-id")).toBe("old-id")
  })
})

test("repository-first routes keep source history independent from immutable commit selection", () => {
  const context = githubContextFromPath("/github/qiniu/deployment/deployments", "?branch=example/vite")
  expect(context).toEqual({ kind: "branches", repository: "qiniu/deployment", name: "example/vite", reference: "" })
  expect(deploymentListAPIPath(context)).toBe("/user/deployments?repository_full_name=qiniu%2Fdeployment&ref=refs%2Fheads%2Fexample%2Fvite&status=running")
  expect(githubContextFromPath("/github/pulls/qiniu/deployment/7/jobs")).toEqual(githubContextFromPath("/github/qiniu/deployment/pr/7/jobs"))
  expect(githubContextFromPath("/github/qiniu/deployment/pr/0/jobs")).toBeNull()
  expect(githubContextFromPath("/github/qiniu/deployment/branch/%zz/jobs")).toBeNull()
  expect(githubContextFromPath("/github/qiniu/deployment/run/1/deployments")).toBeNull()
})

test("deployment status filters preserve source context and readable list URLs", () => {
  for (const source of [null, { kind: "repository", repository: "qiniu/deployment", reference: "" }, { kind: "branches", repository: "qiniu/deployment", name: "feature/中文 #1", reference: "a".repeat(40) }, { kind: "tags", repository: "qiniu/deployment", name: "release/v1", reference: "" }, { kind: "pulls", repository: "qiniu/deployment", reference: "7" }]) {
    for (const status of deploymentListStatuses) {
      const url = new URL(deploymentListPath(source, status), "https://example.com")
      expect(deploymentListStatusFromSearch(url.search)).toEqual(status)
      expect(githubContextFromPath(url.pathname, url.search)).toEqual(source)
      const api = new URL(deploymentListAPIPath(source, status), url)
      expect(api.searchParams.get("status")).toBe(status)
      expect(api.searchParams.get("repository_full_name")).toBe(source?.repository ?? null)
    }
  }
  for (const search of ["?status=unknown", "?status=running&status=failed", "?status=processing,running", "?status=", ""]) expect(deploymentListStatusFromSearch(search)).toBe("running")
})

test("Job creation links pin the selected Job, including older PR commits", () => {
  const job = { repository_full_name: "qiniu/deployment", pull_request_number: 7, head_sha: "a".repeat(40), head_branch: "feature/中文" }
  const context = jobDeploymentContext(job)
  expect(githubContextPath(context, "deployments")).toBe("/github/qiniu/deployment/pull_requests/7/deployments")
  const url = new URL(deploymentCreatePath(context, job.head_sha), "https://example.com")
  expect(deploymentPrefillFromSearch(url.search)).toEqual({ repository: job.repository_full_name, source: "pull_request", ref: "7", commitSHA: job.head_sha })
  expect(deploymentPrefillFromSearch("?repository=o/r&source=pull_request&ref=7&commit_sha=not-a-sha")).toBeNull()
  expect(jobDeploymentContext({ ...job, head_sha: "" })).toBeNull()
  const branch = jobDeploymentContext({ ...job, pull_request_number: 0 })
  const branchURL = new URL(deploymentCreatePath(branch), "https://example.com")
  expect(deploymentPrefillFromSearch(branchURL.search).ref).toBe("feature/中文")
  expect(deploymentPrefillFromSearch(branchURL.search).commitSHA).toBe(job.head_sha)
})

test("deployment details omit commit while explicit list filters retain it", () => {
  const source = { kind: "branches", repository: "qiniu/deployment", name: "example/vite", reference: "a".repeat(40) }
  const list = new URL(githubContextPath(source, "deployments"), "https://example.com")
  expect(list.searchParams.get("commit")).toBe(source.reference)
  expect(deploymentListAPIPath(githubContextFromPath(list.pathname, list.search))).toContain(`commit_sha=${source.reference}`)
  const detail = new URL(githubContextPath(source, "deployments", { deployment: "d1", revision: 2 }), "https://example.com")
  expect(detail.pathname).toBe("/github/qiniu/deployment/deployments/d1")
  expect(detail.searchParams.has("deployment")).toBe(false)
  expect(detail.searchParams.has("commit")).toBe(false)
  expect(detail.searchParams.get("revision")).toBe("2")
})


test("repository collections preserve readable branch/tag filters and repository-only history", () => {
  for (const view of ["jobs", "deployments"]) {
    const context = githubContextFromPath(`/github/qiniu/deployment/${view}`)
    expect(context).toEqual({ kind: "repository", repository: "qiniu/deployment", reference: "" })
    expect(githubContextPath(context, view)).toBe(`/github/qiniu/deployment/${view}`)
    expect(appRouteAccess(`/github/qiniu/deployment/${view}`)).toBe("user")
    expect(deploymentListAPIPath(context)).toBe("/user/deployments?repository_full_name=qiniu%2Fdeployment&status=running")
    const prefill = new URL(deploymentCreatePath(context), "https://example.com")
    expect(deploymentPrefillFromSearch(prefill.search)).toEqual({ repository: "qiniu/deployment", source: "branch", ref: "", commitSHA: "" })
  }
  const branch = { kind: "branches", repository: "qiniu/deployment", name: "feature/jobs/deployments", reference: "a".repeat(40) }
  expect(githubContextPath(branch, "jobs", { job: "j1" })).toBe(`/github/qiniu/deployment/jobs?branch=feature/jobs/deployments&commit=${branch.reference}&job=j1`)
  for (const name of ["example/vite", "release/v1.0", "中文/a&tag=b#c+d=e", "literal%2Fname"]) {
    for (const kind of ["branches", "tags"]) {
      const source = { ...branch, kind, name }
      const url = new URL(githubContextPath(source, "deployments"), "https://example.com")
      expect(url.pathname).toBe("/github/qiniu/deployment/deployments")
      expect(githubContextFromPath(url.pathname, url.search)).toEqual(source)
    }
  }
})

test("all previous named branch and PR links resolve to the new canonical location", () => {
  const sha = "a".repeat(40)
  for (const path of [
    `/github/qiniu/deployment/branch/feature%2Fpreview/jobs?commit=${sha}&job=j1`,
    `/github/branches/qiniu/deployment/${sha}/jobs?branch=feature%2Fpreview&job=j1`,
    `/github/branches/qiniu/deployment/feature/preview/${sha}/jobs?job=j1`,
  ]) {
    const url = new URL(path, "https://example.com")
    expect(githubContextPath(githubContextFromPath(url.pathname, url.search), "jobs", { job: url.searchParams.get("job") })).toBe(`/github/qiniu/deployment/jobs?branch=feature/preview&commit=${sha}&job=j1`)
  }
  for (const path of ["/github/pulls/qiniu/deployment/7/jobs", "/github/qiniu/deployment/pr/7/jobs", "/github/qiniu/deployment/pull_requests/7/jobs"]) {
    expect(githubContextPath(githubContextFromPath(path), "jobs")).toBe("/github/qiniu/deployment/pull_requests/7/jobs")
  }
  expect(githubContextPath(githubContextFromPath("/github/qiniu/deployment/run/42/jobs"), "jobs")).toBe("/github/qiniu/deployment/runs/42/jobs")
})

test("ambiguous and malformed collection filters cannot fall back to account-wide data", () => {
  for (const search of ["?branch=a&tag=b", "?branch=a&branch=b", "?branch=", "?tag=", "?commit=a&commit=b", "?branch=%00bad"]) {
    expect(githubContextFromPath("/github/o/r/deployments", search)).toBeNull()
    expect(appRouteAccess("/github/o/r/deployments", search)).toBe("not-found")
  }
  expect(appRouteAccess("/github/o/r/jobs", "?tag=release/v1")).toBe("not-found")
  expect(githubContextFromPath("/github/o/r/pull_requests/0/jobs")).toBeNull()
})

test("Jobs collection filters never select another repository or silently widen an empty branch", () => {
  const groups = [
    { repository: "qiniu/deployment", headBranch: "example/vite", headSHA: "old", workflowRunIDs: [1] },
    { repository: "qiniu/deployment", headBranch: "example/vite", headSHA: "new", workflowRunIDs: [2] },
    { repository: "qiniu/deployment", headBranch: "main", headSHA: "main", workflowRunIDs: [3] },
    { repository: "other/deployment", headBranch: "example/vite", headSHA: "new", workflowRunIDs: [4] },
    { repository: "qiniu/deployment", headBranch: "example/vite", headSHA: "pr", pullRequestNumber: 7, workflowRunIDs: [5] },
  ]
  const filtered = (path, query = "") => groups.filter((g) => githubContextMatchesJobGroup(githubContextFromPath(path, query), g)).map((g) => g.headSHA)
  expect(filtered("/github/qiniu/deployment/jobs")).toEqual(["old", "new", "main", "pr"])
  expect(filtered("/github/qiniu/deployment/jobs", "?branch=example/vite")).toEqual(["old", "new"])
  expect(filtered("/github/qiniu/deployment/jobs", "?branch=missing")).toEqual([])
  expect(filtered("/github/qiniu/deployment/jobs", "?branch=example/vite&commit=old")).toEqual(["old"])
  expect(filtered("/github/branches/qiniu/deployment/old/jobs")).toEqual(["old"])
  expect(filtered("/github/qiniu/deployment/pull_requests/7/jobs")).toEqual(["pr"])
  expect(filtered("/github/qiniu/deployment/runs/3/jobs")).toEqual(["main"])
})

 test("deployment IDs use paths and retired deployment links are rejected", () => {
  for (const path of [
    "/github/o/r/deployments/d1?branch=feature/preview&revision=2",
    "/github/o/r/deployments/d1?tag=release/v1",
    "/github/o/r/pull_requests/7/deployments/d1?revision=2",
  ]) {
    const url = new URL(path, "https://example.com")
    expect(appRouteAccess(url.pathname, url.search)).toBe("user")
    expect(deploymentIDFromPath(url.pathname)).toBe("d1")
    expect(userDataResources(url.pathname)).toEqual([])
    expect(decodeURIComponent(signInURL(url.pathname, url.search))).toContain(path)
  }
  for (const path of [
    "/github/o/r/deployments?deployment=d1",
    "/github/o/r/pull_requests/7/deployments?deployment=d1",
    "/github/o/r/branch/feature%2Fpreview/deployments",
    "/github/o/r/tag/release/deployments",
    "/github/o/r/pr/7/deployments",
    "/github/pulls/o/r/7/deployments",
    "/github/o/r/deployments/d1/extra",
    "/github/o/r/deployments/%2F",
    "/github/o/r/jobs/d1",
    "/github/o/r/pull_requests/7/jobs/d1",
    "/github/o/r/deployments/d1?branch=a&tag=b",
  ]) {
    const url = new URL(path, "https://example.com")
    expect(appRouteAccess(url.pathname, url.search)).toBe("not-found")
  }
})
