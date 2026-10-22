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

`task state-online-migration-test` runs race-enabled inventory, protocol,
transactional capture, consistent artifact, resumable baseline, transactional replay
and same-position verification, handoff, rollback, startup/finalization and retention tests. Its PostgreSQL gate
uses `RUNNERD_CATALOG_BACKEND_TESTS=1` plus a dedicated
`RUNNERD_POSTGRES_TEST_DSN` ending in `_test`; default invocation skips it.
Migration tests must not use production DBs. Source capture remains opt-in and
requires explicit runtime configuration. Run the real PostgreSQL server gate with
`go test -race ./internal/server -run TestOnlineMigrationServerSQLBackends -count=1`
to preserve the same Server/worker/SSE/fake PTY across a roundtrip. This does not replace live provider acceptance. Snapshot creation uses an
independent WAL reader and `VACUUM INTO`, not an in-place source vacuum.
Keep cross-dialect microsecond normalization explicit. Verify IDs above 2^53,
NULL/false/zero values, composite keys, binary text ordering, resume identity,
concurrent consumers, streaming source availability and whole-group rollback
on row/checkpoint failures, disconnects or malformed/incomplete outbox data.

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

When changing Runner event pagination, preserve composite indexes for both mixed `(request_id, id)` pages and filtered `(request_id, event_type, id)` diagnostic tails. SQLite migration tests must assert those indexes are added to an existing `runner_events` table and that representative cursor queries avoid a temporary ORDER BY B-tree.

For Runner Spec name validation, cover `UpsertProfile` and API writes; policy migration must preserve historical names. Reject newly written names containing `/` or equal to `.` or `..` without committing profile or audit state, and prove historical rows with those names still survive startup migration, remain listable/readable/matchable, and are not automatically repaired.

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
  go test ./internal/state -run 'Test(ApplyMutationWithAudit|FreshSchema|ScopedRunnerCatalogFreshSchema|RunnerProfilePolicy)SQLBackends' -count=1 -v
