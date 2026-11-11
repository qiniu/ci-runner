package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

// This registry models the independent Sandbox provider, not a runnerd node.
func (f *fakeSandbox) CreatePTY(ctx context.Context, id, tag string, size sandboxrunner.PtySize) (sandboxrunner.TerminalReference, uint64, error) {
	term := &fakeTerminalSession{pid: 99, streamID: newID(), data: []byte("terminal ready\n")}
	f.mu.Lock()
	f.terminals++
	f.terminal = term
	if f.replayPTYs == nil {
		f.replayPTYs = map[string]*fakeTerminalSession{}
	}
	f.replayPTYs[tag] = term
	f.mu.Unlock()
	if f.onTerminalOpen != nil {
		f.onTerminalOpen(ctx, func(data []byte) { term.mu.Lock(); defer term.mu.Unlock(); term.data = append(term.data, data...) })
	}
	return sandboxrunner.TerminalReference{PID: term.pid, StreamID: term.streamID}, 0, nil
}

func (f *fakeSandbox) replayPTY(ref sandboxrunner.TerminalReference) (*fakeTerminalSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, term := range f.replayPTYs {
		if term.pid == ref.PID && term.streamID == ref.StreamID {
			return term, nil
		}
	}
	return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("missing PTY"))
}

func (f *fakeSandbox) ConnectPTY(ctx context.Context, id string, ref sandboxrunner.TerminalReference, after uint64) (sandboxrunner.TerminalStream, error) {
	term, err := f.replayPTY(ref)
	if err != nil {
		return nil, err
	}
	term.mu.Lock()
	valid := after <= uint64(len(term.data))
	term.mu.Unlock()
	if !valid {
		return nil, connect.NewError(connect.CodeOutOfRange, fmt.Errorf("gap"))
	}
	return &fakeReplayStream{ctx: ctx, term: term, cursor: after}, nil
}

func (f *fakeSandbox) InputPTY(ctx context.Context, id string, ref sandboxrunner.TerminalReference, data []byte) error {
	term, err := f.replayPTY(ref)
	if err != nil {
		return err
	}
	return term.SendInput(ctx, data)
}

func (f *fakeSandbox) ResizePTY(ctx context.Context, id string, ref sandboxrunner.TerminalReference, size sandboxrunner.PtySize) error {
	term, err := f.replayPTY(ref)
	if err != nil {
		return err
	}
	return term.Resize(ctx, size)
}

func (f *fakeSandbox) CleanupPTY(ctx context.Context, id, tag string, ref sandboxrunner.TerminalReference) (bool, error) {
	var term *fakeTerminalSession
	if ref.PID == 0 {
		f.mu.Lock()
		term = f.replayPTYs[tag]
		f.mu.Unlock()
	} else {
		term, _ = f.replayPTY(ref)
	}
	if term == nil {
		return ref.PID != 0, nil
	}
	return true, term.Close(ctx)
}

type fakeReplayStream struct {
	ctx    context.Context
	term   *fakeTerminalSession
	cursor uint64
}

func (s *fakeReplayStream) Close() error { return nil }
func (s *fakeReplayStream) Receive() (sandboxrunner.TerminalEvent, error) {
	for {
		s.term.mu.Lock()
		if s.cursor < uint64(len(s.term.data)) {
			data := append([]byte(nil), s.term.data[s.cursor:]...)
			s.cursor += uint64(len(data))
			s.term.mu.Unlock()
			return sandboxrunner.TerminalEvent{Data: data, Offset: s.cursor}, nil
		}
		s.term.mu.Unlock()
		select {
		case <-s.ctx.Done():
			return sandboxrunner.TerminalEvent{}, s.ctx.Err()
		case <-time.After(20 * time.Millisecond):
			return sandboxrunner.TerminalEvent{Heartbeat: true, Offset: s.cursor}, nil
		}
	}
}

