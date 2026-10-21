//go:build terminalfault

package server

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
)

// This fixture is opt-in and loopback-only. Authentication and deployment data
// are fixtures; SSE, replay, idle cleanup and the OS PTY are real.
func TestTerminalFaultHarness(t *testing.T) {
	readyFile := os.Getenv("RUNNERD_TERMINAL_FAULT_READY_FILE")
	if readyFile == "" {
		t.Skip("run task ui-terminal-fault-test to start the browser harness")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("terminal fault tests require Python 3 with the Unix pty module")
	}
	srv := &Server{terminals: newTerminalHub(nil)}
	var mu sync.Mutex
	records := map[string]*faultShellRecord{}
	streams := []faultStreamRecord{}
	creates, deletes := 0, 0
	stop := make(chan struct{})
	var stopOnce sync.Once
	deployment := map[string]any{
		"id": "terminal-fault", "repository_full_name": "fixture/project", "ref": "refs/heads/main",
		"commit_sha": strings.Repeat("a", 40), "status": "running", "sandbox_id": "local-pty",
		"revision_number": 1, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		"revisions": []map[string]any{{"number": 1, "status": "running", "ref": "refs/heads/main", "commit_sha": strings.Repeat("a", 40)}},
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.Replace(r.URL.Path, "/revisions/1", "", 1)
		switch {
		case path == "/__stop":
			stopOnce.Do(func() { close(stop) })
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		case path == "/__metrics":
			mu.Lock()
			defer mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"creates": creates, "deletes": deletes, "records": records, "streams": streams})
		case path == "/auth/session":
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "oauth_enabled": true, "login": "fixture", "role": "user"})
		case path == "/sandbox/regions":
			writeJSON(w, http.StatusOK, []any{})
		case path == "/user/deployments/terminal-fault/terminal" && r.Method == http.MethodPost:
			pty, output, err := startFaultPTY(python)
			if err != nil {
				t.Error(err)
				http.Error(w, "start test PTY", http.StatusInternalServerError)
				return
			}
			session := srv.terminals.Add("deployment:terminal-fault", "local-pty", pty)
			srv.terminals.CloseSessionWhenIdle(session.id, terminalIdleCloseDelay)
			mu.Lock()
			creates++
			record := &faultShellRecord{PID: pty.pid, Alive: true}
			records[session.id] = record
			mu.Unlock()
			go func() {
				buffer := make([]byte, 4096)
				for {
					n, err := output.Read(buffer)
					if n > 0 {
						session.append(buffer[:n])
					}
					if err != nil {
						break
					}
				}
				_ = pty.cmd.Wait()
				mu.Lock()
				record.Alive = false
				record.ExitedAt = time.Now()
				mu.Unlock()
				close(pty.done)
			}()
			writeJSON(w, http.StatusCreated, map[string]any{"session_id": session.id, "pid": pty.pid, "sandbox_id": "local-pty"})
		case strings.HasPrefix(path, "/user/deployments/terminal-fault/terminal/"):
			parts := strings.Split(strings.TrimPrefix(path, "/user/deployments/terminal-fault/terminal/"), "/")
			session, ok := srv.terminals.Get(parts[0])
			if !ok {
				http.NotFound(w, r)
				return
			}
			if r.Method == http.MethodDelete && len(parts) == 1 {
				mu.Lock()
				deletes++
				mu.Unlock()
				if err := srv.terminals.CloseSession(r.Context(), session.id); err != nil {
					t.Error(err)
				}
				writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
				return
			}
			if len(parts) == 2 {
				switch parts[1] {
				case "events":
					mu.Lock()
					streams = append(streams, faultStreamRecord{SessionID: session.id, LastEventID: r.Header.Get("Last-Event-ID")})
					mu.Unlock()
					srv.streamTerminalSession(w, r, session)
					return
				case "input":
					srv.sendTerminalInput(w, r, session)
					return
				case "resize":
					srv.resizeTerminalSession(w, r, session)
					return
				}
			}
			http.NotFound(w, r)
		case path == "/user/deployments/terminal-fault":
			writeJSON(w, http.StatusOK, deployment)
		case path == "/user/deployment-repositories":
			writeJSON(w, http.StatusOK, map[string]any{"items": []any{map[string]any{"repository_full_name": "fixture/project", "total": 1, "active_count": 1, "active_deployments": []any{deployment}}}, "has_more": false})
		case path == "/user/deployments":
			writeJSON(w, http.StatusOK, map[string]any{"items": []any{deployment}, "total": 1})
		case path == "/user/deployments/terminal-fault/events":
			writeJSON(w, http.StatusOK, map[string]any{"events": []any{}, "has_more": false})
		case path == "/user/deployments/terminal-fault/resources":
			writeJSON(w, http.StatusOK, map[string]any{})
		default:
			if strings.HasPrefix(path, "/user/") || !srv.serveUIAsset(w, r, path) {
				http.NotFound(w, r)
			}
		}
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	t.Cleanup(srv.terminals.Close)
	if err := os.WriteFile(readyFile, []byte(httpServer.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stop:
	case <-time.After(5 * time.Minute):
		t.Fatal("terminal browser harness exceeded five minutes")
	case <-t.Context().Done():
	}
}

type faultShellRecord struct {
	PID      uint32    `json:"pid"`
	Alive    bool      `json:"alive"`
	ExitedAt time.Time `json:"exited_at"`
}

type faultStreamRecord struct {
	SessionID   string `json:"session_id"`
	LastEventID string `json:"last_event_id"`
}

type faultPTY struct {
	cmd   *exec.Cmd
	stdin io.WriteCloser
	pid   uint32
	done  chan struct{}
	once  sync.Once
}

func startFaultPTY(python string) (*faultPTY, *bufio.Reader, error) {
	cmd := exec.Command(python, "-u", filepath.Join("testdata", "terminal_fault_pty.py"))
	cmd.Stderr = os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		return nil, nil, err
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	reader := bufio.NewReader(output)
	header, err := reader.ReadString('\n')
	pid, parseErr := strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(header, "PTY_PID:")), 10, 32)
	if err != nil || parseErr != nil || pid == 0 {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
		return nil, nil, fmt.Errorf("invalid test PTY handshake %q: read=%v parse=%v", header, err, parseErr)
	}
	return &faultPTY{cmd: cmd, stdin: input, pid: uint32(pid), done: make(chan struct{})}, reader, nil
}

func (p *faultPTY) PID() uint32 { return p.pid }

func (p *faultPTY) SendInput(_ context.Context, data []byte) error {
	_, err := p.stdin.Write(data)
	return err
}

func (p *faultPTY) Resize(context.Context, sandboxrunner.PtySize) error { return nil }

func (p *faultPTY) Close(ctx context.Context) error {
	p.once.Do(func() { _ = p.cmd.Process.Signal(syscall.SIGTERM) })
	select {
	case <-p.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
