# GitHub Actions Runner Auto-update Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Enable GitHub's official Runner self-update for managed and user-supplied templates while retaining the managed template baseline and exposing preinstalled versus effective Runner versions.

**Architecture:** Remove the registration-time update opt-out unconditionally. Capture and freeze the post-update version from a bounded internal control marker emitted by the fixed hook before `RUNNERD_JOB_STARTED`, carry it through the typed process-exit result, and persist it only for the matching Sandbox/PID attempt using the existing additive Runner Request schema path.

**Tech Stack:** Go, embedded Bash, Qiniu Sandbox Go SDK, GORM with SQLite/PostgreSQL/MySQL, React/TypeScript, Bun tests, i18next.

**Spec:** `docs/superpowers/specs/2026-09-21-runner-auto-update.md`

## Global Constraints

- Do not add a custom Runner downloader or a per-Spec update toggle.
- Keep the exact managed-template Runner archive pin, checksum, size, source gate, and release smoke checks.
- Preserve `runner_version` as the compatible preinstalled-template value; add `effective_runner_version` rather than changing its JSON meaning.
- Effective-version collection is best effort, limited to 256 bytes, and never blocks Runner completion or cleanup.
- Accept only the first valid marker before `RUNNERD_JOB_STARTED`; never parse later Workflow output into effective-version state, and bind persistence to the matching Sandbox ID and PID.
- Existing SQLite `runner_requests` tables migrate additively and historical empty fields remain valid.
- Fresh retry and mismatched-job requeue clear the complete runtime snapshot.
- Do not commit or push without a separate explicit user request.

## Review Focus

- A user template whose preinstalled Runner is old must update through GitHub without runnerd-specific template changes; the rendered config command must omit `--disableupdate` for every input.
- A job that never starts must leave `effective_runner_version` empty without turning an otherwise successful lifecycle into a failure.
- A Workflow marker emitted after `RUNNERD_JOB_STARTED`, or any oversized, multiline, or malformed marker, must not persist arbitrary content.
- An exit callback from an old Sandbox/PID must not update the current attempt; lifecycle tests cover stale identity.
- A legacy SQLite row must survive migration and gain the empty nullable-compatible column; migration tests cover row preservation and column creation.

---

### Task 1: Enable the official updater and produce bounded effective-version evidence

**Files:**
- Modify: `internal/sandboxrunner/scripts/start-github-runner.sh`
- Modify: `internal/sandboxrunner/runner.go`
- Test: `internal/sandboxrunner/runner_test.go`

**Interfaces:**
- Consumes: the existing `startScript(StartInput, string) string` renderer and official `config.sh`/`run.sh` contract.
- Produces: `ExitResult.EffectiveRunnerVersion string`, plus an internal bounded pre-job control-marker capture used by the start watcher.

- [x] **Step 1: Write failing startup-script tests**

Add assertions that the rendered script does not contain `--disableupdate`, invokes the writable `Runner.Listener --version` from the job-start hook, validates a one-line version value no longer than 256 bytes, and delivers only the accepted value before `RUNNERD_JOB_STARTED`. The execution fixture must route hook stdout to a separate Job log, matching the official Runner, while asserting that the outer startup command still receives the evidence through its one-shot channel.

- [x] **Step 2: Run the focused tests and confirm the new assertions fail**

Run: `go test ./internal/sandboxrunner -run 'TestStartScript' -count=1 -v`

Expected: FAIL because the current script still contains `--disableupdate` and has no effective-version evidence capture.

- [x] **Step 3: Write failing parser tests**

Add table tests for `parseEffectiveRunnerVersion([]byte) string` with `2.337.0`, surrounding whitespace, 257 bytes, embedded newline, arbitrary text, and a prerelease suffix. Valid one-line Runner versions pass; every invalid or oversized value returns empty.

- [x] **Step 4: Run the parser test and confirm it fails to compile**

Run: `go test ./internal/sandboxrunner -run 'TestParseEffectiveRunnerVersion' -count=1 -v`

Expected: FAIL because `parseEffectiveRunnerVersion` does not exist.

- [x] **Step 5: Implement the minimal script and typed exit evidence**

Remove `--disableupdate`. In the fixed job-start hook, read `"$workdir/bin/Runner.Listener" --version`, validate it, and send a bounded internal marker plus `RUNNERD_JOB_STARTED` through a permission-restricted one-shot FIFO to the outer startup process; do not rely on hook stdout because the official Runner routes it to the Job log. Add `EffectiveRunnerVersion` to `ExitResult`; the start watcher accepts only the first valid pre-job marker forwarded by the outer process, freezes it in memory, ignores later Workflow markers, and includes the frozen value even when the provider returns no command result. Recovery leaves uncaptured evidence empty rather than trusting post-start output.

- [x] **Step 6: Run the Sandbox Runner tests**

Run: `go test ./internal/sandboxrunner -run 'Test(StartScript|ParseEffectiveRunnerVersion|RunnerBootstrap)' -count=1 -v`

