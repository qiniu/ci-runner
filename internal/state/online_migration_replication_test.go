package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func migrationTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func newMigrationReplicaSQLite(t *testing.T) (*DBStore, *onlineMigrationReplica) {
	t.Helper()
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestDB(t, db) })
	if err := store.installOnlineMigrationCapture(context.Background(), "test-migration", "sqlite-target", "replica", 1); err != nil {
		t.Fatal(err)
	}
	replica, err := newOnlineMigrationReplica(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	return store, replica
}

func migrationTestSnapshot(t *testing.T, source *DBStore) *onlineMigrationSnapshot {
	t.Helper()
	snapshot, err := source.createOnlineMigrationSnapshot(migrationTestContext(t), filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	return snapshot
}

func finishMigrationBaseline(t *testing.T, replica *onlineMigrationReplica, snapshot *onlineMigrationSnapshot, limit int) {
	t.Helper()
	ctx := migrationTestContext(t)
	for range 200 {
		complete, err := replica.copySnapshotBatch(ctx, snapshot, limit)
		if err != nil {
			t.Fatal(err)
		}
		if complete {
			return
		}
	}
	t.Fatal("baseline made no bounded progress")
}

func drainMigrationSource(t *testing.T, replica *onlineMigrationReplica, source *onlineMigrationSource) {
	t.Helper()
	ctx := migrationTestContext(t)
	for range 200 {
		applied, err := replica.applyNext(ctx, source)
		if err != nil {
			t.Fatal(err)
		}
		if !applied {
			return
		}
	}
	t.Fatal("replication did not catch up")
}

func TestOnlineMigrationReplicationSQLite(t *testing.T) {
	target, replica := newMigrationReplicaSQLite(t)
	testOnlineMigrationReplication(t, target, replica)
}

func TestOnlineMigrationReplicationSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("set RUNNERD_CATALOG_BACKEND_TESTS=1 with a dedicated PostgreSQL test database")
	}
	dsn := os.Getenv("RUNNERD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("RUNNERD_POSTGRES_TEST_DSN is required")
	}
	t.Run("baseline_and_replay", func(t *testing.T) {
		store, replica := newMigrationReplicaPostgres(t, dsn)
		testOnlineMigrationReplication(t, store, replica)
	})
	for _, stage := range []string{"business", "checkpoint", "disconnect", "incomplete outbox", "malformed outbox"} {
		t.Run(stage, func(t *testing.T) {
			_, replica := newMigrationReplicaPostgres(t, dsn)
			testMigrationReplicationAtomicFailure(t, replica, stage)
		})
	}
}

func newMigrationReplicaPostgres(t *testing.T, dsn string) (*DBStore, *onlineMigrationReplica) {
	t.Helper()
	store := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn}).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	requireCatalogMatcherTestDatabase(t, db, BackendPostgres)
	resetSQLBackendTestTables(t, db)
	t.Cleanup(func() {
		resetSQLBackendTestTables(t, db)
		if err := db.Exec("DROP FUNCTION IF EXISTS runnerd_migration_test_fault()").Error; err != nil {
			t.Error(err)
		}
		inventory, _ := newOnlineMigrationInventory()
		for _, table := range inventory.Tables {
			if err := db.Exec("DROP FUNCTION IF EXISTS " + migrationQuoted("runnerd_mig_"+table.Name) + "()").Error; err != nil {
				t.Error(err)
			}
		}
		for _, model := range []any{&migrationBaselineRecord{}, &migrationCheckpointRecord{}, &migrationChangeRecord{}, &migrationTransactionRecord{}, &migrationRetentionRecord{}, &migrationFrontierRecord{}, &migrationControlRecord{}} {
			if err := db.Migrator().DropTable(model); err != nil {
				t.Error(err)
			}
		}
		closeTestDB(t, db)
	})
	if err := store.migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := store.installOnlineMigrationCapture(context.Background(), "test-migration", "postgres-target", "replica", 1); err != nil {
		t.Fatal(err)
	}
	replica, err := newOnlineMigrationReplica(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	return store, replica
}

