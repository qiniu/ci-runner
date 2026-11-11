package state

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestNetworkDiagnosticClaim(t *testing.T) {
	testNetworkDiagnosticClaim(t, New(t.TempDir()).(*DBStore))
}

func testNetworkDiagnosticClaim(t *testing.T, store *DBStore) {
	t.Helper()
	ctx := t.Context()
	_, st, err := store.CreateRequest(RunnerRequest{ID: "cluster-diagnostic-lease", RunnerName: "diagnostic"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status, st.SandboxID, st.ProcessPID = StatusRunning, "sandbox", 71
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	st, err = store.ReadState(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	const cooldown = 30 * time.Second
	claim := func(st RunnerState, token string) time.Duration {
		t.Helper()
		wait, err := store.ClaimNetworkDiagnostic(ctx, st, token, 10*time.Second, cooldown)
		if err != nil {
			t.Fatal(err)
		}
		return wait
	}
	if wait := claim(st, "first"); wait != 0 {
		t.Fatalf("first claim delayed by %s", wait)
	}
	if wait := claim(st, "blocked"); wait != cooldown {
		t.Fatal("live claim wait", wait)
	}
	// Simulate a slow probe with only one second left on its abandoned-claim
	// deadline. Completion must restart the entire cooldown at database time.
	next := func(st RunnerState, value any) {
		t.Helper()
		if err := db.Model(&runnerRequestRecord{}).Where("id = ?", st.ID).UpdateColumn("diagnostic_next_at", value).Error; err != nil {
			t.Fatal(err)
		}
	}
	next(st, queueTime(db, time.Second))
	if err := store.FinishNetworkDiagnostic(ctx, st, "first", "probe result", cooldown); err != nil {
		t.Fatal(err)
	}
	if wait := claim(st, "blocked"); wait < 29*time.Second || wait > cooldown {
		t.Fatal("post-completion cooldown", wait)
	}
	next(st, queueTime(db, time.Second))
	if wait := claim(st, "blocked"); wait <= 0 || wait > 1100*time.Millisecond {
		t.Fatal("remaining cooldown", wait)
	}
	next(st, time.Unix(1, 0).UTC())
	if wait := claim(st, "after-expiry"); wait != 0 {
		t.Fatal("expired claim", wait)
	}
	// The old owner cannot publish late evidence or release the replacement.
	if err := store.FinishNetworkDiagnostic(ctx, st, "first", "stale", cooldown); !errors.Is(err, ErrConflict) {
		t.Fatal("old token", err)
	}
	current, err := store.ReadState(st.ID)
	if err != nil || current.Version != st.Version || !current.UpdatedAt.Equal(st.UpdatedAt) {
		t.Fatal("diagnostic altered lifecycle version", err)
	}
	current.ProvisionAttemptID = "new-attempt"
	current.LastAttemptAt = time.Now().UTC()
	if err := store.WriteState(current); err != nil {
		t.Fatal(err)
	}
	current, err = store.ReadState(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FinishNetworkDiagnostic(ctx, st, "after-expiry", "old attempt", cooldown); !errors.Is(err, ErrConflict) {
		t.Fatal("stale attempt", err)
	}
	if wait := claim(current, "replacement"); wait != 0 {
		t.Fatal("old attempt blocked replacement", wait)
	}
	// A failed event insert must roll back the release. The same owner can
	// retry the transaction without losing evidence or duplicating an event.
	failedInsert := errors.New("diagnostic event insert failed")
	const callback = "test:fail-diagnostic-insert"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if _, ok := tx.Statement.Dest.(*runnerEventRecord); ok {
			tx.AddError(failedInsert)
		}
	}); err != nil {
		t.Fatal(err)
	}
	finishErr := store.FinishNetworkDiagnostic(ctx, current, "replacement", "replacement result", cooldown)
	if err := db.Callback().Create().Remove(callback); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(finishErr, failedInsert) {
		t.Fatal("event insertion", finishErr)
	}
	if err := store.FinishNetworkDiagnostic(ctx, current, "replacement", "replacement result", cooldown); err != nil {
		t.Fatal("rolled back owner lost claim", err)
	}
	events, _, err := store.ListRunnerEvents(st.ID, 0, 10, "control_log")
	if err != nil || len(events) != 2 || events[0].Message != "probe result" || events[1].Message != "replacement result" {
		t.Fatal("unexpected diagnostic evidence", len(events), err)
	}
}