Expected: PASS.

### Task 2: Persist effective version with attempt-safe lifecycle semantics

**Files:**
- Modify: `internal/state/store.go`
- Modify: `internal/state/records.go`
- Modify: `internal/state/conversions.go`
- Modify: `internal/state/runner_requests.go`
- Modify: `internal/server/server_runner_lifecycle.go`
- Modify: `internal/server/server_loops.go`
- Test: `internal/state/store_test.go`
- Test: `internal/server/server_helpers_test.go`
- Test: `internal/server/server_test.go`

**Interfaces:**
- Consumes: `sandboxrunner.ExitResult.EffectiveRunnerVersion` from Task 1 and the existing `runnerAttemptIdentity` matcher.
- Produces: `state.RunnerState.EffectiveRunnerVersion string` serialized as `effective_runner_version`, persisted in `runner_requests.effective_runner_version`.

- [x] **Step 1: Write failing state tests**

Extend the Runner state round-trip test with `EffectiveRunnerVersion: "2.338.0"`; extend retry and mismatched-job requeue tests to require it to be cleared; extend the additive SQLite migration column list with `effective_runner_version` and verify the legacy row remains unchanged.

- [x] **Step 2: Run focused state tests and confirm failure**

Run: `go test ./internal/state -run 'Test(Write|Retry|MigrateSQLiteRunnerRequest)' -count=1 -v`

Expected: FAIL to compile or fail assertions because the field and column are absent.

- [x] **Step 3: Implement model, conversion, write, and reset paths**

Add the field to the public state and GORM record, copy it in `recordToState`, include it in `WriteState`, and clear it beside `TemplateVersion` and `RunnerVersion` in every fresh retry/requeue path. Rely on `migrateSQLiteRunnerRequestSchema` for additive SQLite creation.

- [x] **Step 4: Write failing lifecycle tests**

Add tests showing that a matching exit result fills an empty effective version, does not replace a previously captured value, and that a stale Sandbox/PID exit result cannot change the current request.

- [x] **Step 5: Run focused lifecycle tests and confirm failure**

Run: `go test ./internal/server -run 'TestRunnerExited.*EffectiveRunnerVersion' -count=1 -v`

Expected: FAIL because exit handling does not persist the typed evidence.

- [x] **Step 6: Implement attempt-safe persistence**

After loading and matching the attempt in `runnerExitedForAttempt`, fill `EffectiveRunnerVersion` only when state is empty and result evidence is non-empty. Apply the same fill-only behavior in late exit evidence without weakening existing exit-code rules, then persist through the existing lifecycle writes.

- [x] **Step 7: Run state and server regression tests**

Run: `go test ./internal/state -count=1`

Run: `go test ./internal/server -run 'Test(RunnerExited|Retry|Mismatched|RunnerRequest)' -count=1`

Expected: PASS.

### Task 3: Expose both Runner version meanings in the Admin UI

**Files:**
- Modify: `ui/src/admin-types.ts`
- Modify: `ui/src/components/runner-request-details.tsx`
- Modify: `ui/src/locales/en.ts`
- Modify: `ui/src/locales/zh.ts`
- Test: `ui/src/components/admin-diagnostics-section.test.js`

**Interfaces:**
- Consumes: `runner_version` and `effective_runner_version` from Task 2.
- Produces: separate localized detail rows for preinstalled and effective Runner versions.

- [x] **Step 1: Write failing UI assertions**

Extend the Runner Request fixture with `effective_runner_version: "2.338.0"` and assert the detail view contains both the preinstalled `2.337.0` value and effective `2.338.0` value with distinct labels.

- [x] **Step 2: Run the focused UI test and confirm failure**

Run: `cd ui && bun test src/components/admin-diagnostics-section.test.js`

Expected: FAIL because the effective field and labels are not rendered.

- [x] **Step 3: Implement the UI and paired copy**

Add the optional TypeScript field. Rename the existing display label to `Preinstalled Runner version` / `预装 Runner 版本`, add `Effective Runner version` / `实际运行 Runner 版本`, and render `-` when either is unavailable.

- [x] **Step 4: Verify UI tests and i18n parity**

Run: `cd ui && bun test src/components/admin-diagnostics-section.test.js`

Run: `task ui-i18n-check`

Expected: PASS.

### Task 4: Synchronize operator, template, and testing documentation

**Files:**
- Modify: `README.md`
- Modify: `README.zh.md`
- Modify: `docs/default-runner-templates.md`
- Modify: `docs/zh/default-runner-templates.md`
- Modify: `docs/testing.md`
- Modify: `docs/zh/testing.md`
- Modify: `docs/deployment-smoke.md`
- Modify: `docs/zh/deployment-smoke.md`
- Modify: `ui/src/content/site-docs/en/custom-templates.md`
- Modify: `ui/src/content/site-docs/zh/custom-templates.md`
- Modify: `AGENTS.md`
- Modify: `.agents/rules/project-architecture.md`

