package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
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
	var input terminalCreateRequest
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid terminal size")
			return
		}
	}
	var start func(context.Context, string, sandboxrunner.PtySize, func([]byte)) (sandboxrunner.TerminalSession, error)
	if s.sandbox != nil {
		start = s.sandbox.StartTerminal
	} else {
		key, err := decryptSecret(d.SandboxAPIKeyEncrypted, s.cfg.AuthEncryptionKey.Value())
		if err != nil || key == "" || d.SandboxAPIURL == "" {
			writeError(w, http.StatusConflict, "deployment Sandbox credentials are unavailable")
			return
		}
		service, err := sandboxrunner.NewE2BService(key, d.SandboxAPIURL, s.sandboxHTTP)
		if err != nil {
			writeError(w, http.StatusBadGateway, "cannot initialize deployment terminal")
			return
		}
		start = func(ctx context.Context, id string, size sandboxrunner.PtySize, output func([]byte)) (sandboxrunner.TerminalSession, error) {
			return service.StartTerminalUntil(ctx, id, size, d.ExpiresAt, output)
		}
	}
	parent := s.loopCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	// Bound the handshake without imposing a deadline on the PTY output stream.
	timer := time.AfterFunc(15*time.Second, cancel)
	var mu sync.Mutex
	var session *terminalSession
	var pending []byte
	term, err := start(ctx, d.SandboxID, sandboxrunner.PtySize{Cols: input.Cols, Rows: input.Rows}, func(data []byte) {
		mu.Lock()
		defer mu.Unlock()
		if session != nil {
			session.append(data)
			return
		}
		pending = append(pending, data...)
		if len(pending) > 64<<10 {
			pending = append([]byte(nil), pending[len(pending)-(64<<10):]...)
		}
	})
	timer.Stop()
	if err != nil {
		cancel()
		writeError(w, http.StatusBadGateway, "failed to connect deployment terminal")
		return
	}
	wrapped := &deploymentTerminalProcess{TerminalSession: term, cancel: cancel}
	// Provisioning races with stop/redeploy must never expose a stale shell.
	current, loadErr := s.store.GetDeployment(d.ID)
	if ctx.Err() != nil || r.Context().Err() != nil || loadErr != nil || !sameDeploymentTerminal(d, current) {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer closeCancel()
		_ = wrapped.Close(closeCtx)
		writeError(w, http.StatusConflict, "deployment changed while connecting terminal")
		return
	}
	mu.Lock()
	session = s.terminals.Add(deploymentAccountTerminalKey(d, accountID), d.SandboxID, wrapped)
	session.append(pending)
	pending = nil
	mu.Unlock()
	s.terminals.CloseSessionWhenIdle(session.id, terminalIdleCloseDelay)
	go s.watchDeploymentTerminal(ctx, d, accountID, session.id)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"session_id": session.id, "pid": term.PID(), "sandbox_id": d.SandboxID})
}

type deploymentTerminalProcess struct {
	sandboxrunner.TerminalSession
	cancel context.CancelFunc
}

func (p *deploymentTerminalProcess) Close(ctx context.Context) error {
	defer p.cancel()
	return p.TerminalSession.Close(ctx)
}

func sameDeploymentTerminal(expected, current state.Deployment) bool {
	return deploymentTerminalAvailable(current) && current.AccountID == expected.AccountID && current.SandboxID == expected.SandboxID && deploymentTerminalKey(current) == deploymentTerminalKey(expected)
}

func (s *Server) watchDeploymentTerminal(ctx context.Context, d state.Deployment, accountID int64, sessionID string) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = s.terminals.CloseSession(closeCtx, sessionID)
	}()
	nextAccessCheck := time.Now().Add(30 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, ok := s.terminals.Get(sessionID); !ok {
				return
			}
			current, err := s.store.GetDeployment(d.ID)
			if err != nil || !sameDeploymentTerminal(d, current) {
				return
			}
			if time.Now().Before(nextAccessCheck) {
				continue
			}
			checkCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			access, err := s.userAuthorizedRepositoryAccess(checkCtx, accountID)
			cancel()
			if err != nil || !hasRepositoryAccess(access, current.GitHubInstallationID, current.RepositoryFullName) {
				return
			}
			nextAccessCheck = time.Now().Add(30 * time.Second)
		}
	}
}

func (s *Server) authorizedDeploymentTerminal(w http.ResponseWriter, r *http.Request) (*terminalSession, bool) {
	d, accountID, ok := s.loadDeploymentTerminal(w, r)
	if !ok {
		return nil, false
	}
	session, ok := s.terminals.Get(r.PathValue("sessionID"))
	if !ok || session.requestID != deploymentAccountTerminalKey(d, accountID) || session.sandboxID != d.SandboxID {
		writeError(w, http.StatusNotFound, "terminal session not found")
		return nil, false
	}
	return session, true
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
	session, found := s.terminals.Get(r.PathValue("sessionID"))
	if err != nil || number <= 0 || !found {
		writeError(w, http.StatusNotFound, "terminal session not found")
		return
	}
	prefix := fmt.Sprintf("account:%d:deployment:%s:revision:%d:attempt:", account.ID, strings.TrimSpace(r.PathValue("id")), number)
	// The attempt suffix is server-generated and is never supplied by the client.
	if !strings.HasPrefix(session.requestID, prefix) {
		writeError(w, http.StatusNotFound, "terminal session not found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := s.terminals.CloseSession(ctx, session.id); err != nil && !errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusBadGateway, "failed to close deployment terminal")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func deploymentAccountTerminalKey(d state.Deployment, accountID int64) string {
	return fmt.Sprintf("account:%d:%s", accountID, deploymentTerminalKey(d))
}
