package state

import (
	"errors"
	"testing"
	"time"
)

func TestJITRejectionReceipt(t *testing.T) {
	testJITRejectionReceipt(t, New(t.TempDir()).(*DBStore))
}

func testJITRejectionReceipt(t *testing.T, store *DBStore) {
	t.Helper()
	for _, code := range []string{"auth_error", "github_registration", "http_rate_limited", "github_secondary_rate_limit", "http_retryable_status"} {
		id := "jit-rejection-" + code
		_, attempt, err := store.CreateRequest(RunnerRequest{ID: id, Labels: []string{"windows"}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer store.db.Delete(&runnerRequestRecord{}, "id = ?", id)
		attempt.Status, attempt.ProvisionAttemptID = StatusCreating, id
		if err := store.WriteState(attempt); err != nil {
			t.Fatal(err)
		}
		attempt, _ = store.ReadState(id)
		if ok, err := store.ClaimRunnerWatch(t.Context(), attempt, "creator", time.Minute); !ok || err != nil {
			t.Fatal("claim", err)
		}
		if err := store.DispatchJITRegistration(t.Context(), id, "creator", id); err != nil {
			t.Fatal(err)
		}
		before, _ := store.ReadState(id)
		before.Status, before.ManualStopRequested = StatusStopping, true
		before.LastErrorCode = "jit_registration_interrupted"
		if err := store.BeginRunnerCleanup(t.Context(), before, "", "cleanup", time.Minute); err != nil {
			t.Fatal(err)
		}
		before, _ = store.ReadState(id)
		attempt.LastErrorCode, attempt.LastErrorMessage = "untrusted fixture error", "private response"
		if err := store.RecordJITRegistrationResult(t.Context(), attempt, 0); !errors.Is(err, ErrConflict) {
			t.Fatalf("unknown classification accepted: %v", err)
		}
		attempt.LastErrorCode = code
		if err := store.RecordJITRegistrationResult(t.Context(), attempt, 0); err != nil {
			t.Fatal(err)
		}
		current, err := store.ReadState(id)
		retryable := code != "auth_error" && code != "github_registration"
		if err != nil || current.JITRegistrationState != JITRejected || !current.CleanupGitHubDone ||
			current.LastErrorCode != code || current.LastErrorRetryable != retryable ||
			current.LastErrorMessage != "Windows JIT registration rejected" ||
			current.Version != before.Version+1 || !current.ManualStopRequested {
			t.Fatalf("rejection receipt lost classification or cancellation: %+v, %v", current, err)
		}
		if err := store.WriteCleanupState(before, "cleanup"); !errors.Is(err, ErrConflict) {
			t.Fatalf("old cleanup writer overwrote receipt: %v", err)
		}
		if err := store.RecordJITRegistrationResult(t.Context(), attempt, 0); !errors.Is(err, ErrConflict) {
			t.Fatalf("duplicate rejection changed the receipt: %v", err)
		}
	}
}
