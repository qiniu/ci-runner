# Local Testing And GitHub Setup

[Chinese](zh/testing.md)

This guide explains how to test the service with a local Qiniu sandbox environment and how to configure a GitHub repository so self-hosted runners are started automatically.

Run `task lint` for local quality checks. Its `staticcheck` step uses the Go version declared in `go.mod`, matching CI.

## CI scope

`Check` runs application tests, builds and release archive validation on PRs and
main pushes. `Template Check` (`.github/workflows/template-check.yaml`) runs only
when its declared inputs change; `.github/template-check-paths.json` selects
individual jobs. Service Docker/release inputs run only service Docker checks;
template source/scripts, compatibility inputs and Linux template Docker inputs
select their respective jobs. Shared `Taskfile.yaml`, application `check.yaml`
and unrelated scripts do not trigger it. Template documentation/configuration
can run source checks without contacting an image registry. Manual dispatch
runs all four jobs. Changes to the workflow or filter configuration also run all
jobs; the path regression test guards event/job filter consistency.

The independent jobs cover template source tests (Node.js 24 pin updater, matrix,
disk gates and archive preparation), upstream compatibility reports, service
Dockerfiles (including both release architectures), and Linux template
Dockerfiles. Source tests do not depend on registry jobs. Docker checks use the
shared `templates/` context and do not rebuild the UI. `docker build --check`
still requests registry metadata: an ECR `429 / Data limit exceeded` is a visible
registry failure, not proof of a source regression. Inspect the actual failed
step before retrying; no failure is silently ignored.

For local checks, use `task docker-check` and `task template-check-all` (Node.js
24, Docker and network required for the full template gate). A green static
check does not prove a real build or runtime. Tag-triggered `Release` still
builds/publishes service images with `Dockerfile.release`; regional Sandbox
template builds and promotion retain their existing separate acceptance gates.
Runner upgrade PRs require current-head evidence from both workflows, including
when maintenance automation must dispatch them explicitly. Do not configure a
path-filtered workflow as an unconditional required check: unrelated PRs do not
create its checks.

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

Sandbox service API URL and API Key are not configured in `runnerd.yaml`. After signing in, `/repositories` shows the effective Sandbox source for the selected account or organization but never embeds the credential editor. When configuration is missing, a manageable scope links to `/account/preferences` or `/organizations/{login}/preferences`; Settings remains the single write surface. For organization installations, visibility can come from repository-only access, while Sandbox management requires an active GitHub organization membership with `role == admin` (an organization owner). Settings lists only the signed-in account and owner-manageable organizations. Ordinary members, outside collaborators, and empty/unknown roles receive only redacted readiness (`manageable: false` plus the effective source) and cannot read API URL, secret state, Cache S3 fields, inheritance-account metadata, templates, runner-created instances, or scoped custom Runner Specs. Failed membership lookups reject the request without returning organization configuration. A local runnerd admin role does not bypass this GitHub organization-owner check. The disabled-by-default platform fallback remains admin-managed at `/admin/sandbox_service`. Its audience is either `all` or `selected`; selected entries match the repository owner's stable GitHub account ID and type. The API Key is encrypted with `auth.encryption_key`. For public-name Specs, resolution order is a saved runner-request snapshot, installation custom/inherited settings, an eligible personal account, eligible organization sponsorship, the enabled and audience-eligible admin default, then a not-configured error. Sponsorship revalidates stable fork/source/owner identities and the selected approval, permission, or membership mode before Sandbox creation; it never supplies Cache S3 or custom Specs. Cover all three modes, public-binding sponsorship behavior, capacity defer, revocation, fresh-retry snapshot clearing, owner-only audited APIs, and account/Organization UI route isolation.

The first-use product tour stores only a version, status, and `tour_seen` marker in the existing account-scoped `account_preferences` table under `onboarding/product-tour`; it never stores the Sandbox API Key. A missing or outdated version is returned as `pending` with `tour_seen=false`. Finishing the overlay writes `pending` with `tour_seen=true`, so it does not auto-start again while required setup remains visible. `completed` is written when the signed-in account resolves any effective Sandbox source: custom, inherited, or an eligible admin default. An explicit first-run skip writes `skipped` to dismiss the overlay without hiding required setup. Replaying the tour from the account menu does not reset or replace the persisted state.

Runner Specs are not `runnerd.yaml` fields. Internal Runner Groups and Repository Policies have been removed; the optional `runner_group` on a Spec remains a GitHub Organization Runner Group registration target.
All platform Runner Specs are database-owned and editable at `/admin/runner_specs`. Startup migrates legacy policies once and does not recreate or reconcile a fixed catalog; fresh databases have no specs. See [Platform Runner Specs](platform-runner-specs.md).

Verify public-name and fixed-ID references separately; IDs can refer to public templates. Public names must resolve uniquely to a runnable public template through the configured admin Sandbox service. Creating/changing a binding and re-enabling a displayed spec require a nonzero effective default BuildID. Publishing an unchanged spec checks publicness alone. These actions perform remote validation; unchanged-binding edits and unpublishing do not. Validation uses only admin credentials, independently of runtime fallback enabled/audience controls. Fixed IDs retain GetTemplate and effective-default-build validation; publication additionally requires the actual public flag. Cover public/private IDs, unknown metadata, pending/stale form responses and no-audit rejection. Standard and large public templates may display using existing IDs; publication must preserve runtime behavior and ID redaction. No new migration or automatic publication is needed.

The total provider check is limited to five seconds. Missing admin configuration
returns `409 sandbox_service_not_configured`; a missing template or no usable
default build during binding/re-enable validation returns `400 template_not_found` or `400 template_not_ready`.
Provider 401/403 produces `502 sandbox_template_access_denied`, other upstream
failures produce `502 template_validation_unavailable`, and cancellation/deadline
returns `504 template_validation_timeout`. Fix the configuration/template or retry;
a failed check never silently permits a save and never exposes the provider body.
Rejected saves leave both profile and audit records unchanged. If another save or deletion changes the spec while validation is in flight, the conditional write returns `409 runner_spec_conflict`; refresh before retrying instead of overwriting the newer state.

PATCH compares trimmed bindings and the publication/enablement transition. Verify that invalid or stale saves leave both the profile and audit unchanged, active execution-setting edits/deletes return `409 runner_spec_in_use`, and enabled/capacity/unpublish changes remain possible. Migration tests must preserve timestamps, controls and indexes, rollback when audit fails, and prove restart neither overwrites explicit policies nor recreates deleted rows. Admin validation does not prove image contents or another scope's access; retain real regional/workflow smoke.

`database.backend` supports `sqlite`, `postgres`, and `mysql`. Prefer sqlite for local development. Before documenting shared-database multi-instance deployment as supported, verify lease behavior with two runnerd processes sharing the same database.

New accepted and rejected runner requests extract workflow context, branch, SHA, Job URL, and PR number from the webhook in memory, then leave `github_payload_json` empty. Installation and Job IDs come from the `RunnerRequest` fields; those fields, repository, labels, and the parsed webhook context remain in structured columns for runtime use and display. A separately structured snapshot retains only an original Workflow Job's terminal name, status, conclusion, runner name, and local observation time; it does not restore raw webhook retention or backfill historical rows. The state tests cover `workflow_job` and `workflow_run` metadata after reopening the database, terminal-result round trips, rejected-request persistence, and preservation of historical payloads during repeated migration and installation-ID repair. Keep the legacy column, existing values, and backfill logic; historical payload cleanup and disk-space reclamation require a separate maintenance operation. Request and log retention are unchanged.