```

Deploy startup regressions must keep queued plans untouched during `Server.Recover` and prove the independent worker survives cancellation of the startup context. Counter rollback/re-upgrade regressions must cover legacy append/deletion, repeated migration, preserved history/timestamps, and one lazy recount on first append after startup on SQLite, PostgreSQL and MySQL.

## Go Server Or API

Run `task ui-terminal-fault-test` after changing terminal SSE replay, browser
recovery deadlines or idle cleanup. Its opt-in `terminalfault` Go fixture and
Chromium exercise real local PTYs under measured 5/15/25-second transport
outages, preserving the actual 20-second client and 30-second idle deadlines.
See [Testing](../../docs/testing.md#timed-terminal-transport-faults) for
prerequisites, artifacts and boundaries; local fixtures do not replace live
provider, production proxy or authorization-revocation acceptance.

- For waiting Job handling (Issue #134), cover the complete queued webhook → REST waiting → waiting webhook → queued webhook sequence, delayed polling without a webhook, capped waiting and capacity/lease release, restart, cancellation, completion-state read/write failures releasing leases and creation capacity through normal failure handling, in-progress ownership, finite unknown/query-error retries with exactly-once terminal failure counters, coalesced waiting-metric scans with failed-refresh recovery and immediate terminal gauges, HTTP 429/secondary-rate-limit deferral beyond the retry budget with recovery after throttling clears, and concurrent queued/workflow_run/stale lookups. Targeted historical retry must reject real results, active/cleanup evidence, missing or stdout-only control evidence, invalid specs, nonqueued/assigned Jobs, failed lookups and concurrent changes. Run focused tests with race detection.

- For focused backend changes, start with the relevant package test.
- For broad server/API behavior, run `go test ./...`.
- For pre-merge confidence, run `task test`; it rebuilds UI assets, runs Bun UI tests, and runs Go tests with race and coverage.
- For ordinary-user Jobs authorization, cover shared installations with different repository access, exact installation/repository pair matching, filtering before the database limit, list/detail/group/log/terminal consistency, missing or rejected GitHub user tokens, inaccessible linked installations, and short-lived access-cache behavior.
- Include suspended Installation metadata with a repository endpoint that still returns HTTP 200 and repository membership. After authorization cache expiry, Jobs/Deploy lists must omit that scope and reads/actions/checkout must deny access; recovery must restore access without changing saved lifecycle state.
- For organization-sponsored forks, cover stable repository IDs and fork/source ownership, exact approvals, write permission, active organization membership, automatic-mode rejection for organization-owned forks, public-binding sponsorship resolution, sponsor capacity defer, revocation, fresh retry clearing, cleanup snapshot reuse, owner-only audited mutations, and personal/Organization UI route isolation. Keep Cache S3 and custom Specs on the fork's original scope.
- Keep regression coverage proving retired Runner Group/Policy models and APIs stay absent, fresh schemas omit their tables, and Runner Spec JSON omits `default_available`. No current behavior or recovery test may depend on legacy physical tables. Runner Spec/Sandbox mutations plus profile policy migration must commit their data change and audit evidence atomically; rejected mutations leave no audit event, while audit failures roll back the mutation.
- For custom-template Runner preflight updates, cover repository and Organization downloads endpoints, strict official URL/filename/version/architecture/SHA-256 validation, the ten-minute cloned cache, and concurrent lookup coalescing. Execute the rendered Sandbox script to prove a stale Runner is replaced before `config.sh`, the complete retrying download is wrapped in a hard five-minute deadline, matching and newer installed versions skip download tools without downgrade, checksum, unsupported-architecture, or missing-tool failures never register, and a catchable interruption after moving the old work directory restores it from the random update root without leaving a backup. A candidate-version mismatch must report both the observed and target versions. Lifecycle tests must prove only private-ID Specs receive descriptors and that lookup failure occurs at `github_runner_downloads` before registration-token or Sandbox creation. Public-name Specs must remain independent from the downloads endpoint.

## Scoped Runner Specs

- Run `go test ./internal/state -count=1` after changing scoped catalog uniqueness, matching, or concurrency. Preserve old SQLite upgrade tests and run the dedicated PostgreSQL/MySQL matrix when schema or transaction behavior changes.
- API tests must cover manageable account scope, owner-manageable Organization scope (`role == admin`), ordinary-member/repository-only/unknown-role rejection, redacted non-owner readiness responses, local validation before provider I/O, duplicate-label and stale-revision `409` mapping, atomic audit behavior, and response-field isolation. Only `scoped_custom` entries may expose the selected scope's `template_id` and Organization `runner_group`; unpublished platform entries must be absent; displayed public fixed-ID entries must retain runtime behavior and hide global IDs.
- Lifecycle tests must mutate both a scoped custom spec and a global spec after admission but before startup, then prove current disabled state or incompatible labels fail at `profile_validation` before GitHub registration or Sandbox creation.
- UI tests must cover Organization-only Runner Group input, exact global-label override confirmation, pending-save suppression, and stale manual-refresh or mutation completion after switching scope. An old response must not replace current items, close the current dialog, or trigger a refresh for the new scope.
- Local `task ui-production-smoke` covers public pages, fixture-backed Jobs, positive public-directory filtering at `/runner-specs`, editable former managed specs, long template references in a narrow dialog, and independent public/private/unavailable template visibility with fixed-ID publication. Admin card coverage includes full labels, independent template loading/retry, shared request reuse, refresh, narrow layouts and delete confirmation. Account/Organization custom routes still need separate browser scope acceptance; fixtures do not prove deployment or provider behavior.

- For GitHub Runner groups, cover owner-only lists, exact installation credentials, five-second and page/body bounds, stale organization responses, quoted group + labels YAML and copy behavior, missing/denied groups before provisioning, redacted transient errors, rejected mutation/audit invariance and unchanged-group/disable/clear compatibility. Include the local production Organization/account form smoke; live group pickup, workflow policy and cleanup remain deployment gates.

## Admin Template Validation

- Extend existing provider/server/UI tests for custom create and changed-template PATCH; use httptest with real SDK decoding, never a production validation bypass.
- Cover configured-but-disabled/selected admin defaults, missing credentials, 404, provider 401/403/429/5xx, cancellation/deadlines, no usable default build, public hidden history, and an older uploaded default during rebuild or failed rebuild, for both name and ID references. Verify that ready/uploaded status without an effective default still fails creation/startup, while publication-only changes validate publicness independently. Do not infer readiness from arbitrary tagged build history.
- Assert rejected writes preserve profile and audit state, including concurrent changes/deletes during provider validation and conflicts on insert-only creation, and unchanged-template edits still work without provider access. Assert UI pending state suppresses duplicate saves and failed validation retains the form for retry.
- For conditional profile persistence, run the audited mutation/fresh schema matrix on dedicated PostgreSQL/MySQL databases, including MySQL `clientFoundRows=true`. Cover duplicate inserts, unchanged values, and revision advancement at millisecond precision even when the clock moves backwards.
- Run `go test ./internal/state -count=1` when changing shared local profile validation, then server/provider tests, Bun tests, i18n, and production smoke for the paired public guide changes. No schema migration is needed for this flow.

## UI

- Apply [Frontend UI Consistency](frontend-ui-consistency.md) as a required acceptance check. Verify shared component reuse and compare changed appearance/interactions with the existing reference surface at desktop and narrow widths, including open popups. Functional tests alone do not establish visual acceptance; report any browser verification gap explicitly.
- Edit source under `ui/`, not generated files under `internal/server/ui/`.
- For focused UI unit tests, run `cd ui && bun run test`.
- For UI source changes, run `task ui-lint` or `task build` depending on scope.
- Use `task build` when verifying production embedded UI behavior.
- Run `task ui-production-smoke` after changing UI dependencies, Vite/Rollup configuration, manual chunking, production asset loading, public guides, or the Jobs viewport/scroll layout. The smoke must execute the built bundle in Chromium, cover the public landing page plus responsive `/docs/getting-started/hosted` and `/docs/guides/custom-templates` routes, and use local authenticated fixtures to prove that the desktop Jobs list scrolls independently beside the Web Console while narrow layouts retain document flow.
- Use `RUNNERD_UI_SMOKE_PORT=<free-port> task ui-production-smoke` when port `4173` is occupied. The local preview supplies a signed-out landing-page auth response plus scoped authenticated Jobs API fixtures because it does not start `runnerd`. Use `RUNNERD_UI_SMOKE_BASE_URL=https://<runnerd-host> task ui-production-smoke` for a post-deploy public browser canary; the local fixture-backed Jobs regression is skipped and deployed canaries must not replace the real auth endpoint.
- Keep Rollup `CIRCULAR_CHUNK` and `CYCLIC_CROSS_CHUNK_REEXPORT` warnings fatal. Do not suppress or broadly allowlist them when changing manual chunk rules.
- Use the real ordinary-user entries `/`, `/docs`, `/docs/getting-started/hosted`, `/repositories`, `/account/repositories`, `/account/preferences`, `/account/sandbox-templates`, and `/account/sandbox-instances` when changing user UI. Also exercise the corresponding `/organizations/{login}/...` route when scope resolution changes.
- Use the real admin entries `/admin/`, `/admin/accounts`, `/admin/github_accounts`, `/admin/github_accounts/{installation_id}`, and `/admin/sandbox_service`; do not assume the `ui/` tree is all admin-only.
- For GitHub App account administration, verify App-JWT installation pagination, installation-token repository pagination, admin rejection before provider I/O, positive numeric detail IDs, login-rename-safe routing, search and empty/error states, stale-response rejection, and omission of tokens, permissions, credentials, local observed repositories, and ordinary-user OAuth intersection data.
- For `/admin/runner_requests/{id}` changes, cover control-only diagnostic evidence separately from the mixed Run history API. Verify exclusive `before_id` history pagination, gap-free `after_id` catch-up across multiple pages, independent history availability, chronological merge and deduplication, individual control/stdout/stderr event rows without nested output cards or disclosures, stable Runner Hook/exit/cleanup stages, active-request refresh, stale-response rejection, and the absence of nested vertical scrolling. Lifecycle display tests must cover Started/Stopping/Completed/Failed, in-progress cleanup, completed requests that never entered cleanup, missing and Go-zero timestamps, inverted intervals, status-selected terminal timestamps, and the rule that `updated_at` is never a cleanup-duration fallback. Environment snapshot tests must cover additive old-SQLite migration, state round trips, retry/requeue clearing, per-region managed template resolution without spec mutation, partial and failed probes, same-attempt recovery, stale-attempt rejection, the five-second/256-byte allowlist, absence of unrelated environment or secret output, and `-` rendering for historical fields. Termination tests must cover additive old-SQLite migration, first-source preservation, nullable exit-code persistence, retry/requeue clearing, typed/stage evidence before historical message fallback, and successful completed cleanup suppressing the unobserved-termination finding. GitHub Job result tests must cover terminal persistence after runnerd completion, zero/mismatched/nonterminal rejection, additive old-SQLite migration, retained-first diagnostics without provider I/O, source/freshness display, and the live fallback for historical rows. Successful fallback lookups must use the short-lived diagnostics cache, and concurrent requests for the same Job must be coalesced.
- For Runner network diagnostics, test admin authentication, the exact three-target allowlist, rejection of arbitrary targets before provider I/O, running/Sandbox/PID preconditions, one concurrent call and 30-second per-attempt cooldown, 10-second operation deadline, fixed command/output/download bounds, typed DNS/IP/timing/status/exit parsing, raw stderr/provider-detail rejection, stable error categories, generic retained provider failures, stale-attempt rejection after the provider call, and the `network_diagnostic` event. UI tests must prove there is no free-form target, the control appears only for a running attempt, success and failure refresh the existing timeline, and terminal/historical requests remain compatible. Deployment smoke must use a real running Sandbox and separately verify non-admin, arbitrary-target, cooldown, event retention, and lifecycle independence.
- Process-exit watcher tests must cover a matching exit result that arrives after explicit cleanup, verify it adds the missing exit code and stable exit event without changing the first termination source or repeating cleanup, and reject a delayed callback whose Sandbox ID or PID belongs to an older attempt.
- For account-role changes, verify global statistics, linked identity/avatar fallback, search, role filters, pagination, self-role protection, immediate authorization changes, and `account.role.update` audit events. Backend tests must also cover atomic audit rollback and concurrent demotions preserving at least one administrator.
- For Sandbox fallback changes, verify scoped override, enabled-default fallback, disabled/incomplete default rejection, catalog access, and config-source display without exposing endpoint/key or audience metadata to ordinary users.
- For audience changes, verify `all`, selected match/miss, selected-empty, user/org stable identity, login rename tolerance, manual preconfiguration before sign-in/sync, GitHub 404 rejection, installation-owner lookup/cache behavior, audit events, and saved snapshot behavior.

