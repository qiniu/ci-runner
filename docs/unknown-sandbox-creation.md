# Unconfirmed Sandbox creation

runnerd records a provision attempt and dispatch before contacting the Sandbox
service. A timeout or lost response does not prove that creation failed. Recovery
searches using the exact request and attempt metadata. An empty list is not a
negative creation receipt: a delayed create can still appear later, and a resource
that has expired may no longer appear in the current list.

## Automatic behavior and diagnostics

- Keep an unresolved dispatch in `creating` or `stopping`, retaining its capacity.
  Do not submit a replacement create because of a list miss, Job completion,
  elapsed Sandbox TTL, restart, or retry count.
- Persist a safe error category in `last_error_message` and a `sandbox_creation`
  control event when the creator still owns the attempt. The service log records
  the request/attempt/category even when ownership has changed. Categories include
  `deadline_exceeded`, `canceled`, `http_NNN`, `dns_error`, `network_timeout`,
  `network_error`, `response_incomplete`, and `unclassified_error`. Raw URLs,
  credentials, headers and provider response bodies are never copied.
- Recovery preserves that original category while scheduling another lookup.
  Historical records without it get the existing waiting explanation; deployment
  does not reconstruct missing historical errors.
- Each stopping-path lookup records a `sandbox_creation_lookup` control event
  in the existing request timeline and a `Sandbox creation lookup` service log.
  `outcome=not_found` means the exact-attempt lookup returned no verified resource;
  it records `matches=0` and the next scheduled retry, **not permanent absence**.
  `outcome=found` records `matches=1` before normal cleanup continues.
  `outcome=failed` records `matches=unknown` and a safe error category; `phase`
  distinguishes request loading, service configuration, provider lookup and
  retry checkpoint failures. `next_retry_at=unchanged` means this observation
  did not schedule a new retry. The original creation error remains intact.
  Only service logs include the internal attempt ID. Timeline insertion checks
  the current attempt and live cleanup lease atomically; a stale owner can log
  its observation with `event_recorded=false` but cannot add a timeline event.
  Evidence persistence failures do not complete or otherwise change the request.
- Admin diagnostics show unresolved creation and warn when stopping has lasted
  at least 15 minutes. This threshold is for review, **not automatic release**.
  `creation_attempt_id` is exposed only in the admin diagnosis response. The
  ordinary-user state does not expose the internal attempt or lease token.
- Periodic recovery says `reconciling runner lifecycle after startup or expired
  lease`; its event does not imply that the process restarted.

## Read-only GitHub Runner registration lookup

On the admin Runner request detail page, click **Check registration** to query
`GET /runner_requests/{id}/github-runner`. The lookup uses runnerd's existing
GitHub client (including GitHub App installation authentication), the saved exact
Runner name and the saved repository/organization registration scope. A configured
Runner group selects the organization scope; the displayed group is the request's
configuration, not a fresh verification of the registration's group membership.
The browser cannot override these targets. Ordinary users cannot call this API.

The response reports `found` or `not_found`, the lookup time, and, when found, the
numeric registration ID, online/offline status and busy flag. A 403/404 from
GitHub, rate limit, malformed response, timeout or other lookup failure is an
error, **not absence**. Exact-name lookup follows pagination and has a ten-second
request deadline. The response is not HTTP-cacheable; the UI queries only on an
explicit click and discards results when switching requests or attempts.

This endpoint does not acquire or renew cleanup leases, write request state,
events or audit records, call Sandbox, delete a GitHub registration, or resolve
unknown creation. It has no schema or configuration migration. A successful
empty result proves only that no same-name registration was found at lookup time;
it does not prove historical absence, Sandbox deletion or creation termination.
Do not use the resolution endpoint as a substitute for this read-only lookup.

## Long-term unknown results

Current list and lifetime information alone cannot safely prove permanent
absence. There is intentionally no TTL-based automatic completion. If an exact
resource is found, let the existing lease-protected cleanup remove it. If an
operation is still pending, retain the reservation and investigate it.

