package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func newCapturedSQLite(t *testing.T) (*DBStore, *gorm.DB) {
	t.Helper()
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestDB(t, db) })
	if err := store.installOnlineMigrationCapture(context.Background(), "test-migration", "sqlite-source", "primary", 1); err != nil {
		t.Fatal(err)
	}
	return store, db
}

func captureRows(t *testing.T, store *DBStore) ([]migrationTransactionRecord, []migrationChangeRecord) {
	t.Helper()
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	var groups []migrationTransactionRecord
	var changes []migrationChangeRecord
	if err := db.Order("position").Find(&groups).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Order("id").Find(&changes).Error; err != nil {
		t.Fatal(err)
	}
	return groups, changes
}

func TestOnlineMigrationCaptureSQLite(t *testing.T) {
	store, originalDB := newCapturedSQLite(t)
	testOnlineMigrationCapture(t, store, originalDB)
}

func TestOnlineMigrationCaptureSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("set RUNNERD_CATALOG_BACKEND_TESTS=1 with a dedicated PostgreSQL test database")
	}
	dsn := os.Getenv("RUNNERD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("RUNNERD_POSTGRES_TEST_DSN is required")
	}
	store := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn}).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	requireCatalogMatcherTestDatabase(t, db, BackendPostgres)
	resetSQLBackendTestTables(t, db)
	t.Cleanup(func() {
		resetSQLBackendTestTables(t, db)
		inventory, err := newOnlineMigrationInventory()
		if err != nil {
			t.Error(err)
		}
		for _, table := range inventory.Tables {
			if err := db.Exec("DROP FUNCTION IF EXISTS " + migrationQuoted("runnerd_mig_"+table.Name) + "()").Error; err != nil {
				t.Error(err)
			}
		}
		for _, model := range []any{&migrationChangeRecord{}, &migrationTransactionRecord{}, &migrationRetentionRecord{}, &migrationFrontierRecord{}, &migrationControlRecord{}} {
			if err := db.Migrator().DropTable(model); err != nil {
				t.Error(err)
			}
		}
		closeTestDB(t, db)
	})
	if err := store.migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := store.installOnlineMigrationCapture(context.Background(), "test-migration", "postgres-source", "primary", 1); err != nil {
		t.Fatal(err)
	}
	testOnlineMigrationCapture(t, store, db)
	testOnlineMigrationCaptureSerialization(t, store)
}