func testOnlineMigrationReplication(t *testing.T, target *DBStore, replica *onlineMigrationReplica) {
	t.Helper()
	source, _ := newCapturedSQLite(t)
	db, _ := source.dbOrEnsure()
	stamp := time.Date(2026, 10, 22, 12, 30, 1, 123456789, time.UTC)
	const largeID int64 = 9007199254740993
	// Every active table participates. Include nullable pointers, false/zero
	// fields, encrypted bytes-as-text and values above JSON float precision.
	models := []any{
		&accountRecord{ID: largeID, Role: "user", CreatedAt: stamp, UpdatedAt: stamp},
		&oauthIdentityRecord{ID: largeID, AccountID: largeID, OAuthProvider: "github", OAuthSubject: "subject", CreatedAt: stamp, UpdatedAt: stamp},
		&githubInstallationRecord{ID: largeID, AccountID: largeID, InstallationID: largeID, CreatedAt: stamp, UpdatedAt: stamp},
		&githubInstallationOwnerRecord{InstallationID: largeID, GitHubAccountID: largeID, CreatedAt: stamp, UpdatedAt: stamp},
		&accountSecretRecord{ID: largeID, ScopeType: "account", ScopeID: largeID, KeyType: "sandbox", EncryptedValue: "opaque-encrypted-value", CreatedAt: stamp, UpdatedAt: stamp},
		&accountPreferenceRecord{ID: largeID, ScopeType: "account", ScopeID: largeID, Namespace: "sandbox", Key: "settings", ValueJSON: `{"enabled":false}`, CreatedAt: stamp, UpdatedAt: stamp},
		&auditEventRecord{ID: largeID, Actor: "test", Action: "seed", ResourceType: "test", ResourceID: "test", CreatedAt: stamp},
		&runnerRequestRecord{ID: "runner", Source: "test", LabelsJSON: "[]", Status: "running", ProcessPID: 4294967295, Version: 99, QueuedAt: stamp, UpdatedAt: stamp, RunningAt: &stamp},
		&runnerEventRecord{ID: largeID, RequestID: "runner", EventType: "stdout", Message: "日志\n\"line\"", CreatedAt: stamp},
		&runnerProfileRecord{Name: "first", LabelsJSON: "[]", TemplateSource: "template_name", TemplateID: "public", MaxConcurrency: 0, Enabled: false, CreatedAt: stamp, UpdatedAt: stamp},
		&scopedRunnerProfileRecord{ScopeType: "account", ScopeID: largeID, Name: "profile", LabelKey: "unique", WorkflowLabelsJSON: "[]", CreatedAt: stamp, UpdatedAt: stamp},
		&forkSponsorshipPolicyRecord{SponsorInstallationID: largeID, SourceRepositoryID: largeID, CreatedAt: stamp, UpdatedAt: stamp},
		&forkSponsorshipApprovalRecord{SourceRepositoryID: largeID, ForkRepositoryID: largeID, CreatedAt: stamp, UpdatedAt: stamp},
		&sandboxServiceDefaultRecord{ID: 1, Enabled: false, APIKeyEncrypted: "opaque-default-key", CreatedAt: stamp, UpdatedAt: stamp},
		&sandboxServiceDefaultAudienceRecord{ID: largeID, GitHubAccountID: largeID, AccountType: "User", CreatedAt: stamp, UpdatedAt: stamp},
		&deploymentRecord{ID: "deploy", RevisionNumber: 2, SpecJSONEncrypted: "opaque-plan", Status: "running", Version: 42, RunningAt: &stamp, ExpiresAt: stamp, CreatedAt: stamp, UpdatedAt: stamp},
		&deploymentRevisionRecord{ID: "revision", DeploymentID: "deploy", Number: 2, SpecJSONEncrypted: "opaque-plan", ExpiresAt: stamp, CreatedAt: stamp, UpdatedAt: stamp},
		&deploymentEventRecord{ID: largeID, DeploymentID: "deploy", RevisionNumber: 2, EventType: "stdout", Message: "deployment output", CreatedAt: stamp},
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		for _, model := range models {
			if err := tx.Create(model).Error; err != nil {
				return err
			}
		}
		for _, name := range []string{"Z", "a", "Å", "é", "中", "😀"} {
			if err := tx.Create(&runnerProfileRecord{Name: name, LabelsJSON: "[]", CreatedAt: stamp, UpdatedAt: stamp}).Error; err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := migrationTestSnapshot(t, source)
	reader, err := newOnlineMigrationSource(migrationTestContext(t), source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := replica.applyNext(migrationTestContext(t), reader); !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("replay before completed baseline: %v", err)
	}
	if complete, err := replica.copySnapshotBatch(migrationTestContext(t), snapshot, 1); err != nil || complete {
		t.Fatalf("first baseline page complete=%v err=%v", complete, err)
	}
	// Source commits while the importer is paused mid-baseline. These changes
	// must wait for replay and must not be overwritten by older snapshot rows.
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Delete(&accountRecord{}, largeID).Error; err != nil {
			return err
		}
		if err := tx.Create(&accountRecord{ID: largeID, Role: "admin", CreatedAt: stamp, UpdatedAt: stamp}).Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE runner_profiles SET name = ? WHERE name = ?", "renamed", "first").Error; err != nil {
			return err
		}
		if err := tx.Exec("UPDATE scoped_runner_profiles SET name = ? WHERE name = ?", "renamed", "profile").Error; err != nil {
			return err
		}
		if err := tx.Model(&runnerRequestRecord{}).Where("id = ?", "runner").UpdateColumns(map[string]any{"running_at": nil, "process_pid": 0, "version": 100}).Error; err != nil {
			return err
		}
		return tx.Create(&runnerEventRecord{RequestID: "runner", EventType: "stderr", Message: "after baseline", CreatedAt: stamp}).Error
	}); err != nil {
		t.Fatal(err)
	}
	later := migrationTestSnapshot(t, source)
	if _, err := replica.copySnapshotBatch(migrationTestContext(t), later, 1); !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("baseline accepted a different artifact: %v", err)
	}
	resumed, err := openOnlineMigrationSnapshot(migrationTestContext(t), snapshot.Path, snapshot.Control)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.Digest != snapshot.Digest {
		t.Fatal("reopened baseline digest changed")
	}
	replica, err = newOnlineMigrationReplica(migrationTestContext(t), target)
	if err != nil {
		t.Fatal(err)
	}
	finishMigrationBaseline(t, replica, resumed, 1)
	if _, err := replica.verifySnapshot(migrationTestContext(t), snapshot, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := replica.verifySnapshot(migrationTestContext(t), later, 1); !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("verification accepted different checkpoints: %v", err)
	}
	type applyResult struct {
		applied bool
		err     error
	}
	results := make(chan applyResult, 2)
	ctx := migrationTestContext(t)
	for range 2 {
		go func() {
			applied, err := replica.applyNext(ctx, reader)
			results <- applyResult{applied, err}
		}()
	}
	appliedCount := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.applied {
			appliedCount++
		}
	}
	if appliedCount != 1 {
		t.Fatalf("concurrent consumers applied one source group %d times", appliedCount)
	}
	// Catching up twice simulates resuming after an ambiguous successful commit.
	drainMigrationSource(t, replica, reader)
	report, err := replica.verifySnapshot(migrationTestContext(t), later, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Tables) != 18 || report.Checkpoint != later.Checkpoint {
		t.Fatal("verification omitted business tables or changed checkpoint")
	}
	for _, table := range report.Tables {
		if table.Rows == 0 || len(table.Digest) != 64 {
			t.Fatalf("table %s was not verified", table.Table)
		}
	}
	var account accountRecord
	if err := replica.db.First(&account, largeID).Error; err != nil || account.Role != "admin" || !account.UpdatedAt.Equal(stamp.Truncate(time.Microsecond)) {
		t.Fatalf("account identity, value or timestamp lost: err=%v", err)
	}
	if target.opts.Backend == BackendPostgres {
		// pgx must bind original timestamps with the same precision as the
		// copied row, preserving existing updated_at optimistic conditions.
		if err := replica.transaction(migrationTestContext(t), func(tx *gorm.DB) error {
			result := tx.Table("accounts").Where("id = ? AND updated_at = ?", largeID, stamp).Update("role", "admin")
			if result.Error == nil && result.RowsAffected != 1 {
				return fmt.Errorf("optimistic timestamp condition no longer matches")
			}
			return result.Error
		}); err != nil {
			t.Fatal(err)
		}
	}
	var groups, changes int64
	if err := replica.db.Model(&migrationTransactionRecord{}).Count(&groups).Error; err != nil {
		t.Fatal(err)
	}
	if err := replica.db.Model(&migrationChangeRecord{}).Count(&changes).Error; err != nil {
		t.Fatal(err)
	}
	if groups != 0 || changes != 0 {
		t.Fatal("replica writes were captured recursively")
	}
	checkpoint, err := readMigrationCheckpoint(replica.db, snapshot.Control)
	if err != nil || !checkpoint.Verified || checkpoint.VerifiedPosition != later.Checkpoint {
		t.Fatalf("verification checkpoint was not retained: %v", err)
	}
}

