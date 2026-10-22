package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/metrics"
	"github.com/qiniu/ci-runner/internal/state"
)

const (
	workflowJobWaitCode         = "github_job_waiting"
	workflowJobStatusCodePrefix = "github_job_status_"
	// Frozen evidence emitted by the pre-fix startup path. Keep this literal
	// independent of current log wording so historical repairs remain valid.
	historicalWorkflowJobWaitingSkip = "runner skipped before sandbox start: workflow job is waiting"
)

func isWorkflowJobWaiting(status string) bool {
	switch status {
	case "waiting", "pending", "requested":
		return true
	}
	return false
}

func (s *Server) ensureWorkflowJobStillQueued(ctx context.Context, req state.RunnerRequest, workerID string) (bool, error) {
	if req.JobID == 0 {
		return true, nil
	}
	before, err := s.store.ReadState(req.ID)
	if err != nil {
		return false, err
	}
	s.store.AppendLog(req.ID, "control.log", []byte("checking workflow job status before sandbox start\n"))
	lookupCtx, cancel := context.WithTimeout(ctx, workflowJobLookupTimeout)
	defer cancel()
	job, lookupErr := s.gh.GetWorkflowJob(lookupCtx, req.RepositoryFullName, req.JobID)

	unlock := s.lockRunner(req.ID)
	defer unlock()
	st, err := s.store.ReadState(req.ID)
	if err != nil {
		return false, err
	}
	// A webhook, retry or stop may have advanced the request during the lookup.
	if st.Status != state.StatusCreating || st.LeaseOwner != workerID || st.SandboxID != "" || st.ProcessPID != 0 || !st.LastAttemptAt.Equal(before.LastAttemptAt) || !st.CreatingAt.Equal(before.CreatingAt) {
		return false, nil
	}
	stale := st.Version != before.Version
	if lookupErr == nil && job.ID != req.JobID {
		lookupErr = fmt.Errorf("workflow job lookup returned a different job")
	}
	if !stale && lookupErr == nil && job.Status == "queued" {
		if st.LastErrorCode == workflowJobWaitCode || strings.HasPrefix(st.LastErrorCode, workflowJobStatusCodePrefix) {
			st.NextRetryAt = time.Time{}
			st.Error, st.FailureStage, st.FailureReason = "", "", ""
			st.LastErrorCode, st.LastErrorMessage, st.LastErrorRetryable = "", "", false
			if err := s.store.WriteState(st); err != nil {
				return false, err
			}
		}
		s.logger.Info("workflow job startup decision", "id", req.ID, "job_status", job.Status, "decision", "start")
		s.store.AppendLog(req.ID, "control.log", []byte("workflow job observed_status=queued decision=start\n"))
		return true, nil
	}
	if !stale && lookupErr == nil && job.Status == "completed" {
		return false, s.completeWithoutSandbox(req.ID, job, "workflow job is completed/"+job.Conclusion)
	}
	// GitHub owns approval/concurrency timeouts. Keep recognized nonterminal
	// states recoverable until GitHub reports queued or completed, at a capped
	// polling interval. in_progress is monitored without creating another Runner;
	// its runner_name identifies the execution that already owns the Job.
	known := stale || (lookupErr == nil && (isWorkflowJobWaiting(job.Status) || job.Status == "in_progress"))
	rateLimited := false
	code, message := workflowJobWaitCode, "workflow job is "+job.Status
	if stale {
		message = "workflow job changed during status lookup; recheck required"
	}
	if !known {
		code, message = workflowJobStatusCodePrefix+"unknown", "unrecognized workflow job status"
		if lookupErr != nil {
			// Do not persist upstream response bodies or credentials.
			code, message = workflowJobStatusCodePrefix+"unavailable", "workflow job status lookup failed"
			errorCode, _ := classifyRetryableError("github_job_status", lookupErr)
			if errorCode == "http_rate_limited" || errorCode == "github_secondary_rate_limit" {
				rateLimited = true
				code, message = workflowJobStatusCodePrefix+"rate_limited", "workflow job status lookup rate limited"
			}
		}
	}
	st.LeaseOwner, st.LeaseExpiresAt = "", time.Time{}
	st.Status = state.StatusQueued
	st.CreatingAt = time.Time{}
	st.Error, st.FailureStage, st.FailureReason = "", "", ""
	st.LastErrorCode, st.LastErrorMessage, st.LastErrorRetryable = code, message, true
	if !known && !rateLimited && st.RetryCount >= s.cfg.RetryMaxAttempts {
		st.Status = state.StatusFailed
		st.Error, st.FailureStage, st.FailureReason = message, "github_job_status", code
		st.NextRetryAt = time.Time{}
	} else if known {
		// Waiting is not a failed creation attempt. Use persisted queue age for
		// capped backoff so it neither consumes nor resets the failure budget,
		// including after a restart or an earlier transient Sandbox failure.
		now := time.Now().UTC()
		delay := s.cfg.RetryBaseDelay
		for delay > 0 && delay < s.cfg.RetryMaxDelay && now.Sub(st.CreatedAt) >= delay {
			delay *= 2
		}
		st.NextRetryAt = now.Add(min(delay, s.cfg.RetryMaxDelay))
	} else {
		// Rate limits defer the queue even after the failure budget is spent,
		// matching applyFailure; saturate the counter while retaining backoff.
		if st.RetryCount < s.cfg.RetryMaxAttempts {
			st.RetryCount++
		}
		st.NextRetryAt = s.nextRetryAt(max(st.RetryCount, 1), time.Now().UTC())
	}
	if err := s.store.WriteState(st); err != nil {
		return false, err
	}
	if st.Status == state.StatusFailed {
		metrics.RecordCreate(st.ProfileName, 0, "failed")
		metrics.RecordWorkflowFailure(st.RepositoryFullName, "unknown", workflowJobName(st, job), st.ProfileName, st.FailureStage, st.FailureReason)
	}
	s.logger.Info("workflow job startup decision", "id", req.ID, "job_status", job.Status, "runner_name", job.RunnerName, "decision", st.Status, "reason", code, "next_retry_at", st.NextRetryAt)
	s.store.AppendLog(req.ID, "control.log", []byte(fmt.Sprintf("workflow job observed_status=%q runner_name=%q decision=%s reason=%s next_check=%s\n", job.Status, job.RunnerName, st.Status, code, st.NextRetryAt.Format(time.RFC3339))))
	s.signalQueue()
	if st.Status == state.StatusFailed {
		s.refreshMetrics()
	} else {
		// Coalesce waiting polls into the existing five-second reconciliation
		// cycle instead of scanning all active requests for every waiting Job.
		s.waitingMetricsDirty.Store(true)
	}
	return false, nil
}

