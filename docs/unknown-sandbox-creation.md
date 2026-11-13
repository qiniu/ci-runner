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
- Admin diagnostics show unresolved creation and warn when stopping has lasted
  at least 15 minutes. This threshold is for review, **not automatic release**.
  `creation_attempt_id` is exposed only in the admin diagnosis response. The
  ordinary-user state does not expose the internal attempt or lease token.
- Periodic recovery says `reconciling runner lifecycle after startup or expired
  lease`; its event does not imply that the process restarted.

## Long-term unknown results

Current list and lifetime information alone cannot safely prove permanent
absence. There is intentionally no TTL-based automatic completion. If an exact
resource is found, let the existing lease-protected cleanup remove it. If an
operation is still pending, retain the reservation and investigate it.

Only an administrator who has independently established **both** that no Sandbox
remains **and** that the original create cannot still complete may close an
unresolved attempt. For example, use the authoritative create-operation outcome
and matching resource deletion/expiry evidence, recorded in an incident record.
An empty catalog, a successful GitHub Job, age, or a guess is insufficient. If that
proof is unavailable, do not use the resolution endpoint. It cannot independently
verify an operator's external attestation.

The initial implementation provides one explicit admin API; it does not add a
bulk close or change the ordinary Stop button:

`POST /runner_requests/{id}/resolve-unknown-creation`

Use a named administrator's normal OAuth session, read the latest
`GET /runner_requests/{id}/diagnostics` response, and submit:

```json
{
  "version": 123,
  "attempt_id": "exact creation_attempt_id from diagnosis",
  "evidence": "Incident reference and the evidence that this exact create cannot complete and no resource remains",
  "no_sandbox_or_pending_creation": true
}
```

The evidence is 20–1024 UTF-8 bytes. Do not include secrets, response bodies, or
credential-bearing URLs. It is retained in the admin audit, not terminal output.
The endpoint:

1. Requires the exact current version and attempt, a stopping duration of at least
   15 minutes, no saved Sandbox/PID/running timestamp, and an original GitHub Job.
   Known numeric GitHub registration receipts or dispatched/confirmed Windows JIT
   registrations must use normal cleanup and are rejected here.
2. Claims the existing database cleanup lease, then makes bounded read-only
   external checks outside the transaction: exact Sandbox lookup must be empty,
   the original GitHub Job must be completed, and the Runner registration must be
   absent. Lookup errors or a present resource reject the operation. No provider
   resource is created or deleted by this endpoint.
3. Atomically commits a completed state with the administrator, evidence, expected
   version and exact attempt in a `runner.resolve_unknown_creation` audit event.
   The live lease, version and attempt are rechecked using database time. If a
   receipt, another owner, or another update won the race, the transaction fails.
   Audit insertion failure rolls back completion and capacity release.
4. Retains attempt identity, original termination ownership and the separate
   GitHub result; records `sandbox_creation_resolved_absent`. It clears dispatch
   and retry/lease fields, releases capacity, and does **not** set a Sandbox-delete
   receipt. Diagnostics continue to identify administrator resolution. This does
   not assert the Job ran on this request or that runnerd observed a deletion.

A 409 means refresh the evidence and state before deciding to retry. Do not loop
against stale versions. Failed preflight may renew/release the cleanup lease and
advance its version, but does not complete the request or release its capacity.
Repeated submissions after completion are rejected without another audit. Late
callbacks cannot overwrite the completed record; this is not a substitute for
proving no remote create is in flight before submitting the attestation.

## Deployment and existing backlog

This change uses existing state and audit columns, adds no schema migration or
configuration, and does not update records during startup. Deploying it only
improves future error evidence and diagnostics; it does not automatically close
historical requests. The original unknown-result recovery remains compatible
with the previous same-schema release. Keep both nodes on the same application
version before performing operator resolutions so the new diagnostics are
consistent. Rolling back does not undo already audited resolutions; the older
binary reads them as completed but lacks the new diagnostic explanation.

For the 2026-11-13 backlog, collect the 29 exact attempts and authoritative
operation/resource evidence first. The initial investigation only sampled four
detailed histories and observed an empty Sandbox column for all 29 requests.
It did not establish absence of remote resources. No production resolution is
part of development or automated testing. Continue to defer controlled node
failure exercises until the backlog and connection/capacity budget are understood.

## Verification

- `go test ./...`
- `go test -race ./internal/server ./internal/state ./internal/sandboxrunner`
- With **disposable** PostgreSQL/MySQL databases ending in `_test`, run
  `TestUnknownCreationResolutionSQLBackends` with the existing
  `RUNNERD_CATALOG_BACKEND_TESTS=1` backend test variables. This test writes only
  fixtures; it must never be pointed at production.
- The tests cover empty lookup preservation after prolonged waits, delayed
  receipts, replaced/expired leases, stale versions/attempts, concurrent database
  connections, audit rollback, permissions, invalid attestations, provider lookup
  failure, present resources, live GitHub Jobs and sensitive-error redaction.