## Shared Runner Event Timeline

- Verify Jobs, the standalone Job fallback, and admin request details through the shared timeline component. Cover mixed event rows, timestamps/stages, loaded-record filters, exclusive forward/backward pages, merging concurrent history and refresh requests, stale responses after switching Jobs, failure recovery, and the final event refresh when an active request becomes terminal.
- Test `/user/runner_requests/{id}/events` against the admin page contract, including the newest 200 records, exclusive cursors, `Cache-Control: no-store`, and anonymous/missing-token/rejected-token/exact installation-repository authorization failures. Keep diagnostics and management endpoints admin-only.
- Run `bun test src/components/runner-event-timeline.test.js src/components/admin-diagnostics-section.test.js` from `ui/` and the production browser smoke. Do not run a production UI rebuild concurrently with Go compilation: the build replaces files read by `go:embed`.

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

- Python/pipx setup changes require `task template-python-test` (Docker and
  network): preserve apt-owned dependencies, tool directories, ordinary-user
  execution and fail-closed installation. This is local regression evidence,
  not a real regional Sandbox template build or publication.

- Service Dockerfile-only validation: `task docker-check`; use
  `task template-check-all` for the public template Dockerfiles.
- Actions Runner pin updater changes require
  `task template-runner-update-test`, workflow syntax validation with
  `actionlint`, and `task template-check-all` under Node.js 24. Tests must
  execute the real CLI against fixture release/archive files and prove that
  draft/prerelease, downgrade, noncanonical/duplicate asset, malformed
  metadata, downloaded size/SHA-256 mismatch, and same-version no-op paths
  never corrupt the pin.
  A live API no-op may use a temporary authenticated `GITHUB_TOKEN`; never log
  the token. Workflow verification does not authorize a Sandbox build or
  publication.