func testOnlineMigrationCapture(t *testing.T, store *DBStore, originalDB *gorm.DB) {
	t.Helper()
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	// Real nested business transactions must remain one replication group.
	_, err = store.ApplyMutationWithAudit(AuditEvent{Actor: "test", Action: "preferences.update"}, func(tx Store) error {
		_, err := tx.UpsertAccountPreferenceAndSecrets(AccountPreference{
			ScopeType: AccountScopeTypeAccount, ScopeID: 1, Namespace: "sandbox", Key: "config", ValueJSON: `{"enabled":false}`,
		}, AccountSecret{ScopeType: AccountScopeTypeAccount, ScopeID: 1, KeyType: "sandbox_api_key", EncryptedValue: "encrypted-value"})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	groups, changes := captureRows(t, store)
	if len(groups) != 1 || groups[0].ChangeCount != 3 || len(changes) != 3 {
		t.Fatalf("nested transaction groups=%d changes=%d", len(groups), len(changes))
	}
	for _, change := range changes {
		if change.TransactionPosition != groups[0].Position {
			t.Fatal("nested business/audit mutations split across source transactions")
		}
		if strings.Contains(fmt.Sprint(change)+fmt.Sprintf("%#v", change), "encrypted-value") {
			t.Fatal("migration diagnostics exposed a row payload")
		}
	}

	// The wide Runner image exceeds SQLite's single json_object argument limit.
	// Explicit NULL keys must survive the chunked object encoding.
	_, _, err = store.CreateRequest(RunnerRequest{ID: "capture-runner", RepositoryFullName: "owner/repo", Labels: []string{"qiniu"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.AppendStagedLog("capture-runner", "control.log", "runner_exit", []byte("exit evidence\n"))
	_, changes = captureRows(t, store)
	var row map[string]json.RawMessage
	var runnerImage, eventImage bool
	for _, change := range changes {
		if change.Table == "runner_requests" {
			if err := json.Unmarshal([]byte(change.RowJSON), &row); err != nil {
				t.Fatal(err)
			}
			if value, exists := row["runner_exit_code"]; !exists || string(value) != "null" {
				t.Fatal("nullable Runner field was omitted or changed")
			}
			inventory, err := newOnlineMigrationInventory()
			if err != nil {
				t.Fatal(err)
			}
			for _, table := range inventory.Tables {
				if table.Name == "runner_requests" && len(row) != len(table.Columns) {
					t.Fatalf("wide Runner image has %d fields, want %d", len(row), len(table.Columns))
				}
			}
			runnerImage = true
		}
		if change.Table == "runner_events" {
			eventImage = true
		}
	}
	if !runnerImage || !eventImage {
		t.Fatal("Runner state or callback log write escaped capture")
	}

	// Raw SQL is captured inside a managed SQL transaction, including batch
	// updates and primary-key changes. Statements outside it must fail closed.
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&[]accountRecord{{ID: 1001, Role: "user"}, {ID: 1002, Role: "user"}}).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE accounts SET role = 'admin' WHERE id IN (1001, 1002)").Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE accounts SET id = 1003 WHERE id = 1001").Error; err != nil {
			return err
		}
		return tx.Exec("DELETE FROM accounts WHERE id IN (1002, 1003)").Error
	}); err != nil {
		t.Fatal(err)
	}
	groups, changes = captureRows(t, store)
	last := groups[len(groups)-1]
	if last.ChangeCount != 8 {
		t.Fatalf("batch/raw/primary-key changes = %d, want 8", last.ChangeCount)
	}
	var keyOps []string
	for _, change := range changes {
		if change.TransactionPosition == last.Position {
			var key map[string]int64
			if err := json.Unmarshal([]byte(change.KeyJSON), &key); err != nil {
				t.Fatal(err)
			}
			keyOps = append(keyOps, fmt.Sprintf("%s:%d", change.Operation, key["id"]))
		}
	}
	if !reflect.DeepEqual(keyOps, []string{"upsert:1001", "upsert:1002", "upsert:1001", "upsert:1002", "delete:1001", "upsert:1003", "delete:1002", "delete:1003"}) {
		t.Fatalf("ordered changes = %v", keyOps)
	}
	if err := originalDB.Create(&accountRecord{ID: 2001, Role: "user"}).Error; err == nil {
		t.Fatal("an old unmanaged GORM handle wrote business data")
	}
	if err := db.Exec("INSERT INTO accounts (id, role, created_at, updated_at) VALUES (2002, 'user', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)").Error; err == nil {
		t.Fatal("unmanaged raw SQL wrote business data")
	}

	beforeGroups, beforeChanges := captureRows(t, store)
	rollback := errors.New("rollback source transaction")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&accountRecord{ID: 3001, Role: "user"}).Error; err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatalf("source rollback error = %v", err)
	}
	groups, changes = captureRows(t, store)
	if len(groups) != len(beforeGroups) || len(changes) != len(beforeChanges) {
		t.Fatal("rolled-back source transaction left outbox records")
	}
	var count int64
	if err := db.Model(&accountRecord{}).Where("id = ?", 3001).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("rolled-back business data count = %d, err=%v", count, err)
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&accountRecord{ID: 4001, Role: "user"}).Error; err != nil {
			return err
		}
		if err := tx.Transaction(func(inner *gorm.DB) error {
			if err := inner.Create(&accountRecord{ID: 4002, Role: "admin"}).Error; err != nil {
				return err
			}
			return rollback
		}); !errors.Is(err, rollback) {
			return fmt.Errorf("nested rollback failed: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	groups, changes = captureRows(t, store)
	if groups[len(groups)-1].ChangeCount != 1 || len(changes) != len(beforeChanges)+1 {
		t.Fatal("savepoint rollback failed to remove nested outbox changes")
	}
	if err := db.Model(&accountRecord{}).Where("id = ?", 4002).Count(&count).Error; err != nil || count != 0 {
		t.Fatal("savepoint rollback retained nested business data")
	}
}

func TestOnlineMigrationCaptureOutboxFailureRollsBackSQLite(t *testing.T) {
	for _, failure := range []string{"change", "group_commit"} {
		t.Run(failure, func(t *testing.T) {
			store, _ := newCapturedSQLite(t)
			db, err := store.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			statement := "CREATE TRIGGER reject_capture BEFORE INSERT ON runnerd_migration_changes BEGIN SELECT RAISE(ABORT, 'outbox unavailable'); END"
			if failure == "group_commit" {
				statement = "CREATE TRIGGER reject_capture BEFORE UPDATE ON runnerd_migration_transactions BEGIN SELECT RAISE(ABORT, 'outbox commit unavailable'); END"
			}
			if err := db.Exec(statement).Error; err != nil {
				t.Fatal(err)
			}
			_, err = store.UpsertAccountPreference(AccountPreference{ScopeType: "account", ScopeID: 1, Namespace: "test", Key: "key", ValueJSON: `{}`})
			if err == nil {
				t.Fatal("business operation succeeded despite outbox failure")
			}
			groups, changes := captureRows(t, store)
			if len(groups) != 0 || len(changes) != 0 {
				t.Fatal("failed capture left committed outbox data")
			}
			var count int64
			if err := db.Model(&accountPreferenceRecord{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("outbox failure left business rows: %d, err=%v", count, err)
			}
		})
	}
}

func TestOnlineMigrationCaptureAuditFailureRollsBackSQLite(t *testing.T) {
	store, _ := newCapturedSQLite(t)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER reject_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'audit unavailable'); END").Error; err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyMutationWithAudit(AuditEvent{Actor: "test", Action: "update"}, func(tx Store) error {
		_, err := tx.UpsertAccountSecret(AccountSecret{ScopeType: "account", ScopeID: 1, KeyType: "test", EncryptedValue: "secret"})
		return err
	})
	if !errors.Is(err, ErrAuditEventPersistence) {
		t.Fatalf("audit failure = %v", err)
	}
	groups, changes := captureRows(t, store)
	if len(groups) != 0 || len(changes) != 0 {
		t.Fatal("failed audit left outbox changes")
	}
	if _, err := store.GetAccountSecret("account", 1, "test"); err == nil {
		t.Fatal("failed audit left business mutation")
	}
}

