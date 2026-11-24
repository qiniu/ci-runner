package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	specQuery := q
	specQuery.IncludeSpecUsage = true
	specQuery.Spec = groups[0].Key
	dashboard, err := s.ReadRunnerReport(t.Context(), specQuery, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(dashboard.SpecItems) != 1 || dashboard.SpecItems[0].Key != specQuery.Spec || dashboard.SpecItems[0].Runtime != groups[0].Runtime || dashboard.SpecItems[0].RepositoryMix == nil {
		t.Fatal("filtered specification dashboard does not reconcile")
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
			verifySharedSpecSnapshotBounds(t, s)
			verifyReportAccountUsage(t, s)
			verifyEfficiency(t, s)
			verifyQueueAgeAfterRetry(t, s)
			verifyReportGlobalSpecIdentity(t, s)
			if backend.name == BackendPostgres {
				verifyLargePostgresReport(t, s)
				t.Run("trend_timezone", func(t *testing.T) { verifyPostgresTrendTimezone(t, s) })
				if os.Getenv("RUNNERD_REPORT_SCALE_TEST") == "1" {
					t.Run("thirty_day_specs", func(t *testing.T) { verifyPostgresSpecReportScale(t, s) })
				}
			}
		})
	}
}

// Exercise a 30-day report and its comparison period at production-like volume
// with recurring repository, owner and spec combinations.
func verifyPostgresSpecReportScale(t *testing.T, s *DBStore) {
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO runner_requests
		(id, runner_name, source, labels_json, status, profile_source, profile_name, github_installation_id,
		 repository_full_name, queued_at, creating_at, running_at, completed_at, updated_at)
		SELECT id, id, 'fixture', '[]', 'completed', 'global', 'spec-' || (i % 10), (i % 101) % 11,
			'repo-' || (i % 101), at, at, at + interval '2 seconds', at + interval '58 seconds', at
		FROM (SELECT i, 'scale-' || i || '-' || copy AS id,
			timestamptz '2025-09-25 UTC' + i * interval '10 seconds' AS at
			FROM generate_series(1, 510000) i CROSS JOIN generate_series(1, 2) copy) fixture`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ANALYZE runner_requests").Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	start := time.Now()
	r, err := s.ReadRunnerReport(ctx, RunnerReportQuery{From: "2025-10-25", To: "2025-11-24", Timezone: "UTC", Grain: "day", IncludeSpecUsage: true}, reportTime("2025-11-23T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	// Each ten-second tick has two rows. The fixture ends before the current
	// period does, and the previous period ends at the matching UTC time of day.
	if r.Totals.Requests != 501602 || r.Previous.Requests != 509758 || r.SpecSummary.Requests != r.Totals.Requests || r.Totals.ActiveRepositories != 101 || r.Totals.Runtime.Seconds != 56*float64(r.Totals.Requests) {
		t.Fatalf("30-day aggregate: current %d, prior %d, repositories %d, runtime %v", r.Totals.Requests, r.Previous.Requests, r.Totals.ActiveRepositories, r.Totals.Runtime)
	}
	accountStart := time.Now()
	accountCtx, accountCancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer accountCancel()
	account, err := s.ReadRunnerReport(accountCtx, RunnerReportQuery{From: r.Query.From, To: r.Query.To, Timezone: "UTC", Grain: "day", IncludeAccountUsage: true}, r.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	if account.Totals != r.Totals || account.Previous != r.Previous || account.Outcomes["unknown"] != account.Totals.Requests {
		t.Fatal("30-day account usage does not reconcile with specs")
	}
	t.Logf("30-day account dashboard: %d current + %d prior in %s", account.Totals.Requests, account.Previous.Requests, time.Since(accountStart))
	t.Logf("30-day Specs dashboard: %d current + %d prior requests in %s", r.Totals.Requests, r.Previous.Requests, accountStart.Sub(start))
	efficiencyCtx, cancelEfficiency := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelEfficiency()
	efficiencyStart := time.Now()
	effQuery := r.Query
	effQuery.IncludeSpecUsage = false
	effQuery.IncludeEfficiency = true
	efficiency, err := s.ReadRunnerReport(efficiencyCtx, effQuery, r.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	if efficiency.Efficiency == nil || efficiency.Totals != r.Totals || efficiency.Rows != nil {
		t.Fatal("invalid large efficiency report")
	}
	t.Logf("30-day efficiency report: %d current + %d previous in %s", efficiency.Totals.Requests, efficiency.Previous.Requests, time.Since(efficiencyStart))
}

// First force the aggregate path with previous-period rows only. Every current
// metric, trend bucket and group must match the existing row-based snapshot.
func verifyLargePostgresReport(t *testing.T, s *DBStore) {
	t.Helper()
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	now := reportTime("2026-11-20T10:00:00Z")
	q := RunnerReportQuery{From: "2026-10-22", To: "2026-11-21", Timezone: "Asia/Shanghai", Grain: "day"}
	baseline, err := s.ReadRunnerReport(t.Context(), q, now)
	if err != nil {
		t.Fatal(err)
	}
	accounts := q
	accounts.IncludeAccountUsage = true
	accounts.Grain = "month"
	smallAccounts, err := s.ReadRunnerReport(t.Context(), accounts, now)
	if err != nil {
		t.Fatal(err)
	}
	specQuery := q
	specQuery.IncludeSpecUsage = true
	smallSpecs, err := s.ReadRunnerReport(t.Context(), specQuery, now)
	if err != nil {
		t.Fatal(err)
	}
	effQuery := q
	effQuery.IncludeEfficiency = true
	smallEff, err := s.ReadRunnerReport(t.Context(), effQuery, now)
	if err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO runner_requests (id, source, runner_name, labels_json, status, repository_full_name, queued_at, updated_at)
		SELECT 'volume-' || i, 'fixture', 'volume-' || i, '[]', 'queued', 'volume/repo', ?, ? FROM generate_series(1, ?) i`
	prior := now.AddDate(0, 0, -40)
	if err := db.Exec(insert, prior, prior, ReportMaxRows+1).Error; err != nil {
		t.Fatal(err)
	}
	read := func() *RunnerReport {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		start := time.Now()
		r, err := s.ReadRunnerReport(ctx, q, now)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("PostgreSQL report: %d current + %d previous, %d retained entries in %s", r.Totals.Requests, r.Previous.Requests, r.RetainedEntries(), time.Since(start))
		return r
	}
	largeEff, err := s.ReadRunnerReport(t.Context(), effQuery, now)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(smallEff.Efficiency, largeEff.Efficiency) || largeEff.Rows != nil {
		t.Fatalf("large efficiency report differs: %+v %+v", smallEff.Efficiency, largeEff.Efficiency)
	}
	largeAccounts, err := s.ReadRunnerReport(t.Context(), accounts, now)
	if err != nil {
		t.Fatal(err)
	}
	for _, dim := range []string{"owner", "repository", "spec"} {
		small, _ := smallAccounts.Groups(dim, "requests")
		large, _ := largeAccounts.Groups(dim, "requests")
		if !reflect.DeepEqual(small, large) {
			t.Fatalf("account %s small vs aggregate: %+v vs %+v", dim, small, large)
		}
	}
	if !reflect.DeepEqual(smallAccounts.Outcomes, largeAccounts.Outcomes) || !reflect.DeepEqual(smallAccounts.Trend, largeAccounts.Trend) {
		t.Fatal("aggregate dashboard differs from detail cohort")
	}
	bigSpecs, err := s.ReadRunnerReport(t.Context(), specQuery, now)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range smallSpecs.SpecItems {
		// Exact percentiles are only available for retained request rows.
		item.P95RuntimeSeconds = nil
		item.P95CreationSeconds = nil
		if !reflect.DeepEqual(item, bigSpecs.SpecItems[i]) {
			t.Fatalf("spec dashboard rollup differs for %s: row=%+v aggregate=%+v", item.Key, item, bigSpecs.SpecItems[i])
		}
	}
	large := read()
	if !reflect.DeepEqual(large.ChartDistributions(), baseline.ChartDistributions()) {
		t.Fatal("large PostgreSQL overview rankings differ from the row snapshot")
	}
	wantPrevious := baseline.Previous
	wantPrevious.Requests += ReportMaxRows + 1
	wantPrevious.ActiveRepositories++
	if large.Previous != wantPrevious || large.Totals != baseline.Totals || !reflect.DeepEqual(large.Trend, baseline.Trend) {
		t.Fatalf("aggregate totals/trend diverged: got %+v / %+v, want %+v / %+v", large.Totals, large.Previous, baseline.Totals, wantPrevious)
	}
	for _, dim := range []string{"spec", "repository", "owner"} {
		want, _ := baseline.Groups(dim, "requests")
		got, err := large.Groups(dim, "requests")
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: got %+v, want %+v: %v", dim, got, want, err)
		}
	}
	if _, err := large.Requests("", ""); !errors.Is(err, ErrReportTooLarge) {
		t.Fatalf("expected explicit detail overflow: %v", err)
	}
	// Put all 100001 generated requests in the current cohort with a narrow
	// repository filter; no request rows may be truncated or retained.
	at := now.Add(-48 * time.Hour)
	if err := db.Exec("UPDATE runner_requests SET queued_at=?, status='completed', running_at=?, completed_at=? WHERE repository_full_name='volume/repo'", at, at, at.Add(90*time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	q.Repository = "volume/repo"
	large = read()
	groups, err := large.Groups("repository", "runtime")
	if err != nil || len(groups) != 1 || groups[0].Key != q.Repository || groups[0].Requests != ReportMaxRows+1 ||
		large.Totals.Runtime != (ReportDuration{Seconds: 90 * (ReportMaxRows + 1), Samples: ReportMaxRows + 1, Eligible: ReportMaxRows + 1}) ||
		large.Totals.Requests != ReportMaxRows+1 || large.Previous.Requests != 0 || len(large.Rows) != 0 || large.RetainedEntries() != 3 {
		t.Fatalf("filtered large report: %+v, groups=%+v: %v", large.Totals, groups, err)
	}
}

// Only the previous cohort grows across the threshold, so both paths must
// return the same current trend. CET observes summer time in Go's IANA data.
func verifyPostgresTrendTimezone(t *testing.T, s *DBStore) {
	t.Helper()
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	q := RunnerReportQuery{From: "2026-03-28", To: "2026-06-02", Timezone: "CET", Grain: "day", Repository: "timezone/repo"}
	now := reportTime("2026-06-03T00:00:00Z")
	insert := func(prefix, queued string, n int) {
		t.Helper()
		at := reportTime(queued)
		err := db.Exec(`INSERT INTO runner_requests (id, source, runner_name, labels_json, status, repository_full_name, queued_at, updated_at)
			SELECT ? || i, 'test', ? || i, '[]', 'queued', ?, ?, ? FROM generate_series(1, ?) i`,
			prefix, prefix, q.Repository, at, at, n).Error
		if err != nil {
			t.Fatal(err)
		}
	}
	insert("cet-winter-", "2026-03-28T22:30:00Z", 1) // 23:30 on March 28
	insert("cet-summer-", "2026-05-31T22:30:00Z", 2) // 00:30 on June 1
	const previous = "2026-03-01T12:00:00Z"
	insert("cet-previous-", previous, ReportMaxRows-3)
	for _, size := range []int{ReportMaxRows, ReportMaxRows + 1} {
		if size > ReportMaxRows {
			insert("cet-extra-", previous, 1)
		}
		r, err := s.ReadRunnerReport(t.Context(), q, now)
		if err != nil {
			t.Fatal(err)
		}
		if r.Totals.Requests != 3 || r.Previous.Requests != int64(size-3) || (r.Rows == nil) != (size > ReportMaxRows) {
			t.Fatalf("cohort size %d: current=%d previous=%d aggregate=%t", size, r.Totals.Requests, r.Previous.Requests, r.Rows == nil)
		}
		got := map[string]int64{}
		for _, b := range r.Trend {
			if b.Requests > 0 {
				got[b.Date] = b.Requests
			}
		}
		want := map[string]int64{"2026-03-28": 1, "2026-06-01": 2}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cohort size %d: got trend %v, want %v", size, got, want)
		}
	}
}

