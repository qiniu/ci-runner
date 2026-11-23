# Runner usage reports

Administrators use `/admin/reports` to inspect request cohorts, recorded runtime,
trends, and usage by Runner Spec, GitHub Installation owner, and repository.
The filter bar above the report tabs applies the date range, timezone, repository
and time grain to both Overview and Specs. Switching tabs keeps those selections.
The Specs tab opens with two comparison charts showing each spec's share of
requests and recorded runtime. Each chart occupies its own row. The legend shows
exact values and shares beside the plot on wide cards and below it on narrow cards.
The runtime donut shows the total in its center and labels each visible slice.
Narrow plots use percentage labels with the full breakdown in the legend.
The catalog includes global, account and
organization specs. Repository and owner filters narrow usage, and an exact
spec filter also narrows the catalog.

A single usage report defaults to all specs. Its spec filter and the comparison
chart selections update the same report in place. The filter includes unused
catalog entries and historical identities, with search and paging for larger
catalogs. The report shows starts, repository coverage, mean and P95 runtime,
mean creation time, retained Job outcomes and a date trend. Repository and
owner rankings use the selected cohort's requests or recorded runtime as the
denominator. Each ranking includes its Top 5 and a metric-specific remainder.
A zero runtime denominator or missing sample displays a dash.

Table mode shows exact aggregate metrics for the current selection. Individual
requests are available on the admin Runner Requests page.

The overview uses Apache ECharts: a dual-axis time-series chart (request-count lines over translucent recorded-runtime bars in hours), spec and owner donut
charts, and horizontal repository bars. Distribution charts show the Top 5 by
request count or recorded runtime, with all remaining groups combined as Other.
Legends show exact values and full-cohort shares and allow filtering the entire
report. Missing runtime samples remain unavailable, distinct from valid zero
durations. The overview loads chart data only.
Charts load on demand, use the existing theme tokens, resize with their container,
and expose values in text legends and a collapsible trend table. The runtime
summary shows its valid record count with an accessible calculation tooltip;
field-by-field coverage remains available in the API rather than a separate
report-page panel. The compact layout uses shared small Cards: six summary
metrics and three distribution charts fit side by side on wide screens, while
narrow layouts wrap controls and stack charts. Chart legends, metric selectors,
filters and the trend data table remain available.

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
  time when today is unfinished. Date-count arithmetic uses the selected calendar
  labels before timezone conversion. Missing midnights resolve to the first valid
  instant after the gap; repeated wall times use their first occurrence. The same
  rule applies to comparison boundaries (including a missing corresponding clock
  time). A range containing no elapsed instants, such as one entirely skipped
  local date, is rejected.
- `queued_at ∈ [from,to)` selects the cohort. All metrics and trend buckets describe
  that cohort as observed in one database snapshot. Day/week/month buckets use
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
This API remains available for compatibility, but the report page does not fetch
or display it. Inspect current lifecycle counts on Runner Requests instead.

## API and limits

All endpoints require the existing administrator authorization, including cache
hits and drilldowns. Responses have `Cache-Control: no-store`. Only local narrow
projections are selected: no credentials, raw webhook payloads, logs, or provider
calls. The spec dashboard reads narrow identity and display fields from the global and
scoped catalogs inside the same report transaction.

| GET endpoint | Parameters and behavior |
| --- | --- |
| `/reports/overview` | `from`, `to`, `timezone`, `grain=day\|week\|month`, exact `owner`, `repository`, `spec`; optional `snapshot` to restore the identical result |
| `/reports/spec-usage` | Same filters as overview. Returns an independent snapshot with `spec_dashboard` counts, full-cohort summary and up to 20 spec records ordered by requests |
| `/reports/active` | Same query contract; date and grain do not restrict current backlog |
| `/reports/{snapshot}/specs` | `sort=requests\|runtime\|average_runtime\|repositories`, optional exact `key`, label search `q`, `limit`, `offset`; pages the frozen catalog and spec usage for a spec dashboard snapshot |
| `/reports/{snapshot}/groups` | `dimension=spec\|owner\|repository`, `sort=requests\|runtime`, optional label substring `q`, `limit`, `offset` |
| `/reports/{snapshot}/requests` | Optional `dimension` and exact `key`; `limit`, `offset`. No dimension returns the full captured cohort |

Overview returns normalized `query`, `window`, `observed_at`, `totals`, `previous`,
zero-filled `trend`, `snapshot`, `expires_at` and `distributions`. Each distribution includes its
group count and the first five groups by requests and by runtime. The dashboard
renders and switches rankings from this single response. Snapshot IDs used by
the pagination endpoints are cached in the serving process. Details return `items`, `total`,
`offset` and `snapshot`. Totals are computed in the server over the entire bounded
cohort, never over a browser page. Groups use the same immutable snapshot as the overview. Request pages are
available for snapshots that retain individual requests. All group shares use full
cohort denominators; chart remainders subtract displayed groups from full totals,
including runtime sample counts. A missing spec name is labeled Unknown while
preserving its saved identity. The request detail itself is live and may have
changed since the report snapshot.