- Local binary and embedded UI: `task build`.
- Production UI bundle execution: `task ui-production-smoke`.
- GoReleaser config: `task release-check`.
- Snapshot release behavior: `task release-snapshot`.
- Template changes may require the relevant `template-*` task.
- `task template-check-all` validates all eight public template configs across
  four source directories and runs
  an offline archive split/reassembly, checksum, reuse, and concurrent preparation
  test. Real template proof requires qshell `Status: ready`, or the same build
  ID reaching exact build status `ready` or `uploaded` during bounded
  reconciliation, plus the regional `template-smoke` gates. A reconciliation
  timeout may leave the remote build active; inspect that exact template/build
  pair before starting another rebuild. If exact status queries remain
  unavailable, retain the last qshell error instead of describing the build as
  active.
- Template builds require qshell 2.19.13 or newer. Standard and large TOML files
  set minimum root disk sizes of 20,480 and 81,920 MiB; qshell does not send
  the setting on same-name rebuilds. Adjust
  the provider team's `DiskMb` before rebuilding in place. Build must preserve
  the existing name and ID and may not reject its stale pre-rebuild total;
  publish and catalog checks use the request only as a lower bound, never as an
  exact expected total. Regional release smoke reads `disk_size_mb` from the
  selected TOML and requires the runtime root disk size to meet that lower
  bound; it remains required before promotion.
