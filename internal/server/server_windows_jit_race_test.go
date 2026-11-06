package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type supersededWindowsSandbox struct {
	*lifecycleSandboxService
	mu         sync.Mutex
	starts     int
	stopped    []string
	oldStarted chan struct{}
	releaseOld chan struct{}
	oldErr     error
}

func (s *supersededWindowsSandbox) StartRunner(_ context.Context, _ sandboxrunner.StartInput) (sandboxrunner.StartResult, error) {
	s.mu.Lock()
	s.starts++
	attempt := s.starts
	s.mu.Unlock()
	if attempt == 1 {
		close(s.oldStarted)
		<-s.releaseOld
		if s.oldErr != nil {
			return sandboxrunner.StartResult{}, s.oldErr
		}
		return sandboxrunner.StartResult{SandboxID: "old-sandbox", PID: 41}, nil
	}
	return sandboxrunner.StartResult{}, errors.New("unexpected replacement Sandbox start")
}

func (s *supersededWindowsSandbox) StopRunner(_ context.Context, sandboxID string, _ uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = append(s.stopped, sandboxID)
	return nil
}

// A second server stops the attempt while its Sandbox start is in flight.
// Exercise both outcomes and an unavailable GitHub registration cleanup.
func TestWindowsJITPendingSandboxStopBlocksReplacement(t *testing.T) {
	for _, lateResult := range []string{"success", "error", "cleanup failure"} {
		t.Run(lateResult, func(t *testing.T) {
			const requestID = "win-jit-race"
			const runnerName = "e2b-" + requestID
			var ghMu sync.Mutex
			registered := map[int64]bool{}
			var deletes []int64
			var cleanupUnavailable, jobCompleted atomic.Bool
			cleanupUnavailable.Store(lateResult == "cleanup failure")
			nextID := int64(70)
			ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ghMu.Lock()
				defer ghMu.Unlock()
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/jobs/123":
					if jobCompleted.Load() {
						_, _ = w.Write([]byte(`{"id":123,"status":"completed","conclusion":"success"}`))
					} else {
						_, _ = w.Write([]byte(`{"id":123,"status":"queued"}`))
					}
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/runners/downloads":
					_, _ = w.Write([]byte(`[{"os":"win","architecture":"x64","filename":"actions-runner-win-x64-2.337.0.zip","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-win-x64-2.337.0.zip","sha256_checksum":"1150692afa94e71f872017e254ea55b6eece1eece3fe7e3a6d4c93d0a1b85cfc"}]`))
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/runners":
					runners := make([]github.Runner, 0)
					for id := range registered {
						runners = append(runners, github.Runner{ID: id, Name: runnerName})
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"runners": runners})
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/actions/runners/generate-jitconfig":
					nextID++
					registered[nextID] = true
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]any{"runner": github.Runner{ID: nextID, Name: runnerName}, "encoded_jit_config": jitTestConfig(t, runnerName, strconv.FormatInt(nextID, 10))})
				case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/repos/o/r/actions/runners/"):
					id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/repos/o/r/actions/runners/"), 10, 64)
					if err != nil {
						t.Error(err)
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					deletes = append(deletes, id)
					if cleanupUnavailable.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					delete(registered, id)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer ghServer.Close()

			store := state.New(t.TempDir())
			upsertLifecycleProfile(t, store, state.RunnerProfile{Name: "windows", Labels: []string{"self-hosted", "windows"}, TemplateID: "windows-template", MaxConcurrency: 1, Enabled: true})
			service := &supersededWindowsSandbox{
				lifecycleSandboxService: &lifecycleSandboxService{},
				oldStarted:              make(chan struct{}),
				releaseOld:              make(chan struct{}),
			}
			if lateResult == "error" {
				service.oldErr = errors.New("temporary network error")
			}
			srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, service)
			// Let the next worker claim the retry as soon as the database clock
			// reaches the scheduled time.
			srv.cfg.RetryBaseDelay = 0
			srv.cfg.RetryMaxDelay = 0
			if lateResult == "cleanup failure" {
				if _, _, err := store.CreateRequest(state.RunnerRequest{ID: requestID, RepositoryFullName: "o/r", RunnerName: runnerName, ProfileName: "windows", Labels: []string{"self-hosted", "windows"}, JobID: 123}, nil); err != nil {
					t.Fatal(err)
				}
			} else {
				createLifecycleRequest(t, store, requestID, "windows", 0)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(service.releaseOld) }) }
			defer release()
			await := func(done <-chan struct{}, message string) {
				t.Helper()
				select {
				case <-done:
				case <-ctx.Done():
					t.Fatal(message)
				}
			}
			oldDone := make(chan struct{})
			go func() {
				defer close(oldDone)
				srv.startClaimedRunner(ctx, queuedRunnerForTest(t, srv.store, requestID, "old-worker"))
			}()
			await(service.oldStarted, "old Sandbox start did not block")
			old, err := store.ReadState(requestID)
			if err != nil {
				t.Fatal(err)
			}
			srv.retryOrFail(old, "sweeper_create_timeout", context.DeadlineExceeded)
			peer := newRunnerLifecycleTestServer(t, store, ghServer.URL, service)
			current, err := store.ReadState(requestID)
			if err != nil || current.Status != state.StatusCreating || current.CleanupGitHubRunnerID != 71 {
				t.Fatalf("sweeper released unresolved Windows attempt: status=%s id=%d err=%v", current.Status, current.CleanupGitHubRunnerID, err)
			}
			if _, _, ok, err := store.ClaimNextRunnable("second", time.Minute); ok || err != nil {
				t.Fatal("replacement claimed before original settled", err)
			}
			pending, _, err := peer.stopRunner(ctx, requestID, github.WorkflowJob{}, state.TerminationSourceManualStop)
			if err != nil || pending.Status != state.StatusStopping {
				t.Fatalf("stop lost the original Sandbox: %s %v", pending.Status, err)
			}
			jobCompleted.Store(true)
			release()
			await(oldDone, "late start did not finish")
			// A lost stop response or definitive start rejection is reconciled
			// by another node. Only the original registration can be removed.
			cleanupUnavailable.Store(false)
			if err := peer.Recover(ctx); err != nil {
				t.Fatal(err)
			}
			final, err := store.ReadState(requestID)
			if err != nil || final.Status != state.StatusCompleted || final.TerminationSource != state.TerminationSourceManualStop || !final.CleanupGitHubDone {
				t.Fatalf("late response lost manual stop: %s %s %v", final.Status, final.TerminationSource, err)
			}
			ghMu.Lock()
			defer ghMu.Unlock()
			if nextID != 71 || len(registered) != 0 || len(deletes) == 0 {
				t.Fatalf("Windows registration leaked or was repeated: posts=%d remaining=%v deletes=%v", nextID-70, registered, deletes)
			}
			for _, id := range deletes {
				if id != 71 {
					t.Fatalf("deleted other registration %d", id)
				}
			}
			service.mu.Lock()
			defer service.mu.Unlock()
			if lateResult != "error" && (len(service.stopped) != 1 || service.stopped[0] != "old-sandbox") {
				t.Fatalf("late Sandbox cleanup %v", service.stopped)
			}
			if lateResult == "error" && len(service.stopped) != 0 {
				t.Fatalf("unexpected Sandbox cleanup %v", service.stopped)
			}
		})
	}
}
