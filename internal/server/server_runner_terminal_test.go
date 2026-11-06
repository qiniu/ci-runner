package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestUserRunnerTerminalRevalidatesAttachedSession(t *testing.T) {
	for _, change := range []string{"authorized", "completed", "sandbox replaced", "attempt replaced", "repository changed", "revoked", "token rejected", "GitHub unavailable"} {
		t.Run(change, func(t *testing.T) {
			t.Parallel()
			var changed atomic.Bool
			var accessChecks atomic.Int32
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveActiveInstallationFixture(w, r, 987) {
					return
				}
				if r.URL.Path != "/user/installations/987/repositories" {
					http.NotFound(w, r)
					return
				}
				accessChecks.Add(1)
				if changed.Load() {
					switch change {
					case "revoked":
						fmt.Fprint(w, `{"repositories":[{"full_name":"hubot/other"}]}`)
						return
					case "token rejected":
						w.WriteHeader(http.StatusUnauthorized)
						return
					case "GitHub unavailable":
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
				}
				fmt.Fprint(w, `{"repositories":[{"full_name":"hubot/project"}]}`)
			}))
			defer api.Close()
			store := state.New(t.TempDir())
			fake := &fakeSandbox{}
			srv := newTestServer(t, store, api.URL, fake)
			srv.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			srv.loopCtx = ctx
			srv.terminals = newTerminalHub(nil)
			defer srv.terminals.Close()
			account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
			if err != nil {
				t.Fatal(err)
			}
			saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
			if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "User", AccountLogin: "hubot"}); err != nil {
				t.Fatal(err)
			}
			_, st, err := store.CreateRequest(state.RunnerRequest{ID: "terminal-runner", GitHubInstallationID: 987, RepositoryFullName: "hubot/project", RunnerName: "test-runner"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			st.Status, st.SandboxID = state.StatusRunning, "original-sandbox"
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/user/runner_requests/"+st.ID+"/terminal", strings.NewReader(`{}`))
			req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != http.StatusCreated {
				t.Fatalf("create terminal: %d %s", rec.Code, rec.Body.String())
			}
			var result struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			session, ok := srv.terminals.Get(result.SessionID)
			if !ok {
				t.Fatal("terminal was not retained")
			}
			_, output, unsubscribe := session.Subscribe()
			defer unsubscribe()
			// An attached, actively producing shell must still be revoked.
			session.append([]byte("before change"))
			if string(<-output) != "before change" {
				t.Fatal("attached session lost output")
			}
			st, err = store.ReadState(st.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "completed":
				st.Status = state.StatusCompleted
			case "sandbox replaced":
				st.SandboxID = "replacement-sandbox"
			case "attempt replaced":
				st.LastAttemptAt = st.LastAttemptAt.Add(time.Second)
			case "repository changed":
				st.RepositoryFullName = "hubot/other"
			}
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			changed.Store(true)
			srv.invalidateUserRepositoryAccess(account.ID)
			wait := 45 * time.Second
			switch change {
			case "completed", "sandbox replaced", "attempt replaced", "repository changed":
				wait = 15 * time.Second
			case "authorized":
				wait = 35 * time.Second
			}
			timeout := time.NewTimer(wait)
			defer timeout.Stop()
			produce := time.NewTicker(50 * time.Millisecond)
			defer produce.Stop()
			for {
				select {
				case <-timeout.C:
					if change == "authorized" && accessChecks.Load() >= 2 && !fake.terminal.isClosed() {
						return
					}
					t.Fatal("attached runner terminal survived lifecycle/access change")
				case <-produce.C:
					session.append([]byte("still producing"))
				case _, open := <-output:
					if open {
						continue
					}
					if change == "authorized" {
						t.Fatal("authorized active terminal was closed")
					}
					// CloseSession removes the hub entry before closing the PTY.
					deadline := time.Now().Add(time.Second)
					for !fake.terminal.isClosed() && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					if !fake.terminal.isClosed() {
						t.Fatal("revocation did not close the remote shell")
					}
					if _, ok := srv.terminals.Get(result.SessionID); ok {
						t.Fatal("revoked session remains in the hub")
					}
					return
				}
			}
		})
	}
}

