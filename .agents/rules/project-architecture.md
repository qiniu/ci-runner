# Project Architecture

## Runtime Shape

- `runnerd` is a single Go service that receives GitHub `workflow_job` webhooks, admits jobs by repository and labels, creates Qiniu sandboxes, registers ephemeral GitHub Actions self-hosted runners, and cleans them up.
- Runtime config is file-first. `runnerd` reads `./runnerd.yaml` by default, or another path passed with `--config`.
- Local development should use `runnerd.local.yaml` for secrets and sqlite state.
- Sensitive scalar config fields use `config.Secret` and accept plaintext or `RUNNERD_ENC(v1:...)`. Obfuscated values are decoded while loading, and callers must use `Value()` explicitly; default text, slog, JSON, and YAML output stays masked. This is only display/log obfuscation because runnerd embeds the decoding key, not an authorization or host-compromise boundary.
- Relative sqlite `database.dsn` and `github.app.private_key_file` paths resolve from the directory containing the config file. Legacy `database.url` remains a deprecated alias when `database.dsn` is empty.

## Admin And UI

- `/` always serves the public Qiniu CI Runner landing page. `/jobs` is the protected ordinary-user Jobs homepage. The landing page links to Jobs and documentation without loading protected user resources.
- Ordinary-user job group routes use a source-context path with the jobs view as the terminal resource, such as `/github/pulls/{owner}/{repo}/{number}/jobs`; individual runner job details remain `/jobs/{id}`.
- `/repositories` is the canonical ordinary-user readiness surface for GitHub App repository access, local job activity, and effective Sandbox service status. It never embeds the Sandbox credential editor: a missing manageable source links to the exact account or organization Preferences route, while outside collaborators receive a read-only prompt to contact an active organization member. `/account/repositories` and `/organizations/{login}/repositories` remain compatible scoped links to the same page. Settings lists only the signed-in account and manageable organization scopes; repository-only organizations stay on `/repositories` and are not Settings navigation options.
- `/api/public/runner-templates` is unauthenticated, cacheable runnerd-owned metadata for the four managed public templates. It exposes only stable public template names, logical Runner Spec names, and workflow labels. Keep it independent from `/user/sandbox/templates` and `/user/sandbox/instances`, which resolve encrypted Sandbox credentials from the selected account or GitHub installation scope, then the enabled admin default when the scope is incomplete. Installation-scoped catalog reads require the same manageable-scope authorization as credential mutations. They are ordinary-user catalog APIs, not admin configuration APIs.
- The current browser entry for the admin console is `/admin/`.
- `/admin/accounts`, `GET /admin/api/accounts`, and `PATCH /admin/api/accounts/{id}/role` provide a role-gated account list and role controls. Accounts remain OAuth/bootstrap-created; linked identities are read-only display/search data on this admin surface, while provider plus stable subject still binds authentication to the local account.
- Account role updates and their audit events commit atomically. Self-role changes and changes that could leave no administrator are rejected, including concurrent demotions.
- `/admin/sandbox_service` and `/admin/api/sandbox-service-default` manage the platform fallback. Keep this singleton independent from account preferences and disabled by default.
- UI source lives in `ui/`.
- Production UI assets are generated into `internal/server/ui/` by `task ui-build` and embedded by `internal/server/ui_assets_production.go`.
- Development UI assets are proxied to Vite by `internal/server/ui_assets_development.go`.
- Shared `ui/` code serves both ordinary-user and admin screens. Keep admin routes and role-gated APIs explicit when changing shared components.

## Auth And Routing

