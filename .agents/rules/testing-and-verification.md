# Testing And Verification

Choose the lightest credible verification for the change, then report exactly what ran.

## Documentation Only

For docs, rules, or skill-only changes:

```bash
test -f AGENTS.md
test -d .agents/rules
test -d .agents/skills
git diff --check
```

Also inspect the diff and keep `docs/README.md` aligned when adding, removing, or reclassifying docs.

## Config Secret Obfuscation

- Run `go test ./internal/config ./cmd/runnerd -count=1` after changing `config.Secret`, `RUNNERD_ENC(v1:...)`, or the stdin generator.
- Cover plaintext compatibility, obfuscation round trips, malformed/tampered value rejection, and masking through fmt, slog, JSON, and YAML.
- When adding a sensitive config field, type it as `config.Secret`, call `Value()` only where plaintext is required, and update the supported-field lists in `README.md`, `README.zh.md`, `docs/testing.md`, and `docs/zh/testing.md`.

## State Schema

For runner-request payload persistence changes, extend the existing state tests: assert accepted and rejected requests leave `github_payload_json` empty, `workflow_job` and `workflow_run` context survives reopening the database, and legacy metadata/installation-ID backfills preserve historical payloads. Keep repeated-migration coverage; do not use tests that create new requests as a substitute for legacy raw-payload fixtures.

When touching `internal/state/records.go`, GORM tags, indexes, or migration helpers in `internal/state/db.go`, run:

```bash
go test ./internal/state -count=1
```

If the change affects callers outside the state package, follow with:

```bash
go test ./...
task test
```

Old-schema upgrade coverage is required when adding required columns, changing uniqueness semantics, or altering relationship constraints. Fresh sqlite creation is not enough. Existing SQLite `runner_requests` and `runner_profiles` migrations are additive-only; non-additive changes require an explicit compatibility helper instead of generic table recreation. Assert preserved Installation ID, Sandbox snapshot fields, and `updated_at` values where migration promises preservation; preserve runner-profile rows and existing indexes while adding all missing model fields; and assert explicit data reset where the compatibility contract requires reconfiguration. For production snapshots, compare total rows plus populated `github_installation_id`, `sandbox_api_url`, `sandbox_api_key_encrypted`, and `sandbox_config_source` counts across two consecutive starts.

For Runner Spec name validation, cover both `UpsertProfile` and managed reconciliation. Reject newly written names containing `/` or equal to `.` or `..` without committing profile or audit state, and prove historical rows with those names still survive startup migration, remain listable/readable/matchable, and are not automatically repaired.

Use the state-only snapshot gate when a production export is available:

```bash
RUNNERD_SQLITE_SNAPSHOT=/path/to/runnerd-export.db \
  go test ./internal/state -run TestMigrateSQLiteRunnerRequestSnapshot -count=1 -v
```

For schema, migration, or audited-mutation transaction changes that affect
Postgres or MySQL, also run the opt-in real-dialect tests against dedicated
disposable databases whose names end in `_test`:

```bash
RUNNERD_CATALOG_BACKEND_TESTS=1 \
RUNNERD_POSTGRES_TEST_DSN='<dedicated postgres test DSN>' \
RUNNERD_MYSQL_TEST_DSN='<dedicated mysql test DSN>' \
  go test ./internal/state -run 'Test(ApplyMutationWithAudit|FreshSchema|ScopedRunnerCatalogFreshSchema)SQLBackends' -count=1 -v
```

## Go Server Or API

- For focused backend changes, start with the relevant package test.
- For broad server/API behavior, run `go test ./...`.
- For pre-merge confidence, run `task test`; it rebuilds UI assets, runs Bun UI tests, and runs Go tests with race and coverage.
- For ordinary-user Jobs authorization, cover shared installations with different repository access, exact installation/repository pair matching, filtering before the database limit, list/detail/group/log/terminal consistency, missing or rejected GitHub user tokens, inaccessible linked installations, and short-lived access-cache behavior.
- Keep regression coverage proving retired Runner Group/Policy models and APIs stay absent, fresh schemas omit their tables, and Runner Spec JSON omits `default_available`. No current behavior or recovery test may depend on legacy physical tables. Runner Spec/Sandbox mutations plus managed reconciliation must commit their data change and audit evidence atomically; rejected mutations leave no audit event, while audit failures roll back the mutation.

## Scoped Runner Specs

- Run `go test ./internal/state -count=1` after changing scoped catalog uniqueness, matching, or concurrency. Preserve old SQLite upgrade tests and run the dedicated PostgreSQL/MySQL matrix when schema or transaction behavior changes.
- API tests must cover manageable account/Organization scope, repository-only rejection, local validation before provider I/O, duplicate-label and stale-revision `409` mapping, atomic audit behavior, and response-field isolation. Only `scoped_custom` entries may expose the selected scope's `template_id` and Organization `runner_group`; `platform_custom` template IDs stay private.
- Lifecycle tests must mutate both a scoped custom spec and a global spec after admission but before startup, then prove current disabled state or incompatible labels fail at `profile_validation` before GitHub registration or Sandbox creation.
- UI tests must cover Organization-only Runner Group input, exact global-label override confirmation, pending-save suppression, and stale manual-refresh or mutation completion after switching scope. An old response must not replace current items, close the current dialog, or trigger a refresh for the new scope.
- Local `task ui-production-smoke` currently covers the public pages and fixture-backed Jobs layout, not `/runner-specs` or the account/Organization custom Spec routes. Do not treat its four passing checks as Runner Specs browser acceptance; keep that fixture extension and real-browser scope acceptance explicit until they are implemented.

