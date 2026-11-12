package server

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

// HTTP middleware must preserve the upgrade capability. Other HTTP routes keep
// their existing accounting and ResponseController behavior.
func (w *loggingResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	w.status = http.StatusSwitchingProtocols
	return http.NewResponseController(w.ResponseWriter).Hijack()
}

type terminalTarget struct {
	row     state.TerminalSession
	start   sandboxrunner.TerminalStart
	service sandboxrunner.Service
	current func() bool
}

func (s *Server) handleRunnerTerminalSocket(w http.ResponseWriter, r *http.Request) {
	st, account, ok := s.userRunnerStateAndAccount(w, r)
	if !ok {
		return
	}
	if !runnerTerminalAvailable(st) {
		writeError(w, 409, "terminal is not available")
		return
	}
	svc, err := s.sandboxServiceForRunnerRequest(r.Context(), state.RunnerRequest{ID: st.ID, SandboxAPIURL: st.SandboxAPIURL, SandboxAPIKeyEncrypted: st.SandboxAPIKeyEncrypted, GitHubInstallationID: st.GitHubInstallationID})
	if err != nil {
		writeError(w, 502, "terminal service unavailable")
		return
	}
	s.serveTerminalSocket(w, r, terminalTarget{
		row:   state.TerminalSession{AccountID: account, ResourceKind: "runner", ResourceID: st.ID, Binding: runnerTerminalKey(st, account), GitHubInstallationID: st.GitHubInstallationID, RepositoryFullName: st.RepositoryFullName, SandboxID: st.SandboxID},
		start: sandboxrunner.TerminalStart{SandboxID: st.SandboxID}, service: svc,
		current: func() bool {
			now, err := s.store.ReadState(st.ID)
			return err == nil && sameRunnerTerminal(st, now, account)
		},
	})
}

func (s *Server) handleDeploymentTerminalSocket(w http.ResponseWriter, r *http.Request) {
	d, account, ok := s.loadDeploymentTerminal(w, r)
	if !ok {
		return
	}
	svc := s.sandbox
	if d.SandboxAPIURL != "" || d.SandboxAPIKeyEncrypted != "" {
		key, err := decryptSecret(d.SandboxAPIKeyEncrypted, s.cfg.AuthEncryptionKey.Value())
		if err != nil || key == "" || d.SandboxAPIURL == "" {
			writeError(w, 409, "deployment Sandbox credentials are unavailable")
			return
		}
		svc, err = sandboxrunner.NewE2BService(key, d.SandboxAPIURL, s.sandboxHTTP)
		if err != nil {
			writeError(w, 502, "terminal service unavailable")
			return
		}
	}
	if svc == nil {
		writeError(w, 409, "terminal service unavailable")
		return
	}
	s.serveTerminalSocket(w, r, terminalTarget{
		row:   state.TerminalSession{AccountID: account, ResourceKind: "deployment", ResourceID: d.ID, Binding: fmt.Sprintf("account:%d:%s", account, deploymentTerminalKey(d)), GitHubInstallationID: d.GitHubInstallationID, RepositoryFullName: d.RepositoryFullName, SandboxID: d.SandboxID},
		start: sandboxrunner.TerminalStart{SandboxID: d.SandboxID, ExpiresAt: d.ExpiresAt}, service: svc,
		current: func() bool {
			now, err := s.store.GetDeployment(d.ID)
			return err == nil && sameDeploymentTerminal(d, now) && now.GitHubInstallationID == d.GitHubInstallationID && now.RepositoryFullName == d.RepositoryFullName
		},
	})
}

func terminalSameOrigin(r *http.Request) bool {
	origin, err := url.Parse(r.Header.Get("Origin"))
	return err == nil && (origin.Scheme == "https" || origin.Scheme == "http") && origin.Host == r.Host && origin.User == nil && origin.Path == "" && origin.RawQuery == "" && origin.Fragment == ""
}

type terminalFrame struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Cols uint32 `json:"cols,omitempty"`
	Rows uint32 `json:"rows,omitempty"`
}

func decodeTerminalFrame(raw []byte) (terminalFrame, error) {
	var frame terminalFrame
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&frame); err != nil {
		return frame, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return frame, errors.New("trailing input")
	}
	switch frame.Type {
	case "input":
		if len(frame.Data) > 4096 || frame.Cols != 0 || frame.Rows != 0 {
			return frame, errors.New("invalid input")
		}
	case "resize":
		if frame.Data != "" || frame.Cols == 0 || frame.Rows == 0 || frame.Cols > 500 || frame.Rows > 500 {
			return frame, errors.New("invalid size")
		}
	default:
		return frame, errors.New("invalid operation")
	}
	return frame, nil
}

