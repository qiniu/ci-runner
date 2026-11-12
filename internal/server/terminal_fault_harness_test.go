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
// are fixtures; the WebSocket handler, database lifecycle and OS PTY are real.
func TestTerminalFaultHarness(t *testing.T) {
	readyFile := os.Getenv("RUNNERD_TERMINAL_FAULT_READY_FILE")
	if readyFile == "" {
		t.Skip("run task ui-terminal-fault-test to start the browser harness")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("terminal fault tests require Python 3 with the Unix pty module")
	}
	fixture := newTerminalFixture(t, "deployment")
	srv := fixture.srv
	var mu sync.Mutex
	records := map[string]*faultShellRecord{}
	creates := 0
	fixture.srv.sandbox = &faultSandbox{fakeSandbox: fixture.fake, start: func(ctx context.Context, input sandboxrunner.TerminalStart, onData func([]byte)) (sandboxrunner.TerminalSession, error) {
		pty, output, err := startFaultPTY(python)
		if err != nil {
			return nil, err
		}
		mu.Lock()
		creates++
		record := &faultShellRecord{PID: pty.pid, Alive: true}
		records[input.ID] = record
		mu.Unlock()
		go func() {
			buffer := make([]byte, 4096)
			for {
				n, err := output.Read(buffer)
				if n > 0 {
					onData(buffer[:n])
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
		return pty, nil
	}}
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
			writeJSON(w, http.StatusOK, map[string]any{"creates": creates, "records": records})
		case path == "/auth/session":
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "oauth_enabled": true, "login": "fixture", "role": "user"})
		case path == "/sandbox/regions":
			writeJSON(w, http.StatusOK, []any{})
		case path == "/user/deployments/terminal-fault/terminal" && r.Method == http.MethodGet:
			r.URL.Path = "/user/deployments/terminal-deploy/revisions/1/terminal"
			r.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
			srv.ServeHTTP(w, r)
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

type faultSandbox struct {
	*fakeSandbox
	start func(context.Context, sandboxrunner.TerminalStart, func([]byte)) (sandboxrunner.TerminalSession, error)
}

func (s *faultSandbox) StartTerminal(ctx context.Context, input sandboxrunner.TerminalStart, output func([]byte)) (sandboxrunner.TerminalSession, error) {
	return s.start(ctx, input, output)
}
func (p *faultPTY) Done() <-chan struct{}           { return p.done }
func (p *faultPTY) KeepAlive(context.Context) error { return nil }
