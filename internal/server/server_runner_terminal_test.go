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

	"github.com/qiniu/ci-runner/internal/state"
)

func TestUserRunnerTerminalRevalidatesAttachedSession(t *testing.T) {
	for _, change := range []string{"authorized", "completed", "sandbox replaced", "repository changed", "revoked", "token rejected", "GitHub unavailable"} {
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
			srv := newTestServer(t, store, api.URL, &fakeSandbox{})
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
			case "completed", "sandbox replaced", "repository changed":
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
					if change == "authorized" && accessChecks.Load() >= 2 && !session.terminal.(*fakeTerminalSession).isClosed() {
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
					for !session.terminal.(*fakeTerminalSession).isClosed() && time.Now().Before(deadline) {
						time.Sleep(time.Millisecond)
					}
					if !session.terminal.(*fakeTerminalSession).isClosed() {
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
