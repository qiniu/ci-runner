package state

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// ClaimRunningWatch invalidates earlier observations once a running attempt
// has no live watcher. A heartbeat only extends a live claim and never changes
// the business version or updated_at.
func (s *DBStore) ClaimRunningWatch(ctx context.Context, st RunnerState, token string, ttl time.Duration) (bool, error) {
	if token == "" || ttl <= 0 {
		return false, ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return false, err
	}
	result := db.WithContext(ctx).Model(&runnerRequestRecord{}).
		Where("id = ? AND version = ? AND status = ?", st.ID, st.Version, StatusRunning).
		Where("watch_expires_at IS NULL OR watch_expires_at <= ?", queueTime(db, 0)).
		UpdateColumns(map[string]any{"watch_token": token, "watch_expires_at": queueTime(db, ttl), "version": gorm.Expr("version + 1")})
	return result.RowsAffected == 1, result.Error
}

func (s *DBStore) RenewRunningWatch(ctx context.Context, id, token string, ttl time.Duration) (bool, error) {
	if token == "" || ttl <= 0 {
		return false, ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return false, err
	}
	result := db.WithContext(ctx).Model(&runnerRequestRecord{}).
		Where("id = ? AND status = ? AND watch_token = ? AND watch_expires_at > ?", id, StatusRunning, token, queueTime(db, 0)).
		UpdateColumn("watch_expires_at", queueTime(db, ttl))
	return result.RowsAffected == 1, result.Error
}

func (s *DBStore) ListExpiredRunningWatches(ctx context.Context, limit int) ([]string, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var ids []string
	err = db.WithContext(ctx).Model(&runnerRequestRecord{}).Where("status = ?", StatusRunning).
		Where("watch_expires_at IS NULL OR watch_expires_at <= ?", queueTime(db, 0)).
		Order("watch_expires_at, id").Limit(limit).Pluck("id", &ids).Error
	return ids, err
}

func (s *DBStore) AdoptRunningWatch(st RunnerState, token string, ttl time.Duration) error {
	if st.Status != StatusRunning || token == "" || ttl <= 0 {
		return ErrConflict
	}
	return s.writeState(st, "", token, ttl)
}

func (s *DBStore) WriteWatchedState(st RunnerState, token string) error {
	if token == "" {
		return ErrConflict
	}
	return s.writeState(st, "", token, 0)
}
