package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/metrics"
	"github.com/qiniu/ci-runner/internal/state"
)

const workflowJobLookupTimeout = 5 * time.Second

func (s *Server) startBackgroundLoops() {
	s.startOnce.Do(func() {
		s.logger.Info("starting background loops", "worker_id", s.workerID, "max_concurrent_runners", s.cfg.MaxConcurrentRunners)
		s.loopWG.Add(3)
		go s.workerLoop(s.loopCtx)
		go s.sweeperLoop(s.loopCtx)
		go s.reconcilerLoop(s.loopCtx)
	})
}

func (s *Server) workerLoop(ctx context.Context) {
	defer s.loopWG.Done()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.queueNotify:
		case <-ticker.C:
		}
		s.processQueuedRequests(ctx)
	}
}

func (s *Server) sweeperLoop(ctx context.Context) {
	defer s.loopWG.Done()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.sweepOnce(ctx)
		}
	}
}

func (s *Server) reconcilerLoop(ctx context.Context) {
	defer s.loopWG.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcileOnce(ctx)
		}
	}
}

func (s *Server) processQueuedRequests(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case s.slots <- struct{}{}:
		default:
			return
		}
		req, _, claimed, err := s.store.ClaimNextRunnable(s.workerID, time.Now().UTC(), s.cfg.WorkerLeaseTTL)
		if err != nil {
			<-s.slots
			s.logger.Error("claim queued runner", "error", err)
			return
		}
		if !claimed {
			<-s.slots
			return
		}
		s.logger.Info("claimed queued runner request", "id", req.ID, "worker_id", s.workerID, "repository", req.RepositoryFullName, "profile", req.ProfileName)
		go func(id string) {
			defer func() { <-s.slots }()
			s.startRunner(ctx, id, s.workerID)
		}(req.ID)
	}
}

func (s *Server) refreshMetrics() {
	if s.recoveryMetricsDeferred.Load() > 0 {
		return
	}
	profiles, err := s.store.ListProfiles()
	if err != nil {
		s.logger.Error("refresh metrics list profiles", "error", err)
		return
	}
	states, err := s.store.ListActiveStates()
	if err != nil {
		s.logger.Error("refresh metrics list states", "error", err)
		return
	}
	metrics.Refresh(profiles, states)
}

