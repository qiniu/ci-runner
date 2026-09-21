# GitHub Actions Runner Auto-update Design

## Context

runnerd currently passes `--disableupdate` to every ephemeral GitHub Actions Runner. That makes both runnerd-managed templates and user-supplied templates depend on their preinstalled Runner version remaining inside GitHub's accepted version window. runnerd can refresh its managed templates, but it cannot rebuild a user's private template.

GitHub Actions Runner already owns a signed, hash-verified self-update flow. For ephemeral runners, the official `run.sh` wrapper also handles the update exit code and restarts the listener before a job is accepted. runnerd should use that protocol instead of implementing another downloader.

## Decision

- Stop passing `--disableupdate` for every Runner registration, regardless of whether the selected Runner Spec is managed or scoped custom.
- Keep the existing managed-template Runner archive pin, checksum, size check, compatibility gate, and periodic template rebuild process. The pin is the preinstalled bootstrap baseline, not a ceiling on the version that executes a job.
- Do not add a per-Spec update mode or an in-sandbox custom downloader.
- Preserve the existing `runner_version` API and database field as the preinstalled template Runner version for compatibility.
- Add `effective_runner_version` as best-effort evidence of the Runner version that reached the job-start hook after any official self-update.

## Runtime and evidence flow

1. runnerd copies the template's `/opt/actions-runner` installation into the writable Runner work directory.
2. runnerd configures the ephemeral Runner without `--disableupdate` and starts the official `run.sh` wrapper.
3. GitHub may direct the Runner to update. The official updater downloads and validates the server-selected package, and `run.sh` restarts the listener.
4. Immediately before the first job starts, runnerd's fixed job-start hook runs the writable work directory's `Runner.Listener --version` command. It accepts only one valid value of at most 256 bytes and writes it with `RUNNERD_JOB_STARTED` to a permission-restricted one-shot FIFO consumed by the outer startup process. Official hook stdout remains in the GitHub Job log and is not used as evidence.
5. The outer startup process forwards the FIFO payload through the Sandbox command stdout observed by runnerd. The Sandbox service accepts only the first valid effective-version marker observed before the job-start boundary, freezes it in memory, and ignores every later Workflow output marker. The hook removes the FIFO before Workflow steps begin, so no mutable evidence channel remains for a step to replace.
6. The exit callback carries the frozen effective version as typed evidence even when provider cleanup returns no command result. The lifecycle layer persists it only for the matching Sandbox ID and PID attempt and only fills an empty value.

The evidence is diagnostic, not an authorization or execution input. Only the fixed hook's first valid marker forwarded by the outer startup process before `RUNNERD_JOB_STARTED` is eligible; hook and Workflow Job-log output is never parsed for the version. Historical rows may keep the field empty.

## State compatibility

- Add nullable/empty-compatible `effective_runner_version` storage through the existing additive SQLite Runner Request migration path and normal GORM migration for other backends.
- Fresh retry and mismatched-job requeue clear region, resolved template ID, template version, preinstalled Runner version, and effective Runner version together.
- A runnerd restart after the pre-job marker may leave the best-effort effective version empty; recovery does not reconstruct it from post-start Workflow output.
- Stale exit callbacks cannot mutate the current request.

## Admin UI

The Runner Request detail page shows:

- Template version
- Preinstalled Runner version (`runner_version`)
- Effective Runner version (`effective_runner_version`)

An unavailable effective version renders as `-`, which is expected for historical requests, jobs that never started, runnerd restarts after capture, and nonfatal probe failures.

## Documentation contract

Managed-template documentation continues to describe the exact pinned version checked by source and release smoke. Custom-template documentation states that a compatible preinstalled Runner is still required, but runnerd leaves official self-update enabled at registration time. Testing and architecture documents distinguish the pre-launch template snapshot from post-update effective-version evidence.

## Verification

- Focused startup-script tests emulate the official Runner's Job-log routing and prove `--disableupdate` is absent while the hook's bounded pre-job evidence still reaches the outer command stdout through the one-shot FIFO.
- Sandbox Runner tests prove effective-version parsing bounds, chunked marker handling, and rejection of Workflow-only markers after `RUNNERD_JOB_STARTED`.
- Lifecycle tests prove matching-attempt persistence and stale-attempt rejection.
- State tests prove persistence, clearing, and additive SQLite migration.
- UI tests and the i18n contract prove both version labels render in English and Chinese.
- Run focused package tests, `task ui-i18n-check`, `task lint`, and `task test` before completion.
