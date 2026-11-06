package server

import (
	"bufio"
	"context"
	"crypto/sha256"
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
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
)

func newPeerTestServer(store state.Store, origin, api string) *Server {
	cfg := config.Config{TerminalPeerOrigin: origin, AuthEncryptionKey: "encryption-key", AuthSessionSecret: "session-secret", GitHubAPIBaseURL: api}
	return New(cfg, store, github.NewClient(api, http.DefaultClient), &fakeSandbox{}, nil)
}

// Subprocess owning a private hub. Its socket accepts both the browser and
// the other runnerd. Peers must never forward browser cookies in the clear.
func TestTerminalPeerProcess(t *testing.T) {
	ready := os.Getenv("RUNNERD_TERMINAL_TEST_READY")
	if ready == "" {
		t.Skip("child of terminal routing test")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	origin := "http://" + listener.Addr().String()
	store := state.NewWithOptions(state.Options{Backend: os.Getenv("RUNNERD_TERMINAL_TEST_BACKEND"), DatabaseDSN: os.Getenv("RUNNERD_TERMINAL_TEST_DSN")})
	srv := newPeerTestServer(store, origin, os.Getenv("RUNNERD_TERMINAL_TEST_GITHUB"))
	if err := os.WriteFile(ready, []byte(origin), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Fatal(http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(terminalPeerHeader) != "" && (r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "") {
			http.Error(w, "forwarded browser credentials", 500)
			return
		}
		srv.ServeHTTP(w, r)
	})))
}

func TestTerminalPeerRouting(t *testing.T) {
	testTerminalPeerRouting(t, state.BackendSQLite, filepath.Join(t.TempDir(), "terminal.db"))
}

func TestTerminalPeerPostgresSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL test database")
	}
	testTerminalPeerRouting(t, state.BackendPostgres, catalogServerTestDSN(t))
}

