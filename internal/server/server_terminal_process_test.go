package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type terminalFixtureRequest struct {
	ID, Tag string
	Ref     sandboxrunner.TerminalReference
	Cursor  uint64
	Data    []byte
	Size    sandboxrunner.PtySize
}
type remoteTerminalFixture struct {
	*fakeSandbox
	origin string
}

func (p *remoteTerminalFixture) call(ctx context.Context, operation string, in terminalFixtureRequest, out any) error {
	raw, _ := json.Marshal(in)
	req, err := http.NewRequestWithContext(ctx, "POST", p.origin+"/"+operation, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("fixture HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (p *remoteTerminalFixture) CreatePTY(ctx context.Context, id, tag string, size sandboxrunner.PtySize) (sandboxrunner.TerminalReference, uint64, error) {
	var ref sandboxrunner.TerminalReference
	err := p.call(ctx, "create", terminalFixtureRequest{ID: id, Tag: tag, Size: size}, &ref)
	return ref, 0, err
}

func (p *remoteTerminalFixture) InputPTY(ctx context.Context, id string, ref sandboxrunner.TerminalReference, data []byte) error {
	var ok bool
	return p.call(ctx, "input", terminalFixtureRequest{ID: id, Ref: ref, Data: data}, &ok)
}

func (p *remoteTerminalFixture) ResizePTY(ctx context.Context, id string, ref sandboxrunner.TerminalReference, size sandboxrunner.PtySize) error {
	var ok bool
	return p.call(ctx, "resize", terminalFixtureRequest{ID: id, Ref: ref, Size: size}, &ok)
}

func (p *remoteTerminalFixture) CleanupPTY(ctx context.Context, id, tag string, ref sandboxrunner.TerminalReference) (bool, error) {
	var ok bool
	err := p.call(ctx, "cleanup", terminalFixtureRequest{ID: id, Tag: tag, Ref: ref}, &ok)
	return ok, err
}

func (p *remoteTerminalFixture) ConnectPTY(ctx context.Context, id string, ref sandboxrunner.TerminalReference, after uint64) (sandboxrunner.TerminalStream, error) {
	raw, _ := json.Marshal(terminalFixtureRequest{ID: id, Ref: ref, Cursor: after})
	req, err := http.NewRequestWithContext(ctx, "POST", p.origin+"/events", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("fixture stream HTTP %d", resp.StatusCode)
	}
	return &remoteTerminalStream{body: resp.Body, decoder: json.NewDecoder(resp.Body)}, nil
}

type remoteTerminalStream struct {
	body    io.ReadCloser
	decoder *json.Decoder
}

func (s *remoteTerminalStream) Receive() (sandboxrunner.TerminalEvent, error) {
	var event sandboxrunner.TerminalEvent
	err := s.decoder.Decode(&event)
	return event, err
}
func (s *remoteTerminalStream) Close() error { return s.body.Close() }

func TestSharedTerminalChildProcess(t *testing.T) {
	ready := os.Getenv("TERMINAL_CHILD_READY")
	if ready == "" {
		t.Skip("child fixture")
	}
	store := state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: os.Getenv("TERMINAL_CHILD_DSN")})
	api := os.Getenv("TERMINAL_CHILD_GITHUB")
	cfg := config.Config{AuthEncryptionKey: "encryption-key", AuthSessionSecret: "session-secret", GitHubOAuthClientID: "test", GitHubAPIBaseURL: api}
	provider := &remoteTerminalFixture{fakeSandbox: &fakeSandbox{}, origin: os.Getenv("TERMINAL_CHILD_PROVIDER")}
	srv := New(cfg, store, github.NewClient(api, http.DefaultClient), provider, nil)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ready, []byte("http://"+listener.Addr().String()), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Fatal(http.Serve(listener, srv))
}

