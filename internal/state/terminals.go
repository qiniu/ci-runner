package state

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// Longer than the remote helper's 30-second watchdog, including an operation
// deadline. Expiration records uncertainty; it never reconstructs a PID handle.
const terminalLeaseMillis = int64(45000)

func (s *DBStore) CreateTerminalSession(ctx context.Context, row TerminalSession) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	if row.ID == "" || row.Owner == "" || row.AccountID <= 0 || row.Binding == "" {
		return errors.New("invalid terminal ownership")
	}
	row.Status, row.Cleanup = "creating", "unknown"
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		return tx.Model(&TerminalSession{}).Where("id = ?", row.ID).UpdateColumn("lease_until", gorm.Expr("(?) + ?", accessClockMillis(tx), terminalLeaseMillis)).Error
	})
}

func (s *DBStore) TouchTerminalSession(ctx context.Context, id, owner string, activate bool) (bool, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return false, err
	}
	status := "active"
	if activate {
		status = "creating"
	}
	result := db.WithContext(ctx).Model(&TerminalSession{}).Where("id = ? AND owner = ? AND status = ? AND lease_until > (?)", id, owner, status, accessClockMillis(db)).Updates(map[string]any{
		"status": "active", "lease_until": gorm.Expr("(?) + ?", accessClockMillis(db), terminalLeaseMillis), "updated_at": time.Now().UTC(),
	})
	return result.RowsAffected == 1, result.Error
}

func (s *DBStore) FinishTerminalSession(ctx context.Context, id, owner, cleanup, reason string) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Model(&TerminalSession{}).Where("id = ? AND owner = ? AND status IN ? AND lease_until > (?)", id, owner, []string{"creating", "active"}, accessClockMillis(db)).Updates(map[string]any{
		"status": "closed", "cleanup": cleanup, "close_reason": reason, "lease_until": 0, "updated_at": time.Now().UTC(),
	}).Error
}

func (s *DBStore) ExpireTerminalSessions(ctx context.Context) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Model(&TerminalSession{}).Where("status IN ? AND lease_until <= (?)", []string{"creating", "active"}, accessClockMillis(db)).Updates(map[string]any{
		"status": "closed", "cleanup": "unknown", "close_reason": "connection_owner_lost", "lease_until": 0, "updated_at": time.Now().UTC(),
	}).Error
}

func (s *DBStore) GetTerminalSession(ctx context.Context, id string) (TerminalSession, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return TerminalSession{}, err
	}
	var row TerminalSession
	err = db.WithContext(ctx).Where("id = ?", id).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrNotFound
	}
	return row, err
}
