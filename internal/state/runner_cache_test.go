package state

import (
	"errors"
	"testing"
)

func TestRunnerCacheEventFencesAttemptAndPreservesLifecycle(t *testing.T) {
	store := New(t.TempDir())
	_, st, err := store.CreateRequest(RunnerRequest{ID: "cache-event"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status, st.ProvisionAttemptID = StatusCreating, "attempt-a"
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	before, _ := store.ReadState(st.ID)
	if err := store.AppendRunnerCacheEvent(t.Context(), st.ID, "attempt-a", "cache STS duration_exceeded (HTTP 400)"); err != nil {
		t.Fatal(err)
	}
	after, _ := store.ReadState(st.ID)
	if before.Version != after.Version || !before.UpdatedAt.Equal(after.UpdatedAt) || before.Status != after.Status {
		t.Fatal("cache diagnostic changed lifecycle")
	}
	for _, attempt := range []string{"", "attempt-b"} {
		if err := store.AppendRunnerCacheEvent(t.Context(), st.ID, attempt, "stale"); !errors.Is(err, ErrConflict) {
			t.Fatalf("attempt %q: %v", attempt, err)
		}
	}
	after.Status = StatusCompleted
	if err := store.WriteState(after); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendRunnerCacheEvent(t.Context(), st.ID, "attempt-a", "late"); !errors.Is(err, ErrConflict) {
		t.Fatalf("terminal attempt: %v", err)
	}
	events, _, err := store.ListRunnerEvents(st.ID, 0, 20)
	if err != nil || len(events) != 1 || events[0].Stage != "cache_sts" {
		t.Fatalf("events: %#v, %v", events, err)
	}
}
