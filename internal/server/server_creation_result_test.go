package server

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type lostCreateResponse struct {
	*fakeSandbox
	recoverInPeer func(sandboxrunner.StartInput)
}

func (p *lostCreateResponse) StartRunner(_ context.Context, input sandboxrunner.StartInput) (sandboxrunner.StartResult, error) {
	p.recoverInPeer(input)
	return sandboxrunner.StartResult{}, errors.New("connection reset after Sandbox creation")
}

type failingBootstrapSandbox struct {
	*fakeSandbox
	failure      error
	operation    string
	starts       int
	lostResponse bool
	beforeStop   func()
}

func (p *failingBootstrapSandbox) StartRunner(ctx context.Context, input sandboxrunner.StartInput) (sandboxrunner.StartResult, error) {
	p.starts++
	id := fmt.Sprintf("bootstrap-%d", p.starts)
	if p.lostResponse {
		p.recoverResult.SandboxID = id // Only the subsequent metadata match identifies the allocation.
		return sandboxrunner.StartResult{}, sandboxrunner.ErrCreateOutcomeUnknown
	}
	if err := input.RecordSandbox(ctx, id); err != nil {
		return sandboxrunner.StartResult{}, err
	}
	return sandboxrunner.StartResult{SandboxID: id, BootstrapOperation: p.operation}, fmt.Errorf("%w: %w", sandboxrunner.ErrCreateOutcomeUnknown, p.failure)
}

func (p *failingBootstrapSandbox) StopRunner(ctx context.Context, sandboxID string, pid uint32) error {
	if p.beforeStop != nil {
		p.beforeStop()
	}
	return p.fakeSandbox.StopRunner(ctx, sandboxID, pid)
}

