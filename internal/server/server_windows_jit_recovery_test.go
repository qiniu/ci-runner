package server

import (
	"database/sql"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
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
			dir := t.TempDir()
			store := state.New(dir)
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
				if err := store.DispatchJITRegistration(t.Context(), st.ID, "creator", st.ProvisionAttemptID); err != nil {
					t.Fatal(err)
				}
				if err := store.RecordJITRegistrationResult(t.Context(), st, 71); err != nil {
					t.Fatal(err)
				}
				if err := store.DispatchSandboxCreate(t.Context(), st.ID, "creator", st.ProvisionAttemptID); err != nil {
					t.Fatal(err)
				}
				if err := store.RecordSandboxCreateResult(t.Context(), st, "interrupted-sandbox"); err != nil {
					t.Fatal(err)
				}
				waitExpiredRecoveryWatch(t, store, st.ID, state.StatusCreating)
			} else {
				// Released Windows rows predate the registration ledger.
				db, err := sql.Open("sqlite", filepath.Join(dir, "runnerd.db"))
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				if _, err := db.Exec("UPDATE runner_requests SET jit_registration_state = NULL WHERE id = ?", st.ID); err != nil {
					t.Fatal(err)
				}
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

// A Windows request stopped before its JIT POST owns no registration, even
// when a previous Runner with its reusable name is still visible to GitHub.
func TestWindowsJITStopBeforeDispatch(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unsent registration contacted GitHub: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer gh.Close()
	store := state.New(t.TempDir())
	srv := newRunnerLifecycleTestServer(t, store, gh.URL, &fakeSandbox{})
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "unsent-jit", RepositoryFullName: "o/r", Labels: []string{"windows"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.stopRunner(t.Context(), st.ID, github.WorkflowJob{}, state.TerminationSourceManualStop); err != nil {
		t.Fatal(err)
	}
	saved, err := store.ReadState(st.ID)
	if err != nil || saved.Status != state.StatusCompleted || !saved.CleanupGitHubDone || saved.CleanupGitHubRunnerID != 0 {
		t.Fatal("unsent registration did not finish cleanly", saved.Status, err)
	}
}

func TestWindowsJITLateRejectionAfterRecovery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     int
		retries    int
		cancel     string
		wantStatus string
		wantCode   string
	}{
		{"forbidden", http.StatusForbidden, 0, "", state.StatusFailed, "auth_error"},
		{"forbidden exhausted", http.StatusForbidden, 1, "", state.StatusFailed, "auth_error"},
		{"conflict retry", http.StatusConflict, 0, "", state.StatusQueued, "http_retryable_status"},
		{"conflict exhausted", http.StatusConflict, 1, "", state.StatusFailed, "http_retryable_status"},
		{"rate limited retry", http.StatusTooManyRequests, 0, "", state.StatusQueued, "http_rate_limited"},
		{"rate limited exhausted", http.StatusTooManyRequests, 1, "", state.StatusQueued, "http_rate_limited"},
		{"secondary limit exhausted", http.StatusForbidden, 1, "", state.StatusQueued, "github_secondary_rate_limit"},
		{"manual stop", http.StatusForbidden, 1, "manual", state.StatusCompleted, ""},
		{"job completed", http.StatusForbidden, 1, "completed", state.StatusCompleted, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "late-rejected"
			store := state.New(t.TempDir())
			if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") == "1" {
				store = state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: catalogServerTestDSN(t), MigrateOnStart: true})
			}
			req := state.RunnerRequest{ID: id, RepositoryFullName: "o/r", RunnerName: "e2b-late-rejected", JobID: 123, Labels: []string{"self-hosted", "windows"}}
			_, attempt, err := store.CreateRequest(req, nil)
			if err != nil {
				t.Fatal(err)
			}
			attempt.Status, attempt.ProvisionAttemptID, attempt.RetryCount = state.StatusCreating, "late-rejection-attempt", tc.retries
			if err := store.WriteState(attempt); err != nil {
				t.Fatal(err)
			}
			attempt, _ = store.ReadState(id)
			if ok, err := store.ClaimRunnerWatch(t.Context(), attempt, "creator", 100*time.Millisecond); !ok || err != nil {
				t.Fatal("creator lease", err)
			}
			attempt, _ = store.ReadState(id)
			accepted, release := make(chan struct{}), make(chan struct{})
			finish := sync.OnceFunc(func() { close(release) })
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodPost:
					close(accepted)
					<-release
					w.WriteHeader(tc.status)
					if tc.wantCode == "github_secondary_rate_limit" {
						w.Write([]byte("secondary rate limit: fixture-private-response"))
					} else {
						w.Write([]byte("fixture-private-response"))
					}
				case r.URL.Path == "/repos/o/r/actions/jobs/123":
					w.Write([]byte(`{"id":123,"status":"queued"}`))
				case r.URL.Path == "/repos/o/r/actions/runners":
					w.Write([]byte(`{"runners":[]}`))
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer api.Close()
			defer finish()
			creator := newRunnerLifecycleTestServer(t, store, api.URL, &fakeSandbox{})
			peer := newRunnerLifecycleTestServer(t, store, api.URL, &fakeSandbox{})
			creator.cfg.WorkerLeaseTTL = 100 * time.Millisecond
			peer.cfg.WorkerLeaseTTL = 100 * time.Millisecond
			peer.cfg.RetryMaxAttempts = 1
			done := make(chan error, 1)
			go func() { _, err := creator.prepareWindowsJIT(t.Context(), req, attempt); done <- err }()
			select {
			case <-accepted:
			case <-time.After(3 * time.Second):
				t.Fatal("JIT POST did not start")
			}
			waitExpiredRecoveryWatch(t, store, id, state.StatusCreating)
			if err := peer.recoverRunner(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			pending := assertAdmissionState(t, store, id, state.StatusStopping)
			if pending.FailureReason != "jit_cleanup_pending" || pending.JITRegistrationState != state.JITDispatched {
				t.Fatalf("missing pending receipt: %+v", pending)
			}
			finish()
			if err := <-done; err == nil {
				t.Fatal("expected JIT rejection")
			}
			receipt := assertAdmissionState(t, store, id, state.StatusStopping)
			if receipt.JITRegistrationState != state.JITRejected || !receipt.CleanupGitHubDone {
				t.Fatalf("rejection receipt not retained: %+v", receipt)
			}
			expectedCode := tc.wantCode
			if tc.cancel != "" {
				expectedCode = "auth_error"
			}
			if receipt.LastErrorCode != expectedCode || receipt.LastErrorMessage != "Windows JIT registration rejected" {
				t.Fatalf("rejection classification was not atomic: code=%q message=%q", receipt.LastErrorCode, receipt.LastErrorMessage)
			}
			if err := store.WriteState(pending); !errors.Is(err, state.ErrConflict) {
				t.Fatalf("old cleanup observation overwrote rejection: %v", err)
			}
			var job github.WorkflowJob
			source := state.TerminationSourceRecoveryCleanup
			if tc.cancel == "manual" {
				source = state.TerminationSourceManualStop
			}
			if tc.cancel == "completed" {
				job = github.WorkflowJob{ID: 123, Status: "completed", Conclusion: "cancelled"}
			}
			if _, _, err := peer.stopRunner(t.Context(), id, job, source); err != nil {
				t.Fatal(err)
			}
			current := assertAdmissionState(t, store, id, tc.wantStatus)
			wantRetries := tc.retries
			if tc.wantStatus == state.StatusQueued && wantRetries < 1 {
				wantRetries++
			}
			if current.RetryCount != wantRetries || current.LastErrorCode != tc.wantCode {
				t.Fatalf("late rejection bypassed its classification/budget: retry=%d code=%q", current.RetryCount, current.LastErrorCode)
			}
			if current.Status == state.StatusQueued && (current.NextRetryAt.IsZero() || current.TerminationSource != "" || current.RunnerExitCode != nil || !current.CreatingAt.IsZero() || !current.StoppingAt.IsZero()) {
				t.Fatalf("retry inherited cleanup or lost backoff: %+v", current)
			}
		})
	}
}