- The recommended production GitHub auth path is GitHub App auth for runner operations plus GitHub App OAuth sign-in for ordinary users and administrators. Local account roles gate management APIs.
- Protected browser routes render a focused sign-in page after session loading and preserve the full same-origin destination through OAuth `return_to`. Authenticated non-admin users see an explicit access-denied page on admin routes, and unknown routes render 404.
- Ordinary-user Jobs authorization is repository-level, not installation-level. Resolve the user's repository intersection for every account-linked GitHub App installation with the stored GitHub user access token, preserve exact `(github_installation_id, repository_full_name)` pairs, filter before query limits, and reuse the same authorization for lists, details, groups, logs, and terminals. Missing or rejected user tokens must fail closed; an inaccessible installation contributes no authorized repositories.
- Token and basic auth still exist as compatibility modes; their long-term product status is undecided.
- GitHub Enterprise Server is not supported. Config validation rejects `github.api_base_url` values other than `https://api.github.com`.
- Global Runner Specs are admin API/UI data, while `/user/runner-specs` exposes the read-only platform catalog together with account or manageable Organization custom Runner Specs. The ordinary-user `/runner-specs` page is a scope-free read-only platform catalog; account and Organization Settings Runner Spec routes show only their owned custom Specs. Internal Runner Groups and Repository Policies were removed completely in Release C: their APIs and state models no longer exist, no current feature or recovery path may depend on legacy database artifacts, and they must never participate in admission.
- The ordinary-user catalog exposes raw `template_id` and `runner_group` only for the selected scope's own `scoped_custom` entries. Managed entries use the stable public template name and expose global policy as read-only, while `platform_custom` entries stay read-only without exposing their physical template IDs. `runner_group` is an Organization-only scoped custom field. Keep custom-list loads, provider-template loads, mutations, and their follow-up refreshes tied to the query scope that started them.
- Admin custom Runner Spec create/template-change validation uses the saved admin Sandbox endpoint/key, independently of runtime fallback enabled/audience controls. It does not read user/organization credentials. GetTemplate proves access; the owned or public default catalog's nonzero effective BuildID proves a usable uploaded default. Latest BuildStatus and detail history are not equivalent to the selected default. Missing credentials, unknown build state, and provider errors reject before the audited transaction under a five-second provider deadline; responses exclude upstream bodies. Unchanged-template edits make no provider calls. Runtime scoped resolution is unchanged. Admin saves use conditional writes against the original updated_at (insert-only for absent specs); concurrent changes/deletes produce 409 without audit or stale overwrites.
- Sandbox credential precedence is request snapshot, installation custom/inherited config, eligible personal account config, enabled and audience-eligible admin default, then not configured. Corrupt scoped config must fail instead of falling through.
- Admin default audience mode is `all` or `selected`. Selected entries match the repository owner's stable GitHub numeric account ID and account type, never the workflow actor or organization membership; an empty selected audience matches nobody.
- Admin audience additions resolve a typed GitHub login to canonical stable ID/type through GitHub before persistence; login remains display metadata. Installation sync and runtime GitHub App lookup both populate the separate installation-owner cache used for selected-audience matching.
- `github.allowed_repositories` limits admitted repositories before enabled Runner Spec label matching.

## State And Migrations

- Runtime state can use sqlite, Postgres, or MySQL.
- New accepted and rejected runner requests persist parsed GitHub context, not raw webhook bodies. Keep `github_payload_json` and existing values for legacy metadata/installation-ID backfill; startup must not clear them or drop the column. Historical cleanup is a separate maintenance operation, and request/log retention is unchanged.
- Do not document multi-instance support until two runnerd processes have been verified against the same database.
- State schema is defined mostly by GORM tags in `internal/state/records.go`.
- Startup migration runs a narrow legacy compatibility pass in `internal/state/db.go`, then GORM `AutoMigrate`. Existing SQLite `runner_requests` and `runner_profiles` tables use additive missing-column/index migration instead of generic table recreation so ALTER-added runner-request fields and legacy runner-profile rows and indexes remain intact.
- Validate Runner Spec names at both state write boundaries after trimming: reject `/`, `.`, and `..`. Keep migration, reads, listing, and matching tolerant of historical rows with those names; do not rename, delete, disable, or otherwise repair them automatically.
- GORM foreign-key creation is intentionally disabled. Legacy compatibility may remove old constraints or reset incompatible legacy scope tables; keep every such action narrow and covered by old-schema tests. Pre-scope account preference/secret rows are intentionally erased, requiring Sandbox reconfiguration and GitHub reauthentication before installation sync.
- Keep old-schema upgrade tests when changing state records, GORM tags, indexes, required columns, or relationship constraints.
- Do not reintroduce legacy `users` migration behavior unless the user explicitly asks for that compatibility path.

## Runner Lifecycle

- Runner request states are `queued`, `creating`, `running`, `stopping`, `completed`, and `failed`.
- Startup recovery clears stale leases for queued requests, reconnects creating/running requests to their existing sandbox runner process when present, requeues an interrupted creation when its sandbox is absent and its GitHub job is not in progress, and reserves destructive sandbox cleanup for stopping, completed, or definitively missing work. Recover at most four requests concurrently; when each worker starts a request, derive its timeout from the remaining whole-startup budget and remaining worker waves, cap it at 30 seconds, and treat the parent deadline as the hard limit. Do not dispatch when no budget remains; when cancellation interrupts dispatch, report every skipped request. Keep `/healthz` available during recovery, but return `503` for other HTTP routes and do not start worker loops until recovery finishes. Temporary recovery lookup failures must preserve the remote workload.
- Global `worker.max_concurrent_runners` and per-spec `max_concurrency` are enforced by worker processing; excess work stays queued.
- Before starting claimed queued work, reload the request's persisted profile source and scope, then validate the latest global or scoped-custom enabled state and requested labels. Admission-time profile data is not sufficient to start a runner after the relevant spec changes.
- Transient Qiniu sandbox placement failures, HTTP 429s, and GitHub secondary rate limits are queue deferrals. Deterministic auth/config/template failures should fail immediately.
- Control/stdout/stderr logs are persisted as runner events and exposed through the admin API/UI.