func TestReconciledBootstrapFailureRespectsRetryBudget(t *testing.T) {
	for _, tc := range []struct {
		name, message, code, operation string
		retries                        int
	}{
		{"deterministic", "status 403: sensitive provider detail", "auth_error", "write runner script", 0},
		{"transient", "connection reset: sensitive provider detail", "temporary_network_error", "write runner script", 1},
		{"rate limited after creation", "status 429", "http_rate_limited", "write runner script", 1},
		{"creator lost before retaining cause", "EOF", "runner_missing_after_create", "", 1},
		{"located Sandbox disappeared before reconnect", "", "runner_missing_after_create", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const id = "bootstrap"
			store := state.New(t.TempDir())
			if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") == "1" {
				store = state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: catalogServerTestDSN(t), MigrateOnStart: true})
			}
			upsertLifecycleProfile(t, store, state.RunnerProfile{Name: "linux", Labels: []string{"self-hosted", "linux"}, TemplateID: "test-template", Enabled: true})
			createLifecycleRequest(t, store, id, "linux", 0)
			api := httptest.NewServer(githubRunnerAPI(t))
			defer api.Close()
			provider := &failingBootstrapSandbox{fakeSandbox: &fakeSandbox{recoverErr: sandboxrunner.ErrRunnerNotFound}, failure: errors.New(tc.message), operation: tc.operation}
			var previousCleanupToken string
			provider.beforeStop = func() {
				current, err := store.ReadState(id)
				if err != nil || current.Status != state.StatusStopping || current.WatchToken == "" {
					t.Fatalf("missing durable cleanup owner: status=%s error=%v", current.Status, err)
				}
				if previousCleanupToken != "" {
					if current.WatchToken == previousCleanupToken {
						t.Fatal("new cleanup reused the former attempt's owner")
					}
					if err := store.RecordRunnerCleanup(t.Context(), id, previousCleanupToken, state.CleanupSandbox); !errors.Is(err, state.ErrConflict) {
						t.Fatalf("old receipt completed a new stopping attempt: %v", err)
					}
					if err := store.BindCleanupGitHubRunner(t.Context(), id, previousCleanupToken, 999); !errors.Is(err, state.ErrConflict) {
						t.Fatalf("old binding changed a new stopping attempt: %v", err)
					}
				}
				previousCleanupToken = current.WatchToken
			}
			if tc.name == "located Sandbox disappeared before reconnect" {
				provider.lostResponse = true
				provider.recoverErr = sandboxrunner.ErrSandboxNotFound
				provider.recoverHook = func(input sandboxrunner.RecoverInput) {
					saved, err := store.ReadState(id)
					if err != nil || input.SandboxID != provider.recoverResult.SandboxID || saved.SandboxID != input.SandboxID {
						t.Errorf("reconnecting without persisted exact ID: input=%q saved=%q error=%v", input.SandboxID, saved.SandboxID, err)
					}
				}
			}
			srv := newRunnerLifecycleTestServer(t, store, api.URL, provider)
			// Keep the creation lease live long enough to checkpoint the failure
			// under race/coverage instrumentation, then let recovery take over.
			// A 150ms lease can lose the cause before this fixture records it.
			srv.cfg.WorkerLeaseTTL = time.Second
			srv.cfg.RetryBaseDelay, srv.cfg.RetryMaxDelay, srv.cfg.RetryMaxAttempts = 20*time.Millisecond, 20*time.Millisecond, 1

			for index := 0; index <= tc.retries; index++ {
				deadline := time.Now().Add(10 * time.Second)
				for {
					_, claim, ok, err := store.ClaimNextRunnable("creator", time.Minute)
					if err != nil || time.Now().After(deadline) {
						t.Fatalf("cannot claim attempt %d: %v", index, err)
					}
					if ok {
						srv.startClaimedRunner(t.Context(), claim)
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
				st, err := store.ReadState(id)
				if err != nil || st.Status != state.StatusCreating || (st.SandboxID == "") != provider.lostResponse || st.RetryCount != index {
					t.Fatalf("attempt not retained for cleanup: %#v, %v", st, err)
				}
				if index > 0 {
					if st.TerminationSource != "" || st.RunnerExitCode != nil || st.CleanupSandboxDone || st.CleanupGitHubDone || st.CleanupGitHubRunnerID != 0 || st.WatchToken == previousCleanupToken {
						t.Fatalf("new bootstrap attempt inherited cleanup state: source=%q watch=%q old=%q", st.TerminationSource, st.WatchToken, previousCleanupToken)
					}
					if previousCleanupToken != "" {
						for _, step := range []state.RunnerCleanupStep{state.CleanupSandbox, state.CleanupGitHub} {
							if err := store.RecordRunnerCleanup(t.Context(), id, previousCleanupToken, step); !errors.Is(err, state.ErrConflict) {
								t.Fatalf("old cleanup receipt accepted: step=%s error=%v", step, err)
							}
						}
						if err := store.BindCleanupGitHubRunner(t.Context(), id, previousCleanupToken, 999); !errors.Is(err, state.ErrConflict) {
							t.Fatalf("old cleanup runner binding accepted: %v", err)
						}
						after, err := store.ReadState(id)
						if err != nil || after.Version != st.Version || after.SandboxID != st.SandboxID || after.CleanupSandboxDone || after.CleanupGitHubDone || after.CleanupGitHubRunnerID != 0 {
							t.Fatalf("old receipt changed replacement: %+v, %v", after, err)
						}
					}
				}
				for st.Status == state.StatusCreating {
					if err := srv.recoverRunner(t.Context(), id); err != nil {
						t.Fatal(err)
					}
					st, err = store.ReadState(id)
					if err != nil || time.Now().After(deadline) {
						t.Fatalf("cleanup did not settle: %#v, %v", st, err)
					}
					if st.Status == state.StatusCreating {
						time.Sleep(10 * time.Millisecond)
					}
				}
				wantMessage := "Sandbox creation ended without a Runner"
				if tc.operation != "" {
					wantMessage = "Sandbox bootstrap failed: " + tc.operation
				}
				if st.LastErrorCode != tc.code || st.LastErrorMessage != wantMessage || st.SandboxID != "" || st.SandboxCreateDispatched {
					t.Fatalf("incorrect failure evidence: %#v", st)
				}
				if index < tc.retries {
					if st.Status != state.StatusQueued || st.RetryCount != index+1 || st.NextRetryAt.IsZero() || st.TerminationSource != "" || st.RunnerExitCode != nil || st.WatchToken != "" || st.CleanupSandboxDone || st.CleanupGitHubDone || st.CleanupGitHubRunnerID != 0 {
						t.Fatalf("expected bounded delayed retry: %#v", st)
					}
				} else {
					expectedStops := provider.starts
					if provider.lostResponse {
						expectedStops = 0 // Connect proved the located Sandbox already disappeared.
					}
					if st.Status != state.StatusFailed || st.RetryCount != tc.retries || provider.starts != index+1 || provider.stoppedCount() != expectedStops {
						t.Fatalf("expected terminal failure after %d attempts and %d stops: %#v", provider.starts, expectedStops, st)
					}
				}
			}
		})
	}
}

