# Runner usage reports

Administrators use `/admin/reports` to inspect request cohorts, recorded runtime,
trends, and usage by Runner Spec, GitHub Installation owner, and repository.
The overview includes paginated groups and request drilldowns. The request detail
page returns to the original filters, group, page and snapshot. This is the first
report surface; dedicated spec, account/repository and efficiency analysis remain
future work.

## Metric contract

The unit is a persisted Runner request, not a GitHub Job or execution attempt.
Retries retain the request ID and do not increment request counts. Lifecycle
fields can be cleared by retries: the report describes the current/last retained
attempt, not complete historical execution usage.

| Metric | Definition / eligible denominator |
| --- | --- |
| Requests | Every row in the selected `queued_at` cohort, including rejected, unknown and historical rows |
| Started requests | Rows with a nonzero, nonfuture `running_at`; not all requests that ever started |
| Admission rejections | `status = failed` and `failure_stage = admission` |
| Active GitHub accounts / organizations | Distinct positive stable GitHub account IDs mapped from the request's Installation, among all cohort requests; not login activity |
| Active repositories | Distinct nonempty saved repository names in the cohort |
| Recorded runtime | Valid terminal samples, `running_at` to `stopping_at`, or status-specific terminal time when no stopping timestamp exists; denominator is terminal requests with a valid running timestamp |
| Queue to running | `running_at - queued_at`; denominator is requests with a valid running timestamp |
| Creating to running | `running_at - creating_at`; denominator is requests with a valid running timestamp |
| Cleanup | Terminal time minus `stopping_at`; denominator is terminal requests with a valid stopping timestamp |
| Queue to terminal | Terminal time minus `queued_at`; denominator is terminal requests |
| Retained Job result coverage | Completed, nonempty saved original-Job conclusions with a valid observation time, divided by terminal requests with a positive original Job ID |

Completed requests use `completed_at`, failed requests use `failed_at`. Missing,
Go-zero, future or inverted interval endpoints are unavailable, never substituted
with `updated_at` or interpreted as zero-duration samples. Runtime additionally
requires a valid terminal timestamp and cannot end after that timestamp. A real
zero-duration interval remains valid. Sums use unrounded seconds; the UI formats
hours only after aggregation. `samples`, `eligible`, and `seconds` accompany every
duration; missing samples equal `eligible - samples`.

Runner completion does not imply Job success. Runtime may include idle Runner
time. It is neither precise Job execution time, complete retry usage, calendar-
period consumption nor billable resource usage. No result is fetched from GitHub
when reading a report.

Account, spec and creating/running/stopping timestamp coverage use all cohort
requests as their denominator. Terminal timestamp coverage uses terminal requests.
These field-presence counters are separate from valid interval samples: two valid
timestamps can still form an inverted, unusable interval. Empty denominators and
runtime totals without valid samples display as unavailable.

## Time and identity

- `from` and exclusive `to` are local `YYYY-MM-DD` dates in an explicit IANA
  `timezone` (default UTC). Default range: the last 30 calendar dates including
  today; presets: 7/30/90 days. Custom ranges: 1–366 dates, no later than tomorrow's
  local midnight. `Local` is rejected. The current range ends at the observation
  time when today is unfinished.
- `queued_at ∈ [from,to)` selects the cohort. All metrics and trend buckets describe
  that cohort as observed during one database SELECT. Day/week/month buckets use
  the same timezone; weeks start Monday. Empty periods are included. Calendar
  labels advance independently of local midnight instants, so midnight DST jumps
  cannot omit an unfinished final date or shift week/month buckets. Requests
  that finish later can change past-date metrics on refresh.
- The prior cohort is shifted backward by the selected number of calendar dates.
  An unfinished current period is compared with the corresponding local clock
  time in the prior period. DST therefore need not yield equal elapsed seconds.
  It is a comparison of retained state at the current observation time, not a
  reconstructed historical snapshot. Count zero baselines display “New” for
  positive current values; unavailable duration baselines remain unavailable.
