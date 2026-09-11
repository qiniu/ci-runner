# Local Testing And GitHub Setup

[Chinese](zh/testing.md)

This guide explains how to test the service with a local Qiniu sandbox environment and how to configure a GitHub repository so self-hosted runners are started automatically.

## 1. Local Config File

The service reads `./runnerd.yaml` by default. You can also pass another path with `--config`:

```bash
cp runnerd.yaml.example runnerd.yaml
mkdir -p ./secrets
```

Relative sqlite `database.dsn` and `github.app.private_key_file` paths are resolved from the directory containing `runnerd.yaml`. Legacy `database.url` is still accepted as a deprecated alias when `database.dsn` is empty. Only GitHub.com is currently supported; GitHub Enterprise Server is not supported. GitHub auth can use GitHub App, PAT token, or basic auth, but exactly one mode must be configured.

Minimal usable config example:

```yaml
server:
  http_addr: ":25500"

database:
  backend: sqlite
  dsn: ./var/runnerd.db

auth:
  session_secret: <random session signing secret>
  encryption_key: <separate random encryption key>
  session_ttl_hours: 12

sandbox:
  regions:
    - id: us-south-1
      label: "United States · Dallas 1"
      sandbox_api_url: https://us-south-1-sandbox.qiniuapi.com
      # Optional: configure both fields to enable Cache S3 in this region.
      s3_region: us-north-1
      s3_endpoint: https://internal-s3-las-us-north-1-dal.qiniucs.com

github:
  webhook_secret: <random webhook secret>
  app:
    id: <github app id>
    # Optional. When omitted, runnerd resolves the installation dynamically
    # from the repository in the webhook payload.
    # installation_id: <installation id>
    private_key_file: ./secrets/github-app.pem
  oauth:
    client_id: <github app client id>
    client_secret: <github app client secret>
    redirect_url: http://127.0.0.1:25500/auth/github/callback
  # Optional. Empty means all installed App repositories that match runner
  # policies/specs are allowed.
  # allowed_repositories:
  #   - <repo owner>/*
  #   - <repo owner>/<repo name>

worker:
  max_concurrent_runners: 100
  recovery_timeout_seconds: 120
  lease_ttl_seconds: 300
  retry_base_delay_seconds: 15
  retry_max_delay_seconds: 300
  retry_max_attempts: 5
```

To hide sensitive values from direct display, build runnerd and pipe each value through `./bin/runnerd --obfuscate-config-value`; paste the resulting `RUNNERD_ENC(v1:...)` value into the YAML. Plaintext values remain compatible. Supported fields are `database.dsn`/`database.url`, `auth.session_secret`, `auth.encryption_key`, `github.webhook_secret`, `github.token`, `github.basic_auth.password`, and `github.oauth.client_secret`. Their runtime wrapper masks accidental text, structured-log, JSON, and YAML output. This is obfuscation only: the decoding key is part of runnerd, so a host user able to inspect or execute the binary can recover the value.

Sandbox service API URL and API Key are not configured in `runnerd.yaml`. After signing in, `/repositories` shows the effective Sandbox source for the selected account or organization but never embeds the credential editor. When configuration is missing, a manageable scope links to `/account/preferences` or `/organizations/{login}/preferences`; Settings remains the single write surface. For organization installations, visibility can come from repository-only access, while Sandbox management requires an active GitHub organization membership. Settings lists only the signed-in account and manageable organizations. Outside collaborators receive read-only readiness and cannot mutate the organization scope or read its Sandbox templates and runner-created instances. The disabled-by-default platform fallback remains admin-managed at `/admin/sandbox_service`. Its audience is either `all` or `selected`; selected entries match the repository owner's stable GitHub account ID and type. The API Key is encrypted with `auth.encryption_key`. Resolution order is a saved runner-request snapshot, installation custom/inherited settings, an eligible personal account, the enabled and audience-eligible admin default, then a not-configured error.

The first-use product tour stores only a version, status, and `tour_seen` marker in the existing account-scoped `account_preferences` table under `onboarding/product-tour`; it never stores the Sandbox API Key. A missing or outdated version is returned as `pending` with `tour_seen=false`. Finishing the overlay writes `pending` with `tour_seen=true`, so it does not auto-start again while required setup remains visible. `completed` is written when the signed-in account resolves any effective Sandbox source: custom, inherited, or an eligible admin default. An explicit first-run skip writes `skipped` to dismiss the overlay without hiding required setup. Replaying the tour from the account menu does not reset or replace the persisted state.

Runner Specs are not `runnerd.yaml` fields. Internal Runner Groups and Repository Policies have been removed; the optional `runner_group` on a Spec remains a GitHub Organization Runner Group registration target.
At startup, runnerd reconciles five managed Qiniu Ubuntu specs. It owns their
labels, required labels, stable public template names, priority, and default
availability while preserving operator-controlled `enabled`,
`max_concurrency`, and `min_idle`. Custom specs remain admin API/UI data: give
them an explicit `template_id`, advertised labels, and optional required
labels. New custom specs and changed template IDs require an admin Sandbox
endpoint and API key configured at `/admin/sandbox_service`. Validation uses
that configuration only, not the signed-in user's or an organization's keys;
the runtime fallback enabled/audience controls do not restrict admin validation.
`GetTemplate` verifies existence/access, then the owned catalog or public default
catalog supplies the effective uploaded default build ID. A failed/in-progress
rebuild does not invalidate an older usable default. Detail build history is not
a readiness signal: it is paginated, includes other tags, and is hidden from
non-owners. Public templates outside the default catalog cannot have their build
state confirmed by this API and are rejected with `template_state_unavailable`.

