package server

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestDeploymentTerminalOutputSurvivesHTTPWriteTimeout(t *testing.T) {
	srv := &Server{terminals: newTerminalHub(nil)}
	defer srv.terminals.Close()
	session := srv.terminals.Add("deployment:active", "sandbox", &fakeTerminalSession{})
	httpServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.streamTerminalSession(&loggingResponseWriter{ResponseWriter: w}, r, session)
	}))
	httpServer.Config.WriteTimeout = 100 * time.Millisecond
	httpServer.Start()
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", httpServer.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := httpServer.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	go func() {
		for i := range 8 {
			time.Sleep(40 * time.Millisecond)
			session.append([]byte(fmt.Sprintf("output %d", i)))
		}
	}()
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if scanner.Text() == `data: "output 7"` {
			return
		}
	}
	t.Fatalf("active terminal disconnected at the HTTP response deadline: %v", scanner.Err())
}

func TestDeploymentTerminalIdleStreamHeartbeat(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		srv := &Server{terminals: newTerminalHub(nil)}
		defer srv.terminals.Close()
		session := srv.terminals.Add("deployment:heartbeat", "sandbox", &fakeTerminalSession{})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rec := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			srv.streamTerminalSession(rec, httptest.NewRequest("GET", "/events", nil).WithContext(ctx), session)
		}()
		defer func() {
			cancel()
			<-done
			time.Sleep(terminalIdleCloseDelay + time.Second)
			synctest.Wait()
		}()
		synctest.Wait()
		for range 2 {
			time.Sleep(15 * time.Second)
			synctest.Wait()
		}
		if strings.Count(rec.Body.String(), ": keepalive\n\n") != 2 {
			t.Fatalf("idle stream has no periodic SSE heartbeat: %q", rec.Body.String())
		}
		session.append([]byte("shell output\r\n"))
		synctest.Wait()
		if !strings.Contains(rec.Body.String(), `data: "shell output\r\n"`) {
			t.Fatalf("heartbeat interrupted terminal output: %q", rec.Body.String())
		}
		if session.terminal.(*fakeTerminalSession).isClosed() {
			t.Fatal("an attached idle terminal was closed")
		}
	})
}

