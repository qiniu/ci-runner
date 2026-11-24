package state

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// AppendRunnerCacheEvent retains credential-free evidence only while the exact
// attempt is active. The row lock prevents a retry/stop racing the event insert;
// no lifecycle version, lease, or timestamps are changed by this observation.
func (s *DBStore) AppendRunnerCacheEvent(ctx context.Context, id, attempt, message string) error {
	if attempt == "" || message == "" || len(message) > 512 {
		return ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		live := tx.Model(&runnerRequestRecord{}).
			Where("id = ? AND provision_attempt_id = ? AND status IN ? AND manual_stop_requested = ?", id, attempt, []string{StatusCreating, StatusRunning}, false)
		if err := live.Session(&gorm.Session{}).UpdateColumn("provision_attempt_id", attempt).Error; err != nil {
			return err
		}
		var count int64
		if err := live.Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return ErrConflict
		}
		return tx.Create(&runnerEventRecord{RequestID: id, EventType: "control_log", Stage: "cache_sts", Message: message, CreatedAt: time.Now().UTC()}).Error
	})
}