The five `-large` workflow labels are intentionally operator-configured public
default specs rather than managed catalog entries. Configure and enable their
specs in Admin separately,
then verify their explicit template IDs and label contract in the custom-spec
checks; they are not expected in the managed-spec reconciliation list or the
public managed-template API.

The total provider check is limited to five seconds. Missing admin configuration
returns `409 sandbox_service_not_configured`; a missing template or no usable
default build returns `400 template_not_found` or `400 template_not_ready`.
Provider 401/403 produces `502 sandbox_template_access_denied`, other upstream
failures produce `502 template_validation_unavailable`, and cancellation/deadline
returns `504 template_validation_timeout`. Fix the configuration/template or retry;
a failed check never silently permits a save and never exposes the provider body.
Rejected saves leave both profile and audit records unchanged. If another save or deletion changes the spec while validation is in flight, the conditional write returns `409 runner_spec_conflict`; refresh before retrying instead of overwriting the newer state.

PATCH compares trimmed template IDs: changing only labels, capacity, or enabled
state remains possible without Sandbox access. Managed spec controls also skip
validation and retain runtime name resolution. No existing spec is automatically
revalidated or disabled. Actual jobs still use their account/organization Sandbox
configuration; admin validation does not grant access in those scopes or prove
that the image contains the runner binaries. Verify the real workflow separately.

`database.backend` supports `sqlite`, `postgres`, and `mysql`. Prefer sqlite for local development. Before documenting shared-database multi-instance deployment as supported, verify lease behavior with two runnerd processes sharing the same database.

New accepted and rejected runner requests extract workflow context, branch, SHA, Job URL, and PR number from the webhook in memory, then leave `github_payload_json` empty. Installation and Job IDs come from the `RunnerRequest` fields; those fields, repository, labels, and the parsed webhook context remain in structured columns for runtime use and display. The state tests cover `workflow_job` and `workflow_run` metadata after reopening the database, rejected-request persistence, and preservation of historical payloads during repeated migration and installation-ID repair. Keep the legacy column, existing values, and backfill logic; historical payload cleanup and disk-space reclamation require a separate maintenance operation. Request and log retention are unchanged.

The state schema is mainly defined by GORM tags in `internal/state/records.go`. On startup, existing SQLite `runner_requests` and `runner_profiles` tables are migrated additively by creating every missing model column and index; they are deliberately excluded from generic SQLite `AutoMigrate` table recreation. This keeps historical ALTER-added runner-request values and preserves legacy runner-profile rows and custom indexes while adding managed-catalog fields. Admin newest-first lists depend on `idx_runner_requests_queued_id` over `(queued_at DESC, id ASC)`. Repository-authorized lists query each installation separately through `idx_runner_requests_github_installation_queued_id` over `(github_installation_id, queued_at DESC, id ASC)`, then merge the bounded results. Creating a missing index does not rewrite rows, but benchmark startup I/O and lock time on a disposable production-sized copy. A future non-additive change to either additive-only table requires a narrow explicit migration and a preserved-data regression fixture. Other tables run through a narrow legacy compatibility pass for older columns, obsolete OAuth constraints, and incompatible legacy scope tables, then run GORM `AutoMigrate`. Legacy `account_preferences` and `account_secrets` tables without `scope_type`/`scope_id` are dropped and recreated rather than data-migrated. Reconfigure their saved Sandbox Preferences and API keys after that upgrade; stored GitHub OAuth tokens are also cleared, so affected users must sign in with GitHub again before syncing installations. When changing state records, indexes, or migration helpers, run at least:

```bash
go test ./internal/state -count=1
```

Do not validate migrations only with a fresh sqlite file. Old-schema upgrade paths also need coverage, especially when adding `NOT NULL` columns, unique indexes, or relationship constraints.

Verify that SQLite can satisfy the newest-page order from the index without a temporary sort:

```bash
sqlite3 ./var/runnerd.db \
  "EXPLAIN QUERY PLAN SELECT id, queued_at FROM runner_requests ORDER BY queued_at DESC, id ASC LIMIT 100;"

sqlite3 ./var/runnerd.db \
  "EXPLAIN QUERY PLAN SELECT id, queued_at FROM runner_requests WHERE github_installation_id = 123 AND LOWER(repository_full_name) IN ('owner/repo') ORDER BY queued_at DESC, id ASC LIMIT 100;"
```

Expected result: the first plan uses `idx_runner_requests_queued_id`, the second uses `idx_runner_requests_github_installation_queued_id`, and neither contains `USE TEMP B-TREE FOR ORDER BY`. Verify each installation predicate separately; combining installations with `OR` can reintroduce a temporary sort.

For a production SQLite snapshot, record data-integrity counts before and after starting the candidate binary against a disposable copy:

