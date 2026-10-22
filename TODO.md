# TODO

This file tracks active project work. Completed behavior should move into `README.md` or `docs/`.

## Active Roadmap

- Rehearse and authorize the [online SQLite to PostgreSQL migration](docs/sqlite-postgres-online-migration.md) on a production copy before rollout. Transaction capture, resumable baseline/replay, same-process cutover, reverse verification/rollback, startup authority, protected local commands and finalization are implemented; local real-database/server gates do not prove production readiness. Preserve a real Runner Job, Deployment and the same open PTY, measure ingress/lease/latency and disk budgets, and rehearse the preparation-release process upgrade separately. Tracking: https://github.com/qbox/sandbox/issues/535.

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
- After the `ui-production-smoke` check has reported on `main`, enable it as a required status check in branch protection; the repository currently has no required status checks.
- Define a privacy-safe documentation activation funnel for `/docs` (guide entry, hosted/deploy path selection, and first successful job) before adding analytics; do not collect repository names, workflow names, credentials, or log content.
- Complete [Issue #68](https://github.com/qiniu/ci-runner/issues/68) and
  [Issue #112](https://github.com/qiniu/ci-runner/issues/112) through the next
  real Actions Runner release: after the automated source PR merges, use the
  existing local Task targets to sequentially rebuild and publish all eight
  templates in both regions, check both catalogs, run real Sandbox smoke, and
  retain the build/catalog/smoke evidence in Issue #68. Keep production
  Sandbox credentials out of GitHub Actions and stop for operator review rather
  than attempting an uncertain automatic rollback.
- Keep old-schema upgrade coverage whenever state records or GORM tags change; the current migration path is a narrow legacy compatibility pass followed by `AutoMigrate`, with additive-only handling for existing SQLite `runner_requests` and `runner_profiles`, not a full handwritten migration history.
- Run the scoped Runner Specs release gates when dedicated environments are available: PostgreSQL/MySQL audited-mutation and fresh-schema tests, a production SQLite snapshot migration, and the SQLite down-version startup check against the documented baseline.
- Extend the existing fixture-backed account/Organization Runner Spec form coverage with broader scope-switching browser scenarios; stale-scope response isolation currently has component coverage.
- Run one real personal-account and one real Organization GitHub Actions workflow through the scoped Runner Specs path, including Sandbox template validation, cleanup, and the repository-only outsider authorization boundary. For organization Runner groups, verify allowed and denied repository preflight, group + labels routing, GitHub workflow restrictions and cleanup; see [the group contract](docs/platform-runner-specs.md#github-runner-groups).
- Execute the deployment canary for the scoped Runner Specs routes and update `docs/user-scoped-runner-configuration.md` with origin, version, and cleanup evidence; local fixture-backed browser smoke is not a deployment substitute.

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
