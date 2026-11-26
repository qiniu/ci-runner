package state

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/qiniu/ci-runner/internal/testpg"
)

func comparePublishedView(t *testing.T, s *DBStore, got *RunnerReport) {
	t.Helper()
	want, err := readAccountSourceForTest(t, s, got.Query, got.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Query != want.Query || !reflect.DeepEqual(got.Window, want.Window) || got.Totals != want.Totals || got.Previous != want.Previous {
		t.Fatalf("cohort differs: current=%+v / %+v previous=%+v / %+v", got.Totals, want.Totals, got.Previous, want.Previous)
	}
	if !reflect.DeepEqual(got.Trend, want.Trend) || !reflect.DeepEqual(got.ChartDistributions(), want.ChartDistributions()) {
		t.Fatalf("trend or chart rankings differ for %+v", got.Query)
	}
	for _, dimension := range []string{"spec", "owner", "repository"} {
		for _, order := range []string{"requests", "runtime"} {
			gotGroups, _ := got.Groups(dimension, order)
			wantGroups, _ := want.Groups(dimension, order)
			if !reflect.DeepEqual(gotGroups, wantGroups) {
				t.Fatalf("%s groups by %s differ", dimension, order)
			}
		}
	}
	if !reflect.DeepEqual(got.SpecSummary, want.SpecSummary) {
		t.Fatalf("spec summary differs: got %+v / want %+v", got.SpecSummary, want.SpecSummary)
	}
	if !reflect.DeepEqual(got.SpecItems, want.SpecItems) {
		t.Fatalf("spec catalog, usage, results or per-spec distributions differ: got %+v / want %+v", got.SpecItems, want.SpecItems)
	}
	// Check both the retained projection and the aggregate-only detail status.
	for _, dim := range []string{"", "spec", "owner", "repository"} {
		key := ""
		if len(want.Rows) > 0 && dim != "" {
			key, _, _ = want.Rows[0].dimension(dim)
		}
		gotRows, gotErr := got.Requests(dim, key)
		wantRows, wantErr := want.Requests(dim, key)
		if gotErr != wantErr || len(gotRows) != len(wantRows) || (gotRows == nil) != (wantRows == nil) {
			t.Fatalf("request drilldown for %q differs: %v / %v", dim, gotErr, wantErr)
		}
		for i := range gotRows {
			a, b := gotRows[i], wantRows[i]
			a.QueuedAt, b.QueuedAt = a.QueuedAt.UTC(), b.QueuedAt.UTC()
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("request drilldown for %q differs: %+v / %+v", dim, a, b)
			}
		}
	}
}

