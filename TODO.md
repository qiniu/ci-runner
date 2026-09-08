# TODO

This file tracks active project work. Completed behavior should move into `README.md` or `docs/`.

## Active Roadmap

- Plan separately authorized cleanup of historical `runner_requests.github_payload_json` after verifying GitHub context and installation-ID backfills on a backup. New requests no longer store raw webhook bodies; existing payloads, the legacy column, and startup backfill remain. Historical request/log retention and repeated-log limits are still undecided.
- Decide whether repository-level Runner Spec overrides and cross-organization approval are needed; the current scope remains account or manageable Organization only.
- Decide whether GitHub token and basic auth remain supported compatibility modes or should be removed in favor of GitHub App-only operation.
- Add an effective-config diagnostics view or config validation workflow if operators need to inspect runtime config from the UI.
- Verify DB lease behavior with two runnerd processes sharing the same database before documenting multi-instance support.
- Decide whether expvar diagnostics need a Prometheus/export adapter or histogram-style latency views for deployment observability.
- After the `ui-production-smoke` check has reported on `main`, enable it as a required status check in branch protection; the repository currently has no required status checks.
- Define a privacy-safe documentation activation funnel for `/docs` (guide entry, hosted/deploy path selection, and first successful job) before adding analytics; do not collect repository names, workflow names, credentials, or log content.
- Decide production credential ownership, approval, and rotation before adding
  a manual `workflow_dispatch` that publishes public Sandbox templates.
- Keep old-schema upgrade coverage whenever state records or GORM tags change; the current migration path is a narrow legacy compatibility pass followed by `AutoMigrate`, with additive-only handling for existing SQLite `runner_requests` and `runner_profiles`, not a full handwritten migration history.
- Run the scoped Runner Specs release gates when dedicated environments are available: PostgreSQL/MySQL audited-mutation and fresh-schema tests, a production SQLite snapshot migration, and the SQLite down-version startup check against the documented baseline.
- Extend fixture-backed production UI smoke to cover `/runner-specs`, `/account/runner-specs`, and one Organization scope, including stale-scope response isolation; the current four-test smoke covers public pages and the Jobs layout only.
- Run one real personal-account and one real Organization GitHub Actions workflow through the scoped Runner Specs path, including Sandbox template validation, cleanup, and the repository-only outsider authorization boundary.
- Execute the deployment canary for the scoped Runner Specs routes and update `docs/user-scoped-runner-configuration.md` with origin, version, and cleanup evidence; local fixture-backed browser smoke is not a deployment substitute.

## Maintenance

- Keep `README.md` and `README.zh.md`, the paired English/Chinese files under `docs/`, and this roadmap in sync when build, dev, config, or UI asset workflows change.
- Keep `docs/deployment-smoke.md` and `docs/zh/deployment-smoke.md` aligned with real GitHub App, webhook, Qiniu sandbox template, runner pickup, cleanup, and diagnostics behavior.
- Keep generated production UI assets under `internal/server/ui/` out of hand edits; change source files in `ui/` and rebuild with `task build`.
- Keep the paired public guide sources under `ui/src/content/site-docs/en/` and `ui/src/content/site-docs/zh/` aligned; add exact public routes through `ui/src/site-doc-routes.ts` instead of deriving arbitrary paths from filenames.
- When changing `internal/state/records.go` tags or migration helpers in `internal/state/db.go`, run `go test ./internal/state -count=1` before the broader test suite, plus `TestMigrateSQLiteRunnerRequestSnapshot` when a production SQLite export is available and `Test(ApplyMutationWithAudit|FreshSchema)SQLBackends` against dedicated PostgreSQL/MySQL test databases for cross-dialect changes.
- Keep `.agents/` focused on agent-only rules and repeatable workflows; keep operator, architecture, and deployment content in `README.md` and `docs/`.