func testTerminalPeerRouting(t *testing.T, backend, dsn string) {
	t.Helper()
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
	store := state.NewWithOptions(state.Options{Backend: backend, DatabaseDSN: dsn, MigrateOnStart: true})
	for _, subject := range []string{"hubot-id", "colleague"} {
		account, _, err := store.EnsureAccountForOAuthIdentity(state.OAuthIdentity{OAuthProvider: "github", OAuthSubject: subject, OAuthLogin: subject}, "user")
		if err != nil {
			t.Fatal(err)
		}
		saveTestGitHubOAuthToken(t, store, account.ID, "encryption-key", "fixture-token")
		if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "Organization", AccountLogin: "hubot"}); err != nil {
			t.Fatal(err)
		}
	}
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := store.CreateRequest(state.RunnerRequest{ID: "peer-run", GitHubInstallationID: 987, RepositoryFullName: "hubot/project", RunnerName: "fixture"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	run.Status, run.SandboxID = state.StatusRunning, "runner-sandbox"
	if err := store.WriteState(run); err != nil {
		t.Fatal(err)
	}
	deploy, err := store.CreateDeployment(state.Deployment{ID: "peer-deploy", AccountID: account.ID, ScopeType: state.AccountScopeTypeAccount, ScopeID: account.ID, GitHubInstallationID: 987, RepositoryID: 42, RepositoryFullName: "hubot/project", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "encrypted", Status: state.DeploymentStatusRunning, SandboxID: "deploy-sandbox", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(t.TempDir(), "ready")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestTerminalPeerProcess$")
	child.Env = append(os.Environ(), "RUNNERD_TERMINAL_TEST_READY="+ready, "RUNNERD_TERMINAL_TEST_BACKEND="+backend, "RUNNERD_TERMINAL_TEST_DSN="+dsn, "RUNNERD_TERMINAL_TEST_GITHUB="+api.URL)
	child.Stderr = os.Stderr
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})
	var owner string
	for owner == "" {
		raw, _ := os.ReadFile(ready)
		owner = string(raw)
		if owner != "" {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("owner startup", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	peer := httptest.NewUnstartedServer(nil)
	srv := newPeerTestServer(store, "http://"+peer.Listener.Addr().String(), api.URL)
	peer.Config.Handler = srv
	peer.Start()
	defer peer.Close()
	defer srv.Close()
	client := http.Client{Timeout: 5 * time.Second}
	call := func(subject, method, target, body, cursor string, want int) string {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if subject != "" {
			req.AddCookie(testSessionCookie(subject, subject, "user"))
		}
		if cursor != "" {
			req.Header.Set("Last-Event-ID", cursor)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != want {
			msg, _ := io.ReadAll(resp.Body)
			t.Fatalf("%s %s: got %d want %d: %s", method, req.URL.Path, resp.StatusCode, want, msg)
		}
		if resp.Header.Get("Content-Type") == "text/event-stream" {
			reader := bufio.NewReader(resp.Body)
			for {
				line, err := reader.ReadString('\n')
				if err != nil {
					t.Fatal(err)
				}
				if strings.HasPrefix(line, "data: ") {
					return line
				}
			}
		}
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	open := func(node, path string) string {
		t.Helper()
		raw := call("hubot-id", "POST", node+path, `{}`, "", 201)
		var opened struct {
			ID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(raw), &opened); err != nil || !strings.HasPrefix(opened.ID, terminalRoutePrefix) {
			t.Fatal("routed terminal missing", err)
		}
		return path + "/" + opened.ID
	}
	paths := []string{"/user/runner_requests/" + run.ID + "/terminal", "/user/deployments/" + deploy.ID + "/revisions/1/terminal"}
	var live []string
	for _, path := range paths {
		session := open(owner, path)
		if line := call("hubot-id", "GET", owner+session+"/events", "", "", 200); !strings.Contains(line, "terminal ready") {
			t.Fatal("initial output", line)
		}
		if line := call("hubot-id", "GET", peer.URL+session+"/events", "", "9", 200); line != "data: \"ready\\n\"\n" {
			t.Fatal("replayed output", line)
		}
		call("", "POST", peer.URL+session+"/input", `{"data":"x"}`, "", 401)
		call("colleague", "POST", peer.URL+session+"/input", `{"data":"x"}`, "", 404)
		call("hubot-id", "POST", peer.URL+session+"/input", `{"data":"pwd"}`, "", 202)
		call("hubot-id", "POST", peer.URL+session+"/resize", `{"cols":100,"rows":48}`, "", 202)
		call("hubot-id", "DELETE", peer.URL+session, "", "", 200)
		call("hubot-id", "POST", owner+session+"/input", `{"data":"x"}`, "", 404)
		live = append(live, open(owner, path))
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("expected killed owner")
	}
	for i, path := range paths {
		call("hubot-id", "POST", peer.URL+live[i]+"/input", `{"data":"x"}`, "", 503)
		reopened := open(peer.URL, path)
		call("hubot-id", "POST", peer.URL+reopened+"/input", `{"data":"pwd"}`, "", 202)
	}
	current, err := store.ReadState(run.ID)
	if err != nil || current.Status != state.StatusRunning || current.SandboxID != run.SandboxID {
		t.Fatal("Runner changed on terminal owner death", err)
	}
	saved, err := store.GetDeployment(deploy.ID)
	if err != nil || saved.Status != state.DeploymentStatusRunning || saved.SandboxID != deploy.SandboxID || saved.RevisionNumber != deploy.RevisionNumber {
		t.Fatal("Deployment changed on terminal owner death", err)
	}
}

func TestTerminalPeerRejectsChangedOperation(t *testing.T) {
	srv := &Server{cfg: config.Config{TerminalPeerOrigin: "https://node.example.test", AuthEncryptionKey: "test-key"}}
	id, err := srv.newTerminalSessionID(17)
	if err != nil {
		t.Fatal(err)
	}
	path := "/user/runner_requests/test/terminal/" + id + "/input"
	payload := `{"data":"pwd"}`
	valid := terminalPeerRequest{Method: "POST", Path: path, Cookie: "signed-fixture", Expires: time.Now().Add(time.Minute).Unix(), Payload: sha256.Sum256([]byte(payload))}
	handler := srv.routeTerminal(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(adminSessionCookieName)
		raw, readErr := io.ReadAll(r.Body)
		if err != nil || cookie.Value != valid.Cookie || readErr != nil || string(raw) != payload {
			t.Fatal("owner did not receive the bound cookie and input")
		}
		w.WriteHeader(http.StatusAccepted)
	})
	for _, change := range []string{"valid", "method", "path", "expired", "payload", "cursor", "forged proof", "forged route", "wrong node"} {
		t.Run(change, func(t *testing.T) {
			grant := valid
			body := payload
			sessionID := id
			want := http.StatusUnauthorized
			switch change {
			case "valid":
				want = http.StatusAccepted
			case "method":
				grant.Method = "DELETE"
			case "path":
				grant.Path += "/resize"
			case "expired":
				grant.Expires = time.Now().Add(-time.Second).Unix()
			case "payload":
				body = `{"data":"different"}`
			case "cursor":
				grant.Cursor = "15"
			case "forged route":
				sessionID = terminalRoutePrefix + "invalid"
				want = http.StatusNotFound
			case "wrong node":
				srv.cfg.TerminalPeerOrigin = "https://different.example.test"
				defer func() { srv.cfg.TerminalPeerOrigin = "https://node.example.test" }()
				want = http.StatusServiceUnavailable
			}
			raw, err := json.Marshal(grant)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := encryptSecret(string(raw), srv.cfg.AuthEncryptionKey.Value()+terminalRequestKeyDomain)
			if err != nil {
				t.Fatal(err)
			}
			if change == "forged proof" {
				proof = "untrusted"
			}
			req := httptest.NewRequest("POST", path, strings.NewReader(body))
			req.SetPathValue("sessionID", sessionID)
			req.Header.Set(terminalPeerHeader, proof)
			rec := httptest.NewRecorder()
			handler(rec, req)
			if rec.Code != want {
				t.Fatalf("got %d want %d", rec.Code, want)
			}
		})
	}
}

func TestTerminalPeerIdleReplaySurvivesHTTPWriteTimeout(t *testing.T) {
	owner := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Last-Event-ID") != "42" {
			t.Error("owner lost replay cursor")
		}
		// Waiting for owner headers can exceed the browser-side write deadline.
		time.Sleep(250 * time.Millisecond)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_ = http.NewResponseController(w).Flush()
		// An idle resumed PTY has no initial body. Its first heartbeat can
		// arrive after the forwarding node's configured HTTP WriteTimeout.
		time.Sleep(250 * time.Millisecond)
		_, _ = io.WriteString(w, "id: 42\nevent: terminal-heartbeat\ndata: {}\n\n")
	}))
	defer owner.Close()
	srv := &Server{cfg: config.Config{AuthEncryptionKey: "shared-key"}, terminalPeerHTTP: owner.Client()}
	for _, http2 := range []bool{false, true} {
		t.Run(fmt.Sprint("http2=", http2), func(t *testing.T) {
			peer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				srv.forwardTerminal(w, r, owner.URL)
			}))
			peer.EnableHTTP2 = http2
			peer.Config.WriteTimeout = 100 * time.Millisecond
			peer.StartTLS()
			defer peer.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, peer.URL+"/events", nil)
			if err != nil {
				t.Fatal(err)
			}
			req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: "fixture"})
			req.Header.Set("Last-Event-ID", "42")
			resp, err := peer.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil || resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "id: 42\nevent: terminal-heartbeat") {
				t.Fatalf("idle forwarded stream lost heartbeat: status=%d body=%q err=%v", resp.StatusCode, body, err)
			}
		})
	}
}