The state schema is mainly defined by GORM tags in `internal/state/records.go`. On startup, existing SQLite `runner_requests` and `runner_profiles` tables are migrated additively by creating every missing model column and index; they are deliberately excluded from generic SQLite `AutoMigrate` table recreation. This keeps historical ALTER-added runner-request values and preserves legacy runner-profile rows and custom indexes while adding managed-catalog fields. Admin newest-first lists depend on `idx_runner_requests_queued_id` over `(queued_at DESC, id ASC)`. The admin `/runner_requests` collection returns an envelope with `items`, `next_cursor`, and `has_more`; its cursor is exclusive over `(queued_at, id)` and must not be reused with different filters. `/runner_request_metrics` returns global queued/creating/running/stopping/completed/failed/unmatched and Runner Spec counts independently from list filters. Repository-authorized lists query each installation separately through `idx_runner_requests_github_installation_queued_id` over `(github_installation_id, queued_at DESC, id ASC)`, then merge the bounded results. Creating a missing index does not rewrite rows, but benchmark startup I/O and lock time on a disposable production-sized copy. A future non-additive change to either additive-only table requires a narrow explicit migration and a preserved-data regression fixture. Other tables run through a narrow legacy compatibility pass for older columns, obsolete OAuth constraints, and incompatible legacy scope tables, then run GORM `AutoMigrate`. Legacy `account_preferences` and `account_secrets` tables without `scope_type`/`scope_id` are dropped and recreated rather than data-migrated. Reconfigure their saved Sandbox Preferences and API keys after that upgrade; stored GitHub OAuth tokens are also cleared, so affected users must sign in with GitHub again before syncing installations. When changing state records, indexes, or migration helpers, run at least:

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
atomic mutation/audit commit and rollback behavior. The profile-policy gate also
checks that legacy private bindings retain their physical IDs, clear inert public
names and remain editable without changing historical timestamps.

```bash
RUNNERD_CATALOG_BACKEND_TESTS=1 \
RUNNERD_POSTGRES_TEST_DSN='host=127.0.0.1 user=runnerd password=runnerd dbname=runnerd_test port=5432 sslmode=disable' \
RUNNERD_MYSQL_TEST_DSN='runnerd:runnerd@tcp(127.0.0.1:3306)/runnerd_test' \
  go test ./internal/state -run 'Test(ApplyMutationWithAudit|FreshSchema|ScopedRunnerCatalogFreshSchema|RunnerProfilePolicy)SQLBackends' -count=1 -v
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
   - For Deploy, approve newly requested `Contents: Read-only` and `Pull requests: Read-only`; retry source discovery after approval, and sign in with GitHub again if the stored user authorization is rejected
   - If `Members: Read-only` was added to an existing App, each installation owner must approve the permission update before organization Settings become manageable
7. Record:
   - App ID
   - App slug, the short name in the App URL such as `https://github.com/apps/<slug>`
   - Installation ID, optional; when omitted, runnerd resolves it dynamically by repository
   - private key file path

After an installation owner approves `Members: Read-only`, reload `/account/preferences`. An organization where the signed-in user has an active membership with `role: admin` should appear in the Settings scope list and open `/organizations/{login}/preferences`. Ordinary organization members and repository-only collaborators can still see authorized repositories under `/repositories`, but the organization must remain absent from Settings. If an eligible organization is still missing after approval, sign out and complete GitHub OAuth again before retrying.

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

No global repo/org mode is required. Webhooks use `repository.full_name` from the payload. runnerd creates repository runners by default. If the matched runner spec sets GitHub `runner_group`, runnerd creates an organization runner for the repository owner; Linux passes the group as `--runnergroup`, while Windows selects its group ID in the JIT configuration request. Admission selects from all enabled Runner Specs after the repository allowlist check; the removed internal Runner Groups and Repository Policies do not affect matching.

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

`<your-github-user-id>` is the stable numeric `id` returned by GitHub `/user`, not the mutable login. Role belongs to the local account. OAuth identity stores provider, stable subject, and login display metadata, so other provider identities can later be linked to the same account. After ordinary users sign in, `/repositories` is the canonical readiness surface: it installs or syncs the configured GitHub App, loads the authorized repository intersection, shows local job activity, and resolves the selected account or organization's effective Sandbox source. If no custom, inherited, or eligible admin-default source exists, a manageable scope shows **Configure Sandbox** linking to the exact account or organization Preferences route; the credential form remains in Settings. Sandbox Templates and Sandbox Instances remain separate resource tabs for the signed-in account and owner-manageable organizations only. When GitHub returns an `installation_id`, runnerd records the GitHub App installation linked to that account. Jobs visible to an ordinary user are filtered by exact `(installation_id, repository_full_name)` pairs loaded with the stored GitHub App user access token. This is the intersection of the user's repository access and each linked App installation's repository scope, and it protects lists, details, groups, logs, and terminal operations. runnerd does not copy the full repository authorization list into local state; successful lookups are cached in memory with a 30-second hard expiry. After 20 seconds, the next hit returns the still-valid intersection immediately and starts one background refresh. Shared refresh work uses a server-scoped timeout rather than the first caller's cancellation. Installation or OAuth changes advance an account cache epoch, so refreshes from an older epoch cannot refill the cache or serve a later request. A rejected token invalidates the cache; a transient refresh error may retry but never extends authorization past the original 30-second expiry. The readiness page loads the same intersection on demand. Missing or rejected user tokens fail closed and require the user to sign in with GitHub again. A linked installation that GitHub reports as inaccessible is skipped so it cannot expose jobs from that installation and does not prevent other accessible installations from loading. The catalog endpoints require an ordinary-user session and, for installation scope, the same active-owner-role check used for credential mutation. They use the account or selected installation's encrypted credentials, map supported region ids to server-owned endpoints, and never expose the credentials. After an admin signs in, the browser stores a signed HttpOnly session cookie and automatically sends it to management APIs such as `/runner_requests`. For `curl` examples, export a cookie jar from the browser or OAuth debug flow:

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

The separate GitHub App accounts page is read-only and uses App authentication to query GitHub's current installation catalog rather than the local OAuth-account table:

```text
http://127.0.0.1:25500/admin/github_accounts
```

Select an account to open `/admin/github_accounts/<installation-id>` and load that installation's current repository scope. The numeric installation ID is the route identity so a GitHub login rename does not invalidate the deep link. These APIs return account display identity and repository full names only; they do not return installation tokens, permissions, credentials, or locally observed repository rows.

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/admin/api/github-app/installations | jq
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/admin/api/github-app/installations/<installation-id>/repositories | jq
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

UI source lives in `ui/` and uses React, Vite, Tailwind CSS, shadcn-style components, and the repository theme CSS. `task build` runs `task ui-build`, writes frontend output to `internal/server/ui/`, and then compiles `runnerd`. In development mode, `internal/server/ui_assets_development.go` proxies UI assets to Vite. In production builds, `internal/server/ui_assets_production.go` embeds `internal/server/ui/*`. `/` always renders the public product landing page with documentation and Jobs destinations; it does not load protected user resources. The protected ordinary-user Jobs homepage is `/jobs`. Opening `/jobs`, a job-group deep link, account settings, or an admin route without a session renders a focused sign-in page whose OAuth link preserves the full same-origin destination through `return_to`. Unknown routes render 404, and authenticated non-admin users receive an explicit access-denied page on admin routes. `/repositories` is the canonical ordinary-user readiness page for GitHub App installation/sync, the user/App authorized repository intersection, local job activity, and effective Sandbox service status. `/account/repositories` and `/organizations/{login}/repositories` remain compatible scoped deep links rendered by the same page. Account and organization Preferences are the only ordinary-user Sandbox credential editors; readiness links there only when a manageable scope is missing configuration. Settings retains Sandbox Service, Sandbox Templates, and Sandbox Instances resource management for the signed-in account and owner-manageable organizations only. Initial navigation loads only resources used by that route. Authenticated user routes load `GET /user/onboarding/product-tour` once for account-level tour state, but a failed onboarding request is ignored so it cannot block core workspace data and it is not part of polling. Only a pending account whose tour has not been seen and that lands on the exact `/jobs` homepage auto-starts the six-step tour; deep links do not interrupt the user. The final steps navigate to `/repositories`; ready sources require no action, while a missing manageable source points the user to Settings. The account-menu replay works for seen, completed, and skipped accounts without changing stored state. The Jobs homepage loads the first `GET /user/runner_requests?limit=100&offset=0` page and polls that page every five seconds while preserving any already-loaded history. Stable job-group routes and the Load older jobs action can load the bounded 500-row history window; the API rejects `limit + offset` values past 500 and does not advertise an unusable next link. GitHub App metadata, preferences, and onboarding state are not part of the polling loop. Admin routes load only the active section's request/spec/audit dependencies. Overview and the Runner Requests collection poll the request list; an active request resource polls its own diagnostics every five seconds and ignores stale responses after navigation. The published public catalog uses unauthenticated `GET /api/public/runner-templates` and must expose only stable published names and workflow labels. Provider catalogs use `GET /user/sandbox/templates?region=<id>` and `GET /user/sandbox/instances?region=<id>&template_id=<id>`; the instance endpoint lists only runner-created sandboxes and uses the effective scoped/default credential resolver. Installation-scoped catalog reads require an owner-manageable organization scope. Tests must prove signed-out and signed-in public responses are identical, exclude provider/scoped metadata, and keep public and provider loading, retry, failure recovery, and stale-response handling independent. The admin surface includes Overview, the account list and role controls at `/admin/accounts`, Runner Requests with retry/stop controls and a unified event timeline, Runner Specs, the platform fallback at `/admin/sandbox_service`, audit, label match test, and runtime diagnostics, but not retired internal Runner Group/Policy management or provider resource catalogs.

