package state

import (
	"testing"

	"gorm.io/gorm"
)

func TestOnlineMigrationRetentionBoundedAndGap(t *testing.T) {
	source, _ := newCapturedSQLite(t)
	_, replica := newMigrationReplicaSQLite(t)
	ctx := migrationTestContext(t)
	snapshot := migrationTestSnapshot(t, source)
	finishMigrationBaseline(t, replica, snapshot, 10)
	reader, err := newOnlineMigrationSource(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	c := &onlineMigrationCoordinator{source: reader, replica: replica}
	if err := c.prune(ctx); err == nil {
		t.Fatal("retention accepted unverified replica")
	}
	var accounts []accountRecord
	for id := int64(1); id <= 300; id++ {
		accounts = append(accounts, accountRecord{ID: id, Role: "user"})
	}
	if err := source.db.Create(&accounts).Error; err != nil {
		t.Fatal(err)
	}
	drainMigrationSource(t, replica, reader)
	if _, err := replica.verifySnapshot(ctx, migrationTestSnapshot(t, source), 100); err != nil {
		t.Fatal(err)
	}
	if err := c.prune(ctx); err != nil {
		t.Fatal(err)
	}
	var remaining int64
	if err := reader.db.Model(&migrationChangeRecord{}).Count(&remaining).Error; err != nil || remaining != 44 {
		t.Fatalf("retention did not bound payload deletion to 256 rows: %d %v", remaining, err)
	}
	if through, err := migrationRetainedThrough(reader.db, reader.control.Epoch); err != nil || through != 0 {
		t.Fatalf("retention published a partially reclaimed transaction: %d %v", through, err)
	}
	if err := c.prune(ctx); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := readMigrationCheckpoint(replica.db, reader.control)
	if err != nil {
		t.Fatal(err)
	}
	if through, err := migrationRetainedThrough(reader.db, reader.control.Epoch); err != nil || through != checkpoint.Position {
		t.Fatalf("reclaimed transaction watermark differs: %d %v", through, err)
	}
	if applied, err := replica.applyNext(ctx, reader); err != nil || applied {
		t.Fatalf("retention lost the latest source position: %t %v", applied, err)
	}
	if err := replica.transaction(ctx, func(tx *gorm.DB) error {
		checkpoint.Position, checkpoint.VerifiedPosition = 0, 0
		return tx.Save(&checkpoint).Error
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := replica.applyNext(ctx, reader); err == nil {
		t.Fatal("stale target silently skipped reclaimed journal")
	}
	var count int64
	if err := source.db.Model(&accountRecord{}).Count(&count).Error; err != nil || count != 300 {
		t.Fatal("journal retention changed business rows")
	}
}
