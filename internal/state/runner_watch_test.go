package state

import (
	"fmt"
	"testing"
	"time"
)

func TestCleanupRecoveryDoesNotStarveExpiredWatches(t *testing.T) {
	testCleanupRecoveryDoesNotStarveExpiredWatches(t, New(t.TempDir()).(*DBStore))
}

func testCleanupRecoveryDoesNotStarveExpiredWatches(t *testing.T, store *DBStore) {
	t.Helper()
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	defer func() {
		if err := db.Delete(&runnerRequestRecord{}, "id IN ?", ids).Error; err != nil {
			t.Error(err)
		}
	}()
	for i := range 7 {
		id := fmt.Sprintf("cleanup-fairness-%d", i)
		_, st, err := store.CreateRequest(RunnerRequest{ID: id}, nil)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		st.Status = StatusStopping
		if err := store.WriteState(st); err != nil {
			t.Fatal(err)
		}
		if i == 6 {
			if err := db.Model(&runnerRequestRecord{}).Where("id = ?", id).UpdateColumns(map[string]any{"status": StatusRunning, "watch_expires_at": time.Unix(1, 0).UTC()}).Error; err != nil {
				t.Fatal(err)
			}
		}
	}
	seen := map[string]bool{}
	for round := range 4 {
		rows, err := store.ListExpiredRunnerWatches(t.Context(), 2)
		if err != nil || len(rows) != 2 {
			t.Fatalf("recovery batch %d: %v, %v", round, rows, err)
		}
		for _, row := range rows {
			seen[row.ID] = true
			st, err := store.ReadState(row.ID)
			if err != nil {
				t.Fatal(err)
			}
			token := fmt.Sprintf("cleanup-%d-%s", round, st.ID)
			if st.Status == StatusRunning {
				if ok, err := store.ClaimRunnerWatch(t.Context(), st, token, time.Minute); !ok || err != nil {
					t.Fatalf("claim running watch: %t, %v", ok, err)
				}
				continue
			}
			if err := store.BeginRunnerCleanup(t.Context(), st, "", token, time.Minute); err != nil {
				t.Fatal(err)
			}
			claimed, err := store.ReadState(st.ID)
			if err != nil {
				t.Fatal(err)
			}
			// A provider lookup failed before a retry deadline was saved. Closing
			// that attempt must let the next bounded scan reach untouched rows.
			if err := store.ReleaseRunnerCleanup(t.Context(), st.ID, token); err != nil {
				t.Fatal(err)
			}
			got, err := store.ReadState(st.ID)
			if err != nil || got.Status != StatusStopping || got.WatchToken != "" || got.Version != claimed.Version || !got.UpdatedAt.Equal(claimed.UpdatedAt) {
				t.Fatalf("release changed lifecycle state: %+v, %v", got, err)
			}
		}
	}
	if len(seen) != len(ids) {
		t.Fatalf("repeated cleanup errors starved expired watches: visited %d/%d: %v", len(seen), len(ids), seen)
	}
}
