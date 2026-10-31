# TODO

This file tracks active project work. Completed behavior should move into `README.md` or `docs/`.

## Active Roadmap

- Complete deployed acceptance for CI Runner account/repository concurrency: configure limits, verify an unrelated repository progresses under backlog, cleanup releases slots, and restart with a reduced limit retains existing Runners. Local regression coverage is documented in [Testing](docs/testing.md#ci-runner-account-and-repository-concurrency).

- Local PostgreSQL Runner/Deployment/new Web Terminal and restart acceptance passed on 2026-10-29. Before the SQLite migration, complete PR review/CI and rehearse production-copy timing/capacity, backup restore, maintenance webhook redelivery and reconciliation. See [the migration runbook](docs/sqlite-postgres-offline-migration.md). Tracking: https://github.com/qbox/sandbox/issues/535.

- Before promoting the Deploy Sandbox service eligibility fix, run the owner/member platform public-template, personal-repository collaborator versus Installation-owner audience, audience denial, scoped/inherited override, quota and saved-runtime cleanup gates in [Deployment Smoke](docs/deployment-smoke.md#9-sandbox-deploy-acceptance). Local regressions do not replace live provider acceptance.

- Complete individual-repository removal acceptance for the [GitHub context workflow](docs/deployment.md#github-context-routes). Live Installation suspension/cache expiry/recovery and OAuth-token revocation/API denial/in-app browser reauthentication passed on 2026-10-16. GitHub disallowed removing the sole selected repository, so that distinct revocation mode remains unverified.

- Before production promotion of PR #133, verify upgrade, rollback writes and re-upgrade on a disposable copy of the production database. Local SQLite and real PostgreSQL/MySQL regressions cover counter repair and independent deployment startup, but do not replace that snapshot gate. MySQL deployment navigation requires MySQL 8.0+; Deploy remains single-process without mixed-version writers.

- Before Issue #125 can close, complete the remaining acceptance gates below and retain the endpoint, per-scope quota, and terminal retention evidence against a deployed runnerd. The first implementation uses public HTTPS endpoints, a transactional per-scope concurrency limit, and configurable terminal retention; an authenticated reverse proxy remains a follow-up if product requirements change.

- Complete the remaining Issue #125 acceptance for cross-account repository authorization with owner-only Sandbox settings (a second real GitHub account is not yet available). Public authenticated API duplicate/terminal replay, DELETE/provider release, per-scope quota/re-admission, synthetic startup-failure cleanup, accelerated terminal retention with audit preservation, ordinary Chrome 375-pixel history/log stacking, same-Commit concurrent CI success/Runner release with deployment survival, public `base` worker execution/reconnection/TTL, real provider lost-create-response reconciliation and release, full local runnerd process restart, authenticated browser creation/detail/reload/stop and revision/latest redeploy evidence, and the user's 2026-10-14 confirmation of real Sandbox acceptance for the Web terminal iteration are recorded in [Deploy configuration](docs/deployment.md#iteration-plan); local fakes and browser fixtures remain supporting evidence only.

- Validate [Issue #134](https://github.com/qiniu/ci-runner/issues/134) with a real environment-gated workflow after rollout: waiting → queued, Runner pickup, cleanup, and targeted historical recovery where needed. Local queue/race tests do not establish production acceptance.

- Before promoting the pipx isolation fix, rebuild and smoke-test Ubuntu 26.04 in each supported Sandbox region; the local Docker regression is not regional release evidence.

- If the original llgo network failure recurs, capture comparable live GitHub API, Ubuntu archive, and LLVM APT evidence from the same running Sandbox before changing diagnostics again. The original root cause remains unverified without that same-window evidence.
- Keep a separate downloadable diagnostic bundle out of the active roadmap unless a real need emerges for cross-team handoff without UI access, long-term offline archiving, or repeated manual evidence assembly. The Admin Runner request page and its retained timeline remain the single diagnostic surface.
- Plan separately authorized cleanup of historical `runner_requests.github_payload_json` after verifying GitHub context and installation-ID backfills on a backup. New requests no longer store raw webhook bodies; existing payloads, the legacy column, and startup backfill remain. Historical request/log retention and repeated-log limits are still undecided.
- Decide separately whether repository-level Runner Spec overrides need a future product model. Organization fork sponsorship is intentionally limited to public-name Specs with explicit sponsorship enabled and must not restore the retired Repository Policy model.
- Decide whether GitHub token and basic auth remain supported compatibility modes or should be removed in favor of GitHub App-only operation.
- Add an effective-config diagnostics view or config validation workflow if operators need to inspect runtime config from the UI.
- Verify DB lease behavior with two runnerd processes sharing the same database before documenting multi-instance support.
- Decide whether expvar diagnostics need a Prometheus/export adapter or histogram-style latency views for deployment observability.
- After landing the Template Check split, retain current-head GitHub evidence for both workflows, verify unrelated PRs skip Template Check and relevant/manual runs execute it, and confirm Fenno follows both workflows. Local Dockerfile checks do not establish hosted trigger behavior or regional template acceptance.
- After the `ui-production-smoke` check has reported on `main`, enable it as a required status check in branch protection; the repository currently has no required status checks.
- Define a privacy-safe documentation activation funnel for `/docs` (guide entry, hosted/deploy path selection, and first successful job) before adding analytics; do not collect repository names, workflow names, credentials, or log content.
- Before merging the Runner maintenance schedule migration, confirm Fenno's
  repository permissions, actual timezone, single-task execution and runtime
  budget; after merge, retain real no-op, changed, failure-recovery and
  repeated-run evidence. Source pin updates already cover Linux and Windows;
  the manual Actions workflow validates only and cannot create an upgrade PR.
- Complete [Issue #68](https://github.com/qiniu/ci-runner/issues/68) through the
  first real Actions Runner upgrade: after the verified source PR merges, use
  existing local Task targets to rebuild and publish all eight Linux templates
  in both regions, check catalogs, run real Sandbox smoke and standard/large
  production Jobs, and retain evidence in a versioned promotion record linked
  from Issue #68. Keep production Sandbox credentials out of Fenno and GitHub
  Actions; stop for operator review rather than attempting uncertain rollback.
- Keep old-schema upgrade coverage whenever state records or GORM tags change; the current migration path is a narrow legacy compatibility pass followed by `AutoMigrate`, with additive-only handling for existing SQLite `runner_requests` and `runner_profiles`, not a full handwritten migration history.
- Run the scoped Runner Specs release gates when dedicated environments are available: PostgreSQL/MySQL audited-mutation and fresh-schema tests, a production SQLite snapshot migration, and the SQLite down-version startup check against the documented baseline.
- Extend the existing fixture-backed account/Organization Runner Spec form coverage with broader scope-switching browser scenarios; stale-scope response isolation currently has component coverage.
- Run one real personal-account and one real Organization GitHub Actions workflow through the scoped Runner Specs path, including Sandbox template validation, cleanup, and the repository-only outsider authorization boundary. For organization Runner groups, verify allowed and denied repository preflight, group + labels routing, GitHub workflow restrictions and cleanup; see [the group contract](docs/platform-runner-specs.md#github-runner-groups).
- Execute the deployment canary for the scoped Runner Specs routes and update `docs/user-scoped-runner-configuration.md` with origin, version, and cleanup evidence; local fixture-backed browser smoke is not a deployment substitute.
- Promote the preview Windows 11 Runner: confirm Windows Sandbox and `windows11` base-template support in each supported region, build and smoke `github-runner-windows-11` there, run one full `task dev` webhook-to-cleanup workflow with a fixed-ID Windows spec in each region, and decide whether it joins the published public-name catalog. The disk is 50 GiB with about 11 GiB free after setup; the build only guarantees 10 GiB free and does not enlarge it.
- Deregister the GitHub Runner when runnerd destroys a Sandbox before its Job starts. The in-Sandbox `config remove` cleanup never runs in that case, so an offline Runner remains registered (observed on Windows; the Linux path has the same shape).
- Add Windows variants of the fixed-target network diagnostics and the Web Console, which currently assume a POSIX shell.

## Maintenance

- Keep `task ui-terminal-fault-test` covering timed recovery, exact cursor/output continuity and real PTY cleanup. Production ingress, remote Sandbox and permission-revocation acceptance remain separate deployment gates; see [Testing](docs/testing.md#timed-terminal-transport-faults).

- Keep [Platform Runner Specs](docs/platform-runner-specs.md) aligned with Admin unified template references and provider-validated directory fields, existing execution behavior, public-directory projection and the per-process cache invalidation contract. Preserve upgrade coverage for legacy ID references with unused public names, displayed public IDs across restarts, and unchanged runtime behavior. The visibility adjustment needs no additional data migration. Production snapshot migration, down-version startup and real regional workflow smoke remain deployment gates.

- Keep Job details focused on the shared Runner event timeline, Web Console, and request details. Jobs and admin request details must use the same event rows and cursor behavior while retaining their authorization boundaries. Workflow step logs remain on GitHub through the Job/Run link; do not restore duplicate log fetching or streaming without a separately validated need. See [Testing: GitHub Actions logs](docs/testing.md#9-how-to-read-github-actions-logs).

- Keep `README.md` and `README.zh.md`, the paired English/Chinese files under `docs/`, and this roadmap in sync when build, dev, config, or UI asset workflows change.
- Keep `docs/deployment-smoke.md` and `docs/zh/deployment-smoke.md` aligned with real GitHub App, webhook, Qiniu sandbox template, runner pickup, cleanup, and diagnostics behavior.
- Keep generated production UI assets under `internal/server/ui/` out of hand edits; change source files in `ui/` and rebuild with `task build`.
- Keep the paired public guide sources under `ui/src/content/site-docs/en/` and `ui/src/content/site-docs/zh/` aligned; add exact public routes through `ui/src/site-doc-routes.ts` instead of deriving arbitrary paths from filenames.
- When changing `internal/state/records.go` tags or migration helpers in `internal/state/db.go`, run `go test ./internal/state -count=1` before the broader test suite, plus `TestMigrateSQLiteRunnerRequestSnapshot` when a production SQLite export is available and `Test(ApplyMutationWithAudit|FreshSchema)SQLBackends` against dedicated PostgreSQL/MySQL test databases for cross-dialect changes.
- Keep `.agents/` focused on agent-only rules and repeatable workflows; keep operator, architecture, and deployment content in `README.md` and `docs/`.
