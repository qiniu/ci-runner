package server

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestWindowsJITRecoveryCleansRegistrationBeforeRequeue(t *testing.T) {
	for _, scenario := range []string{"missing sandbox", "missing process", "cleanup failure"} {
		t.Run(scenario, func(t *testing.T) {
			var registered, cleanupUnavailable, jobCompleted atomic.Bool
			registered.Store(true) // Persisted creating request after JIT POST, before adoption.
			cleanupUnavailable.Store(scenario == "cleanup failure")
			var deletes atomic.Int64
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/jobs/123":
					if jobCompleted.Load() {
						_, _ = w.Write([]byte(`{"id":123,"status":"completed","conclusion":"success"}`))
					} else {
						_, _ = w.Write([]byte(`{"id":123,"status":"queued"}`))
					}
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/runners":
					if registered.Load() {
						_, _ = w.Write([]byte(`{"runners":[{"id":71,"name":"e2b-recover-jit"}]}`))
					} else {
						_, _ = w.Write([]byte(`{"runners":[]}`))
					}
				case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/actions/runners/71":
					deletes.Add(1)
					if cleanupUnavailable.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					registered.Store(false)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected GitHub call: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer gh.Close()
			store := state.New(t.TempDir())
			fake := &fakeSandbox{recoverErr: sandboxrunner.ErrSandboxNotFound}
			if scenario == "missing process" {
				fake.recoverErr = sandboxrunner.ErrRunnerNotFound
				fake.recoverResult = sandboxrunner.StartResult{SandboxID: "interrupted-sandbox"}
			}
			srv := newTestServer(t, store, gh.URL, fake)
			srv.Close() // Drive recovery and startup deterministically.
			upsertLifecycleProfile(t, store, state.RunnerProfile{Name: "windows", Labels: []string{"self-hosted", "windows"}, Enabled: true, TemplateID: "win"})
			_, st, err := store.CreateRequest(state.RunnerRequest{ID: "recover-jit", RepositoryFullName: "o/r", RunnerName: "e2b-recover-jit", ProfileName: "windows", Labels: []string{"self-hosted", "windows"}, JobID: 123}, nil)
			if err != nil {
				t.Fatal(err)
			}
			st.Status = state.StatusCreating
			st.LastAttemptAt = time.Now().UTC().Truncate(time.Millisecond)
			if err = store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			err = srv.recoverRunner(t.Context(), st.ID)
			current, readErr := store.ReadState(st.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if scenario == "cleanup failure" {
				if err == nil || current.Status != state.StatusStopping || current.FailureReason != "jit_cleanup_pending" || current.NextRetryAt.IsZero() || !registered.Load() {
					t.Fatalf("lost pending cleanup: status=%s reason=%s err=%v", current.Status, current.FailureReason, err)
				}
				cleanupUnavailable.Store(false)
				current.NextRetryAt = time.Now().UTC().Add(-time.Minute)
				current.StoppingAt = time.Now().UTC().Add(-time.Minute)
				if err = store.WriteState(current); err != nil {
					t.Fatal(err)
				}
				srv.sweepOnce(t.Context())
				current, _ = store.ReadState(st.ID)
				if registered.Load() || current.Status != state.StatusFailed || deletes.Load() != 2 {
					t.Fatalf("sweeper did not finish pending cleanup: status=%s deletes=%d", current.Status, deletes.Load())
				}
				return
			}
			if err != nil || current.Status != state.StatusQueued || registered.Load() || deletes.Load() != 1 {
				t.Fatalf("requeued without cleanup: status=%s deletes=%d err=%v", current.Status, deletes.Load(), err)
			}
			if scenario == "missing process" && fake.stoppedCount() != 1 {
				t.Fatal("interrupted Sandbox was not stopped")
			}
			// Completion in the next startup precheck must not bypass old JIT cleanup.
			jobCompleted.Store(true)
			srv.startRunner(t.Context(), st.ID, "worker")
			current, _ = store.ReadState(st.ID)
			if current.Status != state.StatusCompleted || registered.Load() || deletes.Load() != 1 {
				t.Fatalf("completed request retained registration: status=%s deletes=%d", current.Status, deletes.Load())
			}
		})
	}
}
