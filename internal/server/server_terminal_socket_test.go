package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/qiniu/ci-runner/internal/state"
)

type terminalFixture struct {
	srv     *Server
	http    *httptest.Server
	store   state.Store
	fake    *fakeSandbox
	account int64
	dir     string
	revoked *atomic.Bool
	path    string
}

func newTerminalFixture(t *testing.T, kind string) terminalFixture {
	return newTerminalFixtureStore(t, kind, nil)
}

func newTerminalFixtureStore(t *testing.T, kind string, selected state.Store) terminalFixture {
	t.Helper()
	var revoked atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}
		if r.URL.Path == "/user/installations/987/repositories" {
			if revoked.Load() {
				fmt.Fprint(w, `{"repositories":[]}`)
			} else {
				fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"hubot/project"}]}`)
			}
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(api.Close)
	dir := t.TempDir()
	store := selected
	if store == nil {
		store = state.New(dir)
	}
	fake := &fakeSandbox{}
	srv := newTestServerWithoutLoops(t, store, api.URL, fake, 10)
	t.Cleanup(srv.Close)
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
	if _, err = store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "User", AccountLogin: "hubot"}); err != nil {
		t.Fatal(err)
	}
	path := "/user/runner_requests/terminal-runner/terminal"
	if kind == "runner" {
		_, st, err := store.CreateRequest(state.RunnerRequest{ID: "terminal-runner", GitHubInstallationID: 987, RepositoryFullName: "hubot/project", RunnerName: "test"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		st.Status, st.SandboxID = state.StatusRunning, "original-sandbox"
		if err = store.WriteState(st); err != nil {
			t.Fatal(err)
		}
	} else {
		_, err := store.CreateDeployment(state.Deployment{ID: "terminal-deploy", AccountID: account.ID + 10, ScopeType: state.AccountScopeTypeAccount, ScopeID: account.ID, GitHubInstallationID: 987, RepositoryID: 42, RepositoryFullName: "hubot/project", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "encrypted", Status: state.DeploymentStatusRunning, SandboxID: "sandbox-deploy", ExpiresAt: time.Now().Add(time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		path = "/user/deployments/terminal-deploy/revisions/1/terminal"
	}
	httpServer := httptest.NewServer(srv)
	t.Cleanup(httpServer.Close)
	return terminalFixture{srv, httpServer, store, fake, account.ID, dir, &revoked, path}
}

func (f terminalFixture) dial(t *testing.T) *websocket.Conn {
	t.Helper()
	c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.http.URL, "http")+f.path+"?cols=100&rows=28", http.Header{"Origin": {f.http.URL}, "Cookie": {testSessionCookie("hubot-id", "hubot", "user").String()}})
	if err != nil {
		t.Fatalf("dial: %v response=%v", err, resp)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func readTerminalReady(t *testing.T, c *websocket.Conn) string {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var frame struct {
		Type, SessionID string
		ID              string `json:"session_id"`
	}
	if err := c.ReadJSON(&frame); err != nil || frame.Type != "ready" || frame.ID == "" {
		t.Fatalf("ready: %+v %v", frame, err)
	}
	return frame.ID
}

func waitTerminalClosed(t *testing.T, f terminalFixture, id string) state.TerminalSession {
	t.Helper()
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		row, err := f.store.GetTerminalSession(context.Background(), id)
		if err == nil && row.Status == "closed" {
			return row
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("terminal record did not close")
	return state.TerminalSession{}
}

func TestTerminalSocketLifecycle(t *testing.T) {
	for _, kind := range []string{"runner", "deployment"} {
		t.Run(kind, func(t *testing.T) {
			f := newTerminalFixture(t, kind)
			c := f.dial(t)
			id := readTerminalReady(t, c)
			row, err := f.store.GetTerminalSession(context.Background(), id)
			if err != nil || row.AccountID != f.account || row.NodeID != f.srv.workerID || row.ResourceKind != kind || row.GitHubInstallationID != 987 || row.RepositoryFullName != "hubot/project" {
				t.Fatalf("ownership %+v %v", row, err)
			}
			var out struct {
				Type string
				Data []byte
			}
			if err = c.ReadJSON(&out); err != nil || string(out.Data) != "terminal ready\n" {
				t.Fatalf("output %+v %v", out, err)
			}
			_ = c.WriteJSON(terminalFrame{Type: "input", Data: "echo first\n"})
			_ = c.WriteJSON(terminalFrame{Type: "resize", Rows: 41, Cols: 121})
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				f.fake.terminal.mu.Lock()
				ok := f.fake.terminal.input == "echo first\n" && f.fake.terminal.resized.Rows == 41
				f.fake.terminal.mu.Unlock()
				if ok {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			f.fake.terminal.mu.Lock()
			input, size := f.fake.terminal.input, f.fake.terminal.resized
			f.fake.terminal.mu.Unlock()
			if input != "echo first\n" || size.Cols != 121 {
				t.Fatalf("input/resize %q %+v", input, size)
			}
			_ = c.Close()
			closed := waitTerminalClosed(t, f, id)
			if closed.Cleanup != "confirmed" {
				t.Fatalf("close %+v", closed)
			}
			next := f.dial(t)
			nextID := readTerminalReady(t, next)
			if nextID == id {
				t.Fatal("resumed old session")
			}
			_ = next.Close()
			waitTerminalClosed(t, f, nextID)
			if kind == "runner" {
				st, _ := f.store.ReadState("terminal-runner")
				if st.Status != state.StatusRunning {
					t.Fatal("console changed job")
				}
			} else {
				d, _ := f.store.GetDeployment("terminal-deploy")
				if d.Status != state.DeploymentStatusRunning {
					t.Fatal("console changed deployment")
				}
			}
		})
	}
}

func TestTerminalSocketRejectsUntrustedRequests(t *testing.T) {
	f := newTerminalFixture(t, "runner")
	for _, tc := range []struct {
		origin, cookie, path string
		status               int
	}{
		{"https://evil.invalid", testSessionCookie("hubot-id", "hubot", "user").String(), f.path, 403},
		{"", testSessionCookie("hubot-id", "hubot", "user").String(), f.path, 403},
		{f.http.URL, "", f.path, 401},
		{f.http.URL, testSessionCookie("12345", "octocat", "admin").String(), f.path, 404},
	} {
		c, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(f.http.URL, "http")+tc.path+"?cols=100&rows=28", http.Header{"Origin": {tc.origin}, "Cookie": {tc.cookie}})
		if c != nil {
			c.Close()
			t.Fatal("unauthorized socket opened")
		}
		if err == nil || resp == nil || resp.StatusCode != tc.status {
			t.Fatalf("status want %d got %v %v", tc.status, resp, err)
		}
		resp.Body.Close()
	}
	f.fake.mu.Lock()
	defer f.fake.mu.Unlock()
	if f.fake.terminals != 0 {
		t.Fatal("unauthorized provider call")
	}
}

func TestTerminalSocketRevocationAndAttemptFencing(t *testing.T) {
	for _, kind := range []string{"runner", "deployment"} {
		for _, change := range []string{"attempt", "repository", "revoke", "shutdown", "invalid frame"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				f := newTerminalFixture(t, kind)
				c := f.dial(t)
				id := readTerminalReady(t, c)
				switch change {
				case "shutdown":
					f.srv.Close()
				case "revoke":
					f.revoked.Store(true)
					ageAccessSnapshot(t, f.dir, f.account, true)
				case "invalid frame":
					_ = c.WriteMessage(websocket.TextMessage, []byte(`{"type":"input","session_id":"old","data":"must not run"}`))
				default:
					if kind == "runner" {
						st, _ := f.store.ReadState("terminal-runner")
						if change == "attempt" {
							st.LastAttemptAt = st.LastAttemptAt.Add(time.Second)
						} else {
							st.RepositoryFullName = "hubot/other"
						}
						if err := f.store.WriteState(st); err != nil {
							t.Fatal(err)
						}
					} else {
						d, _ := f.store.GetDeployment("terminal-deploy")
						if change == "attempt" {
							d.Attempt++
						} else {
							d.SandboxID = "replaced-sandbox"
						}
						if err := f.store.UpdateDeployment(d); err != nil {
							t.Fatal(err)
						}
					}
				}
				if change != "shutdown" && change != "invalid frame" {
					_ = c.WriteJSON(terminalFrame{Type: "input", Data: "must not run"})
				}
				closed := waitTerminalClosed(t, f, id)
				if closed.Cleanup != "confirmed" {
					t.Fatalf("cleanup %+v", closed)
				}
				f.fake.terminal.mu.Lock()
				defer f.fake.terminal.mu.Unlock()
				if f.fake.terminal.input != "" {
					t.Fatal("stale input accepted")
				}
			})
		}
	}
}

func TestTerminalFrameLimits(t *testing.T) {
	for _, raw := range []string{`{"type":"resize","rows":0,"cols":80}`, `{"type":"resize","rows":501,"cols":80}`, `{"type":"close"}`, `{"type":"input"} {}`, `{"type":"input","data":"` + strings.Repeat("a", 4097) + `"}`} {
		if _, err := decodeTerminalFrame([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw[:min(len(raw), 100)])
		}
	}
	if _, err := decodeTerminalFrame([]byte(`{"type":"input","data":"pwd\n"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestTerminalSocketCreationRaceAndUnknownCleanup(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		t.Run(fmt.Sprint(unknown), func(t *testing.T) {
			f := newTerminalFixture(t, "runner")
			f.fake.onTerminalOpen = func(ctx context.Context, _ func([]byte)) {
				if unknown {
					f.fake.terminal.closeError = fmt.Errorf("unknown remote close")
				} else {
					st, _ := f.store.ReadState("terminal-runner")
					st.SandboxID = "replacement"
					if err := f.store.WriteState(st); err != nil {
						t.Error(err)
					}
				}
			}
			c := f.dial(t)
			if unknown {
				id := readTerminalReady(t, c)
				c.Close()
				row := waitTerminalClosed(t, f, id)
				if row.Cleanup != "unknown" {
					t.Fatal("invented cleanup success")
				}
			} else {
				_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
				if _, _, err := c.ReadMessage(); err == nil {
					t.Fatal("stale creation exposed")
				}
				if !f.fake.terminal.isClosed() {
					t.Fatal("stale shell not closed")
				}
			}
		})
	}
}

