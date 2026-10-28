package server

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestRunnerCleanupDoesNotBlockAnotherRequest(t *testing.T) {
	store := state.New(t.TempDir())
	stopBlock := make(chan struct{})
	fake := &fakeSandbox{stopBlock: stopBlock, stopStarted: make(chan struct{}, 1)}
	ghServer := httptest.NewServer(githubRunnerAPI(t))
	defer ghServer.Close()
	srv := newTestServer(t, store, ghServer.URL, fake)
	srv.Close()

	const cleanupID = "probe-37"
	const targetID = "118521076552"
	for _, id := range []string{cleanupID, targetID} {
		_, st, err := store.CreateRequest(state.RunnerRequest{
			ID:                   id,
			Source:               "runner-lock-test",
			RepositoryFullName:   "owner/repository",
			RunnerName:           "runner-" + id,
			Labels:               []string{"self-hosted", "e2b"},
			GitHubInstallationID: 1,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		st.Status = state.StatusRunning
		st.SandboxID = "sandbox-" + id
		st.ProcessPID = 42
		if id == cleanupID {
			// This test isolates Sandbox cleanup from the GitHub registration
			// cleanup path, which has its own request-scoped tests.
			st.RunnerName = ""
		}
		if err := store.WriteState(st); err != nil {
			t.Fatal(err)
		}
	}

	cleanupDone := make(chan error, 1)
	go func() {
		_, _, err := srv.stopRunner(context.Background(), cleanupID, github.WorkflowJob{}, state.TerminationSourceManualStop)
		cleanupDone <- err
	}()
	select {
	case <-fake.stopStarted:
	case <-time.After(time.Second):
		t.Fatal("sandbox cleanup did not start")
	}

	targetDone := make(chan struct{})
	go func() {
		srv.appendRunnerStdout(targetID, []byte("Running job: target\n"))
		close(targetDone)
	}()
	select {
	case <-targetDone:
	case <-time.After(time.Second):
		t.Fatal("runner output for another request was blocked by cleanup")
	}

	control, err := store.ReadLog(targetID, "control.log", 4096)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(control), "runner accepted a job") {
		t.Fatalf("target request did not record accepted-job marker: %q", control)
	}

	close(stopBlock)
	select {
	case err := <-cleanupDone:
		if err != nil {
			t.Fatalf("cleanup failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("sandbox cleanup did not finish")
	}
}

func TestRunnerLocksAreSerializedPerRequest(t *testing.T) {
	srv := &Server{}
	first := srv.lockRunner("same-request")
	secondAcquired := make(chan struct{})
	go func() {
		unlock := srv.lockRunner("same-request")
		close(secondAcquired)
		unlock()
	}()
	select {
	case <-secondAcquired:
		t.Fatal("same request acquired lifecycle lock concurrently")
	case <-time.After(50 * time.Millisecond):
	}
	first()
	select {
	case <-secondAcquired:
	case <-time.After(time.Second):
		t.Fatal("same request lock did not become available")
	}

	held := srv.lockRunner("held-request")
	otherAcquired := make(chan struct{})
	go func() {
		unlock := srv.lockRunner("other-request")
		close(otherAcquired)
		unlock()
	}()
	select {
	case <-otherAcquired:
	case <-time.After(time.Second):
		t.Fatal("different request was serialized unexpectedly")
	}
	held()
}

func TestRunnerTimingRecordsGitHubRunnerGroupStage(t *testing.T) {
	store := state.New(t.TempDir())
	if _, _, err := store.CreateRequest(state.RunnerRequest{ID: "timing-request"}, nil); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store, logger: slog.Default()}
	var timings runnerTimings
	timings.record("github_runner_group", time.Now().Add(-time.Millisecond))
	timings.flush(srv, "timing-request")

	events, _, err := store.ListRunnerEvents("timing-request", 0, 10, "control_log")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("timing events = %#v, want one event", events)
	}
	if events[0].Stage != runnerEventStageTiming || !strings.Contains(events[0].Message, "stage=github_runner_group duration_ms=") {
		t.Fatalf("timing event = %#v", events[0])
	}
}

func TestRunnerWallTimingRecordsClockSkewInsteadOfDroppingEvidence(t *testing.T) {
	store := state.New(t.TempDir())
	if _, _, err := store.CreateRequest(state.RunnerRequest{ID: "wall-timing-request"}, nil); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store, logger: slog.Default()}
	var timings runnerTimings
	timings.recordWall("queue_wait", time.Now().UTC().Add(time.Hour))
	timings.flush(srv, "wall-timing-request")

	events, _, err := store.ListRunnerEvents("wall-timing-request", 0, 10, "control_log")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || !strings.Contains(events[0].Message, "stage=queue_wait duration_ms=0 clock=wall_skew") {
		t.Fatalf("wall timing event = %#v", events)
	}
}

func TestRunnerCleanupTimingSkipsCompletedRequest(t *testing.T) {
	store := state.New(t.TempDir())
	if _, st, err := store.CreateRequest(state.RunnerRequest{ID: "completed-cleanup-timing"}, nil); err != nil {
		t.Fatal(err)
	} else {
		st.Status = state.StatusCompleted
		if err := store.WriteState(st); err != nil {
			t.Fatal(err)
		}
	}
	srv := &Server{store: store, logger: slog.Default()}
	if _, _, err := srv.stopRunner(context.Background(), "completed-cleanup-timing", github.WorkflowJob{}, state.TerminationSourceManualStop); err != nil {
		t.Fatal(err)
	}

	events, _, err := store.ListRunnerEvents("completed-cleanup-timing", 0, 10, "control_log")
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Stage == runnerEventStageTiming && strings.Contains(event.Message, "stage=cleanup ") {
			t.Fatalf("completed request recorded cleanup timing: %#v", event)
		}
	}
}

