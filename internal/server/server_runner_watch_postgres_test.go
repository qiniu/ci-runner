//go:build unix

package server

import (
	"bufio"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

// The fixture replaces the provider connection only. Each child runs normal
// startup recovery and background loops with its own database pool.
func TestRunnerWatchPostgresProcess(t *testing.T) {
	if ready := os.Getenv("RUNNERD_WATCH_TEST_READY"); ready != "" {
		store := state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: os.Getenv("RUNNERD_WATCH_TEST_DSN")})
		provider := &fakeSandbox{}
		cfg := config.Config{WorkerLeaseTTL: 2 * time.Second, SandboxTimeout: time.Hour, SandboxStopTimeout: time.Minute, MaxConcurrentRunners: 1}
		srv := New(cfg, store, github.NewClient("", nil), provider, nil)
		if err := srv.Recover(t.Context()); err != nil {
			t.Fatal(err)
		}
		srv.Start()
		defer srv.Close()
		api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost {
				input := provider.lastRecoverInput()
				if input.OnExit == nil {
					http.Error(w, "no watcher", http.StatusConflict)
					return
				}
				input.OnExit(sandboxrunner.ExitResult{}, nil)
			}
			_ = json.NewEncoder(w).Encode([2]int{provider.recoveredCount(), provider.stoppedCount()})
		}))
		defer api.Close()
		if err := os.WriteFile(ready, []byte(api.URL), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = bufio.NewReader(os.Stdin).ReadByte()
		return
	}
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL database")
	}
	dsn := catalogServerTestDSN(t)
	store := state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: dsn, MigrateOnStart: true})
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "watch", RunnerName: "watch"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status, st.SandboxID, st.ProcessPID = state.StatusRunning, "sandbox", 42
	st.AssignedJobID = 123 // This attempt already accepted a job.
	st.RunningAt = time.Now().UTC()
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	wait := func(what string, condition func() bool) {
		t.Helper()
		end := time.Now().Add(20 * time.Second)
		for !condition() {
			if time.Now().After(end) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	start := func() (*os.Process, string) {
		t.Helper()
		dir := t.TempDir()
		ready := filepath.Join(dir, "ready")
		logFile, err := os.Create(filepath.Join(dir, "runner.log"))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(os.Args[0], "-test.run=^TestRunnerWatchPostgresProcess$")
		cmd.Env = append(os.Environ(), "RUNNERD_WATCH_TEST_READY="+ready, "RUNNERD_WATCH_TEST_DSN="+dsn)
		cmd.Stdout, cmd.Stderr = logFile, logFile
		hold, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_ = cmd.Process.Kill()
			_ = hold.Close()
			_ = cmd.Wait()
			_ = logFile.Close()
			if t.Failed() {
				data, _ := os.ReadFile(logFile.Name())
				t.Logf("child: %s", data)
			}
		})
		var address []byte
		wait("child startup", func() bool {
			address, _ = os.ReadFile(ready)
			return len(address) > 0
		})
		return cmd.Process, string(address)
	}
	client := &http.Client{Timeout: 4 * time.Second}
	call := func(address, method string) [2]int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), method, address, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var counts [2]int
		if err := json.NewDecoder(resp.Body).Decode(&counts); err != nil || resp.StatusCode != 200 {
			t.Fatalf("provider stats: status=%d err=%v", resp.StatusCode, err)
		}
		return counts
	}
	a, first := start()
	b, second := start()
	// Several lease periods, including the second node's initial scan.
	time.Sleep(6 * time.Second)
	if call(first, http.MethodGet) != [2]int{1, 0} || call(second, http.MethodGet) != [2]int{} {
		t.Fatal("healthy watch was attached more than once")
	}
	pausedAt := time.Now()
	if err := a.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	wait("B to take over the paused watcher", func() bool { return call(second, http.MethodGet)[0] == 1 })
	pausedTakeover := time.Since(pausedAt)
	before, err := store.ReadState("watch")
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Signal(syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	if got := call(first, http.MethodPost); got[1] != 0 {
		t.Fatalf("old watcher stopped the Sandbox: %v", got)
	}
	after, err := store.ReadState("watch")
	if err != nil || after.Status != state.StatusRunning || after.Version != before.Version {
		t.Fatalf("stale callback changed the attempt: status=%s version=%d err=%v", after.Status, after.Version, err)
	}
	events, _, err := store.ListRunnerEvents("watch", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Stage == runnerEventStageRunnerExit {
			t.Fatal("stale callback recorded exit evidence for the active runner")
		}
	}
	killedAt := time.Now()
	if err := b.Kill(); err != nil {
		t.Fatal(err)
	}
	wait("A to take over the crashed watcher", func() bool { return call(first, http.MethodGet)[0] == 2 })
	crashTakeover := time.Since(killedAt)
	if got := call(first, http.MethodPost); got != [2]int{2, 1} {
		t.Fatalf("current watcher: %v", got)
	}
	after, err = store.ReadState("watch")
	if err != nil || after.Status != state.StatusCompleted || after.RunnerExitCode == nil || *after.RunnerExitCode != 0 {
		t.Fatalf("current watcher did not complete attempt: status=%s err=%v", after.Status, err)
	}
	t.Logf("2s watch lease, 5s polling: SIGSTOP to reattach %s, SIGKILL to reattach %s, final status %s", pausedTakeover.Round(time.Millisecond), crashTakeover.Round(time.Millisecond), after.Status)
}
