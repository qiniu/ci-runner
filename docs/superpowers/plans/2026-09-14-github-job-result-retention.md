# GitHub Job Result Retention Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retain the latest authoritative GitHub Workflow Job result on each managed Runner request so the Admin detail page remains useful after GitHub lookup failures or result expiry, without conflating GitHub conclusions with runnerd lifecycle status.

**Architecture:** Add an additive, nullable terminal Workflow Job snapshot to `runner_requests`, captured only when the observed Job ID is the request's original `workflow_job_id`. Webhook and pre-start completion paths update the snapshot through one helper that rejects nonterminal observations, so active requests keep their existing live lookup behavior and an out-of-order delivery cannot erase a completed result. Diagnostics prefer the retained snapshot and use the existing cached GitHub lookup only for historical rows without one; the UI labels the source and observation time explicitly.

**Tech Stack:** Go, GORM, SQLite/PostgreSQL/MySQL-compatible schema tags, React, TypeScript, i18next, Bun tests.

**Delivery boundary:** Environment snapshots, typed exit payloads, network probes, automatic Job retry, and production deployment are excluded. Repository rules defer commits and pushes until the user explicitly requests them.

---

### Task 1: Persist an additive GitHub Job result snapshot

**Files:**
- Modify: `internal/state/store.go`
- Modify: `internal/state/records.go`
- Modify: `internal/state/conversions.go`
- Modify: `internal/state/runner_requests.go`
- Test: `internal/state/store_test.go`

- [x] **Step 1: Write a failing state round-trip test**

Add a test that creates a request, writes these `RunnerState` values, reopens the database, and verifies both `ReadState` and list projections retain them:

```go
st.GitHubJobName = "test"
st.GitHubJobStatus = "completed"
st.GitHubJobConclusion = "cancelled"
st.GitHubJobRunnerName = "e2b-1001"
st.GitHubJobObservedAt = observedAt
```

Extend the existing SQLite legacy-request migration fixture to assert the five new columns exist and its existing row, timestamps, GitHub installation metadata, and Sandbox snapshot fields are unchanged.

- [x] **Step 2: Run the focused test and verify RED**

Run:

```bash
go test ./internal/state -run 'TestRunnerStatePersistsGitHubJobResult|TestMigrateSQLiteRunnerRequest' -count=1
```

Expected: compilation fails because the five `RunnerState` fields do not exist.

- [x] **Step 3: Add the model-driven fields and mappings**

Add nullable/default-empty fields to `RunnerState` and `runnerRequestRecord` using these JSON/column names:

```text
github_job_name
github_job_status
github_job_conclusion
github_job_runner_name
github_job_observed_at
```

Map them in `recordToState`, `WriteState`, and `runnerRequestListSelectColumns`. Do not add a bespoke migration: the existing additive SQLite migration and GORM `AutoMigrate` are the compatibility path.

- [x] **Step 4: Run the focused state tests and verify GREEN**

Run:

```bash
go test ./internal/state -run 'TestRunnerStatePersistsGitHubJobResult|TestMigrateSQLiteRunnerRequest|TestRunnerRequestListProjection' -count=1
```

Expected: PASS.

### Task 2: Capture only the original Job result and preserve terminal snapshots

**Files:**
- Modify: `internal/server/server_runner_lifecycle.go`
- Test: `internal/server/server_test.go`

- [x] **Step 1: Write failing lifecycle tests**

Cover these behaviors with existing webhook helpers and real state reads:

```text
completed webhook after runnerd already completed -> result remains persisted
completed original Job while a different Job is assigned -> original result is persisted without replacing assignment or stopping the Sandbox
in_progress observations are not retained and cannot downgrade a completed result
pre-start GetWorkflowJob returning completed -> result is persisted without a Sandbox
```

- [x] **Step 2: Run the focused tests and verify RED**

Run:

```bash
go test ./internal/server -run 'Test.*(Retains|Persists).*GitHubJobResult' -count=1
```

Expected: FAIL because webhook and pre-start paths do not populate retained result fields.

- [x] **Step 3: Implement one monotonic snapshot helper**

Add a helper with this contract:

```go
func retainWorkflowJobResult(st *state.RunnerState, job github.WorkflowJob, observedAt time.Time) bool
```

It must trim strings, reject zero/mismatched Job IDs and all nonterminal observations, preserve an existing completed/non-empty-conclusion snapshot from later nonterminal deliveries, update `GitHubJobObservedAt` only when accepted, and return whether fields changed. Call it from `completeWithoutSandbox` and `stopRunner` before their state write/early-return decisions. Do not copy the raw webhook payload.

- [x] **Step 4: Run the lifecycle tests and verify GREEN**

Run:

```bash
go test ./internal/server -run 'Test.*(Retains|Persists).*GitHubJobResult|TestWebhookCompletedStopsActualRunnerAndRecordsJob|TestOriginalWorkflowJobCompletionKeepsRunnerAssignedToDifferentJob' -count=1
```

Expected: PASS with existing assignment and cleanup behavior unchanged.

### Task 3: Prefer retained diagnostics and display source/freshness