- Accounts use `github:<stable-ID>`. Missing owner mappings use
  `installation:<saved-installation-ID>` (including 0). The unique local
  `github_installation_owners` join never expands platform user-installation links.
  Current cached mappings are the available evidence, not complete historical
  ownership. Renames/transfers without evidence are not inferred.
- Spec keys are canonical unpadded base64url JSON of `source`, `scope_type`,
  `scope_id`, and `name`. Global specs normalize scope to empty/0 because saved
  request scope identifies their consumer, not a different platform spec; global
  spec filters therefore include all consuming scopes. Scoped-custom specs with
  the same name in different scopes stay separate. Deleted
  specs, unknown sources and missing identities remain in totals and groups;
  current spec catalogs never overwrite history. A known spec requires a nonempty
  name and global source, or a supported scoped-custom identity with positive ID.
- Repository identity is the exact saved name. Missing names remain a group with
  an empty key; no rename/transfer merging is attempted.

Current backlog is a separate read of queued/creating/running/stopping requests,
without the cohort date filter. Owner, repository and spec filters still apply.
Its own observation time is visible. It is never added to historical totals.

## API and limits

All endpoints require the existing administrator authorization, including cache
hits and drilldowns. Responses have `Cache-Control: no-store`. Only local narrow
projections are selected: no credentials, raw webhook payloads, logs, provider
calls or current catalog scans.

| GET endpoint | Parameters and behavior |
| --- | --- |
| `/reports/overview` | `from`, `to`, `timezone`, `grain=day\|week\|month`, exact `owner`, `repository`, `spec`; optional `snapshot` to restore the identical result |
| `/reports/active` | Same query contract; date and grain do not restrict current backlog |
| `/reports/{snapshot}/groups` | `dimension=spec\|owner\|repository`, `sort=requests\|runtime`, optional label substring `q`, `limit`, `offset` |
| `/reports/{snapshot}/requests` | Optional `dimension` and exact `key`; `limit`, `offset`. No dimension returns the full captured cohort |

Overview returns normalized `query`, `window`, `observed_at`, `totals`, `previous`,
zero-filled `trend`, `snapshot` and `expires_at`. Details return `items`, `total`,
`offset` and `snapshot`. Totals are computed in the server over the entire bounded
cohort, never over a browser page. Groups and request pages use the immutable
current-cohort rows retained with that same snapshot. All group shares use full
cohort denominators; the UI explicitly counts requests outside the displayed page.
The request detail itself is live and may have changed since the report snapshot.

- Current and prior cohorts together: at most 100,000 requests. Read 100,001 to
  detect overflow, then return **422**, never a truncated total.
- Query deadline: 10 seconds, including waiting for a build slot. Identical
  concurrent queries coalesce; cancelling one browser subscriber does not cancel
  other subscribers. Errors are not cached. There is one historical build slot
  and one independent current-backlog slot per server.
- Overview reuse: 30 seconds. Snapshot retention: up to 5 minutes, 8 entries and
  200,000 retained rows. Capacity eviction can expire an entry sooner. Current
  backlog reuse: 5 seconds, at most 8 dimension-filter entries.
- Detail pages: default 20, maximum 100 rows; offsets 0–100,000. Group order is
  stable, with request count and identity tie-breakers. Requests are ordered by
  queued date descending, then ID ascending.
- Invalid parameters: **400**. Query timeout: **504**. Missing/expired snapshot or
  changed filters on restoration: **410**, requiring an explicit refresh. A
  refresh may reuse a result within its freshness window; timestamps stay visible.
- Caches are process-local. A restart or request sent to another process may
  return 410. This does not establish multi-instance support. Never silently
  substitute another snapshot while preserving an old drilldown page.

