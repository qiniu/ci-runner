package server

import (
	"bytes"
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

func TestCleanupSandboxLookupEvidence(t *testing.T) {
	for _, outcome := range []string{"not_found", "failed", "found", "lease_lost", "event_failed"} {
		t.Run(outcome, func(t *testing.T) {
			store := state.New(t.TempDir())
			createLifecycleRequest(t, store, "lookup", "linux", 0)
			st, _ := store.ReadState("lookup")
			st.Status, st.ProvisionAttemptID = state.StatusCreating, "private-attempt"
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			st, _ = store.ReadState(st.ID)
			if ok, err := store.ClaimRunnerWatch(t.Context(), st, "creator", time.Minute); !ok || err != nil {
				t.Fatal(err)
			}
			if err := store.DispatchSandboxCreate(t.Context(), st.ID, "creator", st.ProvisionAttemptID); err != nil {
				t.Fatal(err)
			}
			st, _ = store.ReadState(st.ID)
			st.Status = state.StatusStopping
			st.LastErrorMessage = "original creation timeout"
			if err := store.BeginRunnerCleanup(t.Context(), st, "creator", "initial", time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := store.ReleaseRunnerCleanup(t.Context(), st.ID, "initial"); err != nil {
				t.Fatal(err)
			}
			provider := &cleanupLookupSandbox{fakeSandbox: &fakeSandbox{}}
			provider.lookup = func() (string, error) {
				switch outcome {
				case "failed":
					return "", fmt.Errorf("https://user:credential-canary@example.invalid: %w", context.DeadlineExceeded)
				case "found":
					return "found-box", nil
				case "lease_lost":
					current, _ := store.ReadState(st.ID)
					if err := store.ReleaseRunnerCleanup(t.Context(), st.ID, current.WatchToken); err != nil {
						t.Fatal(err)
					}
				}
				return "", sandboxrunner.ErrSandboxNotFound
			}
			api := httptest.NewServer(githubRunnerAPI(t))
			defer api.Close()
			actualStore := store
			if outcome == "event_failed" {
				actualStore = &cleanupLookupEventFailureStore{Store: store}
			}
			srv := newRunnerLifecycleTestServer(t, actualStore, api.URL, provider)
			var serviceLog bytes.Buffer
			srv.logger = slog.New(slog.NewJSONHandler(&serviceLog, nil))
			_, _, stopErr := srv.stopRunner(t.Context(), st.ID, github.WorkflowJob{}, state.TerminationSourceRecoveryCleanup)
			if (stopErr != nil) != (outcome == "failed" || outcome == "lease_lost") {
				t.Fatalf("unexpected stop error: %v", stopErr)
			}
			got, _ := store.ReadState(st.ID)
			events, _, err := store.ListRunnerEvents(st.ID, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			var lookupEvents []state.RunnerEvent
			for _, e := range events {
				if strings.Contains(e.Message, "private-attempt") || strings.Contains(e.Message, "credential-canary") {
					t.Fatal("sensitive event")
				}
				if e.Stage == "sandbox_creation_lookup" {
					lookupEvents = append(lookupEvents, e)
				}
			}
			if outcome == "lease_lost" || outcome == "event_failed" {
				if len(lookupEvents) != 0 {
					t.Fatal("stale cleanup wrote event")
				}
			} else {
				if len(lookupEvents) != 1 || !strings.Contains(lookupEvents[0].Message, "outcome="+outcome) {
					t.Fatalf("missing lookup evidence: %+v", lookupEvents)
				}
			}
			if strings.Contains(serviceLog.String()+fmt.Sprint(stopErr), "credential-canary") {
				t.Fatal("sensitive error logged or returned")
			}
			if !strings.Contains(serviceLog.String(), `"attempt_id":"private-attempt"`) {
				t.Fatal("service log missing attempt")
			}
			if outcome == "found" {
				if got.SandboxID != "found-box" || provider.stoppedCount() != 1 || got.Status != state.StatusCompleted {
					t.Fatalf("found resource not cleaned: %+v", got)
				}
			} else {
				counts, _ := store.RunnerCapacityCounts(state.RunnerRequest{RepositoryFullName: "o/r"})
				if got.Status != state.StatusStopping || counts.Global != 1 || provider.stoppedCount() != 0 || got.LastErrorMessage != "original creation timeout" {
					t.Fatalf("lookup changed lifecycle: %+v", got)
				}
				if outcome == "not_found" && (got.NextRetryAt.IsZero() || !strings.Contains(lookupEvents[0].Message, "matches=0")) {
					t.Fatal("missing empty lookup retry evidence")
				}
				if outcome == "failed" && !strings.Contains(lookupEvents[0].Message, "matches=unknown category=deadline_exceeded") {
					t.Fatal("failed lookup mistaken for absence")
				}
			}
		})
	}
}

type cleanupLookupEventFailureStore struct{ state.Store }

func (s *cleanupLookupEventFailureStore) AppendRunnerCleanupEvent(context.Context, state.RunnerState, string, string, string) error {
	return fmt.Errorf("database error containing credential-canary")
}

type cleanupLookupSandbox struct {
	*fakeSandbox
	lookup func() (string, error)
}

func (s *cleanupLookupSandbox) FindRunnerSandbox(context.Context, string, string) (string, error) {
	return s.lookup()
}