func TestRunnerTerminalBelongsToOpeningAccountAndAttempt(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}
		if r.URL.Path == "/user/installations/987/repositories" {
			fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"hubot/project"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer gh.Close()
	store := state.New(t.TempDir())
	fake := &fakeSandbox{}
	srv := newTestServer(t, store, gh.URL, fake)
	srv.Close()
	srv.loopCtx = t.Context()
	srv.terminals = newTerminalHub(nil)
	defer srv.terminals.Close()
	for _, subject := range []string{"hubot-id", "peer-id"} {
		account, _, err := store.EnsureAccountForOAuthIdentity(state.OAuthIdentity{OAuthProvider: "github", OAuthSubject: subject, OAuthLogin: subject}, "user")
		if err != nil {
			t.Fatal(err)
		}
		saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "fixture-token")
		if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountLogin: "hubot", AccountType: "Organization"}); err != nil {
			t.Fatal(err)
		}
	}
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "terminal-owner", GitHubInstallationID: 987, RepositoryFullName: "hubot/project", RunnerName: "test-runner"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status, st.SandboxID = state.StatusRunning, "same-sandbox"
	st.LastAttemptAt = time.Now().UTC()
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	call := func(subject, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.AddCookie(testSessionCookie(subject, subject, "user"))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	path := "/user/runner_requests/" + st.ID + "/terminal"
	// Output can arrive before the provider returns a PTY. Keep its original
	// byte offset even if the handshake alone fills the replay tail.
	burst := strings.Repeat("x", 160<<10)
	fake.onTerminalOpen = func(_ context.Context, output func([]byte)) { output([]byte(burst)) }
	created := call("hubot-id", http.MethodPost, path, `{}`)
	var response struct {
		ID string `json:"session_id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &response); err != nil || created.Code != 201 || response.ID == "" {
		t.Fatal("create terminal", created.Code, err)
	}
	active, ok := srv.terminals.Get(response.ID)
	if !ok {
		t.Fatal("published terminal is missing")
	}
	start := uint64(0)
	if _, next, ok := active.outputAfter(&start); ok || next != uint64(len("terminal ready\n")+len(burst)) {
		t.Fatalf("lost truncated handshake offset: next=%d replayable=%t", next, ok)
	}
	if tail, _, ok := active.outputAfter(nil); !ok || len(tail) != 64<<10 {
		t.Fatalf("handshake tail: bytes=%d retained=%t", len(tail), ok)
	}
	session := path + "/" + response.ID
	// Both accounts can see this Job. Possessing its session ID must not
	// let a colleague read, write, resize, or close the opening user's PTY.
	for _, op := range []struct{ method, suffix, body string }{
		{http.MethodGet, "/events", ""},
		{http.MethodPost, "/input", `{"data":"peer"}`},
		{http.MethodPost, "/resize", `{"cols":80,"rows":24}`},
		{http.MethodDelete, "", ""},
	} {
		if rec := call("peer-id", op.method, session+op.suffix, op.body); rec.Code != http.StatusNotFound {
			t.Fatalf("peer %s %s returned %d", op.method, op.suffix, rec.Code)
		}
	}
	if rec := call("hubot-id", http.MethodPost, session+"/input", `{"data":"owner"}`); rec.Code != http.StatusAccepted || fake.terminal.isClosed() {
		t.Fatal("owner lost shell", rec.Code)
	}
	current, err := store.ReadState(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.LastAttemptAt = current.LastAttemptAt.Add(time.Second)
	if err := store.WriteState(current); err != nil {
		t.Fatal(err)
	}
	if rec := call("hubot-id", http.MethodPost, session+"/input", `{"data":"after replacement"}`); rec.Code != http.StatusNotFound {
		t.Fatal("old session reached a later attempt", rec.Code)
	}

	// The provider may finish opening a PTY after the request has moved on.
	var openingCtx context.Context
	fake.onTerminalOpen = func(ctx context.Context, _ func([]byte)) {
		openingCtx = ctx
		fake.terminal.closeError = errors.New("remote Kill unavailable")
		current, err := store.ReadState(st.ID)
		if err != nil {
			t.Fatal(err)
		}
		current.LastAttemptAt = current.LastAttemptAt.Add(time.Second)
		if err := store.WriteState(current); err != nil {
			t.Fatal(err)
		}
	}
	if rec := call("hubot-id", http.MethodPost, path, `{}`); rec.Code != http.StatusConflict || !fake.terminal.isClosed() {
		t.Fatal("stale PTY escaped open", rec.Code)
	}
	if openingCtx.Err() == nil {
		t.Fatal("rejected PTY output stream stayed open after Kill failed")
	}
}