func TestReconciledBootstrapFailureRetainsTerminalEvidence(t *testing.T) {
	for _, retryable := range []bool{true, false} {
		t.Run(fmt.Sprint(retryable), func(t *testing.T) {
			exitCode := 1
			st := state.RunnerState{
				Status: state.StatusStopping, FailureStage: pendingBootstrapFailure,
				LastErrorCode: "bootstrap_failure", LastErrorMessage: "test", LastErrorRetryable: retryable,
				TerminationSource: state.TerminationSourceRecoveryCleanup, RunnerExitCode: &exitCode,
			}
			srv := &Server{}
			srv.cfg.RetryMaxAttempts = 1
			srv.cfg.RetryBaseDelay = time.Second
			srv.applyReconciledBootstrapFailure(&st)
			if retryable {
				if st.Status != state.StatusQueued || st.TerminationSource != "" || st.RunnerExitCode != nil {
					t.Fatalf("retry retained old termination evidence: %+v", st)
				}
			} else if st.Status != state.StatusFailed || st.TerminationSource != state.TerminationSourceRecoveryCleanup || st.RunnerExitCode == nil || *st.RunnerExitCode != exitCode {
				t.Fatalf("terminal bootstrap failure lost termination evidence: %+v", st)
			}
		})
	}
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
		phase := os.Getenv("RUNNERD_CREATION_RESULT_PHASE")
		if phase == "original" {
			svc := &lostCreateResponse{fakeSandbox: &fakeSandbox{}, recoverInPeer: func(input sandboxrunner.StartInput) {
				if err := os.WriteFile(filepath.Join(dir, "accepted"), []byte(input.ProvisionAttemptID), 0o600); err != nil {
					t.Fatal(err)
				}
				<-t.Context().Done() // Parent kills the process before the provider returns.
			}}
			srv := newRunnerLifecycleTestServer(t, store, os.Getenv("RUNNERD_CREATION_RESULT_GITHUB"), svc)
			srv.cfg.WorkerLeaseTTL = time.Second
			srv.startClaimedRunner(t.Context(), queuedRunnerForTest(t, store, id, "original"))
			return
		}
		fake := &fakeSandbox{recoverResult: sandboxrunner.StartResult{SandboxID: "adopted", PID: 49}}
		if phase == "invisible" {
			fake.recoverResult = sandboxrunner.StartResult{}
			fake.recoverErr = sandboxrunner.ErrSandboxNotFound
		}
		fake.recoverHook = func(input sandboxrunner.RecoverInput) {
			receipt, err := os.ReadFile(filepath.Join(dir, "accepted"))
			if err != nil || string(receipt) == "" || string(receipt) != input.ProvisionAttemptID {
				t.Errorf("recovery did not ask for the accepted attempt")
			}
		}
		srv := newRunnerLifecycleTestServer(t, store, "", fake)
		srv.cfg.WorkerLeaseTTL = time.Second
		srv.cfg.SandboxCreateTimeout = time.Nanosecond
		if phase == "live" {
			before := assertAdmissionState(t, store, id, state.StatusCreating)
			if !before.SandboxCreateDispatched || before.WatchToken == "" {
				t.Fatal("missing dispatch or lease")
			}
			// Cross at least one renewal, then run both startup and timeout paths.
			time.Sleep(1500 * time.Millisecond)
			if err := srv.Recover(t.Context()); err != nil {
				t.Fatal(err)
			}
			srv.sweepOnce(t.Context())
			after := assertAdmissionState(t, store, id, state.StatusCreating)
			if fake.recoveredCount() != 0 || after.Version != before.Version || after.RetryCount != 0 {
				t.Fatal("healthy provider call was interrupted")
			}
			return
		}
		end := time.Now().Add(8 * time.Second)
		for time.Now().Before(end) {
			srv.recoverExpiredRunnerWatches(t.Context())
			if phase == "invisible" {
				got := assertAdmissionState(t, store, id, state.StatusCreating)
				if got.LastErrorCode == "sandbox_create_outcome_unknown" {
					if !got.SandboxCreateDispatched || got.ProvisionAttemptID == "" || got.RetryCount != 0 || fake.stoppedCount() != 0 || fake.recoveredCount() != 0 {
						t.Fatal("unknown create result was abandoned or reconnected without an ID")
					}
					return
				}
			} else if fake.recoveredCount() == 1 {
				break
			}
			time.Sleep(30 * time.Millisecond)
		}
		if fake.recoveredCount() != 1 {
			t.Fatal("expected a single recovery")
		}
		assertAdmissionState(t, store, id, state.StatusRunning)
		return
	}
	t.Run("late response", func(t *testing.T) { verifyCreatingTakeover(t, id, "late") })
	t.Run("process killed", func(t *testing.T) { verifyCreatingTakeover(t, id, "kill") })
	t.Run("stop after crash", func(t *testing.T) { verifyCreatingTakeover(t, id, "stop") })
}