func (s *Server) sweepOnce(ctx context.Context) {
	states, err := s.store.ListActiveStates()
	if err != nil {
		s.logger.Error("list states for sweeper", "error", err)
		return
	}
	now := time.Now().UTC()
	stoppedRunning := make(map[string]struct{})
	for _, st := range states {
		switch st.Status {
		case state.StatusCreating:
			if !st.CreatingAt.IsZero() && now.Sub(st.CreatingAt) > s.cfg.SandboxCreateTimeout {
				s.logger.Info("sweeper detected create timeout", "id", st.ID, "creating_at", st.CreatingAt)
				s.retryOrFail(st, "sweeper_create_timeout", fmt.Errorf("runner create timed out"))
			}
		case state.StatusRunning:
			if !st.RunningAt.IsZero() && now.Sub(st.RunningAt) > s.cfg.SandboxTimeout {
				s.logger.Info("sweeper stopping timed out runner", "id", st.ID, "sandbox_id", st.SandboxID, "running_at", st.RunningAt)
				s.failAndStopRunner(ctx, st.ID, "sandbox_timeout", "timeout", "runner exceeded sandbox timeout")
				stoppedRunning[st.ID] = struct{}{}
				continue
			}
		case state.StatusStopping:
			if !st.NextRetryAt.IsZero() && now.Before(st.NextRetryAt) {
				continue
			}
			if !st.StoppingAt.IsZero() && now.Sub(st.StoppingAt) > s.cfg.SandboxStopTimeout {
				s.logger.Info("sweeper retrying timed out stop", "id", st.ID, "sandbox_id", st.SandboxID, "stopping_at", st.StoppingAt)
				s.stopIfExists(ctx, st.ID, github.WorkflowJob{}, state.TerminationSourceRecoveryCleanup)
			}
		}
	}
	for _, st := range states {
		if st.Status != state.StatusRunning || !s.shouldStopIdleRunner(st, now) {
			continue
		}
		if _, ok := stoppedRunning[st.ID]; ok {
			continue
		}
		queued, err := s.originalWorkflowJobQueued(ctx, st)
		if err != nil {
			s.logger.Warn("sweeper skipped idle runner stop because workflow job status could not be verified", "id", st.ID, "workflow_job_id", st.WorkflowJobID, "error", err)
			s.store.AppendLog(st.ID, "control.log", []byte("sweeper skipped idle stop because workflow job status could not be verified: "+err.Error()+"\n"))
			continue
		}
		if queued {
			s.logger.Info("sweeper keeping idle-timed-out runner because workflow job is still queued", "id", st.ID, "workflow_job_id", st.WorkflowJobID)
			s.store.AppendLog(st.ID, "control.log", []byte("sweeper kept idle runner because workflow job is still queued\n"))
			continue
		}
		busy, err := s.githubRunnerBusy(ctx, st)
		if err != nil {
			s.logger.Warn("sweeper skipped idle runner stop because github runner status could not be verified", "id", st.ID, "runner_name", st.RunnerName, "error", err)
			s.store.AppendLog(st.ID, "control.log", []byte("sweeper skipped idle stop because github runner status could not be verified: "+err.Error()+"\n"))
		} else if busy {
			s.logger.Info("sweeper keeping idle-timed-out runner because github reports it is busy", "id", st.ID, "runner_name", st.RunnerName)
			s.store.AppendLog(st.ID, "control.log", []byte("sweeper kept runner because github reports it is running a job\n"))
			s.markRunnerJobStarted(st.ID)
		} else {
			s.logger.Info("sweeper stopping idle runner", "id", st.ID, "sandbox_id", st.SandboxID, "running_at", st.RunningAt, "idle_timeout", s.cfg.RunnerIdleTimeout)
			s.store.AppendLog(st.ID, "control.log", []byte("sweeper stopping idle runner that never accepted a job\n"))
			s.stopIfExists(ctx, st.ID, github.WorkflowJob{}, state.TerminationSourceIdleCleanup)
		}
	}
}

func (s *Server) shouldStopIdleRunner(st state.RunnerState, now time.Time) bool {
	if s.cfg.RunnerIdleTimeout <= 0 || st.RunningAt.IsZero() {
		return false
	}
	if st.AssignedJobID != 0 || st.AssignedJobName == runnerJobStartedMarker {
		return false
	}
	return now.Sub(st.RunningAt) > s.cfg.RunnerIdleTimeout
}

func (s *Server) githubRunnerBusy(ctx context.Context, st state.RunnerState) (bool, error) {
	runnerName := strings.TrimSpace(st.RunnerName)
	if runnerName == "" || strings.TrimSpace(st.RepositoryFullName) == "" {
		return false, nil
	}
	lookupCtx, cancel := context.WithTimeout(ctx, workflowJobLookupTimeout)
	defer cancel()
	runners, err := s.gh.ListRunners(lookupCtx, st.RepositoryFullName, st.RunnerGroup)
	if err != nil {
		return false, err
	}
	for _, runner := range runners {
		if runner.Name == runnerName {
			return runner.Busy, nil
		}
	}
	return false, nil
}

func (s *Server) reconcileOnce(ctx context.Context) {
	s.processQueuedRequests(ctx)
	states, err := s.store.ListActiveStates()
	if err != nil {
		s.logger.Error("list states for reconciler", "error", err)
		return
	}
	for _, st := range states {
		switch st.Status {
		case state.StatusQueued:
			s.signalQueue()
		case state.StatusStopping:
			if !st.NextRetryAt.IsZero() && time.Now().UTC().Before(st.NextRetryAt) {
				continue
			}
			if !st.StoppingAt.IsZero() && time.Since(st.StoppingAt) > s.cfg.SandboxStopTimeout {
				s.logger.Info("reconciler retrying timed out stop", "id", st.ID, "sandbox_id", st.SandboxID, "stopping_at", st.StoppingAt)
				s.stopIfExists(ctx, st.ID, github.WorkflowJob{}, state.TerminationSourceRecoveryCleanup)
			}
		}
	}
	s.reconcileMismatchedCompletedJobs(ctx)
	s.reconcileCompletedWorkflowJobs(ctx)
}