- `task template-build-all` runs all eight remote build targets sequentially
  and stops at the first failure. Use individual build targets for scoped
  rebuilds.

## Deployment Smoke

Real deployment readiness still requires `docs/deployment-smoke.md` with a GitHub.com App, webhook delivery, a usable Qiniu sandbox template, runner pickup, cleanup, and diagnostics. Do not claim production readiness from local tests alone.
- For the Issue #125 worker, run `go test ./internal/deployment/...` and `go test ./internal/state -count=1`; cover strict plan restoration, bounded/redacted output, Sandbox/PID/tag identity, attempt increments, queued-to-running transitions, expiry cleanup, and cleanup failure remaining visible. A local fake runtime proves lifecycle logic only; it does not prove provider creation or public ingress.
- Cover startup-only exact Vite Host injection, SDK RPCs without a remote startup deadline, locally bounded response/PID confirmation, expiry crossing provider I/O, and stale-observation rejection. `TestDeploymentLivePublicWorker` is an explicit opt-in public execution gate; use the parameters documented in `docs/testing.md` and distinguish controller-object reconnection from a full runnerd process restart.
- Keep the worker available without a default; verify scoped template choice, admission rejection before queuing, persisted template replay/retry, 26-character lowercase Base32 API IDs for keyed and unkeyed creation, account-scoped idempotency, deployd operation bounds, and template separation from Runner Specs. Before a real acceptance run, verify the effective account/Organization Sandbox credential, the dedicated template, fixed Commit checkout, restart reconnect, stop/expiry cleanup, and no command or credential leakage in API responses or retained evidence. Also cover the transactional per-scope quota (`429 deployment_quota_exceeded`), endpoint `endpoint_access: public` warning, and terminal retention pruning; a local fake runtime still does not prove provider creation or public ingress.