func TestWindowsJITRejectedReceiptSurvivesCreatorExit(t *testing.T) {
	store := state.New(t.TempDir())
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") == "1" {
		store = state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: catalogServerTestDSN(t), MigrateOnStart: true})
	}
	req := state.RunnerRequest{ID: "rejected-creator-exit", RepositoryFullName: "o/r", RunnerName: "e2b-rejected", JobID: 123, Labels: []string{"windows"}}
	_, attempt, err := store.CreateRequest(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	attempt.Status, attempt.ProvisionAttemptID = state.StatusCreating, "rejected-attempt"
	if err := store.WriteState(attempt); err != nil {
		t.Fatal(err)
	}
	attempt, _ = store.ReadState(req.ID)
	if ok, err := store.ClaimRunnerWatch(t.Context(), attempt, "creator", 100*time.Millisecond); !ok || err != nil {
		t.Fatal("creator lease", err)
	}
	attempt, _ = store.ReadState(req.ID)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusForbidden)
		case r.URL.Path == "/repos/o/r/actions/jobs/123":
			w.Write([]byte(`{"id":123,"status":"queued"}`))
		default:
			t.Errorf("rejected receipt must not query registrations: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer api.Close()
	creator := newRunnerLifecycleTestServer(t, store, api.URL, &fakeSandbox{})
	creator.cfg.WorkerLeaseTTL = 100 * time.Millisecond
	if _, err := creator.prepareWindowsJIT(t.Context(), req, attempt); err == nil {
		t.Fatal("expected definitive rejection")
	}
	peer := newRunnerLifecycleTestServer(t, store, api.URL, &fakeSandbox{})
	waitExpiredRecoveryWatch(t, store, req.ID, state.StatusCreating)
	if err := peer.recoverRunner(t.Context(), req.ID); err != nil {
		t.Fatal(err)
	}
	current := assertAdmissionState(t, store, req.ID, state.StatusFailed)
	if current.LastErrorCode != "auth_error" || current.RetryCount != 0 {
		t.Fatalf("recovery lost rejection: %+v", current)
	}
}