Public-catalog cache tests cover concurrent read coalescing, expiry, recovery after a database error, invalidation after successful Admin create/update/delete, and retention after rejected mutations.

The protected `/runner-specs` route is the scope-free, read-only ordinary-user platform catalog. It shows only enabled, published public Specs with copyable workflow labels; source/status badges, disabled Specs, account/Organization selectors, and enabled or concurrency controls stay out of this page. Settings routes `/account/runner-specs` and `/organizations/{login}/runner-specs` show only custom Specs owned by that scope. All three surfaces use `/user/runner-specs`, which requires the signed-in account or an owner-manageable Organization scope; scoped custom templates are validated with that scope's Sandbox credentials and never the admin fallback.

Runner Spec regression coverage must distinguish published public `platform_public` or `platform_custom` entries from scope-owned `scoped_custom` entries. Unpublished platform entries are absent from user responses; physical IDs and Runner Groups appear only on the selected scope's own entries.

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

Local admin fixtures also cover Runner Spec cards, complete labels, independent template loading and retry, shared-template request reuse, refresh, narrow-screen wrapping, editing and delete confirmation. These fixtures do not prove real Sandbox service behavior.

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

### Timed terminal transport faults

```bash
task ui-terminal-fault-test
```

Requires Linux or macOS, Go, Bun and `python3` with its standard-library `pty`
module. The Task installs matching Chromium and rebuilds the embedded production
UI. For direct execution after `task ui-deps` and `cd ui`, install Chromium with
`bunx playwright install chromium`, then run `bun run test:terminal-fault`.
Linux hosts missing browser system libraries can use
`bunx playwright install --with-deps chromium`. No runnerd config, GitHub login,
Sandbox credentials or production endpoint is used. Allow about two minutes
after the first Go/browser setup; tests run serially without automatic retries.

The dedicated Playwright suite starts a loopback-only Go test fixture with the
`terminalfault` build tag and race detector. It serves the built UI, uses the
actual terminal SSE/replay/idle-cleanup code and starts an interactive `/bin/sh`
in a real local PTY through Python. Authentication, deployment metadata, provider
creation and PTY resize are fixtures. A loopback HTTP proxy drops existing
connections and rejects new browser requests for measured intervals, using the
browser's native EventSource and real clocks:

| Outage | Required behavior |
| --- | --- |
| 5 seconds | Resume before the 20-second recovery deadline with the same session/PID and exact `Last-Event-ID`; all 60 numbered outputs arrive once in order and later input works. |
| 15 seconds | The same assertions near the recovery deadline; no replacement shell or premature cleanup. |
| 25 seconds | The UI closes around 20 seconds and attempts DELETE during the outage. Because DELETE cannot reach runnerd, the shell remains alive at network restoration and exits through the real 30-second idle cleanup. No automatic replacement or reconnection occurs. |

The recovery deadline starts at the first EventSource error, not the instant the
proxy is interrupted. The assertions include bounded scheduler allowances and
retain measured durations; browser clocks are never accelerated. The proxy
rechecks its monotonic deadline before restoring traffic, even if a timer fires
early. The 5-second case waits for an extra frame after an early cursor observation
and verifies replay against the final data/heartbeat event preceding the first
error, so in-flight output cannot make the cursor assertion stale. Fixtures and
child processes are torn down after the suite. This command runs separately from
`task test` and `task ui-production-smoke`; Check runs it in the `terminal-fault`
job. `RUNNERD_UI_SMOKE_BASE_URL` does not redirect this local-only suite.

