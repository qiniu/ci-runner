# SQLite to PostgreSQL migration runbook

Use the standalone `runnerd-migrate` in three stages: **online pre-copy → stopped-source cutover → historical-event backfill while PostgreSQL serves traffic**. Read the cutover and recovery boundaries before starting. Execute one stage at a time, not the entire document as a script. See the [Chinese runbook](zh/sqlite-postgres-phased-migration.md) for the same procedure.

Deploying new binaries while retaining the SQLite configuration does not start migration. For a full maintenance window with a single import transaction, use the separate [offline runbook](sqlite-postgres-offline-migration.md). Do not mix the protocols. Every command below supplies `--mode`; omitting it selects `offline`.

## 1. Choose the scope of this session

| Stage | SQLite runnerd | PG runnerd | Completion gate |
| --- | --- | --- | --- |
| Deploy binaries | Serving normally | Not started | Original configuration, health and real tasks verified |
| `prepare` | May run | Not started | Online backup, bound identity and target in `copying` |
| `sync --skip-events` | May run | Not started | Request baseline complete and final work within budget |
| `status` | According to phase | According to phase | Observe state; no service switch |
| `cutover` | All writers stopped | Not started | Exit zero, `ready_for_pg: true`, `source_sealed: true` |
| Switch configuration and start PG runnerd | Stopped | One instance | Health, sign-in and real task/deployment acceptance |
| `backfill` | Sealed and stopped | May run | `history_complete: true` |

For the first production trial, deploy, prepare and pre-copy first. Observe service latency and resource use before scheduling cutover separately. Online copying consumes CPU, I/O and disk space; it does not guarantee zero service impact.

**Successful cutover is the switch boundary.** Once PG reaches `ready` or `complete`, the tool refuses to restore SQLite writes even if PG runnerd has not started. There is no runtime dual-write, reverse replication or lossless switch back to SQLite.

## 2. Deploy and prepare configuration

Build and deploy `runnerd` and `runnerd-migrate` from the same version. `task build` produces `bin/runnerd` and `bin/runnerd-migrate`. First start the new runnerd with the existing SQLite configuration and verify health, task execution, logs and recovery. Keep the tool and business model version fixed during migration.

Confirm the actual SQLite file used by the running service. `./var/runnerd.db` below is an example, not automatic discovery. The source must already use WAL. Do not change journal mode on the running database or copy its live `.db` file as a consistent backup. Allow space for the backup, SQLite WAL/change journal and PG data/WAL. Run as the service user with source read/write access and ownership of the private backup directory.

Keep the original SQLite configuration. Prepare a separate protected `runnerd.postgres.yaml` from the complete existing configuration, changing only the database connection. Preserve `auth.encryption_key`, `auth.session_secret`, GitHub/Sandbox configuration and key files. Put it in its final location so relative paths remain valid.

Example database section, not a complete configuration:

```yaml
database:
  backend: postgres
  dsn: "host=PG_HOST port=5432 user=ci_runner dbname=ci_runner password=RUNNERD_ENC(...) sslmode=disable"
```

