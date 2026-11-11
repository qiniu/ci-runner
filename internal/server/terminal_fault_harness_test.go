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
	"github.com/qiniu/ci-runner/internal/state"
)

// This fixture is opt-in and loopback-only. Authentication and deployment data
// are fixtures; SSE, replay, idle cleanup and the OS PTY are real.
func TestTerminalFaultHarness(t *testing.T) {
	readyFile := os.Getenv("RUNNERD_TERMINAL_FAULT_READY_FILE")
	if readyFile == "" {
		t.Skip("run task ui-terminal-fault-test")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}
		if r.URL.Path == "/user/installations/987/repositories" {
			fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"fixture/project"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	store := state.New(t.TempDir())
	provider := &faultReplayProvider{python: python, shells: map[string]*faultPTY{}, records: map[string]*faultShellRecord{}}
	srv := newTestServerWithoutLoops(t, store, api.URL, &provider.fakeSandbox, 1)
	srv.sandbox = provider
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "token")
	if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "User", AccountLogin: "hubot"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateDeployment(state.Deployment{ID: "terminal-fault", AccountID: account.ID, ScopeType: state.AccountScopeTypeAccount, ScopeID: account.ID, GitHubInstallationID: 987, RepositoryID: 42, RepositoryFullName: "fixture/project", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "encrypted", Status: state.DeploymentStatusRunning, SandboxID: "local-pty", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	srv.loopWG.Add(1)
	go srv.terminalCleanupLoop(srv.loopCtx)
	stop := make(chan struct{})
	var stopOnce sync.Once
	deployment := map[string]any{"id": "terminal-fault", "repository_full_name": "fixture/project", "ref": "refs/heads/main", "commit_sha": strings.Repeat("a", 40), "status": "running", "sandbox_id": "local-pty", "revision_number": 1, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339), "revisions": []map[string]any{{"number": 1, "status": "running", "ref": "refs/heads/main", "commit_sha": strings.Repeat("a", 40)}}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.Replace(r.URL.Path, "/revisions/1", "", 1)
		switch {
		case path == "/__stop":
			stopOnce.Do(func() { close(stop) })
			writeJSON(w, 200, map[string]string{"status": "ok"})
		case path == "/__metrics":
			provider.fixtureMu.Lock()
			defer provider.fixtureMu.Unlock()
			writeJSON(w, 200, map[string]any{"creates": provider.creates, "deletes": provider.deletes, "records": provider.records, "streams": provider.streams})
		case path == "/auth/session":
			writeJSON(w, 200, map[string]any{"authenticated": true, "oauth_enabled": true, "login": "fixture", "role": "user"})
		case path == "/sandbox/regions":
			writeJSON(w, 200, []any{})
		case strings.HasPrefix(path, "/user/deployments/terminal-fault/terminal"):
			if strings.Contains(path, "/terminal/") {
				parts := strings.Split(strings.SplitN(path, "/terminal/", 2)[1], "/")
				provider.fixtureMu.Lock()
				if r.Method == "DELETE" {
					provider.deletes++
				}
				if len(parts) == 2 && parts[1] == "events" {
					provider.streams = append(provider.streams, faultStreamRecord{SessionID: parts[0], LastEventID: r.Header.Get("Last-Event-ID")})
				}
				provider.fixtureMu.Unlock()
			}
			if !strings.Contains(r.URL.Path, "/revisions/") {
				r.URL.Path = strings.Replace(r.URL.Path, "/terminal", "/revisions/1/terminal", 1)
			}
			r.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
			srv.ServeHTTP(w, r)
		case path == "/user/deployments/terminal-fault":
			writeJSON(w, 200, deployment)
		case path == "/user/deployment-repositories":
			writeJSON(w, 200, map[string]any{"items": []any{map[string]any{"repository_full_name": "fixture/project", "total": 1, "active_count": 1, "active_deployments": []any{deployment}}}, "has_more": false})
		case path == "/user/deployments":
			writeJSON(w, 200, map[string]any{"items": []any{deployment}, "total": 1})
		case path == "/user/deployments/terminal-fault/events":
			writeJSON(w, 200, map[string]any{"events": []any{}, "has_more": false})
		case path == "/user/deployments/terminal-fault/resources":
			writeJSON(w, 200, map[string]any{})
		default:
			if strings.HasPrefix(path, "/user/") || !srv.serveUIAsset(w, r, path) {
				http.NotFound(w, r)
			}
		}
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	defer func() {
		provider.fixtureMu.Lock()
		shells := make([]*faultPTY, 0, len(provider.shells))
		for _, shell := range provider.shells {
			shells = append(shells, shell)
		}
		provider.fixtureMu.Unlock()
		for _, shell := range shells {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = shell.Close(ctx)
			cancel()
		}
	}()
	if err := os.WriteFile(readyFile, []byte(server.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stop:
	case <-time.After(5 * time.Minute):
		t.Fatal("terminal harness timed out")
	case <-t.Context().Done():
	}
}

type faultReplayProvider struct {
	fakeSandbox
	fixtureMu        sync.Mutex
	python           string
	shells           map[string]*faultPTY
	records          map[string]*faultShellRecord
	streams          []faultStreamRecord
	creates, deletes int
}

func (p *faultReplayProvider) CreatePTY(ctx context.Context, id, tag string, size sandboxrunner.PtySize) (sandboxrunner.TerminalReference, uint64, error) {
	shell, output, err := startFaultPTY(p.python)
	if err != nil {
		return sandboxrunner.TerminalReference{}, 0, err
	}
	term := &fakeTerminalSession{pid: shell.pid, streamID: newID()}
	p.mu.Lock()
	if p.replayPTYs == nil {
		p.replayPTYs = map[string]*fakeTerminalSession{}
	}
	p.replayPTYs[tag] = term
	p.mu.Unlock()
	record := &faultShellRecord{PID: shell.pid, Alive: true}
	p.fixtureMu.Lock()
	p.creates++
	p.shells[term.streamID] = shell
	p.records[strings.TrimPrefix(tag, "runnerd-terminal-")] = record
	p.fixtureMu.Unlock()
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := output.Read(buffer)
			if n > 0 {
				term.mu.Lock()
				term.data = append(term.data, buffer[:n]...)
				term.mu.Unlock()
			}
			if err != nil {
				break
			}
		}
		_ = shell.cmd.Wait()
		p.fixtureMu.Lock()
		record.Alive = false
		record.ExitedAt = time.Now()
		p.fixtureMu.Unlock()
		close(shell.done)
	}()
	return sandboxrunner.TerminalReference{PID: shell.pid, StreamID: term.streamID}, 0, nil
}

func (p *faultReplayProvider) InputPTY(ctx context.Context, id string, ref sandboxrunner.TerminalReference, data []byte) error {
	p.fixtureMu.Lock()
	shell := p.shells[ref.StreamID]
	p.fixtureMu.Unlock()
	if shell == nil || shell.pid != ref.PID {
		return fmt.Errorf("missing PTY")
	}
	return shell.SendInput(ctx, data)
}

func (p *faultReplayProvider) CleanupPTY(ctx context.Context, id, tag string, ref sandboxrunner.TerminalReference) (bool, error) {
	p.fixtureMu.Lock()
	shell := p.shells[ref.StreamID]
	p.fixtureMu.Unlock()
	if shell == nil {
		return false, nil
	}
	return true, shell.Close(ctx)
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
