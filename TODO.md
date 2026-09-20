# TODO

This file tracks active project work. Completed behavior should move into `README.md` or `docs/`.

## Active Roadmap

- Complete [Issue #93](https://github.com/qiniu/ci-runner/issues/93) release promotion after the shared-source Actions Runner 2.337.0 change. The Ubuntu 24.04 development template version `20260917.3` reached `Status: ready` on 2026-09-17 (template `w54qc828mejc6x8sgacn`, build `56114e2c-b41d-4239-aa6b-134a8da39c31`), and all nine then-current Sandbox smoke checks passed against the configured endpoint. On 2026-09-20, `github-runner-ubuntu-slim-large` was rebuilt in place with its existing ID `4kkforq8trhqqmgpxmfc`; build `c68ceb54-85a2-48a7-bdaf-58e54b30d0cc` reached `uploaded` with 87,501 MiB total disk. Its fresh Sandbox exposed only 77,149 MiB free, below the 80,896-MiB release threshold. Remaining: increase the effective provider allocation for that template and repeat its rebuild/smoke, then rebuild and verify the other existing physical template names and IDs; run release smoke in `cn-yangzhou-1` and `us-south-1`; verify representative workflows and cleanup; then publish and check the regional catalogs. Total rootfs size can exceed the requested free space, so catalog capacity alone does not establish the runtime gate.
- If the original llgo network failure recurs, capture comparable live GitHub API, Ubuntu archive, and LLVM APT evidence from the same running Sandbox before changing diagnostics again. The original root cause remains unverified without that same-window evidence.
- Keep a separate downloadable diagnostic bundle out of the active roadmap unless a real need emerges for cross-team handoff without UI access, long-term offline archiving, or repeated manual evidence assembly. The Admin Runner request page and its retained timeline remain the single diagnostic surface.
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
