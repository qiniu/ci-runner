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
}

// This is deliberately separate from Stop and from automatic recovery. Only a
// named administrator can attest external evidence about an unresolved create.
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
	if !input.NoSandboxOrPendingCreation || input.AttemptID == "" || input.Version <= 0 || len(input.Evidence) < 20 || len(input.Evidence) > 1024 {
		writeError(w, http.StatusBadRequest, "exact version/attempt, external verification evidence (20–1024 bytes), and confirmation of no Sandbox or pending creation are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	st, err := s.store.ReadState(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "runner not found")
		return
	}
	if !state.UnknownSandboxCreation(st) || st.Status != state.StatusStopping || st.Version != input.Version || st.ProvisionAttemptID != input.AttemptID ||
		st.StoppingAt.IsZero() || time.Since(st.StoppingAt) < state.UnknownCreationReviewDelay ||
		st.WorkflowJobID <= 0 || !st.RunningAt.IsZero() || st.CleanupGitHubRunnerID != 0 || st.RepositoryFullName == "" || st.RunnerName == "" ||
		(st.JITRegistrationState != "" && st.JITRegistrationState != state.JITUnsent && st.JITRegistrationState != state.JITRejected) {
		writeError(w, http.StatusConflict, "request changed or is not an eligible long-unknown creation")
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
	runnerID, err := s.gh.FindRunnerIDByName(ctx, st.RepositoryFullName, st.RunnerGroup, st.RunnerName)
	if err != nil || runnerID != 0 {
		writeError(w, http.StatusConflict, "GitHub Runner absence is not verified")
		return
	}
	retainWorkflowJobResult(&st, job, time.Now().UTC())
	provider := session.Provider
	if provider == "" {
		provider = "github"
	}
	audit, err := auditEventFor(provider+":"+session.Subject, "runner.resolve_unknown_creation", "runner_request", st.ID, input)
	if err == nil {
		err = s.store.ResolveUnknownSandboxCreation(ctx, st, cleanup.token, audit)
	}
	if err != nil {
		if errors.Is(err, state.ErrConflict) {
			writeError(w, http.StatusConflict, "request or lease changed during verification; refresh before retrying")
			return
		}
		writeError(w, http.StatusInternalServerError, "resolution was not committed")
		return
	}
	s.logRequiredAuditEvent(audit)
	s.store.AppendStagedLog(st.ID, "control.log", "sandbox_creation_resolution", []byte("Administrator resolved the exact creation attempt as absent using external evidence; no Sandbox deletion was observed. See audit.\n"))
	s.refreshMetrics()
	writeJSON(w, http.StatusOK, map[string]string{"id": st.ID, "status": state.StatusCompleted, "resolution": state.UnknownCreationResolvedCode})
}
