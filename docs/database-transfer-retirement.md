# SQLite to PostgreSQL transfer retirement

The one-time `runnerd-migrate` command and its offline/phased transfer implementation have been retired. Current builds produce only `runnerd`. SQLite, PostgreSQL and MySQL remain supported; normal schema upgrades and historical data compatibility are unchanged.

## Historical procedure

The production transfer was recorded complete on 2026-11-02: cutover and backfill exited successfully, `phase=complete`, `history_complete=true`, and both pending counters were zero. The completion report recorded 17,928,514 verified historical events. Service and release configuration were confirmed to use PostgreSQL. See [the completion record](https://github.com/qbox/sandbox/issues/535) (repository access required).

The implementation and instructions remain available at the fixed pre-retirement revision:

- [Phased transfer runbook](https://github.com/qiniu/ci-runner/blob/def63260e4ba16f903f8d750c61915ceb68733ad/docs/sqlite-postgres-phased-migration.md)
- [Offline transfer runbook](https://github.com/qiniu/ci-runner/blob/def63260e4ba16f903f8d750c61915ceb68733ad/docs/sqlite-postgres-offline-migration.md)
- [Transfer command source](https://github.com/qiniu/ci-runner/blob/def63260e4ba16f903f8d750c61915ceb68733ad/cmd/runnerd-migrate)

These are historical procedures, not commands for the current release. An unfinished transfer must use its matching tool/model version and original recovery protocol. Never run an old importer against a newer live schema or replay a completed cutover.

## Retained startup protection

Before schema upgrades or backfills, runnerd still rejects sealed SQLite sources, unreleased PostgreSQL targets, unknown transfer protocols, and unfinished legacy online migrations. It accepts protocol 1 SQLite sources in `copying` and PostgreSQL targets in `ready` or `complete`, preserving the previous startup contract. Existing legacy standalone markers remain valid. Retired `database.online_migration` configuration is still rejected.

Startup only reads these markers; it does not remove transfer tables, triggers, functions, backups or fences. These guards are separate from normal GORM schema upgrades, additive SQLite upgrades, and Runner Spec policy compatibility.

## Remaining operational work

Source-code retirement does not perform production database cleanup. Before a separately scheduled cleanup:

1. Verify PostgreSQL backup and restore procedures and retain the final transfer report, sealed source SQLite, first backup, configuration and keys according to the retention policy. The source SQLite is not a lossless rollback target after PostgreSQL accepts writes.
2. Read the actual database catalog and completion markers. The phased tool leaves target `runnerd_transfer_guard` / `runnerd_transfer_truncate_guard` triggers and the `runnerd_transfer_guard` function installed after `complete`; normal writes still execute the row guard. Inventory their dependencies before proposing any DDL.
3. Review a transactionally consistent removal of only the completed target's transfer objects, including dependency order and lock budget. Do not drop a control table while a surviving trigger still references it. Preserve source fences, business rows and sequence watermarks, and verify application startup and writes afterward. Do not add automatic cleanup to application startup.

PG backup/restore verification and production object removal are not established by the source cleanup. Production transfer acceptance also does not establish multi-instance operation or uninterrupted Web Terminal sessions.
