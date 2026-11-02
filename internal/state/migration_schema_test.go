package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMigrationSchemaSQLitePrimaryKeys(t *testing.T) {
	for _, tc := range []struct {
		name      string
		ddl       string
		wantErr   string
		extra     int
		composite bool
	}{
		{name: "inline", ddl: "id TEXT PRIMARY KEY, value TEXT"},
		{name: "multiline inline", ddl: "id TEXT\n PRIMARY KEY, value TEXT"},
		{name: "named constraint", ddl: "id TEXT, value TEXT, CONSTRAINT record_pk PRIMARY KEY (id)"},
		{name: "spaced constraint", ddl: "id TEXT, value TEXT, PRIMARY KEY ( id )"},
		{name: "matching composite key", ddl: "id TEXT, value TEXT, PRIMARY KEY ( value, id )", composite: true},
		{name: "extra column", ddl: "id TEXT PRIMARY KEY, value TEXT, legacy TEXT", extra: 1},
		{name: "generated extra column", ddl: "id TEXT PRIMARY KEY, value TEXT, legacy TEXT GENERATED ALWAYS AS (value) VIRTUAL", extra: 1},
		{name: "missing primary key", ddl: "id TEXT, value TEXT", wantErr: "schema primary key differs in records.id"},
		{name: "unique is not primary", ddl: "id TEXT UNIQUE, value TEXT", wantErr: "schema primary key differs in records.id"},
		{name: "wrong primary key", ddl: "id TEXT, value TEXT PRIMARY KEY", wantErr: "schema primary key differs in records.id"},
		{name: "extra model primary key", ddl: "id TEXT, value TEXT, PRIMARY KEY(id, value)", wantErr: "schema primary key differs in records.value"},
		{name: "extra legacy primary key", ddl: "id TEXT, value TEXT, legacy TEXT, PRIMARY KEY(id, legacy)", wantErr: "unexpected schema primary key in records.legacy"},
		{name: "missing column", ddl: "id TEXT PRIMARY KEY", wantErr: "required schema column missing in records.value"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestDB(t, db)
			if err := db.Exec("CREATE TABLE records (" + tc.ddl + ")").Error; err != nil {
				t.Fatal(err)
			}
			inventory := migrationInventory{Tables: []migrationTable{{Name: "records", Columns: []migrationColumn{
				{Name: "id", PrimaryKey: true}, {Name: "value", PrimaryKey: tc.composite},
			}}}}
			report, err := inspectMigrationSchema(context.Background(), db, inventory)
			if tc.wantErr != "" {
				if !errors.Is(err, errMigrationInvalid) || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || report.ExtraColumns != tc.extra {
				t.Fatalf("report = %+v, error = %v", report, err)
			}
		})
	}
}

func TestOfflineMigrationLegacySourceSchema(t *testing.T) {
	source := New(t.TempDir()).(*DBStore)
	db, err := source.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	// Replace only the disposable fixture's fresh table with historical DDL.
	if err := db.Exec("DROP TABLE runner_requests").Error; err != nil {
		t.Fatal(err)
	}
	ddl, err := os.ReadFile("testdata/migration_legacy_runner_requests.sql")
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(string(ddl)).Error; err != nil {
		t.Fatal(err)
	}
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&runnerRequestRecord{}); err != nil {
		t.Fatal(err)
	}
	for _, index := range stmt.Schema.ParseIndexes() {
		if err := db.Migrator().CreateIndex(&runnerRequestRecord{}, index.Name); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Migrator().CreateIndex(&runnerRequestMetricsIndex{}, "idx_runner_requests_metrics"); err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	want := runnerRequestRecord{
		ID: "legacy", Source: "github", RunnerName: "runner", LabelsJSON: "[]", Status: StatusCompleted,
		GitHubInstallationID: 42, SandboxAPIKeyEncrypted: "opaque-secret", QueuedAt: stamp, UpdatedAt: stamp,
	}
	if err := db.Create(&want).Error; err != nil {
		t.Fatal(err)
	}
	var schemaBefore string
	if err := db.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'runner_requests'").Scan(&schemaBefore).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	backupPath := filepath.Join(offlinePrivateDir(t), "backup.db")
	// Source validation remains covered even without a PostgreSQL test service.
	_, err = MigrateSQLiteToPostgres(ctx, OfflineMigrationOptions{
		SQLitePath: source.opts.DatabaseDSN, BackupPath: backupPath, PostgresDSN: "invalid",
	})
	if err == nil || !strings.Contains(err.Error(), "connect PostgreSQL target: invalid DSN") {
		t.Fatalf("expected legacy schema to pass validation: %v", err)
	}
	t.Run("backupSQLBackends", func(t *testing.T) {
		dsn := offlinePostgresDSN(t)
		// Cancel after the backup is verified, before the import transaction opens.
		_, err = MigrateSQLiteToPostgres(ctx, OfflineMigrationOptions{
			SQLitePath: source.opts.DatabaseDSN, BackupPath: backupPath, PostgresDSN: dsn,
			Progress: func(p OfflineMigrationProgress) {
				if p.Stage == "begin PostgreSQL import transaction" {
					cancel()
				}
			},
		})
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "begin PostgreSQL import transaction") {
			t.Fatalf("expected cancellation after backup verification: %v", err)
		}
		backup, err := gorm.Open(sqlite.Open(backupPath+"?mode=ro"), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		defer closeTestDB(t, backup)
		for _, reader := range []*gorm.DB{db, backup} {
			var got runnerRequestRecord
			if err := reader.First(&got, "id = ?", want.ID).Error; err != nil {
				t.Fatal(err)
			}
			if got.GitHubInstallationID != want.GitHubInstallationID || got.SandboxAPIKeyEncrypted != want.SandboxAPIKeyEncrypted || !got.UpdatedAt.Equal(stamp) {
				t.Fatal("legacy record changed")
			}
			var schemaAfter string
			if err := reader.Raw("SELECT sql FROM sqlite_master WHERE type = 'table' AND name = 'runner_requests'").Scan(&schemaAfter).Error; err != nil {
				t.Fatal(err)
			}
			if schemaAfter != schemaBefore {
				t.Fatal("source schema changed")
			}
		}
	})
}
