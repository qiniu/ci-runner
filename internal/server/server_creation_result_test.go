package server

import (
	"context"
	"errors"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type lostCreateResponse struct {
	*fakeSandbox
	recoverInPeer func()
}

func (p *lostCreateResponse) StartRunner(context.Context, sandboxrunner.StartInput) (sandboxrunner.StartResult, error) {
	p.recoverInPeer()
	return sandboxrunner.StartResult{}, errors.New("connection reset after Sandbox creation")
}

// Simulate a lost provider response. A separate service process finds the
// live Sandbox and adopts it before the original StartRunner returns its error.
func TestLateStartFailureAfterPeerAdoption(t *testing.T) {
	const id = "lost-create-response"
	if dir := os.Getenv("RUNNERD_CREATION_RESULT_PEER"); dir != "" {
		store := state.New(dir)
		if dsn := os.Getenv("RUNNERD_CREATION_RESULT_DSN"); dsn != "" {
			store = state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: dsn})
		}
		fake := &fakeSandbox{recoverResult: sandboxrunner.StartResult{SandboxID: "adopted", PID: 49}}
		srv := newRunnerLifecycleTestServer(t, store, "", fake)
		if err := srv.recoverRunner(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		assertAdmissionState(t, store, id, state.StatusRunning)
		return
	}
	dir := t.TempDir()
	store := state.New(dir)
	var dsn string
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") == "1" {
		dsn = catalogServerTestDSN(t)
		store = state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: dsn, MigrateOnStart: true})
	}
	upsertLifecycleProfile(t, store, state.RunnerProfile{Name: "linux", Labels: []string{"self-hosted", "linux"}, TemplateID: "test-template", Enabled: true})
	createLifecycleRequest(t, store, id, "linux", 0)
	api := httptest.NewServer(githubRunnerAPI(t))
	defer api.Close()
	var adopted state.RunnerState
	provider := &lostCreateResponse{fakeSandbox: &fakeSandbox{}, recoverInPeer: func() {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLateStartFailureAfterPeerAdoption$")
		cmd.Env = append(os.Environ(), "RUNNERD_CREATION_RESULT_PEER="+dir, "RUNNERD_CREATION_RESULT_DSN="+dsn)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("peer recovery: %v\n%s", err, out)
		}
		adopted = assertAdmissionState(t, store, id, state.StatusRunning)
	}}
	srv := newRunnerLifecycleTestServer(t, store, api.URL, provider)
	srv.startClaimedRunner(t.Context(), queuedRunnerForTest(t, store, id, "original"))
	got := assertAdmissionState(t, store, id, state.StatusRunning)
	if adopted.ProvisionAttemptID == "" || got.Version != adopted.Version || got.ProvisionAttemptID != adopted.ProvisionAttemptID || got.SandboxID != "adopted" || got.ProcessPID != 49 || got.RetryCount != 0 {
		t.Fatalf("late failure changed adopted attempt: status=%s sandbox=%s pid=%d version=%d (adopted %d)", got.Status, got.SandboxID, got.ProcessPID, got.Version, adopted.Version)
	}
}