func TestOnlineMigrationCaptureConcurrentSQLiteWrites(t *testing.T) {
	store, _ := newCapturedSQLite(t)
	const writers = 12
	errs := make(chan error, writers)
	for i := range writers {
		go func() {
			_, err := store.UpsertAccountPreference(AccountPreference{ScopeType: "account", ScopeID: int64(i + 1), Namespace: "test", Key: "value", ValueJSON: `{}`})
			errs <- err
		}()
	}
	for range writers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	groups, changes := captureRows(t, store)
	if len(groups) != writers || len(changes) != writers {
		t.Fatalf("concurrent groups=%d changes=%d", len(groups), len(changes))
	}
	for i, group := range groups {
		if group.ChangeCount != 1 || changes[i].TransactionPosition != group.Position {
			t.Fatal("concurrent transaction identity was mixed")
		}
	}
}

func testOnlineMigrationCaptureSerialization(t *testing.T, store *DBStore) {
	t.Helper()
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- db.Transaction(func(tx *gorm.DB) error {
			close(entered)
			<-release
			return tx.Create(&accountRecord{ID: 5001, Role: "user"}).Error
		})
	}()
	select {
	case <-entered:
	case err := <-firstDone:
		t.Fatalf("first managed transaction failed before body: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("first managed transaction failed to start")
	}
	// Deadline expiry proves the second transaction cannot allocate a group
	// or reach its body before the first commit (rather than relying on sleeps).
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	err = db.WithContext(ctx).Transaction(func(_ *gorm.DB) error {
		t.Error("concurrent transaction passed the commit-order lock")
		return nil
	})
	close(release)
	if err == nil {
		t.Fatal("concurrent transaction unexpectedly succeeded")
	}
	if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatalf("waiting writer failed before its lock deadline: %v", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	_, err = store.UpsertAccountPreference(AccountPreference{ScopeType: "account", ScopeID: 5002, Namespace: "test", Key: "value", ValueJSON: `{}`})
	if err != nil {
		t.Fatalf("capture did not recover after a waiting writer was cancelled: %v", err)
	}
}

func TestOnlineMigrationCaptureAttachmentRejectsIdentityDrift(t *testing.T) {
	store, original := newCapturedSQLite(t)
	beforeGroups, beforeChanges := captureRows(t, store)
	for name, change := range map[string]func(*migrationControlRecord){
		"protocol": func(c *migrationControlRecord) { c.ProtocolVersion++ },
		"schema":   func(c *migrationControlRecord) { c.SchemaFingerprint = strings.Repeat("b", 64) },
		"epoch":    func(c *migrationControlRecord) { c.Epoch++ },
		"instance": func(c *migrationControlRecord) { c.InstanceID = "different-instance" },
		"role":     func(c *migrationControlRecord) { c.Role = "replica" },
	} {
		t.Run(name, func(t *testing.T) {
			var control migrationControlRecord
			if err := original.First(&control, 1).Error; err != nil {
				t.Fatal(err)
			}
			changed := control
			change(&changed)
			if err := original.Save(&changed).Error; err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := original.Save(&control).Error; err != nil {
					t.Error(err)
				}
			}()
			reattached := &DBStore{opts: store.opts, db: original, migrated: true}
			if err := reattached.installOnlineMigrationCapture(context.Background(), "test-migration", "sqlite-source", "primary", 1); !errors.Is(err, errOnlineMigrationCapture) {
				t.Fatalf("changed capture identity error = %v", err)
			}
			var after migrationControlRecord
			if err := original.First(&after, 1).Error; err != nil || after != changed {
				t.Fatal("attachment silently repaired a mismatched authority")
			}
		})
	}
	groups, changes := captureRows(t, store)
	if len(groups) != len(beforeGroups) || len(changes) != len(beforeChanges) {
		t.Fatal("rejected attachment changed migration history")
	}
}

func TestOnlineMigrationCaptureReplicaRejectsBusinessWrites(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestDB(t, db) })
	if err := store.installOnlineMigrationCapture(context.Background(), "migration", "replica", "replica", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountSecret(AccountSecret{ScopeType: "account", ScopeID: 1, KeyType: "key", EncryptedValue: "secret"}); !errors.Is(err, errOnlineMigrationCapture) {
		t.Fatalf("replica write error = %v", err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&migrationControlRecord{}).Where("id = 1").Update("active_transaction", -1).Error; err != nil {
			return err
		}
		if err := tx.Create(&accountRecord{ID: 42, Role: "user", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}).Error; err != nil {
			return err
		}
		return tx.Model(&migrationControlRecord{}).Where("id = 1").Update("active_transaction", 0).Error
	}); err != nil {
		t.Fatalf("replication context failed: %v", err)
	}
	groups, changes := captureRows(t, store)
	if len(groups) != 0 || len(changes) != 0 {
		t.Fatal("replication wrote reverse outbox changes")
	}
}
