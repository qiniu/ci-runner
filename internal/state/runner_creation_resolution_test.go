package state

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func resolutionFixture(t *testing.T, store *DBStore, id string, acceptUnknown bool) (RunnerState, AuditEvent) {
	t.Helper()
	id = fmt.Sprintf("resolution-%s-%d", strings.ReplaceAll(id, " ", "-"), time.Now().UnixNano())
	_, _, err := store.CreateRequest(RunnerRequest{ID: id, JobID: time.Now().UnixNano(), RepositoryFullName: "o/r", RunnerName: "runner-" + id}, nil)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	err = db.Model(&runnerRequestRecord{}).Where("id = ?", id).UpdateColumns(map[string]any{
		"status": StatusStopping, "provision_attempt_id": fmt.Sprintf("attempt-%d", time.Now().UnixNano()), "sandbox_create_dispatched": true,
		"stopping_at": time.Now().UTC().Add(-time.Hour), "watch_token": "owner", "watch_expires_at": time.Now().UTC().Add(time.Minute),
		"last_error_code": "sandbox_create_outcome_unknown", "version": 8,
		"termination_source": TerminationSourceRecoveryCleanup,
	}).Error
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.ReadState(id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Where("resource_type = ? AND resource_id = ?", "runner_request", id).Delete(&auditEventRecord{})
		db.Where("id = ?", id).Delete(&runnerRequestRecord{})
	})
	st.GitHubJobStatus = "completed"
	st.GitHubJobConclusion = "success"
	st.GitHubJobObservedAt = time.Now().UTC()
	payload := `{"evidence":"verified exact dispatch absent and no pending operation","no_sandbox_or_pending_creation":true}`
	if acceptUnknown {
		payload = `{"evidence":"accept historical unknown result and possible orphan resources","accept_unknown_result":true}`
	}
	return st, AuditEvent{Actor: "github:123", Action: "runner.resolve_unknown_creation", ResourceType: "runner_request", ResourceID: id, PayloadJSON: payload}
}

func TestUnknownCreationResolution(t *testing.T) {
	testUnknownCreationResolution(t, New(t.TempDir()).(*DBStore))
}

func testUnknownCreationResolution(t *testing.T, store *DBStore) {
	t.Helper()
	for _, acceptUnknown := range []bool{false, true} {
		name := "verified absence"
		if acceptUnknown {
			name = "accept unknown"
		}
		t.Run(name, func(t *testing.T) { testUnknownCreationResolutionMode(t, store, acceptUnknown) })
	}
}