SQLite uses the existing queued-date index and a short-lived read-only connection
to the main database file resolved from the initialized connection. It opens with
`mode=ro`, enables `query_only`, runs no migrations, and closes after the read.
WAL permits concurrent scheduler writes while a SELECT retains its snapshot.
This avoids occupying the normal single-connection pool for the report scan.
In-memory SQLite retains the original connection. PostgreSQL and MySQL reuse their
configured pools. No schema or index changes are introduced. On disk, historical
payloads are still physically colocated in SQLite table pages; projecting narrow
columns avoids decoding/returning them, but is not a claim of zero physical I/O
for pages containing those columns.

## Verification and rollout evidence

Local verification on 2026-11-20:

- SQLite, PostgreSQL 16 and MySQL 8.4 ran the same identity, filtering, lifecycle,
  result, immutable-drilldown and comparison fixture assertions. No production
  database was used.
- The fixture's current cohort has 5 requests, 4 started, 1 rejection, 1 mapped
  active account and 2 repositories. Owner coverage is 4/5; known spec coverage is
  2/5. Runtime is 4,200 seconds from 2/3 eligible samples; retained Job results are
  1/1 eligible. A completed Runner with Job result `failure` stays a failed Job.
  These are synthetic contract examples, not platform usage figures.
- SQLite query-plan tests exercise both date ranges and require the existing
  `idx_runner_requests_queued_id` index. The implementation adds no schema or
  preaggregation based solely on hypothetical load.
- A synthetic 100,000-request SQLite database has 20 repositories and an extra
  1 KiB historical payload per request. Final local report read: **1.299 s**;
  simultaneous 25-row list: **1.276 ms**; simultaneous real queue claim:
  **2.012 ms** (baseline **0.812 ms**). With the original shared connection, the
  concurrent list/claim waited about **1.87 s**. The read-only connection removed
  that connection-pool blockage. Overflow at 100,001 is explicitly rejected.
  The scale test has a 10-second read deadline and a 500 ms local contention gate.
  These single-machine measurements are not production latency guarantees.
- Browser fixture coverage exercises URL restoration, same-snapshot drilldown and
  return, independent backlog loading, obsolete-response isolation, and desktop /
  390px layouts with long identities, a keyboard-operated shared Select, empty
  requests and expired-snapshot errors. Fixtures do not prove live service or
  production-data acceptance.

The implementation reuses the shared Card, Button, Input, Label, Select and Table
components, following the GitHub App accounts page for headings, local errors and
read-only navigation. Desktop and narrow screenshots were visually inspected,
including the open Select, long group names and error states.

The final local checks passed: `task lint`, `task ui-i18n-check`, `task test`
(362 frontend tests and the full Go race suite), focused report race tests, and
all 54 production-bundle browser cases. The browser run initially had one
unrelated page-navigation 404 while another check rebuilt the same output
folder; its trace recorded that document 404. The other 53 cases passed, and the
single affected case passed when rerun after builds stopped. Future browser
checks should run after build-producing checks, not concurrently with them.

Reproduce focused backend checks with `go test ./internal/state ./internal/server
-run TestRunnerReport -count=1`. Set `RUNNERD_REPORT_SCALE_TEST=1` for the scale
case. Real SQL compatibility requires `RUNNERD_CATALOG_BACKEND_TESTS=1` and
`RUNNERD_POSTGRES_TEST_DSN` / `RUNNERD_MYSQL_TEST_DSN` pointing at dedicated
`*_test` databases: those tests reset their tables. UI fixtures run via
`bun run test:production runner-reports.pw.ts` in `ui/`.

Production data completeness, actual row/cardinality distributions, production
query plans/latency and deployed browser acceptance remain **unverified**. Before
operational rollout, inspect the displayed coverage with a representative approved
snapshot or a bounded read-only production query, record the range and sample
size, and measure scheduler latency under the real workload. If legitimate ranges
exceed the explicit cap or the cost is excessive, design bounded preaggregation or
an asynchronous reporting store with evidence; do not silently raise the cap or
mix attempt history and period consumption into this contract.