Inspect `ui/test-results/`: `terminal-harness-*.log`, per-test `timings.json`,
native `events.json` and server `metrics.json`, recovery/timeout screenshots, and retained
Playwright traces on failure. CI uploads these artifacts on success and failure.
On failure inspect timing, cursor, sequence and process-exit evidence before
changing bounds or adding retries. These tests do not establish production proxy,
GitHub authorization, remote Sandbox, TTL or permission-revocation acceptance;
retain those [deployment smoke gates](deployment-smoke.md#9-sandbox-deploy-acceptance).

Inspect configured public-name specs (migration preserves existing rows; startup creates none):

```bash
curl -fsS -b "$COOKIE_JAR" \
  http://127.0.0.1:25500/runner_specs |
  jq '.[] | select(.template_source == "public") |
      {name, required_labels, default_template_name, enabled}'
```

An existing migrated standard catalog may contain `qiniu-ubuntu-slim`, `qiniu-ubuntu-22.04`, `qiniu-ubuntu-24.04`, `qiniu-ubuntu-26.04`, and `qiniu-ubuntu-latest`; Admin edits and deletions change this list. Verify
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
curl -fsS -b "$COOKIE_JAR" http://127.0.0.1:25500/runner_request_metrics | jq
```

Admin request loading must remain usable when a request takes longer than the five-second polling interval. List, statistics, and spec reads publish independently; same-resource requests coalesce, with at least five seconds between list completion and another automatic read. Hidden tabs skip polling. Statistics poll at most every 30 seconds and the authenticated server endpoint shares a successful 30-second process-local cache; concurrent misses share one read with a ten-second query deadline. A cancelled HTTP waiter does not cancel other subscribers. Errors are not cached. Summary values may therefore lag current state and must not be used for admission decisions.

The first list load reads one 100-row page. The overview and a collection without expanded history keep polling only the latest page, so incoming requests do not grow the window indefinitely. After history is expanded, background refresh stops at the loaded history boundary or five pages, whichever comes first. If the boundary no longer matches or exceeds the budget, it replaces the list with that bounded newest window, shows a notice, and retains the returned continuation cursor for older history. It never scans all retained requests. Once 500 or more rows are loaded, automatic list refresh pauses with a notice so reading older history is not disrupted; manual refresh returns to the latest page and resumes polling. Explicit history paging is not invalidated by polling; filter/route changes cancel stale work. Manual refresh retains displayed rows on failure. Initial loading, failed loading, and a successful empty response have distinct UI states. Browser reads have a 30-second deadline, and list database reads use the HTTP cancellation context.

Run `cd ui && bun test src/admin-resource-loader.test.js src/app-load-policy.test.js src/components/runner-requests-section.test.js` and `bunx playwright test --config playwright.production.config.ts admin-request-loading.pw.ts`. The production-bundle fixtures cover responses delayed beyond multiple poll ticks, statistics failure, filter changes, bounded refresh and continuation. Run `go test -race ./internal/state ./internal/server -run 'Test(AdminRunnerReads|RunnerRequestMetrics|ListRunnerRequestsIsPaginated)'` for cache/authentication and connection-wait cancellation. These fixtures do not establish production latency acceptance. On production, compare the two endpoints' `Server-Timing` headers: `auth` includes local account authorization and `data` includes the list read or cached/shared statistics read. These durations include connection waiting, not just SQL execution; correlate with database pool waits and `EXPLAIN QUERY PLAN` on an authorized database copy before changing indexes or connection limits.

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
  managed stable name with a nonzero effective default BuildID, including when the latest rebuild failed.
- sandbox creation fails: confirm the account/organization Preferences or enabled admin default has a complete Sandbox service config matching the template and local environment; the Runner detail shows which source was selected.
- `template-smoke` reports insufficient runtime root disk size: verify the
  fresh Sandbox root disk meets `disk_size_mb` from the selected TOML.
  Confirm the provider team's `DiskMb`, then follow the
  [same-name rebuild steps](default-runner-templates.md) and repeat smoke.
- Template conformance or release smoke loses `stdout.raw` or its temporary
  directory: verify cleanup runs only in the main shell. A watchdog terminated
  during Bash startup can invoke an inherited EXIT trap; it must not remove the
  main shell's files or kill its Sandbox. Run `go test ./internal/sandboxrunner
  -run 'TestSandboxConformance|TestTemplateReleaseSmoke' -count=1` for cleanup,
  timeout and release-evidence regressions.
- registration token fails: check the [GitHub App permission table](../README.md#required-permissions). Specs without `runner_group` require repository `Administration`; specs with `runner_group` require organization `Self-hosted runners`. Group lookup also needs that organization permission. `runner_group_not_supported` means the scope is not an organization. `runner_group_not_found` means the group is missing or inherited from an enterprise; `runner_group_repository_denied` means the target repository cannot use it. `runner_group_permission_denied`, `runner_group_rate_limited`, `runner_group_timeout` and `runner_group_unavailable` distinguish lookup failures without exposing upstream responses. See [GitHub Runner groups](platform-runner-specs.md#github-runner-groups) for form, YAML and validation behavior.

### Waiting workflow jobs

A queued webhook is a snapshot: the startup REST lookup may still return
`waiting`. runnerd retains these requests as local `queued`, releases the
creation lease/capacity, and rechecks using `worker.retry_base_delay_seconds`
and `worker.retry_max_delay_seconds`. Recognized `waiting`, `pending`,
`requested`, and already `in_progress` Jobs remain recoverable until GitHub
reports queued or completed; GitHub controls environment and concurrency wait
timeouts. `in_progress` observations include the owning Runner name in service
logs and never create a second Runner. A queued delivery may clear only the
known wait's next-check time, never reopen a terminal or active request.
Restart preserves deferred queue records. Unknown statuses and lookup errors
use `worker.retry_max_attempts`; exhaustion fails at `github_job_status`.
HTTP 429 and GitHub secondary rate limits are queue deferrals instead: the
retry count saturates at that limit while capped backoff continues, releasing
the lease and creation capacity. The sanitized reason is
`github_job_status_rate_limited`; polling resumes startup after throttling
clears without requiring another webhook. Terminal status-check failures
increment the existing Runner operation and Workflow Job failure counters
only after the failed state is persisted; waiting deferrals do not count as
terminal failures. Waiting metric changes are coalesced into the existing
five-second reconciliation cycle instead of scanning all active requests on
every poll; failed metric reads remain pending, and terminal failures refresh
gauges immediately. If persisting a completed Job fails, propagate the error
through normal startup failure handling to release the lease and creation
capacity; a temporary store failure retries without starting a Runner.
Control events show the decision/reason/next check, and service logs correlate
webhook action with Delivery ID. Raw webhook bodies are not persisted.

For a historical false completion from Issue #134, an authenticated admin may
explicitly call `POST /runner_requests/<request_id>/retry` using the existing
admin API session. This is a targeted API recovery, not a completed-row Retry
button. The endpoint requires the exact old waiting-skip control-log suffix,
no Sandbox/process/running/cleanup/terminal-result evidence, current GitHub
`queued` with no assigned Runner, repository admission, and a currently valid
Runner Spec. A concurrent state change rejects recovery with HTTP 409. Missing
or expired control evidence also rejects it; do not edit the database or bulk
reset completed rows. Successful recovery returns 202 and re-enters normal
startup checks, capacity, credentials and registration. Real completions stay
protected. No historical records are rewritten at startup.

Regression coverage:

```bash
go test ./internal/server -run 'Test(WorkflowJobWaiting|WorkflowJobStartup|QueuedDeliveryDoesNot|HistoricalWaiting)' -race -count=1
```

Use `task ui-build` first in a fresh checkout to generate embedded assets.
The tests use local GitHub/Sandbox fixtures; production acceptance still needs
a real environment-gated workflow, Runner pickup and cleanup.

## 9. How To Read GitHub Actions Logs

In the Jobs panel, select the job name to open its GitHub Job or the workflow name to open its Run. Standalone Job details provide **View job on GitHub** (or **View workflow run on GitHub** when only the Run is known). The default **Runner logs** tab shares the admin request event timeline, showing Runner lifecycle output and process stdout/stderr with timestamps and stages. It initially loads the newest 200 mixed events; filters apply to loaded records. The ordinary-user `GET /user/runner_requests/{id}/events` endpoint uses exact installation/repository authorization, accepts exclusive `before_id` or `after_id` cursors, and returns `{events, has_more}`. Older pages and active five-second incremental refresh merge without discarding loaded history; manual refresh and explicit scrolling to the bottom remain available. Workflow step output stays on GitHub. Both the Jobs panel and standalone Job fallback use this navigation; neither requests GitHub log archives. The retired `GET /user/runner_requests/{id}/github-log` endpoint returns `404`.

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

`/runner_requests/{id}/diagnostics` is admin-only, runs on demand, and uses the canonical internal Request ID. It combines the stored request state, the newest 200 `control_log` lifecycle events, and GitHub Job evidence. After a Sandbox becomes ready and before its Runner starts, runnerd records the selected Sandbox region and resolved physical template ID, then runs a best-effort five-second probe that may add the image version from `IMAGE_VERSION` or `ImageVersion` and the preinstalled GitHub Actions Runner version from `/opt/actions-runner/bin/Runner.Listener --version`. The probe decodes only those two allowlisted values, caps each at 256 bytes, and never persists or logs the complete environment, proxy values, tokens, keys, or cache credentials. Runner registration leaves GitHub's official self-update enabled. Immediately before a job starts, the fixed runnerd hook validates the writable Runner copy's version and sends one bounded marker plus `RUNNERD_JOB_STARTED` through a permission-restricted one-shot FIFO to the outer startup process; official hook stdout remains in the GitHub Job log and is not observed by the Sandbox command callback. The Sandbox service accepts only the first valid pre-job marker forwarded by that outer process, freezes that value in memory, ignores later Workflow output for version evidence, and stores it as `effective_runner_version` only through the matching Sandbox/PID exit callback. The hook removes the FIFO before Workflow steps begin, and the outer process removes or stops any unused channel during cleanup. The frozen value survives provider cleanup even when no command result is returned; a runnerd restart after capture may instead leave this best-effort field empty because recovery never trusts post-start Workflow output. Windows Sandboxes probe `C:\actions-runner\bin\Runner.Listener.exe --version` and `C:\ProgramData\qiniu-runner\image.env` instead. Windows has no FIFO and no separate unprivileged Runner user, so the hook atomically publishes a marker file that the outer script relays once; the hook waits up to 10 seconds for that relay before workflow steps begin and drops an unconsumed marker, so a workflow can forge at most this cosmetic version field. Probe or capture failure does not block Runner startup, completion, or cleanup; known region and template identity still persist, while unavailable values remain empty. A fresh retry or mismatched-Job requeue clears the prior environment snapshot. Region-specific physical template IDs are stored only on Runner Requests and never written back to public-name Runner Specs. Existing SQLite tables gain nullable columns additively, so historical rows remain valid.

Private-ID specs add a mandatory pre-registration update gate. runnerd fetches GitHub's repository- or Organization-scoped official application descriptors before it requests Linux registration credentials or Windows JIT configuration, validates the GitHub-owned URL, filename, architecture, version, and SHA-256, and passes no API credential into the Sandbox. A stale writable Runner requires `curl`, `tar`, `sha256sum`, `mktemp`, and `timeout`; the complete retrying download has a hard five-minute deadline and 512 MiB bound, verifies the archive and candidate version, and replaces the work directory before `config.sh`. Windows pre-creates its JIT identity before provisioning, but launches it only after guest preflight succeeds and reconciles it through the API if startup fails. Windows Sandboxes use the built-in `curl.exe`, `Get-FileHash`, and `Expand-Archive` with the same total deadline, size bound, and checks, use a server-created one-time JIT configuration with `Runner.Listener.exe run --jitconfig` once instead of `config.cmd` and a reusable registration token; the outer supervisor restarts retry/update exits using saved configuration without replaying JIT, and restore the previous directory from the script's `finally` block when the cutover did not commit. The swap keeps the candidate and previous-directory backup under one random sibling update root until cutover completes; a catchable HUP, INT, or TERM after the old directory moves restores it, and a failed restoration preserves and reports the backup path. An installed Runner that matches or is newer than the target skips those tool and network requirements and is never downgraded. Lookup or Sandbox preflight failure prevents Runner launch; the server-side lookup is recorded as `github_runner_downloads`, while an archive-version mismatch reports both the observed and target versions. Because the template is immutable, the same download is expected on every new Sandbox until the user rebuilds it. Public-name specs skip this gate and use their preinstalled baseline. Production acceptance for a deliberately stale custom template requires the detail page to show `runner_version != effective_runner_version` for a Job that completed on the refreshed Runner.

`POST /runner_requests/{id}/network-diagnostics` is a separate admin-only action for a request that is still `running` with a persisted Sandbox ID and Runner PID. Its JSON body accepts only `github_api`, `ubuntu_archive`, or `llvm_apt` as `target`; the server never accepts a URL, host, command, header, or credential from the browser and does not follow redirects. The fixed Sandbox command records at most eight DNS addresses, the connected IP, DNS/TCP/TLS/first-byte/total timings, HTTP status, exit code, and at most 512 bytes of a stable probe-error category. Raw command stderr and provider details are discarded. The complete action is limited to 10 seconds, with a three-second connection timeout, eight-second transfer timeout, and 65,536-byte range/maximum download. Combined command stdout/stderr is independently limited to 65,536 bytes and exceeding that budget cancels the command. Only one probe can run per Request ID/Sandbox ID/PID attempt, and a completed attempt has a 30-second cooldown. The handler does not hold the Runner lifecycle lock during provider I/O, reloads the request after the provider call, and discards a result if status or attempt identity changed. A safe structured result or generic provider-failure result is appended to `control.log` under the `network_diagnostic` stage; it never changes request status, retry fields, termination ownership, cleanup, or the GitHub workflow timeout. Historical and terminal requests remain readable but cannot run a live probe.

New requests retain the original Workflow Job's terminal name, status, conclusion, runner name, and local observation time when a completion webhook, pre-start check, recovery, reconciler, or bounded post-runner-exit lookup observes that terminal result. The post-exit lookup runs only after Sandbox cleanup has completed or entered its retry state, so GitHub availability cannot delay that cleanup. A zero or mismatched Job ID and every nonterminal observation are ignored, so an assigned different Job cannot overwrite the original result and active requests keep live behavior. Repeated identical terminal observations preserve the first retained observation time, and requeueing a mismatched completed request clears the old snapshot together with its lifecycle and termination fields. Diagnostics return retained evidence with `lookup_status` and `source` set to `retained`; rows created before this schema increment continue to use a bounded live lookup marked `source: live`. Successful live lookups are cached for 30 seconds, and concurrent lookups for the same Job are coalesced. Filtering happens before the event limit so high-volume stdout/stderr output cannot displace lifecycle evidence. New lifecycle writes persist the first termination owner as `process_exit`, `workflow_job_webhook`, `manual_stop`, `recovery_cleanup`, or `failure_cleanup`; an observed process result also persists its nullable exit code. The diagnostic finding for an unobserved termination is emitted only when neither typed state nor stable `runner_exit`, `runner_hook`, or `runner_cleanup` control stages nor the historical message fallback provide evidence. Consequently, a successful GitHub-completed request with a completed cleanup event is not treated as an abnormal unobserved termination. GitHub lookup failure does not discard local evidence, and the endpoint never returns the saved Sandbox credential or raw webhook payload. Exact collection lookup uses `GET /runner_requests_lookup/{identifier}`, preferring an exact Runner Name before an internal Request ID when both namespaces contain the same value, and then navigates to the canonical ID route. `/diagnostics/runner-requests/{identifier}` remains as a compatibility alias with the same lookup precedence. Runner hook, exit, and cleanup control events use stable `runner_hook`, `runner_exit`, `sandbox_cleanup`, `github_cleanup`, and `runner_cleanup` stages while preserving legacy `control.log` reads. The Sandbox command result does not currently expose the actual exit signal, so runnerd does not infer one from an exit code or from a requested stop signal.

Runner start diagnostics use one grouped `runner_timing` control event per startup. Each line includes a fixed stage, `duration_ms`, clock source, and UTC `observed_at`; the event timestamp is the later persistence time. Durations are collected in memory and written after lifecycle/admission locks are released. Cleanup and mismatched-job requeue timings follow the same rule. `queue_wait` uses persisted creation time and can span multiple attempts; a negative wall-clock difference is retained as zero with `clock=wall_skew`. The Runner timestamp label “Last started” refers to the latest attempt, so creation-to-start alone does not measure one attempt's startup duration.

The startup admission stages are sequential: `request_read` (`ReadRequest`), `initial_state_read` (`ReadState`), `admission_lock_wait` (acquisition of the shared spec-mutation guard), `profile_lookup` (global or scoped profile lookup), `global_capacity_check` (`InFlightCount`), and `profile_capacity_check` (the selected profile's global or scoped in-flight check), followed by `capacity_reservation` and `creating_state_persist`. After Sandbox startup, `running_state_read` measures the state reload between `running_lock_wait` and `running_state_persist`. Each duration excludes the adjacent stages, uses a monotonic clock, and ends at its `observed_at`; it includes the entire named operation, including Store/connection waits and query work, rather than pure SQL execution time. Profile validation is local; capacity checks can short-circuit without a query. The stages reached before a lookup error or capacity deferral are retained without inventing later phases. If the initial request cannot be read, no timing event is written, as before. Fixed stage names contain no request data, SQL, credentials or error text.

Startup and queue admission share a read guard; spec mutations take its exclusive side. A delayed profile/count/creating Store operation therefore does not serialize unrelated admissions, while mutations still cannot interleave between profile reload and the creating CAS. A separate short mutex reserves global and exact-profile capacity without Store I/O. Reservation keys use the reloaded profile row's name, so request spellings that resolve to one row under the database collation share capacity; distinct rows on a case-sensitive backend remain separate. Counts read outside that mutex include reservations pending before the read and all admissions accepted while it was in progress, including writes that finished before the stale count returned. This deliberately overcounts uncertain overlap rather than oversubscribing; a conservative rejection uses the existing five-second queue deferral and releases the worker lease. Reservations end when creating persistence returns (including error/CAS conflict), with persisted creating/running/stopping rows supplying subsequent counts. Profile observations are removed when no reader/reservation remains; restart relies on durable counts. This retains the existing single-process boundary, not multi-instance admission support.

`capacity_reservation` measures the final in-memory capacity decision after Store queries; `admission_lock_wait` now identifies contention with spec mutations, not serialization behind another ordinary admission. Database/connection contention can still delay Store operations and an exclusive spec mutation can still delay admissions. Delayed-Store regressions require another request to reach running before releasing the first; stale-count and pending-write cases check global/scoped capacity, failed writes, version conflicts, duplicate starts and lease release. Production latency acceptance still requires a deployment and real load.

To investigate a gap after `queue_wait`, compare `admission_lock_wait`, `profile_lookup`, and both capacity checks with `creating_state_persist`. A gap after `running_lock_wait` can now be attributed to the state reload separately from its write. A slow Store stage alone cannot identify whether the cause is connection-pool waiting, database locking, query execution or another Store cost. Do not sum all startup stages: `queue_wait` is cumulative wall-clock age, and `sandbox_start` includes `cache_credentials`. Controlled delayed-Store and contended-admission tests verify attribution, early-return evidence and lock-free timing persistence; production root-cause confirmation still requires post-release observation under real load.

The Runner Requests page accepts the user-visible Runner Name or an internal request ID for exact lookup, resolves that lookup before navigation, then opens the canonical `/admin/runner_requests/{id}` resource page. Its table keeps every request field on one line and uses horizontal scrolling when the viewport is narrower than the full dataset; vertical scrolling remains on the page instead of a nested table scroller. The resource page aggregates request state, findings, a retained terminal GitHub Job result or historical live fallback with explicit source/freshness, and one chronological Run history surface. It displays persisted Last started, Stopping, Completed, and Failed timestamps; Sandbox region, resolved template ID, template version, preinstalled Runner version, and effective Runner version; the localized termination source plus its stable identifier; and a process exit code only when one was observed. Historical or partially captured environment fields render as `-`. While a request has a live running attempt, the same page progressively exposes the fixed-target network diagnostic control and directly presents resolved addresses, the connected IP, phase timings, HTTP status, exit code, and stable error category. There is no free-form target input, and success or failure refreshes the existing timeline rather than creating a second diagnostic record surface or export format. Cleanup Duration is calculated only from `stopping_at` to the terminal timestamp selected by the current status; a request completed before Sandbox creation leaves `stopping_at` absent. Missing, invalid, inverted, or Go-zero timestamps render as unavailable, and `updated_at` is never used as a cleanup endpoint. Every persisted control/stdout/stderr event remains an individual timeline row, and its message and optional stage are shown directly in normal page flow without a nested output card or disclosure. The newest 200 mixed events load first, and `GET /runner_requests/{id}/events?before_id=<event-id>` retrieves older pages using an exclusive cursor. Active diagnostics refresh every five seconds through one or more exclusive `after_id` pages until caught up, while the older-history cursor remains independent, so newly persisted events are merged without gaps or loss of history already loaded by the administrator. The separate `/admin/diagnostics` page is reserved for runnerd runtime inspection; it contains the redacted summary and pprof discovery, and does not fetch or render the potentially large expvar snapshot until an administrator explicitly loads or refreshes it. Legacy `/admin/diagnostics?runner=...` links resolve the identifier and redirect to the canonical request resource.

The idle-runner sweeper uses the additional `idle_cleanup` termination source so a successful cleanup of a Runner that never accepted a Job is not mislabeled as failure cleanup.
A Runner process that ends before accepting any Job, for example after GitHub terminates its listener or its Sandbox output stream is lost while GitHub reports it idle, schedules a bounded `runner_exited_without_job` retry instead of completing or failing the request when Sandbox and GitHub Runner cleanup both succeeded; the next start rechecks the original Job before creating a Sandbox, and exhausted retries fail visibly.

Each accepted process-exit watcher is bound to the Sandbox ID and PID of its execution attempt. If a matching process result arrives after a webhook, administrator action, or recovery path already completed or scheduled cleanup, runnerd stores a previously missing exit code and appends a `runner_exit` event without changing the first termination source or repeating cleanup. A delayed callback from an older attempt is ignored when either identity value no longer matches.

Release C removed the temporary catalog migration readiness endpoint and UI after the matcher cutover completed. The retired Runner Group and Policy APIs return `404`, and no active state, server, or UI behavior depends on those removed models.

### Python/pipx template regression

Run `task template-python-test` after changing Python/pipx setup. It requires
Docker and network access, executes the pinned upstream installer on the pinned
Ubuntu 26.04 rootfs, and checks apt-owned `packaging` stays unchanged, pipx tools
retain their directories, ordinary users can invoke pipx, and installer failures
still stop the build. This does not replace regional Sandbox build/smoke gates.

### Windows runner end-to-end test

`TestWindowsRunnerE2E` in `internal/sandboxrunner` drives `StartRunner`,
`RecoverRunner`, one real GitHub Actions job, and `StopRunner` against a real
Windows Sandbox. It is skipped unless `RUNNERD_WINDOWS_E2E=1`, and needs
`QINIU_SANDBOX_API_URL`, `QINIU_API_KEY`, `RUNNERD_E2E_TEMPLATE_ID`,
`RUNNERD_E2E_REPOSITORY_URL`, and a fresh `RUNNERD_E2E_JIT_CONFIG` with the exact `RUNNERD_E2E_RUNNER_NAME` and requested labels. Create JIT configuration with the GitHub repository or organization `generate-jitconfig` API, using the intended Runner group ID and `work_folder` of `..\work`. Keep the configuration private; never log it. Windows requires JIT and fails closed if only a registration token is supplied. The startup file is scrubbed before listener launch; same-user process memory and the current Runner credentials remain outside this isolation claim.

Windows interrupted-creation recovery removes any pre-created JIT Runner before requeueing, including when the next Job check may finish the request without a Sandbox. Failed deletion persists a stopping request for the normal cleanup retry loop. Regression coverage includes absent Sandboxes, absent processes, and cleanup failure followed by a successful sweep.

Optionally set `RUNNERD_E2E_TEMPLATE_VERSION`, `RUNNERD_E2E_LABELS`, and a
`RUNNERD_E2E_TRIGGER_CMD` bash command that queues one matching job after the
Runner starts listening; otherwise trigger the job manually. The test reads the
expected template Runner version and archive digest from
`templates/github-runner-windows-11/template.env`, and accepts a newer effective
version after GitHub's official self-update.

```bash
RUNNERD_WINDOWS_E2E=1 go test -tags development ./internal/sandboxrunner -run TestWindowsRunnerE2E -v -timeout 25m
```

The caller owns the pre-created GitHub Runner identity: if startup or the test fails, remove that exact Runner through the GitHub API; `StopRunner` only removes the Sandbox. This direct-service test does not prove controller cleanup or a completed official self-update.

It does not exercise webhooks, Runner Specs, or the web UI; use `task dev` with
a fixed-ID spec whose labels include `windows` for that path.

## 11. Official References

- GitHub self-hosted runner workflow labels: https://docs.github.com/en/actions/hosting-your-own-runners/managing-self-hosted-runners/using-self-hosted-runners-in-a-workflow
- GitHub self-hosted runner autoscaling: https://docs.github.com/en/actions/hosting-your-own-runners/autoscaling-with-self-hosted-runners
- GitHub webhook `workflow_job` event: https://docs.github.com/en/webhooks/webhook-events-and-payloads

## Sandbox Deploy verification

Deployment details use path IDs under the repository or PR context, retain readable Branch/Tag and revision queries, and reject retired query-ID/source links. Verify signed-out return destinations, direct reload, revision selection, and browser history against those paths.

Run `go test ./internal/state -count=1` first, then
`go test ./internal/deployment/... ./internal/server -count=1` and `task test`.
The state coverage includes old SQLite deployment target-column upgrades,
optimistic lifecycle fencing, retention and bounded events. The CI PostgreSQL/MySQL
gate includes `TestDeploymentScopeQuotaSQLBackends`, which exercises concurrent
account admissions against one organization quota after earlier transaction reads,
with rejected-write/audit invariance. `TestDeploymentRevisionsSQLBackends` covers
immutable history; `TestDeploymentQuerySQLBackends` verifies constant query counts
for historical source lists, transactional UTF-8 byte counters and concurrent appends.
The rollback/re-upgrade regression simulates legacy append and deletion, repeats
migration, preserves history and timestamps, and verifies one lazy recount per
revision after startup on SQLite, PostgreSQL and MySQL.
`TestStartupRecoveryDefersQueuedDeploymentToIndependentWorker` verifies that
startup recovery leaves queued plans untouched and a cancelled startup context
cannot cancel the worker started afterward; retain the existing Runner recovery
and HTTP-gate tests separately.

Deploy recovery tests distinguish healthy running attempts, transient provider
failures, mismatched process identity, interrupted creation, unknown creation
outcomes and confirmed missing resources. Provider HTTP fixtures check metadata
lookup without creation, missing Sandbox handling and reconnect lifetime.
These checks do not prove public ingress, real Contents permission or remote
resource release. Use [Deployment Smoke](deployment-smoke.md#9-sandbox-deploy-acceptance)
for those gates. Deploy uses the shared scoped/inherited/platform Sandbox service
eligibility rules without a separate opt-in. `TestDeploymentSandboxServiceMatchesJobEligibility`
covers platform enablement, complete configuration, selected-audience identity/type,
scoped and inherited priority, fail-closed corruption and legacy records without snapshots.
`TestDeploymentPlatformServiceTemplateAccess` exercises owner/member public-only
discovery, creation and redeploy without reading the platform private catalog;
`TestRepositoryMemberCanDeployWithoutPrivateCatalogAccess` retains scoped private
binding replay. `TestDeploymentRuntimePreservesSavedService` covers reconnection
with saved credentials when no platform default is enabled and rejects corrupt snapshots.
`TestLegacyDeploymentRejectsPlatformPrivateTemplate` rejects private/unbuilt choices
before provider validation or creation when legacy queued records lack snapshots.
`TestPersonalRepositoryCollaboratorDeploymentAudience` covers personal-repository
collaborators across template discovery, creation, redeploy and legacy resolution:
owner selected allows platform use, collaborator-only or wrong-type selection denies
it without revision/audit changes, and collaborator-scoped credentials keep priority.
Its platform outcomes are checked against Jobs.
`TestPlatformDeploymentRetryDoesNotRequireOrganizationOwnership` verifies that
forbidden/unavailable organization membership lookup cannot block a platform-backed
public retry. Exact repository authorization, audience checks and private-template
rejection still apply; rejected retries leave revision/audit state unchanged.
The execution-only live worker probe
selects operator credentials directly; repository-owner audience admission belongs
to the live API gate. These local fixtures do not prove a real provider deployment.

The worker tests also cover the startup-only exact Host environment for Vite,
preservation of unrelated environment values, and rejection before startup when
the application endpoint cannot be resolved. To exercise the real worker with
the public `qiniu/deployment` Vite fixture and the public `base` template:

```bash
RUNNERD_DEPLOY_LIVE=1 \
RUNNERD_DEPLOY_LIVE_CONFIG="$PWD/runnerd.local.yaml" \
RUNNERD_DEPLOY_LIVE_ACCOUNT_ID="<configured-account-id>" \
RUNNERD_DEPLOY_LIVE_TEMPLATE=base \
go test ./internal/server -run '^TestDeploymentLivePublicWorker$' -count=1 -v -timeout 20m
```

This opt-in test reads that account's scoped Sandbox configuration, uses an
isolated temporary deployment database, and creates real billable Sandboxes.
It checks public HTML, same-attempt controller reconnection, manual stop,
accelerated expiry, and provider-confirmed resource release. It does not prove
GitHub admission authorization or a full runnerd process restart. Set
`RUNNERD_DEPLOY_LIVE_EVIDENCE` to an absolute, untracked output path to save
credential-free results; no local account or physical template ID is embedded
in the test. The test skips unless explicitly enabled.

The `create-timeout` subtest forwards one real create, discards its successful
response, and injects a timeout for the initial metadata lookup. It requires an
unresolved `creating` attempt, then restores transport and starts a new worker
to reconcile the exact resource, compensate it without another create, preserve
the original failure, and confirm provider 404. Run only this case with the same
environment and `-run '^TestDeploymentLivePublicWorker$/^create-timeout$'`.
The transport faults are injected locally; provider operations remain real.
This does not cover duplicate authenticated API requests. Worker regressions
also verify that interruption during failed cleanup preserves failure status and
cause in both the deployment and its revision.

The `start-failure` worker subtest uses the same pinned public source with a
locally injected `exit 23` startup plan. It must fail at starting/probing, retain
failure evidence on the revision, create exactly one real Sandbox, and confirm
provider 404 after cleanup. This is execution/cleanup evidence, not GitHub
configuration-admission evidence.

For authenticated admission against an already running **local** runnerd, use:

```bash
RUNNERD_DEPLOY_LIVE_API=1 \
RUNNERD_DEPLOY_LIVE_BASE_URL=http://127.0.0.1:25500 \
RUNNERD_DEPLOY_LIVE_CONFIG="$PWD/runnerd.local.yaml" \
RUNNERD_DEPLOY_LIVE_ACCOUNT_ID="<configured-account-id>" \
RUNNERD_DEPLOY_LIVE_EVIDENCE=/tmp/deployment-api-evidence.json \
go test ./internal/server -run '^TestDeploymentLivePublicAPI$' -count=1 -v -timeout 12m
```

By default this check uses `qiniu/deployment` at branch `example/vite`. It requires
the running server's config, a linked installation for the repository owner,
no existing active deployment in the selected scope, a runnable public `base`
template, and a per-scope limit of 1–3. It signs an in-memory short session for
the configured existing account and sends it only to a loopback origin; it does
not test OAuth login. Real GitHub authorization, API admission, workers and
Sandbox calls run in the existing process. Concurrent keyed requests must yield
one resource/attempt; quota rejection must leave no row, DELETE must release the
Sandbox while retaining history, terminal replay must remain idempotent, and
admission must recover after capacity is released. It creates at most the scope
limit plus one new deployment, stops only its own submissions, and preserves
those terminal records for inspection. It does not prove a second user's access,
private Contents permissions or fork PR execution with its default source. Never publish cookies/configs.

To use a designated public fork or PR fixture, set:

- `RUNNERD_DEPLOY_LIVE_REPOSITORY` (default `qiniu/deployment`).
- `RUNNERD_DEPLOY_LIVE_SOURCE`: `branch`, `tag`, or `pull_request` (default `branch`).
- `RUNNERD_DEPLOY_LIVE_REF`: branch/Tag name or positive PR number (default `example/vite`).
- `RUNNERD_DEPLOY_LIVE_COMMIT_SHA`: optional full immutable SHA to deploy.
- `RUNNERD_DEPLOY_LIVE_SOURCE_REPOSITORY`: optional expected PR head repository.
- `RUNNERD_DEPLOY_LIVE_HTML_MARKER`: optional text required in the public HTML.

The quota check follows the selected personal account or Organization scope.
For an older fork PR Commit, advance the fixture branch first, then pin the old
SHA and its HTML marker; the saved fork identity and served old content must match.
This public gate does not revoke GitHub access or prove private Contents permissions.

To additionally verify CI lifecycle independence, set
`RUNNERD_DEPLOY_LIVE_CI_RUN_ID=<single-job-run-id>` and
`RUNNERD_DEPLOY_LIVE_CI_RUN_ATTEMPT=<attempt>` with the API gate above. CI must
use `qiniu/deployment` and the same Commit as the deployment. The test waits for
that exact attempt, observes concurrent running Sandboxes, requires retained CI
success and real Runner Sandbox absence, then probes the unchanged deployment
immediately and after 20 seconds. Missing overlap is a failure. The gate does
not dispatch/rerun CI or mutate Runner requests. Prepare a real workflow, wait
for the test's `waiting for concurrent CI` message, then trigger the selected
attempt separately. For fast startup, compile first with
`go test -c -o /tmp/deployment-live.test ./internal/server`, then run that binary
with the same environment and `-test.run '^TestDeploymentLivePublicAPI$'
-test.v -test.timeout 12m`. Keep private repositories and second-user acceptance
separate from this public gate.

SDK protocol fixtures verify that application startup sends no remote
`Connect-Timeout-Ms` deadline while response-header and PID confirmation remain
bounded locally. Worker regressions cover provider lookups that cross expiry
and stale expiry observations that must not stop a replacement attempt.


Deployment list/detail/event tests must prove cross-account repository sharing,
exact installation-repository isolation before count/pagination, Jobs cache reuse,
and rejection of missing/rejected OAuth access. Verify non-owner creation and
public-only template choices, owner-only private catalogs/settings, revision actor
persistence and worker revalidation, and per-user PTY isolation. Deployment
content remains local; a cold authorization cache may contact GitHub.
UI tests must prove that `/deployments` loads only local history/navigation and links to
`/deployments/new`, that creation only loads form dependencies, and that both
routes reload directly. Deployment UI verification also covers Branch/Tag discovery, empty and error retry
states, version pagination, delayed source/scope responses, template selection,
create-to-detail navigation, direct `/deployments/{id}` reload, and before/after
log pagination with history retained on errors. Run the focused Bun deployment
interaction tests and `task ui-production-smoke`; its deployment fixture checks
the production bundle, while authenticated real Sandbox acceptance remains separate.
Navigation tests also cover more than 100 history rows, terminal-only repositories, exact-pair filtering before repository counts/pages, active summaries without credentials, independent history/sidebar pagination, retained selected terminal entries, delayed responses after repository/account switches, in-app back/forward, and desktop/narrow independent scrolling. Fixture browser checks do not establish real Sandbox acceptance.
Single-select status-filter coverage includes authorized/source-scoped counts beyond the first page,
all eight lifecycle states, current-state grouping for historical source matches,
invalid/repeated/empty API CSV members, each UI status, default All,
exclusive selection and repeated clicks retaining the current status, empty results, source-preserving URL selection and reload,
back/forward, keyboard focus and Space toggling, narrow-screen overflow, pagination reset, and
late page rejection after switching status. Run `go test ./internal/state -run
TestDeploymentStatus -count=1` and the `filters deployment status` production fixture.
API regressions cover qualified branch/tag resolution, same-name idempotency
conflicts, source-prefix length limits, and unauthorized discovery before provider reads.

Deployment revision verification must cover initial #1, atomic next-version creation and audit rollback, frozen old state, revision-scoped cursor pages, stale-output rejection, repeated legacy SQLite upgrade, Postgres/MySQL schema and mutations, URL-restored selection, and delayed-response isolation. Verify the history/log columns stack on a narrow screen and read without provider calls.

### Deployment terminal checks

Reconnect regression checks additionally cover a ten-second proxy read-idle
limit across more than a minute of periodic output, HTTP/1.1 and HTTP/2 server
read/write deadlines, byte-offset suffix replay without duplicates, invalid or
evicted cursors, closed-session output rejection, and stale idle timers. Run
`cd ui && bun run test:production --grep 'resumes deployment terminal'` for real
Chromium EventSource transport loss followed by resume on the same PTY, permission
denial, explicit server closure, repeated empty reconnects, idle-heartbeat recovery, and the twenty-second outage limit. These are
local transport fixtures, not proof that the production ingress keeps a stream
open. Production acceptance must run continuous output and an idle shell for more
than one minute, confirm the same PID survives a transport recovery, then verify
stop/expiry and access revocation still close it. Keep runnerd and ingress logs
when diagnosing the initial transport close; heartbeat and local deadline tests
alone cannot identify a proxy's total request limit.


Run `go test ./internal/server ./internal/sandboxrunner -run 'TestDeploymentTerminal|TestDeploymentsShareJobs|TestDeploymentReadsAndMutations|TestUser.*Terminal|TestTerminalHub' -count=1` for PTY input/resize/close, repository authorization and per-user session ownership, revision/attempt identity, stopped/expired rejection, and remaining-lifetime Connect coverage. Run `cd ui && bun run test:production --grep 'separates deployment'` for browser tab navigation, manual connection, typing and disconnect with fixture APIs/output. Fixtures do not prove a real Sandbox PTY. For live acceptance, open a running revision, connect, run `pwd` and inspect an application log file, resize, disconnect, reconnect, then stop the deployment and confirm the terminal becomes unavailable. Historical revisions must never open the current Sandbox.

Deployment context checks: run `cd ui && bun test src/github-context-routes.test.js src/app-load-policy.test.js src/components/user-deployments-section.test.js` and the deployment production browser fixture. Cover legacy redirects, direct reload/OAuth return, encoded branch names, immutable revision selection, back navigation, and local-only filtered history before pagination. State coverage includes `TestDeploymentContextListsMatchHistoricalRevisionsBeforePagination`.

PR source verification: `go test ./internal/server ./internal/github -run 'TestPullRequestDeployment|TestDeploymentSource' -count=1` covers immutable head/older-commit resolution, fork identity, base mismatch, deleted heads, head races, idempotent replay, revoked access, and latest/revision redeploy. Run state upgrade and dedicated PostgreSQL/MySQL revision gates for the additive source snapshot fields. The PR production fixture proves form/routing; real fork PR execution remains a separate live acceptance.

The production browser fixtures cover repository-first PR Jobs → historical Job → pinned creation → deployment detail/reload. Focused UI tests additionally cover Branch/Tag encoded names, old links, delayed catalogs, and pin reset on source changes. Run `task ui-production-smoke`; fixtures do not prove a real fork PR Sandbox execution.

Route migration coverage additionally verifies readable slash queries, literal percent escapes and reserved characters, repository/branch Jobs filtering (including empty results), repository-only deployment/creation links, old named-source URLs, OAuth return, refresh, back/forward and historical resource selection. Run `bun test src/url-query.test.js src/github-context-routes.test.js src/components/auth-pages.test.js` in `ui/` and `task ui-production-smoke`.

Deployment URL checks also cover SHA-free Branch/Tag/PR detail URLs, source history without an implicit commit filter, explicit list commit filters, pinned Job creation, and GitHub links/full-SHA copying for the selected immutable revision.

PR title/link regressions: `TestFillDeploymentPullRequestTitlePreservesSnapshotsAndLifecycle`
and `TestPullRequestDeploymentPinsSourceAndRejectsInvalidContext` prove empty-only,
exact-ref title repair, timestamp preservation, repository authorization and local
detail reads before optional GitHub metadata. Deployment UI tests cover delayed
legacy title responses, current versus historical headings, and Branch/PR/Tag
links. The PR production fixture checks long titles, focus and narrow layouts.

Deployment authentication fixtures cover structured GitHub `REAUTH_REQUIRED`
responses from the list, detail, logs, Web terminal and creation form; runnerd
session expiry with a successful/failed session recheck; and ordinary forbidden
or rate-limit errors. Assert a single OAuth redirect with the complete source,
resource and revision destination, no false reauthentication for other errors,
and no delayed redirect after leaving the deployment page.

To verify an existing PR deployment against the running local API without creating
or stopping Sandboxes, use:

```bash
RUNNERD_DEPLOY_LIVE_METADATA=1 \
RUNNERD_DEPLOY_LIVE_BASE_URL=http://127.0.0.1:25500 \
RUNNERD_DEPLOY_LIVE_CONFIG="$PWD/runnerd.local.yaml" \
RUNNERD_DEPLOY_LIVE_ACCOUNT_ID="<configured-account-id>" \
RUNNERD_DEPLOY_LIVE_DETAIL_ID="<existing-pr-deployment-id>" \
RUNNERD_DEPLOY_LIVE_EVIDENCE=/tmp/deployment-metadata-evidence.json \
go test ./internal/server -run '^TestDeploymentLivePullRequestMetadata$' -count=1 -v -timeout 2m
```

This probe signs a short-lived in-memory session using the specified local config.
It checks anonymous rejection, authenticated details, the PR title endpoint,
retained revisions, persisted titles and unchanged lifecycle fields. Missing
legacy titles may be filled through the normal metadata endpoint. Evidence says
whether that backfill was exercised and records initial/warm request latency
separately. It does not test browser OAuth, layout, private Contents access,
second-account authorization, or live resource release.

## CI Runner account and repository concurrency

Operators configure optional limits in `runnerd.yaml` and restart runnerd:

```yaml
worker:
  max_concurrent_runners: 100
  account_concurrency_limits:
    example-org: 20
    example-user: 5
  repository_concurrency_limits:
    example-org/large-project: 4
```

An account means the GitHub user or organization owning the Job's repository,
identified by the owner part of its saved `repository_full_name`. It is not the
person triggering the workflow, the OAuth account, App Installation ID, credential
owner or fork sponsor. An organization's repositories share its account limit;
a fork uses its own owner and repository, alongside any existing sponsorship limit.
Manual requests and App reinstallations therefore use the same ownership limits.
All Runner Specs, including scoped custom Specs, count together within each owner
and repository. Keys are exact names, case-insensitive, without wildcard matching; owner logins
may include underscores for Enterprise Managed Users. Validation reports the
original key and limit in deterministic key order.
Update configuration when renaming an owner or repository or transferring a repository;
historical requests retain their saved ownership names until they finish.

Missing entries and `0` add no limit. Negative limits, invalid names and duplicate
keys after normalization reject startup. Global, Runner Spec, account and repository
limits must all allow admission. `creating`, `running` and `stopping` consume
capacity; queued and terminal requests do not. Pending creating writes are reserved
in memory; durable state supplies counts after restart. Failed writes release their
reservation. This coordination supports one runnerd process only.

At capacity, requests stay queued, release their worker lease and become eligible
again after five seconds. Claims are ordered by next eligibility time (or original
queue time for fresh requests), so expired deferrals rejoin behind already-waiting
requests instead of reclaiming their original position.
This does not consume the failure retry budget or promise equal scheduling shares.
`last_error_code` identifies `runner_capacity_global`, `runner_capacity_profile`,
`runner_capacity_account` or `runner_capacity_repository`; Job details show the
waiting message and the event timeline records changes under `runner_capacity`.
The reason clears on successful admission. Repeated checks of the same restriction
do not append duplicate capacity events. Capacity-only deferrals also skip timing
events to keep repeated polling from growing the timeline.

Configuration changes require restart; there is no live reload or new Settings UI.
Reducing a limit preserves existing Runner requests and stops new admissions until
usage falls below the new limit. Removing a limit or setting it to zero restores
unrestricted admission at that dimension, while other limits remain effective.
These settings do not change Sandbox Deploy quotas or repository authorization.

Local regression coverage includes stale count reads and pending writes across
Specs/repositories, independent scopes, failed persistence, durable counts on reopen,
and a backlog of restricted requests followed by an eligible repository. Real
GitHub/Sandbox acceptance must still verify completion, cleanup, restart with reduced
limits and waiting time under representative workloads.