func TestRunnerReportChartRankings(t *testing.T) {
	rows := &RunnerReport{}
	for i := range 7 {
		key := fmt.Sprint(i)
		duration := float64(i * i)
		for range 7 - i {
			rows.Rows = append(rows.Rows, ReportRequest{
				Repository: key, Owner: key, Spec: key, RuntimeSeconds: &duration, terminal: true, started: true,
			})
		}
	}
	aggregate := &RunnerReport{GroupTotals: make(map[string][]ReportGroup)}
	for _, dim := range []string{"spec", "owner", "repository"} {
		aggregate.GroupTotals[dim] = rows.groupRows(dim)
	}
	for _, report := range []*RunnerReport{rows, aggregate} {
		rankings := report.ChartDistributions()
		for _, dim := range []string{"spec", "owner", "repository"} {
			got := rankings[dim]
			requests, _ := rows.Groups(dim, "requests")
			runtime, _ := rows.Groups(dim, "runtime")
			if got.Total != 7 || !reflect.DeepEqual(got.Requests, requests[:5]) || !reflect.DeepEqual(got.Runtime, runtime[:5]) {
				t.Fatalf("%s rankings diverged from paginated groups", dim)
			}
		}
	}
}

func TestRunnerReportSpecUsagesMergesCatalogAndCalculatesSamples(t *testing.T) {
	duration := func(value float64) *float64 { return &value }
	customKey := reportSpecKey(reportSpecIdentity{Source: "scoped_custom", ScopeType: "account", ScopeID: 7, Name: "shared"})
	report := &RunnerReport{Rows: []ReportRequest{
		{Spec: reportSpecKey(reportSpecIdentity{Source: "global", Name: "standard"}), JobResult: "success", RuntimeSeconds: duration(10), creation: duration(4)},
		{Spec: reportSpecKey(reportSpecIdentity{Source: "global", Name: "standard"}), JobResult: "failure", RuntimeSeconds: duration(20), creation: duration(8)},
		{Spec: customKey, JobResult: "cancelled", RuntimeSeconds: duration(30), creation: duration(12)},
	}}
	for i := range report.Rows {
		report.Rows[i].started, report.Rows[i].terminal = true, true
		report.Rows[i].SpecLabel = report.Rows[i].Spec
		report.Rows[i].Repository = fmt.Sprintf("team/repo-%d", i)
	}
	catalog := []ReportSpecUsage{
		{Name: "standard", Source: "global", Enabled: true, Published: true, TemplateSource: TemplateSourcePublic},
		{Name: "unused", Source: "global"},
	}
	usages := report.specUsages(catalog)
	if len(usages) != 3 {
		t.Fatalf("usage count=%d, want catalog and historical entries", len(usages))
	}
	var standard, unused, custom *ReportSpecUsage
	for i := range usages {
		switch usages[i].Name {
		case "standard":
			standard = &usages[i]
		case "unused":
			unused = &usages[i]
		case "shared":
			custom = &usages[i]
		}
	}
	if standard == nil || standard.Requests != 2 || standard.JobResults["success"] != 1 || standard.JobResults["failure"] != 1 || standard.P95RuntimeSeconds == nil || *standard.P95RuntimeSeconds != 20 {
		t.Fatalf("standard usage=%+v", standard)
	}
	if unused == nil || !unused.Current || unused.Requests != 0 {
		t.Fatalf("unused usage=%+v", unused)
	}
	if custom == nil || custom.Current || custom.ScopeID != 7 || custom.Requests != 1 {
		t.Fatalf("custom usage=%+v", custom)
	}
}