// A fresh queued delivery can shorten a known wait, but must not reset failed,
// terminal or active requests, or override a worker's current lease. Use the
// store's version check rather than taking a runner lock under admissionMu.
func (s *Server) wakeWaitingWorkflowJob(req state.RunnerRequest, fallback state.RunnerState) (state.RunnerState, error) {
	if req.JobID == 0 {
		return fallback, nil
	}
	st, err := s.store.ReadState(req.ID)
	if err != nil {
		return fallback, err
	}
	if st.Status != state.StatusQueued || st.LastErrorCode != workflowJobWaitCode || st.LeaseOwner != "" || st.SandboxID != "" || st.ProcessPID != 0 || st.WorkflowJobID != req.JobID || st.RepositoryFullName != req.RepositoryFullName || st.GitHubInstallationID != req.GitHubInstallationID || st.NextRetryAt.IsZero() {
		return st, nil
	}
	st.NextRetryAt = time.Time{}
	if err := s.store.WriteState(st); err != nil {
		if errors.Is(err, state.ErrConflict) {
			return s.store.ReadState(req.ID)
		}
		return st, err
	}
	s.signalQueue()
	return st, nil
}

// Only an explicit admin retry may repair a historical false completion. The
// exact control-log suffix is evidence from the old startup path, not stdout.
func (s *Server) retryHistoricalWaitingJob(ctx context.Context, id string) (state.RunnerState, error) {
	st, err := s.store.ReadState(id)
	if err != nil {
		return st, err
	}
	if st.Status != state.StatusCompleted || st.WorkflowJobID == 0 || st.SandboxID != "" || st.ProcessPID != 0 || !st.RunningAt.IsZero() || !st.StoppingAt.IsZero() || st.TerminationSource != "" || st.RunnerExitCode != nil || st.GitHubJobStatus != "" || st.GitHubJobConclusion != "" || !st.GitHubJobObservedAt.IsZero() {
		return st, state.ErrRetryNotAllowed
	}
	control, err := s.store.ReadLog(id, "control.log", 16<<10)
	if err != nil || !strings.HasSuffix(strings.TrimSpace(string(control)), historicalWorkflowJobWaitingSkip) {
		return st, state.ErrRetryNotAllowed
	}
	req, err := s.store.ReadRequest(id)
	if err != nil {
		return st, err
	}
	if req.Source != "github_webhook" || !s.cfg.RepositoryAllowed(req.RepositoryFullName) {
		return st, state.ErrRetryNotAllowed
	}
	lookupCtx, cancel := context.WithTimeout(ctx, workflowJobLookupTimeout)
	defer cancel()
	job, err := s.gh.GetWorkflowJob(lookupCtx, req.RepositoryFullName, req.JobID)
	if err != nil || job.ID != req.JobID || job.Status != "queued" || job.RunnerName != "" || job.Conclusion != "" {
		return st, state.ErrRetryNotAllowed
	}
	unlock := s.lockRunner(id)
	defer unlock()
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	profile, err := s.profileForRunnerRequest(req)
	if err != nil || validateRequestedProfile(profile, req.RequestedLabels) != nil {
		return st, state.ErrRetryNotAllowed
	}
	st.Status = state.StatusQueued
	st.CompletedAt, st.CreatingAt, st.FailedAt, st.NextRetryAt = time.Time{}, time.Time{}, time.Time{}, time.Time{}
	st.LeaseOwner, st.LeaseExpiresAt = "", time.Time{}
	st.AssignedJobID, st.AssignedJobName = 0, ""
	st.Error, st.FailureStage, st.FailureReason = "", "", ""
	st.LastErrorCode, st.LastErrorMessage, st.LastErrorRetryable = "", "", false
	st.RetryCount = 0
	clearRunnerEnvironmentSnapshot(&st)
	clearSandboxServiceSnapshot(&st)
	// WriteState's version check rejects any change during the remote lookup.
	if err := s.store.WriteState(st); err != nil {
		return st, state.ErrRetryNotAllowed
	}
	s.signalQueue()
	return st, nil
}