func (s *Server) serveTerminalSocket(w http.ResponseWriter, r *http.Request, target terminalTarget) {
	// A stateless HTTP preflight preserves the application's normal OAuth/error
	// handling. The subsequent upgrade repeats authorization and creates nothing
	// on this path; the preflight does not grant or reserve a session.
	if !websocket.IsWebSocketUpgrade(r) {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, map[string]bool{"available": true})
		return
	}
	if !terminalSameOrigin(r) {
		writeError(w, 403, "invalid terminal origin")
		return
	}
	cols, err1 := strconv.ParseUint(r.URL.Query().Get("cols"), 10, 32)
	rows, err2 := strconv.ParseUint(r.URL.Query().Get("rows"), 10, 32)
	if err1 != nil || err2 != nil || cols == 0 || rows == 0 || cols > 500 || rows > 500 {
		writeError(w, 400, "invalid terminal size")
		return
	}
	s.terminalGate.Lock()
	if s.terminalClosed {
		s.terminalGate.Unlock()
		writeError(w, 503, "server is stopping")
		return
	}
	s.terminalWG.Add(1)
	s.terminalGate.Unlock()
	defer s.terminalWG.Done()
	upgrader := websocket.Upgrader{HandshakeTimeout: 5 * time.Second, CheckOrigin: terminalSameOrigin}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(8192)
	_ = conn.SetReadDeadline(time.Now().Add(20 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(20 * time.Second)) })
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	if s.loopCtx != nil {
		stop := context.AfterFunc(s.loopCtx, cancel)
		defer stop()
	}
	// Close the socket on shutdown even if its reader is blocked. No business
	// session index is retained: this cancellation belongs only to this request.
	stopSocket := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stopSocket()
	frames := make(chan terminalFrame, 8)
	go func() {
		defer cancel()
		for {
			kind, raw, err := conn.ReadMessage()
			if err != nil || kind != websocket.TextMessage {
				return
			}
			frame, err := decodeTerminalFrame(raw)
			if err != nil {
				return
			}
			select {
			case frames <- frame:
			case <-ctx.Done():
				return
			}
		}
	}()
	write := func(value any) error {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		return conn.WriteJSON(value)
	}
	target.row.ID, target.row.Owner = newID(), newID()
	target.row.NodeID = s.workerID
	target.start.ID = target.row.ID
	target.start.Size = sandboxrunner.PtySize{Cols: uint32(cols), Rows: uint32(rows)}
	if err = s.store.CreateTerminalSession(ctx, target.row); err != nil {
		_ = write(map[string]string{"type": "error", "message": "terminal state unavailable"})
		return
	}
	reason, cleanup := "disconnected", "unknown"
	var term sandboxrunner.TerminalSession
	var cancelStream context.CancelFunc
	defer func() {
		if cancelStream != nil {
			defer cancelStream()
		}
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer closeCancel()
		if term != nil && term.Close(closeCtx) == nil {
			cleanup = "confirmed"
		}
		// Use a separate budget: slow remote cleanup must not suppress its record.
		dbCtx, dbCancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer dbCancel()
		if err := s.store.FinishTerminalSession(dbCtx, target.row.ID, target.row.Owner, cleanup, reason); err != nil {
			s.logger.Warn("record terminal close", "session_id", target.row.ID, "error", err)
		}
	}()
	authorized := func() bool {
		if !target.current() {
			return false
		}
		checkCtx, checkCancel := context.WithTimeout(ctx, 5*time.Second)
		defer checkCancel()
		access, err := s.userAuthorizedRepositoryAccess(checkCtx, target.row.AccountID)
		return err == nil && hasRepositoryAccess(access, target.row.GitHubInstallationID, target.row.RepositoryFullName)
	}
	if !authorized() {
		reason = "access_or_attempt_changed"
		return
	}
	output := make(chan []byte, 16)
	// A bounded output queue provides backpressure without dropping bytes.
	startCtx, startCancel := context.WithCancel(context.WithoutCancel(ctx))
	cancelStream = startCancel
	timer := time.AfterFunc(20*time.Second, startCancel)
	term, err = target.service.StartTerminal(startCtx, target.start, func(data []byte) {
		for len(data) > 0 {
			n := min(len(data), 4096)
			chunk := append([]byte(nil), data[:n]...)
			data = data[n:]
			select {
			case output <- chunk:
			default:
				cancel()
				return
			}
		}
	})
	timer.Stop()
	if err != nil {
		reason = "create_outcome_unknown"
		_ = write(map[string]string{"type": "error", "message": "terminal could not be started"})
		return
	}
	if ctx.Err() != nil || startCtx.Err() != nil || !authorized() {
		reason = "access_or_attempt_changed"
		return
	}
	active, err := s.store.TouchTerminalSession(ctx, target.row.ID, target.row.Owner, true)
	if err != nil || !active {
		reason = "ownership_lost"
		return
	}
	if err = write(map[string]any{"type": "ready", "session_id": target.row.ID, "pid": term.PID(), "sandbox_id": target.row.SandboxID}); err != nil {
		return
	}
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-term.Done():
			reason = "process_exited"
			return
		case data := <-output:
			if err := write(map[string]any{"type": "output", "data": data}); err != nil {
				return
			}
		case <-tick.C:
			if !authorized() {
				reason = "access_or_attempt_changed"
				return
			}
			live, err := s.store.TouchTerminalSession(ctx, target.row.ID, target.row.Owner, false)
			if err != nil || !live {
				reason = "ownership_lost"
				return
			}
			opCtx, opCancel := context.WithTimeout(ctx, 5*time.Second)
			err = term.KeepAlive(opCtx)
			opCancel()
			if err != nil {
				reason = "remote_unavailable"
				return
			}
			if err = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)); err != nil {
				return
			}
		case frame := <-frames:
			if !authorized() {
				reason = "access_or_attempt_changed"
				return
			}
			// A delayed operation must not outlive its database ownership.
			live, err := s.store.TouchTerminalSession(ctx, target.row.ID, target.row.Owner, false)
			if err != nil || !live {
				reason = "ownership_lost"
				return
			}
			opCtx, opCancel := context.WithTimeout(ctx, 5*time.Second)
			if frame.Type == "input" {
				err = term.SendInput(opCtx, []byte(frame.Data))
			} else {
				err = term.Resize(opCtx, sandboxrunner.PtySize{Cols: frame.Cols, Rows: frame.Rows})
			}
			opCancel()
			if err != nil {
				reason = "operation_outcome_unknown"
				return
			}
		}
	}
}

func (s *Server) terminalExpiryLoop(ctx context.Context, store state.TerminalStore) {
	defer s.loopWG.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		sweepCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		err := store.ExpireTerminalSessions(sweepCtx)
		cancel()
		if err != nil && ctx.Err() == nil {
			s.logger.Warn("expire terminal sessions", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