func TestRunnerReportSpecDashboardCatalogAndShares(t *testing.T) {
	s := reportTestStore(t)
	q, now := reportFixture(t, s)
	db, _ := s.dbOrEnsure()
	if err := db.Create(&runnerProfileRecord{Name: "standard", LabelsJSON: "[]", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]scopedRunnerProfileRecord{{ScopeType: "account", ScopeID: 7, Name: "standard", LabelKey: "a", WorkflowLabelsJSON: "[]", Enabled: true}, {ScopeType: "account", ScopeID: 8, Name: "unused", LabelKey: "b", WorkflowLabelsJSON: "[]"}}).Error; err != nil {
		t.Fatal(err)
	}
	q.IncludeSpecUsage = true
	r, err := s.ReadRunnerReport(t.Context(), q, now)
	if err != nil {
		t.Fatal(err)
	}
	var current, used int
	var requests int64
	for _, item := range r.SpecItems {
		if item.Current {
			current++
			if item.Requests > 0 {
				used++
			}
		}
		requests += item.Requests
		if item.Requests == 0 {
			continue
		}
		for _, groups := range [][]ReportSpecSegment{item.RepositoryMix.Requests, item.RepositoryMix.Runtime, item.Owners.Requests, item.Owners.Runtime, item.Trend} {
			var count, samples int64
			var seconds float64
			for _, g := range groups {
				count += g.Requests
				samples += g.Runtime.Samples
				seconds += g.Runtime.Seconds
			}
			if count != item.Requests || samples != item.Runtime.Samples || seconds != item.Runtime.Seconds {
				t.Fatalf("group mismatch for %s: %+v", item.Key, groups)
			}
		}
	}
	if current != 3 || used != 2 || requests != r.Totals.Requests {
		t.Fatalf("catalog=%d used=%d requests=%d", current, used, requests)
	}
	summary := r.SpecSummary
	if summary == nil || summary.Requests != requests || summary.Repositories != r.Totals.ActiveRepositories || summary.Runtime != r.Totals.Runtime || summary.P95RuntimeSeconds == nil {
		t.Fatalf("incorrect full-cohort spec summary: %+v", summary)
	}
	for _, ranking := range [][]ReportSpecSegment{summary.RepositoryMix.Requests, summary.Owners.Runtime} {
		var count int64
		for _, group := range ranking {
			count += group.Requests
		}
		if count != requests {
			t.Fatalf("summary ranking lost requests: %+v", ranking)
		}
	}
	q.Spec = reportSpecKey(reportSpecIdentity{Source: "scoped_custom", ScopeType: "account", ScopeID: 8, Name: "unused"})
	r, err = s.ReadRunnerReport(t.Context(), q, now)
	if err != nil || len(r.SpecItems) != 1 || r.SpecItems[0].Requests != 0 || !r.SpecItems[0].Current {
		t.Fatalf("filtered zero-use entry: %+v, %v", r, err)
	}
}