**Interfaces:**
- Consumes: the finalized runtime and API semantics from Tasks 1-3.
- Produces: aligned English/Chinese documentation and durable repository guidance.

- [x] **Step 1: Update managed and custom template guidance**

State that the pinned archive remains the tested preinstalled baseline, while registration leaves GitHub's official updater enabled for both managed and custom templates. Keep the custom-template requirement for a compatible `/opt/actions-runner` bootstrap installation.

- [x] **Step 2: Update diagnostics and migration documentation**

Describe the pre-launch `runner_version` snapshot, the post-update best-effort `effective_runner_version`, the 256-byte pre-job marker bound, matching-attempt persistence, empty historical/restarted values, and reset behavior.

- [x] **Step 3: Update durable agent rules**

Apply the same compatibility and trust-boundary language to `AGENTS.md` and `.agents/rules/project-architecture.md`; explicitly prohibit deriving effective version from workflow stdout.

- [x] **Step 4: Run documentation contract checks**

Run: `task ui-i18n-check`

Run: `rg -n --glob '!internal/server/ui/**' -- '--disableupdate' internal templates docs README.md README.zh.md AGENTS.md .agents ui/src`

Expected: i18n PASS; no production invocation of `--disableupdate`, with any remaining occurrence limited to historical/design explanation or a negative test.

### Task 5: Full verification and implementation record

**Files:**
- Modify: `docs/superpowers/plans/2026-09-21-runner-auto-update.md`

**Interfaces:**
- Consumes: all implementation tasks.
- Produces: checked plan items and recorded verification evidence; no commit or push.

- [x] **Step 1: Format changed Go files**

Run: `gofmt -w <changed-go-files>`

- [x] **Step 2: Run focused verification**

Run: `go test ./internal/sandboxrunner -count=1`

Run: `go test ./internal/state -count=1`

Run: `go test ./internal/server -count=1`

Run: `cd ui && bun test`

Expected: PASS.

- [x] **Step 3: Run repository gates**

Run: `task lint`

Run: `task test`

Expected: PASS. If an unavailable external database or credentialed provider gate is skipped by its existing test guard, record that explicitly rather than claiming it ran.

- [x] **Step 4: Inspect final scope**

Run: `git status --short && git diff --check && git diff --stat && git diff -- docs/superpowers/specs/2026-09-21-runner-auto-update.md docs/superpowers/plans/2026-09-21-runner-auto-update.md internal/sandboxrunner internal/state internal/server ui/src README.md README.zh.md docs AGENTS.md .agents/rules/project-architecture.md`

Expected: only files listed by this plan are changed; generated `internal/server/ui/` assets remain untouched unless a repository verification command intentionally rebuilds them, in which case they are not staged or committed without explicit authorization.

- [x] **Step 5: Record results in this plan**

Check completed items and append a short `Verification Results` section with exact commands and outcomes. Leave the branch uncommitted for user review unless the user separately asks to commit or push.

## Verification Results

- `go test ./internal/sandboxrunner -run TestStartScriptUsesHostedRunnerFilesystemContract -count=1 -v`: review follow-up first failed because official Runner hook stdout was simulated as a separate Job log, then passed after the generated hook delivered `2.338.0` and the job-start boundary through the one-shot FIFO to the outer command stdout.
- `go test ./internal/sandboxrunner -run 'Test(EffectiveRunnerVersionCapture|StartScriptUsesHostedRunnerFilesystemContract|ParseEffectiveRunnerVersion)' -count=1 -v`: passed; coverage includes split control markers, post-start Workflow-marker rejection, official Job-log routing, and generated-hook capture of `2.338.0` with a custom `RUNNER_HOOK_ROOT`.
- `go test ./internal/sandboxrunner ./internal/server ./internal/state -count=1`: passed; the production SQLite snapshot test and credentialed PostgreSQL/MySQL tests skipped behind their documented environment guards.
- `cd ui && bun test`: 233 passed, 0 failed.
- `task ui-i18n-check`: 9 passed, 0 failed, TypeScript build passed.
- `GOTOOLCHAIN=go1.26.3 task lint`: passed after the review follow-up, covering staticcheck, gofmt, gofumpt, goimports, and go vet. ESLint reported one pre-existing warning in `ui/src/components/sandbox-catalog-sections.tsx:150` and no errors.
- `task test`: exited 0 after the review follow-up, rebuilding the production UI, passing all 233 UI tests, and passing the Go race/coverage suite. Environment-gated production SQLite and credentialed PostgreSQL/MySQL tests remained skipped.
- `rg -n --glob '!internal/server/ui/**' -- '--disableupdate' ...`: no production invocation remains; matches are limited to documentation and the negative regression assertion.
- Real Qiniu Sandbox template build/smoke was not run because this change does not alter a template image and that gate requires provider credentials. A deployed workflow run remains the required end-to-end proof of GitHub-directed self-update.
- The verified implementation is committed and pushed only under the user's separate explicit authorization.