func terminalTestCall(t *testing.T, s *Server, subject, method, path, body, cursor string) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Millisecond)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
	if subject != "" {
		req.AddCookie(testSessionCookie(subject, subject, "user"))
	}
	if cursor != "" {
		req.Header.Set("Last-Event-ID", cursor)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec
}

func TestSharedTerminalAcrossThreeNodes(t *testing.T) {
	for _, kind := range []string{"runner", "deployment"} {
		t.Run(kind, func(t *testing.T) {
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
			defer api.Close()
			dir := t.TempDir()
			stores := []state.Store{state.New(dir), state.New(dir), state.New(dir)}
			provider := &fakeSandbox{}
			nodes := []*Server{}
			for _, store := range stores {
				nodes = append(nodes, newTestServerWithoutLoops(t, store, api.URL, provider, 1))
			}
			a, b, c := nodes[0], nodes[1], nodes[2]
			account, _, err := stores[0].GetAccountByOAuthIdentity("github", "hubot-id")
			if err != nil {
				t.Fatal(err)
			}
			for _, subject := range []string{"hubot-id", "peer-id"} {
				acct, _, err := stores[0].EnsureAccountForOAuthIdentity(state.OAuthIdentity{OAuthProvider: "github", OAuthSubject: subject, OAuthLogin: subject}, "user")
				if err != nil {
					t.Fatal(err)
				}
				saveTestGitHubOAuthToken(t, stores[0], acct.ID, a.cfg.AuthEncryptionKey.Value(), "token")
				if _, err := stores[0].UpsertGitHubInstallation(state.GitHubInstallation{AccountID: acct.ID, InstallationID: 987, AccountType: "User", AccountLogin: "hubot"}); err != nil {
					t.Fatal(err)
				}
			}
			base := ""
			var changed func()
			if kind == "runner" {
				_, st, err := stores[0].CreateRequest(state.RunnerRequest{ID: "job", GitHubInstallationID: 987, RepositoryFullName: "hubot/project", RunnerName: "runner"}, nil)
				if err != nil {
					t.Fatal(err)
				}
				st.Status, st.SandboxID = state.StatusRunning, "sb"
				if err := stores[0].WriteState(st); err != nil {
					t.Fatal(err)
				}
				base = "/user/runner_requests/job/terminal"
				changed = func() {
					current, _ := stores[0].ReadState("job")
					current.LastAttemptAt = current.LastAttemptAt.Add(time.Second)
					if err := stores[0].WriteState(current); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				d, err := stores[0].CreateDeployment(state.Deployment{ID: "deploy", RepositoryID: 42, CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "encrypted", AccountID: account.ID, ScopeType: state.AccountScopeTypeAccount, ScopeID: account.ID, GitHubInstallationID: 987, RepositoryFullName: "hubot/project", Ref: "refs/heads/main", Status: state.DeploymentStatusRunning, SandboxID: "sb", ExpiresAt: time.Now().Add(time.Hour)})
				if err != nil {
					t.Fatal(err)
				}
				base = fmt.Sprintf("/user/deployments/deploy/revisions/%d/terminal", d.RevisionNumber)
				changed = func() {
					current, _ := stores[0].GetDeployment(d.ID)
					current.Attempt++
					if err := stores[0].UpdateDeployment(current); err != nil {
						t.Fatal(err)
					}
				}
			}
			created := terminalTestCall(t, a, "hubot-id", "POST", base, `{}`, "")
			var result struct {
				ID string `json:"session_id"`
			}
			_ = json.Unmarshal(created.Body.Bytes(), &result)
			if created.Code != 201 || result.ID == "" {
				t.Fatalf("create %d %s", created.Code, created.Body.String())
			}
			path := base + "/" + result.ID
			a.Close() // The creation node is gone. B and C have only the shared database.
			if provider.terminal.isClosed() {
				t.Fatal("node shutdown killed PTY")
			}
			first := terminalTestCall(t, b, "hubot-id", "GET", path+"/events", "", "")
			if first.Code != 200 || !strings.Contains(first.Body.String(), "terminal ready") {
				t.Fatalf("node B replay %d %s", first.Code, first.Body.String())
			}
			provider.terminal.mu.Lock()
			offset := len(provider.terminal.data)
			provider.terminal.data = append(provider.terminal.data, []byte("after node A exit\n")...)
			provider.terminal.mu.Unlock()
			resumed := terminalTestCall(t, c, "hubot-id", "GET", path+"/events", "", fmt.Sprint(offset))
			if resumed.Code != 200 || strings.Contains(resumed.Body.String(), "terminal ready") || !strings.Contains(resumed.Body.String(), "after node A exit") {
				t.Fatalf("node C replay %d %s", resumed.Code, resumed.Body.String())
			}
			if got := terminalTestCall(t, c, "hubot-id", "POST", path+"/input", `{"data":"pwd\n"}`, ""); got.Code != 202 {
				t.Fatalf("input %d %s", got.Code, got.Body.String())
			}
			for _, operation := range []struct{ method, suffix string }{{"GET", "/events"}, {"POST", "/input"}, {"POST", "/resize"}, {"DELETE", ""}} {
				if got := terminalTestCall(t, b, "peer-id", operation.method, path+operation.suffix, `{}`, ""); got.Code != 404 {
					t.Fatalf("foreign account %s %d", operation.suffix, got.Code)
				}
			}
			gap := terminalTestCall(t, b, "hubot-id", "GET", path+"/events", "", "999999")
			if !strings.Contains(gap.Body.String(), "terminal-closed") {
				t.Fatal("gap did not fail explicitly")
			}
			revoked.Store(true)
			ageAccessSnapshot(t, dir, account.ID, true)
			if got := terminalTestCall(t, b, "hubot-id", "POST", path+"/input", `{}`, ""); got.Code != 404 {
				t.Fatal("revocation accepted input", got.Code)
			}
			revoked.Store(false)
			ageAccessSnapshot(t, dir, account.ID, true)
			changed()
			if got := terminalTestCall(t, c, "hubot-id", "POST", path+"/input", `{}`, ""); got.Code == 202 {
				t.Fatal("old attempt accepted input")
			}
			if err := c.closeSharedTerminal(t.Context(), result.ID); err != nil {
				t.Fatal(err)
			}
			if !provider.terminal.isClosed() {
				t.Fatal("other node failed to clean PTY")
			}
			row, err := stores[1].GetTerminal(t.Context(), result.ID)
			if err != nil || row.Status != "closed" {
				t.Fatalf("durable cleanup %+v %v", row, err)
			}
		})
	}
}

func TestSharedTerminalActiveStreamRevokesAccess(t *testing.T) {
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
	defer api.Close()
	dir := t.TempDir()
	store := state.New(dir)
	provider := &fakeSandbox{}
	srv := newTestServerWithoutLoops(t, store, api.URL, provider, 1)
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "token")
	if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "User", AccountLogin: "hubot"}); err != nil {
		t.Fatal(err)
	}
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "active-terminal", GitHubInstallationID: 987, RepositoryFullName: "hubot/project", RunnerName: "fixture"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status, st.SandboxID = state.StatusRunning, "sb"
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	base := "/user/runner_requests/active-terminal/terminal"
	created := terminalTestCall(t, srv, "hubot-id", "POST", base, `{}`, "")
	var result struct {
		ID string `json:"session_id"`
	}
	_ = json.Unmarshal(created.Body.Bytes(), &result)
	if created.Code != 201 {
		t.Fatal(created.Body.String())
	}
	server := httptest.NewServer(srv)
	defer server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", server.URL+base+"/"+result.ID+"/events", nil)
	req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	revoked.Store(true)
	ageAccessSnapshot(t, dir, account.ID, true)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "terminal-closed") {
		t.Fatal("attached stream survived revoked access")
	}
	srv.reconcileTerminals(t.Context())
	row, err := store.GetTerminal(t.Context(), result.ID)
	if err != nil || row.Status != "closed" || !provider.terminal.isClosed() {
		t.Fatal("revocation did not persist and complete cleanup", err, row.Status)
	}
}
