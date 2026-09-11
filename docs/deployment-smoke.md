# Deployment Smoke Checklist

[Chinese](zh/deployment-smoke.md)

Use this checklist before treating a runnerd deployment as ready for real GitHub Actions traffic.

## Prerequisites

- A runnerd deployment reachable over HTTPS for GitHub webhooks and console sign-in.
- A `runnerd.yaml` with `database`, `auth`, `github`, and `worker` sections configured.
- A GitHub.com App installed on the target repository or organization.
- The GitHub App has the [required repository and organization permissions](../README.md#required-permissions) for the runner modes used by this deployment.
- A GitHub App OAuth callback URL pointing at `/auth/github/callback` on the runnerd origin.
- A GitHub App webhook or repository webhook delivering `workflow_job` events to `POST /webhooks/github`.
- Sandbox service API URL and API key configured in the target account/organization Preferences page, or an enabled admin fallback at `/admin/sandbox_service`.
- All four standard public Qiniu templates built, published, catalog-checked,
  and smoke-tested in both supported regions according to
  [Public Runner Templates](default-runner-templates.md). The four large
  templates must pass the same gate before their development specs are used;
  do not deploy enabled managed defaults before the applicable gate passes.
- An admin account bootstrapped by running `runnerd --bootstrap-admin github:<github-user-id>` (sets the admin and exits; run before starting the service).

Do not use real secrets in this document or commit deployment-local files such as `runnerd.local.yaml`, `.smee-url`, sqlite databases, private keys, or cookie jars.

## 1. Service Health

Verify the service is reachable:

```bash
curl -fsS https://<runnerd-host>/healthz
```

Expected result: HTTP 200 with `status: ok`.

Verify the production UI cache and compression headers using the current hashed JavaScript or CSS path from `index.html`:

```bash
curl -sS -D - -o /dev/null https://<runnerd-host>/
curl -sS --compressed -D - -o /dev/null https://<runnerd-host>/assets/<current-hashed-asset>.js
```

Expected result:

- The HTML shell returns `Cache-Control: no-store`.
- Content-hashed files under `/assets/` return `Cache-Control: public, max-age=31536000, immutable`.
- Large JavaScript and CSS responses return `Content-Encoding: gzip` when the request accepts gzip, plus `Vary: Accept-Encoding`.
- Unversioned static files use a short browser cache instead of the immutable policy.

Run the production browser canary from a checkout with UI dependencies installed:

```bash
RUNNERD_UI_SMOKE_BASE_URL=https://<runnerd-host> task ui-production-smoke
```

Expected result: Chromium renders the public landing-page heading without page
errors, console errors, failed script/style requests, or an empty React root.
Unlike the HTTP checks above, this executes the deployed JavaScript chunks and
must pass before the deployment is marked ready or receives full traffic.

Log in through the admin console:

```text
https://<runnerd-host>/admin/
```

Expected result: GitHub OAuth completes and the signed session has `role: admin`.

With a signed-in user who has an active membership in an installed organization, open:

```text
https://<runnerd-host>/account/preferences
```

Expected result: the organization appears in the Settings scope list and opens
`/organizations/<login>/preferences`. This verifies that the GitHub App's
`Members: Read-only` permission has been approved for the installation. Repeat
with a repository-only collaborator: authorized repositories may remain visible
under `/repositories`, but the organization must not appear in Settings and its
scoped Sandbox mutations and catalog reads must be rejected.

Open the Accounts page with at least one secondary account:

```text
https://<runnerd-host>/admin/accounts
```

Check:

- Summary totals stay global while search, role filters, page size, and pagination change the account list.
- Linked GitHub identities load the avatar derived from their login and fall back to account initials if it is unavailable.
- The current administrator's role control is disabled.
- A `role: user` session is rejected by both the accounts list and role-update APIs, and an administrator's direct attempt to patch their own role returns a conflict.
- Changing a secondary account from `user` to `admin` takes effect immediately and creates an `account.role.update` audit event.
- With exactly two administrators and two signed-in sessions, concurrent cross-demotion attempts cannot both succeed; at least one administrator remains.
- After all role checks, use the surviving administrator to restore the original administrator if needed, then use the intended administrator to restore the secondary account's role.

## 2. Diagnostics

Open the diagnostics page in the admin console, or call:

```bash
curl -fsS -b "$COOKIE_JAR" https://<runnerd-host>/diagnostics/pprof | jq
curl -fsS -b "$COOKIE_JAR" https://<runnerd-host>/diagnostics/vars | jq
test "$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" https://<runnerd-host>/runner_groups)" = 404
test "$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" https://<runnerd-host>/runner_policies)" = 404
test "$(curl -sS -o /dev/null -w '%{http_code}' -b "$COOKIE_JAR" https://<runnerd-host>/diagnostics/catalog-migration-readiness)" = 404
```

Check:

- `github.auth_mode` is `app` for the recommended deployment path.
- `state.database` points at the intended sqlite, Postgres, or MySQL database.
- pprof discovery files and dump scripts are visible when the local pprof service is available.
- For a failed or suspicious request, `/admin/runner_requests/{id}` shows findings and a chronological event timeline that explain the observed outcome; local evidence remains available when the GitHub lookup fails.
- The retired Runner Group, Policy, and temporary catalog migration readiness APIs return `404`; `/admin/runner_groups` and `/admin/runner_policies` still redirect harmlessly to Runner Specs.
- A Runner Request persisted as `failed` with `failure_stage=admission` and `failure_reason=profile_labels_not_matched` is shown as **Not matched**, is excluded from the failed metric, can be filtered independently, and has no retry action. Genuine failed requests remain **Failed** and retryable where applicable.
- Existing workflow `runs-on` labels and enabled Runner Spec matching remain unchanged. Do not edit Catalog or Sandbox configuration as part of the Release C deployment.
- Verify `/runner-specs`, `/account/runner-specs`, and one manageable `/organizations/{login}/runner-specs` route. Confirm the top-level page is a scope-free, read-only platform catalog with copyable workflow labels and no enabled/concurrency controls, while Settings shows only owned custom Specs; confirm `/user/runner-specs` rejects repository-only scopes, exposes `template_id` only for that scope's own `scoped_custom` entries, and omits platform custom template IDs.

## 3. Runner Catalog

Before creating runner specs, verify Sandbox credential precedence:

- An account with no scoped credentials can list templates only while the admin default is enabled and complete.
- In `all` mode, both a personal repository owner and an organization owner can use the complete default.
- In `selected` mode, an owner on the stable-ID audience list can use the default, while an unselected owner and an empty audience cannot.
- Add a GitHub login that has never signed in or synchronized, and verify the admin response shows GitHub's canonical login, stable ID, and account type.
- With GitHub App auth enabled, verify the first selected-owner workflow resolves and caches an otherwise unknown installation owner; a later request should not require another owner lookup.
- Saving scoped account or organization credentials changes the effective source away from `admin_default`.
- Removing an audience entry blocks new fallback resolution without changing an already-snapshotted runner request.
- Disabling the admin default makes an otherwise unconfigured account fail with `sandbox service not configured`.

On startup, confirm runnerd reconciles exactly five managed specs without a
custom-name conflict:

```bash
curl -fsS -b "$COOKIE_JAR" https://<runnerd-host>/runner_specs |
  jq '[.[] | select(.managed_by == "qiniu/ci-runner") |
      {name, required_labels, default_template_name, enabled}]'
```

Expected names are `qiniu-ubuntu-slim`, `qiniu-ubuntu-22.04`,
`qiniu-ubuntu-24.04`, `qiniu-ubuntu-26.04`, and `qiniu-ubuntu-latest`.
Confirm startup logs contain no managed-profile name collision. In each
configured Sandbox region, run `task template-defaults-check` and retain the
eight physical-template IDs; runnerd must resolve the same stable name through that scoped
endpoint rather than persist one region's ID.
Separately verify each of the five enabled `-large` public default specs in
Admin; those labels are operator-configured and must not appear as managed
entries.

Run positive and negative match tests:

```bash
curl -fsS -X POST https://<runnerd-host>/runner_specs/match \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"repository_full_name":"<owner>/<repo>","labels":["qiniu","ubuntu-24.04"]}' | jq

curl -fsS -X POST https://<runnerd-host>/runner_specs/match \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"repository_full_name":"<owner>/<repo>","labels":["ubuntu-24.04"]}' | jq

curl -fsS -X POST https://<runnerd-host>/runner_specs/match \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"repository_full_name":"<owner>/<repo>","labels":["qiniu"]}' | jq
```

Expected result: only the first request selects `qiniu-ubuntu-24.04`.

Configure the admin Sandbox endpoint/key at `/admin/sandbox_service`, then
create one custom regression spec with an explicit template ID and operator
labels. Saving must query template access and its usable default build with the
admin credentials; running still uses the repository owner's effective Sandbox
configuration and stored template ID. Confirm the following without disrupting
existing jobs:

- A missing template returns `400 template_not_found` with no spec/audit change.
- A template without a usable default build returns `400 template_not_ready`.
- Missing admin credentials return `409 sandbox_service_not_configured`;
  unchanged-template custom edits remain available.
- Provider auth/errors/timeouts reject the save with an actionable error, not a
  false success. Retrying after correcting the template/configuration succeeds.
- An existing usable default is accepted while a newer rebuild is in progress.
- The custom spec can still be edited and deleted; test the real job separately.

Example successful create:

```bash
curl -fsS -X POST https://<runnerd-host>/runner_specs \
  -b "$COOKIE_JAR" \
  -H 'content-type: application/json' \
  -d '{"name":"deployment-custom","labels":["self-hosted","deployment-custom"],"required_labels":["deployment-custom"],"template_id":"<private-template-id>","max_concurrency":1,"enabled":true}' | jq
```

## 4. Webhook Delivery

In the GitHub App or target repository webhook settings, send a recent delivery or trigger a new workflow.

Expected result:

- The delivery uses `application/json`.
- The delivery includes a valid `X-Hub-Signature-256`.
- The runnerd response is a 2xx JSON response for supported `workflow_job` actions.
- Unsupported events are ignored intentionally, not treated as runner failures.

## 5. Workflow Pickup

Trigger one job for every logical managed label:

```yaml
name: runnerd-smoke

on:
  workflow_dispatch:

jobs:
  slim:
    runs-on: [qiniu, ubuntu-slim]
    steps:
      - run: uname -a
  ubuntu_22:
    runs-on: [qiniu, ubuntu-22.04]
    steps:
      - run: uname -a
  ubuntu_24:
    runs-on: [qiniu, ubuntu-24.04]
    steps:
      - run: uname -a
  ubuntu_26_preview:
    runs-on: [qiniu, ubuntu-26.04]
    steps:
      - run: uname -a
  latest:
    runs-on: [qiniu, ubuntu-latest]
    steps:
      - run: |
          uname -a
          whoami
          pwd
```

Trigger it manually.

Expected result:

- A runner request appears as `queued`, then `creating`, then `running`.
- All five GitHub Actions jobs leave the queued state and run on ephemeral
  managed runners. The two 24.04 logical labels resolve the 24.04 physical
  template through the scoped region catalog.
- Each job starts the requested OS; the 26.04 job is recorded as preview
  acceptance.
- The job's `Set up runner` log includes the Qiniu sandbox id, runner request id, and runner name.
- After the job finishes, the runner request becomes `completed`.

Reference evidence: on 2026-08-04 CST,
[run 30858489153](https://github.com/miclle/qiniu-ci-runner-test/actions/runs/30858489153)
accepted signed GitHub `workflow_run` and `workflow_job` deliveries and completed
all five jobs. The five requests reached `completed`, each runner process exited
cleanly, each Sandbox was cleaned, and the repository had zero self-hosted
runners afterward. This reference run does not replace verification that the
deployment's own webhook secret matches its configured GitHub webhook.

## 6. Restart Recovery

Add a step that runs long enough to restart runnerd while the job is active. Restart only the runnerd service; do not stop the sandbox directly.

Expected result:

- `/healthz` remains available during startup recovery; other HTTP routes return `503` until recovery finishes.
- Active requests recover with bounded concurrency instead of waiting for every earlier request serially; each worker derives its per-request sub-budget from the remaining whole-startup timeout and remaining worker-wave count, while the parent deadline remains the hard limit.
- Startup recovery finishes before runnerd starts its worker loops and accepts new queued work.
- A `running` request remains `running`, keeps the same sandbox ID and runner PID, and records a successful reconnect event. A recoverable `creating` request may discover and persist the sandbox ID and runner PID that were not saved before restart.
- The GitHub Actions job continues without returning to the queue or losing its runner.
- A queued request with a lease owned by the previous process becomes eligible for the new worker.
- A temporary GitHub or sandbox status lookup failure is logged without stopping the existing sandbox.

## 7. Cleanup

After the workflow completes, verify:

- The Qiniu sandbox has stopped or is no longer active.
- The GitHub self-hosted runner registration has been removed or is offline and cleaned up by runnerd.
- The runner request has control/stdout/stderr logs available from the admin UI or `/runner_requests/{id}/logs/{name}`.
- `/diagnostics/vars` shows updated workflow job, runner registration, cleanup, and duration counters.

## 8. Failure Drill

Run controlled routing and operator-control checks while the deployment is
still under observation:

- Trigger `[ubuntu-24.04]` and `[qiniu]`; both must remain unmatched.
- Disable `qiniu-ubuntu-24.04` in Admin, restart runnerd, and confirm it remains
  disabled. Re-enable it and confirm scheduling resumes.
- Lower a managed spec's concurrency and trigger two jobs.
- Run the `deployment-custom` spec, confirm its explicit template ID is used,
  then delete it and confirm managed-spec delete still returns conflict.

Expected result depends on the scenario:

- unmatched labels or disabled specs are recorded as admission failures;
- reconciliation preserves the operator-controlled disabled state across
  restart;
- concurrency pressure leaves later requests queued rather than dropped;
- retryable placement or rate-limit failures populate `next_retry_at` and remain eligible for later processing.

If routing or template health regresses, disable all managed specs and any
enabled `-large` custom specs first. Do not delete public templates or custom
specs during rollback.

Record any deployment-specific notes outside the repository if they include private hosts, account names, channel URLs, secrets, or cookie data.
