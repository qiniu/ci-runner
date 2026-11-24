package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type unknownCreationResolutionRequest struct {
	Version                    int64  `json:"version"`
	AttemptID                  string `json:"attempt_id"`
	Evidence                   string `json:"evidence"`
	NoSandboxOrPendingCreation bool   `json:"no_sandbox_or_pending_creation"`
	AcceptUnknownResult        bool   `json:"accept_unknown_result"`
}

// This is only a local eligibility hint. Closure still requires live provider
// checks and the database lease/version/attempt fence in the mutation path.
func canResolveUnknownCreation(st state.RunnerState, now time.Time) bool {
	return state.UnknownSandboxCreation(st) && st.Status == state.StatusStopping && st.Version > 0 &&
		!st.StoppingAt.IsZero() && now.Sub(st.StoppingAt) >= state.UnknownCreationReviewDelay &&
		st.WorkflowJobID > 0 && st.RunningAt.IsZero() && st.CleanupGitHubRunnerID == 0 &&
		st.RepositoryFullName != "" && st.RunnerName != "" &&
		(st.JITRegistrationState == "" || st.JITRegistrationState == state.JITUnsent || st.JITRegistrationState == state.JITRejected)
}

// This is deliberately separate from Stop and from automatic recovery. Only a
// named administrator can attest absence or explicitly accept an unknown result.
func (s *Server) handleResolveUnknownCreation(w http.ResponseWriter, r *http.Request) {
	session, _, ok := s.requireAdminAccount(w, r)
	if !ok {
		return
	}
	var input unknownCreationResolutionRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid resolution payload")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeError(w, http.StatusBadRequest, "expected one resolution payload")
		return
	}
	input.Evidence = strings.TrimSpace(input.Evidence)
	if input.NoSandboxOrPendingCreation == input.AcceptUnknownResult || input.AttemptID == "" || input.Version <= 0 || len(input.Evidence) < 20 || len(input.Evidence) > 1024 {
		writeError(w, http.StatusBadRequest, "exact version/attempt, evidence or closure reason (20–1024 bytes), and exactly one of no_sandbox_or_pending_creation or accept_unknown_result are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	st, err := s.store.ReadState(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "runner not found")
		return
	}
	if !canResolveUnknownCreation(st, time.Now()) || st.Version != input.Version || st.ProvisionAttemptID != input.AttemptID {
		writeError(w, http.StatusConflict, "request changed or is not an eligible long-unknown creation")
		return
	}
	if s.gh == nil {
		writeError(w, http.StatusServiceUnavailable, "GitHub verification is unavailable")
		return
	}
	cleanup, err := s.beginRunnerCleanup(ctx, &st, time.Now(), "")
	if err != nil {
		if errors.Is(err, state.ErrConflict) {
			writeError(w, http.StatusConflict, "cleanup is owned by another operation; refresh before retrying")
		} else {
			writeError(w, http.StatusInternalServerError, "cannot claim cleanup")
		}
		return
	}
	defer cleanup.close()
	// Provider I/O stays outside the state/audit transaction and is bounded by
	// both the HTTP request and the cleanup owner's lifetime.
	stopCancel := context.AfterFunc(cleanup.ctx, cancel)
	defer stopCancel()
	req, err := s.store.ReadRequest(st.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "cannot read request")
		return
	}
	service, err := s.sandboxServiceForRunnerRequest(ctx, req)
	if err != nil {
		writeError(w, http.StatusBadGateway, "cannot verify saved Sandbox service")
		return
	}
	_, err = service.FindRunnerSandbox(ctx, st.ID, st.ProvisionAttemptID)
	if !errors.Is(err, sandboxrunner.ErrSandboxNotFound) {
		writeError(w, http.StatusConflict, "Sandbox absence is not verified; continue normal reconciliation")
		return
	}
	job, err := s.gh.GetWorkflowJob(ctx, st.RepositoryFullName, st.WorkflowJobID)
	if err != nil || job.ID != st.WorkflowJobID || job.Status != "completed" {
		writeError(w, http.StatusConflict, "original GitHub Job completion is not verified")
		return
	}
	observedAt := time.Now().UTC()
	runner, err := s.gh.FindRunnerByName(ctx, st.RepositoryFullName, st.RunnerGroup, st.RunnerName)
	if err != nil || runner != nil {
		writeError(w, http.StatusConflict, "GitHub Runner absence is not verified")
		return
	}
	retainWorkflowJobResult(&st, job, observedAt)
	provider := session.Provider
	if provider == "" {
		provider = "github"
	}
	audit, err := auditEventFor(provider+":"+session.Subject, "runner.resolve_unknown_creation", "runner_request", st.ID, input)
	if err == nil {
		err = s.store.ResolveUnknownSandboxCreation(ctx, st, cleanup.token, input.AcceptUnknownResult, audit)
	}
	if err != nil {
		if errors.Is(err, state.ErrConflict) {
			writeError(w, http.StatusConflict, "request or lease changed during verification; refresh before retrying")
			return
		}
		writeError(w, http.StatusInternalServerError, "resolution was not committed")
		return
	}
	s.observeJobTimes(ctx, st.RepositoryFullName, job, job.Status, "github_api", observedAt)
	s.logRequiredAuditEvent(audit)
	code := state.UnknownCreationResolvedCode
	message := "Administrator resolved the exact creation attempt as absent using external evidence; no Sandbox deletion was observed. See audit.\n"
	if input.AcceptUnknownResult {
		code = state.UnknownCreationClosedCode
		message = "Administrator accepted the unknown creation outcome and ended reconciliation for this exact attempt; resource absence is unverified and no Sandbox deletion was observed. See audit.\n"
	}
	s.store.AppendStagedLog(st.ID, "control.log", "sandbox_creation_resolution", []byte(message))
	s.refreshMetrics()
	writeJSON(w, http.StatusOK, map[string]string{"id": st.ID, "status": state.StatusCompleted, "resolution": code})
}
