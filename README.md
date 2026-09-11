<h1 align="center">Qiniu Sandbox GitHub Runner</h1>

<p align="center">
  <strong>Ephemeral, isolated GitHub Actions runners powered by Qiniu Sandbox</strong>
</p>

<p align="center">
  <a href="./README.zh.md">中文</a> ·
  <a href="https://runner.qiniuinc.com/docs/getting-started/hosted">Hosted Guide</a> ·
  <a href="#quick-start">Quick Start</a> ·
  <a href="https://app-6a6b0d723d3a24e095531129.app.qiniucc.com/">Deploy to Qiniu LAS</a> ·
  <a href="https://runner.qiniuinc.com/docs/getting-started/deploy">Deployment Guide</a> ·
  <a href="https://runner.qiniuinc.com/docs">Documentation</a> ·
  <a href="#license">License</a> ·
  <a href="#community--contributing">Community &amp; Contributing</a>
</p>

---

Qiniu Sandbox GitHub Runner provisions a clean [Qiniu Sandbox](https://www.qiniu.com/) for each GitHub Actions workflow job, registers a [self-hosted runner](https://docs.github.com/en/actions/hosting-your-own-runners/about-self-hosted-runners) just in time, and removes the runner and sandbox when the job ends. Teams keep the familiar GitHub Actions workflow while moving each job into a disposable environment.

The Qiniu CI Runner control plane is open source. Qiniu Sandbox, where workflow jobs execute, is a cloud service provided and operated by Qiniu.

## Core Capabilities

- **Ephemeral runners** — one sandbox per job, automatically cleaned up after completion
- **GitHub App auth** — recommended production path with OAuth sign-in for the built-in web console
- **Multi-database** — SQLite (default), PostgreSQL, or MySQL for runtime state
- **Concurrency control** — global `max_concurrent_runners` and per-spec `max_concurrency` with queue-based backpressure
- **Built-in web UI** — admin console for runner requests, global runner specs, accounts, the platform Sandbox fallback, audit, matching, and diagnostics; ordinary-user console for job groups, logs, repository readiness, a platform Runner Spec catalog, scoped Sandbox management, and account/Organization custom Runner Specs
- **Config obfuscation** — sensitive values can be hidden from casual config inspection
- **Retry & recovery** — transient failures are retried with backoff; queued work and active remote runners are recovered after a service restart

## How It Works

```
GitHub webhook (workflow_job)
        │
        ▼
   ┌─────────┐     create sandbox      ┌──────────────────┐
   │ runnerd  │ ──────────────────────►  │  Qiniu Sandbox   │
   │ (server) │     register runner     │  (ephemeral VM)  │
   │          │ ──────────────────────►  │                  │
   └─────────┘                          │  GitHub Actions  │
        │                               │  self-hosted     │
        │  job completed / timeout      │  runner          │
        │◄────────────────────────────── │                  │
        │     stop & cleanup sandbox    └──────────────────┘
        ▼
   state DB (sqlite / postgres / mysql)
```

1. GitHub sends a `workflow_job` (queued) webhook to runnerd.
2. runnerd applies the repository allowlist, then matches the job labels against enabled Runner Specs.
3. runnerd creates a Qiniu Sandbox instance and registers a self-hosted runner inside it.
4. GitHub Actions dispatches the job to the runner; the job executes in the sandbox.
5. When the job completes (or times out), runnerd removes the runner registration and stops the sandbox.

## Quick Start

```bash
# 1. Build
task build

# 2. Create config from example
cp runnerd.yaml.example runnerd.yaml
#    Edit runnerd.yaml: set database, GitHub App credentials, sandbox settings

# 3. Bootstrap the first admin (one-time, exits without starting the server)
./bin/runnerd --bootstrap-admin github:<github-user-id> --config runnerd.yaml

# 4. Start runnerd
./bin/runnerd --config runnerd.yaml
```

5. Open `http://<host>:25500/` and sign in with GitHub OAuth. The public product landing page links to the same-origin `/docs` guides and the protected Jobs console at `/jobs`. On the first authenticated visit to `/jobs`, a six-step product tour introduces Jobs, Repositories, Settings, and Sandbox setup; it can be replayed from the account menu.
6. Open **Repositories** to review **Runner readiness** for the account or organization. Ready sources are shown without configuration controls. If Sandbox setup is missing and you can manage that scope, use **Configure Sandbox** to open the exact account or organization **Preferences** page and configure **Sandbox Service** credentials. Settings lists only your account and organizations where you are an active member; outside collaborators receive a read-only readiness prompt and cannot browse that organization's Sandbox catalogs. Administrators can provide a fallback at `/admin/sandbox_service`.
7. Confirm the five built-in managed Qiniu Runner Specs in the **Admin Console**. The four standard public templates have passed the two-region release gate. The four `-large` templates are public operator-configured default Runner Specs backed by the 80-GiB physical templates; their records use the custom-spec path but are enabled for ordinary workflow use. Operators can disable managed or large default specs and adjust their concurrency and idle capacity.
8. Configure a GitHub webhook → `POST http://<host>:25500/webhooks/github`.
9. Use `runs-on: [qiniu, ubuntu-24.04]` for a managed default, or use the labels required by your custom spec.

For local development, use `task dev` with `runnerd.local.yaml`. See [docs/testing.md](docs/testing.md) for detailed local setup including GitHub App creation and webhook forwarding.

## Configuration

`runnerd` reads `./runnerd.yaml` by default, or the path passed with `--config`. See [`runnerd.yaml.example`](runnerd.yaml.example) for a fully commented reference.

| Section | Description |
| --- | --- |
| `server` | Listen address, read/write/idle timeouts |
| `database` | Backend (`sqlite` / `postgres` / `mysql`) and DSN |
| `auth` | Session secret, encryption key, session TTL |
| `sandbox` | Sandbox lifecycle timeouts (create, run, stop) |
| `github` | Webhook secret, auth method (App / PAT / basic), OAuth, allowed repositories |
| `worker` | Lease, retry, and concurrency settings |

Key notes:

- Relative `database.dsn` and `github.app.private_key_file` paths resolve from the config file's directory.
- Use SQLite for local and single-node deployments. PostgreSQL and MySQL are supported but multi-instance operation on a shared database has not been verified.
- CI creates fresh PostgreSQL and MySQL schemas and reruns migration against an existing catalog before exercising policy-free matching. See [docs/testing.md](docs/testing.md) for the opt-in local real-dialect command.
- Existing SQLite `runner_requests` and `runner_profiles` tables add missing model columns and indexes on startup without table recreation. This preserves historical runner values plus legacy profile rows and indexes. Creating missing indexes does not rewrite rows, but it can add brief startup I/O and lock contention on a large database; see [docs/testing.md](docs/testing.md) for migration and query-plan checks.
- GitHub Enterprise Server is **not** supported; use a GitHub.com App.
- Configure exactly one GitHub auth method: `github.app`, `github.token`, or `github.basic_auth`.
- When `github.app.installation_id` is omitted, runnerd resolves the installation dynamically per repository, allowing one App to serve multiple accounts.

### Config Value Obfuscation

Sensitive fields accept `RUNNERD_ENC(v1:...)` values to avoid plaintext in the config file:

```bash
read -r -s secret_value
printf '%s' "$secret_value" | ./bin/runnerd --obfuscate-config-value
unset secret_value
```

Supported fields: `database.dsn`, `auth.session_secret`, `auth.encryption_key`, `github.webhook_secret`, `github.token`, `github.basic_auth.password`, `github.oauth.client_secret`. These values are also masked as `******` in logs and serialized output.

> **Note:** This hides plaintext from casual inspection only — the decoding key is embedded in the binary. It is not encryption against a host-level attacker.

### Cache and S3

Each user configures a Cache S3 Bucket, optional Prefix, AK, and SK in account or GitHub installation Preferences. The default prefix is `gh-actions-cache`. The S3 region and endpoint are derived from the selected Sandbox service region and are configured by the operator in `runnerd.yaml` under `sandbox.regions`. Because the configured endpoint may be private to the Sandbox network, runnerd only validates the configuration shape when saving it; bucket reachability and permissions are verified by the actual workflow in the Sandbox. AK/SK are encrypted in scoped state and are never returned to the browser or runner.

On every sandbox start, runnerd resolves the GitHub repository to its installation/account scope, verifies the Workflow Run trust context, and mints a Qiniu IAM federation token through the configured `cache.sts_endpoint` (default `https://sts-ov.qiniuapi.com`). Its requested lifetime is the configured Sandbox lifecycle plus five minutes for the post-job cache save step; no refresh mechanism is provided yet. It injects the token plus bucket/endpoint/prefix as `AWS_*` / `RUNS_ON_S3_*` environment variables in the Sandbox start script, so the scoped cache action can upload and restore caches directly to the user bucket without proxying bytes through runnerd.

Cache object keys are isolated by repository and workflow context:

```text
<configured-prefix>/<owner>/<repo>/scopes/branch-<first-16-bytes-of-sha256(branch)-hex>/...
<configured-prefix>/<owner>/<repo>/scopes/pr-<number>/...
```

With the scoped `qiniu/actions-cache@v5` action, runnerd injects ordered read prefixes plus one write prefix. A trusted branch searches its own scope and then the default-branch scope, and writes only its own scope. A pull request, including a Fork PR, searches its PR scope, base-branch scope, and default-branch scope in that order, and writes only its own PR scope; this matches GitHub's merge-ref cache isolation so a Fork cannot poison a base-branch cache. `pull_request_target`, `workflow_run`, `issue_comment`, and unverified metadata are default-branch read-only. Kodo list operations are scoped to authorized prefixes via `kodo:prefix` Condition, so cache key names outside the granted scopes are not enumerable across credentials.

#### Operator configuration

```yaml
sandbox:
  regions:
    - id: us-south-1
      label: "United States · Dallas 1"
      sandbox_api_url: https://us-south-1-sandbox.qiniuapi.com
      s3_region: us-north-1
      s3_endpoint: https://internal-s3-las-us-north-1-dal.qiniucs.com

cache:
  sts_endpoint: https://sts-ov.qiniuapi.com
```

`sandbox.regions` defines the public Sandbox region catalog exposed through `GET /sandbox/regions`; S3 mappings remain server-side because endpoints can be private. Each entry must include `id`, `label`, and `sandbox_api_url`. `s3_region` and `s3_endpoint` are optional and must be configured together; only regions with both fields support Cache S3. At least one region is required. For stronger cache isolation, use a dedicated bucket per GitHub installation. `cache.sts_endpoint` is the Qiniu IAM federation token endpoint used to mint short-lived credentials.

#### Using cache in GitHub Actions workflows

Use [`qiniu/actions-cache@v5`](https://github.com/qiniu/actions-cache) instead of `actions/cache`. No additional configuration is needed in the workflow — runnerd injects S3 credentials plus ordered read prefixes and a single write prefix as environment variables:

```yaml
steps:
  - uses: qiniu/actions-cache@v5
    with:
      path: |
        ~/.cache/go-build
        ~/go/pkg/mod
      key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}
      restore-keys: |
        ${{ runner.os }}-go-
```

The upload/download concurrency can be tuned via environment variables (defaults shown):

```yaml
env:
  UPLOAD_QUEUE_SIZE: "16"    # concurrent multipart upload parts
  UPLOAD_PART_SIZE: "16"     # part size in MiB
  DOWNLOAD_QUEUE_SIZE: "16"  # concurrent range-request downloads
  DOWNLOAD_PART_SIZE: "16"   # part size in MiB
```

## GitHub App Setup

### Required Permissions

| Scope | Permission | Access | Purpose |
| --- | --- | --- | --- |
| Repository | Actions | Read-only | Query job/run status, list queued jobs, read logs; required for webhook events |
| Repository | Administration | Read & write | Repository-level runner registration (when spec has no `runner_group`) |
| Repository | Metadata | Read-only | Identify repositories and owners |
| Repository | Pull requests | Read-only | Show PR titles in job groups |
| Organization | Members | Read-only | Verify active organization membership for organization Settings and scoped Sandbox management |
| Organization | Self-hosted runners | Read & write | Organization-level runner registration (when spec sets `runner_group`) |

Set `github.app.slug` to show an "Install GitHub App" link in the user UI. Use `github.allowed_repositories` (patterns like `owner/repo` or `owner/*`) to restrict which repositories can use this runnerd instance.

### OAuth Sign-in

`github.oauth` enables GitHub App OAuth login for the built-in console:

- Use the GitHub App's **Client ID** and **Client Secret**.
- Set the App callback URL to `http://<host>:<port>/auth/github/callback`.
- Set `auth.session_secret` (session signing) and `auth.encryption_key` (user secret encryption) to separate random values.

First OAuth login creates a `role: user` account. Use `--bootstrap-admin <github-user-id>` to promote an account to admin.

### Webhook Events

In your GitHub App settings (**Settings → Developer settings → GitHub Apps → your app → General**), configure:

1. Set the **Webhook URL** to `https://<your-runnerd-host>/webhooks/github`.
2. Under **Subscribe to events**, check:
   - **Workflow jobs** (`workflow_job`) — **required**, triggers runner creation.
   - **Workflow runs** (`workflow_run`) — optional, acts as a compensating signal for missed `workflow_job` events.
3. Save changes.

> **⚠️ Common pitfall:** If no events are subscribed, GitHub will not send any webhooks and jobs will stay queued forever. This is configured in the **GitHub App settings**, not in the repository's webhook settings.

## Webhook & Workflow Setup

1. Ensure the GitHub App webhook is configured as described in [Webhook Events](#webhook-events) above, with the `webhook_secret` matching `github.webhook_secret` in your config.
2. Use a verified managed label pair, for example:

```yaml
runs-on: [qiniu, ubuntu-24.04]
```

The `qiniu` label is mandatory for managed defaults. A custom spec can define
its own advertised and required labels instead.

runnerd handles `queued`, `in_progress`, and `completed` actions. For `workflow_run`, it lists all queued jobs in the run and enqueues any matching jobs not already seen.

New runner requests, including admission rejections, store the parsed GitHub context but not the raw webhook body. The legacy `github_payload_json` column and existing values remain available for historical metadata backfill; this change does not clean up old payloads, request history, or logs.

## Runner Specs & Matching

Runner specs are managed through the admin API and console — not through `runnerd.yaml`. Every enabled spec is eligible for repositories admitted by `github.allowed_repositories`; labels select the spec.

- **Managed Runner Spec**: runnerd reconciles five built-in specs for
  `ubuntu-slim`, `ubuntu-22.04`, `ubuntu-24.04`, preview `ubuntu-26.04`, and
  `ubuntu-latest`. Their catalog labels, required labels, public template name,
  and priority are managed by runnerd. Operators retain
  `enabled`, `max_concurrency`, and `min_idle`.
- **Custom Runner Spec**: an operator-owned spec with an explicit
  `template_id`, advertised labels, and optional required labels and
  `runner_group`. The five `-large` workflow labels are public
  operator-configured default specs using this path; they are not part of
  runnerd's built-in managed catalog or public managed-template API.
  Creating a custom spec or changing its template validates access and a usable
  default build with the endpoint/key configured at
  `/admin/sandbox_service` (also when runtime fallback is disabled). Without
  those credentials, only managed defaults and unchanged-template edits are
  available. Validation failure rejects the save without a profile/audit change.
  Updating controls without changing the template does not contact Sandbox.
- **GitHub Runner Group**: when a spec sets `runner_group`, runnerd creates an organization-level runner in that GitHub group; otherwise it creates a repository-level runner. This is not the retired internal Runner Group model.

> **⚠️ Personal accounts:** `runner_group` requires the organization-level GitHub API. If the repository belongs to a personal account (not an organization), leave `runner_group` **empty** — otherwise runner registration will fail with a 404 error.
Matching always enforces `required_labels ⊆ job_labels ⊆ labels`. Managed
Ubuntu specs therefore require both `qiniu` and the exact OS label: neither
`[ubuntu-24.04]` nor `[qiniu]` is sufficient. Removing `qiniu` from a workflow
prevents managed-default routing; an operator can also disable an individual
managed spec in Admin without changing its reconciled catalog identity.

Internal Runner Groups and Repository Policies have been removed. Their legacy
management APIs now return `404 Not Found`, while old Admin bookmarks redirect
to Runner Specs. They are not part of supported configuration, matching, or
recovery behavior; any legacy database artifacts are ignored by current code.

Managed specs store a stable public template name. Immediately before runner
creation, runnerd resolves that name against the repository owner's scoped
Sandbox endpoint, so different regions can return different template IDs.
Custom specs continue to send their stored `template_id` directly.
`ubuntu-latest` maps to Ubuntu 24.04 in the current catalog revision; changing
that mapping requires a reviewed catalog update and new regional smoke
evidence.

See [Public Runner Templates](docs/default-runner-templates.md) for supported
workflow labels, publication status, and regional verification.

`GET /api/public/runner-templates` exposes the four standard runnerd-managed
public templates without authentication. Its stable response contains only each
public template name, logical Runner Spec names, and supported workflow label
sets; it never includes provider template IDs, credentials, endpoints, or
operator-configured large/custom templates. The public runner-labels guide still
documents those large defaults and their resource contract. The credential-bound
`GET /user/sandbox/templates?region=<id>` catalog remains a separate scoped
resource.

Ordinary users browse the read-only platform Runner Spec catalog at
`/runner-specs`. That page has no account/Organization selector or user-editable
availability and concurrency policy. Users manage only owned custom Specs under
`/account/runner-specs` or
`/organizations/{login}/runner-specs`. The authenticated `/user/runner-specs`
API combines runnerd-managed specs, read-only platform custom specs, and custom
specs owned by that account or manageable Organization. Platform availability
and concurrency remain global Admin policy. Scoped custom specs use exact
normalized workflow labels, may override a global spec with the same label set,
and validate new or changed template IDs only with that scope's explicit or
legally inherited Sandbox credentials. `runner_group` is available only for
Organization custom specs. The response exposes a template ID only for the
caller's own scoped custom spec, never for a platform custom spec. A queued
request reloads and validates the same persisted source and scope immediately
before startup, so a spec disabled while waiting cannot launch a runner.

An Admin-created custom Runner Spec is a platform-shared spec: it is available
read-only in every manageable account and Organization catalog. Admin creation
therefore uses explicit platform-wide copy and validates the template only with
the Admin Sandbox service. Use a scoped custom spec instead when an environment
must remain private to one account or Organization.

For custom specs, `template_id` should point to a Qiniu Sandbox template containing the GitHub runner image. Template access is checked against the repository owner's effective Sandbox service shown under **Repositories → Runner readiness** at sandbox creation time.

## Admin Console

The built-in web UI provides:

| Route | Description |
| --- | --- |
| `/admin/` | Dashboard with runnerd runtime diagnostics and metrics |
| `/admin/accounts` | Account management — list, search, and change roles |
| `/admin/runner_requests` | Runner request history, filters, controls, and exact lookup by Runner Name or internal request ID |
| `/admin/runner_requests/{id}` | One Runner request resource with persisted state, diagnostic findings, the GitHub Job result, and a cursor-paged timeline that shows every control/stdout/stderr event directly in chronological order |
| `/admin/runner_specs` | Managed and custom global Runner Spec administration |
| `/runner-specs` | Read-only platform Runner Spec catalog and workflow labels |
| `/account/runner-specs` and `/organizations/{login}/runner-specs` | Custom Runner Specs owned by an account or manageable Organization |
| `/admin/sandbox_service` | Sandbox service configuration |
| `/admin/match` | Label-match preview against the current enabled Runner Specs |
| `/admin/audit` | Audit event history |
| `/admin/diagnostics` | runnerd runtime diagnostics with redacted summary, pprof discovery, and on-demand expvar |

`/` is always the public Qiniu CI Runner product landing page. `/docs` and its fixed guide routes are public, same-origin, and available in English and Simplified Chinese. The ordinary-user Jobs homepage is `/jobs`; other protected routes include `/repositories`, PR job groups (`/github/pulls/{owner}/{repo}/{number}/jobs`), and account settings (`/account/preferences`, `/account/sandbox-templates`, `/account/sandbox-instances`), with matching `/organizations/{login}/...` routes. Opening a protected route without a session shows a focused GitHub sign-in page and returns to the original URL after OAuth.

Runner request lists return the newest 100 rows by default and cap pages at 500. They project only public runner-state fields instead of stored webhook payloads or Sandbox credentials. Admin polling uses the `(queued_at DESC, id ASC)` index; repository-authorized user polling queries each installation through `(github_installation_id, queued_at DESC, id ASC)` and merges the bounded results while preserving exact installation/repository access pairs.

## Troubleshooting

| Symptom | Likely Cause | Fix |
| --- | --- | --- |
| Job stays **queued** forever, no webhook in runnerd logs | GitHub App has no subscribed events | Go to GitHub App settings → Subscribe to **Workflow jobs** event |
| `github registration token: status 404` | `runner_group` is set but the repo owner is a personal account | Clear `runner_group` in the runner spec to use repository-level registration |
| `invalid signature` in logs | Webhook secret mismatch | Ensure `github.webhook_secret` matches the secret in GitHub App/repo webhook settings |
| `runner start deferred ... at capacity` | Global or per-spec concurrency limit reached | Wait for running jobs to finish, or increase `max_concurrent_runners` / spec `max_concurrency` |
| Sandbox creation fails | Repository owner has no effective Sandbox service | Open **Repositories**, select the account or organization, and complete **Runner readiness**; admins may also configure an eligible fallback at `/admin/sandbox_service` |
| GitHub reports that a self-hosted runner lost communication | The runner, Sandbox, or its network path stopped reporting heartbeats | Open **Admin → Runner Requests**, look up the Runner Name shown by GitHub (for example, `e2b-101445685709`), and inspect the request resource's findings and lifecycle timeline |

For detailed local debugging steps, see [docs/testing.md](docs/testing.md#8-troubleshooting-order).

## Docker

The container image uses file-config only. Mount `runnerd.yaml` and any referenced secret files into the container:

```bash
docker run --rm -p 25500:25500 \
  -v "$PWD/runnerd.yaml:/etc/runnerd/runnerd.yaml:ro" \
  -v "$PWD/secrets:/etc/runnerd/secrets:ro" \
  ghcr.io/qiniu/ci-runner
```

## Build & Development

```bash
task deps          # Install Go dependencies
task ui-deps       # Install UI dependencies
task build         # Build runnerd with embedded production UI
task ui-production-smoke # Execute the production UI bundle in Chromium
task dev           # Start local dev (runnerd + Vite + smee)
task lint          # Run linters
task test          # Rebuild UI + run all tests (Go with race detection + Bun UI tests)
task docker-check  # Verify Docker build
task release-check # Verify release build
```

For focused UI tests, run `cd ui && bun run test`. Use `task ui-production-smoke`
after changing UI dependencies, Vite/Rollup configuration, or production asset
loading.

### Sandbox Templates

| Template | Description |
| --- | --- |
| `templates/github-runner-ubuntu-slim` | Maintained Ubuntu Slim x64 runner template |
| `templates/github-runner-ubuntu-22.04` | Maintained Ubuntu 22.04 x64 runner template |
| `templates/github-runner-ubuntu-24.04` | Maintained Ubuntu 24.04 x64 runner template |
| `templates/github-runner-ubuntu-26.04` | Preview Ubuntu 26.04 x64 runner template |
| `templates/github-runner-ubuntu-slim-large` | Ubuntu Slim x64 runner template with an 80-GiB provider disk |
| `templates/github-runner-ubuntu-22.04-large` | Ubuntu 22.04 x64 runner template with an 80-GiB provider disk |
| `templates/github-runner-ubuntu-24.04-large` | Ubuntu 24.04 x64 runner template with an 80-GiB provider disk |
| `templates/github-runner-ubuntu-26.04-large` | Ubuntu 26.04 x64 runner template with an 80-GiB provider disk |

The public `ubuntu-latest-large` Runner Spec is a logical label mapped to the
`github-runner-ubuntu-24-04-large` physical template; it does not add another
template directory or build target.

Run `task template-check-all`, then use the eight
`task template-build-ubuntu-*` targets for real qshell Sandbox builds. See
[Public Runner Templates](docs/default-runner-templates.md) for publication and
cache-resume guidance after a remote build time limit, plus publication and
smoke commands.

## Documentation

| Document | Description |
| --- | --- |
| [Hosted site guides](https://runner.qiniuinc.com/docs) | Hosted quick start, runnerd deployment, workflow example, custom template lifecycle, troubleshooting, and managed labels |
| [docs/testing.md](docs/testing.md) | Local testing, GitHub App/OAuth setup, webhook forwarding, troubleshooting |
| [docs/deployment-smoke.md](docs/deployment-smoke.md) | Production-style readiness checklist |
| [docs/default-runner-templates.md](docs/default-runner-templates.md) | Public template labels, qshell release flow, regional smoke, and rollback |
| [docs/runner-architecture-comparison.md](docs/runner-architecture-comparison.md) | Architecture diagrams and comparison with ARC / Fireactions |
| [docs/runner-implementation-review.md](docs/runner-implementation-review.md) | Implementation status and schema migration notes |

## License

Qiniu CI Runner is licensed under the [Apache License 2.0](LICENSE).

## Community & Contributing

Bug reports, feature ideas, documentation improvements, and code contributions are welcome.

- [Report a bug or propose a feature](https://github.com/qiniu/ci-runner/issues).
- [Open a Pull Request](https://github.com/qiniu/ci-runner/pulls) to improve the code or documentation.
- Scan the QR code below to join the community chat.

---

<p align="center">
  <img src="./docs/assets/qrcode.png" width="220" alt="Qiniu CI Runner community chat QR code" />
</p>
<p align="center">
  <em>Scan the QR code to connect with maintainers and other Qiniu CI Runner users.</em>
</p>
