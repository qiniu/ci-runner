package state

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
)

// TerminalSession is the durable authority for one user's shell. Output belongs
// to the sandbox PTY; runnerd keeps no user/session registry or replay buffer.
type TerminalSession struct {
	ID                     string `gorm:"primaryKey;size:64"`
	AccountID              int64  `gorm:"not null;index"`
	Kind                   string `gorm:"size:16;not null"`
	ResourceID             string `gorm:"size:255;not null"`
	BindingKey             string `gorm:"type:text;not null"`
	Revision               int64
	SandboxID              string `gorm:"size:255;not null"`
	SandboxAPIURL          string `gorm:"type:text"`
	SandboxAPIKeyEncrypted string `gorm:"type:text" json:"-"`
	PID                    uint32 `gorm:"column:pid"`
	StreamID               string `gorm:"size:64"`
	Baseline               uint64
	Status                 string    `gorm:"size:16;not null;index"`
	IdleDeadline           time.Time `gorm:"index"`
	ExpiresAt              *time.Time
	WorkToken              string `gorm:"size:64"`
	WorkExpiresAt          *time.Time
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

func (TerminalSession) TableName() string { return "terminal_sessions" }

type TerminalStore interface {
	CreateTerminal(context.Context, TerminalSession) error
	GetTerminal(context.Context, string) (TerminalSession, error)
	ActivateTerminal(context.Context, string, uint32, string, uint64) error
	RenewTerminal(context.Context, string) error
	CloseTerminal(context.Context, string) error
	ListTerminalCleanupCandidates(context.Context, string, int) ([]TerminalSession, error)
	ClaimTerminalCleanup(context.Context, string, string) (bool, error)
	FinishTerminalCleanup(context.Context, string, string, bool) error
}

func (s *DBStore) CreateTerminal(ctx context.Context, t TerminalSession) error {
	if t.ID == "" || t.AccountID <= 0 || t.SandboxID == "" || t.BindingKey == "" || (t.Kind != "runner" && t.Kind != "deployment") {
		return ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	if t.ExpiresAt != nil {
		expires := t.ExpiresAt.UTC()
		t.ExpiresAt = &expires
	}
	t.IdleDeadline = time.Now().UTC()
	t.Status = "opening"
	t.PID = 0
	t.StreamID = ""
	t.WorkToken = ""
	t.WorkExpiresAt = nil
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&t).Error; err != nil {
			return err
		}
		return tx.Model(&TerminalSession{}).Where("id = ?", t.ID).UpdateColumn("idle_deadline", queueTime(tx, 30*time.Second)).Error
	})
}

func (s *DBStore) GetTerminal(ctx context.Context, id string) (TerminalSession, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return TerminalSession{}, err
	}
	var t TerminalSession
	err = db.WithContext(ctx).First(&t, "id = ?", id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrNotFound
	}
	return t, err
}

// Positive creation evidence is retained even if cleanup already won. Activation
// is conditional so a delayed response cannot reopen an expired/closed session.
func (s *DBStore) ActivateTerminal(ctx context.Context, id string, pid uint32, stream string, baseline uint64) error {
	if pid == 0 || stream == "" {
		return ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	res := db.WithContext(ctx).Model(&TerminalSession{}).Where("id = ? AND status IN ? AND pid = 0", id, []string{"opening", "closing"}).UpdateColumns(map[string]any{"pid": pid, "stream_id": stream, "baseline": baseline})
	if res.Error != nil {
		return res.Error
	}
	res = db.WithContext(ctx).Model(&TerminalSession{}).Where("id = ? AND status = ? AND pid = ? AND stream_id = ? AND idle_deadline > ?", id, "opening", pid, stream, queueTime(db, 0)).UpdateColumns(map[string]any{"status": "open", "idle_deadline": queueTime(db, 30*time.Second)})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

func (s *DBStore) RenewTerminal(ctx context.Context, id string) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	q := db.WithContext(ctx).Model(&TerminalSession{}).Where("id = ? AND status = ? AND idle_deadline > ?", id, "open", queueTime(db, 0)).Where("expires_at IS NULL OR expires_at > ?", queueTime(db, 0))
	greatest := "GREATEST"
	if db.Dialector.Name() == BackendSQLite {
		greatest = "MAX"
	}
	res := q.UpdateColumn("idle_deadline", gorm.Expr(greatest+"(idle_deadline, ?)", queueTime(db, 30*time.Second)))
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		var n int64
		if err := q.Count(&n).Error; err != nil {
			return err
		}
		if n == 0 {
			return ErrConflict
		}
	}
	return nil
}

func (s *DBStore) CloseTerminal(ctx context.Context, id string) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Model(&TerminalSession{}).Where("id = ? AND status IN ?", id, []string{"opening", "open"}).UpdateColumn("status", "closing").Error
}

func (s *DBStore) ListTerminalCleanupCandidates(ctx context.Context, after string, limit int) ([]TerminalSession, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var rows []TerminalSession
	err = db.WithContext(ctx).Where("status <> ? AND id > ?", "closed", after).
		Where("status = ? OR idle_deadline <= ? OR expires_at <= ?", "closing", queueTime(db, 0), queueTime(db, 0)).
		Where("work_expires_at IS NULL OR work_expires_at <= ?", queueTime(db, 0)).Order("id").Limit(max(1, min(limit, 100))).Find(&rows).Error
	return rows, err
}

func (s *DBStore) ClaimTerminalCleanup(ctx context.Context, id, token string) (bool, error) {
	if token == "" {
		return false, ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return false, err
	}
	res := db.WithContext(ctx).Model(&TerminalSession{}).Where("id = ? AND status <> ?", id, "closed").Where("status = ? OR idle_deadline <= ? OR expires_at <= ?", "closing", queueTime(db, 0), queueTime(db, 0)).Where("work_expires_at IS NULL OR work_expires_at <= ?", queueTime(db, 0)).UpdateColumns(map[string]any{"status": "closing", "work_token": token, "work_expires_at": queueTime(db, 20*time.Second)})
	return res.RowsAffected == 1, res.Error
}

func (s *DBStore) FinishTerminalCleanup(ctx context.Context, id, token string, closed bool) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	values := map[string]any{"work_token": "", "work_expires_at": queueTime(db, 5*time.Second)}
	if closed {
		values["status"] = "closed"
		values["sandbox_api_key_encrypted"] = ""
	}
	return db.WithContext(ctx).Model(&TerminalSession{}).Where("id = ? AND work_token = ? AND status = ? AND work_expires_at > ?", id, token, "closing", queueTime(db, 0)).UpdateColumns(values).Error
}
