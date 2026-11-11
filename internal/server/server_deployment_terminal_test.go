package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestDeploymentTerminalPrefersSavedProvider(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-API-Key") != "saved-scope-key" {
			t.Error("terminal did not use the saved scope credential")
		}
		if r.Method != http.MethodGet || r.URL.Path != "/sandboxes/saved-sandbox" {
			t.Errorf("unexpected terminal request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusBadRequest) // Observe Connect without creating a PTY.
	}))
	defer provider.Close()
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}
		if r.URL.Path == "/user/installations/987/repositories" {
			fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"hubot/project"}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer githubAPI.Close()
	store := state.New(t.TempDir())
	global := &fakeSandbox{}
	srv := newTestServer(t, store, githubAPI.URL, global)
	srv.Close()
	srv.loopCtx = context.Background()
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
	if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "user", AccountLogin: "hubot"}); err != nil {
		t.Fatal(err)
	}
	key, err := encryptSecret("saved-scope-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.CreateDeployment(state.Deployment{ID: "saved-provider", AccountID: account.ID, ScopeType: state.AccountScopeTypeAccount, ScopeID: account.ID, GitHubInstallationID: 987, RepositoryID: 42, RepositoryFullName: "hubot/project", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "encrypted", Status: state.DeploymentStatusRunning, SandboxID: "saved-sandbox", SandboxAPIURL: provider.URL, SandboxAPIKeyEncrypted: key, ExpiresAt: time.Now().Add(20 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []bool{false, true} {
		if invalid {
			d.SandboxAPIKeyEncrypted = "invalid-ciphertext"
			if err := store.UpdateDeployment(d); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(http.MethodPost, "/user/deployments/"+d.ID+"/revisions/1/terminal", strings.NewReader(`{}`))
		req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		want := http.StatusBadGateway
		if invalid {
			want = http.StatusConflict
		}
		if rec.Code != want {
			t.Fatalf("invalid=%v: status=%d body=%s", invalid, rec.Code, rec.Body.String())
		}
	}
	if calls.Load() != 1 || global.terminals != 0 {
		t.Fatalf("saved provider calls=%d global terminals=%d; must never fall back from saved credentials", calls.Load(), global.terminals)
	}
}
