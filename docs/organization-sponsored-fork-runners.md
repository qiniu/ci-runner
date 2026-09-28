# Organization-Sponsored Fork Runners

Status: local implementation is complete on `feat/organization-sponsored-forks`;
real-fork deployment acceptance remains pending.

## Problem And Product Boundary

Jobs running in an organization repository already use that repository's
GitHub App installation and effective Sandbox service, including
`pull_request` jobs whose head comes from a fork. A workflow run directly in a
member's personal fork uses the fork's personal installation and currently
requires that user to configure a personal Sandbox service.

The new capability is **organization sponsorship**, not credential inheritance:

- An organization owner enables sponsorship for an exact upstream repository.
- The job remains registered against the fork. Only Sandbox provisioning uses
  the sponsor's service.
- Credentials and provider resources remain hidden from contributors.
- New policies are disabled until explicitly enabled.
- Existing global and Runner Spec limits still apply. Each policy also has a
  positive concurrent-run limit.

Policies support three eligibility modes:

| Mode | Eligibility |
| --- | --- |
| `approval_required` | The exact fork repository has an owner-created approval. |
| `write_permission` | The personal fork owner currently has `write`, `maintain`, or `admin` permission on the upstream repository. |
| `organization_member` | The personal fork owner is currently an active member of the sponsoring organization. |

`approval_required` is the recommended and UI default mode. Automatic modes
apply only to personal fork owners; organization-owned forks require an exact
approval so sponsorship cannot cross into another organization implicitly.

## Trust And Resolution

Runtime decisions use GitHub API metadata, never repository names supplied by a
browser or inferred naming conventions.

1. Resolve the queued request repository through the fork's installation.
2. Require `fork: true`, stable repository and owner IDs, and an original
   `source` repository.
3. Load an enabled policy by the source repository's stable ID.
4. Confirm the source owner still matches the sponsoring organization
   installation. Repository transfer fails closed.
5. Re-evaluate the configured eligibility mode immediately before Sandbox
   creation. GitHub lookup failure fails closed for that attempt.
6. Keep GitHub I/O outside startup locks, then use a short per-policy critical
   section to recheck the policy and exact approval, enforce capacity, and save
   the request snapshot atomically within one runnerd process.
7. Snapshot the encrypted Sandbox configuration and credential-free sponsorship
   provenance on the Runner request.

Managed Runner request resolution order becomes:

1. saved request snapshot;
2. fork installation configuration;
3. personal account fallback;
4. eligible organization sponsorship;
5. eligible platform default;
6. not configured.

Corrupt fork or personal configuration remains an error and does not fall
through. Sponsorship applies only to runnerd-managed Runner Specs. Platform
custom and scoped custom Specs preserve their owning-scope credential contract.

## State Model

`fork_sponsorship_policies` stores sponsor installation, stable source repository
ID and full name, mode, enabled state, positive maximum concurrency, and
timestamps. Sponsor installation plus source repository ID is the primary key;
source repository ID is globally unique so one fork network has one sponsor.

`fork_sponsorship_approvals` binds a policy to the stable fork repository ID,
full name, owner ID, and owner login. Runtime still validates the current
fork/source relationship so transfers and fork-network changes fail closed.

Sponsored Runner requests retain sponsor installation ID, source repository ID
and full name, and the authorization reason. The encrypted Sandbox key remains
in the existing hidden snapshot fields. Every fresh retry, interrupted-creation
requeue, and mismatched-job requeue clears both snapshots so revoked or changed
policies are evaluated again.

## API And UI

Owner-manageable organization scopes expose:

- `GET /user/fork-sponsorship-repositories?installation_id=...` lists
  non-fork repositories owned by the organization and visible through both the
  signed-in user token and the selected GitHub App installation.
- `GET /user/fork-sponsorship-policies?installation_id=...`
- `POST /user/fork-sponsorship-policies?installation_id=...` resolves a new source repository by full name.
- `PUT /user/fork-sponsorship-policies/{source_repository_id}?installation_id=...`
- `DELETE /user/fork-sponsorship-policies/{source_repository_id}?installation_id=...`
- `POST /user/fork-sponsorship-policies/{source_repository_id}/approvals?installation_id=...`
- `DELETE /user/fork-sponsorship-policies/{source_repository_id}/approvals/{fork_repository_id}?installation_id=...`

Repository metadata is validated before an audited database transaction. Data
and audit evidence commit atomically; rejected writes leave no audit event.

Organization Settings adds a **Fork sponsorship** tab. Personal account routes
do not show it. Policy creation uses a searchable organization-repository
picker. Eligibility is one mutually exclusive mode, and exact fork approvals
are shown only for the approval-required mode. The page also manages enabled
state and concurrency without exposing credentials or catalogs.

Sponsorship never broadens Settings access or ordinary-user Job access beyond
the existing exact `(installation_id, repository_full_name)` intersection.

## Deliberate Exclusions

- Organization Cache S3 is not inherited. The existing fork/account resolver is
  used, and sponsored jobs run without Cache S3 when none is configured.
- Organization custom Runner Specs, physical template IDs, and Runner Groups are
  not shared with personal forks.
- Sponsorship grants no Settings, provider catalog, audit-log, or unrelated
  repository access.
- Historical jobs are not backfilled or reclassified.

## Failure And Revocation

- Invalid or absent fork metadata and disabled/ineligible policies behave as no
  sponsorship and allow the existing platform-default resolution to continue.
- An eligible sponsor with corrupt Sandbox configuration fails closed; the
  platform default must not hide the owner configuration error.
- Policy capacity defers the request with a retryable result instead of changing
  the billing source.
- Deleting or disabling a policy blocks new starts and fresh retries but does not
  terminate an already running Sandbox.
- Cleanup and recovery use the request snapshot so revocation cannot orphan an
  already-created Sandbox.

## Verification

Coverage must include GitHub metadata and permission clients; fresh and old
SQLite schema; PostgreSQL/MySQL-ready audited mutations; all eligibility modes;
transfer, revocation, capacity and retry behavior; managed-only enforcement;
owner/member/collaborator API authorization; UI route isolation; and English /
Chinese i18n parity.

Local gates are `go test ./internal/state -count=1`, focused GitHub/server tests,
`task ui-i18n-check`, `task test`, and `task ui-production-smoke`. Dedicated
PostgreSQL/MySQL databases and a production SQLite snapshot remain external
release gates. Production acceptance requires an approved fork run and a
rejected control run with cleanup and provenance evidence.

## Delivery Status

- [x] Confirm current installation and Sandbox resolution behavior.
- [x] Define product, trust, state, API, and verification boundaries.
- [x] Add GitHub metadata and permission clients.
- [x] Add state models, audited mutations, and migration coverage.
- [x] Add owner-only policy APIs.
- [x] Add runtime resolution and request provenance.
- [x] Add Organization Settings UI and localization.
- [x] Synchronize operator, testing, architecture, and agent documentation.
- [x] Pass local verification gates.
- [ ] Complete real-fork deployment acceptance.