An administrator can explicitly end an eligible attempt in one of two ways:

- **Verified absence:** independently establish both that no Sandbox remains and
  that the original create cannot still complete. For example, record the
  authoritative create-operation outcome and matching resource deletion/expiry
  evidence in an incident record. An empty catalog, Job completion or age alone
  is not this evidence.
- **Accept the unknown result:** decide to close the request despite missing
  authoritative creation/resource evidence. Record the reason and explicit risk
  acceptance. This ends reconciliation and releases capacity; a delayed create
  or an unobserved remaining resource is still possible and will no longer be
  reconciled by this request. The record must continue to say the result is
  unknown, rather than claim verified absence or deletion.

Neither choice is automatic or inferred from age, retries or list misses. Both
require current external checks and the same lease/version/attempt fencing.
The service cannot verify the operator's independent evidence or risk decision.

Both modes use the existing explicit admin API; there is no bulk close or
change to the ordinary Stop button:

`POST /runner_requests/{id}/resolve-unknown-creation`

### Admin browser action

For an eligible request, `/admin/runner_requests/{id}` shows **Close with unknown
outcome** (中文：**接受未知结果并结案**). Open the dialog, enter a closure reason,
and explicitly confirm that a remaining or delayed resource may go unreconciled.
The reason is required and must fit the same 20–1024 UTF-8 byte limit as the API.
Canceling or reopening clears the reason and confirmation. The browser exposes
only the acceptance-of-uncertainty mode; verified absence remains an API action.

The admin diagnosis includes `unknown_creation_resolution_available`, a local
eligibility hint using the mutation handler's existing state checks. It does not
claim current Job completion, Runner absence, or ownership of a cleanup lease.
The browser reads the latest diagnosis again on submission, uses its current
version and refuses to transfer confirmation to a changed attempt. The service
then performs all live checks and the atomic state/audit write described below.

While submitting, the dialog prevents duplicate submissions and dismissal.
Navigating to another request discards the old dialog and does not submit after
an outstanding fresh-state read. A POST already sent may still commit after
navigation or a lost response: reload that request and check its audit before
deciding whether to submit again. Failed checks are displayed and never retried
automatically. Successful closure refreshes diagnosis and the existing timeline;
the request retains the unknown-outcome warning.

### API payloads

Use a named administrator's normal OAuth session, read the latest
`GET /runner_requests/{id}/diagnostics` response, and submit one of these payloads.

For verified absence (existing callers remain compatible):

```json
{
  "version": 123,
  "attempt_id": "exact creation_attempt_id from diagnosis",
  "evidence": "Incident reference and the evidence that this exact create cannot complete and no resource remains",
  "no_sandbox_or_pending_creation": true
}
```

For explicit acceptance of an unknown result:

```json
{
  "version": 123,
  "attempt_id": "exact creation_attempt_id from diagnosis",
  "evidence": "Incident reference and administrator decision to end reconciliation and release capacity despite possible delayed creation or remaining resources",
  "accept_unknown_result": true
}
```

Exactly one of `no_sandbox_or_pending_creation` and `accept_unknown_result` must
be true. Both true or both false/missing reject before acquiring a cleanup lease.
`evidence` contains the external proof for verified absence, or the administrator's
closure reason/risk acceptance for an unknown result. It is 20–1024 UTF-8 bytes
and is retained in the admin audit, not terminal output. Do not include secrets,
response bodies or credential-bearing URLs. Do not attest verified absence when
only accepting uncertainty.

The endpoint, in either mode:

1. Requires the exact current version and attempt, a stopping duration of at least
   15 minutes, no saved Sandbox/PID/running timestamp, and an original GitHub Job.
   Known numeric GitHub registration receipts or dispatched/confirmed Windows JIT
   registrations must use normal cleanup and are rejected here.
