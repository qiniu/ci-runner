package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"connectrpc.com/connect"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func terminalReference(t state.TerminalSession) sandboxrunner.TerminalReference {
	return sandboxrunner.TerminalReference{PID: t.PID, StreamID: t.StreamID}
}
func terminalTag(t state.TerminalSession) string { return "runnerd-terminal-" + t.ID }

func (s *Server) sharedTerminalService(t state.TerminalSession) (sandboxrunner.SharedTerminalService, error) {
	if t.SandboxAPIURL != "" && t.SandboxAPIKeyEncrypted != "" {
		key, err := decryptSecret(t.SandboxAPIKeyEncrypted, s.cfg.AuthEncryptionKey.Value())
		if err != nil {
			return nil, err
		}
		return sandboxrunner.NewE2BService(key, t.SandboxAPIURL, s.sandboxHTTP)
	}
	if service, ok := s.sandbox.(sandboxrunner.SharedTerminalService); ok {
		return service, nil
	}
	return nil, errors.New("terminal provider unavailable")
}

func (s *Server) createSharedTerminal(w http.ResponseWriter, r *http.Request, t state.TerminalSession, service sandboxrunner.SharedTerminalService) {
	var input terminalCreateRequest
	if r.Body != nil {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid terminal size")
			return
		}
	}
	if err := s.store.CreateTerminal(r.Context(), t); err != nil {
		writeError(w, http.StatusServiceUnavailable, "cannot persist terminal session")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	ref, baseline, err := service.CreatePTY(ctx, t.SandboxID, terminalTag(t), sandboxrunner.PtySize{Cols: input.Cols, Rows: input.Rows})
	cancel()
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer persistCancel()
	if err == nil {
		err = s.store.ActivateTerminal(persistCtx, t.ID, ref.PID, ref.StreamID, baseline)
	}
	if err == nil {
		err = s.checkTerminalAccess(r.Context(), t)
	}
	if err != nil {
		_ = s.store.CloseTerminal(persistCtx, t.ID)
		if errors.Is(err, sandboxrunner.ErrTerminalReplayUnsupported) {
			writeError(w, http.StatusConflict, "Sandbox envd must be upgraded to support shared terminals")
			return
		}
		writeError(w, http.StatusBadGateway, "cannot open terminal; cleanup is retained for recovery")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]any{"session_id": t.ID, "pid": ref.PID, "sandbox_id": t.SandboxID})
}

func (s *Server) checkTerminalAccess(ctx context.Context, t state.TerminalSession) error {
	var installation int64
	var repository string
	switch t.Kind {
	case "runner":
		st, err := s.store.ReadState(t.ResourceID)
		if err != nil {
			return err
		}
		if !runnerTerminalAvailable(st) || st.SandboxID != t.SandboxID || runnerTerminalKey(st, t.AccountID) != t.BindingKey {
			return state.ErrConflict
		}
		installation, repository = st.GitHubInstallationID, st.RepositoryFullName
	case "deployment":
		d, err := s.store.GetDeployment(t.ResourceID)
		if err != nil {
			return err
		}
		if !deploymentTerminalAvailable(d) || d.SandboxID != t.SandboxID || deploymentAccountTerminalKey(d, t.AccountID) != t.BindingKey {
			return state.ErrConflict
		}
		installation, repository = d.GitHubInstallationID, d.RepositoryFullName
	default:
		return state.ErrConflict
	}
	access, err := s.userAuthorizedRepositoryAccess(ctx, t.AccountID)
	if err != nil {
		return err
	}
	if !hasRepositoryAccess(access, installation, repository) {
		return state.ErrConflict
	}
	return nil
}