func TestRunnerReportSpecRepositoryTopAndOther(t *testing.T) {
	var repos []ReportSpecSegment
	for i := range 8 {
		repos = append(repos, ReportSpecSegment{Key: fmt.Sprint(i), Requests: int64(8 - i), Runtime: ReportDuration{Seconds: float64(i), Samples: 1}})
	}
	mix := rankSpecGroups(repos)
	if mix.Requests[0].Key != "0" || mix.Runtime[0].Key != "7" || !mix.Requests[5].Other || mix.Requests[5].Requests != 6 || !mix.Runtime[5].Other || mix.Runtime[5].Runtime.Seconds != 3 || mix.Runtime[5].Runtime.Samples != 3 {
		t.Fatalf("wrong rank or remainder: %+v", mix)
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
	q := RunnerReportQuery{IncludeAccountUsage: true, From: "2026-11-19", To: "2026-11-21", Timezone: "UTC", Grain: "day"}
	// Two queued requests outside the cohort let us measure real queue claims.
	for _, id := range []string{"baseline-claim", "concurrent-claim", "efficiency-claim"} {
		if err := db.Create(&runnerRequestRecord{ID: id, RunnerName: id, Source: "fixture", LabelsJSON: "[]", Status: StatusQueued, QueuedAt: start.AddDate(0, 0, -100), UpdatedAt: now}).Error; err != nil {
			t.Fatal(err)
		}
	}
	baselineStart := time.Now()
	if _, _, ok, err := s.ClaimNextRunnable("report-baseline", time.Minute); err != nil || !ok {
		t.Fatalf("baseline claim: %v %v", ok, err)
	}
	t.Logf("baseline queue claim=%s", time.Since(baselineStart))
	for _, name := range []string{"accounts", "efficiency"} {
		t.Run(name, func(t *testing.T) {
			query := q
			query.IncludeAccountUsage = name == "accounts"
			query.IncludeEfficiency = name == "efficiency"
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			reportDone := make(chan error, 1)
			reportStart := time.Now()
			go func() { _, err := s.ReadRunnerReport(ctx, query, now); reportDone <- err }()
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
			t.Logf("report=%s; during report: 25-row list=%s, queue claim=%s", time.Since(reportStart), listLatency, claimLatency)
			if listLatency > 500*time.Millisecond || claimLatency > 500*time.Millisecond {
				t.Fatal("report exceeded the 500ms local scheduling contention gate")
			}
		})
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
	t.Logf("100000 requests, 20 repositories, 1KiB historical payload/row: report=%s, retained entries=%d", elapsed, r.RetainedEntries())
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
		{Source: "global", Name: "Shared"},
		{Source: "GLOBAL", Name: "shared"},
		{Source: "global", ScopeType: "account", ScopeID: 7, Name: "shared"},
		{Source: "global", ScopeType: "installation", ScopeID: 8, Name: "shared"},
		{Source: "scoped_custom", ScopeType: "account", ScopeID: 7, Name: "shared"},
		{Source: "scoped_custom", ScopeType: "Account", ScopeID: 7, Name: "shared"},
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
	if err != nil || len(groups) != 6 || groups[0].Requests != 3 || groups[0].Label != "shared · global" {
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

	// The case variant belongs to a separate repository group even under the
	// default MySQL collation. Its filter must select exactly its captured row.
	variant := runnerRequestRecord{ID: "repo-case", RunnerName: "repo-case", Source: "fixture", LabelsJSON: "[]", QueuedAt: now.Add(-time.Hour), UpdatedAt: now, Status: StatusRunning, RepositoryFullName: "Identity/Scope"}
	if err := db.Create(&variant).Error; err != nil {
		t.Fatal(err)
	}
	q.Repository, q.Spec = variant.RepositoryFullName, ""
	filtered, err := s.ReadRunnerReport(context.Background(), q, now)
	if err != nil || len(filtered.Rows) != 1 || filtered.Rows[0].ID != variant.ID {
		t.Fatalf("case-sensitive repository filter: %+v %v", filtered, err)
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

func verifySharedSpecSnapshotBounds(t *testing.T, s *DBStore) {
	t.Helper()
	expires := time.Now().UTC().Add(time.Minute)
	save := func(id int, weight int) {
		t.Helper()
		key := fmt.Sprint(id)
		if err := s.SaveReportSnapshot(t.Context(), key, expires.Add(time.Duration(id)*time.Second), weight, []byte(key)); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i <= ReportSnapshotLimit; i++ {
		save(i, 1)
	}
	get := func(id int, now time.Time, want bool) {
		t.Helper()
		key := fmt.Sprint(id)
		b, err := s.LoadReportSnapshot(t.Context(), key, now)
		if want && (err != nil || string(b) != key) || !want && !errors.Is(err, ErrNotFound) {
			t.Fatalf("snapshot %s: body %q err %v, want present %t", key, b, err, want)
		}
	}
	get(0, time.Now(), false)
	get(1, time.Now(), true)
	save(99, ReportSnapshotEntries)
	get(ReportSnapshotLimit, time.Now(), false)
	get(99, time.Now(), true)
	get(99, expires.Add(100*time.Second), false)

	// Independent publishers must not each claim the same remaining capacity.
	const writers = 12
	done := make(chan error, writers)
	for i := range writers {
		go func() {
			key := fmt.Sprintf("writer-%d", i)
			done <- s.SaveReportSnapshot(t.Context(), key, expires, ReportSnapshotEntries/2, []byte(key))
		}()
	}
	for range writers {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Model(&reportSpecSnapshotRecord{}).Where("id <> ?", reportSnapshotWriter).Count(&count).Error; err != nil || count != 2 {
		t.Fatalf("concurrent publications retained %d snapshots, want 2: %v", count, err)
	}
}

func TestRunnerReportSharedSpecSnapshotBounds(t *testing.T) {
	verifySharedSpecSnapshotBounds(t, reportTestStore(t))
}

func verifyReportAccountUsage(t *testing.T, s *DBStore) {
	t.Helper()
	now := reportTime("2026-11-21T10:00:00Z")
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []int64{42, 43} {
		if err := db.Create(&githubInstallationRecord{AccountID: account, InstallationID: 1234, GitHubAccountID: 456, AccountLogin: "fork"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&githubInstallationOwnerRecord{InstallationID: 1234, GitHubAccountID: 456, AccountLogin: "fork"}).Error; err != nil {
		t.Fatal(err)
	}
	instants := []string{"2026-11-20T15:59:00Z", "2026-11-20T16:01:00Z", "2026-11-20T17:00:00Z", "2026-11-20T18:00:00Z"}
	for i, name := range []string{"fork/old-name", "fork/new-name", "fork/new-name", ""} {
		id := fmt.Sprintf("activity-%d", i)
		at := reportTime(instants[i])
		r := runnerRequestRecord{ID: id, Source: "fixture", RunnerName: id, LabelsJSON: "[]", Status: StatusCompleted, QueuedAt: at, UpdatedAt: at, GitHubInstallationID: 1234, RepositoryFullName: name, ProfileSource: "scoped_custom", ProfileScopeType: "account", ProfileScopeID: 43, ProfileName: "custom", RunningAt: &at, CompletedAt: &at}
		if i == 3 {
			r.GitHubInstallationID = 9876
		}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	q := RunnerReportQuery{IncludeAccountUsage: true, From: "2026-11-20", To: "2026-11-22", Timezone: "Asia/Shanghai", Grain: "month", Owner: "github:456"}
	report, err := s.ReadRunnerReport(t.Context(), q, now)
	if err != nil {
		t.Fatal(err)
	}
	groups, _ := report.Groups("owner", "requests")
	if len(groups) != 1 {
		t.Fatalf("owner groups: %v", groups)
	}
	g := groups[0]
	if g.Requests != 3 || g.ActiveRepositories != 2 || g.UsedSpecs != 1 || g.ActiveDays != 2 || !g.LastQueuedAt.Equal(reportTime(instants[2])) || g.Outcomes["unknown"] != 3 || g.Runtime.Samples != 3 || g.Runtime.Seconds != 0 || g.JobResults != 0 {
		t.Fatalf("owner usage %+v activity %+v", g, g.ReportActivity)
	}
	q.Owner = "installation:9876"
	q.MissingRepository = "1"
	report, err = s.ReadRunnerReport(t.Context(), q, now)
	if err != nil {
		t.Fatal(err)
	}
	groups, _ = report.Groups("repository", "requests")
	if len(groups) != 1 || groups[0].Key != "" || report.Totals.Requests != 1 || report.Rows[0].ID != "activity-3" || report.Rows[0].Owner != q.Owner {
		t.Fatal("unknown repository or Installation lost in drilldown")
	}
}

func TestRunnerReportAccountUsage(t *testing.T) { verifyReportAccountUsage(t, reportTestStore(t)) }