func TestSharedTerminalThreeProcessesPostgres(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL test database")
	}
	dsn := catalogServerTestDSN(t)
	store := state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: dsn, MigrateOnStart: true})
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}
		if r.URL.Path == "/user/installations/987/repositories" {
			fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"hubot/project"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	provider := &fakeSandbox{}
	bootstrap := newTestServerWithoutLoops(t, store, api.URL, provider, 1)
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, bootstrap.cfg.AuthEncryptionKey.Value(), "token")
	if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "User", AccountLogin: "hubot"}); err != nil {
		t.Fatal(err)
	}
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "process-terminal-job", GitHubInstallationID: 987, RepositoryFullName: "hubot/project", RunnerName: "fixture"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status, st.SandboxID = state.StatusRunning, "remote-sb"
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in terminalFixtureRequest
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, "bad fixture", 400)
			return
		}
		var out any = true
		var err error
		switch r.URL.Path {
		case "/create":
			out, _, err = provider.CreatePTY(r.Context(), in.ID, in.Tag, in.Size)
		case "/input":
			err = provider.InputPTY(r.Context(), in.ID, in.Ref, in.Data)
		case "/resize":
			err = provider.ResizePTY(r.Context(), in.ID, in.Ref, in.Size)
		case "/cleanup":
			out, err = provider.CleanupPTY(r.Context(), in.ID, in.Tag, in.Ref)
		case "/events":
			stream, e := provider.ConnectPTY(r.Context(), in.ID, in.Ref, in.Cursor)
			if e != nil {
				http.Error(w, "connect", 404)
				return
			}
			defer stream.Close()
			for {
				event, e := stream.Receive()
				if e != nil {
					return
				}
				if json.NewEncoder(w).Encode(event) != nil {
					return
				}
				if http.NewResponseController(w).Flush() != nil {
					return
				}
			}
		default:
			http.NotFound(w, r)
			return
		}
		if err != nil {
			http.Error(w, "provider failure", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	defer upstream.Close()
	nodes := make([]string, 3)
	children := make([]*exec.Cmd, 3)
	for i := range nodes {
		ready := filepath.Join(t.TempDir(), "ready")
		cmd := exec.Command(os.Args[0], "-test.run=^TestSharedTerminalChildProcess$", "-test.timeout=1m")
		cmd.Env = append(os.Environ(), "TERMINAL_CHILD_READY="+ready, "TERMINAL_CHILD_DSN="+dsn, "TERMINAL_CHILD_GITHUB="+api.URL, "TERMINAL_CHILD_PROVIDER="+upstream.URL)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		children[i] = cmd
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			data, err := os.ReadFile(ready)
			if err == nil {
				nodes[i] = string(data)
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if nodes[i] == "" {
			t.Fatal("child did not start")
		}
	}
	call := func(node int, method, path, body, cursor string) (int, string) {
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, method, nodes[node]+path, strings.NewReader(body))
		req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		if cursor != "" {
			req.Header.Set("Last-Event-ID", cursor)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(data)
	}
	base := "/user/runner_requests/process-terminal-job/terminal"
	status, body := call(0, "POST", base, `{}`, "")
	var result struct {
		ID string `json:"session_id"`
	}
	_ = json.Unmarshal([]byte(body), &result)
	if status != 201 || result.ID == "" {
		t.Fatalf("create %d %s", status, body)
	}
	if err := children[0].Process.Kill(); err != nil {
		t.Fatal(err)
	}
	path := base + "/" + result.ID
	status, body = call(1, "GET", path+"/events", "", "")
	if status != 200 || !strings.Contains(body, "terminal ready") {
		t.Fatalf("resume B %d %s", status, body)
	}
	provider.terminal.mu.Lock()
	cursor := len(provider.terminal.data)
	provider.terminal.data = append(provider.terminal.data, []byte("after kill")...)
	provider.terminal.mu.Unlock()
	status, body = call(2, "GET", path+"/events", "", fmt.Sprint(cursor))
	if status != 200 || strings.Contains(body, "terminal ready") || !strings.Contains(body, "after kill") {
		t.Fatalf("resume C %d %s", status, body)
	}
	if status, body := call(2, "POST", path+"/input", `{"data":"pwd\n"}`, ""); status != 202 {
		t.Fatalf("input %d %s", status, body)
	}
	if status, body := call(1, "DELETE", path, "", ""); status != 200 {
		t.Fatalf("cleanup %d %s", status, body)
	}
	if !provider.terminal.isClosed() || provider.terminals != 1 {
		t.Fatal("PTY recreated or leaked")
	}
}
