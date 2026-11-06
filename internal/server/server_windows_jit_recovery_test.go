package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestWindowsJITRecoveryCleansRegistrationBeforeRequeue(t *testing.T) {
	for _, scenario := range []string{"missing sandbox", "missing process", "cleanup failure", "bootstrap cleanup failure", "bootstrap sandbox gone"} {
		t.Run(scenario, func(t *testing.T) {
			var registered, cleanupUnavailable, jobCompleted atomic.Bool
			registered.Store(true) // Persisted creating request after JIT POST, before adoption.
			bootstrapFailed := scenario == "bootstrap cleanup failure" || scenario == "bootstrap sandbox gone"
			cleanupUnavailable.Store(scenario == "cleanup failure" || bootstrapFailed)
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
			if bootstrapFailed && os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") == "1" {
				store = state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: catalogServerTestDSN(t), MigrateOnStart: true})
			}
			fake := &fakeSandbox{recoverErr: sandboxrunner.ErrSandboxNotFound}
			if scenario == "missing process" || scenario == "bootstrap cleanup failure" {
				fake.recoverErr = sandboxrunner.ErrRunnerNotFound
				fake.recoverResult = sandboxrunner.StartResult{SandboxID: "interrupted-sandbox"}
			}
			srv := newTestServerWithoutLoops(t, store, gh.URL, fake, 10)
			upsertLifecycleProfile(t, store, state.RunnerProfile{Name: "windows", Labels: []string{"self-hosted", "windows"}, Enabled: true, TemplateID: "win"})
			_, st, err := store.CreateRequest(state.RunnerRequest{ID: "recover-jit", RepositoryFullName: "o/r", RunnerName: "e2b-recover-jit", ProfileName: "windows", Labels: []string{"self-hosted", "windows"}, JobID: 123}, nil)
			if err != nil {
				t.Fatal(err)
			}
			st.Status = state.StatusCreating
			st.LastAttemptAt = time.Now().UTC().Truncate(time.Millisecond)
			if bootstrapFailed {
				st.ProvisionAttemptID = "attempt"
				st.FailureStage, st.LastErrorCode = pendingBootstrapFailure, "auth_error"
				st.LastErrorMessage = "Sandbox bootstrap failed: write runner script"
			}
			if err = store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			if bootstrapFailed {
				const ttl = 200 * time.Millisecond
				srv.cfg.WorkerLeaseTTL = ttl
				st = assertAdmissionState(t, store, st.ID, state.StatusCreating)
				ok, err := store.ClaimRunnerWatch(t.Context(), st, "creator", ttl)
				if err != nil || !ok {
					t.Fatalf("creator lease: %t %v", ok, err)
				}
				if err := store.DispatchSandboxCreate(t.Context(), st.ID, "creator", st.ProvisionAttemptID); err != nil {
					t.Fatal(err)
				}
				if err := store.RecordSandboxCreateResult(t.Context(), st, "interrupted-sandbox"); err != nil {
					t.Fatal(err)
				}
				waitExpiredRecoveryWatch(t, store, st.ID, state.StatusCreating)
			}
			err = srv.recoverRunner(t.Context(), st.ID)
			current, readErr := store.ReadState(st.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if bootstrapFailed {
				if err == nil || current.Status != state.StatusStopping || !current.CleanupSandboxDone || current.CleanupGitHubRunnerID != 71 || current.LastErrorRetryable || current.RetryCount != 0 || current.FailureStage != pendingBootstrapFailure || current.FailureReason != interruptedCreateCleanup || current.SandboxID != "interrupted-sandbox" || current.LastErrorCode != "auth_error" || !registered.Load() {
					t.Fatalf("lost bootstrap decision during JIT cleanup: %#v err=%v", current, err)
				}
				cleanupUnavailable.Store(false)
				waitExpiredRecoveryWatch(t, store, st.ID, state.StatusStopping)
				if err := srv.recoverRunner(t.Context(), st.ID); err != nil {
					t.Fatal(err)
				}
				current = assertAdmissionState(t, store, st.ID, state.StatusFailed)
				if current.LastErrorCode != "auth_error" || current.FailureStage != "sandbox_bootstrap" || current.RetryCount != 0 || current.SandboxCreateDispatched || registered.Load() || deletes.Load() != 2 {
					t.Fatalf("bootstrap outcome bypassed after JIT cleanup: %#v deletes=%d", current, deletes.Load())
				}
				return
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
				if registered.Load() || current.Status != state.StatusQueued || deletes.Load() != 2 {
					t.Fatalf("sweeper did not finish pending cleanup: registered=%t status=%s deletes=%d", registered.Load(), current.Status, deletes.Load())
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
			srv.startClaimedRunner(t.Context(), queuedRunnerForTest(t, srv.store, st.ID, "worker"))
			current, _ = store.ReadState(st.ID)
			if current.Status != state.StatusCompleted || registered.Load() || deletes.Load() != 1 {
				t.Fatalf("completed request retained registration: status=%s deletes=%d", current.Status, deletes.Load())
			}
		})
	}
}

func waitExpiredRecoveryWatch(t *testing.T, store state.Store, id, status string) {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		rows, err := store.ListExpiredRunnerWatches(t.Context(), 10)
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range rows {
			if row.ID == id && row.Status == status {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s recovery", status)
}
