//go:build unix

package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type cleanupProcessSandbox struct {
	*fakeSandbox
	url string
}

func (p *cleanupProcessSandbox) StopRunner(ctx context.Context, id string, pid uint32) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/sandbox/%s/%d/stop", p.url, id, pid), nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("stop sandbox: HTTP %d", resp.StatusCode)
	}
	return p.fakeSandbox.StopRunner(ctx, id, pid)
}

// The child blocks inside the GitHub DELETE after checkpointing a Sandbox
// stop. The parent is a second server with an independent database pool.
func TestRunnerCleanupProcess(t *testing.T) {
	const id = "cleanup-owner"
	if dir := os.Getenv("RUNNERD_TEST_CLEANUP_DIR"); dir != "" {
		store := state.New(dir)
		if dsn := os.Getenv("RUNNERD_TEST_CLEANUP_DSN"); dsn != "" {
			store = state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: dsn})
		}
		url := os.Getenv("RUNNERD_TEST_CLEANUP_GITHUB")
		fake := &cleanupProcessSandbox{fakeSandbox: &fakeSandbox{}, url: url}
		srv := newRunnerLifecycleTestServer(t, store, url, fake)
		srv.cfg.SandboxStopTimeout = 10 * time.Second
		srv.cfg.WorkerLeaseTTL = time.Second
		_, _, _ = srv.stopRunner(t.Context(), id, github.WorkflowJob{}, state.TerminationSourceManualStop)
		if fake.stoppedCount() != 1 {
			t.Fatal("original owner did not stop the sandbox exactly once")
		}
		return
	}
	for _, action := range []string{"kill", "pause"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			store := state.New(dir)
			var dsn string
			if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") == "1" {
				dsn = catalogServerTestDSN(t)
				store = state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: dsn, MigrateOnStart: true})
			}
			_, st, err := store.CreateRequest(state.RunnerRequest{ID: id, RepositoryFullName: "o/r", RunnerName: "old-runner"}, nil)
			if err != nil {
				t.Fatal(err)
			}
			st.Status, st.SandboxID, st.ProcessPID = state.StatusRunning, "original", 17
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			var lists, deletes atomic.Int32
			sandboxStarted, sandboxRelease := make(chan struct{}), make(chan struct{})
			started, release := make(chan struct{}), make(chan struct{})
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST" && r.URL.Path == "/sandbox/original/17/stop":
					close(sandboxStarted)
					select {
					case <-sandboxRelease:
					case <-r.Context().Done():
					}
					w.WriteHeader(http.StatusNoContent)
				case r.Method == "GET" && r.URL.Path == "/repos/o/r/actions/runners":
					runnerID := 71
					if lists.Add(1) > 1 {
						runnerID = 99
					} // A different registration now owns the same name.
					fmt.Fprintf(w, `{"runners":[{"id":%d,"name":"old-runner"}]}`, runnerID)
				case r.Method == "DELETE" && r.URL.Path == "/repos/o/r/actions/runners/71":
					if deletes.Add(1) == 1 {
						close(started)
						select {
						case <-release:
						case <-r.Context().Done():
						}
					}
					w.WriteHeader(http.StatusNotFound)
				default:
					t.Errorf("unexpected provider call %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer api.Close()
			defer close(release)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestRunnerCleanupProcess$")
			cmd.Env = append(os.Environ(), "RUNNERD_TEST_CLEANUP_DIR="+dir, "RUNNERD_TEST_CLEANUP_DSN="+dsn, "RUNNERD_TEST_CLEANUP_GITHUB="+api.URL)
			var log bytes.Buffer
			cmd.Stdout, cmd.Stderr = &log, &log
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
				if t.Failed() {
					t.Log(log.String())
				}
			}()
			fake := &fakeSandbox{}
			peer := newRunnerLifecycleTestServer(t, store, api.URL, fake)
			peer.cfg.WorkerLeaseTTL = time.Second
			select {
			case <-sandboxStarted:
			case <-ctx.Done():
				t.Fatal("original stop did not reach Sandbox")
			}
			time.Sleep(1200 * time.Millisecond) // Cross a full lease while inside the provider.
			if err := peer.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			before := assertAdmissionState(t, store, id, state.StatusStopping)
			if before.CleanupSandboxDone || before.WatchToken == "" || fake.stoppedCount() != 0 || lists.Load() != 0 {
				t.Fatal("peer repeated an in-flight Sandbox stop")
			}
			close(sandboxRelease)
			select {
			case <-started:
			case <-ctx.Done():
				t.Fatal("original stop never reached GitHub")
			}
			peer.runnerExitedForAttempt(id, runnerAttemptIdentity{sandboxID: "original", processPID: 17}, sandboxrunner.ExitResult{ExitCode: 23}, nil)
			if err := peer.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			live := assertAdmissionState(t, store, id, state.StatusStopping)
			if !live.CleanupSandboxDone || live.CleanupGitHubRunnerID != 71 || live.WatchToken == "" || deletes.Load() != 1 || fake.stoppedCount() != 0 {
				t.Fatal("healthy cleanup was stolen or its checkpoint lost")
			}
			if action == "kill" {
				err = cmd.Process.Kill()
			} else {
				err = cmd.Process.Signal(syscall.SIGSTOP)
			}
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			for ctx.Err() == nil {
				peer.recoverExpiredRunnerWatches(ctx)
				current, readErr := store.ReadState(id)
				if readErr != nil {
					t.Fatal(readErr)
				}
				if current.Status == state.StatusCompleted {
					break
				}
				time.Sleep(30 * time.Millisecond)
			}
			got := assertAdmissionState(t, store, id, state.StatusCompleted)
			if got.TerminationSource != state.TerminationSourceManualStop || got.RunnerExitCode == nil || *got.RunnerExitCode != 23 || !got.CleanupGitHubDone || !got.StoppingAt.Equal(live.StoppingAt) || got.WatchToken != "" || fake.stoppedCount() != 0 || lists.Load() != 1 || deletes.Load() != 2 {
				t.Fatalf("incorrect takeover status=%s lists=%d deletes=%d stops=%d", got.Status, lists.Load(), deletes.Load(), fake.stoppedCount())
			}
			t.Logf("%s to completed cleanup: %v", action, time.Since(start).Round(time.Millisecond))
			if action == "pause" {
				if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
					t.Fatal(err)
				}
				// Release the original HTTP response and observe its late callback.
				api.CloseClientConnections()
				if err := cmd.Wait(); err != nil {
					t.Fatal(err)
				}
				after := assertAdmissionState(t, store, id, state.StatusCompleted)
				if after.Version != got.Version || lists.Load() != 1 || deletes.Load() != 2 {
					t.Fatal("resumed owner changed the completed attempt")
				}
			}
		})
	}
}