func TestTerminalPeerOutageAllowsResumingEvents(t *testing.T) {
	var ownerStatus atomic.Int64
	ownerStatus.Store(http.StatusServiceUnavailable)
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status := int(ownerStatus.Load()); status != http.StatusOK {
			writeError(w, status, "owner terminal unavailable")
			return
		}
		if r.Header.Get("Last-Event-ID") != "42" {
			t.Error("owner lost last confirmed cursor")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: 45\ndata: \"abc\"\n\n")
	}))
	defer owner.Close()
	srv := &Server{cfg: config.Config{AuthEncryptionKey: "shared-key"}, terminalPeerHTTP: owner.Client()}
	fwd := func(origin string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/events", nil)
		req.Header.Set("Last-Event-ID", "42")
		req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: "fixture"})
		rec := httptest.NewRecorder()
		srv.forwardTerminal(rec, req, origin)
		return rec
	}
	down := httptest.NewServer(http.NotFoundHandler())
	down.Close() // Real peer transport error before headers.
	for _, origin := range []string{down.URL, owner.URL} {
		got := fwd(origin)
		// An ended SSE response will reconnect. An HTTP error makes native
		// EventSource permanently CLOSED. A comment cannot confirm recovery
		// or advance the last event ID in the Jobs/Deploy frontend.
		if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "text/event-stream" || !strings.HasPrefix(got.Body.String(), ":") || strings.Contains(got.Body.String(), "\nid:") || strings.Contains(got.Body.String(), "\ndata:") {
			t.Fatalf("transient owner failure ended or falsely recovered the stream: %d %q", got.Code, got.Body.String())
		}
	}
	ownerStatus.Store(http.StatusOK)
	got := fwd(owner.URL)
	if got.Code != http.StatusOK || got.Body.String() != "id: 45\ndata: \"abc\"\n\n" {
		t.Fatalf("lost replay after owner returned: %d %q", got.Code, got.Body.String())
	}
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		ownerStatus.Store(int64(status))
		if got := fwd(owner.URL); got.Code != status {
			t.Fatalf("changed definitive owner response %d to %d", status, got.Code)
		}
	}
}
