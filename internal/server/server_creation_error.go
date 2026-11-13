package server

import (
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func (s *Server) retainUnknownCreateFailure(attempt state.RunnerState, failure error) {
	category := sandboxrunner.CreateFailureCategory(failure)
	message := "Sandbox creation outcome unknown; category=" + category + "; awaiting exact-attempt reconciliation"
	recorded := false
	current, err := s.store.ReadState(attempt.ID)
	if err == nil && current.Status == state.StatusCreating && current.ProvisionAttemptID == attempt.ProvisionAttemptID && current.WatchToken == attempt.WatchToken && current.SandboxID == "" {
		current.LastErrorCode = "sandbox_create_outcome_unknown"
		current.LastErrorMessage = message
		current.LastErrorRetryable = false // Reconcile this dispatch; do not create another.
		err = s.store.WriteWatchedState(current, attempt.WatchToken)
		if err == nil {
			recorded = true
			s.store.AppendStagedLog(attempt.ID, "control.log", "sandbox_creation", []byte(message+"\n"))
		}
	}
	// Preserve the safe category even when a peer already owns the row. Do not
	// let a late creator overwrite that peer's state or its current event stream.
	s.logger.Warn("Sandbox creation outcome unknown", "id", attempt.ID, "attempt_id", attempt.ProvisionAttemptID, "category", category, "state_recorded", recorded)
}