- Deployment Sandbox service changes must cover Jobs eligibility parity, scoped/inherited priority, enabled/complete defaults, audience match/miss/type isolation, personal-repository collaborator versus Installation-owner identity across discovery/create/retry/legacy resolution, corrupt config fail-closed behavior, and legacy records without snapshots. Verify platform-backed owner/member discovery and creation/redeploy remain public-only without private-catalog reads, scoped private-binding replay stays compatible, and saved runtime credentials survive platform disablement or audience removal.

- Deployment UI changes must cover separate list/create/detail routes, the `/deployments/new` creation link and reload, route-specific dependency loading, Branch/Tag discovery, delayed source/scope responses, empty lists, retry and paging, creation navigation to repository/PR-context `/deployments/{id}`, direct reload, delayed cross-scope template responses, cursor catch-up/history preservation, and visible log errors. List/detail/event tests must prove cross-account repository sharing, exact installation-repository isolation before pagination, shared Jobs cache reuse, missing/rejected OAuth denial, and anonymous rejection. Verify non-owner creation with public-only choices, owner-only private catalogs/settings, revision actor migration and per-user PTY isolation. API tests must cover qualified refs, same-name source idempotency conflicts, source-prefix length limits, and discovery rejection before upstream reads. Run the deployment Chromium production fixture as well as focused interaction tests; keep real API/browser evidence separate from fixtures.

- Deployments own numbered immutable revisions. Creation inserts #1; retry creates the next revision of the pinned plan in the audited transaction. Keep execution attempt fencing distinct, reject stale revision output, preserve old version state/logs, and budget retained events per revision. Revision reads share Jobs' exact installation/repository cache and keep content local. Store each revision's execution account separately from its immutable creator, and revalidate that actor before checkout. Preserve legacy rows/events through additive upgrade and import missing history as #1 without fabricating old status. Verify SQLite upgrades, real PostgreSQL/MySQL gates, cursor isolation, URL selection and stale UI responses.

- Deployment terminal coverage must exercise account/repository/organization authorization, current revision/attempt/Sandbox binding, input/resize/output/close, stop/expiry cleanup and remaining-lifetime Connect. Browser fixtures cover manual connection, typing, tab cleanup and historical/stopped unavailability; they do not replace real Sandbox PTY acceptance.

- GitHub context routes and Job deployment links must cover repository collections, readable Branch/Tag query slashes and reserved characters, empty/scoped Jobs filters, OAuth return, back/forward, deployment path IDs, retired deployment-link rejection, legacy Jobs canonicalization, selected historical Job SHA, PR/branch prefill through delayed account catalogs, closed PR selection, source changes clearing the pin, and rejected unavailable commits. The production browser fixture must navigate a historical PR Job to creation and reload the resulting deployment; real fork execution remains separate acceptance.

- Deployment creation idempotency compares an immutable hash of normalized admission fields. Never reconstruct identity from mutable provider expiry or the current revision. Preserve the hash across retry and cleanup; historical empty hashes reject keyed replay. Cover concurrent admission, expiry precision changes, terminal replay, changed-input conflicts and revision changes.

- The opt-in live public API CI gate must bind a single-job Run and exact Run attempt to the deployment Commit, observe concurrent running Sandboxes, confirm retained CI success and Runner provider absence, and recheck the same deployment after one reconciliation interval. Missing overlap is not acceptance; the test never dispatches workflows or mutates Runner requests.

- The live public API gate may use a designated public fork or PR. Bind admission to the selected repository owner installation and account/Organization quota scope; when pinning an older PR Commit, check the full SHA, saved fork identity and actual HTML marker after the branch head advances. Keep live revocation/private/second-user gates separate.

- Deployment scheduler changes must cover occupied provisioning slots alongside independent monitoring and expiry cleanup, capacity bounds, fair keyset continuation, no duplicate in-flight identity, one probe per monitoring cycle, and cancellation/join of blocked work. Event-counter changes require old SQLite column upgrade with history/timestamp preservation, rollback on counter persistence failure, per-revision isolation, concurrent appends and byte-budget coverage on PostgreSQL/MySQL as well as SQLite.