func TestDeploymentTerminalPrefersSavedProvider(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-API-Key") != "saved-scope-key" {
			t.Error("terminal did not use the saved scope credential")
		}
		var input struct {
			Timeout int32 `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		if input.Timeout < 1190 || input.Timeout > 1200 {
			t.Errorf("Connect timeout %d does not preserve remaining lifetime", input.Timeout)
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
	srv.terminals = newTerminalHub(nil)
	defer srv.terminals.Close()
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

func TestDeploymentTerminalLifecycleAndAuthorization(t *testing.T) {
	var revoked atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}

		if r.URL.Path == "/user/installations/987/repositories" {
			if revoked.Load() {
				fmt.Fprint(w, `{"repositories":[]}`)
			} else {
				fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"hubot/project","owner":{"login":"hubot"}}]}`)
			}
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	store := state.New(t.TempDir())
	sandbox := &fakeSandbox{}
	srv := newTestServer(t, store, api.URL, sandbox)
	srv.Close()
	srv.loopCtx = context.Background()
	srv.terminals = newTerminalHub(nil)
	defer srv.terminals.Close()
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
	if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "user", AccountLogin: "hubot"}); err != nil {
		t.Fatal(err)
	}
	d, err := store.CreateDeployment(state.Deployment{ID: "terminal-deploy", AccountID: account.ID + 10, ScopeType: state.AccountScopeTypeAccount, ScopeID: account.ID, GitHubInstallationID: 987, RepositoryID: 42, RepositoryFullName: "hubot/project", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "encrypted", Status: state.DeploymentStatusRunning, SandboxID: "sandbox-deploy", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	base := "/user/deployments/" + d.ID + "/revisions/1/terminal"
	call := func(method, path, body string, authenticated bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if authenticated {
			req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	rec := call("POST", base, `{"cols":100,"rows":28}`, true)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	var result struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	session, ok := srv.terminals.Get(result.SessionID)
	if !ok || session.requestID != deploymentAccountTerminalKey(d, account.ID) || !strings.Contains(string(session.buffer), "terminal ready") {
		t.Fatal("missing bound session/output")
	}
	path := base + "/" + result.SessionID
	streamCtx, cancelStream := context.WithTimeout(context.Background(), 100*time.Millisecond)
	streamReq := httptest.NewRequest("GET", path+"/events", nil).WithContext(streamCtx)
	streamReq.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	streamRec := httptest.NewRecorder()
	srv.ServeHTTP(streamRec, streamReq)
	cancelStream()
	if streamRec.Code != 200 || streamRec.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(streamRec.Body.String(), "terminal ready") {
		t.Fatalf("stream %d %s", streamRec.Code, streamRec.Body.String())
	}

	if rec := call("POST", path+"/input", `{"data":"pwd\r"}`, true); rec.Code != 202 {
		t.Fatalf("input %d %s", rec.Code, rec.Body.String())
	}
	if rec := call("POST", path+"/resize", `{"cols":120,"rows":40}`, true); rec.Code != 202 {
		t.Fatalf("resize %d %s", rec.Code, rec.Body.String())
	}
	sandbox.terminal.mu.Lock()
	input, size := sandbox.terminal.input, sandbox.terminal.resized
	sandbox.terminal.mu.Unlock()
	if input != "pwd\r" || size.Cols != 120 || size.Rows != 40 {
		t.Fatalf("PTY data %q %#v", input, size)
	}
	for _, action := range []struct{ method, suffix string }{{"POST", ""}, {"GET", "/" + result.SessionID + "/events"}, {"POST", "/" + result.SessionID + "/input"}, {"POST", "/" + result.SessionID + "/resize"}, {"DELETE", "/" + result.SessionID}} {
		if rec := call(action.method, base+action.suffix, `{}`, false); rec.Code != 401 {
			t.Fatalf("anonymous %s: %d", action.suffix, rec.Code)
		}
	}
	if rec := call("POST", strings.Replace(base, "/revisions/1/", "/revisions/2/", 1), `{}`, true); rec.Code != 409 {
		t.Fatalf("historical/future revision %d", rec.Code)
	}
	foreign := srv.terminals.Add("runner-request", d.SandboxID, &fakeTerminalSession{})
	if rec := call("POST", base+"/"+foreign.id+"/input", `{"data":"pwd"}`, true); rec.Code != 404 {
		t.Fatalf("foreign session %d", rec.Code)
	}
	otherActor := srv.terminals.Add(deploymentAccountTerminalKey(d, account.ID+1), d.SandboxID, &fakeTerminalSession{})
	for _, action := range []struct{ method, suffix string }{{"GET", "/events"}, {"POST", "/input"}, {"POST", "/resize"}, {"DELETE", ""}} {
		if rec := call(action.method, base+"/"+otherActor.id+action.suffix, `{}`, true); rec.Code != 404 {
			t.Fatalf("other actor session %s: %d", action.suffix, rec.Code)
		}
	}
	revoked.Store(true)
	srv.invalidateUserRepositoryAccess(account.ID)
	for _, suffix := range []string{"", "/" + result.SessionID + "/input", "/" + result.SessionID + "/resize"} {
		if rec := call("POST", base+suffix, `{}`, true); rec.Code != 404 {
			t.Fatalf("revoked access %s: %d", suffix, rec.Code)
		}
	}
	revoked.Store(false)
	srv.invalidateUserRepositoryAccess(account.ID)
	// A separate explicit disconnect must not close the original shell.
	second := call("POST", base, `{}`, true)
	var secondResult struct {
		SessionID string `json:"session_id"`
	}
	if second.Code != 201 {
		t.Fatalf("second terminal %d", second.Code)
	}
	if err := json.Unmarshal(second.Body.Bytes(), &secondResult); err != nil {
		t.Fatal(err)
	}
	if rec := call("DELETE", base+"/"+secondResult.SessionID, "", true); rec.Code != 200 || !sandbox.terminal.isClosed() {
		t.Fatalf("disconnect %d", rec.Code)
	}
	d.Status = state.DeploymentStatusCompleted
	if err := store.UpdateDeployment(d); err != nil {
		t.Fatal(err)
	}
	if rec := call("POST", path+"/input", `{"data":"pwd"}`, true); rec.Code != 409 {
		t.Fatalf("stopped %d", rec.Code)
	}
	firstPTY := session.terminal.(*deploymentTerminalProcess).TerminalSession.(*fakeTerminalSession)
	deadline := time.Now().Add(7 * time.Second)
	for !firstPTY.isClosed() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !firstPTY.isClosed() {
		t.Fatal("stopped deployment left its terminal open")
	}
	if _, ok := srv.terminals.Get(result.SessionID); ok {
		t.Fatal("stopped session retained")
	}
}

func TestDeploymentTerminalRejectsChangedAttempt(t *testing.T) {
	original := state.Deployment{ID: "d", AccountID: 1, RevisionNumber: 2, Attempt: 3, Status: state.DeploymentStatusRunning, SandboxID: "sb", ExpiresAt: time.Now().Add(time.Hour)}
	if !sameDeploymentTerminal(original, original) {
		t.Fatal("current attempt rejected")
	}
	for _, mutate := range []func(*state.Deployment){
		func(d *state.Deployment) { d.RevisionNumber++ }, func(d *state.Deployment) { d.Attempt++ }, func(d *state.Deployment) { d.SandboxID = "new" }, func(d *state.Deployment) { d.Status = state.DeploymentStatusStopping }, func(d *state.Deployment) { d.ExpiresAt = time.Now().Add(-time.Second) }, func(d *state.Deployment) { d.AccountID++ },
	} {
		changed := original
		mutate(&changed)
		if sameDeploymentTerminal(original, changed) {
			t.Fatalf("accepted stale attempt %#v", changed)
		}
	}
}