**Files:**
- Modify: `internal/server/server.go`
- Modify: `internal/server/server_diagnostics.go`
- Test: `internal/server/server_helpers_test.go`
- Modify: `ui/src/admin-types.ts`
- Modify: `ui/src/components/admin-sections.tsx`
- Modify: `ui/src/locales/en.ts`
- Modify: `ui/src/locales/zh.ts`
- Test: `ui/src/components/admin-diagnostics-section.test.js`

- [x] **Step 1: Write failing backend diagnostics tests**

Add tests proving that a request with a retained snapshot returns:

```json
{
  "lookup_status": "retained",
  "source": "retained",
  "status": "completed",
  "conclusion": "cancelled",
  "observed_at": "<non-zero RFC3339 timestamp>"
}
```

The test GitHub endpoint must fail if called. Preserve another test proving a historical row without a snapshot still uses the cached/coalesced live lookup and returns `lookup_status: "ok"`, `source: "live"`. Use `retained`, not `webhook`, because runtime status checks and recovery can also capture the snapshot.

- [x] **Step 2: Run the backend diagnostic tests and verify RED**

Run:

```bash
go test ./internal/server -run 'TestDiagnosticsRunnerRequest.*(Retained|Historical)' -count=1
```

Expected: FAIL because diagnostics always perform a live lookup and do not expose source/freshness.

- [x] **Step 3: Implement retained-first diagnostics**

Extend `diagnosticGitHubJob` with `source` and `observed_at`. When `RunnerState.GitHubJobObservedAt` is non-zero, build the diagnostic result from persisted fields and skip GitHub. Only rows without a snapshot use `diagnosticWorkflowJob`; mark those successful results as `source: "live"` and preserve the provider fetch time across cache hits instead of replacing it with each response time. Treat both `ok` and `retained` as available evidence in `diagnoseRunnerRequest`.

- [x] **Step 4: Write the failing UI test**

Extend the static diagnosis fixture to use a retained result and assert the header renders the existing conclusion badge plus localized retained-snapshot text and its observation time. Do not call the source `webhook`, because recovery and reconciler paths can also capture it.

- [x] **Step 5: Run the UI test and verify RED**

Run:

```bash
cd ui && bun test src/components/admin-diagnostics-section.test.js
```

Expected: FAIL because `retained`, `source`, and `observed_at` are not rendered.

- [x] **Step 6: Implement typed UI source/freshness output**

Add `retained` to the diagnostic lookup-status type, add `source` and `observed_at`, show the result badge for both `ok` and `retained`, and render concise localized metadata beside it. Do not imply that a retained result is a current live GitHub query.

- [x] **Step 7: Run backend, UI, and i18n checks and verify GREEN**

Run:

```bash
go test ./internal/server -run 'TestDiagnosticsRunnerRequest' -count=1
cd ui && bun test src/components/admin-diagnostics-section.test.js
task ui-i18n-check
```

Expected: PASS.

### Task 4: Update the durable contract and roadmap

**Files:**
- Modify: `TODO.md`
- Modify: `docs/current-work-handoff.md`
- Modify: `docs/runner-architecture-comparison.md`
- Modify: `docs/zh/runner-architecture-comparison.md`
- Modify: `docs/testing.md`
- Modify: `docs/zh/testing.md`
- Modify: `docs/README.md`
- Modify: `docs/zh/README.md`
- Modify: `README.md`
- Modify: `README.zh.md`
- Modify: `AGENTS.md`
- Modify: `.agents/rules/project-architecture.md`
- Modify: `.agents/rules/testing-and-verification.md`

- [x] **Step 1: Update documentation**

Document the additive retained fields, local observation timestamp, original-Job guard, monotonic terminal behavior, retained-first diagnostics, and historical live-lookup fallback. Mark Job result retention complete in the handoff/TODO while leaving environment snapshots, typed exit payloads, and network probes active.

- [x] **Step 2: Verify documentation symmetry and formatting**

Run:

```bash
git diff --check
rg -n 'retained|留存|historical|历史' docs/runner-architecture-comparison.md docs/zh/runner-architecture-comparison.md docs/testing.md docs/zh/testing.md TODO.md docs/current-work-handoff.md
```

Expected: no whitespace errors and matching English/Chinese contracts.

### Task 5: Full verification and review handoff

**Files:**
- Verify all changed files; do not edit generated `internal/server/ui/` manually.

- [x] **Step 1: Run schema-first verification**

```bash
go test ./internal/state -count=1
```

Expected: PASS, including additive old-SQLite upgrade tests.

- [x] **Step 2: Run project verification**

```bash
go test ./...
cd ui && bun run test
task ui-i18n-check
task build
GOTOOLCHAIN=go1.26.3 task lint
```

Expected: PASS. `task build` regenerates production UI assets through the supported build path. Pin lint to the repository's `go.mod` toolchain when the host default Go version is newer than staticcheck supports.

- [x] **Step 3: Review the final diff**

```bash
git status --short --branch
git diff --check
git diff --stat
git diff
```

Expected: only the scoped state, lifecycle, diagnostics, UI, test, generated production UI, roadmap, handoff, and paired documentation changes are present. PostgreSQL/MySQL opt-in schema tests and production deployment remain explicitly unverified unless dedicated environments are supplied.
