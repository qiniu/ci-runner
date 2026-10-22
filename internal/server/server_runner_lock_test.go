package server

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
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
	srv.appendRunnerTiming("timing-request", "github_runner_group", time.Now().Add(-time.Millisecond))

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
	srv.appendRunnerWallTiming("wall-timing-request", "queue_wait", time.Now().UTC().Add(time.Hour))

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
