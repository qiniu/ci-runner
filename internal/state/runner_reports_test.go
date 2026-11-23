package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func reportTestStore(t *testing.T) *DBStore {
	t.Helper()
	s := New(t.TempDir()).(*DBStore)
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestDB(t, db) })
	return s
}

func reportTime(s string) time.Time {
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return v
}
func reportPtr(t time.Time) *time.Time { return &t }
func reportFixture(t *testing.T, s *DBStore) (RunnerReportQuery, time.Time) {
	t.Helper()
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	now := reportTime("2026-11-20T10:00:00Z")
	start := reportTime("2026-11-19T01:00:00Z")
	owners := []githubInstallationOwnerRecord{{InstallationID: 1, GitHubAccountID: 10, AccountLogin: "team"}, {InstallationID: 2, GitHubAccountID: 10, AccountLogin: "team"}}
	if err := db.Create(&owners).Error; err != nil {
		t.Fatal(err)
	}
	rows := []runnerRequestRecord{
		{ID: "success", QueuedAt: start, Status: StatusCompleted, GitHubInstallationID: 1, RepositoryFullName: "team/one", ProfileName: "standard", ProfileSource: "global", CreatingAt: reportPtr(start.Add(30 * time.Second)), RunningAt: reportPtr(start.Add(time.Minute)), StoppingAt: reportPtr(start.Add(11 * time.Minute)), CompletedAt: reportPtr(start.Add(12 * time.Minute)), WorkflowJobID: new(int64(1)), GitHubJobStatus: "completed", GitHubJobConclusion: "failure", GitHubJobObservedAt: reportPtr(start.Add(12 * time.Minute))},
		{ID: "rejected", QueuedAt: start, Status: StatusFailed, FailureStage: "admission", FailedAt: reportPtr(start.Add(time.Minute)), GitHubInstallationID: 1, RepositoryFullName: "team/one"},
		{ID: "custom", QueuedAt: start.Add(time.Hour), Status: StatusFailed, GitHubInstallationID: 2, RepositoryFullName: "team/two", ProfileName: "standard", ProfileSource: "scoped_custom", ProfileScopeType: "account", ProfileScopeID: 7, RunningAt: reportPtr(start.Add(2 * time.Hour)), StoppingAt: reportPtr(start.Add(3 * time.Hour)), FailedAt: reportPtr(start.Add(4 * time.Hour))},
		{ID: "bad-time", QueuedAt: start, Status: StatusCompleted, GitHubInstallationID: 3, RepositoryFullName: "team/one", ProfileName: "removed", RunningAt: reportPtr(start.Add(time.Hour)), CompletedAt: reportPtr(start.Add(time.Minute))},
		{ID: "active", QueuedAt: start, Status: StatusRunning, GitHubInstallationID: 1, RepositoryFullName: "team/one", RunningAt: reportPtr(start.Add(time.Hour))},
		{ID: "old-active", QueuedAt: start.AddDate(0, 0, -20), Status: StatusQueued, GitHubInstallationID: 1, RepositoryFullName: "team/one"},
		{ID: "previous", QueuedAt: reportTime("2026-11-17T02:00:00Z"), Status: StatusCompleted, GitHubInstallationID: 1, RepositoryFullName: "team/one"},
		{ID: "previous-too-late", QueuedAt: reportTime("2026-11-18T10:00:00Z"), Status: StatusCompleted, GitHubInstallationID: 1},
		{ID: "future-boundary", QueuedAt: now, Status: StatusRunning, GitHubInstallationID: 1},
	}
	for i := range rows {
		rows[i].Source = "fixture"
		rows[i].RunnerName = rows[i].ID
		rows[i].LabelsJSON = "[]"
		rows[i].UpdatedAt = now
		rows[i].GitHubPayloadJSON = "SECRET_PAYLOAD"
		rows[i].SandboxAPIKeyEncrypted = "SECRET_KEY"
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return RunnerReportQuery{From: "2026-11-19", To: "2026-11-21", Timezone: "UTC", Grain: "day"}, now
}

func verifyRunnerReport(t *testing.T, s *DBStore) {
	t.Helper()
	q, now := reportFixture(t, s)
	r, err := s.ReadRunnerReport(context.Background(), q, now)
	if err != nil {
		t.Fatal(err)
	}
	total := r.Totals
	if total.Requests != 5 || total.Started != 4 || total.Rejected != 1 || total.Terminal != 4 || total.ActiveAccounts != 1 || total.ActiveRepositories != 2 || total.MappedAccounts != 4 || total.KnownSpecs != 2 {
		t.Fatalf("totals=%+v", total)
	}
	if total.Runtime.Seconds != 4200 || total.Runtime.Samples != 2 || total.Runtime.Eligible != 3 || total.JobResults != 1 || total.JobResultEligible != 1 {
		t.Fatalf("duration/result coverage=%+v", total)
	}
	if r.Previous.Requests != 1 || len(r.Trend) != 2 || r.Trend[0].Requests != 5 || r.Trend[1].Requests != 0 {
		t.Fatalf("comparison/trend=%+v / %+v", r.Previous, r.Trend)
	}
	groups, err := r.Groups("spec", "runtime")
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	var secs float64
	for _, g := range groups {
		sum += g.Requests
		secs += g.Runtime.Seconds
		detail, err := r.Requests("spec", g.Key)
		if err != nil || int64(len(detail)) != g.Requests {
			t.Fatalf("group mismatch %v", err)
		}
	}
	if sum != total.Requests || secs != total.Runtime.Seconds || len(groups) != 4 {
		t.Fatalf("groups=%+v", groups)
	}
	if groups[0].Key == groups[1].Key {
		t.Fatal("scoped identity collapsed")
	}
	// Captured drilldowns remain stable even after lifecycle mutations.
	db, _ := s.dbOrEnsure()
	if err := db.Model(&runnerRequestRecord{}).Where("id = ?", "active").Update("status", StatusCompleted).Error; err != nil {
		t.Fatal(err)
	}
	captured, _ := r.Requests("", "")
	for _, v := range captured {
		if v.ID == "active" && v.Status != StatusRunning {
			t.Fatal("snapshot changed")
		}
		if v.ID == "success" && v.JobResult != "failure" {
			t.Fatal("Runner completion mapped to Job success")
		}
	}
	encoded, _ := json.Marshal(captured)
	if strings.Contains(string(encoded), "SECRET") {
		t.Fatal("secrets escaped")
	}
	q.Owner = "github:10"
	q.Repository = "team/two"
	q.Spec = reportSpecKey(reportSpecIdentity{"scoped_custom", "account", 7, "standard"})
	filtered, err := s.ReadRunnerReport(context.Background(), q, now)
	if err != nil || filtered.Totals.Requests != 1 || filtered.Previous.Requests != 0 {
		t.Fatalf("filtered=%+v err=%v", filtered, err)
	}
	q.Spec = ""
	q.Repository = "team/one"
	active, err := s.ReadRunnerReportActive(context.Background(), q)
	if err != nil || active[StatusQueued] != 1 || active[StatusRunning] != 0 {
		t.Fatalf("active=%v err=%v", active, err)
	}
	q.Owner = "installation:3"
	unknown, err := s.ReadRunnerReport(context.Background(), q, now)
	if err != nil || unknown.Totals.Requests != 1 {
		t.Fatalf("unknown=%+v err=%v", unknown, err)
	}
}
func TestRunnerReportCohortsAndIdentity(t *testing.T) { verifyRunnerReport(t, reportTestStore(t)) }
func TestRunnerReportLocalMidnightUsesUTCStorage(t *testing.T) {
	s := reportTestStore(t)
	db, _ := s.dbOrEnsure()
	now := reportTime("2026-11-20T10:00:00Z")
	for id, timestamp := range map[string]string{"current": "2026-11-19T16:30:00Z", "gap": "2026-11-19T15:59:00Z", "previous": "2026-11-18T17:00:00Z", "end": "2026-11-20T10:00:00Z"} {
		if err := db.Create(&runnerRequestRecord{ID: id, RunnerName: id, Source: "fixture", LabelsJSON: "[]", QueuedAt: reportTime(timestamp), UpdatedAt: now, Status: StatusQueued}).Error; err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.ReadRunnerReport(context.Background(), RunnerReportQuery{From: "2026-11-20", To: "2026-11-21", Timezone: "Asia/Singapore", Grain: "day"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Rows) != 1 || r.Rows[0].ID != "current" || r.Previous.Requests != 1 || len(r.Trend) != 1 || r.Trend[0].Date != "2026-11-20" {
		t.Fatalf("wrong local midnight: %+v", r)
	}
}

func TestRunnerReportTimeBoundaries(t *testing.T) {
	now := reportTime("2026-11-20T10:00:00Z")
	for _, test := range []struct {
		name  string
		q     RunnerReportQuery
		valid bool
	}{
		{"UTC", RunnerReportQuery{From: "2026-11-19", To: "2026-11-21", Timezone: "UTC", Grain: "day"}, true},
		{"bad zone", RunnerReportQuery{From: "2026-11-19", To: "2026-11-21", Timezone: "Local", Grain: "day"}, false},
		{"bad dates", RunnerReportQuery{From: "2026-11-21", To: "2026-11-21", Timezone: "UTC", Grain: "day"}, false},
		{"future", RunnerReportQuery{From: "2026-11-19", To: "2026-11-23", Timezone: "UTC", Grain: "day"}, false},
		{"limit", RunnerReportQuery{From: "2025-01-01", To: "2026-11-21", Timezone: "UTC", Grain: "day"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.q.Window(now)
			if (err == nil) != test.valid {
				t.Fatalf("err=%v", err)
			}
		})
	}
	q := RunnerReportQuery{From: "2026-10-31", To: "2026-11-03", Timezone: "America/New_York", Grain: "week"}
	w, err := q.Window(now)
	if err != nil {
		t.Fatal(err)
	}
	if w.End.Sub(w.Start) != 73*time.Hour {
		t.Fatalf("DST window=%v", w.End.Sub(w.Start))
	}
	if reportBucketDate(reportTime("2026-11-01T03:00:00Z"), w.Location, "day") != "2026-10-31" || reportBucketDate(w.Start, w.Location, "week") != "2026-10-26" {
		t.Fatal("local calendar bucket incorrect")
	}
	zero := time.Time{}
	start := now.Add(-time.Hour)
	for _, end := range []*time.Time{nil, &zero, reportPtr(start.Add(-time.Minute)), reportPtr(now.Add(time.Minute))} {
		if reportSeconds(&start, end, now) != nil {
			t.Fatal("invalid duration accepted")
		}
	}
	p := reportProjection{RunningAt: &start, Status: StatusCompleted, CompletedAt: reportPtr(now), StoppingAt: reportPtr(now.Add(time.Minute))}
	if p.request(now).RuntimeSeconds != nil {
		t.Fatal("stopping after terminal accepted")
	}
}

func TestRunnerReportQueryPlanAndCancellation(t *testing.T) {
	s := reportTestStore(t)
	q, now := reportFixture(t, s)
	db, _ := s.dbOrEnsure()
	var plan []struct{ Detail string }
	err := db.Raw("EXPLAIN QUERY PLAN SELECT r.id, r.queued_at FROM runner_requests r LEFT JOIN github_installation_owners o ON o.installation_id=r.github_installation_id WHERE (r.queued_at >= ? AND r.queued_at < ?) OR (r.queued_at >= ? AND r.queued_at < ?) ORDER BY r.queued_at DESC, r.id ASC LIMIT 100001", now.Add(-time.Hour), now, now.Add(-48*time.Hour), now.Add(-47*time.Hour)).Scan(&plan).Error
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("report plan: %+v", plan)
	found := false
	for _, v := range plan {
		if strings.Contains(v.Detail, "idx_runner_requests_queued_id") {
			found = true
		}
	}
	if !found {
		t.Fatalf("range index missing: %+v", plan)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.ReadRunnerReport(ctx, q, now); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestRunnerReportInterruptsSQLiteScan(t *testing.T) {
	for _, name := range []string{"disk", "memory"} {
		t.Run(name, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "reports.db")
			if name == "memory" {
				dsn = ":memory:"
			}
			s := NewWithOptions(Options{Backend: BackendSQLite, DatabaseDSN: dsn, MigrateOnStart: true}).(*DBStore)
			db, err := s.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { closeTestDB(t, db) })
			q, now := reportFixture(t, s)

			// The report cannot produce its first matching row until SQLite finishes
			// this scalar subquery. A context check between rows is too late.
			if err := db.Exec("ALTER TABLE github_installation_owners RENAME TO fixture_owners").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(`CREATE VIEW github_installation_owners AS
		SELECT installation_id, account_login, github_account_id + (
			WITH RECURSIVE seq(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM seq WHERE x < 6000000)
			SELECT min(x)-1 FROM seq
		) AS github_account_id FROM fixture_owners`).Error; err != nil {
				t.Fatal(err)
			}
			q.Owner = "github:10"
			for _, active := range []bool{false, true} {
				ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
				start := time.Now()
				if active {
					_, err = s.ReadRunnerReportActive(ctx, q)
				} else {
					_, err = s.ReadRunnerReport(ctx, q, now)
				}
				cancel()
				if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
					t.Fatalf("executing report (active=%t) returned %v after %s", active, err, time.Since(start))
				}
			}
			if err := db.Exec("DROP VIEW github_installation_owners").Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec("ALTER TABLE fixture_owners RENAME TO github_installation_owners").Error; err != nil {
				t.Fatal(err)
			}
			next, err := s.ReadRunnerReport(context.Background(), q, now)
			if err != nil || next.Totals.Requests != 4 {
				t.Fatalf("subsequent report after interruptions: %v, err=%v", next, err)
			}
		})
	}
}

func TestRunnerReportSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL/MySQL _test databases")
	}
	for _, backend := range []struct{ name, key string }{{BackendPostgres, "RUNNERD_POSTGRES_TEST_DSN"}, {BackendMySQL, "RUNNERD_MYSQL_TEST_DSN"}} {
		t.Run(backend.name, func(t *testing.T) {
			dsn := os.Getenv(backend.key)
			if dsn == "" {
				t.Fatal("missing dedicated test DSN")
			}
			setup := NewWithOptions(Options{Backend: backend.name, DatabaseDSN: dsn}).(*DBStore)
			db, err := setup.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			requireCatalogMatcherTestDatabase(t, db, backend.name)
			resetSQLBackendTestTables(t, db)
			defer func() { resetSQLBackendTestTables(t, db); closeTestDB(t, db) }()
			s := NewWithOptions(Options{Backend: backend.name, DatabaseDSN: dsn, MigrateOnStart: true}).(*DBStore)
			if err := s.Ensure(); err != nil {
				t.Fatal(err)
			}
			sdb, _ := s.dbOrEnsure()
			defer closeTestDB(t, sdb)
			verifyRunnerReport(t, s)
			verifyReportGlobalSpecIdentity(t, s)
		})
	}
}

