package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

type phasedTestFixture struct {
	source, target *gorm.DB
	options        PhasedMigrationOptions
}

func newPhasedTestFixture(t *testing.T) phasedTestFixture {
	t.Helper()
	dsn := offlinePostgresDSN(t)
	source := New(offlinePrivateDir(t)).(*DBStore)
	db, err := source.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestDB(t, db) })
	target := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn}).(*DBStore)
	pg, err := target.open()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestDB(t, pg) })
	return phasedTestFixture{source: db, target: pg, options: PhasedMigrationOptions{
		OfflineMigrationOptions: OfflineMigrationOptions{SQLitePath: source.opts.DatabaseDSN, BackupPath: filepath.Join(offlinePrivateDir(t), "online.db"), PostgresDSN: dsn},
		SkipEvents:              true, SourceStopped: true, MaxFinalChanges: 10000,
	}}
}

func (f phasedTestFixture) run(t *testing.T, mode string) PhasedMigrationReport {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f.options.Mode = mode
	report, err := MigrateSQLiteToPostgresPhased(ctx, f.options)
	if err != nil {
		t.Fatalf("%s: %v", mode, err)
	}
	return report
}

func phasedTestRequest(id string) runnerRequestRecord {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return runnerRequestRecord{ID: id, Source: "github", RunnerName: "runner-" + id, LabelsJSON: "[]", Status: StatusCompleted, QueuedAt: now, UpdatedAt: now, GitHubContextBackfilled: true}
}

func phasedAssertError(t *testing.T, f phasedTestFixture, mode, want string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	f.options.Mode = mode
	_, err := MigrateSQLiteToPostgresPhased(ctx, f.options)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("%s: error=%v want %q", mode, err, want)
	}
}