func TestManualStopCancelsInterruptedCreationRequeue(t *testing.T) {
	store := state.New(t.TempDir())
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "interrupted-cleanup"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status, st.SandboxID, st.ProvisionAttemptID = state.StatusCreating, "orphan", "original"
	st.FailureStage, st.FailureReason = "recovery", interruptedCreateCleanup
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	first := &fakeSandbox{stopBlock: release, stopStarted: entered}
	a := newRunnerLifecycleTestServer(t, store, "", first)
	a.cfg.SandboxStopTimeout = 10 * time.Second
	peer := &fakeSandbox{}
	b := newRunnerLifecycleTestServer(t, store, "", peer)
	done := make(chan error, 1)
	go func() { done <- a.recoverRunner(t.Context(), st.ID) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("orphan cleanup never reached Sandbox")
	}
	current, _, err := b.stopRunner(t.Context(), st.ID, github.WorkflowJob{}, state.TerminationSourceManualStop)
	close(release)
	ownerErr := <-done
	if err != nil || current.Status != state.StatusStopping || current.FailureReason == interruptedCreateCleanup || !errors.Is(ownerErr, state.ErrConflict) {
		t.Fatalf("manual stop did not supersede the requeue: status=%s err=%v ownerErr=%v", current.Status, err, ownerErr)
	}
	if err := b.recoverRunner(t.Context(), st.ID); err != nil {
		t.Fatal(err)
	}
	got := assertAdmissionState(t, store, st.ID, state.StatusCompleted)
	if !got.CleanupSandboxDone || first.stoppedCount() != 1 || peer.stoppedCount() != 0 {
		t.Fatal("orphan Sandbox cleaned twice")
	}
}
