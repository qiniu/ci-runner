# GitHub Actions Runner Auto-update Design

## Context

runnerd originally passed `--disableupdate` to every ephemeral GitHub Actions Runner. PR #107 removed that flag for managed and user-supplied templates so the official updater remains available. Production evidence then showed an important ordering limit: an old ephemeral Runner can accept the queued Job before GitHub sends its update message, complete the Job on the old version, update only afterward, and lose the updated writable copy when the Sandbox is destroyed.

Managed templates can keep their tested pin current through runnerd's release process. runnerd cannot rebuild a user's private template, so a stale custom template needs a pre-registration gate if the current queued Job must use the current official Runner.

## Decision

- Continue omitting `--disableupdate` for every Runner registration, regardless of whether the selected Runner Spec is managed or scoped custom.
- Keep the existing managed-template Runner archive pin, checksum, size check, compatibility gate, and periodic template rebuild process. The pin is the preinstalled bootstrap baseline, not a ceiling on the version that executes a job.
- Before registering a custom-template Runner, use runnerd's existing GitHub authentication to query the repository- or Organization-scoped Runner downloads endpoint. Strictly validate the official Linux descriptors in runnerd and pass only architecture, version, GitHub-owned release URL, and SHA-256 into the Sandbox.
- If the custom template's writable Runner is stale, download under fixed time and 512-MiB bounds, verify checksum and extracted version, replace the writable directory, and only then run `config.sh`. Fail closed before registration. A matching version bypasses the downloader and its tool requirements.
- Do not apply this preflight to managed templates and do not add a per-Spec update mode. Official self-update remains enabled after registration as a secondary compatibility mechanism.
- Preserve the existing `runner_version` API and database field as the preinstalled template Runner version for compatibility.
- Add `effective_runner_version` as best-effort evidence of the Runner version that reached the job-start hook after any official self-update.

## Runtime and evidence flow

1. For a custom Runner Spec, runnerd queries and validates GitHub's official Linux application descriptors before it creates a registration token. A lookup failure is recorded as `github_runner_downloads` and no Sandbox starts.
2. runnerd copies the template's `/opt/actions-runner` installation into the writable Runner work directory. Managed Specs have no preflight manifest and proceed unchanged.
3. A custom Sandbox selects the descriptor for `uname -m`. If the writable listener is stale, it requires `curl`, `tar`, `sha256sum`, and `mktemp`, downloads and verifies the archive, extracts it into a fresh sibling directory, verifies `Runner.Listener --version`, and replaces the work directory. Any failure exits before `config.sh`; an equal version skips this step.
4. runnerd configures the ephemeral Runner without `--disableupdate` and starts the official `run.sh` wrapper. GitHub may still direct a later official self-update.
5. Immediately before the first job starts, runnerd's fixed job-start hook runs the writable work directory's `Runner.Listener --version` command. It accepts only one valid value of at most 256 bytes and writes it with `RUNNERD_JOB_STARTED` to a permission-restricted one-shot FIFO consumed by the outer startup process. Official hook stdout remains in the GitHub Job log and is not used as evidence.
6. The outer startup process forwards the FIFO payload through the Sandbox command stdout observed by runnerd. The Sandbox service accepts only the first valid effective-version marker observed before the job-start boundary, freezes it in memory, and ignores every later Workflow output marker. The hook removes the FIFO before Workflow steps begin, so no mutable evidence channel remains for a step to replace.
7. The exit callback carries the frozen effective version as typed evidence even when provider cleanup returns no command result. The lifecycle layer persists it only for the matching Sandbox ID and PID attempt and only fills an empty value.

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

Managed-template documentation continues to describe the exact pinned version checked by source and release smoke. Custom-template documentation describes the registration preflight, required stale-update tools, fail-closed behavior, and expected repeated download from an unchanged immutable template. Testing and architecture documents distinguish the pre-launch template snapshot from post-update effective-version evidence. Production acceptance for a deliberately stale custom template requires `runner_version != effective_runner_version` on a completed Job.

## Verification

- GitHub client tests cover repository and Organization endpoints, strict descriptor validation, caching, and concurrent lookup coalescing.
- Focused startup-script tests execute real rendered scripts to prove a stale Runner is replaced before `config.sh`, a matching version skips downloader tools, failure paths never register, `--disableupdate` is absent, and the hook's bounded pre-job evidence still reaches the outer command stdout through the one-shot FIFO.
- Lifecycle tests prove only custom Specs resolve and receive descriptors, while lookup failure occurs before registration-token and Sandbox creation.
- Sandbox Runner tests prove effective-version parsing bounds, chunked marker handling, and rejection of Workflow-only markers after `RUNNERD_JOB_STARTED`.
- Lifecycle tests prove matching-attempt persistence and stale-attempt rejection.
- State tests prove persistence, clearing, and additive SQLite migration.
- UI tests and the i18n contract prove both version labels render in English and Chinese.
- Run focused package tests, `task ui-i18n-check`, `task lint`, and `task test` before completion.
