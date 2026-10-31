package state

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func offlinePostgresDSN(t *testing.T) string {
	t.Helper()
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL test database")
	}
	dsn := os.Getenv("RUNNERD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("missing PostgreSQL test DSN")
	}
	store := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	requireCatalogMatcherTestDatabase(t, db, BackendPostgres)
	schema := fmt.Sprintf("offline_%d", time.Now().UnixNano())
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE"); closeTestDB(t, db) })
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return dsn + " search_path=" + schema
}

func TestOfflineMigrationSQLBackends(t *testing.T) {
	dsn := offlinePostgresDSN(t)
	directory := offlinePrivateDir(t)
	source := New(directory).(*DBStore)
	db, err := source.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	if err := db.Create(&accountSecretRecord{ScopeType: "account", ScopeID: 9007199254740993, KeyType: "sandbox", EncryptedValue: "opaque-encrypted-value"}).Error; err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2026, 10, 29, 1, 2, 3, 123456789, time.FixedZone("CST", 28800))
	// Non-ASCII keys, nanoseconds, legacy extras, encrypted strings and deleted
	// high IDs exercise the boundaries a fresh empty database cannot cover.
	if err := db.Create(&accountRecord{ID: 9007199254740993, Role: "admin", CreatedAt: stamp, UpdatedAt: stamp}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&accountRecord{ID: 9007199254741000, Role: "user", CreatedAt: stamp, UpdatedAt: stamp}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&accountRecord{}, "id = ?", int64(9007199254741000)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE historical_extra (value TEXT)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE runner_profiles ADD COLUMN historical_extra TEXT").Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 260; i++ {
		if _, err := source.UpsertProfile(RunnerProfile{Name: fmt.Sprintf("规格-%03d", i), Labels: []string{"qiniu"}, Enabled: false}); err != nil {
			t.Fatal(err)
		}
	}
	opts := OfflineMigrationOptions{SQLitePath: filepath.Join(directory, "runnerd.db"), BackupPath: filepath.Join(directory, "backup.db"), PostgresDSN: dsn}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	report, err := MigrateSQLiteToPostgres(ctx, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Verified || len(report.Tables) != 18 || report.ExtraTables != 1 || report.ExtraColumns != 1 {
		t.Fatalf("unexpected report: %+v", report)
	}
	info, err := os.Stat(opts.BackupPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("backup not private")
	}
	backupBefore, _ := os.ReadFile(opts.BackupPath)
	if _, err := MigrateSQLiteToPostgres(ctx, opts); err == nil {
		t.Fatal("overwrote existing backup")
	}
	backupAfter, _ := os.ReadFile(opts.BackupPath)
	if sha256.Sum256(backupBefore) != sha256.Sum256(backupAfter) {
		t.Fatal("backup changed")
	}
	opts.BackupPath = filepath.Join(directory, "second.db")
	if _, err := MigrateSQLiteToPostgres(ctx, opts); err == nil || !strings.Contains(err.Error(), "empty PostgreSQL") {
		t.Fatalf("nonempty target accepted: %v", err)
	}
	pgStore := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn, MigrateOnStart: true}).(*DBStore)
	pg, err := pgStore.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, pg)
	var got accountRecord
	if err := pg.First(&got, "id = ?", int64(9007199254740993)).Error; err != nil || got.Role != "admin" || !got.UpdatedAt.Equal(stamp.Truncate(time.Microsecond)) {
		t.Fatalf("record lost: %v", err)
	}
	var secret accountSecretRecord
	if err := pg.First(&secret).Error; err != nil || secret.EncryptedValue != "opaque-encrypted-value" {
		t.Fatalf("encrypted value changed: %v", err)
	}
	inserted := accountRecord{Role: "user"}
	if err := pg.Create(&inserted).Error; err != nil || inserted.ID <= 9007199254741000 {
		t.Fatalf("sequence watermark lost: %v", err)
	}
	profile, err := pgStore.GetProfile("规格-000")
	if err != nil {
		t.Fatal(err)
	}
	profile.Priority = 3
	if _, err := pgStore.UpsertProfileIfUnchanged(profile, &profile.UpdatedAt); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineMigrationRejectsActiveFenceAndWriter(t *testing.T) {
	source := New(t.TempDir()).(*DBStore)
	db, err := source.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	opts := OfflineMigrationOptions{SQLitePath: source.opts.DatabaseDSN, BackupPath: filepath.Join(offlinePrivateDir(t), "backup.db"), PostgresDSN: "invalid-secret"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.AutoMigrate(&migrationControlRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&migrationControlRecord{ID: 1, MigrationID: "migration", InstanceID: "source", Role: "primary", ProtocolVersion: 1, Epoch: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := MigrateSQLiteToPostgres(ctx, opts); err == nil || !strings.Contains(err.Error(), "migration fence") {
		t.Fatalf("active migration accepted: %v", err)
	}
	plain := New(t.TempDir()).(*DBStore)
	plainDB, err := plain.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, plainDB)
	opts.SQLitePath = plain.opts.DatabaseDSN
	if err := plainDB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("UPDATE runner_profiles SET priority = priority").Error; err != nil {
			return err
		}
		if _, err := MigrateSQLiteToPostgres(ctx, opts); err == nil || !strings.Contains(err.Error(), "lock SQLite") {
			t.Fatalf("writer accepted: %v", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestOfflineMigrationFailureRollsBackSQLBackends(t *testing.T) {
	dsn := offlinePostgresDSN(t)
	source := New(t.TempDir()).(*DBStore)
	db, err := source.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	// This row copies successfully before the deliberately invalid later table.
	if err := db.Create(&accountSecretRecord{ScopeType: "account", ScopeID: 1, KeyType: "sandbox", EncryptedValue: "ciphertext-keep-verbatim"}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := source.UpsertProfile(RunnerProfile{Name: "bad", Labels: []string{"qiniu"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE runner_profiles SET labels_json = ? WHERE name = ?", "invalid\x00payload-secret", "bad").Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err = MigrateSQLiteToPostgres(ctx, OfflineMigrationOptions{SQLitePath: source.opts.DatabaseDSN, BackupPath: filepath.Join(offlinePrivateDir(t), "backup.db"), PostgresDSN: dsn})
	if err == nil || !strings.Contains(err.Error(), "copy table runner_profiles") || strings.Contains(err.Error(), "payload-secret") || strings.Contains(err.Error(), dsn) {
		t.Fatalf("expected redacted failure: %v", err)
	}
	target := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn}).(*DBStore)
	pg, err := target.open()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, pg)
	tables, err := pg.Migrator().GetTables()
	if err != nil || len(tables) != 0 {
		t.Fatalf("partial schema/data committed: %v %v", tables, err)
	}
}

func offlinePrivateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOfflineMigrationSearchPathSQLBackends(t *testing.T) {
	dsn := offlinePostgresDSN(t)
	target := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn}).(*DBStore)
	db, err := target.open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestDB(t, db) })
	fallback := fmt.Sprintf("offline_fallback_%d", time.Now().UnixNano())
	if err := db.Exec("CREATE SCHEMA " + fallback).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Exec("DROP SCHEMA " + fallback + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	// A deliberately incompatible same-name table must never be inspected,
	// migrated or used by the importer's unqualified business SQL.
	if err := db.Exec("CREATE TABLE " + fallback + ".accounts (sentinel text)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO " + fallback + ".accounts VALUES ('preserved')").Error; err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("search_path", query.Get("search_path")+","+fallback)
		parsed.RawQuery = query.Encode()
		dsn = parsed.String()
	} else {
		dsn += "," + fallback
	}
	source := New(t.TempDir()).(*DBStore)
	sourceDB, err := source.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, sourceDB)
	if err := sourceDB.Create(&accountRecord{Role: "admin"}).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	report, err := MigrateSQLiteToPostgres(ctx, OfflineMigrationOptions{SQLitePath: source.opts.DatabaseDSN, BackupPath: filepath.Join(offlinePrivateDir(t), "backup.db"), PostgresDSN: dsn})
	if err != nil || !report.Verified {
		t.Fatalf("schema-isolated import failed: %v", err)
	}
	var marker string
	if err := db.Raw("SELECT sentinel FROM " + fallback + ".accounts").Scan(&marker).Error; err != nil || marker != "preserved" {
		t.Fatalf("fallback data modified: %v", err)
	}
	var columns int64
	if err := db.Raw("SELECT count(*) FROM information_schema.columns WHERE table_schema = ? AND table_name = 'accounts'", fallback).Scan(&columns).Error; err != nil || columns != 1 {
		t.Fatalf("fallback schema modified: %v", err)
	}
}

func TestOfflineMigrationVerificationRejectsTargetDifferences(t *testing.T) {
	openStore := func(t *testing.T) *DBStore {
		t.Helper()
		store := New(t.TempDir()).(*DBStore)
		db, err := store.dbOrEnsure()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { closeTestDB(t, db) })
		return store
	}
	table := migrationTable{Name: "accounts", Columns: []migrationColumn{
		{Name: "id", Kind: "int", Size: 64, PrimaryKey: true},
		{Name: "role", Kind: "string"},
	}}
	for _, tc := range []struct {
		name string
		edit func(*gorm.DB) error
	}{
		{name: "missing", edit: func(db *gorm.DB) error { return db.Delete(&accountRecord{}, "id = ?", 1).Error }},
		{name: "changed", edit: func(db *gorm.DB) error {
			return db.Model(&accountRecord{}).Where("id = ?", 1).Update("role", "user").Error
		}},
		{name: "extra", edit: func(db *gorm.DB) error { return db.Create(&accountRecord{ID: 2, Role: "user"}).Error }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, target := openStore(t), openStore(t)
			sourceDB, _ := source.dbOrEnsure()
			targetDB, _ := target.dbOrEnsure()
			if err := sourceDB.Create(&accountRecord{ID: 1, Role: "admin"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := targetDB.Create(&accountRecord{ID: 1, Role: "admin"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := tc.edit(targetDB); err != nil {
				t.Fatal(err)
			}
			_, err := verifyOfflineTable(sourceDB, targetDB, table)
			if err == nil || !errors.Is(err, errMigrationPrecondition) {
				t.Fatalf("verification error = %v, want migration precondition", err)
			}
		})
	}
}

func TestOfflineMigrationBackupDirectoryValidation(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := validateMigrationBackupDirectory(dir); err == nil || !errors.Is(err, errMigrationPrecondition) {
		t.Fatalf("private directory validation error = %v", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := validateMigrationBackupDirectory(dir); err != nil {
		t.Fatal(err)
	}
}
