# Runnerd Implementation Review

[Chinese](zh/runner-implementation-review.md)

Originally reviewed: 2026-07-16. Baseline refreshed: 2026-08-28.

Scope:

- Review target: implementation status after file-based config, GORM-backed DB schema migration, retry/lease/audit handling, ordinary-user Sandbox catalogs and scoped Runner Specs, admin account-role controls, embedded UI assets, and local development workflow updates.
- Local references still useful for future comparison: actions-runner-controller style reconciliation and fireactions style pool/config modeling.

## Executive Summary

Runnerd has moved past the original 2026-05-19 gap list. Runtime configuration is now file-first, runner state is DB-backed, schema creation is mostly driven by GORM model tags, retry/lease/audit fields exist, GitHub App auth can resolve installations dynamically, the ordinary-user UI covers job/repository/account setup and account/Organization Runner Specs, the admin console covers the core management workflow including audited account-role changes, diagnostics expose pprof/expvar state, and the documented local workflow includes `task dev`.

The remaining work is no longer a basic architecture catch-up. The next decisions are product and operations hardening: whether to keep token/basic auth as local compatibility modes, how much config management belongs in the admin console, and consistently executing and maintaining the canonical deployment smoke checklist before treating the service as production-ready.

## Current Baseline

- Configuration is loaded from `runnerd.yaml` by default, or from `--config`. Relative sqlite database paths and GitHub App private-key paths resolve from the config file directory.
- The config schema covers server, database, OAuth session auth, Sandbox lifecycle timeouts, GitHub webhook/auth/OAuth, allowed repositories, and worker retry/lease/concurrency behavior. Sandbox service API URL and API key are database-backed scoped Preferences or the disabled-by-default admin fallback rather than file config.
- Exactly one GitHub API auth mode is allowed: GitHub App, token, or basic auth. GitHub App mode supports optional static `installation_id`; when omitted, runnerd resolves installation access per job repository and caches transports.
- Runner requests, events, Runner Specs, retry metadata, leases, accounts, GitHub installations, scoped preferences/secrets, and audit events are stored in the configured database backend. Internal Runner Group and Repository Policy models were removed completely in Release C and are not part of supported state behavior.
- State schema creation runs through GORM `AutoMigrate` after a narrow compatibility pass for older columns, obsolete OAuth constraints, and incompatible legacy scope tables. GORM foreign-key creation is intentionally disabled. Legacy account preference/secret tables without scope columns are intentionally reset, so Sandbox settings/API keys require reconfiguration and affected users require GitHub reauthentication before installation sync.
- Worker processing uses DB claim/lease semantics and retry scheduling instead of only in-memory queue ownership.
- Transient Qiniu sandbox, GitHub, rate-limit, timeout, and temporary network failures are classified for retry or queue deferral. Deterministic auth/config/template failures fail immediately.
- Admin routes expose the account list and audited role controls at `/admin/accounts` and `/admin/api/accounts`, runner request management, retry/stop/log access, Runner Specs, the platform Sandbox fallback, match tests, audit events, and diagnostics. Account administration is role-only; self-role changes and changes that could leave no administrator are rejected. Retired Group/Policy and catalog-readiness APIs return 404.
- `/` is the public landing page and `/jobs` is the protected ordinary-user Jobs dashboard. Stable GitHub-context job-group routes include `/github/pulls/{owner}/{repo}/{number}/jobs`; unified repository/Sandbox readiness is at `/repositories`. `/account/repositories` and `/organizations/{login}/repositories` remain scoped compatibility links to that page. Sandbox Service, Templates, and Instances remain available under account or manageable-organization settings routes.
- The admin console exposes `/admin/sandbox_service` and role-gated `/admin/api/sandbox-service-default` endpoints for the global fallback, including all/selected repository-owner audience controls; provider catalogs remain ordinary-user resources.
- Authenticated catalog APIs expose region-filtered templates and region/template-filtered runner instances through `/user/sandbox/templates` and `/user/sandbox/instances`. They resolve encrypted credentials from the selected account or installation scope and do not expose provider secrets.
- Ordinary users browse the scope-free, read-only platform catalog at `/runner-specs`; `/account/runner-specs` and `/organizations/{login}/runner-specs` manage only custom Specs owned by that scope. All use `/user/runner-specs`. Managed catalog identity, enabled state, and concurrency remain global Admin policy. Scoped custom template validation uses the selected scope's Sandbox credentials, while repository-only collaborators retain only readiness visibility under `/repositories`. The user response returns physical template IDs only for the selected scope's own custom entries, and queued requests revalidate the latest global or scoped-custom enabled state and labels before startup. The admin global Runner Specs API remains a separate role-gated surface.
- The React UI in `ui/` is embedded for production from `internal/server/ui/*`; development builds proxy UI assets to Vite through `internal/server/ui_assets_development.go`.
- `task dev` starts Vite and the Go service together in development mode. `task build` builds the UI first, then compiles `bin/runnerd` with embedded production assets.
- Diagnostics are available through the admin UI and `/diagnostics/pprof` / `/diagnostics/vars`, backed by `github.com/jimmicro/pprof` and expvar.