func TestPublishedAccountOverviewAndSpecsPostgres(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires a dedicated PostgreSQL _test database")
	}
	opts := Options{Backend: BackendPostgres, DatabaseDSN: testpg.SchemaDSN(t), MigrateOnStart: true}
	s := NewWithOptions(opts).(*DBStore)
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	days := time.Now().UTC().Truncate(24 * time.Hour)
	q, fixtureNow := reportFixture(t, s)
	shift := days.AddDate(0, 0, -2).Sub(fixtureNow.Truncate(24 * time.Hour))
	timestamps := map[string]any{}
	for _, column := range []string{"queued_at", "creating_at", "running_at", "stopping_at", "failed_at", "completed_at", "github_job_observed_at"} {
		timestamps[column] = gorm.Expr(column+" + ? * interval '1 second'", int64(shift.Seconds()))
	}
	if err := db.Model(&runnerRequestRecord{}).Where("source = ?", "fixture").UpdateColumns(timestamps).Error; err != nil {
		t.Fatal(err)
	}
	q.From, q.To = days.AddDate(0, 0, -4).Format(time.DateOnly), days.Format(time.DateOnly)
	for _, name := range []string{"standard", "no-usage"} {
		if _, err := s.UpsertProfile(RunnerProfile{Name: name, Labels: []string{"qiniu"}, TemplateID: "test", MaxConcurrency: 1}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpsertScopedProfileIfUnchanged(ScopedRunnerProfile{
		ScopeType: "account", ScopeID: 7, Name: "standard", WorkflowLabels: []string{"qiniu", "scoped"},
		TemplateID: "test-scoped", Enabled: true,
	}, nil); err != nil {
		t.Fatal(err)
	}
	for i, at := range []time.Time{reportTime("2024-11-03T02:15:00Z"), reportTime("2024-11-04T03:15:00Z")} {
		id := fmt.Sprintf("view-dst-%d", i)
		if err := db.Create(&runnerRequestRecord{
			ID: id, RunnerName: id, QueuedAt: at, Status: StatusCompleted, RunningAt: &at, CompletedAt: &at,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// More than five exact owners and repositories, including a recorded zero
	// duration and several missing ones. Keep the original fixture's failed,
	// rejected, historical and scoped-spec records in the same current period.
	at := days.AddDate(0, 0, -2)
	for i := range 8 {
		id := fmt.Sprintf("view-rank-%d", i)
		r := runnerRequestRecord{
			ID: id, RunnerName: id, QueuedAt: at, RepositoryFullName: "team/" + id, GitHubInstallationID: int64(10 + i),
			ProfileSource: "global", ProfileName: "standard", Status: StatusCompleted,
		}
		if i%2 == 0 {
			r.CreatingAt, r.RunningAt, r.CompletedAt = &at, &at, &at
		}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	runViews := func(q RunnerReportQuery, check func(RunnerReportQuery)) {
		t.Helper()
		check(q)
		q.IncludeSpecUsage = true
		check(q)
	}
	runViews(q, func(q RunnerReportQuery) {
		at := time.Now().UTC()
		r, err := s.ReadRunnerReport(t.Context(), q, at)
		if err != nil || !r.ObservedAt.Equal(at) || len(r.Rows) == 0 {
			t.Fatalf("expected the original query before first publication: %v", err)
		}
	})
	publish := func(rebuild bool) accountReportPublication {
		t.Helper()
		if err := RefreshAccountReports(t.Context(), opts, rebuild); err != nil {
			t.Fatal(err)
		}
		var pub accountReportPublication
		if err := db.Take(&pub, 1).Error; err != nil {
			t.Fatal(err)
		}
		return pub
	}
	check := func(q RunnerReportQuery, pub accountReportPublication) {
		t.Helper()
		got, err := s.ReadRunnerReport(t.Context(), q, pub.ObservedAt.Add(time.Second))
		if err != nil || !got.ObservedAt.Equal(pub.ObservedAt) {
			t.Fatalf("expected publication for %+v: %v", q, err)
		}
		comparePublishedView(t, s, got)
	}
	first := publish(false)
	for _, change := range []func(*RunnerReportQuery){
		func(*RunnerReportQuery) {},
		func(q *RunnerReportQuery) { q.Owner = "github:10" },
		func(q *RunnerReportQuery) { q.Repository = "team/one" },
		func(q *RunnerReportQuery) { q.Repository = "team/ONE" },
		func(q *RunnerReportQuery) { q.MissingRepository = "1" },
		func(q *RunnerReportQuery) {
			q.Spec = reportSpecKey(reportSpecIdentity{Source: "global", Name: "standard"})
		},
		func(q *RunnerReportQuery) {
			q.Spec = reportSpecKey(reportSpecIdentity{Source: "scoped_custom", ScopeType: "account", ScopeID: 7, Name: "standard"})
		},
		func(q *RunnerReportQuery) { q.Timezone, q.Grain = "America/St_Johns", "week" },
		func(q *RunnerReportQuery) { q.Timezone, q.Grain = "Asia/Shanghai", "month" },
	} {
		filtered := q
		change(&filtered)
		runViews(filtered, func(q RunnerReportQuery) { check(q, first) })
	}

	dst := RunnerReportQuery{From: "2024-11-02", To: "2024-11-05", Timezone: "America/St_Johns", Grain: "day"}
	runViews(dst, func(q RunnerReportQuery) { check(q, first) })

	// Force the aggregate-only branch for both the cached default window and a
	// custom non-UTC window. One set of rows is in both comparison periods.
	previous := days.AddDate(0, 0, -32)
	if err := db.Exec(`INSERT INTO runner_requests(id, runner_name, source, labels_json, status, profile_source, profile_name, queued_at, updated_at)
		SELECT 'view-volume-'||i, 'view-volume-'||i, 'fixture', '[]', 'queued', 'global', 'standard', ?, ? FROM generate_series(1, ?::int) i`,
		previous, previous, ReportMaxRows+1).Error; err != nil {
		t.Fatal(err)
	}
	large := publish(true)
	query := defaultAccountQuery(large.ObservedAt)
	query.IncludeAccountUsage = false
	runViews(query, func(q RunnerReportQuery) { check(q, large) })
	custom := query
	custom.To = days.Format(time.DateOnly)
	custom.Timezone, custom.Grain = "Asia/Shanghai", "week"
	runViews(custom, func(q RunnerReportQuery) { check(q, large) })

	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("ALTER TABLE runner_requests RENAME TO hidden_view_source").Error; err != nil {
			return err
		}
		for _, q := range []RunnerReportQuery{query, custom, q} {
			for _, specs := range []bool{false, true} {
				q.IncludeSpecUsage = specs
				got, err := readPublishedUsageReport(tx, q, large.ObservedAt)
				if err != nil {
					return err
				}
				if got == nil || got.Totals.Requests == 0 || (specs && len(got.SpecItems) == 0) {
					return fmt.Errorf("publication lost without the source table: %+v", q)
				}
			}
		}
		return gorm.ErrInvalidTransaction // Restore the source table.
	})
	if !errors.Is(err, gorm.ErrInvalidTransaction) {
		t.Fatal(err)
	}
}

// Two repositories tie at the fifth place at PostgreSQL's stored precision.
// Use the comparison period to enter the aggregate-only path without
// letting the volume fixture change the current period's ranking.
func TestPublishedAccountRuntimeRankingPostgres(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires a dedicated PostgreSQL _test database")
	}
	opts := Options{Backend: BackendPostgres, DatabaseDSN: testpg.SchemaDSN(t), MigrateOnStart: true}
	s := NewWithOptions(opts).(*DBStore)
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)

	at := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -2)
	for i, fixture := range []struct {
		repo    string
		runtime time.Duration
	}{
		{"team/alpha", 1003691 * time.Microsecond},
		{"team/alpha", 0},
		{"team/beta", time.Second},
		{"team/beta", 3691 * time.Microsecond},
		{"team/one", 3 * time.Second},
		{"team/two", 3 * time.Second},
		{"team/three", 3 * time.Second},
		{"team/four", 3 * time.Second},
	} {
		id := fmt.Sprintf("runtime-rank-%d", i)
		end := at.Add(fixture.runtime)
		if err := db.Create(&runnerRequestRecord{
			ID: id, RunnerName: id, QueuedAt: at, RunningAt: &at, CompletedAt: &end,
			Status: StatusCompleted, RepositoryFullName: fixture.repo, ProfileSource: "global", ProfileName: "standard",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	prev := at.AddDate(0, 0, -30)
	if err := db.Exec(`INSERT INTO runner_requests(id, runner_name, source, labels_json, status, queued_at, updated_at)
		SELECT 'runtime-volume-'||i, 'runtime-volume-'||i, 'fixture', '[]', 'queued', ?, ? FROM generate_series(1, ?::int) i`,
		prev, prev, ReportMaxRows+1).Error; err != nil {
		t.Fatal(err)
	}

	check := func(t *testing.T) {
		t.Helper()
		var pub accountReportPublication
		if err := db.Take(&pub, 1).Error; err != nil {
			t.Fatal(err)
		}
		if pub.Definition != 2 {
			t.Fatalf("account batch definition changed: %d", pub.Definition)
		}
		// UTC reads the cached dashboard and completed-day aggregates. The
		// non-UTC case forces both views to aggregate individual facts.
		for _, zone := range []string{"UTC", "Asia/Shanghai"} {
			for _, specs := range []bool{false, true} {
				q := defaultAccountQuery(pub.ObservedAt)
				q.IncludeAccountUsage, q.IncludeSpecUsage = false, specs
				q.Timezone = zone
				got, err := s.ReadRunnerReport(t.Context(), q, pub.ObservedAt)
				if err != nil || !got.ObservedAt.Equal(pub.ObservedAt) || got.Rows != nil {
					t.Fatalf("expected published aggregate report for %s (specs=%t): %v", zone, specs, err)
				}
				rank := got.ChartDistributions()["repository"].Runtime
				if len(rank) != 5 || rank[4].Key != "team/alpha" || rank[4].Runtime.Seconds != 1.003691 {
					t.Fatalf("unexpected fifth repository for %s (specs=%t): %+v", zone, specs, rank)
				}
				if specs {
					perSpec := got.SpecItems[0].RepositoryMix.Runtime
					if len(perSpec) != 6 || perSpec[4].Key != "team/alpha" || !perSpec[5].Other || perSpec[5].Runtime.Seconds != 1.003691 {
						t.Fatalf("unexpected spec ranking or Other: %+v", perSpec)
					}
				}
				comparePublishedView(t, s, got)
			}
		}
	}

	legacy := os.Getenv("RUNNERD_TEST_ACCOUNT_V2_BINARY")
	t.Run("released_worker", func(t *testing.T) {
		if legacy == "" {
			t.Skip("requires the released v2 runnerd")
		}
		releasedAccountRefresh(t, opts, legacy)()
		check(t)
	})
	t.Run("current_worker", func(t *testing.T) {
		if err := RefreshAccountReports(t.Context(), opts, false); err != nil {
			t.Fatal(err)
		}
		check(t)
	})
}
