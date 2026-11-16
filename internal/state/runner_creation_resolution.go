package state

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const (
	UnknownCreationReviewDelay  = 15 * time.Minute
	UnknownCreationResolvedCode = "sandbox_creation_resolved_absent"
)

// UnknownSandboxCreation identifies outstanding dispatches, not evidence that
// the remote resource exists or that an empty provider list proves its absence.
func UnknownSandboxCreation(st RunnerState) bool {
	return (st.Status == StatusCreating || st.Status == StatusStopping) &&
		st.SandboxCreateDispatched && st.SandboxID == "" && st.ProcessPID == 0 && st.ProvisionAttemptID != ""
}

// ResolveUnknownSandboxCreation is an explicit, audited operator resolution.
// Its caller must verify the original Job is completed, GitHub registration is
// absent, and an exact Sandbox search is empty. The operator must separately
// attest that no create is still in flight; a list miss or elapsed TTL alone is
// never sufficient. No cleanup receipt is fabricated for a Sandbox DELETE.
func (s *DBStore) ResolveUnknownSandboxCreation(ctx context.Context, st RunnerState, token string, audit AuditEvent) error {
	if !UnknownSandboxCreation(st) || st.Status != StatusStopping || token == "" ||
		(st.JITRegistrationState != "" && st.JITRegistrationState != JITUnsent && st.JITRegistrationState != JITRejected) ||
		st.WorkflowJobID <= 0 || !st.RunningAt.IsZero() || st.CleanupGitHubRunnerID != 0 || st.GitHubJobStatus != "completed" ||
		audit.Actor == "" || audit.Action != "runner.resolve_unknown_creation" || audit.ResourceID != st.ID || audit.ResourceType != "runner_request" {
		return ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&runnerRequestRecord{}).
			Where("id = ? AND version = ? AND status = ? AND provision_attempt_id = ?", st.ID, st.Version, StatusStopping, st.ProvisionAttemptID).
			Where("watch_token = ? AND watch_expires_at > ?", token, queueTime(tx, 0)).
			Where("stopping_at <= ? AND sandbox_create_dispatched = ?", queueTime(tx, -UnknownCreationReviewDelay), true).
			Where("(sandbox_id IS NULL OR sandbox_id = '') AND (process_pid IS NULL OR process_pid = 0)").
			Where("running_at IS NULL AND (cleanup_github_runner_id IS NULL OR cleanup_github_runner_id = 0)").
			Where("jit_registration_state IS NULL OR jit_registration_state IN ?", []string{"", JITUnsent, JITRejected}).
			UpdateColumns(map[string]any{
				"status": StatusCompleted, "version": gorm.Expr("version + 1"),
				"completed_at": queueTime(tx, 0), "updated_at": queueTime(tx, 0),
				"sandbox_create_dispatched": false, "cleanup_github_done": true,
				"manual_stop_requested": true, "next_retry_at": nil,
				"watch_token": nil, "watch_expires_at": nil, "lease_owner": "", "lease_expires_at": nil,
				"last_error_code":        UnknownCreationResolvedCode,
				"last_error_message":     "Administrator verified no Sandbox or pending creation; see resolution audit",
				"last_error_retryable":   false,
				"github_job_name":        st.GitHubJobName,
				"github_job_status":      st.GitHubJobStatus,
				"github_job_conclusion":  st.GitHubJobConclusion,
				"github_job_runner_name": st.GitHubJobRunnerName,
				"github_job_observed_at": zeroTimeToPointer(st.GitHubJobObservedAt),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrConflict
		}
		if _, err := appendAuditEvent(tx, audit); err != nil {
			return fmt.Errorf("%w: %w", ErrAuditEventPersistence, err)
		}
		return nil
	})
}