func (s *Server) streamTerminalSession(w http.ResponseWriter, r *http.Request, t *state.TerminalSession) {
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if s.loopCtx != nil {
		stop := context.AfterFunc(s.loopCtx, cancel)
		defer stop()
	}
	cursor := t.Baseline
	if id := r.Header.Get("Last-Event-ID"); id != "" {
		offset, err := strconv.ParseUint(id, 10, 64)
		if err != nil || offset < t.Baseline {
			writeError(w, http.StatusBadRequest, "invalid terminal cursor")
			return
		}
		cursor = offset
	}
	if err := s.store.RenewTerminal(ctx, t.ID); err != nil {
		writeError(w, http.StatusConflict, "terminal is no longer available")
		return
	}
	service, err := s.sharedTerminalService(*t)
	if err != nil {
		writeError(w, http.StatusBadGateway, "terminal provider unavailable")
		return
	}
	// Handshake timeout is independent of the lifetime of the output subscription.
	handshake := time.AfterFunc(15*time.Second, cancel)
	stream, err := service.ConnectPTY(ctx, t.SandboxID, terminalReference(*t), cursor)
	handshake.Stop()
	controller := http.NewResponseController(w)
	if !prepareTerminalStreamWrite(controller) {
		if stream != nil {
			_ = stream.Close()
		}
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	closeEvent := func() { _, _ = io.WriteString(w, "event: terminal-closed\ndata: {}\n\n"); _ = controller.Flush() }
	if err != nil {
		if connect.CodeOf(err) == connect.CodeOutOfRange || connect.CodeOf(err) == connect.CodeNotFound {
			closeEvent()
		} else {
			_, _ = io.WriteString(w, ": terminal reconnect\n\n")
			_ = controller.Flush()
		}
		return
	}
	defer func() {
		cancel()
		_ = stream.Close()
		// An observed disconnect gets the full shared grace period. A delayed
		// disconnect never shortens a newer subscriber's deadline or reopens a
		// closing session. A hard process exit is covered by the last heartbeat.
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer releaseCancel()
		_ = s.store.RenewTerminal(releaseCtx, t.ID)
	}()
	if controller.Flush() != nil {
		return
	}
	type received struct {
		event sandboxrunner.TerminalEvent
		err   error
	}
	events := make(chan received, 1)
	go func() {
		for {
			event, err := stream.Receive()
			select {
			case events <- received{event, err}:
			case <-ctx.Done():
				return
			}
			if err != nil || event.End != nil {
				return
			}
		}
	}()
	heartbeat := time.NewTicker(5 * time.Second)
	defer heartbeat.Stop()
	silence := time.NewTimer(15 * time.Second)
	defer silence.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-silence.C:
			return // no synthetic healthy heartbeat for a silent provider
		case <-heartbeat.C:
			checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
			accessErr := s.checkTerminalAccess(checkCtx, *t)
			if accessErr == nil {
				accessErr = s.store.RenewTerminal(checkCtx, t.ID)
			}
			checkCancel()
			if accessErr != nil {
				_ = s.store.CloseTerminal(ctx, t.ID)
				closeEvent()
				return
			}
		case item := <-events:
			if item.err != nil {
				if connect.CodeOf(item.err) == connect.CodeOutOfRange || connect.CodeOf(item.err) == connect.CodeNotFound {
					closeEvent()
				}
				return
			}
			silence.Reset(15 * time.Second)
			if !prepareTerminalStreamWrite(controller) {
				return
			}
			event := item.event
			if event.End != nil {
				closeEvent()
				_ = s.store.CloseTerminal(ctx, t.ID)
				return
			}
			cursor = event.Offset
			if len(event.Data) > 0 {
				err = writeTerminalEvent(w, event.Data, cursor)
			} else if event.Heartbeat {
				_, err = fmt.Fprintf(w, "id: %d\nevent: terminal-heartbeat\ndata: {}\n\n", cursor)
			}
			if err != nil || controller.Flush() != nil {
				return
			}
		}
	}
}

func (s *Server) closeSharedTerminal(ctx context.Context, id string) error {
	if err := s.store.CloseTerminal(ctx, id); err != nil {
		return err
	}
	return s.cleanupTerminal(ctx, id)
}

func (s *Server) cleanupTerminal(ctx context.Context, id string) error {
	token := newID()
	claimed, err := s.store.ClaimTerminalCleanup(ctx, id, token)
	if err != nil || !claimed {
		return err
	}
	t, err := s.store.GetTerminal(ctx, id)
	if err != nil {
		return err
	}
	service, err := s.sharedTerminalService(t)
	if err != nil {
		return err
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	closed, err := service.CleanupPTY(cleanupCtx, t.SandboxID, terminalTag(t), terminalReference(t))
	cancel()
	if err != nil {
		return err
	}
	return s.store.FinishTerminalCleanup(ctx, id, token, closed)
}

func (s *Server) terminalCleanupLoop(ctx context.Context) {
	defer s.loopWG.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		s.reconcileTerminals(ctx)
	}
}

func (s *Server) reconcileTerminals(ctx context.Context) {
	after := ""
	for ctx.Err() == nil {
		rows, err := s.store.ListTerminalCleanupCandidates(ctx, after, 100)
		if err != nil {
			s.logger.Error("list shared terminals", "error", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		for _, t := range rows {
			after = t.ID
			// Active subscribers reauthorize every five seconds. Without a subscriber,
			// the shared idle deadline closes the PTY, including after node failure.
			if err := s.cleanupTerminal(ctx, t.ID); err != nil && ctx.Err() == nil {
				s.logger.Warn("reconcile shared terminal", "session", t.ID, "error", err)
			}
		}
	}
}