```bash
sqlite3 runnerd-export.db \
  "SELECT COUNT(*), SUM(CASE WHEN github_installation_id > 0 THEN 1 ELSE 0 END), SUM(CASE WHEN sandbox_api_url <> '' THEN 1 ELSE 0 END), SUM(CASE WHEN sandbox_api_key_encrypted <> '' THEN 1 ELSE 0 END), SUM(CASE WHEN sandbox_config_source <> '' THEN 1 ELSE 0 END) FROM runner_requests;"
```

Run the migration twice. The total and all populated-field counts must remain unchanged on both starts, except that missing `github_installation_id` values may increase when `github_payload_json.installation.id` can repair them.

The repository includes an opt-in state-only snapshot test that copies the source database before migration and does not start runner recovery:

```bash
RUNNERD_SQLITE_SNAPSHOT=/path/to/runnerd-export.db \
  go test ./internal/state -run TestMigrateSQLiteRunnerRequestSnapshot -count=1 -v
```

State migration and audited catalog mutations also have an opt-in real-dialect
compatibility gate. Both DSNs must point to
dedicated disposable databases whose names end in `_test`: the tests refuse
other database names, then drop and recreate runnerd state tables. They cover
fresh schema creation without retired catalog tables, repeated migration, and
atomic mutation/audit commit and rollback behavior.

```bash
RUNNERD_CATALOG_BACKEND_TESTS=1 \
RUNNERD_POSTGRES_TEST_DSN='host=127.0.0.1 user=runnerd password=runnerd dbname=runnerd_test port=5432 sslmode=disable' \
RUNNERD_MYSQL_TEST_DSN='runnerd:runnerd@tcp(127.0.0.1:3306)/runnerd_test' \
  go test ./internal/state -run 'Test(ApplyMutationWithAudit|FreshSchema|ScopedRunnerCatalogFreshSchema)SQLBackends' -count=1 -v
```

Restart recovery has focused tests that do not require a live sandbox:

```bash
go test -tags development ./cmd/runnerd -run TestRecoveryGateAllowsOnlyHealthUntilReady -count=1
go test -tags development ./internal/server -run TestRecover -count=1
go test ./internal/sandboxrunner -count=1
```

The startup-gate test must verify that only `/healthz` remains available before recovery finishes. The `TestRecover*` cases must verify that at most four requests recover concurrently, each worker derives its per-request timeout from the remaining whole-startup budget and remaining worker waves, an exhausted parent budget prevents dispatch, cancellation reports every skipped request, queued requests have stale leases cleared, creating/running requests reconnect without stopping their sandbox, a concurrent state version change wins over both successful and failed reconnect results, timed-out sandboxes stop without reconnecting, missing interrupted creations are requeued while a missing creation whose GitHub job is already in progress fails explicitly, completed workflow jobs continue through cleanup, and one reconnect failure does not prevent other requests from being recovered.

## 2. Configure GitHub Auth

GitHub App is recommended. PAT token and basic auth are also supported, mainly for local verification or existing credential scenarios.