func TestPhasedMigrationLifecycleSQLBackends(t *testing.T) {
	f := newPhasedTestFixture(t)
	for i := 0; i < 520; i++ {
		r := phasedTestRequest(fmt.Sprintf("r-%04d", i))
		if err := f.source.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	events := make([]runnerEventRecord, 700)
	for i := range events {
		events[i] = runnerEventRecord{RequestID: "r-0001", Message: strings.Repeat("history", 200), CreatedAt: time.Now().UTC()}
	}
	if err := f.source.CreateInBatches(events, 100).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.source.Create(&runnerEventRecord{ID: 10000, RequestID: "r-0001", Message: "deleted historical ID"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.source.Delete(&runnerEventRecord{}, 10000).Error; err != nil {
		t.Fatal(err)
	}
	secret := accountSecretRecord{ScopeType: "account", ScopeID: 1, KeyType: "sandbox", EncryptedValue: "private-original"}
	if err := f.source.Create(&secret).Error; err != nil {
		t.Fatal(err)
	}
	report := f.run(t, "prepare")
	if report.ReadyForPG || report.ReadyForCutover {
		t.Fatalf("premature readiness: %+v", report)
	}
	f.run(t, "prepare") // existing backup and target are retained, not overwritten
	if err := f.target.Create(&accountRecord{Role: "user"}).Error; err == nil {
		t.Fatal("unreleased PG accepted a business write")
	}
	if err := validatePhasedMigrationStartup(f.target); err == nil {
		t.Fatal("unreleased PG accepted startup")
	}
	if err := validatePhasedMigrationStartup(f.source); err != nil {
		t.Fatal(err)
	}
	// Stop after a committed baseline page. No process-local cursor is reused.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	options := f.options
	options.Mode = "sync"
	options.Progress = func(p OfflineMigrationProgress) {
		if p.Stage == "copy and verify request baseline" && p.Rows == 256 {
			cancel()
		}
	}
	if _, err := MigrateSQLiteToPostgresPhased(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	cancel()
	report = f.run(t, "status")
	if report.RequestsCopied != 256 || report.ReadyForCutover {
		t.Fatalf("checkpoint: %+v", report)
	}
	// Capture changes without updated_at/version and insert behind the baseline cursor.
	if err := f.source.Model(&runnerRequestRecord{}).Where("id = ?", "r-0001").UpdateColumns(map[string]any{"runner_exit_code": 17, "github_job_conclusion": "success"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.source.Delete(&runnerRequestRecord{}, "id = ?", "r-0002").Error; err != nil {
		t.Fatal(err)
	}
	added := phasedTestRequest("a-new")
	if err := f.source.Create(&added).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.source.Delete(&accountSecretRecord{}, "scope_id = ?", 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := f.source.Create(&runnerEventRecord{RequestID: "r-0001", Message: "late exit evidence"}).Error; err != nil {
		t.Fatal(err)
	}
	report = f.run(t, "sync")
	if !report.ReadyForCutover || report.EventsCopied != 0 || report.PendingRequestChanges != 0 {
		t.Fatalf("sync: %+v", report)
	}
	inventory, _ := newMigrationInventory()
	requests, _ := migrationTableByName(inventory, "runner_requests")
	if _, err := verifyOfflineTable(f.source, f.target, requests); err != nil {
		t.Fatal(err)
	}
	var secrets int64
	f.target.Model(&accountSecretRecord{}).Count(&secrets)
	if secrets != 0 {
		t.Fatal("deleted secret resurrected")
	}
	// Event copying is independently resumable before or after cutover.
	ctx, cancel = context.WithTimeout(context.Background(), time.Minute)
	options = f.options
	options.Mode = "sync"
	options.SkipEvents = false
	options.Progress = func(p OfflineMigrationProgress) {
		if p.Stage == "append and verify event history" && p.Rows == 256 {
			cancel()
		}
	}
	if _, err := MigrateSQLiteToPostgresPhased(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("event cancellation: %v", err)
	}
	cancel()
	report = f.run(t, "cutover")
	if !report.ReadyForPG || !report.SourceSealed || report.HistoryComplete || report.EventsCopied != 256 || report.EventHighWatermark != 10001 {
		t.Fatalf("cutover: %+v", report)
	}
	if err := f.source.Model(&runnerRequestRecord{}).Where("id = ?", "r-0001").Update("status", StatusQueued).Error; err == nil {
		t.Fatal("sealed source accepted update")
	}
	if err := validatePhasedMigrationStartup(f.source); err == nil {
		t.Fatal("sealed source accepted startup")
	}
	if err := validatePhasedMigrationStartup(f.target); err != nil {
		t.Fatal(err)
	}
	phasedAssertError(t, f, "sync", "sync requires")
	phasedAssertError(t, f, "resume-source", "PG is already released")
	// PG writes must survive repeated cutover/backfill and occupy IDs after the entire frozen history.
	pgEvent := runnerEventRecord{RequestID: "r-0001", Message: "new PG event"}
	if err := f.target.Create(&pgEvent).Error; err != nil {
		t.Fatal(err)
	}
	if pgEvent.ID <= 10001 {
		t.Fatalf("ID collision: %d", pgEvent.ID)
	}
	if err := f.target.Model(&runnerRequestRecord{}).Where("id = ?", "r-0001").Update("status", StatusQueued).Error; err != nil {
		t.Fatal(err)
	}
	f.run(t, "cutover")
	report = f.run(t, "backfill")
	if !report.HistoryComplete || report.EventsCopied != 701 {
		t.Fatalf("backfill: %+v", report)
	}
	f.run(t, "backfill")
	var got runnerRequestRecord
	if err := f.target.First(&got, "id = ?", "r-0001").Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusQueued {
		t.Fatal("old SQLite overwrote PG request")
	}
	var eventCount int64
	f.target.Model(&runnerEventRecord{}).Count(&eventCount)
	if eventCount != 702 {
		t.Fatalf("events=%d", eventCount)
	}
	next := runnerEventRecord{RequestID: "r-0001", Message: "another PG event"}
	if err := f.target.Create(&next).Error; err != nil || next.ID <= pgEvent.ID {
		t.Fatalf("sequence regressed: %+v %v", next, err)
	}
}

func TestPhasedMigrationCutoverRecoverySQLBackends(t *testing.T) {
	f := newPhasedTestFixture(t)
	request := phasedTestRequest("r")
	if err := f.source.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	f.run(t, "prepare")
	f.run(t, "sync")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	options := f.options
	options.Mode = "cutover"
	options.Progress = func(p OfflineMigrationProgress) {
		if strings.HasPrefix(p.Stage, "refresh and verify table ") {
			cancel()
		}
	}
	if _, err := MigrateSQLiteToPostgresPhased(ctx, options); err == nil {
		t.Fatal("cutover ignored cancellation")
	}
	cancel()
	report := f.run(t, "status")
	if !report.SourceSealed || report.ReadyForPG {
		t.Fatalf("failed cutover: %+v", report)
	}
	f.run(t, "resume-source")
	if err := f.source.Model(&runnerRequestRecord{}).Where("id = ?", "r").Update("runner_exit_code", 2).Error; err != nil {
		t.Fatal(err)
	}
	f.run(t, "sync")
	f.run(t, "cutover")
	var got runnerRequestRecord
	f.target.First(&got, "id = ?", "r")
	if got.RunnerExitCode == nil || *got.RunnerExitCode != 2 {
		t.Fatal("resumed changes lost")
	}
}

func TestPhasedMigrationGuardsSQLBackends(t *testing.T) {
	t.Run("changed capture", func(t *testing.T) {
		f := newPhasedTestFixture(t)
		f.run(t, "prepare")
		if err := f.source.Exec("DROP TRIGGER runnerd_transfer_runner_requests_update").Error; err != nil {
			t.Fatal(err)
		}
		phasedAssertError(t, f, "sync", "triggers changed")
	})
	t.Run("wrong target", func(t *testing.T) {
		f := newPhasedTestFixture(t)
		f.run(t, "prepare")
		f.options.PostgresDSN = offlinePostgresDSN(t)
		phasedAssertError(t, f, "prepare", "another target")
	})
	t.Run("append only events", func(t *testing.T) {
		f := newPhasedTestFixture(t)
		if err := f.source.Create(&runnerEventRecord{ID: 20, RequestID: "r", Message: "old"}).Error; err != nil {
			t.Fatal(err)
		}
		f.run(t, "prepare")
		if err := f.source.Delete(&runnerEventRecord{}, 20).Error; err == nil {
			t.Fatal("event delete accepted")
		}
		if err := f.source.Create(&runnerEventRecord{ID: 10, RequestID: "r", Message: "out of order"}).Error; err == nil {
			t.Fatal("out of order event accepted")
		}
	})
	t.Run("backlog budget", func(t *testing.T) {
		f := newPhasedTestFixture(t)
		f.run(t, "prepare")
		f.run(t, "sync")
		for i := 0; i < 3; i++ {
			r := phasedTestRequest(fmt.Sprint(i))
			if err := f.source.Create(&r).Error; err != nil {
				t.Fatal(err)
			}
		}
		f.options.MaxFinalChanges = 1
		phasedAssertError(t, f, "cutover", "too many final")
		if f.run(t, "status").SourceSealed {
			t.Fatal("budget failure sealed source")
		}
	})
	t.Run("event conflict", func(t *testing.T) {
		f := newPhasedTestFixture(t)
		if err := f.source.Create(&runnerEventRecord{ID: 10, RequestID: "r", Message: "original"}).Error; err != nil {
			t.Fatal(err)
		}
		f.run(t, "prepare")
		f.run(t, "sync")
		f.run(t, "cutover")
		if err := f.target.Create(&runnerEventRecord{ID: 10, RequestID: "r", Message: "different"}).Error; err != nil {
			t.Fatal(err)
		}
		phasedAssertError(t, f, "backfill", "content differs")
		if f.run(t, "status").HistoryComplete {
			t.Fatal("conflict marked history complete")
		}
	})
	t.Run("backup exclusive", func(t *testing.T) {
		f := newPhasedTestFixture(t)
		if err := os.WriteFile(f.options.BackupPath, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		phasedAssertError(t, f, "prepare", "create private online SQLite backup")
		data, _ := os.ReadFile(f.options.BackupPath)
		if string(data) != "keep" {
			t.Fatal("backup replaced")
		}
	})
}

func TestPhasedMigrationLiveWritesSQLBackends(t *testing.T) {
	f := newPhasedTestFixture(t)
	request := phasedTestRequest("live")
	if err := f.source.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	f.run(t, "prepare")
	// All mutations come through an independent connection while the tool owns
	// its own source connection. Capture and catch-up must include late evidence.
	done := make(chan struct{})
	failures := make(chan error, 1)
	go func() {
		defer close(done)
		for i := 1; i <= 100; i++ {
			if err := f.source.Model(&runnerRequestRecord{}).Where("id = ?", "live").UpdateColumns(map[string]any{"runner_exit_code": i}).Error; err != nil {
				failures <- err
				return
			}
			if err := f.source.Create(&runnerEventRecord{RequestID: "live", Message: fmt.Sprint(i)}).Error; err != nil {
				failures <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()
	f.options.SkipEvents = false
	f.run(t, "sync")
	<-done
	select {
	case err := <-failures:
		t.Fatal(err)
	default:
	}
	f.run(t, "sync")
	f.run(t, "cutover")
	f.run(t, "backfill")
	var got runnerRequestRecord
	if err := f.target.First(&got, "id = ?", "live").Error; err != nil {
		t.Fatal(err)
	}
	if got.RunnerExitCode == nil || *got.RunnerExitCode != 100 {
		t.Fatal("live terminal update lost")
	}
	var count int64
	f.target.Model(&runnerEventRecord{}).Count(&count)
	if count != 100 {
		t.Fatalf("live events=%d", count)
	}
}

func TestPhasedMigrationInterruptedPrepareSQLBackends(t *testing.T) {
	f := newPhasedTestFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	options := f.options
	options.Mode = "prepare"
	options.Progress = func(p OfflineMigrationProgress) {
		if p.Stage == "prepare fenced PostgreSQL schema" {
			cancel()
		}
	}
	if _, err := MigrateSQLiteToPostgresPhased(ctx, options); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepare cancellation=%v", err)
	}
	cancel()
	// Source capture was installed, PG DDL did not commit. Reuse the established
	// binding and original backup; do not require an empty source or overwrite it.
	f.run(t, "prepare")
	f.run(t, "sync")
	f.run(t, "cutover")
}

func TestPhasedMigrationTargetGuardsAndBudgetSQLBackends(t *testing.T) {
	t.Run("target guard removed", func(t *testing.T) {
		f := newPhasedTestFixture(t)
		f.run(t, "prepare")
		if err := f.target.Exec("DROP TRIGGER runnerd_transfer_guard ON runner_requests").Error; err != nil {
			t.Fatal(err)
		}
		phasedAssertError(t, f, "sync", "target write guards changed")
	})
	t.Run("target truncate", func(t *testing.T) {
		f := newPhasedTestFixture(t)
		f.run(t, "prepare")
		if err := f.target.Exec("TRUNCATE runner_requests").Error; err == nil {
			t.Fatal("unreleased target allowed truncate")
		}
	})
	t.Run("final table budget", func(t *testing.T) {
		f := newPhasedTestFixture(t)
		for i := 0; i < 3; i++ {
			if err := f.source.Create(&accountRecord{Role: "user"}).Error; err != nil {
				t.Fatal(err)
			}
		}
		f.run(t, "prepare")
		f.run(t, "sync")
		f.options.MaxFinalRows = 1
		phasedAssertError(t, f, "cutover", "final table row budget exceeded")
		report := f.run(t, "status")
		if report.SourceSealed || report.ReadyForCutover || report.FinalTableRows != 3 {
			t.Fatalf("budget status: %+v", report)
		}
	})
	t.Run("startup boundary", func(t *testing.T) {
		f := newPhasedTestFixture(t)
		f.run(t, "prepare")
		pg := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: f.options.PostgresDSN, MigrateOnStart: true}).(*DBStore)
		if err := pg.Ensure(); err == nil {
			t.Fatal("PG service started before cutover")
		}
		f.run(t, "sync")
		f.run(t, "cutover")
		source := NewWithOptions(Options{Backend: BackendSQLite, DatabaseDSN: f.options.SQLitePath, MigrateOnStart: true}).(*DBStore)
		if err := source.Ensure(); err == nil {
			t.Fatal("SQLite service started after cutover")
		}
		if err := pg.Ensure(); err != nil {
			t.Fatal(err)
		}
		closeTestDB(t, pg.db)
	})
}

func TestPhasedMigrationConcurrentBackfillSQLBackends(t *testing.T) {
	f := newPhasedTestFixture(t)
	old := phasedTestRequest("old")
	if err := f.source.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	events := make([]runnerEventRecord, 800)
	for i := range events {
		events[i] = runnerEventRecord{RequestID: "old", Message: strings.Repeat("old event", 100)}
	}
	if err := f.source.CreateInBatches(events, 100).Error; err != nil {
		t.Fatal(err)
	}
	f.run(t, "prepare")
	f.run(t, "sync")
	f.run(t, "cutover")
	started, done := make(chan struct{}), make(chan error, 1)
	go func() {
		<-started
		for i := 0; i < 100; i++ {
			if err := f.target.Create(&runnerEventRecord{RequestID: "old", Message: "new PG event"}).Error; err != nil {
				done <- err
				return
			}
			if err := f.target.Model(&runnerRequestRecord{}).Where("id = ?", "old").Update("retry_count", i).Error; err != nil {
				done <- err
				return
			}
			time.Sleep(time.Millisecond)
		}
		done <- nil
	}()
	released := false
	f.options.BatchPause = 5 * time.Millisecond
	f.options.Progress = func(p OfflineMigrationProgress) {
		if p.Stage == "append and verify event history" && !released {
			released = true
			close(started)
		}
	}
	report := f.run(t, "backfill")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if !report.HistoryComplete || report.PendingEventRows != 0 {
		t.Fatalf("backfill incomplete: %+v", report)
	}
	var count int64
	f.target.Model(&runnerEventRecord{}).Count(&count)
	if count != 900 {
		t.Fatalf("concurrent events=%d", count)
	}
	var got runnerRequestRecord
	f.target.First(&got, "id = ?", "old")
	if got.RetryCount != 99 {
		t.Fatal("backfill overwrote PG request")
	}
}

func TestPhasedMigrationStatusDuringSyncSQLBackends(t *testing.T) {
	f := newPhasedTestFixture(t)
	request := phasedTestRequest("r")
	if err := f.source.Create(&request).Error; err != nil {
		t.Fatal(err)
	}
	f.run(t, "prepare")
	inspected := false
	var inspectErr error
	f.options.Progress = func(p OfflineMigrationProgress) {
		if inspected || p.Stage != "copy and verify request baseline" || p.Rows != 1 {
			return
		}
		inspected = true
		options := f.options
		options.Mode = "status"
		options.Progress = nil
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, inspectErr = MigrateSQLiteToPostgresPhased(ctx, options)
	}
	f.run(t, "sync")
	if !inspected || inspectErr != nil {
		t.Fatalf("status during sync: inspected=%t err=%v", inspected, inspectErr)
	}
}
