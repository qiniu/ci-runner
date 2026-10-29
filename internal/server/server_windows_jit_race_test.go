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
	newStarted chan sandboxrunner.StartInput
	releaseOld chan struct{}
	oldErr     error
}

func (s *supersededWindowsSandbox) StartRunner(_ context.Context, input sandboxrunner.StartInput) (sandboxrunner.StartResult, error) {
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
	s.newStarted <- input
	return sandboxrunner.StartResult{SandboxID: "new-sandbox", PID: 42}, nil
}

func (s *supersededWindowsSandbox) StopRunner(_ context.Context, sandboxID string, _ uint32) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = append(s.stopped, sandboxID)
	return nil
}

// Value: protects=a superseded Windows start cannot mutate a replacement attempt;
// fails_when=late failure overwrites running state or cleanup deletes its JIT identity;
// why_new=synchronous JIT failure tests never overlap two attempts; seam=none;
// no_claim=GitHub and Sandbox provider acceptance (both providers are mocked).
func TestWindowsJITSupersededSandboxStartPreservesReplacement(t *testing.T) {
	for _, lateResult := range []string{"success", "error"} {
		t.Run(lateResult, func(t *testing.T) {
			const requestID = "win-jit-race"
			const runnerName = "e2b-" + requestID
			var ghMu sync.Mutex
			registered := map[int64]bool{}
			var deletes []int64
			nextID := int64(70)
			ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ghMu.Lock()
				defer ghMu.Unlock()
				switch {
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
				newStarted:              make(chan sandboxrunner.StartInput, 1),
				releaseOld:              make(chan struct{}),
			}
			if lateResult == "error" {
				service.oldErr = errors.New("temporary network error")
			}
			srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, service)
			createLifecycleRequest(t, store, requestID, "windows", 0)
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
				srv.startRunner(ctx, requestID, "old-worker")
			}()
			await(service.oldStarted, "old Sandbox start did not block")
			old, err := store.ReadState(requestID)
			if err != nil {
				t.Fatal(err)
			}
			srv.retryOrFail(old, "sweeper_create_timeout", context.DeadlineExceeded)
			newDone := make(chan struct{})
			go func() {
				defer close(newDone)
				srv.startRunner(ctx, requestID, "new-worker")
			}()
			var input sandboxrunner.StartInput
			select {
			case input = <-service.newStarted:
			case <-ctx.Done():
				t.Fatal("replacement Sandbox did not start")
			}
			waitForState(t, store, requestID, state.StatusRunning)
			before, err := store.ReadState(requestID)
			if err != nil {
				t.Fatal(err)
			}
			release()
			await(oldDone, "superseded start did not return")
			after, err := store.ReadState(requestID)
			if err != nil {
				t.Fatal(err)
			}
			if after.Status != state.StatusRunning || after.SandboxID != "new-sandbox" || after.ProcessPID != 42 || !after.LastAttemptAt.Equal(before.LastAttemptAt) || after.Version != before.Version {
				t.Errorf("superseded %s changed replacement: before=%+v after=%+v", lateResult, before, after)
			}
			ghMu.Lock()
			oldDeletes, newDeleted := 0, false
			for _, id := range deletes {
				if id == 71 {
					oldDeletes++
				}
				newDeleted = newDeleted || id == 72
			}
			newRegistered := registered[72]
			ghMu.Unlock()
			if oldDeletes != 2 || newDeleted || !newRegistered {
				t.Errorf("JIT cleanup ownership lost: oldDeletes=%d newDeleted=%t newRegistered=%t", oldDeletes, newDeleted, newRegistered)
			}
			service.mu.Lock()
			stopped := append([]string(nil), service.stopped...)
			service.mu.Unlock()
			if lateResult == "success" && (len(stopped) != 1 || stopped[0] != "old-sandbox") {
				t.Errorf("late Sandbox cleanup = %v, want only old-sandbox", stopped)
			}
			if lateResult == "error" && len(stopped) != 0 {
				t.Errorf("failed old start stopped another Sandbox: %v", stopped)
			}
			// Finish the replacement only after all ownership assertions.
			input.OnExit(sandboxrunner.ExitResult{}, nil)
			await(newDone, "replacement start did not finish")
		})
	}
}