2. Claims the existing database cleanup lease, then makes bounded read-only
   external checks outside the transaction: exact Sandbox lookup must be empty,
   the original GitHub Job must be completed, and the Runner registration must be
   absent. Malformed GitHub registration responses, lookup errors, a running Job
   or a present resource reject the operation even when accepting uncertainty.
   A retained Job snapshot is not a substitute for this live check. No provider
   resource is created or deleted by this endpoint.
3. Atomically commits a completed state with the administrator, evidence, expected
   version, exact attempt and selected confirmation flag in a
   `runner.resolve_unknown_creation` audit event.
   The live lease, version and attempt are rechecked using database time. If a
   receipt, another owner, or another update won the race, the transaction fails.
   Audit insertion failure rolls back completion and capacity release.
4. Retains attempt identity, original termination ownership and the separate
   GitHub result. Verified absence records `sandbox_creation_resolved_absent`;
   accepting uncertainty records `sandbox_creation_closed_unknown`. It clears
   dispatch and retry/lease fields, releases capacity, and does **not** set a
   Sandbox-delete receipt. The latter code, timeline and English/Chinese admin
   finding explicitly retain the unknown outcome and unverified resource absence.
   Both remain `completed` request lifecycle records, independently of the Job
   result; this does not assert that the Job ran on this request or that runnerd
   observed a deletion. Requests and their histories are not deleted.

A 409 means refresh the evidence and state before deciding to retry. Do not loop
against stale versions. Failed preflight may renew/release the cleanup lease and
advance its version, but does not complete the request or release its capacity.
Repeated submissions after completion are rejected without another audit. Late
callbacks cannot overwrite the completed record. For verified absence, this is
not a substitute for proving no remote create is in flight. For accepted unknown
results, loss of automatic reconciliation for a later resource is part of the
explicit operator decision, not evidence that such a resource cannot appear.

## Deployment and existing backlog

This change uses existing state and audit columns, adds no schema migration or
configuration, and does not update records during startup. Deploying it only
improves future error evidence and diagnostics; it does not automatically close
historical requests. The original unknown-result recovery remains compatible
with the previous same-schema release. Keep both nodes on the same application
version before performing operator resolutions so the new diagnostics are
consistent. Rolling back does not undo already audited resolutions; the older
binary reads them as completed but lacks the new diagnostic explanation.

For a historical backlog, first agree on the exact request/attempt scope and
which resolution mode the administrator authorizes. Do not turn an unavailable
historical receipt into a verified-absence statement. Refresh each request before
submission and stop on conflicts or failed external checks rather than bypassing
them. After each successful closure, verify the saved resolution marker, audit,
completed state and released capacity. Publishing this capability does not
authorize any production closure; development and tests use disposable fixtures.

For a current stopping request, filter service logs before taking the tail so
browser polling does not hide cleanup observations. Run on both runnerd nodes
from their log directory (replace the example request ID):

```bash
grep -F '131669767108' qiniu-ci-runner.log.1 \
  | grep -v '"msg":"http request"' \
  | tail -n 60
```

After deploying lookup diagnostics, wait for the next normal retry and inspect
`sandbox_creation_lookup` in that request's timeline. No manual Stop or database
edit is needed to collect this evidence; deployment does not close the backlog.

## Verification

- `go test ./...`
- `go test -race ./internal/server ./internal/state ./internal/sandboxrunner`
- With **disposable** PostgreSQL/MySQL databases ending in `_test`, run
  `TestUnknownCreationResolutionSQLBackends` with the existing
  `RUNNERD_CATALOG_BACKEND_TESTS=1` backend test variables. This test writes only
  fixtures; it must never be pointed at production.
- Both modes cover delayed receipts, replaced/expired leases, stale versions and
  attempts, concurrent database connections, audit rollback, permissions,
  conflicting/missing confirmations, provider lookup failure, present resources,
  live or unavailable GitHub Jobs and malformed registration responses. Tests
  verify distinct provenance, retained history/results, released capacity and
  sensitive-error redaction. Empty lookup preservation without explicit closure
  remains covered; acceptance is never an automatic recovery policy.