func TestRunnerLockWaitersReuseEntryAndReleaseReferences(t *testing.T) {
	srv := &Server{}
	first := srv.lockRunner("request")
	entry := srv.runnerLocks["request"]
	acquired := make(chan func(), 1)
	go func() { acquired <- srv.lockRunner("request") }()

	deadline := time.After(time.Second)
	for {
		srv.runnerLocksMu.Lock()
		refs := entry.refs
		same := srv.runnerLocks["request"] == entry
		srv.runnerLocksMu.Unlock()
		if !same {
			t.Fatal("waiter replaced the held lock entry")
		}
		if refs == 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("waiter did not retain the lock entry")
		case <-time.After(time.Millisecond):
		}
	}
	first()
	var second func()
	select {
	case second = <-acquired:
	case <-time.After(time.Second):
		t.Fatal("waiter did not acquire the released lock")
	}
	first() // A duplicate unlock must not release the waiter's reference or mutex.
	srv.runnerLocksMu.Lock()
	if srv.runnerLocks["request"] != entry || entry.refs != 1 {
		t.Errorf("waiter entry lost after duplicate unlock: refs=%d", entry.refs)
	}
	srv.runnerLocksMu.Unlock()
	if entry.mu.TryLock() {
		entry.mu.Unlock()
		t.Error("duplicate unlock released the waiter's mutex")
	}
	second()
	second()
	srv.runnerLocksMu.Lock()
	defer srv.runnerLocksMu.Unlock()
	if len(srv.runnerLocks) != 0 {
		t.Errorf("unused lock entries were not reclaimed: %v", srv.runnerLocks)
	}
}

type timingObservingStore struct {
	state.Store
	onReadRequest func()
	onTiming      func(string)
}

func (s *timingObservingStore) ReadRequest(id string) (state.RunnerRequest, error) {
	s.onReadRequest()
	return s.Store.ReadRequest(id)
}

func (s *timingObservingStore) AppendStagedLog(id, name, stage string, data []byte) {
	if stage == runnerEventStageTiming {
		s.onTiming(string(data))
	}
	s.Store.AppendStagedLog(id, name, stage, data)
}

func TestRunnerStartupFlushesTimingsOutsideLocks(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		t.Run(fmt.Sprintf("profile_enabled_%t", enabled), func(t *testing.T) {
			base := state.New(t.TempDir())
			events := &lifecycleEventRecorder{}
			ghServer := httptest.NewServer(githubRunnerAPI(t))
			defer ghServer.Close()
			sandbox := &lifecycleSandboxService{events: events}
			upsertLifecycleProfile(t, base, state.RunnerProfile{
				Name: "custom", Labels: []string{"self-hosted", "custom"},
				TemplateID: "custom-id", Enabled: enabled, MaxConcurrency: 10,
			})
			const id = "timed-start"
			if _, _, err := base.CreateRequest(state.RunnerRequest{
				ID: id, RepositoryFullName: "o/r", ProfileName: "custom",
				Labels: []string{"self-hosted", "custom"}, SandboxAPIURL: "https://us-south-1.example",
			}, nil); err != nil {
				t.Fatal(err)
			}
			reads := make(chan time.Time, 10)
			batches := make(chan string, 10)
			store := &timingObservingStore{Store: base, onReadRequest: func() { reads <- time.Now() }}
			srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, sandbox)
			store.onTiming = func(message string) {
				// A synchronous diagnostics write may be slow; neither lifecycle
				// nor global admission should remain locked during that write.
				srv.runnerLocksMu.Lock()
				entry := srv.runnerLocks[id]
				if entry != nil {
					if entry.mu.TryLock() {
						entry.mu.Unlock()
					} else {
						t.Error("timing persistence held the request lock")
					}
				}
				srv.runnerLocksMu.Unlock()
				if srv.admissionMu.TryLock() {
					srv.admissionMu.Unlock()
				} else {
					t.Error("timing persistence held the admission lock")
				}
				batches <- message
			}
			done := make(chan struct{})
			go func() {
				srv.startRunner(t.Context(), id, "worker-test")
				close(done)
			}()
			var batch string
			select {
			case batch = <-batches:
			case <-time.After(3 * time.Second):
				t.Fatal("startup timings were not flushed before waiting for process exit")
			}
			firstRead := <-reads
			firstLine := strings.Split(batch, "\n")[0]
			_, observed, _ := strings.Cut(firstLine, "observed_at=")
			observedAt, err := time.Parse(time.RFC3339Nano, observed)
			if err != nil || observedAt.After(firstRead) {
				t.Fatalf("initial lock timing includes request read: %q, read at %v", firstLine, firstRead)
			}
			if enabled {
				for _, stage := range []string{"request_read", "initial_state_read", "admission_lock_wait", "profile_lookup", "global_capacity_check", "profile_capacity_check", "creating_state_persist", "github_runner_applications", "sandbox_start", "running_lock_wait", "running_state_read", "running_state_persist"} {
					if !strings.Contains(batch, "stage="+stage+" ") {
						t.Errorf("missing startup timing %q: %s", stage, batch)
					}
				}
				select {
				case <-done:
					t.Error("successful startup did not wait for process exit")
				default:
				}
				sandbox.startInputs()[0].OnExit(sandboxrunner.ExitResult{}, nil)
			}
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("startup did not finish")
			}
			// Cleanup may have its own batch, but startup must be written once.
			close(batches)
			for message := range batches {
				if strings.Contains(message, "stage=initial_lock_wait ") {
					t.Error("startup timings were persisted more than once")
				}
			}
		})
	}
}
