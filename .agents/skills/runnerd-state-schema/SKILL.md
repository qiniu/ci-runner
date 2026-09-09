---
name: runnerd-state-schema
description: Use when changing qiniu-ci-runner state records, GORM tags, indexes, migration compatibility, old sqlite upgrade behavior, or DB-backed runner state semantics.
---

# Runnerd State Schema

Start by saying: "I am using the runnerd-state-schema skill to change or verify state schema behavior."

## Goal

Keep runnerd's database schema model-driven through GORM while preserving known older state database upgrade paths.

## When To Use

- Editing `internal/state/records.go`.
- Editing migration logic in `internal/state/db.go`.
- Changing state indexes, uniqueness, required columns, defaults, or relationship constraints.
- Reviewing schema changes for sqlite/Postgres/MySQL compatibility.
- Investigating failures around `AutoMigrate`, old sqlite files, or `NOT NULL` column additions.

## Rules

- Prefer GORM tags and `AutoMigrate` for normal schema source of truth. Existing SQLite `runner_requests` and `runner_profiles` tables are additive-only: create missing model columns and indexes without generic table recreation.
- Use handwritten SQL only for genuine GORM gaps.
- Keep every legacy compatibility action narrow and explicit. This includes column additions, obsolete constraint removal, and destructive reset of legacy tables that cannot represent the current scope model. Document required operator reconfiguration and user reauthentication.
- GORM foreign-key creation is disabled intentionally. Preserve the foreign-keyless schema convention unless a separately tested migration changes it.
- Do not reintroduce legacy `users` migration behavior unless the user explicitly asks.
- Avoid `default:true` on business booleans when zero-value preservation matters.
- Fresh database tests are not enough for required-column changes; add or preserve old-schema upgrade coverage.

## Workflow

1. Inspect current state records and migration helpers:

```bash
sed -n '1,260p' internal/state/records.go
sed -n '1,220p' internal/state/db.go
```

2. Check existing tests for old-schema fixtures and migration behavior:

```bash
rg -n "Migrate|Legacy|default_available|runner_group_name|AutoMigrate" internal/state
```

3. For schema edits, update the model tags first. Change `migrateLegacySchemaColumns` only when old databases cannot safely migrate through `AutoMigrate` alone. Keep existing SQLite `runner_requests` and `runner_profiles` changes additive; any non-additive change requires a narrow explicit compatibility migration. Cover column additions, constraint removal, or legacy-table reset with an old-schema fixture that proves the required cleanup and asserts preservation or intentional data loss according to the compatibility contract.

4. Add or update tests before relying on the fix. Include old sqlite upgrade coverage for required columns, unique indexes with existing rows, and relationship changes. Assert preservation of `github_installation_id`, Sandbox snapshot fields, and `updated_at` for existing runner requests; preserve runner-profile rows and existing indexes while adding every missing model field.

5. Run:

```bash
go test ./internal/state -count=1
```

When a production SQLite export is available, also run:

```bash
RUNNERD_SQLITE_SNAPSHOT=/path/to/runnerd-export.db \
  go test ./internal/state -run TestMigrateSQLiteRunnerRequestSnapshot -count=1 -v
```

When a schema, migration, or audited-mutation transaction change can affect
Postgres or MySQL, run the real-dialect gate against dedicated disposable
databases whose names end in `_test`:

```bash
RUNNERD_CATALOG_BACKEND_TESTS=1 \
RUNNERD_POSTGRES_TEST_DSN='<dedicated postgres test DSN>' \
RUNNERD_MYSQL_TEST_DSN='<dedicated mysql test DSN>' \
  go test ./internal/state -run 'Test(ApplyMutationWithAudit|FreshSchema|ScopedRunnerCatalogFreshSchema)SQLBackends' -count=1 -v
```

6. If callers or server behavior changed, also run:

```bash
go test ./...
task test
```

7. Sync docs if behavior or maintenance rules changed:

- `README.md` and `README.zh.md`
- `TODO.md`
- `AGENTS.md`
- `docs/testing.md` and `docs/zh/testing.md`
- `.agents/rules/project-architecture.md`
- `.agents/rules/testing-and-verification.md`

## Output

Report:

- schema files changed;
- whether old-schema compatibility was needed;
- tests added or preserved;
- verification commands and results;
- any unverified DB backend or multi-instance boundary.