Configure the [required GitHub App permissions](../README.md#required-permissions) before continuing. The steps below cover local setup details.

Suggested setup:

1. Open GitHub `Settings -> Developer settings -> GitHub Apps -> New GitHub App`.
2. Basic information:
   - GitHub App name: for example `runnerd-local`
   - Homepage URL: use the repository URL or local project docs URL
   - Setup URL: runnerd's `/github-app/setup`, for example `http://127.0.0.1:25500/github-app/setup`
   - Webhook: if runnerd receives repository webhooks itself, you can leave the App webhook disabled; this is different from the `workflow_job` webhook
3. Under `Permissions`, apply the settings in the [required permissions table](../README.md#required-permissions).
4. Where can this GitHub App be installed:
   - For local verification, usually choose `Only on this account`
5. After creating the App, generate a private key on the App page, download the `.pem`, and save it locally, for example `./secrets/github-app.pem`
6. Install the App on the target repository or organization:
   - Click `Install App`
   - Choose the target owner
   - Choose the repositories to authorize
   - If `Members: Read-only` was added to an existing App, each installation owner must approve the permission update before organization Settings become manageable
7. Record:
   - App ID
   - App slug, the short name in the App URL such as `https://github.com/apps/<slug>`
   - Installation ID, optional; when omitted, runnerd resolves it dynamically by repository
   - private key file path

After an installation owner approves `Members: Read-only`, reload `/account/preferences`. An organization where the signed-in user has an active membership should appear in the Settings scope list and open `/organizations/{login}/preferences`. Repository-only collaborators can still see authorized repositories under `/repositories`, but the organization must remain absent from Settings. If an eligible organization is still missing after approval, sign out and complete GitHub OAuth again before retrying.

Put the values into `runnerd.yaml`:

```yaml
github:
  app:
    id: <app id>
    slug: <app slug>
    # installation_id: <installation id>
    private_key_file: ./secrets/github-app.pem
```

PAT example:

```yaml
github:
  webhook_secret: <random webhook secret>
  token: <github token>
```

Basic auth example:

```yaml
github:
  webhook_secret: <random webhook secret>
  basic_auth:
    username: <github username>
    password: <token or password>
```

No global repo/org mode is required. Webhooks use `repository.full_name` from the payload. runnerd creates repository runners by default. If the matched runner spec sets GitHub `runner_group`, runnerd creates an organization runner for the repository owner and passes the group as `--runnergroup` during GitHub runner registration. Admission selects from all enabled Runner Specs after the repository allowlist check; the removed internal Runner Groups and Repository Policies do not affect matching.

## 3. Start The Service

For UI or backend development, prefer development mode:

```bash
task deps
task ui-deps
cp runnerd.yaml.example runnerd.local.yaml
task dev
```

`task dev` reads `runnerd.local.yaml` by default, starts the Vite dev server on the first available port at or after `127.0.0.1:5173`, and starts the Go service with the `development` build tag. Browsers still access the runnerd address.

Public landing page:

```text
http://127.0.0.1:25500/
```

Protected ordinary-user Jobs homepage:

```text
http://127.0.0.1:25500/jobs
```

Ordinary-user repository access and Runner readiness page:

```text
http://127.0.0.1:25500/repositories
```

Compatible personal repository deep link, rendered by the same readiness page:

```text
http://127.0.0.1:25500/account/repositories
```

Ordinary-user personal Preferences page:

```text
http://127.0.0.1:25500/account/preferences
```

Ordinary-user personal Sandbox catalogs:

```text
http://127.0.0.1:25500/account/sandbox-templates
http://127.0.0.1:25500/account/sandbox-instances
```

Admin UI:

```text
http://127.0.0.1:25500/admin/
```

To use another config file:

```bash
RUNNERD_CONFIG=./runnerd.yaml task dev
```

To pin the Vite port:

```bash
RUNNERD_VITE_PORT=5173 task dev
```

For production mode or embedded UI asset verification, rebuild the UI and binary before starting runnerd:

```bash
task build
./bin/runnerd --config ./runnerd.yaml
```

Health check:

```bash
curl -fsS http://127.0.0.1:25500/healthz
```

Protected ordinary-user Jobs page:

```text
http://127.0.0.1:25500/jobs
```

The page presents GitHub OAuth sign-in and returns to `/jobs` after authentication. The first login creates a local account with `role=user` and links the GitHub OAuth identity. The first admin must be bootstrapped as a separate one-time step before starting the server; the command sets the admin role and exits without starting runnerd:

```bash
go run ./cmd/runnerd --config ./runnerd.yaml --bootstrap-admin github:<your-github-user-id>
```

`<your-github-user-id>` is the stable numeric `id` returned by GitHub `/user`, not the mutable login. Role belongs to the local account. OAuth identity stores provider, stable subject, and login display metadata, so other provider identities can later be linked to the same account. After ordinary users sign in, `/repositories` is the canonical readiness surface: it installs or syncs the configured GitHub App, loads the authorized repository intersection, shows local job activity, and resolves the selected account or organization's effective Sandbox source. If no custom, inherited, or eligible admin-default source exists, a manageable scope shows **Configure Sandbox** linking to the exact account or organization Preferences route; the credential form remains in Settings. Sandbox Templates and Sandbox Instances remain separate resource tabs for the signed-in account and manageable organizations only. When GitHub returns an `installation_id`, runnerd records the GitHub App installation linked to that account. Jobs visible to an ordinary user are filtered by exact `(installation_id, repository_full_name)` pairs loaded with the stored GitHub App user access token. This is the intersection of the user's repository access and each linked App installation's repository scope, and it protects lists, details, groups, logs, and terminal operations. runnerd does not copy the full repository authorization list into local state; successful lookups are cached in memory with a 30-second hard expiry. After 20 seconds, the next hit returns the still-valid intersection immediately and starts one background refresh. Shared refresh work uses a server-scoped timeout rather than the first caller's cancellation. Installation or OAuth changes advance an account cache epoch, so refreshes from an older epoch cannot refill the cache or serve a later request. A rejected token invalidates the cache; a transient refresh error may retry but never extends authorization past the original 30-second expiry. The readiness page loads the same intersection on demand. Missing or rejected user tokens fail closed and require the user to sign in with GitHub again. A linked installation that GitHub reports as inaccessible is skipped so it cannot expose jobs from that installation and does not prevent other accessible installations from loading. The catalog endpoints require an ordinary-user session and, for installation scope, the same active-organization-membership check used for credential mutation. They use the account or selected installation's encrypted credentials, map supported region ids to server-owned endpoints, and never expose the credentials. After an admin signs in, the browser stores a signed HttpOnly session cookie and automatically sends it to management APIs such as `/runner_requests`. For `curl` examples, export a cookie jar from the browser or OAuth debug flow:

```bash
export COOKIE_JAR=./runnerd.cookies
```

Admin account page:

```text
http://127.0.0.1:25500/admin/accounts
```

The summary cards report total accounts, administrators, users, and linked OAuth identities. These statistics are global and do not change with search, role filtering, or pagination. The account list searches linked OAuth login, provider, and stable subject values; `role` filters to `admin` or `user`, while `limit` and `offset` control pagination. The default page size is 20 and the maximum is 100. Linked GitHub identities load the conventional GitHub avatar URL derived from their login and fall back to account initials if it is unavailable. The page only changes another account's role between `admin` and `user`; accounts remain OAuth/bootstrap-created, and it cannot create or delete accounts or link or unlink identities. Role changes take effect immediately and create an `account.role.update` audit event. Self-role changes and changes that could leave no administrator are rejected, including concurrent demotions that race with each other.

```bash
curl -fsS -b "$COOKIE_JAR" \
  'http://127.0.0.1:25500/admin/api/accounts?q=octo&role=admin&limit=20&offset=0' | jq
curl -fsS -X PATCH -b "$COOKIE_JAR" -H 'content-type: application/json' \
  http://127.0.0.1:25500/admin/api/accounts/<account-id>/role \
  -d '{"role":"admin"}' | jq
```

Admins manage the platform fallback through explicit role-gated APIs. Omitting `api_key` preserves the saved encrypted key; omitting `audience_mode` preserves the current mode; the response never returns the key. `selected` with no audience entries matches nobody. Audience additions accept `login` or `@login`; runnerd queries GitHub for the canonical login, stable numeric ID, and user/organization type before saving. Existing synchronized or cached owners are optional suggestions, not a prerequisite. When the first workflow for a selected owner has no local installation row, GitHub App auth resolves the installation owner and runnerd caches that stable identity.

```bash
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/admin/api/sandbox-service-default | jq
curl -fsS -X PUT -b "$COOKIE_JAR" -H 'content-type: application/json' \
  http://127.0.0.1:25500/admin/api/sandbox-service-default \
  -d '{"enabled":true,"audience_mode":"selected","api_url":"https://us-south-1-sandbox.qiniuapi.com","api_key":"<sandbox-api-key>"}' | jq
curl -fsS -X POST -b "$COOKIE_JAR" -H 'content-type: application/json' \
  http://127.0.0.1:25500/admin/api/sandbox-service-default/audiences \
  -d '{"account_login":"octo-org"}' | jq
curl -fsS -X DELETE -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/admin/api/sandbox-service-default/audiences/<audience-id> | jq
curl -fsS -X DELETE -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/admin/api/sandbox-service-default/api-key | jq
```

UI source lives in `ui/` and uses React, Vite, Tailwind CSS, shadcn-style components, and the repository theme CSS. `task build` runs `task ui-build`, writes frontend output to `internal/server/ui/`, and then compiles `runnerd`. In development mode, `internal/server/ui_assets_development.go` proxies UI assets to Vite. In production builds, `internal/server/ui_assets_production.go` embeds `internal/server/ui/*`. `/` always renders the public product landing page with documentation and Jobs destinations; it does not load protected user resources. The protected ordinary-user Jobs homepage is `/jobs`. Opening `/jobs`, a job-group deep link, account settings, or an admin route without a session renders a focused sign-in page whose OAuth link preserves the full same-origin destination through `return_to`. Unknown routes render 404, and authenticated non-admin users receive an explicit access-denied page on admin routes. `/repositories` is the canonical ordinary-user readiness page for GitHub App installation/sync, the user/App authorized repository intersection, local job activity, and effective Sandbox service status. `/account/repositories` and `/organizations/{login}/repositories` remain compatible scoped deep links rendered by the same page. Account and organization Preferences are the only ordinary-user Sandbox credential editors; readiness links there only when a manageable scope is missing configuration. Settings retains Sandbox Service, Sandbox Templates, and Sandbox Instances resource management for the signed-in account and manageable organizations only. Initial navigation loads only resources used by that route. Authenticated user routes load `GET /user/onboarding/product-tour` once for account-level tour state, but a failed onboarding request is ignored so it cannot block core workspace data and it is not part of polling. Only a pending account whose tour has not been seen and that lands on the exact `/jobs` homepage auto-starts the six-step tour; deep links do not interrupt the user. The final steps navigate to `/repositories`; ready sources require no action, while a missing manageable source points the user to Settings. The account-menu replay works for seen, completed, and skipped accounts without changing stored state. The Jobs homepage loads the first `GET /user/runner_requests?limit=100&offset=0` page and polls that page every five seconds while preserving any already-loaded history. Stable job-group routes and the Load older jobs action can load the bounded 500-row history window; the API rejects `limit + offset` values past 500 and does not advertise an unusable next link. GitHub App metadata, preferences, and onboarding state are not part of the polling loop. Admin routes load only the active section's request/spec/audit dependencies. Overview and the Runner Requests collection poll the request list; an active request resource polls its own diagnostics every five seconds and ignores stale responses after navigation. The public managed catalog uses unauthenticated `GET /api/public/runner-templates` and must expose only stable runnerd-owned names and workflow labels. Provider catalogs use `GET /user/sandbox/templates?region=<id>` and `GET /user/sandbox/instances?region=<id>&template_id=<id>`; the instance endpoint lists only runner-created sandboxes and uses the effective scoped/default credential resolver. Installation-scoped catalog reads require a manageable organization scope. Tests must prove signed-out and signed-in public responses are identical, exclude provider/scoped metadata, and keep public and provider loading, retry, failure recovery, and stale-response handling independent. The admin surface includes Overview, the account list and role controls at `/admin/accounts`, Runner Requests with retry/stop controls and a unified event timeline, Runner Specs, the platform fallback at `/admin/sandbox_service`, audit, label match test, and runtime diagnostics, but not retired internal Runner Group/Policy management or provider resource catalogs.

The protected `/runner-specs` route is the scope-free, read-only ordinary-user platform catalog. It shows only enabled platform Specs with copyable workflow labels; source/status badges, disabled Specs, account/Organization selectors, and enabled or concurrency controls stay out of this page. Settings routes `/account/runner-specs` and `/organizations/{login}/runner-specs` show only custom Specs owned by that scope. All three surfaces use `/user/runner-specs`, which requires the signed-in account or a manageable Organization scope; scoped custom templates are validated with that scope's Sandbox credentials and never the admin fallback.

Runner Spec regression coverage must keep the three catalog sources distinct:
`managed` entries expose stable public template names and global policy as
read-only, `platform_custom` entries are read-only and omit their private
`template_id`, and `scoped_custom` entries expose only the current scope's own
`template_id` plus the optional Organization-only `runner_group`. Exact
normalized scoped labels shadow the same global label set even when the scoped
spec is disabled. Create and changed-template validation happens before the
audited transaction, while unsafe names and invalid local fields are rejected
before any provider call. Duplicate normalized labels, stale revisions, and
in-use edits return stable `409` errors without partial audit state. Lifecycle
tests must also disable a scoped custom spec and a global spec after admission
but before startup, then prove that neither starts a Sandbox. UI
tests must resolve an older manual refresh or mutation after switching
Organization scope and prove it cannot overwrite or submit against the new
scope.

For focused UI unit tests, run:

```bash
cd ui && bun run test
```

After changing fixed UI copy, locale resources, translation-key construction,
or locale formatting, run:

```bash
task ui-i18n-check
```

This checks that the English and Chinese resource trees have matching types and
array lengths, contain no empty values, and use the same interpolation
variables. It also type-checks i18next keys and scans JSX text, selected visible
attributes, and direct toast calls for untranslated fixed literals. Runtime
logs, repository names, IDs, and raw server errors remain untranslated; exact
language-neutral technical literals can be intentionally allowlisted. GitHub
Actions runs the same gate in an independent `i18n` job. The job is
merge-blocking only when repository branch protection or a ruleset requires it.

After changing UI dependencies, Vite/Rollup configuration, manual chunking,
production asset loading, or the Jobs viewport/scroll layout, execute the built
bundle in Chromium:

```bash
task ui-production-smoke
```

The task installs the matching Chromium runtime, builds `internal/server/ui/`,
and starts Vite preview on port `4173`. Playwright opens `/` to catch JavaScript
or console errors, failed script/stylesheet requests, an empty `#root`, or a
missing landing-page heading. The local preview also opens `/jobs` with scoped
authenticated API fixtures and proves that a long desktop Jobs list scrolls
without moving the document or Web Console, while a narrow viewport preserves
normal document flow. If port `4173` is occupied, set
`RUNNERD_UI_SMOKE_PORT=<free-port>`. The local preview does not start `runnerd`,
so these responses are test fixtures. Set
`RUNNERD_UI_SMOKE_BASE_URL=https://<runnerd-host>` to test a deployed origin
with its real auth endpoint; that deployed public canary skips the local
fixture-backed Jobs regression. This production smoke is a dedicated GitHub
Actions job because a successful Vite build does not prove that generated
chunks execute in a browser.

The Vite build also promotes Rollup `CIRCULAR_CHUNK` and
`CYCLIC_CROSS_CHUNK_REEXPORT` warnings to errors. Do not suppress or broadly
allowlist these warnings: they indicate that manual chunking can produce a
browser-unsafe module execution order.

`task test` rebuilds the UI, runs the same Bun suite, and then runs Go tests with race detection and coverage. The Bun suite covers helpers and server-rendered component output; use a real browser to verify navigation, dialogs, avatar loading/fallback, and access transitions after a role change. For onboarding changes, also verify the automatic `/jobs` start, all six targets, the transition to `/account/preferences`, the persistent setup task after the overlay closes, explicit skip persistence, and replay without a state change.

Confirm the reconciled managed specs:

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_specs |
  jq '.[] | select(.managed_by == "qiniu/ci-runner") |
      {name, required_labels, default_template_name, enabled}'
```

The result should contain exactly `qiniu-ubuntu-slim`, `qiniu-ubuntu-22.04`,
`qiniu-ubuntu-24.04`, `qiniu-ubuntu-26.04`, and `qiniu-ubuntu-latest`. Verify
the five `-large` labels through separately configured custom specs when testing
the backward-compatible explicit-template path:

```bash
curl -fsS -X POST http://127.0.0.1:25500/runner_specs \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"name":"custom-ubuntu","labels":["self-hosted","custom-ubuntu"],"required_labels":["custom-ubuntu"],"template_id":"<template id>","max_concurrency":1,"enabled":true}' | jq
```

Runner Spec names are trimmed single-path-segment identifiers. New names must
not contain `/` and must not equal `.` or `..`; rejected creates return
`400 Bad Request` without committing the profile or its audit event. Startup
and matching continue to tolerate historical rows with those names, but
runnerd does not rename or delete them automatically and their single-resource
management URLs are not guaranteed to survive reverse-proxy path normalization.

Manually create a runner:

```bash
curl -fsS -X POST http://127.0.0.1:25500/runner_requests \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"id":"manual-001","repository_full_name":"<owner>/<repo>","runner_spec_name":"qiniu-ubuntu-24.04"}' | jq
```

Check state:

```bash
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/runner_requests | jq
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/runner_requests/manual-001 | jq
```

Stop a runner:

```bash
curl -fsS -X DELETE -b "$COOKIE_JAR" http://127.0.0.1:25500/runner_requests/manual-001 | jq
```

The default state database is written to:

```text
var/runnerd.db
```

Runner control/stdout/stderr logs are stored in the DB-backed event store and can still be read through the management API:

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/manual-001/logs/control.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/manual-001/logs/stdout.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/manual-001/logs/stderr.log
```

The Admin request detail timeline reads the same event store as a bounded mixed page. Pass the oldest returned event ID as the exclusive cursor to read earlier records:

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/manual-001/events | jq
curl -fsS -b "$COOKIE_JAR" \
  'http://127.0.0.1:25500/runner_requests/manual-001/events?before_id=<oldest-event-id>' | jq
curl -fsS -b "$COOKIE_JAR" \
  'http://127.0.0.1:25500/runner_requests/manual-001/events?after_id=<newest-event-id>' | jq
```

`before_id` and `after_id` are mutually exclusive. Each is an exclusive cursor; `has_more` refers to additional records in the requested direction.

## 4. First Startup Check

Confirm runnerd read the GitHub App config correctly:

```bash
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/diagnostics/pprof | jq
```

Check:

- whether `github.auth_mode` is `app`;
- if a static installation is configured, whether `github.installation_id` matches expectations; in dynamic installation mode it may be `0`;
- whether `state.database` points at the database configured in `runnerd.yaml`.

## 5. Expose A Webhook URL

GitHub webhooks must be able to reach the local service. Choose one option.

Use smee:

```bash
open https://smee.io/new
echo 'https://smee.io/<your-channel>' > .smee-url
task dev
```

Use the same smee URL as the GitHub webhook Payload URL. When `.smee-url` exists, `task dev` starts the smee forwarder automatically. You can also start forwarding standalone with `task smee`. By default it forwards to `http://127.0.0.1:25500/webhooks/github`. If `runnerd.yaml` uses another listener address, set `SMEE_TARGET`:

```bash
SMEE_TARGET=http://127.0.0.1:25501/webhooks/github task smee
```

You can also use ngrok:

```bash
ngrok http 25500
```

Or cloudflared:

```bash
cloudflared tunnel create e2b-local-runner
cloudflared tunnel route dns e2b-local-runner runner.example.com
cloudflared tunnel run --url http://127.0.0.1:25500 e2b-local-runner
```

The final webhook URL looks like:

```text
https://<public-host>/webhooks/github
```

Replace `runner.example.com` with your own domain. Do not hard-code a random temporary `trycloudflare.com` quick tunnel address into GitHub settings.

For public deployments, only `/webhooks/github` needs to be exposed to GitHub. The `/runner_requests` management API can be served by the same service, but it must include a valid OAuth admin session cookie. In production, put it behind an HTTPS reverse proxy and restrict management API source IPs.

## 6. Configure GitHub Repository Webhook

In the target repository, open:

```text
Settings -> Webhooks -> Add webhook
```

Fill in:

- Payload URL: `https://<public-host>/webhooks/github`
- Content type: `application/json`
- Secret: exactly the same as `github.webhook_secret` in `runnerd.yaml`
- Which events: choose `Workflow jobs`. If you want the compensating path, also choose `Workflow runs`
- Active: checked

After saving, GitHub sends a ping. The service handles `workflow_job.queued` / `workflow_job.in_progress` / `workflow_job.completed` as the main path, and `workflow_run.requested` / `workflow_run.in_progress` as a compensating path. Other events return ignored; this is expected.

## 7. Configure GitHub Actions Workflow

Add this to the target repository:

```yaml
name: qiniu-runner-smoke

on:
  workflow_dispatch:

jobs:
  smoke:
    runs-on: [qiniu, ubuntu-24.04]
    steps:
      - name: Print runner info
        run: |
          uname -a
          whoami
          pwd
```

After triggering `workflow_dispatch`, the expected flow is:

1. GitHub creates a `workflow_job.queued` webhook.
2. This service verifies the signature and writes a `queued` runner request into the state database.
3. The service creates a sandbox, obtains a GitHub registration token, and starts an ephemeral runner inside the sandbox.
4. The GitHub job is picked up by the managed `qiniu,ubuntu-24.04` runner.
5. After the runner process exits, the service cleans up the sandbox.

If `Workflow runs` events are also configured, `workflow_run.requested` / `workflow_run.in_progress` are only compensating signals. runnerd queries queued jobs in the run and creates runner requests for matching jobs that have not already been enqueued through `workflow_job`. This compensating action does not immediately make the GitHub Actions UI show the job as running. The UI remains queued / waiting for runner until the ephemeral runner inside the sandbox registers successfully and GitHub assigns it to that job.

## 8. Troubleshooting Order

First check service state:

```bash
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/runner_requests | jq
```

Then inspect the request state and logs:

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id> | jq
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/control.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/stdout.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/stderr.log
```

Common issues:

- `invalid signature`: GitHub webhook secret and `github.webhook_secret` do not match.
- `runner start deferred because global concurrency is at capacity` or `runner start deferred because profile is at capacity`: the request remains queued until global or per-spec capacity is available.
- GitHub job stays queued: a managed default requires both `qiniu` and the
  exact OS label. `[ubuntu-24.04]`, `[qiniu]`, and unsupported extra labels do
  not match. For a custom spec, use its advertised and required labels.
- `template_resolution` admission failure: confirm the repository owner's
  scoped Sandbox endpoint exposes exactly one public default template with the
  managed stable name in `ready` or `uploaded` state.
- sandbox creation fails: confirm the account/organization Preferences or enabled admin default has a complete Sandbox service config matching the template and local environment; the Runner detail shows which source was selected.
- registration token fails: check the [GitHub App permission table](../README.md#required-permissions). Specs without `runner_group` require repository `Administration`; specs with `runner_group` require organization `Self-hosted runners`.

## 9. How To Read GitHub Actions Logs

runnerd creates a repository-level self-hosted GitHub Actions runner by default; a spec with `runner_group` creates an organization runner for the repository owner instead. After a job is picked up by the runner inside the sandbox, workflow step logs appear normally in GitHub Actions:

```text
Repository -> Actions -> choose workflow run -> choose job
```

GitHub Actions shows:

- workflow step stdout/stderr;
- logs for checkout, build, test, and other steps;
- job success, failure, or cancellation status.

Do not rely on GitHub Actions for all control-plane logs:

- sandbox creation failure logs, because the runner has not registered with GitHub yet;
- errors before runner download, `config.sh` registration, or `run.sh` startup;
- webhook validation failures, GitHub token request failures, or sandbox API call failures.

Read those control-plane logs from this service's management API:

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id> | jq
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/control.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/stdout.log
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/<request_id>/logs/stderr.log
```

The service's own logs are printed to runnerd's stdout/stderr, whether captured by the terminal or a service manager.

## 10. Diagnostics / pprof

The service imports `github.com/jimmicro/pprof`. After startup it generates `.pprof` address files and dump scripts near the binary. Diagnostics can be read directly from the management API:

```bash
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/diagnostics/pprof | jq
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/diagnostics/vars | jq
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_requests/e2b-<request_id>/diagnostics | jq
```

`/diagnostics/pprof` returns:

- discovered pprof address files;
- dump script paths;
- database backend and redacted DSN/path;
- GitHub auth mode, `app`, `token`, or `basic`;

`/diagnostics/vars` serves the current runnerd process's expvar registry directly. It never selects a discovered pprof address file, so a stale artifact from an older process cannot hide the current metrics. Current metrics cover profile current/busy/idle/pending/desired, retry/lease, create/stop counts and durations, GitHub API calls, runner registration/cleanup, and workflow job queued/started/completed, conclusion, failure, queue duration, and run duration.

`/runner_requests/{id}/diagnostics` is admin-only, runs on demand, and uses the canonical internal Request ID. It combines the stored request state, the newest 200 `control_log` lifecycle events, and a bounded live GitHub Job lookup. Successful GitHub Job lookups are cached for 30 seconds, and concurrent lookups for the same Job are coalesced so active-page polling does not spend provider rate budget on every refresh. Filtering happens before the event limit so high-volume stdout/stderr output cannot displace lifecycle evidence. The response reports machine-readable findings such as a failed GitHub Job whose runner termination was not observed by runnerd. GitHub lookup failure does not discard local evidence, and the endpoint never returns the saved Sandbox credential or raw webhook payload. Exact collection lookup uses `GET /runner_requests_lookup/{identifier}`, preferring an exact Runner Name before an internal Request ID when both namespaces contain the same value, and then navigates to the canonical ID route. `/diagnostics/runner-requests/{identifier}` remains as a compatibility alias with the same lookup precedence. New workflow completions also persist the GitHub conclusion, Sandbox stop request/result, and final cleanup result so future incidents can be diagnosed without searching the service manager's stdout log first.

The Runner Requests page accepts the user-visible Runner Name or an internal request ID for exact lookup, resolves that lookup before navigation, then opens the canonical `/admin/runner_requests/{id}` resource page. Its table keeps every request field on one line and uses horizontal scrolling when the viewport is narrower than the full dataset; vertical scrolling remains on the page instead of a nested table scroller. The resource page aggregates request state, findings, GitHub Job result, and one chronological Run history surface. Every persisted control/stdout/stderr event remains an individual timeline row, and its message is shown directly in normal page flow without a nested output card or disclosure. The newest 200 mixed events load first, and `GET /runner_requests/{id}/events?before_id=<event-id>` retrieves older pages using an exclusive cursor. Active diagnostics refresh every five seconds through one or more exclusive `after_id` pages until caught up, while the older-history cursor remains independent, so newly persisted events are merged without gaps or loss of history already loaded by the administrator. The separate `/admin/diagnostics` page is reserved for runnerd runtime inspection; it contains the redacted summary and pprof discovery, and does not fetch or render the potentially large expvar snapshot until an administrator explicitly loads or refreshes it. Legacy `/admin/diagnostics?runner=...` links resolve the identifier and redirect to the canonical request resource.

Release C removed the temporary catalog migration readiness endpoint and UI after the matcher cutover completed. The retired Runner Group and Policy APIs return `404`, and no active state, server, or UI behavior depends on those removed models.

## 11. Official References

- GitHub self-hosted runner workflow labels: https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/using-self-hosted-runners-in-a-workflow
- GitHub self-hosted runner autoscaling: https://docs.github.com/en/actions/hosting-your-own-runners/autoscaling-with-self-hosted-runners
- GitHub webhook `workflow_job` event: https://docs.github.com/en/webhooks/webhook-events-and-payloads
