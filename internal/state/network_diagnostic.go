package state

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// Match the persisted attempt even for historical requests with no provision ID.
// Diagnostic writes deliberately leave the lifecycle version and timestamps alone.
func diagnosticRunner(db *gorm.DB, st RunnerState) *gorm.DB {
	q := db.Model(&runnerRequestRecord{}).Where("id = ? AND status = ? AND sandbox_id = ? AND process_pid = ?", st.ID, StatusRunning, st.SandboxID, st.ProcessPID).
		Where("COALESCE(provision_attempt_id, '') = ?", st.ProvisionAttemptID)
	if st.LastAttemptAt.IsZero() {
		return q.Where("last_attempt_at IS NULL OR last_attempt_at = ?", time.Time{})
	}
	return q.Where("last_attempt_at = ?", st.LastAttemptAt)
}

func (s *DBStore) ClaimNetworkDiagnostic(ctx context.Context, st RunnerState, token string, timeout, cooldown time.Duration) (time.Duration, error) {
	if st.Status != StatusRunning || st.SandboxID == "" || st.ProcessPID == 0 || token == "" || timeout <= 0 || cooldown <= 0 {
		return 0, ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return 0, err
	}
	db = db.WithContext(ctx)
	identity := fmt.Sprintf("%q/%d/%q/%s", st.SandboxID, st.ProcessPID, st.ProvisionAttemptID, st.LastAttemptAt.UTC().Format(time.RFC3339Nano))
	attempt := fmt.Sprintf("%x", sha256.Sum256([]byte(identity)))
	// If the owner dies, reserve enough time for the bounded provider call
	// and its full cooldown. Normal completion uses the actual finish time.
	res := diagnosticRunner(db, st).Where("diagnostic_attempt IS NULL OR diagnostic_attempt <> ? OR diagnostic_next_at IS NULL OR diagnostic_next_at <= ?", attempt, queueTime(db, 0)).
		UpdateColumns(map[string]any{"diagnostic_attempt": attempt, "diagnostic_token": token, "diagnostic_next_at": queueTime(db, timeout+cooldown)})
	if res.Error != nil || res.RowsAffected == 1 {
		return 0, res.Error
	}
	// Return the old API's cooldown hint while a probe is running. Once
	// completed, calculate Retry-After against the same clock as admission.
	diff := "(julianday(diagnostic_next_at) - julianday(?)) * 86400"
	switch db.Dialector.Name() {
	case BackendPostgres:
		diff = "EXTRACT(EPOCH FROM (diagnostic_next_at - ?))"
	case BackendMySQL:
		diff = "TIMESTAMPDIFF(MICROSECOND, ?, diagnostic_next_at) / 1000000.0"
	}
	var row struct {
		Token   string
		Seconds float64
	}
	res = diagnosticRunner(db, st).Where("diagnostic_attempt = ?", attempt).Select("diagnostic_token AS token, "+diff+" AS seconds", queueTime(db, 0)).Scan(&row)
	if res.Error != nil {
		return 0, res.Error
	}
	if res.RowsAffected != 1 {
		return 0, ErrConflict
	}
	if row.Token != "" {
		return cooldown, nil
	}
	return max(time.Millisecond, min(cooldown, time.Duration(row.Seconds*float64(time.Second)))), nil
}

// The conditional update locks the current attempt through the event insert.
// A late probe cannot append an event to a replacement or release its claim.
func (s *DBStore) FinishNetworkDiagnostic(ctx context.Context, st RunnerState, token, message string, cooldown time.Duration) error {
	if token == "" || message == "" || cooldown <= 0 {
		return ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := diagnosticRunner(tx, st).Where("diagnostic_token = ? AND diagnostic_next_at > ?", token, queueTime(tx, 0)).
			UpdateColumns(map[string]any{"diagnostic_token": "", "diagnostic_next_at": queueTime(tx, cooldown)})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrConflict
		}
		return tx.Create(&runnerEventRecord{RequestID: st.ID, EventType: "control_log", Stage: "network_diagnostic", Message: message, CreatedAt: time.Now().UTC()}).Error
	})
}
