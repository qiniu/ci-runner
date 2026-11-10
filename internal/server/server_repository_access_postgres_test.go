package server

import (
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

// Run the HTTP server in another OS process so a warm authorization read cannot
// accidentally depend on any singleton shared with the test or the other node.
// The test constructor permits a loopback GitHub API; runnerd's config pins the production endpoint.
func TestRepositoryAccessNode(t *testing.T) {
	ready := os.Getenv("RUNNERD_TEST_ACCESS_READY")
	if ready == "" {
		t.Skip("child process only")
	}
	store := state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: os.Getenv("RUNNERD_TEST_ACCESS_DSN")})
	server := httptest.NewServer(newTestServer(t, store, os.Getenv("RUNNERD_TEST_ACCESS_GITHUB"), &fakeSandbox{}))
	defer server.Close()
	if err := os.WriteFile(ready, []byte(server.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
}

func startRepositoryAccessNode(t *testing.T, dsn, githubURL string) (string, func()) {
	t.Helper()
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestRepositoryAccessNode$")
	cmd.Env = append(os.Environ(), "RUNNERD_TEST_ACCESS_DSN="+dsn, "RUNNERD_TEST_ACCESS_GITHUB="+githubURL, "RUNNERD_TEST_ACCESS_READY="+ready)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stop := sync.OnceFunc(func() {
		_ = cmd.Process.Kill()
		_ = stdin.Close()
		_ = cmd.Wait()
	})
	t.Cleanup(stop)
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if data, err := os.ReadFile(ready); err == nil && len(data) > 0 {
			return string(data), stop
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("authorization test process failed to start")
	return "", nil
}

func TestSharedRepositoryAccessPostgresSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL test database")
	}
	dsn := catalogServerTestDSN(t)
	store := state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: dsn, MigrateOnStart: true})
	account, _, err := store.EnsureAccountForOAuthIdentity(state.OAuthIdentity{OAuthProvider: "github", OAuthSubject: "hubot-id", OAuthLogin: "hubot"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987}); err != nil {
		t.Fatal(err)
	}
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "private-job", GitHubInstallationID: 987, RepositoryFullName: "acme/private"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = state.StatusCompleted
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	var unavailable atomic.Bool
	staleStarted, releaseStale := make(chan struct{}), make(chan struct{})
	release := sync.OnceFunc(func() { close(releaseStale) })
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if unavailable.Load() {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}
		if r.URL.Path != "/user/installations/987/repositories" {
			http.NotFound(w, r)
			return
		}
		lookup := calls.Add(1)
		repo := "acme/private"
		switch r.Header.Get("Authorization") {
		case "Bearer stale":
			close(staleStarted)
			<-releaseStale
		case "Bearer replacement":
			repo = "acme/other"
		case "Bearer initial":
			if lookup == 1 {
				time.Sleep(200 * time.Millisecond)
			} // overlap the two cold HTTP requests
		default:
			http.Error(w, "unknown fixture credential", http.StatusUnauthorized)
			return
		}
		fmt.Fprintf(w, `{"repositories":[{"full_name":%q}]}`, repo)
	}))
	defer api.Close()
	defer release()
	setToken := func(value string) { saveTestGitHubOAuthToken(t, store, account.ID, "encryption-key", value) }
	setToken("initial")
	a, killA := startRepositoryAccessNode(t, dsn, api.URL)
	b, _ := startRepositoryAccessNode(t, dsn, api.URL)
	type response struct {
		code int
		body string
	}
	read := func(node string) response {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, node+"/user/runner_requests", nil)
		if err != nil {
			return response{body: err.Error()}
		}
		req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return response{body: err.Error()}
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return response{body: err.Error()}
		}
		return response{resp.StatusCode, string(data)}
	}
	check := func(got response, code int, visible bool) {
		t.Helper()
		if got.code != code || strings.Contains(got.body, "private-job") != visible {
			t.Fatalf("code=%d body=%s", got.code, got.body)
		}
	}
	cold := make(chan response, 2)
	for _, node := range []string{a, b} {
		go func() { cold <- read(node) }()
	}
	check(<-cold, 200, true)
	check(<-cold, 200, true)
	killA()
	check(read(b), 200, true)
	if calls.Load() != 1 {
		t.Fatal("surviving process refreshed a valid snapshot")
	}

	a, _ = startRepositoryAccessNode(t, dsn, api.URL)
	setToken("stale")
	done := make(chan response, 1)
	go func() { done <- read(a) }()
	select {
	case <-staleStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("refresh did not reach GitHub")
	}
	setToken("replacement")
	check(read(b), 200, false)
	release()
	check(<-done, 200, false)
	check(read(a), 200, false)

	setToken("initial")
	check(read(b), 200, true)
	unavailable.Store(true)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec("UPDATE repository_access_snapshots SET expires_at=0, refresh_after=0 WHERE account_id=$1", account.ID); err != nil {
		t.Fatal(err)
	}
	check(read(a), http.StatusBadGateway, false)
	check(read(b), http.StatusBadGateway, false)
}
