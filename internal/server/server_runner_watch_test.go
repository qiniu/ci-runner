package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

// The SDK returns this result with a nil Go error when cancellation closes
// the stream without receiving a process-end event.
func assertCanceledWatchPreservesRunner(t *testing.T, store state.Store, id string, onExit func(sandboxrunner.ExitResult, error)) {
	t.Helper()
	before, err := store.ReadState(id)
	if err != nil || before.Status != state.StatusRunning {
		t.Fatalf("expected a running attempt: %#v, %v", before, err)
	}
	onExit(sandboxrunner.ExitResult{ExitCode: -1, Error: "context canceled"}, nil)
	after, err := store.ReadState(id)
	if err != nil || after.Status != state.StatusRunning || after.Version != before.Version || after.RunnerExitCode != nil {
		t.Fatalf("canceled observation changed the running attempt: %#v, %v", after, err)
	}
}

func TestCanceledStartWatchPreservesRunner(t *testing.T) {
	srv, store, batches := newStartupTimingFixture(t)
	claim := queuedRunnerForTest(t, store.Store, "timing-start", "test-worker")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.startClaimedRunner(ctx, claim)
	}()
	select {
	case <-batches:
	case <-time.After(5 * time.Second):
		t.Fatal("running attempt was not persisted")
	}
	cancel()
	awaitAdmissionTest(t, done, "start worker did not stop watching")
	assertCanceledWatchPreservesRunner(t, store, claim.ID, store.sandbox.startInputs()[0].OnExit)
}

func TestCanceledRecoveryWatchPreservesRunner(t *testing.T) {
	store := state.New(t.TempDir())
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "reconnect"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status, st.SandboxID, st.ProcessPID = state.StatusRunning, "sandbox", 42
	st.AssignedJobID = 123
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	fake := &fakeSandbox{}
	srv := New(config.Config{SandboxTimeout: time.Hour}, store, github.NewClient("", nil), fake, nil)
	if err := srv.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	assertCanceledWatchPreservesRunner(t, store, st.ID, fake.lastRecoverInput().OnExit)
	if fake.stoppedCount() != 0 {
		t.Fatalf("canceled observation stopped %d sandboxes", fake.stoppedCount())
	}
}

type interruptedAdoptionStore struct {
	state.Store
	calls     int
	committed bool
}

func (s *interruptedAdoptionStore) AdoptRunningWatch(st state.RunnerState, token string, ttl time.Duration) error {
	s.calls++
	if s.calls == 1 {
		if s.committed {
			if err := s.Store.AdoptRunningWatch(st, token, ttl); err != nil {
				return err
			}
			return errors.New("commit response lost")
		}
		return state.ErrConflict
	}
	return s.Store.AdoptRunningWatch(st, token, ttl)
}

func TestRunningWatchReconcilesInterruptedAdoption(t *testing.T) {
	for _, scenario := range []string{"conflict", "lost_commit_reply"} {
		t.Run(scenario, func(t *testing.T) {
			srv, store, batches := newStartupTimingFixture(t)
			fault := &interruptedAdoptionStore{Store: srv.store, committed: scenario == "lost_commit_reply"}
			srv.store = fault
			claim := queuedRunnerForTest(t, store.Store, "timing-start", "test-worker")
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				srv.startClaimedRunner(ctx, claim)
			}()
			for i := 0; i < 2; i++ { // The failed response, then reconciliation.
				select {
				case <-batches:
				case <-time.After(5 * time.Second):
					t.Fatal("worker did not reconcile the running state")
				}
			}
			st, err := store.Store.ReadState(claim.ID)
			if err != nil || st.Status != state.StatusRunning || st.SandboxID == "" || st.ProcessPID == 0 {
				t.Fatalf("the started Sandbox is no longer managed: %#v, %v", st, err)
			}
			if len(store.sandbox.startInputs()) != 1 {
				t.Fatalf("provider starts: %d", len(store.sandbox.startInputs()))
			}
			store.sandbox.startInputs()[0].OnExit(sandboxrunner.ExitResult{}, nil)
			awaitAdmissionTest(t, done, "reconciled watcher did not finish")
			after, err := store.Store.ReadState(st.ID)
			if err != nil || after.Status == state.StatusRunning {
				t.Fatalf("reconciled watcher ignored process exit: status=%s err=%v", after.Status, err)
			}
		})
	}
}

func TestRunnerWatchDiscardsBusyCheckErrorAfterTakeover(t *testing.T) {
	store := state.New(t.TempDir())
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "watch-busy", RunnerName: "runner", RepositoryFullName: "o/r"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status, st.ProcessPID, st.SandboxID = state.StatusRunning, 42, "sandbox"
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	st, err = store.ReadState(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	const ttl = 200 * time.Millisecond
	if ok, err := store.ClaimRunningWatch(t.Context(), st, "old", ttl); err != nil || !ok {
		t.Fatalf("initial watch claim: %v, %v", ok, err)
	}
	lookup, release := make(chan struct{}, 1), make(chan struct{})
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		lookup <- struct{}{}
		<-release
		http.Error(w, "busy lookup failed", http.StatusInternalServerError)
	}))
	defer gh.Close()
	finishLookup := sync.OnceFunc(func() { close(release) })
	defer finishLookup()
	fake := &fakeSandbox{}
	srv := New(config.Config{WorkerLeaseTTL: ttl}, store, github.NewClient(gh.URL, gh.Client()), fake, nil)
	defer srv.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.runnerExitedForAttempt(st.ID, runnerAttemptIdentity{sandboxID: st.SandboxID, processPID: st.ProcessPID, watchToken: "old"}, sandboxrunner.ExitResult{ExitCode: 1}, nil)
	}()
	awaitAdmissionTest(t, lookup, "busy lookup did not start")
	current, err := store.ReadState(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(4 * time.Second); ; {
		ok, err := store.ClaimRunningWatch(t.Context(), current, "new", time.Minute)
		if err != nil || time.Now().After(deadline) {
			t.Fatalf("watch takeover failed: %v", err)
		}
		if ok {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	finishLookup()
	awaitAdmissionTest(t, done, "busy lookup did not finish")
	events, _, err := store.ListRunnerEvents(st.ID, 0, 10)
	if err != nil || len(events) != 0 || fake.stoppedCount() != 0 {
		t.Fatalf("stale observer recorded an event or stopped a Sandbox: events=%v stopped=%d err=%v", events, fake.stoppedCount(), err)
	}
}
