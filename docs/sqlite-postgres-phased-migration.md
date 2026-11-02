# Phased SQLite to PostgreSQL migration

Use `runnerd-migrate` to pre-copy while SQLite runnerd is online, stop briefly for final synchronization, then backfill historical events while PG serves business traffic. The existing [offline importer](sqlite-postgres-offline-migration.md) remains the default `--mode offline`. See the detailed [Chinese runbook](zh/sqlite-postgres-phased-migration.md).

| Mode | SQLite service | PG service | Result |
| --- | --- | --- | --- |
| `prepare` | May run | Stopped | Private online backup, source capture, bound identity, fenced empty target |
| `sync` | May run | Stopped | Refresh other tables, resume request baseline, replay changes, optionally copy events |
| `status` | According to phase | According to phase | Durable checkpoints, pending work and readiness |
| `cutover` | Stopped | Stopped | Seal source, synchronize final changes, reserve sequences, release PG |
| `resume-source` | Stopped | Stopped | Unseal SQLite only if PG has never been released |
| `backfill` | Sealed/stopped | May run | Append and verify missing historical events only |

## Online preparation

Build both binaries with this protocol. Keep the original SQLite configuration and prepare a separate protected PostgreSQL configuration with the same encryption/session keys. Use a dedicated empty target schema and credentials permitted to create tables, functions, triggers and sequences. Never start the PG service during preparation.

The source must already use WAL. `prepare` takes an online `VACUUM INTO` backup without a migration-wide writer lock; it still consumes CPU/I/O and a long read may grow WAL. Rehearse under representative load. The backup directory must be owned by the current user with mode `0700`; files are exclusive `0600` and never overwritten.

```sh
install -d -m 700 ./var/offline-backups
./runnerd-migrate --mode prepare \
  --config ./runnerd.postgres.yaml --sqlite "$PWD/var/runnerd.db" \
  --backup "$PWD/var/offline-backups/online-before-postgres-20261102.db" \
  --timeout 2h > ./var/offline-backups/prepare.json

./runnerd-migrate --mode sync --skip-events \
  --config ./runnerd.postgres.yaml --sqlite "$PWD/var/runnerd.db" \
  --timeout 2h > ./var/offline-backups/sync.json

./runnerd-migrate --mode status \
  --config ./runnerd.postgres.yaml --sqlite "$PWD/var/runnerd.db" \
  > ./var/offline-backups/status.json
```

Omit `--skip-events` to pre-copy logs too. Requests and events use verified 256-row batches, committing data and checkpoints together. Timeout or interruption retains committed progress. Other tables refresh in individual consistent-snapshot transactions, including deletions. The CLI pauses `10ms` between batches; increase `--batch-pause` to reduce sustained load. Cutover does not pause.

Terminal requests are not immutable: exit evidence and GitHub results may arrive later, failed requests can retry, and startup backfills may not advance timestamps. SQLite triggers capture changed request keys, including deletes. Events must remain append-only: update/delete or out-of-order explicit IDs are rejected while migration capture is active.

`ready_for_cutover` means the request baseline is complete and final work fits the configured row budgets. It is not a shutdown acknowledgement or a complete-history flag. `requests_copied` counts baseline rows, `pending_request_changes` counts journal entries (including repeated keys), `final_table_rows` counts the other tables, and `pending_event_rows` is observed remaining event history. Live-source status is advisory; cutover rechecks its budgets.

## Final switch

Pause admission, arrange webhook buffering/redelivery, and handle active Runner/Deployment work through the normal maintenance procedure. Stop every SQLite writer and restart supervisor; keep PG runnerd stopped.

```sh
./runnerd-migrate --mode cutover --source-stopped \
  --config ./runnerd.postgres.yaml --sqlite "$PWD/var/runnerd.db" \
  --timeout 10m > ./var/offline-backups/cutover.json
```

Defaults allow up to `10000` pending request journal entries and `100000` combined rows in other tables. Excess work is rejected before sealing SQLite. Catch up online first; adjust `--max-final-changes` and `--max-final-rows` only after rehearsal. A row budget does not guarantee a duration. Cutover does not make another full backup, reread all verified request contents or wait for historical log transfer.

Only after exit code zero, `ready_for_pg: true` and `source_sealed: true` should one PG runnerd start. Verify health, sign-in, deduplication/recovery and real task lifecycles before restoring admission; redeliver and reconcile missed webhooks. All requests must be migrated for deduplication and recovery. Only historical events may remain incomplete.

Cutover persistently fences the SQLite business tables, refreshes other tables and changed requests, checks convergence, and reserves all sequence high-watermarks including untransferred event IDs before releasing PG writes. Repeating cutover after release does not overwrite data or reset sequences.

## Backfill and recovery

```sh
./runnerd-migrate --mode backfill \
  --config ./runnerd.postgres.yaml --sqlite "$PWD/var/runnerd.db" \
  --timeout 2h > ./var/offline-backups/backfill.json
```

Retain the original sealed SQLite and its WAL/SHM lifecycle. Backfill inserts missing events at their original IDs, requires identical content on conflicts, and never changes requests, settings, authorization, new PG events or sequences. Resume until `history_complete: true`. Verification covers the frozen historical prefix, excluding new PG events. Historical logs may be incomplete in the UI until then.

- Failed prepare/sync: SQLite can continue serving; PG remains fenced. Retry with the same identity. Before capture is installed, a failed backup needs a fresh path. After binding, repeated prepare resumes PG initialization without replacing the backup.
- Failed cutover: inspect `status`; SQLite may already be sealed. While PG remains `copying`, retry cutover or run `--mode resume-source --source-stopped` with both services stopped, then restore SQLite service and catch up again later.
- PG `ready`/`complete`: resuming SQLite is refused, even if an operator believes no PG writes occurred. After uncertain commit acknowledgement, inspect status rather than assuming rollback. No lossless reverse migration is provided.
- Failed backfill: PG may continue serving; diagnose and retry. Conflicting data is not silently skipped.

Do not change the source file, restore an earlier backup over it, upgrade model versions, edit capture/guard metadata or create another source writer during the operation. Source identity, target connection/schema, model fingerprint and guard definitions are checked. An advisory lock serializes importers. Database owners can still bypass guards with DDL; this is not protection against deliberate database-owner tampering. The initial online backup is an earlier snapshot, not a replacement for the final sealed source. Extra historical tables/columns stay in the retained source and backup.

This is not runtime dual-write, automatic switching, reverse replication or multi-instance runnerd support. Unfinished legacy online migrations still require their original finalization protocol. Progress/heartbeats go to stderr, result JSON to stdout. Timeout applies to one invocation; phased mode intentionally commits batches instead of promising whole-database rollback.

## Validation

```sh
RUNNERD_CATALOG_BACKEND_TESTS=1 RUNNERD_POSTGRES_TEST_DSN='<dedicated _test DSN>' \
  go test -race ./internal/state ./cmd/runnerd-migrate \
  -run 'Test(PhasedMigration|OfflineMigration|MigrationProgress|RunPhasedMigration)' -count=1
```

Random-schema PostgreSQL tests cover live writes, timestamp-independent changes, deletes, resumable batches, interrupted preparation, failed-cutover recovery, fences, budgets, conflict rejection and preservation of new PG writes. Production 5.2-GB timing, WAL/disk growth, daytime load, backup restoration and real Runner/Deployment cutover still require rehearsal.