func verifyCreatingTakeover(t *testing.T, id string, scenario string) {
	t.Helper()
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
	startCtx, stopOwner := context.WithCancel(t.Context())
	defer stopOwner()
	peer := func(ctx context.Context, phase string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestLateStartFailureAfterPeerAdoption$")
		cmd.Env = append(os.Environ(), "RUNNERD_CREATION_RESULT_PEER="+dir, "RUNNERD_CREATION_RESULT_DSN="+dsn, "RUNNERD_CREATION_RESULT_PHASE="+phase, "RUNNERD_CREATION_RESULT_GITHUB="+api.URL)
		return cmd
	}
	runPeer := func(phase string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := peer(ctx, phase)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("peer %s: %v\n%s", phase, err, out)
		}
	}
	if scenario != "late" {
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		owner := peer(ctx, "original")
		if err := owner.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if owner.ProcessState == nil {
				_ = owner.Process.Kill()
				_ = owner.Wait()
			}
		})
		var accepted []byte
		for ctx.Err() == nil {
			accepted, _ = os.ReadFile(filepath.Join(dir, "accepted"))
			if len(accepted) > 0 {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		if len(accepted) == 0 {
			t.Fatal("provider did not accept creation")
		}
		if err := owner.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		if err := owner.Wait(); err == nil {
			t.Fatal("expected killed process")
		}
		killed := time.Now()
		runPeer("invisible")
		if scenario == "stop" {
			missing := &fakeSandbox{}
			stopping := newRunnerLifecycleTestServer(t, store, api.URL, missing)
			st, _, err := stopping.stopRunner(t.Context(), id, github.WorkflowJob{}, state.TerminationSourceManualStop)
			if err != nil || st.Status != state.StatusStopping || st.LastErrorCode != "sandbox_create_outcome_unknown" || missing.stoppedCount() != 0 {
				t.Fatalf("stop lost outstanding creation: status=%s err=%v", st.Status, err)
			}
			visible := &fakeSandbox{recoverResult: sandboxrunner.StartResult{SandboxID: "adopted"}}
			next := newRunnerLifecycleTestServer(t, store, api.URL, visible)
			if err := next.recoverRunner(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			done := assertAdmissionState(t, store, id, state.StatusCompleted)
			if done.SandboxID != "adopted" || done.TerminationSource != state.TerminationSourceManualStop || visible.stoppedCount() != 1 || done.ProvisionAttemptID != string(accepted) {
				t.Fatal("stop did not reconcile the original create")
			}
			return
		}
		runPeer("visible")
		got := assertAdmissionState(t, store, id, state.StatusRunning)
		if got.ProvisionAttemptID != string(accepted) || got.SandboxID != "adopted" || got.ProcessPID != 49 || got.RetryCount != 0 {
			t.Fatal("failed to adopt accepted creation")
		}
		t.Logf("killed creating process to adopted Sandbox after one invisible lookup: %s", time.Since(killed).Round(time.Millisecond))
		return
	}
	provider := &lostCreateResponse{fakeSandbox: &fakeSandbox{}, recoverInPeer: func(input sandboxrunner.StartInput) {
		if err := os.WriteFile(filepath.Join(dir, "accepted"), []byte(input.ProvisionAttemptID), 0o600); err != nil {
			t.Fatal(err)
		}
		runPeer("live")
		stopOwner() // The in-flight provider call can still return after loss of its owner.
		runPeer("invisible")
		runPeer("visible")
		adopted = assertAdmissionState(t, store, id, state.StatusRunning)
	}}
	srv := newRunnerLifecycleTestServer(t, store, api.URL, provider)
	srv.cfg.WorkerLeaseTTL = time.Second
	srv.startClaimedRunner(startCtx, queuedRunnerForTest(t, store, id, "original"))
	got := assertAdmissionState(t, store, id, state.StatusRunning)
	if adopted.ProvisionAttemptID == "" || got.Version != adopted.Version || got.ProvisionAttemptID != adopted.ProvisionAttemptID || got.SandboxID != "adopted" || got.ProcessPID != 49 || got.RetryCount != 0 {
		t.Fatalf("late failure changed adopted attempt: status=%s sandbox=%s pid=%d version=%d (adopted %d)", got.Status, got.SandboxID, got.ProcessPID, got.Version, adopted.Version)
	}
}