func TestRunnerReportScale(t *testing.T) {
	if os.Getenv("RUNNERD_REPORT_SCALE_TEST") != "1" {
		t.Skip("set RUNNERD_REPORT_SCALE_TEST=1 for the 100000-row scale gate")
	}
	s := reportTestStore(t)
	db, _ := s.dbOrEnsure()
	now := reportTime("2026-11-20T10:00:00Z")
	start := now.Add(-time.Hour)
	for batch := 0; batch < 100; batch++ {
		records := make([]runnerRequestRecord, 1000)
		for i := range records {
			id := fmt.Sprintf("report-%06d", batch*1000+i)
			records[i] = runnerRequestRecord{ID: id, Source: "fixture", RunnerName: id, LabelsJSON: "[]", ProfileSource: "global", ProfileName: "standard", RepositoryFullName: fmt.Sprintf("team/repo-%d", i%20), Status: StatusCompleted, QueuedAt: start, UpdatedAt: now, RunningAt: reportPtr(start.Add(time.Minute)), CompletedAt: reportPtr(now.Add(-time.Minute)), GitHubPayloadJSON: strings.Repeat("x", 1024)}
		}
		if err := db.CreateInBatches(records, 100).Error; err != nil {
			t.Fatal(err)
		}
	}
	q := RunnerReportQuery{From: "2026-11-19", To: "2026-11-21", Timezone: "UTC", Grain: "day"}
	// Two queued requests outside the cohort let us measure real queue claims.
	for _, id := range []string{"baseline-claim", "concurrent-claim"} {
		if err := db.Create(&runnerRequestRecord{ID: id, RunnerName: id, Source: "fixture", LabelsJSON: "[]", Status: StatusQueued, QueuedAt: start.AddDate(0, 0, -100), UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	baselineStart := time.Now()
	if _, _, ok, err := s.ClaimNextRunnable("report-baseline", time.Minute); err != nil || !ok {
		t.Fatalf("baseline claim: %v %v", ok, err)
	}
	t.Logf("baseline queue claim=%s", time.Since(baselineStart))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reportDone := make(chan error, 1)
	go func() { _, err := s.ReadRunnerReport(ctx, q, now); reportDone <- err }()
	// The query is intentionally much longer than this head start. Assert it
	// is still in flight before and after the overlapping operations.
	time.Sleep(50 * time.Millisecond)
	select {
	case err := <-reportDone:
		t.Fatalf("report finished before overlap: %v", err)
	default:
	}

	claimDone := make(chan time.Duration, 1)
	go func() {
		started := time.Now()
		_, _, ok, err := s.ClaimNextRunnable("report-concurrent", time.Minute)
		if err != nil || !ok {
			t.Errorf("concurrent claim: %v %v", ok, err)
		}
		claimDone <- time.Since(started)
	}()
	listStart := time.Now()
	if _, _, err := s.ListStatesCursor(ctx, RunnerRequestListOptions{Limit: 25}, nil); err != nil {
		t.Fatal(err)
	}
	listLatency, claimLatency := time.Since(listStart), <-claimDone
	if err := <-reportDone; err != nil {
		t.Fatal(err)
	}
	t.Logf("during report: 25-row list=%s, queue claim=%s", listLatency, claimLatency)
	if listLatency > 500*time.Millisecond || claimLatency > 500*time.Millisecond {
		t.Fatal("report exceeded the 500ms local scheduling contention gate")
	}
	before := time.Now()
	r, err := s.ReadRunnerReport(context.Background(), q, now)
	elapsed := time.Since(before)
	if err != nil {
		t.Fatal(err)
	}
	if r.Totals.Requests != 100000 {
		t.Fatal("truncated report")
	}
	t.Logf("100000 requests, 20 repositories, 1KiB historical payload/row: report=%s, retained=%d", elapsed, len(r.Rows))
	before = time.Now()
	if _, _, err := s.ListStatesCursor(context.Background(), RunnerRequestListOptions{Limit: 25}, nil); err != nil {
		t.Fatal(err)
	}
	t.Logf("subsequent 25-row list=%s", time.Since(before))
	if err := db.Create(&runnerRequestRecord{ID: "overflow", RunnerName: "overflow", Source: "fixture", LabelsJSON: "[]", Status: StatusQueued, QueuedAt: start, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadRunnerReport(context.Background(), q, now); !errors.Is(err, ErrReportTooLarge) {
		t.Fatalf("expected explicit bound error, got %v", err)
	}
}

func TestRunnerReportSQLiteReaderIsReadOnlyAndKeepsPrimaryOpen(t *testing.T) {
	s := reportTestStore(t)
	reader, closeReader, err := s.reportDB(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer closeReader()
	if err := reader.Exec("CREATE TABLE report_must_not_write (id INTEGER)").Error; err == nil {
		t.Fatal("report connection allowed writes")
	}
	closeReader()
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TABLE primary_remains_writable (id INTEGER)").Error; err != nil {
		t.Fatal(err)
	}
}

func verifyReportGlobalSpecIdentity(t *testing.T, s *DBStore) {
	t.Helper()
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	now := reportTime("2026-11-20T10:00:00Z")
	for i, identity := range []reportSpecIdentity{
		{Source: "global", Name: "shared"},
		{Source: "global", ScopeType: "account", ScopeID: 7, Name: "shared"},
		{Source: "global", ScopeType: "installation", ScopeID: 8, Name: "shared"},
		{Source: "scoped_custom", ScopeType: "account", ScopeID: 7, Name: "shared"},
		{Source: "scoped_custom", ScopeType: "installation", ScopeID: 8, Name: "shared"},
	} {
		id := fmt.Sprintf("scope-identity-%d", i)
		row := runnerRequestRecord{ID: id, RunnerName: id, Source: "fixture", LabelsJSON: "[]", QueuedAt: now.Add(-time.Hour), UpdatedAt: now, Status: StatusRunning, ProfileName: identity.Name, ProfileSource: identity.Source, ProfileScopeType: identity.ScopeType, ProfileScopeID: identity.ScopeID, RepositoryFullName: "identity/scope"}
		if err := db.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	q := RunnerReportQuery{From: "2026-11-20", To: "2026-11-21", Timezone: "UTC", Grain: "day", Repository: "identity/scope"}
	report, err := s.ReadRunnerReport(context.Background(), q, now)
	if err != nil {
		t.Fatal(err)
	}
	groups, err := report.Groups("spec", "requests")
	if err != nil || len(groups) != 3 || groups[0].Requests != 3 || groups[0].Label != "shared · global" {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	for _, group := range groups {
		q.Spec = group.Key
		filtered, err := s.ReadRunnerReport(context.Background(), q, now)
		if err != nil {
			t.Fatal(err)
		}
		details, err := report.Requests("spec", group.Key)
		if err != nil || int64(len(details)) != group.Requests || filtered.Totals.Requests != group.Requests {
			t.Fatalf("filter/detail mismatch: %+v / %+v", group, filtered)
		}
		counts, err := s.ReadRunnerReportActive(context.Background(), q)
		if err != nil || counts[StatusRunning] != group.Requests {
			t.Fatalf("active filter: %+v %v", counts, err)
		}
	}
}

func TestRunnerReportGlobalSpecIdentity(t *testing.T) {
	verifyReportGlobalSpecIdentity(t, reportTestStore(t))
}

func TestRunnerReportTrendAcrossMidnightDST(t *testing.T) {
	for _, tc := range []struct {
		name, zone, from, to, observed string
		days, weeks, months            int
		lastDay, lastWeek, lastMonth   string
	}{
		{"Havana unfinished date", "America/Havana", "2026-03-01", "2026-03-10", "2026-03-09T12:00:00Z", 9, 3, 1, "2026-03-09", "2026-03-09", "2026-03-01"},
		{"Havana exclusive midnight", "America/Havana", "2026-03-01", "2026-03-09", "2026-03-09T04:00:00Z", 8, 2, 1, "2026-03-08", "2026-03-02", "2026-03-01"},
		{"Sao Paulo month start", "America/Sao_Paulo", "2015-10-30", "2015-11-03", "2015-11-02T12:00:00Z", 4, 2, 2, "2015-11-02", "2015-11-02", "2015-11-01"},
		{"Havana repeated midnight", "America/Havana", "2026-10-31", "2026-11-03", "2026-11-02T12:00:00Z", 3, 2, 2, "2026-11-02", "2026-11-02", "2026-11-01"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := reportTestStore(t)
			db, _ := s.dbOrEnsure()
			now := reportTime(tc.observed)
			queued := now.Add(-time.Hour)
			row := runnerRequestRecord{ID: "dst", RunnerName: "dst", Source: "fixture", LabelsJSON: "[]", QueuedAt: queued, UpdatedAt: now, Status: StatusCompleted, RunningAt: reportPtr(queued.Add(time.Minute)), CompletedAt: reportPtr(queued.Add(11 * time.Minute))}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			for _, grain := range []struct {
				name  string
				count int
				last  string
			}{{"day", tc.days, tc.lastDay}, {"week", tc.weeks, tc.lastWeek}, {"month", tc.months, tc.lastMonth}} {
				t.Run(grain.name, func(t *testing.T) {
					report, err := s.ReadRunnerReport(context.Background(), RunnerReportQuery{From: tc.from, To: tc.to, Timezone: tc.zone, Grain: grain.name}, now)
					if err != nil {
						t.Fatal(err)
					}
					if len(report.Trend) != grain.count || report.Trend[len(report.Trend)-1].Date != grain.last {
						t.Fatalf("trend dates: %+v", report.Trend)
					}
					var requests, samples int64
					var seconds float64
					previous := ""
					for _, bucket := range report.Trend {
						if bucket.Date <= previous {
							t.Fatalf("duplicate or unordered date %s", bucket.Date)
						}
						previous = bucket.Date
						requests += bucket.Requests
						samples += bucket.Runtime.Samples
						seconds += bucket.Runtime.Seconds
					}
					if report.Totals.Requests != 1 || requests != report.Totals.Requests || samples != report.Totals.Runtime.Samples || seconds != report.Totals.Runtime.Seconds {
						t.Fatalf("trend does not reconcile: requests=%d samples=%d seconds=%v totals=%+v", requests, samples, seconds, report.Totals)
					}
				})
			}
		})
	}
}

func TestRunnerReportMissingMidnightBoundaries(t *testing.T) {
	for _, tc := range []struct{ name, from, to, now, start, end, previousStart, previousEnd string }{
		{"missing start", "2026-03-08", "2026-03-09", "2026-03-09T12:00:00Z", "2026-03-08T05:00:00Z", "2026-03-09T04:00:00Z", "2026-03-07T05:00:00Z", "2026-03-08T05:00:00Z"},
		{"missing end", "2026-03-07", "2026-03-08", "2026-03-09T12:00:00Z", "2026-03-07T05:00:00Z", "2026-03-08T05:00:00Z", "2026-03-06T05:00:00Z", "2026-03-07T05:00:00Z"},
		{"missing comparison end", "2026-03-09", "2026-03-10", "2026-03-09T04:30:00Z", "2026-03-09T04:00:00Z", "2026-03-09T04:30:00Z", "2026-03-08T05:00:00Z", "2026-03-08T05:00:00Z"},
		{"repeated start", "2026-11-01", "2026-11-02", "2026-11-02T12:00:00Z", "2026-11-01T04:00:00Z", "2026-11-02T05:00:00Z", "2026-10-31T04:00:00Z", "2026-11-01T04:00:00Z"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := reportTime(tc.now)
			q := RunnerReportQuery{From: tc.from, To: tc.to, Timezone: "America/Havana", Grain: "day"}
			w, err := q.Window(now)
			if err != nil {
				t.Fatal(err)
			}
			if !w.Start.Equal(reportTime(tc.start)) || !w.End.Equal(reportTime(tc.end)) || !w.PreviousStart.Equal(reportTime(tc.previousStart)) || !w.PreviousEnd.Equal(reportTime(tc.previousEnd)) {
				t.Fatalf("unexpected boundaries: %+v", w)
			}
			s := reportTestStore(t)
			db, _ := s.dbOrEnsure()
			for id, when := range map[string]time.Time{"before": reportTime(tc.start).Add(-time.Second), "start": reportTime(tc.start), "last": reportTime(tc.end).Add(-time.Second), "end": reportTime(tc.end)} {
				if err := db.Create(&runnerRequestRecord{ID: id, RunnerName: id, Source: "fixture", LabelsJSON: "[]", QueuedAt: when, UpdatedAt: now, Status: StatusQueued}).Error; err != nil {
					t.Fatal(err)
				}
			}
			r, err := s.ReadRunnerReport(context.Background(), q, now)
			if err != nil {
				t.Fatal(err)
			}
			if r.Totals.Requests != 2 || len(r.Rows) != 2 || len(r.Trend) != 1 || r.Trend[0].Date != tc.from || r.Trend[0].Requests != 2 {
				t.Fatalf("wrong cohort: %+v", r)
			}
		})
	}
}

func TestRunnerReportWallClockResolution(t *testing.T) {
	for _, tc := range []struct{ zone, civil, want string }{
		{"UTC", "2026-03-08T00:00:00Z", "2026-03-08T00:00:00Z"},
		{"Asia/Singapore", "2026-03-08T00:00:00Z", "2026-03-07T16:00:00Z"},
		{"Asia/Kathmandu", "1986-01-01T00:00:00Z", "1985-12-31T18:30:00Z"},
		{"Pacific/Apia", "2011-12-30T00:00:00Z", "2011-12-30T10:00:00Z"},
		{"America/New_York", "2026-11-01T01:30:00Z", "2026-11-01T05:30:00Z"},
		{"America/New_York", "2026-03-08T02:30:00Z", "2026-03-08T07:00:00Z"},
	} {
		t.Run(tc.zone+tc.civil, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.zone)
			if err != nil {
				t.Fatal(err)
			}
			got, err := resolveReportWallTime(reportTime(tc.civil), loc)
			if err != nil || !got.Equal(reportTime(tc.want)) {
				t.Fatalf("resolved %v err=%v, want %s", got, err, tc.want)
			}
		})
	}
}

func TestRunnerReportMissingSpecLabel(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	for _, identity := range []reportSpecIdentity{
		{Source: "global"},
		{Source: "scoped_custom", ScopeType: "account", ScopeID: 7},
	} {
		row := (reportProjection{ProfileSource: identity.Source, ProfileScopeType: identity.ScopeType, ProfileScopeID: identity.ScopeID}).request(now)
		if row.SpecLabel != "" || row.Spec != reportSpecKey(identity) || row.knownSpec {
			t.Fatalf("missing name must stay unlabeled and retain identity: %+v", row)
		}
	}
	row := (reportProjection{ProfileName: "· standard", ProfileSource: "global"}).request(now)
	if row.SpecLabel != "· standard · global" {
		t.Fatalf("real spec names must not be mistaken for missing names: %q", row.SpecLabel)
	}
}
