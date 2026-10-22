package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	deploy "github.com/miclle/deployd"
	"github.com/qiniu/ci-runner/internal/config"
	deploymentworker "github.com/qiniu/ci-runner/internal/deployment/worker"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
)

type migrationLiveProcess struct {
	deploy.Runtime
	closed atomic.Int64
}

func (*migrationLiveProcess) Inspect(context.Context, deploy.ProcessRef) (deploy.ProcessState, error) {
	return deploy.ProcessState{Running: true}, nil
}

func (p *migrationLiveProcess) Close(context.Context) error { p.closed.Add(1); return nil }

type migrationLiveFactory struct {
	process     migrationLiveProcess
	connections atomic.Int64
	creates     atomic.Int64
}

func (f *migrationLiveFactory) Connect(context.Context, state.Deployment) (deploymentworker.RuntimeHandle, error) {
	f.connections.Add(1)
	return &f.process, nil
}

func (f *migrationLiveFactory) Create(context.Context, state.Deployment, int64) (deploymentworker.RuntimeHandle, string, error) {
	f.creates.Add(1)
	return nil, "", fmt.Errorf("unexpected recreation")
}

func (f *migrationLiveFactory) Reconcile(context.Context, state.Deployment) (deploymentworker.RuntimeHandle, string, error) {
	return nil, "", fmt.Errorf("unexpected provisioning reconciliation")
}

