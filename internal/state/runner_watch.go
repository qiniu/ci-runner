package state

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// ClaimRunnerWatch invalidates earlier observations once a creating or running attempt
// has no live watcher. A heartbeat only extends a live claim and never changes
// the business version or updated_at.
func (s *DBStore) ClaimRunnerWatch(ctx context.Context, st RunnerState, token string, ttl time.Duration) (bool, error) {
	if token == "" || ttl <= 0 || (st.Status != StatusCreating && st.Status != StatusRunning) {
		return false, ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return false, err
	}
	result := db.WithContext(ctx).Model(&runnerRequestRecord{}).
		Where("id = ? AND version = ? AND status = ?", st.ID, st.Version, st.Status).
		Where("watch_expires_at IS NULL OR watch_expires_at <= ?", queueTime(db, 0)).
		UpdateColumns(map[string]any{"watch_token": token, "watch_expires_at": queueTime(db, ttl), "version": gorm.Expr("version + 1")})
	return result.RowsAffected == 1, result.Error
}

func (s *DBStore) RenewRunnerWatch(ctx context.Context, id, token string, ttl time.Duration) (bool, error) {
	if token == "" || ttl <= 0 {
		return false, ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return false, err
	}
	live := db.WithContext(ctx).Model(&runnerRequestRecord{}).
		Where("id = ? AND status IN ? AND watch_token = ? AND watch_expires_at > ?", id, []string{StatusCreating, StatusRunning, StatusStopping}, token, queueTime(db, 0))
	result := live.Session(&gorm.Session{}).UpdateColumn("watch_expires_at", queueTime(db, ttl))
	if result.Error != nil || result.RowsAffected != 0 || db.Dialector.Name() != "mysql" {
		return result.RowsAffected == 1, result.Error
	}
	// MySQL normally reports changed rows. Two renewals can round to the
	// same stored millisecond, leaving a matching, live lease unchanged.
	var count int64
	err = live.Count(&count).Error
	return count == 1, err
}

type RunnerWatchCandidate struct{ ID, Status string }

func (s *DBStore) ListExpiredRunnerWatches(ctx context.Context, limit int) ([]RunnerWatchCandidate, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var rows []RunnerWatchCandidate
	err = db.WithContext(ctx).Model(&runnerRequestRecord{}).Where("status IN ? OR (status = ? AND (next_retry_at IS NULL OR next_retry_at <= ?))", []string{StatusCreating, StatusRunning}, StatusStopping, queueTime(db, 0)).
		Where("watch_expires_at IS NULL OR watch_expires_at <= ?", queueTime(db, 0)).
		Order("watch_expires_at, id").Limit(limit).Select("id", "status").Scan(&rows).Error
	return rows, err
}

func (s *DBStore) AdoptRunningWatch(st RunnerState, token string, ttl time.Duration) error {
	if st.Status != StatusRunning || token == "" || ttl <= 0 {
		return ErrConflict
	}
	return s.writeState(st, stateWriteLease{currentToken: token, newToken: token, ttl: ttl})
}

func (s *DBStore) WriteWatchedState(st RunnerState, token string) error {
	if token == "" {
		return ErrConflict
	}
	return s.writeState(st, stateWriteLease{currentToken: token})
}

func (s *DBStore) BeginRunnerCreation(st RunnerState, ttl time.Duration) error {
	if st.Status != StatusCreating || st.LeaseOwner == "" || st.ProvisionAttemptID == "" || st.WatchToken == "" || ttl <= 0 {
		return ErrConflict
	}
	return s.writeState(st, stateWriteLease{queueOwner: st.LeaseOwner, newToken: st.WatchToken, ttl: ttl})
}

// Commit intent before crossing the provider boundary. A metadata miss on
// recovery cannot distinguish a lost response from a delayed create request.
func (s *DBStore) DispatchSandboxCreate(ctx context.Context, id, token, attempt string) error {
	return s.dispatchCreationOperation(ctx, id, token, attempt, "sandbox_create_dispatched")
}