func TestDeploymentTerminalSocketSavedCredentialsAndLifetime(t *testing.T) {
	f := newTerminalFixture(t, "deployment")
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-API-Key") != "saved-key" {
			t.Error("saved credential not used")
		}
		var input struct {
			Timeout int32 `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if input.Timeout < 1190 || input.Timeout > 1200 {
			t.Errorf("lifetime changed: %d", input.Timeout)
		}
		w.WriteHeader(400)
	}))
	defer provider.Close()
	d, _ := f.store.GetDeployment("terminal-deploy")
	d.SandboxAPIURL = provider.URL
	d.SandboxAPIKeyEncrypted, _ = encryptSecret("saved-key", f.srv.cfg.AuthEncryptionKey.Value())
	d.ExpiresAt = time.Now().Add(20 * time.Minute)
	if err := f.store.UpdateDeployment(d); err != nil {
		t.Fatal(err)
	}
	c := f.dial(t)
	var frame map[string]any
	if err := c.ReadJSON(&frame); err != nil || frame["type"] != "error" {
		t.Fatalf("failed provider: %v %v", frame, err)
	}
	c.Close()
	if calls.Load() != 1 || f.fake.terminals != 0 {
		t.Fatal("fell back from saved provider")
	}
	d, _ = f.store.GetDeployment(d.ID)
	d.SandboxAPIKeyEncrypted = "invalid"
	if err := f.store.UpdateDeployment(d); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", f.path+"?cols=100&rows=28", nil)
	r.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	if w.Code != 409 || calls.Load() != 1 {
		t.Fatalf("invalid saved credential %d", w.Code)
	}
}