Replace the host and credentials; choose `sslmode` for the actual environment. Plaintext passwords, obfuscated passwords and whole obfuscated DSNs are supported. `RUNNERD_ENC(...)` must contain a generated value, not the placeholder; see the [README configuration instructions](../README.md#config-value-obfuscation). `psql` cannot decode this format. A manual connection check must use the same host, port, user, database, SSL mode and real password, without putting credentials in shell history or shared output.

Use a dedicated empty PG schema and an account allowed to create tables, functions, triggers and sequences. Inspect any earlier failed migration before reusing its target; do not delete data merely to satisfy the empty-schema requirement. Keep PG runnerd stopped throughout pre-copy. Unfinished legacy online protocols require their original finalization, not removal of fence metadata.

### Open the operator shell

These examples require **Bash**. Enter the real deployment directory, then start Bash:

```bash
cd ~/qiniu-ci-runner/_package
bash
```

Use the same terminal for subsequent steps. After reconnecting, restore these path variables with the same source and target configuration:

```bash
umask 077
SQLITE_PATH="$PWD/var/runnerd.db"
PG_CONFIG="$PWD/runnerd.postgres.yaml"
MIGRATION_DIR="$PWD/var/postgres-migration"
install -d -m 700 "$MIGRATION_DIR"

./runnerd-migrate --help
test -f "$SQLITE_PATH" && test -r "$PG_CONFIG"
```

Confirm that help lists `prepare`, `sync`, `cutover` and `backfill`, and the file check succeeds. Restrict target configuration permissions to the service user, for example with `chmod 600 "$PG_CONFIG"`.

If sqlite3 is installed, inspect journal mode without changing it:

```bash
sqlite3 -readonly "$SQLITE_PATH" 'PRAGMA journal_mode;'
```

Expect `wal`. The tool also validates this; otherwise stop and arrange a separate journal-mode change.

Each invocation below creates a unique directory containing `report.json` and `progress.log`. Bash's `2> >(tee ... >&2)` keeps progress visible while recording it. Do not replace this with `2>&1`, which would corrupt the JSON. Capture the exit code immediately, before another command replaces it.

## 3. Prepare online

SQLite runnerd may continue serving. Initial preparation creates and verifies an online backup, installs request capture and write guards, then initializes PG.

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/prepare.XXXXXX") || exit 1
./runnerd-migrate --mode prepare \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" \
  --backup "$RUN_DIR/before-postgres.db" --timeout 2h \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf 'Exit: %s; report directory: %s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

Proceed only after exit zero with `phase: "copying"` and `source_sealed: false`. The backup directory must belong to the current user with mode `0700`; backup files are exclusive `0600` and never overwritten.

A backup file appearing does not mean preparation has finished. On failure, retain its directory and retry the same source/target/version with a new `RUN_DIR`. Before source binding, this creates a fresh backup. After binding, preparation resumes initialization without taking another backup; the new directory may contain no `.db`. Retain the original backup.

Request writes now append change keys. Events must remain append-only: updates, deletes and out-of-order event IDs are rejected. Stopping the importer does not uninstall capture, so the journal keeps growing. Monitor space if postponing the migration; do not manually remove triggers or journal data.

## 4. Pre-copy online

Copy requests and other business tables first, deferring historical events:

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/sync.XXXXXX") || exit 1
./runnerd-migrate --mode sync --skip-events \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" \
  --batch-pause 50ms --timeout 2h \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf 'Exit: %s; report directory: %s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

Repeat the entire block as needed, keeping a separate report per invocation. Requests/events use verified 256-row batches with transactional checkpoints. After timeout or Ctrl-C, wait for process exit before retrying; committed batches survive, and the failed batch rolls back. The other 16 tables refresh using individual consistent-snapshot transactions including deletions. This is not whole-database rollback.

The example uses `--batch-pause 50ms`; the default is `10ms`. It pauses applicable copy batches, not backups, individual queries or complete small-table refreshes, and is not a global resource cap. Observe service latency, tasks, errors, CPU, I/O, WAL and free disk. If online sync affects service, stop it, wait for exit and adjust. Do not apply that recovery shortcut to a failed cutover without checking durable state.

Omit `--skip-events` to pre-copy historical events too. **All requests must converge before PG release; only historical events may be deferred.** Terminal requests can still receive exit/version/GitHub evidence or be retried.

## 5. Inspect progress and plan the window

Status can run in another terminal after setting the same path variables:

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/status.XXXXXX") || exit 1
./runnerd-migrate --mode status \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" --timeout 5m \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf 'Exit: %s; report directory: %s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

| Field | Meaning |
| --- | --- |
| `phase` | PG migration phase: `copying`, `ready` or `complete` |
| `source_sealed` | SQLite business writes are fenced |
| `requests_copied` | Processed baseline rows, not current total request count |
| `pending_request_changes` | Unreplayed journal entries, possibly several per request |
| `final_table_rows` | Combined other-table rows refreshed during cutover |
| `pending_event_rows` | Observed remaining historical events |
| `ready_for_cutover` | Baseline complete and final work within budgets; not stopped-source evidence or a duration guarantee |
| `ready_for_pg` | PG released; SQLite writes cannot resume |
| `history_complete` | Historical events copied and verified |

Schedule cutover only when `ready_for_cutover: true`. Otherwise continue sync or inspect the budgets. Live-source status may change; cutover rechecks it. If overriding final budgets, pass identical limits to status and cutover.

Progress and ten-second heartbeats go to stderr. `stage` identifies the operation; `rows` is its count; `since_progress` measures time since a count or stage change. On resumed stages, `rows_per_sec` may include persisted counts, so it is not a reliable remaining-time estimate.

- Backup and integrity checks may show no row advancement for a long time.
- A heartbeat proves the reporter is alive, not that the database operation is advancing. Compare stage, CPU time, I/O and database waits.
- `status=cancelling` means the command has not exited yet. Wait before retrying.
- JSON may remain empty until successful completion. `status=finished` means one stage ended, not permission to switch configuration.

## 6. Stop writers and cut over

**This begins the maintenance window. Do not execute with SQLite runnerd still running.**

1. Run another `sync --skip-events` and inspect status to reduce final work.
2. Pause new Workflow/deployment admission and arrange webhook buffering or redelivery/reconciliation. The importer provides neither draining nor webhook buffering.
3. Let active Runners/Deployments finish or stop them through the established maintenance procedure. Web terminals disconnect.
4. Back up the complete configuration and keys. Stop runnerd and its restart supervisor through the actual deployment manager; confirm every SQLite writer has exited. Keep PG runnerd stopped.
5. Execute cutover. `--source-stopped` is an operator assertion, not a service-stop command.

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/cutover.XXXXXX") || exit 1
./runnerd-migrate --mode cutover --source-stopped \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" --timeout 10m \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf 'Exit: %s; report directory: %s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

Default limits are `--max-final-changes 10000` and `--max-final-rows 100000`. Excess work is rejected before sealing in this invocation. Catch up online before increasing limits. `10m` is a command deadline, not a downtime guarantee; allow recovery time based on rehearsal. Cutover does not take another full backup, rescan all verified request contents or wait for historical events.

### Switch configuration only after release

Require exit zero and both report fields:

```json
{"ready_for_pg": true, "source_sealed": true}
```

This is a field excerpt, not a full report. `history_complete: false` permits PG startup with incomplete historical event views.

Now change the database section of the effective `runnerd.yaml` to the verified PG configuration, or explicitly start the service with `--config ./runnerd.postgres.yaml`, using the established deployment process. Preserve original encryption/session keys and other settings. Start exactly one PG runnerd. Never start the old SQLite service or a second full instance with the same production provider credentials for testing.

Keep general admission paused while checking health, sign-in, existing requests/specs and deduplication/recovery. Then conduct controlled real Runner/Deployment creation/completion, new-event and Web Terminal checks. Restore admission only after acceptance, then redeliver and reconcile missing webhooks. Database tests do not establish real workload acceptance.

## 7. Backfill history while PG serves traffic

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/backfill.XXXXXX") || exit 1
./runnerd-migrate --mode backfill \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" \
  --batch-pause 50ms --timeout 2h \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf 'Exit: %s; report directory: %s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

Exit zero with `history_complete: true` completes migration. Retry after timeout; committed batches remain. Historical event views may be incomplete until then, independently of request migration.

Backfill inserts only missing historical events and verifies identical content for existing IDs. It does not overwrite PG requests/settings/authorization/new events or reset live sequences. Cutover reserved historical sequence high-watermarks. PG may serve traffic, but continue watching resource usage.

Retain the sealed original SQLite and its WAL/SHM lifecycle, initial backup, original configuration/keys and all reports/logs through acceptance and the retention period. The initial backup is an earlier snapshot, not a replacement for the final sealed source used for backfill.

## 8. Recover according to durable state

| Failure or state | Action |
| --- | --- |
| PG connection failure | Check target configuration, actual password, network, TLS and permissions; a successful manual psql call does not prove the tool uses the same DSN/schema |
| Failed prepare | Preserve evidence and retry the same source/target/version; status may also fail during incomplete initialization |
| Failed/timed-out sync | SQLite may continue serving; wait for importer exit, inspect logs and retry without clearing PG or checkpoints |
| Failed cutover, missing report or uncertain commit | Keep both services stopped and inspect status; failure does not prove SQLite is writable or PG rolled back |
| PG `copying`, source sealed | Retry cutover, or restore SQLite using the command below if abandoning this window |
| PG `ready` / `complete` | Do not restore SQLite writes; troubleshoot PG or arrange a separate recovery plan |
| Failed backfill | PG may continue serving; diagnose and retry without ignoring content conflicts |

Only with **both services stopped and PG confirmed `copying`**:

```bash
RUN_DIR=$(mktemp -d "$MIGRATION_DIR/resume-source.XXXXXX") || exit 1
./runnerd-migrate --mode resume-source --source-stopped \
  --config "$PG_CONFIG" --sqlite "$SQLITE_PATH" --timeout 5m \
  > "$RUN_DIR/report.json" 2> >(tee "$RUN_DIR/progress.log" >&2)
migration_exit=$?
printf 'Exit: %s; report directory: %s\n' "$migration_exit" "$RUN_DIR"
cat "$RUN_DIR/report.json"
```

Restore runnerd with the original SQLite configuration only after exit zero, `phase: "copying"` and `source_sealed: false`. Capture remains active for later sync; this does not cancel migration or uninstall triggers. If status is unavailable or unsealing fails, do not edit metadata, remove guards or restart the old database to bypass protection.

## 9. Data and verification boundaries

SQLite triggers capture request keys including deletes, recreation, terminal evidence and writes that do not advance `updated_at`. Each batch checks current Job ownership in one source snapshot; stale target cleanup, replacement write/verification and checkpoint commit share a PG transaction. The journal restores recreated requests. Only current-model data is imported; extra historical tables/columns remain in the source and backup.

Source identity, target host/port/database/schema, model fingerprint and guard definitions are bound. Do not replace the source file, restore an earlier backup over it, change target identity, edit migration metadata or upgrade models. An advisory lock serializes importers. Database owners can still remove protection through DDL; the tool does not prevent deliberate tampering.

Development regression tests use a dedicated database ending in `_test` and isolated random schemas:

```bash
RUNNERD_CATALOG_BACKEND_TESTS=1 \
RUNNERD_POSTGRES_TEST_DSN='<dedicated _test database DSN>' \
go test -race ./internal/state ./cmd/runnerd-migrate \
  -run 'Test(PhasedMigration|OfflineMigration|MigrationProgress|RunPhasedMigration)' -count=1
```

Coverage includes live source writes, resumption, request deletion/recreation, rollback, cutover fences/budgets, historical conflicts and preservation of new PG writes. Production load, WAL/disk growth, backup restoration, actual downtime and real Runner/Deployment acceptance still require rehearsal; passing tests is not production migration acceptance.