func (s *Server) reconcileMismatchedCompletedJobs(ctx context.Context) {
	states, err := s.store.ListMismatchedCompletedStates(50)
	if err != nil {
		s.logger.Error("list mismatched completed states for reconciler", "error", err)
		return
	}
	for _, st := range states {
		unlock := s.lockRunner(st.ID)
		current, readErr := s.store.ReadState(st.ID)
		if readErr != nil {
			unlock()
			s.logger.Error("read mismatched completed state", "id", st.ID, "error", readErr)
			continue
		}
		if current.Status != state.StatusCompleted || current.WorkflowJobID == 0 || current.AssignedJobID == 0 || current.WorkflowJobID == current.AssignedJobID {
			unlock()
			continue
		}
		observed := github.WorkflowJob{ID: current.AssignedJobID, Name: current.AssignedJobName}
		unlock()
		queued, queueErr := s.originalWorkflowJobQueued(ctx, current)
		if queueErr != nil {
			s.logger.Warn("original workflow job status check failed; requeueing mismatched request for retry", "id", current.ID, "workflow_job_id", current.WorkflowJobID, "assigned_job_id", observed.ID, "error", queueErr)
			queued = true
		}
		unlock = s.lockRunner(current.ID)
		latest, readErr := s.store.ReadState(current.ID)
		if readErr != nil {
			unlock()
			s.logger.Error("read mismatched completed state after workflow job check", "id", current.ID, "error", readErr)
			continue
		}
		if latest.Status != state.StatusCompleted || latest.WorkflowJobID != current.WorkflowJobID || latest.AssignedJobID != current.AssignedJobID {
			unlock()
			continue
		}
		requeued, next, requeueErr := s.requeueMismatchedWorkflowJob(latest, observed, queued)
		unlock()
		if requeueErr != nil {
			s.logger.Error("requeue mismatched completed workflow job", "id", st.ID, "workflow_job_id", current.WorkflowJobID, "assigned_job_id", current.AssignedJobID, "error", requeueErr)
			continue
		}
		if requeued {
			s.logger.Warn("reconciler requeued mismatched completed workflow job", "id", next.ID, "workflow_job_id", next.WorkflowJobID, "assigned_job_id", current.AssignedJobID, "repository", next.RepositoryFullName)
			s.refreshMetrics()
		}
	}
}

func (s *Server) requeueMismatchedWorkflowJob(st state.RunnerState, observed github.WorkflowJob, queued bool) (bool, state.RunnerState, error) {
	if !queued {
		next := st
		next.AssignedJobID = 0
		next.AssignedJobName = ""
		if err := s.store.WriteState(next); err != nil {
			return false, state.RunnerState{}, fmt.Errorf("write mismatched workflow job inspection: %w", err)
		}
		// Keep the returned state aligned with WriteState's CAS-persisted version.
		next.Version++
		s.store.AppendLog(st.ID, "control.log", []byte(fmt.Sprintf("runner completed different workflow job %d; original workflow job %d is no longer queued\n", observed.ID, st.WorkflowJobID)))
		return false, next, nil
	}
	next := st
	next.Status = state.StatusQueued
	next.SandboxID = ""
	next.ProcessPID = 0
	next.AssignedJobID = 0
	next.AssignedJobName = ""
	next.TerminationSource = ""
	next.RunnerExitCode = nil
	next.Error = ""
	next.FailureStage = ""
	next.FailureReason = ""
	next.LastErrorCode = ""
	next.LastErrorMessage = ""
	next.LastErrorRetryable = false
	next.LeaseOwner = ""
	next.LeaseExpiresAt = time.Time{}
	next.NextRetryAt = time.Time{}
	next.CreatingAt = time.Time{}
	next.RunningAt = time.Time{}
	next.StoppingAt = time.Time{}
	next.CompletedAt = time.Time{}
	next.FailedAt = time.Time{}
	next.GitHubJobName = ""
	next.GitHubJobStatus = ""
	next.GitHubJobConclusion = ""
	next.GitHubJobRunnerName = ""
	next.GitHubJobObservedAt = time.Time{}
	if err := s.store.WriteState(next); err != nil {
		return false, state.RunnerState{}, fmt.Errorf("write mismatched workflow job requeue: %w", err)
	}
	// Keep the returned state aligned with WriteState's CAS-persisted version.
	next.Version++
	s.store.AppendLog(st.ID, "control.log", []byte(fmt.Sprintf("runner completed different workflow job %d; original workflow job %d is queued, requeued request\n", observed.ID, st.WorkflowJobID)))
	s.signalQueue()
	return true, next, nil
}