func (s *DBStore) DispatchJITRegistration(ctx context.Context, id, token, attempt string) error {
	return s.dispatchCreationOperation(ctx, id, token, attempt, "jit_registration_state")
}

// column is one of the two server-owned, one-shot create intents.
func (s *DBStore) dispatchCreationOperation(ctx context.Context, id, token, attempt, column string) error {
	if token == "" || attempt == "" {
		return ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	query := db.WithContext(ctx).Model(&runnerRequestRecord{}).
		Where("id = ? AND status = ? AND provision_attempt_id = ? AND watch_token = ? AND watch_expires_at > ?", id, StatusCreating, attempt, token, queueTime(db, 0))
	var dispatched any = true
	if column == "jit_registration_state" {
		query = query.Where("jit_registration_state = ?", JITUnsent)
		dispatched = JITDispatched
	} else {
		query = query.Where("sandbox_create_dispatched IS NULL OR sandbox_create_dispatched = ?", false)
	}
	res := query.UpdateColumns(map[string]any{column: dispatched, "version": gorm.Expr("version + 1")})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// RecordSandboxCreateResult accepts provider evidence only for the dispatched
// attempt. A positive ID survives owner loss. An empty ID denotes a definitive
// rejection and unblocks a stop that was waiting for the create response.
func (s *DBStore) RecordSandboxCreateResult(ctx context.Context, attempt RunnerState, sandboxID string) error {
	if attempt.ProvisionAttemptID == "" {
		return ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	q := db.WithContext(ctx).Model(&runnerRequestRecord{}).
		Where("id = ? AND provision_attempt_id = ? AND sandbox_create_dispatched = ?", attempt.ID, attempt.ProvisionAttemptID, true).
		Where("sandbox_id = '' OR sandbox_id IS NULL").
		Where("status IN ?", []string{StatusCreating, StatusStopping})
	updates := map[string]any{"version": gorm.Expr("version + 1")}
	if sandboxID == "" {
		q = q.Where("status = ? OR (watch_token = ? AND watch_expires_at > ?)", StatusStopping, attempt.WatchToken, queueTime(db, 0))
		updates["sandbox_create_dispatched"] = false
	} else {
		updates["sandbox_id"] = sandboxID
	}
	res := q.UpdateColumns(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrConflict
	}
	return nil
}

// The POST reply can arrive after its owner lost the lease. A confirmed
// numeric ID belongs to its attempt and pins all future deletion to that ID.
// Zero records a definitive rejection, never a list miss or a transport error.
func (s *DBStore) RecordJITRegistrationResult(ctx context.Context, attempt RunnerState, runnerID int64) error {
	if attempt.ProvisionAttemptID == "" || runnerID < 0 {
		return ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	query := db.WithContext(ctx).Model(&runnerRequestRecord{}).
		Where("id = ? AND provision_attempt_id = ? AND jit_registration_state = ?", attempt.ID, attempt.ProvisionAttemptID, JITDispatched).
		Where("status IN ?", []string{StatusCreating, StatusStopping}).
		Where("cleanup_github_runner_id IS NULL OR cleanup_github_runner_id = 0")
	updates := map[string]any{"version": gorm.Expr("version + 1")}
	if runnerID == 0 {
		updates["jit_registration_state"] = JITRejected
		updates["cleanup_github_done"] = true
	} else {
		updates["cleanup_github_runner_id"] = runnerID
	}
	result := query.UpdateColumns(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

// BeginRunnerCleanup atomically persists the first stop intent and replaces the
// active lifecycle watch. Recovery competes only for an expired stopping lease.
func (s *DBStore) BeginRunnerCleanup(ctx context.Context, st RunnerState, observedWatch, token string, ttl time.Duration) error {
	if st.Status != StatusStopping || token == "" || ttl <= 0 {
		return ErrConflict
	}
	return s.writeState(st, stateWriteLease{ctx: ctx, currentToken: observedWatch, newToken: token, ttl: ttl, beginCleanup: true})
}

func (s *DBStore) ReleaseRunnerCleanup(ctx context.Context, id, token string) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Model(&runnerRequestRecord{}).Where("id = ? AND status = ? AND watch_token = ?", id, StatusStopping, token).
		UpdateColumns(map[string]any{"watch_token": nil, "watch_expires_at": nil}).Error
}

// Checkpoints do not bump the business version. Concurrent exit/job evidence
// writes never overwrite them. A fresh attempt clears both receipts.
func (s *DBStore) RecordRunnerCleanup(ctx context.Context, id, token string, step RunnerCleanupStep) error {
	if token == "" || (step != CleanupSandbox && step != CleanupGitHub) {
		return ErrConflict
	}
	return s.checkpointRunnerCleanup(ctx, id, token, map[string]any{string(step): true}, false)
}

// Bind the numeric GitHub identity before DELETE. A retry must not look up
// another registration that has since acquired the same name.
func (s *DBStore) BindCleanupGitHubRunner(ctx context.Context, id, token string, runnerID int64) error {
	if token == "" || runnerID <= 0 {
		return ErrConflict
	}
	return s.checkpointRunnerCleanup(ctx, id, token, map[string]any{"cleanup_github_runner_id": runnerID}, true)
}

func (s *DBStore) checkpointRunnerCleanup(ctx context.Context, id, token string, updates map[string]any, bindIdentity bool) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	query := db.WithContext(ctx).Model(&runnerRequestRecord{}).
		Where("id = ? AND status = ? AND watch_token = ? AND watch_expires_at > ?", id, StatusStopping, token, queueTime(db, 0))
	if bindIdentity {
		query = query.Where("cleanup_github_runner_id IS NULL OR cleanup_github_runner_id = 0")
	}
	result := query.UpdateColumns(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}

func (s *DBStore) WriteCleanupState(st RunnerState, token string) error {
	if token == "" {
		return ErrConflict
	}
	return s.writeState(st, stateWriteLease{currentToken: token, cleanupWrite: true})
}

// RecordLateRunnerExit fills missing evidence for the exact stopping or terminal
// attempt. A concurrent cleanup may advance the business version between the
// observer's read and this write. Advance it again to fence stale full writes.
func (s *DBStore) RecordLateRunnerExit(ctx context.Context, observed RunnerState) (bool, error) {
	if observed.SandboxID == "" || observed.ProcessPID == 0 {
		return false, nil
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return false, err
	}
	target := db.WithContext(ctx).Model(&runnerRequestRecord{}).
		Where("id = ? AND sandbox_id = ? AND process_pid = ? AND status IN ?", observed.ID, observed.SandboxID, observed.ProcessPID, []string{StatusStopping, StatusCompleted, StatusFailed})
	if observed.ProvisionAttemptID != "" {
		target = target.Where("provision_attempt_id = ?", observed.ProvisionAttemptID)
	}
	effective := strings.TrimSpace(observed.EffectiveRunnerVersion)
	updates := map[string]any{"version": gorm.Expr("version + 1"), "updated_at": time.Now().UTC()}
	if observed.RunnerExitCode != nil {
		updates["runner_exit_code"] = *observed.RunnerExitCode
		if effective != "" {
			updates["effective_runner_version"] = gorm.Expr("COALESCE(NULLIF(effective_runner_version, ''), ?)", effective)
		}
		res := target.Session(&gorm.Session{}).Where("runner_exit_code IS NULL").UpdateColumns(updates)
		if res.Error != nil || res.RowsAffected != 0 {
			return res.RowsAffected == 1, res.Error
		}
		delete(updates, "runner_exit_code")
	}
	if effective == "" {
		return false, nil
	}
	updates["effective_runner_version"] = effective
	err = target.Where("effective_runner_version IS NULL OR effective_runner_version = ''").UpdateColumns(updates).Error
	return false, err
}
