package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func deploymentTerminalKey(d state.Deployment) string {
	return fmt.Sprintf("deployment:%s:revision:%d:attempt:%d", d.ID, d.RevisionNumber, d.Attempt)
}

func deploymentTerminalAvailable(d state.Deployment) bool {
	return d.Status == state.DeploymentStatusRunning && d.SandboxID != "" && time.Now().Before(d.ExpiresAt)
}

func (s *Server) loadDeploymentTerminal(w http.ResponseWriter, r *http.Request) (state.Deployment, int64, bool) {
	_, account, d, ok := s.loadAuthorizedDeployment(w, r)
	if !ok {
		return state.Deployment{}, 0, false
	}
	number, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || number <= 0 {
		writeError(w, http.StatusBadRequest, "invalid revision number")
		return state.Deployment{}, 0, false
	}
	if number != d.RevisionNumber || !deploymentTerminalAvailable(d) {
		writeError(w, http.StatusConflict, "terminal is only available for the current running deployment revision")
		return state.Deployment{}, 0, false
	}
	return d, account.ID, true
}

func (s *Server) handleUserCreateDeploymentTerminal(w http.ResponseWriter, r *http.Request) {
	d, accountID, ok := s.loadDeploymentTerminal(w, r)
	if !ok {
		return
	}
	session := state.TerminalSession{ID: newID(), AccountID: accountID, Kind: "deployment", ResourceID: d.ID, Revision: d.RevisionNumber, BindingKey: deploymentAccountTerminalKey(d, accountID), SandboxID: d.SandboxID, SandboxAPIURL: d.SandboxAPIURL, SandboxAPIKeyEncrypted: d.SandboxAPIKeyEncrypted, ExpiresAt: &d.ExpiresAt}
	service, err := s.sharedTerminalService(session)
	if err != nil {
		writeError(w, http.StatusConflict, "deployment Sandbox credentials are unavailable")
		return
	}
	s.createSharedTerminal(w, r, session, service)
}

func (s *Server) authorizedDeploymentTerminal(w http.ResponseWriter, r *http.Request) (*state.TerminalSession, bool) {
	d, accountID, ok := s.loadDeploymentTerminal(w, r)
	if !ok {
		return nil, false
	}
	session, err := s.store.GetTerminal(r.Context(), r.PathValue("sessionID"))
	if err != nil || session.Kind != "deployment" || session.AccountID != accountID || session.ResourceID != d.ID || session.BindingKey != deploymentAccountTerminalKey(d, accountID) || session.SandboxID != d.SandboxID || session.Status != "open" {
		writeError(w, http.StatusNotFound, "terminal session not found")
		return nil, false
	}
	return &session, true
}

func (s *Server) handleUserDeploymentTerminalEvents(w http.ResponseWriter, r *http.Request) {
	if session, ok := s.authorizedDeploymentTerminal(w, r); ok {
		s.streamTerminalSession(w, r, session)
	}
}

func (s *Server) handleUserDeploymentTerminalInput(w http.ResponseWriter, r *http.Request) {
	if session, ok := s.authorizedDeploymentTerminal(w, r); ok {
		s.sendTerminalInput(w, r, session)
	}
}

func (s *Server) handleUserDeploymentTerminalResize(w http.ResponseWriter, r *http.Request) {
	if session, ok := s.authorizedDeploymentTerminal(w, r); ok {
		s.resizeTerminalSession(w, r, session)
	}
}

func (s *Server) handleUserCloseDeploymentTerminal(w http.ResponseWriter, r *http.Request) {
	// The session creator may close their own PTY even after access is revoked.
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	number, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	session, loadErr := s.store.GetTerminal(r.Context(), r.PathValue("sessionID"))
	if err != nil || number <= 0 || loadErr != nil {
		writeError(w, http.StatusNotFound, "terminal session not found")
		return
	}
	prefix := fmt.Sprintf("account:%d:deployment:%s:revision:%d:attempt:", account.ID, strings.TrimSpace(r.PathValue("id")), number)
	// The attempt suffix is server-generated and is never supplied by the client.
	if !strings.HasPrefix(session.BindingKey, prefix) {
		writeError(w, http.StatusNotFound, "terminal session not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.closeSharedTerminal(ctx, session.ID); err != nil && !errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusBadGateway, "failed to close deployment terminal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func deploymentAccountTerminalKey(d state.Deployment, accountID int64) string {
	return fmt.Sprintf("account:%d:%s", accountID, deploymentTerminalKey(d))
}