func (s *Server) originalWorkflowJobQueued(ctx context.Context, st state.RunnerState) (bool, error) {
	if st.WorkflowJobID == 0 || strings.TrimSpace(st.RepositoryFullName) == "" {
		return false, nil
	}
	jobCtx, cancel := context.WithTimeout(ctx, workflowJobLookupTimeout)
	defer cancel()
	job, err := s.gh.GetWorkflowJob(jobCtx, st.RepositoryFullName, st.WorkflowJobID)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(strings.TrimSpace(job.Status), "queued"), nil
}

func (s *Server) reconcileCompletedWorkflowJobs(ctx context.Context) {
	states, err := s.store.ListFailedWorkflowJobStates(50)
	if err != nil {
		s.logger.Error("list failed workflow job states for reconciler", "error", err)
		return
	}
	for _, st := range states {
		if err := s.completeIfWorkflowJobCompleted(ctx, st.ID); err != nil {
			s.logger.Error("reconcile completed workflow job", "id", st.ID, "workflow_job_id", st.WorkflowJobID, "error", err)
		}
	}
}

func (s *Server) completeIfWorkflowJobCompleted(ctx context.Context, id string) error {
	unlock := s.lockRunner(id)
	st, err := s.store.ReadState(id)
	if err != nil {
		unlock()
		return err
	}
	if st.WorkflowJobID == 0 || st.RepositoryFullName == "" {
		unlock()
		return nil
	}
	if st.Status != state.StatusFailed || (st.FailureStage != "recovery" && st.FailureStage != "cleanup") {
		unlock()
		return nil
	}
	repositoryFullName := st.RepositoryFullName
	workflowJobID := st.WorkflowJobID
	stateVersion := st.Version
	unlock()

	jobCtx, cancel := context.WithTimeout(ctx, workflowJobLookupTimeout)
	defer cancel()
	job, err := s.gh.GetWorkflowJob(jobCtx, repositoryFullName, workflowJobID)
	if err != nil {
		return err
	}
	if !strings.EqualFold(strings.TrimSpace(job.Status), "completed") {
		return nil
	}

	unlock = s.lockRunner(id)
	defer unlock()
	latest, err := s.store.ReadState(id)
	if err != nil {
		return err
	}
	if latest.Version != stateVersion || latest.WorkflowJobID != workflowJobID || latest.RepositoryFullName != repositoryFullName {
		return nil
	}
	if latest.Status != state.StatusFailed || (latest.FailureStage != "recovery" && latest.FailureStage != "cleanup") {
		return nil
	}
	latest.Status = state.StatusCompleted
	latest.AssignedJobID = 0
	latest.AssignedJobName = ""
	latest.Error = ""
	latest.FailureStage = ""
	latest.FailureReason = ""
	latest.LastErrorCode = ""
	latest.LastErrorMessage = ""
	latest.LastErrorRetryable = false
	latest.NextRetryAt = time.Time{}
	latest.LeaseOwner = ""
	latest.LeaseExpiresAt = time.Time{}
	completedAt := time.Now().UTC()
	retainWorkflowJobResult(&latest, job, completedAt)
	latest.CompletedAt = completedAt
	if err := s.store.WriteState(latest); err != nil {
		return err
	}
	s.store.AppendLog(id, "control.log", []byte(fmt.Sprintf("workflow job %d is completed on GitHub; marked request completed\n", workflowJobID)))
	s.logger.Info("marked failed request completed because workflow job is completed on GitHub", "id", id, "workflow_job_id", workflowJobID, "repository", repositoryFullName, "conclusion", job.Conclusion)
	s.refreshMetrics()
	return nil
}