func TestOnlineMigrationReplicationAtomicFailureAndRetry(t *testing.T) {
	for _, stage := range []string{"business", "checkpoint", "incomplete outbox", "malformed outbox"} {
		t.Run(stage, func(t *testing.T) {
			_, replica := newMigrationReplicaSQLite(t)
			testMigrationReplicationAtomicFailure(t, replica, stage)
		})
	}
}

func testMigrationReplicationAtomicFailure(t *testing.T, replica *onlineMigrationReplica, stage string) {
	t.Helper()
	source, sourceNative := newCapturedSQLite(t)
	snapshot := migrationTestSnapshot(t, source)
	finishMigrationBaseline(t, replica, snapshot, 2)
	db, _ := source.dbOrEnsure()
	if err := db.Create(&[]accountRecord{{ID: 1, Role: "user"}, {ID: 2, Role: "admin"}}).Error; err != nil {
		t.Fatal(err)
	}
	reader, err := newOnlineMigrationSource(migrationTestContext(t), source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	faultDB := replica.db
	query := "CREATE TRIGGER migration_test_failure BEFORE INSERT ON accounts WHEN NEW.id = 2 BEGIN SELECT RAISE(ABORT, 'sensitive rejected row'); END"
	if stage == "checkpoint" {
		query = "CREATE TRIGGER migration_test_failure BEFORE UPDATE ON runnerd_migration_checkpoints BEGIN SELECT RAISE(ABORT, 'sensitive checkpoint'); END"
	}
	if replica.db.Dialector.Name() == BackendPostgres && stage != "incomplete outbox" && stage != "malformed outbox" {
		body := "RAISE EXCEPTION 'sensitive rejected row';"
		if stage == "disconnect" {
			body = "PERFORM pg_terminate_backend(pg_backend_pid());"
		}
		if err := replica.db.Exec("CREATE FUNCTION runnerd_migration_test_fault() RETURNS trigger LANGUAGE plpgsql AS $fault$ BEGIN " + body + " RETURN NEW; END $fault$").Error; err != nil {
			t.Fatal(err)
		}
		query = "CREATE TRIGGER migration_test_failure BEFORE INSERT ON accounts FOR EACH ROW WHEN (NEW.id = 2) EXECUTE FUNCTION runnerd_migration_test_fault()"
		if stage == "checkpoint" || stage == "disconnect" {
			query = "CREATE TRIGGER migration_test_failure BEFORE UPDATE ON runnerd_migration_checkpoints FOR EACH ROW EXECUTE FUNCTION runnerd_migration_test_fault()"
		}
	}
	if stage == "incomplete outbox" {
		faultDB = sourceNative
		query = "UPDATE runnerd_migration_transactions SET change_count = change_count + 1"
	}
	var originalChange migrationChangeRecord
	if stage == "malformed outbox" {
		if err := sourceNative.Order("id DESC").First(&originalChange).Error; err != nil {
			t.Fatal(err)
		}
		faultDB = sourceNative
		query = "UPDATE runnerd_migration_changes SET row_json = 'sensitive invalid json' WHERE id = (SELECT MAX(id) FROM runnerd_migration_changes)"
	}
	if err := faultDB.Exec(query).Error; err != nil {
		t.Fatal(err)
	}
	applied, err := replica.applyNext(migrationTestContext(t), reader)
	if err == nil || applied || strings.Contains(err.Error(), "sensitive") {
		t.Fatalf("fault was accepted or leaked payload: applied=%v err=%v", applied, err)
	}
	var rows int64
	if err := replica.db.Model(&accountRecord{}).Count(&rows).Error; err != nil || rows != 0 {
		t.Fatalf("partial business transaction retained: rows=%d err=%v", rows, err)
	}
	checkpoint, err := readMigrationCheckpoint(replica.db, snapshot.Control)
	if err != nil || checkpoint.Position != snapshot.Checkpoint {
		t.Fatalf("failed transaction advanced checkpoint: %v", err)
	}
	if stage == "incomplete outbox" {
		query = "UPDATE runnerd_migration_transactions SET change_count = change_count - 1"
	} else if stage == "malformed outbox" {
		if err := sourceNative.Model(&migrationChangeRecord{}).Where("id = ?", originalChange.ID).Update("row_json", originalChange.RowJSON).Error; err != nil {
			t.Fatal(err)
		}
		query = "SELECT 1"
	} else {
		query = "DROP TRIGGER migration_test_failure"
		if replica.db.Dialector.Name() == BackendPostgres {
			query += " ON accounts"
			if stage == "checkpoint" || stage == "disconnect" {
				query = "DROP TRIGGER migration_test_failure ON runnerd_migration_checkpoints"
			}
		}
	}
	if err := faultDB.Exec(query).Error; err != nil {
		t.Fatal(err)
	}
	drainMigrationSource(t, replica, reader)
	if _, err := replica.verifySnapshot(migrationTestContext(t), migrationTestSnapshot(t, source), 1); err != nil {
		t.Fatal(err)
	}
}

func TestOnlineMigrationVerificationDetectsDamageAndClearsEvidence(t *testing.T) {
	source, _ := newCapturedSQLite(t)
	db, _ := source.dbOrEnsure()
	if err := db.Create(&accountRecord{ID: 1, Role: "user"}).Error; err != nil {
		t.Fatal(err)
	}
	_, replica := newMigrationReplicaSQLite(t)
	snapshot := migrationTestSnapshot(t, source)
	finishMigrationBaseline(t, replica, snapshot, 1)
	ctx := migrationTestContext(t)
	if _, err := replica.verifySnapshot(ctx, snapshot, 1); err != nil {
		t.Fatal(err)
	}
	if err := replica.transaction(ctx, func(tx *gorm.DB) error {
		return tx.Table("accounts").Where("id = 1").Update("role", "damaged-private-value").Error
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := replica.verifySnapshot(ctx, snapshot, 1); !errors.Is(err, errOnlineMigrationNotReady) || strings.Contains(err.Error(), "private") {
		t.Fatalf("damaged target was accepted or leaked: %v", err)
	}
	checkpoint, err := readMigrationCheckpoint(replica.db, snapshot.Control)
	if err != nil || checkpoint.Verified {
		t.Fatalf("failed verification retained success evidence: %v", err)
	}
}

func TestOnlineMigrationReplicationRejectsIdentityDriftAndSourceRewind(t *testing.T) {
	for _, drift := range []string{"source epoch", "target role", "rewind"} {
		t.Run(drift, func(t *testing.T) {
			source, native := newCapturedSQLite(t)
			db, _ := source.dbOrEnsure()
			if err := db.Create(&accountRecord{ID: 1, Role: "user"}).Error; err != nil {
				t.Fatal(err)
			}
			_, replica := newMigrationReplicaSQLite(t)
			snapshot := migrationTestSnapshot(t, source)
			finishMigrationBaseline(t, replica, snapshot, 2)
			reader, err := newOnlineMigrationSource(migrationTestContext(t), source)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			switch drift {
			case "source epoch":
				err = native.Exec("UPDATE runnerd_migration_control SET epoch = 2").Error
			case "target role":
				err = replica.db.Exec("UPDATE runnerd_migration_control SET role = 'primary'").Error
			case "rewind":
				err = native.Exec("DELETE FROM runnerd_migration_transactions").Error
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := replica.applyNext(migrationTestContext(t), reader); !errors.Is(err, errOnlineMigrationNotReady) {
				t.Fatalf("replication accepted %s: %v", drift, err)
			}
		})
	}
}

func TestOnlineMigrationBaselineRejectsNonemptyTargetAndRollsBackPage(t *testing.T) {
	source, _ := newCapturedSQLite(t)
	db, _ := source.dbOrEnsure()
	if err := db.Create(&[]accountRecord{{ID: 1, Role: "user"}, {ID: 2, Role: "admin"}}).Error; err != nil {
		t.Fatal(err)
	}
	snapshot := migrationTestSnapshot(t, source)
	_, replica := newMigrationReplicaSQLite(t)
	if err := replica.transaction(migrationTestContext(t), func(tx *gorm.DB) error {
		return tx.Create(&accountRecord{ID: 99, Role: "existing"}).Error
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := replica.copySnapshotBatch(migrationTestContext(t), snapshot, 2); !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("nonempty target was accepted: %v", err)
	}
	var account accountRecord
	if err := replica.db.First(&account, 99).Error; err != nil || account.Role != "existing" {
		t.Fatal("nonempty target was changed")
	}
	_, replica = newMigrationReplicaSQLite(t)
	for _, table := range replica.inventory.Tables {
		if table.Name == "accounts" {
			break
		}
		if _, err := replica.copySnapshotBatch(migrationTestContext(t), snapshot, 2); err != nil {
			t.Fatal(err)
		}
	}
	var before migrationBaselineRecord
	if err := replica.db.First(&before, 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := replica.db.Exec("CREATE TRIGGER migration_test_failure BEFORE INSERT ON accounts WHEN NEW.id = 2 BEGIN SELECT RAISE(ABORT, 'private payload'); END").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := replica.copySnapshotBatch(migrationTestContext(t), snapshot, 2); err == nil || strings.Contains(err.Error(), "private payload") {
		t.Fatalf("baseline failure was accepted or leaked: %v", err)
	}
	var rows int64
	if err := replica.db.Model(&accountRecord{}).Count(&rows).Error; err != nil {
		t.Fatal(err)
	}
	var progress migrationBaselineRecord
	if err := replica.db.First(&progress, 1).Error; err != nil {
		t.Fatal(err)
	}
	if rows != 0 || progress != before {
		t.Fatal("failed baseline page retained partial rows or progress")
	}
	if err := replica.db.Exec("DROP TRIGGER migration_test_failure").Error; err != nil {
		t.Fatal(err)
	}
	finishMigrationBaseline(t, replica, snapshot, 1)
	if _, err := replica.verifySnapshot(migrationTestContext(t), snapshot, 1); err != nil {
		t.Fatal(err)
	}
}

func TestOnlineMigrationReplicationSourceWritesContinueDuringStreaming(t *testing.T) {
	source, _ := newCapturedSQLite(t)
	_, replica := newMigrationReplicaSQLite(t)
	snapshot := migrationTestSnapshot(t, source)
	finishMigrationBaseline(t, replica, snapshot, 50)
	db, _ := source.dbOrEnsure()
	var accounts []accountRecord
	for i := int64(1); i <= 300; i++ {
		accounts = append(accounts, accountRecord{ID: i, Role: "user"})
	}
	if err := db.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	ctx := migrationTestContext(t)
	reader, err := newOnlineMigrationSource(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	paused, release := make(chan struct{}), make(chan struct{})
	var pauseOnce, releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	if err := reader.db.Callback().Query().After("gorm:query").Register("migration_test_pause", func(tx *gorm.DB) {
		if tx.Statement.Table == "runnerd_migration_changes" {
			pauseOnce.Do(func() {
				close(paused)
				select {
				case <-release:
				case <-ctx.Done():
				}
			})
		}
	}); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, err := replica.applyNext(ctx, reader)
		result <- err
	}()
	select {
	case <-paused:
	case err := <-result:
		t.Fatalf("stream ended before first page: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// A source read transaction remains open while this write commits. Using
	// the single business pool for streaming would block until cancellation.
	writeCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	err = db.WithContext(writeCtx).Create(&accountRecord{ID: 999, Role: "admin"}).Error
	cancel()
	releaseOnce.Do(func() { close(release) })
	if err != nil {
		t.Fatalf("stream blocked source business writes: %v", err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	drainMigrationSource(t, replica, reader)
	if _, err := replica.verifySnapshot(ctx, migrationTestSnapshot(t, source), 100); err != nil {
		t.Fatal(err)
	}
}