## Remaining Decisions

### 1. Auth Policy

Token and basic auth are still supported alongside GitHub App auth. That is useful for local verification or legacy credentials, but it means the product is not GitHub-App-only. Decide whether these modes are intentional compatibility paths or should be removed before production hardening.

### 2. Ordinary-User Repository Readiness

The ordinary-user UI is now routed outside `/admin/*`. `/repositories` lists every repository in the user/GitHub App authorization intersection, annotates local job activity without hiding repositories that have not run, and shows the effective Sandbox service source for the selected account or organization. When no effective source exists, a manageable scope links to its account or organization Preferences page; credentials are edited only in Settings.

### 3. Scoped Runner Specs Verification

Platform and scoped custom Runner Specs are implemented through `/runner-specs`, `/account/runner-specs`, `/organizations/{login}/runner-specs`, and `/user/runner-specs`. Local State, API, UI, i18n, full Go, and the existing four fixture-backed production smoke checks pass. Those browser checks cover public pages and the Jobs layout, not Runner Specs routes. Dedicated Runner Specs browser fixtures, PostgreSQL/MySQL transactions, production SQLite snapshot and downgrade gates, real Sandbox template validation, personal/Organization GitHub workflow execution, and a deployed canary remain release gates; they are tracked in `TODO.md` and are not represented as completed evidence.

### 4. Config Management

Runtime config is file-first, but the admin console does not yet provide an effective-config view, config validation preview, reload workflow, or import/export flow. Keep the current file-only operations model unless live config operations become a clear requirement.

### 5. Deployment Smoke

Local build/lint/test coverage validates the code path, but production readiness still depends on a real GitHub App installation, real Qiniu sandbox templates, webhook delivery, and sandbox runner execution. Run and maintain the [deployment smoke checklist](deployment-smoke.md) covering webhook signature handling, installation resolution, runner spec matching, sandbox creation, GitHub job pickup, cleanup, and diagnostics.

### 6. Multi-Instance And Operations

The DB lease model is in place, but multi-process behavior should be verified with two runnerd instances against the same database before documenting multi-instance support. Expvar diagnostics cover useful counters and gauges; add histogram/export adapters only if deployment observability needs them.

### 7. Schema Compatibility

The current migration path intentionally avoids a full handwritten migration history. GORM tags in `internal/state/records.go` define the normal schema, while `internal/state/db.go` keeps only narrow compatibility actions for older state databases, including the explicit reset of pre-scope account preference/secret tables. Future schema changes should include old-schema upgrade tests that assert preservation or intentional data loss as appropriate when they add required columns, indexes with uniqueness semantics, or relationship constraints.

## Suggested Next Order

1. Keep `task dev`, `task build`, `task lint`, and `task test` green on every branch that touches backend/UI boundaries.
2. Run and maintain the deployment smoke checklist using a real GitHub App, one repository, and one Qiniu sandbox template.
3. Decide whether token/basic auth remain supported modes.
4. Add an effective-config diagnostics view only after the desired config operations model is clear.
5. Stress DB lease behavior with concurrent runnerd processes before advertising multi-instance support.
6. Preserve old-schema upgrade coverage whenever state records or GORM migration tags change.

## Verification Notes

The stale findings from the 2026-05-19 review have been retired because the referenced implementation has changed materially. Re-run the current verification commands when this document is updated:

```bash
task lint
task test
task build
```
