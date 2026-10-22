package state

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"
)

type migrationRetentionRecord struct {
	SourceEpoch   int64 `gorm:"primaryKey;autoIncrement:false"`
	PrunedThrough int64 `gorm:"not null"`
}

func (migrationRetentionRecord) TableName() string { return "runnerd_migration_retention" }

func migrationRetainedThrough(db *gorm.DB, epoch int64) (int64, error) {
	var retention migrationRetentionRecord
	err := db.First(&retention, "source_epoch = ?", epoch).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	return retention.PrunedThrough, err
}

// prune removes only change payloads at or before the replica's persisted full
// proof. Transaction headers (including the latest allocated position) remain.
// The source watermark commits with deletion; a stale replica must fail with a
// gap instead of silently skipping already reclaimed data. Historical business
// rows and raw webhook payloads are never involved in this maintenance action.
func (c *onlineMigrationCoordinator) prune(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.operational(ctx); err != nil {
		return err
	}
	checkpoint, err := readMigrationCheckpoint(c.replica.db.WithContext(ctx), c.source.control)
	if err != nil || !checkpoint.Verified || checkpoint.VerifiedPosition > checkpoint.Position {
		return fmt.Errorf("%w: retention requires a valid full proof", errOnlineMigrationNotReady)
	}
	return migrationStorageError(ctx, "prune acknowledged change payloads", c.source.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockMigrationControl(tx, c.source.control); err != nil {
			return err
		}
		through, err := migrationRetainedThrough(tx, c.source.control.Epoch)
		if err != nil {
			return err
		}
		if through >= checkpoint.VerifiedPosition {
			return nil
		}
		// Bounded batches avoid a single large DELETE monopolizing the primary's
		// control lock. Only the last fully reclaimed transaction is published.
		var groups []migrationTransactionRecord
		if err := tx.Where("epoch = ? AND position > ? AND position <= ?", c.source.control.Epoch, through, checkpoint.VerifiedPosition).
			Order("position").Limit(128).Find(&groups).Error; err != nil {
			return err
		}
		if len(groups) == 0 {
			return nil
		}
		last := groups[len(groups)-1].Position
		positions := tx.Model(&migrationTransactionRecord{}).Select("position").
			Where("epoch = ? AND position > ? AND position <= ?", c.source.control.Epoch, through, last)
		var ids []int64
		if err := tx.Model(&migrationChangeRecord{}).Where("transaction_position IN (?)", positions).
			Order("id").Limit(256).Pluck("id", &ids).Error; err != nil {
			return err
		}
		if len(ids) > 0 {
			if err := tx.Where("id IN ?", ids).Delete(&migrationChangeRecord{}).Error; err != nil {
				return err
			}
		}
		var firstRemaining int64
		if err := tx.Model(&migrationChangeRecord{}).Where("transaction_position IN (?)", positions).
			Select("COALESCE(MIN(transaction_position), 0)").Scan(&firstRemaining).Error; err != nil {
			return err
		}
		if firstRemaining > 0 {
			last = through
			for _, group := range groups {
				if group.Position < firstRemaining {
					last = group.Position
				}
			}
		}
		return tx.Save(&migrationRetentionRecord{SourceEpoch: c.source.control.Epoch, PrunedThrough: last}).Error
	}))
}
