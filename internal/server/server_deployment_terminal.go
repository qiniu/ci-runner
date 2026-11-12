package server

import (
	"fmt"
	"net/http"
	"strconv"
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

func sameDeploymentTerminal(expected, current state.Deployment) bool {
	return deploymentTerminalAvailable(current) && current.AccountID == expected.AccountID && current.SandboxID == expected.SandboxID && deploymentTerminalKey(current) == deploymentTerminalKey(expected)
}