// A private schema keeps this server test independent of the state package's
// real-dialect fixtures when packages run concurrently in the same test DB.
func migrationServerTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("RUNNERD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("RUNNERD_POSTGRES_TEST_DSN is required")
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var database string
	if err := pool.QueryRowContext(ctx, "SELECT current_database()").Scan(&database); err != nil || !strings.HasSuffix(database, "_test") {
		t.Fatal("server migration test requires a dedicated database ending in _test")
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "runnerd_server_" + hex.EncodeToString(nonce[:])
	if _, err := pool.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.ExecContext(ctx, `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Error(err)
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return dsn + " search_path=" + schema
}

func TestOnlineMigrationServerSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("set RUNNERD_CATALOG_BACKEND_TESTS=1 with a dedicated PostgreSQL test database")
	}
	dsn := migrationServerTestDSN(t)
	directory := t.TempDir()
	initial := state.New(directory)
	if err := initial.Ensure(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	runtime, err := state.OpenOnlineMigration(ctx, state.OnlineMigrationOptions{
		Directory: filepath.Join(directory, "migration"),
		SQLiteDSN: filepath.Join(directory, "runnerd.db"), PostgresDSN: dsn, Initialize: true, PauseBudget: 3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	store := runtime.Store()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}
		if r.URL.Path == "/user/installations/987/repositories" {
			fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"hubot/project","owner":{"login":"hubot"}}]}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer api.Close()
	fake := &fakeSandbox{}
	cfg := config.Config{
		AuthSessionSecret: "session-secret", AuthEncryptionKey: "encryption-key", AuthSessionTTL: time.Hour,
		MaxConcurrentRunners: 10, WorkerLeaseTTL: 30 * time.Second, RunnerIdleTimeout: time.Hour, SandboxTimeout: time.Hour,
		GitHubAPIBaseURL: api.URL, DeploymentWorkRoot: t.TempDir(),
	}
	srv := New(cfg, store, github.NewClient(api.URL, api.Client()), fake, slog.New(slog.NewTextHandler(io.Discard, nil)))
	factory := &migrationLiveFactory{}
	srv.deploymentWorker.Runtime = factory
	srv.Start()
	defer srv.Close()
	originalHub, originalWorker := srv.terminals, srv.deploymentWorker
	account, _, err := store.UpsertAccountForOAuthIdentity(state.OAuthIdentity{OAuthProvider: "github", OAuthSubject: "hubot-id", OAuthLogin: "hubot"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, cfg.AuthEncryptionKey.Value(), "user-token")
	if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "User", AccountLogin: "hubot"}); err != nil {
		t.Fatal(err)
	}
	_, runner, err := store.CreateRequest(state.RunnerRequest{ID: "migration-runner", GitHubInstallationID: 987, RepositoryFullName: "hubot/project", RunnerName: "original-runner"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	runner.Status, runner.SandboxID, runner.ProcessPID = state.StatusRunning, "original-runner-sandbox", 123
	if err := store.WriteState(runner); err != nil {
		t.Fatal(err)
	}
	deployment, err := store.CreateDeployment(state.Deployment{
		ID: "migration-deploy", AccountID: account.ID, ScopeType: state.AccountScopeTypeAccount,
		ScopeID: account.ID, GitHubInstallationID: 987, RepositoryID: 42, RepositoryFullName: "hubot/project", Ref: "refs/heads/main",
		CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "encrypted", Status: state.DeploymentStatusRunning, SandboxID: "original-deployment-sandbox",
		RuntimeID: "original-runtime", ProcessID: "456", ProcessTag: "original-process", ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		recorder := httptest.NewRecorder()
		srv.ServeHTTP(recorder, req)
		return recorder
	}
	base := "/user/deployments/" + deployment.ID + "/revisions/1/terminal"
	recorder := call(http.MethodPost, base, `{}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("open original PTY: %d %s", recorder.Code, recorder.Body.String())
	}
	var opened struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &opened); err != nil {
		t.Fatal(err)
	}
	session, found := srv.terminals.Get(opened.SessionID)
	if !found {
		t.Fatal("PTY session missing")
	}
	httpServer := httptest.NewServer(srv)
	defer httpServer.Close()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, httpServer.URL+base+"/"+opened.SessionID+"/events", nil)
	request.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	response, err := httpServer.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	streamDone := make(chan bool, 1)
	streamFinished := make(chan struct{})
	go func() {
		defer close(streamFinished)
		streamDone <- terminalOutputContains(t, response.Body, "after-rollback")
	}()
	defer func() { _ = response.Body.Close(); <-streamFinished }()
	session.append([]byte("before-cutover\n"))
	writeCtx, stopWriting := context.WithCancel(ctx)
	var written atomic.Int64
	writesDone := make(chan struct{})
	go func() {
		defer close(writesDone)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-writeCtx.Done():
				return
			case <-ticker.C:
				store.AppendLog(runner.ID, "stdout.log", []byte(fmt.Sprintf("row-%d\n", written.Add(1))))
			}
		}
	}()
	defer func() { stopWriting(); <-writesDone }()
	if err := runtime.Execute(ctx, "prepare"); err != nil {
		t.Fatal(err)
	}
	for {
		status, err := runtime.Status(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if status.Phase == "catching_up" {
			break
		}
		if err := runtime.Execute(ctx, "baseline"); err != nil {
			t.Fatal(err)
		}
	}
	for _, command := range []string{"verify", "cutover", "verify", "prune", "rollback"} {
		if err := runtime.Execute(ctx, command); err != nil {
			t.Fatalf("%s: %v", command, err)
		}
		recorder := call(http.MethodPost, base+"/"+opened.SessionID+"/input", `{"data":"still-open\n"}`)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("original PTY input after %s: %d %s", command, recorder.Code, recorder.Body.String())
		}
	}
	session.append([]byte("after-rollback\n"))
	select {
	case ok := <-streamDone:
		if !ok {
			t.Fatal("original SSE stream lost PTY output")
		}
	case <-ctx.Done():
		t.Fatal("original SSE stream failed across migration")
	}
	stopWriting()
	<-writesDone
	log, err := store.ReadLog(runner.ID, "stdout.log", 1<<20)
	if err != nil || int64(strings.Count(string(log), "\n")) != written.Load() || written.Load() == 0 {
		t.Fatalf("continuous log writer lost rows: written=%d err=%v", written.Load(), err)
	}
	for i, line := range strings.Split(strings.TrimSpace(string(log)), "\n") {
		if line != fmt.Sprintf("row-%d", i+1) {
			t.Fatal("log replay duplicated or reordered a row")
		}
	}
	after, err := store.ReadState(runner.ID)
	if err != nil || after.SandboxID != runner.SandboxID || after.ProcessPID != runner.ProcessPID || after.Status != state.StatusRunning {
		t.Fatalf("Runner attempt changed across migration: %+v %v", after, err)
	}
	afterDeployment, err := store.GetDeployment(deployment.ID)
	if err != nil || afterDeployment.SandboxID != deployment.SandboxID || afterDeployment.ProcessID != deployment.ProcessID || afterDeployment.Version != deployment.Version {
		t.Fatalf("deployment lifecycle changed across migration: %v", err)
	}
	retained, ok := srv.terminals.Get(opened.SessionID)
	if !ok || retained != session || srv.terminals != originalHub || srv.deploymentWorker != originalWorker || factory.creates.Load() != 0 || factory.process.closed.Load() != 0 {
		t.Fatal("migration replaced the worker, Sandbox or original PTY")
	}
	if err := srv.deploymentWorker.RunOnce(ctx); err != nil || factory.connections.Load() == 0 {
		t.Fatalf("same worker cannot monitor retained deployment after rollback: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.terminals != 1 || fake.terminal.isClosed() {
		t.Fatal("migration reopened or closed the original PTY")
	}
}
