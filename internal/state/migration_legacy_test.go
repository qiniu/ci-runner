package state

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLegacyMigrationFenceBeforeStartupAndImport(t *testing.T) {
	for _, tc := range []struct {
		name, role string
		version    int
		active     int64
		trigger    bool
		accepted   bool
	}{
		{name: "primary", role: "primary", version: 1},
		{name: "replica", role: "replica", version: 1},
		{name: "frozen", role: "frozen", version: 1},
		{name: "unknown protocol", role: "standalone", version: 2},
		{name: "unfinished transaction", role: "standalone", version: 1, active: 1},
		{name: "leftover trigger", role: "standalone", version: 1, trigger: true},
		{name: "finalized", role: "standalone", version: 1, accepted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := offlinePrivateDir(t)
			store := New(dir).(*DBStore)
			db, err := store.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestDB(t, db)
			if err := db.AutoMigrate(&migrationControlRecord{}); err != nil {
				t.Fatal(err)
			}
			control := migrationControlRecord{ID: 1, MigrationID: "legacy", InstanceID: "source", ProtocolVersion: tc.version, Role: tc.role, Epoch: 1, ActiveTransaction: tc.active}
			if err := db.Create(&control).Error; err != nil {
				t.Fatal(err)
			}
			if tc.trigger {
				if err := db.Exec("CREATE TRIGGER runnerd_mig_accounts_insert AFTER INSERT ON accounts BEGIN SELECT 1; END").Error; err != nil {
					t.Fatal(err)
				}
			}
			// Startup must reject before AutoMigrate can recreate this table.
			if !tc.accepted {
				if err := db.Migrator().DropTable(&runnerEventRecord{}); err != nil {
					t.Fatal(err)
				}
			}
			reopened := New(dir).(*DBStore)
			err = reopened.Ensure()
			if tc.accepted {
				if err != nil {
					t.Fatal(err)
				}
				defer closeTestDB(t, reopened.db)
			} else {
				if err == nil {
					t.Fatal("legacy fence bypassed")
				}
				if tc.role == "replica" {
					if !errors.Is(err, errMigrationPrecondition) {
						t.Fatalf("replica startup error = %v", err)
					}
				} else if !errors.Is(err, errLegacyMigration) {
					t.Fatalf("legacy startup error = %v", err)
				}
				if db.Migrator().HasTable(&runnerEventRecord{}) {
					t.Fatal("schema changed before fence validation")
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err = MigrateSQLiteToPostgres(ctx, OfflineMigrationOptions{SQLitePath: store.opts.DatabaseDSN, BackupPath: filepath.Join(dir, "backup.db"), PostgresDSN: "invalid"})
			if tc.accepted {
				if err == nil || !strings.Contains(err.Error(), "connect PostgreSQL") {
					t.Fatalf("finalized source rejected before target connection: %v", err)
				}
			} else if err == nil || (tc.role == "replica" && !errors.Is(err, errMigrationPrecondition)) || (tc.role != "replica" && !errors.Is(err, errLegacyMigration)) {
				t.Fatalf("legacy import fence bypassed: %v", err)
			}
		})
	}
}

func TestLegacyMigrationErrorClassification(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	if err := db.AutoMigrate(&migrationControlRecord{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&migrationControlRecord{ID: 1, MigrationID: "legacy", InstanceID: "source", ProtocolVersion: 1, Role: "replica"}).Error; err != nil {
		t.Fatal(err)
	}
	err = validateStandaloneMigrationDB(db)
	if err == nil || !errors.Is(err, errMigrationPrecondition) || errors.Is(err, errLegacyMigration) {
		t.Fatalf("replica classification = %v", err)
	}
}
