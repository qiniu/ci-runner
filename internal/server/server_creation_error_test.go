package server

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestUnknownCreateFailureRetentionAndStaleOwner(t *testing.T) {
	store := state.New(t.TempDir())
	createLifecycleRequest(t, store, "unknown", "linux", 0)
	st, _ := store.ReadState("unknown")
	st.Status = state.StatusCreating
	st.ProvisionAttemptID = "attempt"
	st.CreatingAt = time.Now()
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	st, _ = store.ReadState(st.ID)
	if ok, err := store.ClaimRunnerWatch(t.Context(), st, "creator", time.Minute); !ok || err != nil {
		t.Fatal(err)
	}
	st, _ = store.ReadState(st.ID)
	if err := store.DispatchSandboxCreate(t.Context(), st.ID, "creator", st.ProvisionAttemptID); err != nil {
		t.Fatal(err)
	}
	srv := newRunnerLifecycleTestServer(t, store, "", &fakeSandbox{})
	srv.retainUnknownCreateFailure(st, fmt.Errorf("sensitive URL credential-canary: %w", context.DeadlineExceeded))
	got, _ := store.ReadState(st.ID)
	if got.LastErrorCode != "sandbox_create_outcome_unknown" || !strings.Contains(got.LastErrorMessage, "deadline_exceeded") || strings.Contains(got.LastErrorMessage, "credential-canary") {
		t.Fatalf("bad retained error: %q", got.LastErrorMessage)
	}
	logs, _ := store.ReadLog(st.ID, "control.log", 10000)
	if strings.Contains(string(logs), "credential-canary") || !strings.Contains(string(logs), "deadline_exceeded") {
		t.Fatalf("unsafe or missing evidence: %s", logs)
	}
	// A result from an old attempt cannot overwrite a peer's retained evidence.
	before := got
	st.ProvisionAttemptID = "old-attempt"
	srv.retainUnknownCreateFailure(st, sandboxrunner.ErrCreateOutcomeUnknown)
	got, _ = store.ReadState(st.ID)
	if got.Version != before.Version || got.LastErrorMessage != before.LastErrorMessage {
		t.Fatal("stale creator changed state")
	}
}

func TestUnknownCreationDiagnostics(t *testing.T) {
	st := state.RunnerState{Status: state.StatusStopping, ProvisionAttemptID: "attempt", SandboxCreateDispatched: true, StoppingAt: time.Now().Add(-2 * time.Hour), LastErrorCode: "sandbox_create_outcome_unknown", LastErrorMessage: "deadline_exceeded"}
	findings := diagnoseRunnerRequest(st, nil, false, diagnosticGitHubJob{LookupStatus: "retained", Status: "completed", Conclusion: "success"})
	codes := map[string]bool{}
	for _, f := range findings {
		codes[f.Code] = true
	}
	if !codes["sandbox_creation_unresolved"] || !codes["runner_cleanup_overdue"] || codes["no_anomaly_detected"] {
		t.Fatalf("misleading diagnostics: %+v", findings)
	}
	st.Status = state.StatusCompleted
	st.LastErrorCode = state.UnknownCreationResolvedCode
	findings = diagnoseRunnerRequest(st, nil, false, diagnosticGitHubJob{})
	if len(findings) != 1 || findings[0].Code != "sandbox_creation_resolved_absent" {
		t.Fatalf("resolution lost provenance: %+v", findings)
	}
}

func TestUnknownCreationNeverAutoCompletesFromEmptyList(t *testing.T) {
	store := state.New(t.TempDir())
	createLifecycleRequest(t, store, "still-unknown", "linux", 0)
	st, _ := store.ReadState("still-unknown")
	st.Status = state.StatusCreating
	st.ProvisionAttemptID = "original"
	st.CreatingAt = time.Now().Add(-24 * time.Hour)
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	st, _ = store.ReadState(st.ID)
	if ok, err := store.ClaimRunnerWatch(t.Context(), st, "owner", time.Minute); !ok || err != nil {
		t.Fatal(err)
	}
	if err := store.DispatchSandboxCreate(t.Context(), st.ID, "owner", st.ProvisionAttemptID); err != nil {
		t.Fatal(err)
	}
	st, _ = store.ReadState(st.ID)
	st.Status = state.StatusStopping
	st.StoppingAt = time.Now().Add(-24 * time.Hour)
	st.LastErrorMessage = "category=deadline_exceeded"
	st.LastErrorCode = "sandbox_create_outcome_unknown"
	if err := store.BeginRunnerCleanup(t.Context(), st, "owner", "cleanup", time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseRunnerCleanup(t.Context(), st.ID, "cleanup"); err != nil {
		t.Fatal(err)
	}
	fake := &fakeSandbox{}
	srv := newRunnerLifecycleTestServer(t, store, "", fake)
	for range 3 {
		if _, _, err := srv.stopRunner(t.Context(), st.ID, github.WorkflowJob{}, state.TerminationSourceRecoveryCleanup); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := store.ReadState(st.ID)
	counts, _ := store.RunnerCapacityCounts(state.RunnerRequest{RepositoryFullName: "o/r"})
	if got.Status != state.StatusStopping || !got.SandboxCreateDispatched || got.LastErrorMessage != "category=deadline_exceeded" || counts.Global != 1 || fake.stoppedCount() != 0 {
		t.Fatalf("unknown released or original error lost: %+v", got)
	}
}

type unknownCreateFailureService struct{ *fakeSandbox }

func (s *unknownCreateFailureService) StartRunner(context.Context, sandboxrunner.StartInput) (sandboxrunner.StartResult, error) {
	return sandboxrunner.StartResult{}, fmt.Errorf("%w: POST https://user:credential-canary@example.invalid: %w", sandboxrunner.ErrCreateOutcomeUnknown, context.DeadlineExceeded)
}

func TestUnknownCreateErrorRecordedThroughStartPath(t *testing.T) {
	store := state.New(t.TempDir())
	upsertLifecycleProfile(t, store, state.RunnerProfile{Name: "linux", Labels: []string{"self-hosted", "linux"}, TemplateID: "test-template", Enabled: true})
	createLifecycleRequest(t, store, "start-unknown", "linux", 0)
	api := httptest.NewServer(githubRunnerAPI(t))
	defer api.Close()
	svc := &unknownCreateFailureService{fakeSandbox: &fakeSandbox{}}
	srv := newRunnerLifecycleTestServer(t, store, api.URL, svc)
	srv.startClaimedRunner(t.Context(), queuedRunnerForTest(t, store, "start-unknown", "creator"))
	got, _ := store.ReadState("start-unknown")
	if got.Status != state.StatusCreating || !got.SandboxCreateDispatched || got.LastErrorCode != "sandbox_create_outcome_unknown" || !strings.Contains(got.LastErrorMessage, "deadline_exceeded") {
		t.Fatalf("start lost unknown error: %+v", got)
	}
	logs, _ := store.ReadLog(got.ID, "control.log", 10000)
	if strings.Contains(got.LastErrorMessage+string(logs), "credential-canary") {
		t.Fatal("sensitive create error retained")
	}
}