func testUnknownCreationResolutionMode(t *testing.T, store *DBStore, acceptUnknown bool) {
	t.Helper()
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	t.Run("audited completion and delayed writers", func(t *testing.T) {
		st, audit := resolutionFixture(t, store, "resolved", acceptUnknown)
		st.GitHubJobName = "Build and test"
		st.GitHubJobRunnerName = "runner-that-executed-job"
		store.AppendStagedLog(st.ID, "control.log", "sandbox_creation", []byte("historical creation outcome unknown\n"))
		expectedCode := UnknownCreationResolvedCode
		if acceptUnknown {
			expectedCode = UnknownCreationClosedCode
		}
		beforeCount, _ := store.RunnerCapacityCounts(RunnerRequest{RepositoryFullName: "o/r"})
		if err := store.ResolveUnknownSandboxCreation(t.Context(), st, "owner", acceptUnknown, audit); err != nil {
			t.Fatalf("resolution: %v st=%+v", err, st)
		}
		got, _ := store.ReadState(st.ID)
		if got.Status != StatusCompleted || got.CleanupSandboxDone || !got.CleanupGitHubDone || got.SandboxCreateDispatched || got.ProvisionAttemptID != st.ProvisionAttemptID || got.LastErrorCode != expectedCode || !got.NextRetryAt.IsZero() || got.WatchToken != "" {
			t.Fatalf("bad resolution: %+v", got)
		}
		if got.TerminationSource != st.TerminationSource || !got.StoppingAt.Equal(st.StoppingAt) || !got.CreatedAt.Equal(st.CreatedAt) || got.CompletedAt.IsZero() {
			t.Fatal("closure lost lifecycle evidence")
		}
		log, err := store.ReadLog(st.ID, "control.log", 0)
		if err != nil || string(log) != "historical creation outcome unknown\n" {
			t.Fatalf("closure lost history: %s %v", log, err)
		}
		if got.GitHubJobName != st.GitHubJobName || got.GitHubJobRunnerName != st.GitHubJobRunnerName ||
			got.GitHubJobStatus != st.GitHubJobStatus || got.GitHubJobConclusion != st.GitHubJobConclusion || got.GitHubJobObservedAt.IsZero() {
			t.Fatalf("incomplete retained GitHub Job result: name=%q runner=%q status=%q conclusion=%q observed=%v",
				got.GitHubJobName, got.GitHubJobRunnerName, got.GitHubJobStatus, got.GitHubJobConclusion, got.GitHubJobObservedAt)
		}
		events, _ := store.ListAuditEvents(100)
		found := 0
		for _, e := range events {
			if e.ResourceID == st.ID {
				found++
				if e.Actor != audit.Actor || e.PayloadJSON != audit.PayloadJSON {
					t.Fatal("audit lost evidence")
				}
			}
		}
		if found != 1 {
			t.Fatal("missing audit")
		}
		if err := store.RecordSandboxCreateResult(t.Context(), st, "late-box"); !errors.Is(err, ErrConflict) {
			t.Fatalf("late receipt changed resolution: %v", err)
		}
		if err := store.WriteCleanupState(st, "owner"); !errors.Is(err, ErrConflict) {
			t.Fatalf("old cleanup changed resolution: %v", err)
		}
		counts, _ := store.RunnerCapacityCounts(RunnerRequest{RepositoryFullName: "o/r"})
		if counts.Global != beforeCount.Global-1 {
			t.Fatalf("capacity retained: %+v", counts)
		}
		candidates, err := store.ListExpiredRunnerWatches(t.Context(), 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, candidate := range candidates {
			if candidate.ID == st.ID {
				t.Fatal("closed request still scheduled for reconciliation")
			}
		}
	})
	for _, tc := range []struct {
		name   string
		update map[string]any
		token  string
	}{
		{"expired lease", map[string]any{"watch_expires_at": time.Now().UTC().Add(-time.Minute)}, "owner"},
		{"replaced owner", map[string]any{"watch_token": "peer"}, "owner"},
		{"version changed", map[string]any{"version": 9}, "owner"},
		{"attempt changed", map[string]any{"provision_attempt_id": "replacement"}, "owner"},
		{"recent stop", map[string]any{"stopping_at": time.Now().UTC()}, "owner"},
		{"dispatched JIT", map[string]any{"jit_registration_state": JITDispatched}, "owner"},
		{"known registration receipt", map[string]any{"cleanup_github_runner_id": 456}, "owner"},
		{"not dispatched", map[string]any{"sandbox_create_dispatched": false}, "owner"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st, audit := resolutionFixture(t, store, tc.name, acceptUnknown)
			if err := db.Model(&runnerRequestRecord{}).Where("id = ?", st.ID).UpdateColumns(tc.update).Error; err != nil {
				t.Fatal(err)
			}
			if err := store.ResolveUnknownSandboxCreation(t.Context(), st, tc.token, acceptUnknown, audit); !errors.Is(err, ErrConflict) {
				t.Fatalf("accepted invalid resolution: %v", err)
			}
			events, _ := store.ListAuditEvents(100)
			for _, e := range events {
				if e.ResourceID == st.ID {
					t.Fatal("rejected resolution audited")
				}
			}
		})
	}
	t.Run("positive receipt wins", func(t *testing.T) {
		st, audit := resolutionFixture(t, store, "receipt-first", acceptUnknown)
		if err := store.RecordSandboxCreateResult(t.Context(), st, "real-box"); err != nil {
			t.Fatal(err)
		}
		if err := store.ResolveUnknownSandboxCreation(t.Context(), st, "owner", acceptUnknown, audit); !errors.Is(err, ErrConflict) {
			t.Fatalf("lost receipt: %v", err)
		}
		got, _ := store.ReadState(st.ID)
		if got.SandboxID != "real-box" || got.Status != StatusStopping {
			t.Fatal("resource abandoned")
		}
	})
	t.Run("simultaneous resolutions commit once", func(t *testing.T) {
		st, audit := resolutionFixture(t, store, "contended", acceptUnknown)
		peer := NewWithOptions(Options{Backend: store.opts.Backend, DatabaseDSN: store.opts.DatabaseDSN, MigrateOnStart: false}).(*DBStore)
		peerDB, err := peer.dbOrEnsure()
		if err != nil {
			t.Fatal(err)
		}
		defer closeTestDB(t, peerDB)
		results := make(chan error, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, candidate := range []*DBStore{store, peer} {
			wg.Go(func() {
				<-start
				results <- candidate.ResolveUnknownSandboxCreation(t.Context(), st, "owner", acceptUnknown, audit)
			})
		}
		close(start)
		wg.Wait()
		close(results)
		success := 0
		for err := range results {
			if err == nil {
				success++
			} else if !errors.Is(err, ErrConflict) && !isTransientStoreError(err) {
				t.Fatal(err)
			}
		}
		if success != 1 {
			t.Fatalf("committed %d times", success)
		}
	})
}

func TestUnknownCreationResolutionAuditRollback(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, _ := store.dbOrEnsure()
	if err := db.Exec(`CREATE TRIGGER fail_resolution_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'forced audit failure'); END`).Error; err != nil {
		t.Fatal(err)
	}
	for _, acceptUnknown := range []bool{false, true} {
		st, audit := resolutionFixture(t, store, "rollback", acceptUnknown)
		if err := store.ResolveUnknownSandboxCreation(t.Context(), st, "owner", acceptUnknown, audit); !errors.Is(err, ErrAuditEventPersistence) {
			t.Fatal(err)
		}
		got, _ := store.ReadState(st.ID)
		if got.Status != StatusStopping || got.Version != st.Version || !got.SandboxCreateDispatched {
			t.Fatal("unaudited mutation committed")
		}
	}
}

func TestUnknownCreationResolutionSQLBackends(t *testing.T) {
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
			// Verify the database identity before startup migration or any writes.
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
			testUnknownCreationResolution(t, store)
			name := "fail_resolution_audit"
			var create, drop string
			if backend == BackendPostgres {
				if err := db.Exec(`CREATE OR REPLACE FUNCTION fail_resolution_audit_fn() RETURNS trigger AS $$ BEGIN RAISE EXCEPTION 'forced audit failure'; END; $$ LANGUAGE plpgsql`).Error; err != nil {
					t.Fatal(err)
				}
				create = "CREATE TRIGGER " + name + " BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION fail_resolution_audit_fn()"
				drop = "DROP TRIGGER " + name + " ON audit_events"
			} else {
				create = "CREATE TRIGGER " + name + " BEFORE INSERT ON audit_events FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'forced audit failure'"
				drop = "DROP TRIGGER " + name
			}
			if err := db.Exec(create).Error; err != nil {
				t.Fatal(err)
			}
			defer func() {
				db.Exec(drop)
				if backend == BackendPostgres {
					db.Exec("DROP FUNCTION fail_resolution_audit_fn()")
				}
			}()
			for _, acceptUnknown := range []bool{false, true} {
				st, audit := resolutionFixture(t, store, "sql-rollback", acceptUnknown)
				if err := store.ResolveUnknownSandboxCreation(t.Context(), st, "owner", acceptUnknown, audit); !errors.Is(err, ErrAuditEventPersistence) {
					t.Fatalf("%s: %v", backend, err)
				}
				got, _ := store.ReadState(st.ID)
				if got.Status != StatusStopping || got.Version != st.Version {
					t.Fatal("audit failed but state committed")
				}
			}
		})
	}
}
