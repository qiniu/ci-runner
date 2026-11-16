package state

import (
	"errors"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestRunnerCleanupEventFencing(t *testing.T) {
	testRunnerCleanupEventFencing(t, New(t.TempDir()).(*DBStore))
}

func testRunnerCleanupEventFencing(t *testing.T, store *DBStore) {
	t.Helper()
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"live", "stale version", "wrong attempt", "replaced lease", "expired lease", "completed"} {
		t.Run(scenario, func(t *testing.T) {
			id := "lookup-event-" + scenario
			_, st, err := store.CreateRequest(RunnerRequest{ID: id}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				db.Where("request_id = ?", st.ID).Delete(&runnerEventRecord{})
				db.Where("id = ?", st.ID).Delete(&runnerRequestRecord{})
			})
			st.Status, st.ProvisionAttemptID = StatusStopping, "attempt"
			if err := store.BeginRunnerCleanup(t.Context(), st, "", "cleanup", time.Minute); err != nil {
				t.Fatal(err)
			}
			st, _ = store.ReadState(st.ID)
			switch scenario {
			case "stale version":
				st.Version-- // Late Job evidence must not discard an otherwise current observation.
			case "wrong attempt":
				st.ProvisionAttemptID = "old"
			case "replaced lease":
				if err := db.Model(&runnerRequestRecord{}).Where("id = ?", st.ID).UpdateColumn("watch_token", "peer").Error; err != nil {
					t.Fatal(err)
				}
			case "expired lease":
				if err := db.Model(&runnerRequestRecord{}).Where("id = ?", st.ID).UpdateColumn("watch_expires_at", time.Unix(1, 0)).Error; err != nil {
					t.Fatal(err)
				}
			case "completed":
				if err := db.Model(&runnerRequestRecord{}).Where("id = ?", st.ID).UpdateColumn("status", StatusCompleted).Error; err != nil {
					t.Fatal(err)
				}
			}
			before, _ := store.ReadState(st.ID)
			err = store.AppendRunnerCleanupEvent(t.Context(), st, "cleanup", "sandbox_creation_lookup", "safe lookup evidence\n")
			live := scenario == "live" || scenario == "stale version"
			if live && err != nil {
				t.Fatal(err)
			}
			if !live && !errors.Is(err, ErrConflict) {
				t.Fatalf("stale observation accepted: %v", err)
			}
			after, _ := store.ReadState(st.ID)
			if !reflect.DeepEqual(before, after) {
				t.Fatal("diagnostic changed runner state")
			}
			events, _, err := store.ListRunnerEvents(st.ID, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			if live && (len(events) != 1 || events[0].Stage != "sandbox_creation_lookup") {
				t.Fatal("missing event")
			}
			if !live && len(events) != 0 {
				t.Fatal("stale event retained")
			}
		})
	}
}

func TestRunnerCleanupEventSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated SQL test databases")
	}
	for _, backend := range []string{BackendPostgres, BackendMySQL} {
		t.Run(backend, func(t *testing.T) {
			key := "RUNNERD_POSTGRES_TEST_DSN"
			if backend == BackendMySQL {
				key = "RUNNERD_MYSQL_TEST_DSN"
			}
			store := NewWithOptions(Options{Backend: backend, DatabaseDSN: os.Getenv(key), MigrateOnStart: true}).(*DBStore)
			db, err := store.open()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeTestDB(t, db) })
			requireCatalogMatcherTestDatabase(t, db, backend)
			if err := store.initializeSchema(db); err != nil {
				t.Fatal(err)
			}
			store.db = db
			testRunnerCleanupEventFencing(t, store)
		})
	}
}
