package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestOnlineMigrationSnapshotUsesItsOwnCheckpointAndCanResume(t *testing.T) {
	store, _ := newCapturedSQLite(t)
	preference := AccountPreference{ScopeType: "account", ScopeID: 1, Namespace: "test", Key: "value", ValueJSON: `{"value":1}`}
	if _, err := store.UpsertAccountPreference(preference); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	snapshot, err := store.createOnlineMigrationSnapshot(ctx, filepath.Join(t.TempDir(), "private snapshots"))
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := captureRows(t, store)
	if snapshot.Checkpoint != groups[len(groups)-1].Position {
		t.Fatal("snapshot checkpoint does not match the committed baseline")
	}
	preference.ValueJSON = `{"value":2}`
	if _, err := store.UpsertAccountPreference(preference); err != nil {
		t.Fatal(err)
	}
	assertBaseline := func(snapshot *onlineMigrationSnapshot) {
		t.Helper()
		var row accountPreferenceRecord
		if err := snapshot.db.First(&row).Error; err != nil {
			t.Fatal(err)
		}
		if row.ValueJSON != `{"value":1}` {
			t.Fatal("resumable baseline changed after a live source update")
		}
		if err := snapshot.db.Exec("DELETE FROM account_preferences").Error; err == nil {
			t.Fatal("the baseline artifact accepted a write")
		}
	}
	assertBaseline(snapshot)
	path, control, position := snapshot.Path, snapshot.Control, snapshot.Checkpoint
	if err := snapshot.Close(); err != nil {
		t.Fatal(err)
	}
	resumed, err := openOnlineMigrationSnapshot(ctx, path, control)
	if err != nil {
		t.Fatal(err)
	}
	defer resumed.Close()
	if resumed.Checkpoint != position {
		t.Fatal("resume advanced to the live source checkpoint")
	}
	assertBaseline(resumed)
	control.InstanceID = "wrong-instance"
	if wrong, err := openOnlineMigrationSnapshot(ctx, path, control); err == nil {
		_ = wrong.Close()
		t.Fatal("snapshot from another source instance was accepted")
	}
}

func TestOnlineMigrationSnapshotPreservesConcurrentTransactions(t *testing.T) {
	store, _ := newCapturedSQLite(t)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	// A source transaction must appear entirely in the snapshot or entirely in
	// the subsequent outbox, never as mixed business data and checkpoint views.
	for i := range 20 {
		writer := make(chan error, 1)
		go func() {
			writer <- db.Transaction(func(tx *gorm.DB) error {
				return tx.Create(&[]accountRecord{{ID: int64(i*2 + 1), Role: "user"}, {ID: int64(i*2 + 2), Role: "user"}}).Error
			})
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		snapshot, err := store.createOnlineMigrationSnapshot(ctx, filepath.Join(t.TempDir(), "private"))
		cancel()
		writeErr := <-writer
		if err != nil || writeErr != nil {
			t.Fatalf("snapshot error=%v, concurrent write error=%v", err, writeErr)
		}
		var rows, changes int64
		if err := snapshot.db.Model(&accountRecord{}).Count(&rows).Error; err != nil {
			t.Fatal(err)
		}
		if err := snapshot.db.Model(&migrationChangeRecord{}).Where("table_name = ?", "accounts").Count(&changes).Error; err != nil {
			t.Fatal(err)
		}
		if rows%2 != 0 || rows != changes || rows != snapshot.Checkpoint*2 {
			t.Fatalf("snapshot split a source transaction: rows=%d changes=%d checkpoint=%d", rows, changes, snapshot.Checkpoint)
		}
		if err := snapshot.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOnlineMigrationSnapshotRefusesUnsafeOrCancelledCreation(t *testing.T) {
	store, _ := newCapturedSQLite(t)
	root := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.createOnlineMigrationSnapshot(ctx, root); !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("unbounded snapshot context error = %v", err)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	public := filepath.Join(root, "public")
	if err := os.Mkdir(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := store.createOnlineMigrationSnapshot(ctx, public); !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("public snapshot directory error = %v", err)
	}
	cancelled, stop := context.WithTimeout(context.Background(), time.Second)
	stop()
	if snapshot, err := store.createOnlineMigrationSnapshot(cancelled, root); err == nil {
		_ = snapshot.Close()
		t.Fatal("cancelled snapshot succeeded")
	}
	files, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Name() != "public" {
		t.Fatal("failed snapshot left a partial baseline artifact")
	}
	link := filepath.Join(root, "linked")
	if err := os.Symlink(public, link); err != nil {
		t.Fatal(err)
	}
	if _, err := store.createOnlineMigrationSnapshot(ctx, link); !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("symlinked snapshot directory error = %v", err)
	}
}

func TestOnlineMigrationSnapshotRetainsHistoricalSequenceWatermarks(t *testing.T) {
	store, _ := newCapturedSQLite(t)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&accountRecord{ID: 900000, Role: "user"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&accountRecord{}, 900000).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	snapshot, err := store.createOnlineMigrationSnapshot(ctx, filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	var count, watermark int64
	if err := snapshot.db.Model(&accountRecord{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("deleted account survived snapshot: count=%d, err=%v", count, err)
	}
	if err := snapshot.db.Raw("SELECT seq FROM sqlite_sequence WHERE name = ?", "accounts").Scan(&watermark).Error; err != nil || watermark != 900000 {
		t.Fatalf("historical sequence watermark = %d, err=%v", watermark, err)
	}
}