For spec dashboard snapshots, the specs endpoint includes unused catalog entries
and historical groups. The PostgreSQL aggregate path computes spec-by-repository,
owner, date and Job outcome groups in the bounded report query. Average times
remain available from sums and valid sample counts. Exact P95 needs individual
samples and is unavailable on aggregate-only snapshots. The standard overview
keeps its original grouping sets.

- Detail snapshots retain up to 100,000 requests across the current and prior
  cohorts combined. For larger PostgreSQL cohorts, the overview and all three
  group distributions use database aggregates over the full selected period.
  The size probe and rollups share a read-only repeatable-read transaction.
  The chart snapshot contains aggregate values only. Its request-detail endpoint
  returns **422**; narrowing the dates or filters can produce a detail snapshot.
  More than 100,000 aggregate rows also returns **422**. SQLite and MySQL keep
  the original 100,000-request limit for all reports.
- Query deadline: 10 seconds, including waiting for a build slot. Identical
  concurrent queries coalesce; cancelling one browser subscriber does not cancel
  other subscribers. Errors are not cached. There is one historical build slot
  and one independent current-backlog slot per server.
- Overview reuse: 30 seconds. Snapshot retention: up to 5 minutes, 8 entries and
  200,000 retained request or group entries. Capacity eviction can expire an entry sooner. Current
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
`mode=ro`, runs no migrations, and closes after the read. Inside one transaction,
an interruptible statement selects at most 100,001 ordered request keys and
their owner mappings into a connection-local temporary table. The reader walks
those keys in order and looks up each request by primary key. For current backlog
the temporary table holds at most four status counts. The query deadline covers
the scan, sort and grouping as well as row reads. Rollback removes the temporary
table. WAL allows concurrent scheduler writes on the primary connection.
In-memory SQLite uses the original connection and retains its data on cancellation.
PostgreSQL and MySQL reuse their
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
  1 KiB historical payload per request. Initial local report read: **1.299 s**;
  simultaneous 25-row list: **1.276 ms**; simultaneous real queue claim:
  **2.012 ms** (baseline **0.812 ms**). With the original shared connection, the
  concurrent list/claim waited about **1.87 s**. The read-only connection removed
  that connection-pool blockage. Overflow at 100,001 is explicitly rejected.
  The scale test has a 10-second read deadline and a 500 ms local contention gate.
  These single-machine measurements are not production latency guarantees.
- Browser fixture coverage exercises URL restoration, chart metric selection and
  dimension filtering, full-cohort Other shares, missing versus zero runtime,
  absence of request-detail and current-backlog fetches, dual-axis trend selection, obsolete-response
  isolation, and desktop / 390px layouts with long identities, a keyboard-operated
  shared Select, empty groups and expired-snapshot errors. Fixtures do not prove live service or
  production-data acceptance.

The implementation reuses the shared Card, Button, Input, Label, Select and Table
components, following the GitHub App accounts page for headings, local errors and
read-only navigation. Desktop and narrow screenshots were visually inspected,
including the open Select, long group names and error states.

The chart revision passed frontend lint, i18n/type checks and the production
build, 364 frontend tests, the full Go race suite, and 58 production-bundle
browser cases. That revision also passed all 7 report-only cases after
adding multi-group chart, dual-axis trend, tooltip, dark-theme and narrow-layout
evidence. The report page no longer requests current backlog. A
focused race regression covers missing spec names without rewriting their saved
identity. These browser screenshots use fixtures; the existing local signed-in
browser and production-data rendering were not verified in this revision.
Build-producing checks must finish before browser verification to avoid changing
assets beneath a running preview server.

Follow-up verification on 2026-11-23:

- Disk and in-memory SQLite interrupted an executing scan in the overview and
  current-backlog paths under a 75 ms test deadline. A subsequent report read
  succeeded on each database. On the same 100,000-row synthetic fixture, the
  updated report read took 1.125 s, the concurrent list 1.479 ms and the queue
  claim 0.848 ms (0.633 ms baseline).
- The report contract passed on a dedicated PostgreSQL 16 test database. All
  eight report browser cases passed, including retaining the distribution metric
  when applying a filter, choosing a date preset, clearing dimensions and reloading.

Reproduce focused backend checks with `go test ./internal/state ./internal/server
-run TestRunnerReport -count=1`. Set `RUNNERD_REPORT_SCALE_TEST=1` for the scale
case. Real SQL compatibility requires `RUNNERD_CATALOG_BACKEND_TESTS=1` and
`RUNNERD_POSTGRES_TEST_DSN` / `RUNNERD_MYSQL_TEST_DSN` pointing at dedicated
`*_test` databases: those tests reset their tables. UI fixtures run via
`bun run test:production runner-reports.pw.ts` in `ui/`.

Production data completeness, actual row/cardinality distributions, production
query plans/latency and deployed browser acceptance remain **unverified**. Before
operational rollout, inspect API coverage with a representative approved
snapshot or a bounded read-only production query, record the range and sample
size, and measure scheduler latency under the real workload. If legitimate ranges
exceed the explicit cap or the cost is excessive, design bounded preaggregation or
an asynchronous reporting store with evidence; do not silently raise the cap or
mix attempt history and period consumption into this contract.