## Admin Template Validation

- Extend existing provider/server/UI tests for custom create and changed-template PATCH; use httptest with real SDK decoding, never a production validation bypass.
- Cover configured-but-disabled/selected admin defaults, missing credentials, 404, provider 401/403/429/5xx, cancellation/deadlines, no usable default build, public hidden history, and an older uploaded default during rebuild. Do not infer readiness from arbitrary tagged build history.
- Assert rejected writes preserve profile and audit state, including concurrent changes/deletes during provider validation and conflicts on insert-only creation, and unchanged-template edits still work without provider access. Assert UI pending state suppresses duplicate saves and failed validation retains the form for retry.
- For conditional profile persistence, run the audited mutation/fresh schema matrix on dedicated PostgreSQL/MySQL databases, including MySQL `clientFoundRows=true`. Cover duplicate inserts, unchanged values, and revision advancement at millisecond precision even when the clock moves backwards.
- Run `go test ./internal/state -count=1` when changing shared local profile validation, then server/provider tests, Bun tests, i18n, and production smoke for the paired public guide changes. No schema migration is needed for this flow.

## UI

- Edit source under `ui/`, not generated files under `internal/server/ui/`.
- For focused UI unit tests, run `cd ui && bun run test`.
- For UI source changes, run `task ui-lint` or `task build` depending on scope.
- Use `task build` when verifying production embedded UI behavior.
- Run `task ui-production-smoke` after changing UI dependencies, Vite/Rollup configuration, manual chunking, production asset loading, public guides, or the Jobs viewport/scroll layout. The smoke must execute the built bundle in Chromium, cover the public landing page plus responsive `/docs/getting-started/hosted` and `/docs/guides/custom-templates` routes, and use local authenticated fixtures to prove that the desktop Jobs list scrolls independently beside the Web Console while narrow layouts retain document flow.
- Use `RUNNERD_UI_SMOKE_PORT=<free-port> task ui-production-smoke` when port `4173` is occupied. The local preview supplies a signed-out landing-page auth response plus scoped authenticated Jobs API fixtures because it does not start `runnerd`. Use `RUNNERD_UI_SMOKE_BASE_URL=https://<runnerd-host> task ui-production-smoke` for a post-deploy public browser canary; the local fixture-backed Jobs regression is skipped and deployed canaries must not replace the real auth endpoint.
- Keep Rollup `CIRCULAR_CHUNK` and `CYCLIC_CROSS_CHUNK_REEXPORT` warnings fatal. Do not suppress or broadly allowlist them when changing manual chunk rules.
- Use the real ordinary-user entries `/`, `/docs`, `/docs/getting-started/hosted`, `/repositories`, `/account/repositories`, `/account/preferences`, `/account/sandbox-templates`, and `/account/sandbox-instances` when changing user UI. Also exercise the corresponding `/organizations/{login}/...` route when scope resolution changes.
- Use the real admin entries `/admin/`, `/admin/accounts`, and `/admin/sandbox_service`; do not assume the `ui/` tree is all admin-only.
- For account-role changes, verify global statistics, linked identity/avatar fallback, search, role filters, pagination, self-role protection, immediate authorization changes, and `account.role.update` audit events. Backend tests must also cover atomic audit rollback and concurrent demotions preserving at least one administrator.
- For Sandbox fallback changes, verify scoped override, enabled-default fallback, disabled/incomplete default rejection, catalog access, and config-source display without exposing endpoint/key or audience metadata to ordinary users.
- For audience changes, verify `all`, selected match/miss, selected-empty, user/org stable identity, login rename tolerance, manual preconfiguration before sign-in/sync, GitHub 404 rejection, installation-owner lookup/cache behavior, audit events, and saved snapshot behavior.

## UI Internationalization

Run this after changing fixed UI copy, locale resources, translation-key
construction, or language-aware formatting:

```bash
task ui-i18n-check
```

The task checks matching English/Chinese resource shapes, non-empty values,
array lengths, interpolation-variable parity, typed i18next keys, and a narrow
AST scan of JSX text, visible JSX attributes, and direct toast messages. Runtime
data such as logs, repository names, IDs, and raw server errors remains outside
the translation boundary. An exact technical literal may be allowlisted only
when it is intentionally language-neutral.

GitHub Actions runs the same command as an independent `i18n` job. Configure
repository branch protection or a ruleset separately if that job must be a
required merge check.

## Development Startup

For `task dev`, Vite proxy, or smee startup changes, prefer a real startup smoke using temporary ports if defaults are occupied:

```bash
RUNNERD_VITE_PORT=<free-port> RUNNERD_CONFIG=<local-config> task dev
curl -fsS http://127.0.0.1:<runnerd-port>/healthz
curl -I http://127.0.0.1:<runnerd-port>/admin/
```

Keep `SMEE_TARGET` aligned with the runnerd port when testing webhook forwarding.

## Docker, Templates, And Release

- Dockerfile-only validation: `task docker-check`.
- Local binary and embedded UI: `task build`.
- Production UI bundle execution: `task ui-production-smoke`.
- GoReleaser config: `task release-check`.
- Snapshot release behavior: `task release-snapshot`.
- Template changes may require the relevant `template-*` task.

## Deployment Smoke

Real deployment readiness still requires `docs/deployment-smoke.md` with a GitHub.com App, webhook delivery, a usable Qiniu sandbox template, runner pickup, cleanup, and diagnostics. Do not claim production readiness from local tests alone.
