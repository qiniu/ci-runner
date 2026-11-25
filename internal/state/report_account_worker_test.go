package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/qiniu/ci-runner/internal/testpg"
)

func TestPublishedAccountReportsPostgres(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL _test database")
	}
	opts := Options{Backend: BackendPostgres, DatabaseDSN: testpg.SchemaDSN(t), MigrateOnStart: true}
	live := NewWithOptions(opts).(*DBStore)
	db, err := live.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	q, fixtureNow := reportFixture(t, live)
	// Keep the fixture entirely inside a closed period regardless of the host date.
	endDay := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -2)
	shift := endDay.Sub(fixtureNow.Truncate(24 * time.Hour))
	times := map[string]any{}
	for _, col := range []string{"queued_at", "creating_at", "running_at", "stopping_at", "failed_at", "completed_at", "github_job_observed_at"} {
		times[col] = gorm.Expr(col+" + ? * interval '1 second'", int64(shift.Seconds()))
	}
	if err := db.Model(&runnerRequestRecord{}).Where("source=?", "fixture").UpdateColumns(times).Error; err != nil {
		t.Fatal(err)
	}
	q.From = endDay.AddDate(0, 0, -1).Format(time.DateOnly)
	q.To = endDay.AddDate(0, 0, 1).Format(time.DateOnly)
	q.IncludeAccountUsage = true
	opts.PublishedAccountReports = true
	opts.MigrateOnStart = false
	published := NewWithOptions(opts).(*DBStore)
	pdb, err := published.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, pdb)
	now := time.Now().UTC()
	if _, err := published.ReadRunnerReport(t.Context(), q, now); !errors.Is(err, ErrAccountReportUnavailable) {
		t.Fatalf("unpublished report: %v", err)
	}
	refresh := func(rebuild bool) {
		t.Helper()
		if err := RefreshAccountReports(t.Context(), opts, rebuild); err != nil {
			t.Fatal(err)
		}
	}
	check := func(q RunnerReportQuery) *RunnerReport {
		t.Helper()
		got, err := published.ReadRunnerReport(t.Context(), q, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		want, err := live.ReadRunnerReport(t.Context(), q, got.ObservedAt)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(got.Rows)
		b, _ := json.Marshal(want.Rows)
		if string(a) != string(b) || !reflect.DeepEqual(got.Totals, want.Totals) || !reflect.DeepEqual(got.Trend, want.Trend) || !reflect.DeepEqual(got.Previous, want.Previous) || !reflect.DeepEqual(got.Outcomes, want.Outcomes) {
			t.Fatalf("published report differs: totals %+v / %+v, previous %+v / %+v; rows %s / %s", got.Totals, want.Totals, got.Previous, want.Previous, a, b)
		}
		for _, dim := range []string{"owner", "repository", "spec"} {
			a, _ := got.Groups(dim, "requests")
			b, _ := want.Groups(dim, "requests")
			if !reflect.DeepEqual(a, b) {
				t.Fatalf("%s groups differ: %+v / %+v", dim, a, b)
			}
		}
		return got
	}
	refresh(false)
	check(q)
	for _, zone := range []string{"Asia/Shanghai", "America/St_Johns"} {
		f := q
		f.Timezone = zone
		f.Grain = "week"
		check(f)
	}
	f := q
	f.Owner = "github:10"
	f.Repository = "team/one"
	check(f)
	refresh(false)
	// Both slots are warm. A lease-only source update must leave the stored
	// contribution untouched, even though PostgreSQL wrote a new source tuple.
	var nextSlot int
	if err := db.Raw("SELECT 1-slot FROM report_account_publications WHERE id=1").Scan(&nextSlot).Error; err != nil {
		t.Fatal(err)
	}
	factVersion := func() string {
		t.Helper()
		var xid string
		if err := db.Raw("SELECT xmin::text FROM report_account_facts WHERE slot=? AND id='success'", nextSlot).Scan(&xid).Error; err != nil {
			t.Fatal(err)
		}
		return xid
	}
	before := factVersion()
	if err := db.Model(&runnerRequestRecord{}).Where("id='success'").UpdateColumn("watch_expires_at", now).Error; err != nil {
		t.Fatal(err)
	}
	refresh(false)
	if after := factVersion(); before == "" || after != before {
		t.Fatalf("unrelated update rewrote fact %q to %q", before, after)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SELECT pg_advisory_xact_lock(?)", accountWorkerLock).Error; err != nil {
			return err
		}
		if err := RefreshAccountReports(t.Context(), opts, false); err == nil {
			return errors.New("second writer acquired the report lock")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	oldReport := check(q)
	var old accountReportPublication
	if err := db.First(&old, 1).Error; err != nil {
		t.Fatal(err)
	}
	// Interrupt after changed facts have committed, before daily rollups.
	if err := db.Exec("ALTER TABLE report_account_days ADD CONSTRAINT reject_report_days CHECK (false) NOT VALID").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&runnerRequestRecord{}).Where("id=?", "success").UpdateColumns(map[string]any{"github_job_conclusion": "success", "stopping_at": nil}).Error; err != nil {
		t.Fatal(err)
	}
	if err := RefreshAccountReports(t.Context(), opts, true); err == nil {
		t.Fatal("expected report-only write failure")
	}
	var still accountReportPublication
	if err := db.First(&still, 1).Error; err != nil || !old.ObservedAt.Equal(still.ObservedAt) || old.Slot != still.Slot {
		t.Fatalf("failed job published: %+v %v", still, err)
	}
	previous, err := published.ReadRunnerReport(t.Context(), q, time.Now().UTC())
	if err != nil || previous.Totals != oldReport.Totals || !reflect.DeepEqual(previous.Outcomes, oldReport.Outcomes) {
		t.Fatalf("reader lost published values after interrupted rebuild: %v", err)
	}
	if still.PendingEpoch <= old.Epoch {
		t.Fatal("interrupted rebuild has no resumable epoch")
	}
	// Keep both the fact and rollup writes broken while the Runner uses its
	// real lifecycle transactions. A future synchronous fact write would fail here.
	if err := db.Exec("ALTER TABLE report_account_facts ADD CONSTRAINT reject_report_facts CHECK (false) NOT VALID").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&runnerRequestRecord{}).Where("id='success'").UpdateColumn("github_job_conclusion", "cancelled").Error; err != nil {
		t.Fatal(err)
	}
	if err := RefreshAccountReports(t.Context(), opts, false); err == nil || !strings.Contains(err.Error(), "reject_report_facts") {
		t.Fatalf("fact write was not rejected: %v", err)
	}
	verifyExecutionUsage(t, live)
	if err := db.Migrator().DropConstraint(&accountFact{}, "reject_report_facts"); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropConstraint(&accountDay{}, "reject_report_days"); err != nil {
		t.Fatal(err)
	}
	// Same request requeued, owner renamed, deleted and new source rows.
	if _, err := live.RetryRequest("custom", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	if err := db.Model(&githubInstallationOwnerRecord{}).Where("installation_id=1").Update("account_login", "renamed").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Where("id=?", "bad-time").Delete(&runnerRequestRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	refresh(false)
	var resumed accountReportPublication
	if err := db.First(&resumed, 1).Error; err != nil || resumed.Epoch != still.PendingEpoch {
		t.Fatalf("failed to resume epoch: %+v %v", resumed, err)
	}
	check(q)
	refresh(true)
	check(q)
	refresh(false)
	check(q)
	// A definition upgrade must recompute unchanged source rows, including
	// the second slot the next time it becomes active.
	if err := db.Model(&accountReportPublication{}).Where("id=1").Update("definition", accountReportDefinition-1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&accountFact{}).Where("id='success'").UpdateColumn("runtime_seconds", 9999).Error; err != nil {
		t.Fatal(err)
	}
	refresh(false)
	check(q)
	refresh(false)
	check(q)
	// Readers use only the published generation.
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("ALTER TABLE runner_requests RENAME TO hidden_source").Error; err != nil {
			return err
		}
		if _, err := readPublishedAccounts(tx, q, time.Now().UTC()); err != nil {
			return err
		}
		return gorm.ErrInvalidTransaction
	})
	if !errors.Is(err, gorm.ErrInvalidTransaction) {
		t.Fatal(err)
	}
	if os.Getenv("RUNNERD_REPORT_SCALE_TEST") == "1" {
		t.Run("scale", func(t *testing.T) { verifyPublishedAccountScale(t, live, published, opts) })
	}

	t.Run("rollback preserves newer schema", func(t *testing.T) {
		if err := db.Exec("ALTER TABLE report_account_facts ALTER COLUMN status SET NOT NULL").Error; err != nil {
			t.Fatal(err)
		}
		for _, column := range []string{"definition", "pending_definition"} {
			versions := map[string]any{"definition": accountReportDefinition, "pending_definition": 0}
			versions[column] = accountReportDefinition + 1
			if err := db.Model(&accountReportPublication{}).Where("id=1").Updates(versions).Error; err != nil {
				t.Fatal(err)
			}
			err := RefreshAccountReports(t.Context(), opts, true)
			if err == nil || !strings.Contains(err.Error(), "require a newer worker") {
				t.Fatalf("newer %s: got %v", column, err)
			}
			var nullable string
			err = db.Raw(`SELECT is_nullable FROM information_schema.columns WHERE table_schema=current_schema()
				AND table_name='report_account_facts' AND column_name='status'`).Scan(&nullable).Error
			if err != nil || nullable != "NO" {
				t.Fatalf("newer %s: status constraint changed: nullable=%q, err=%v", column, nullable, err)
			}
		}
	})
}

func TestPublishedAccountGroupBudgetsPostgres(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL _test database")
	}
	opts := Options{Backend: BackendPostgres, DatabaseDSN: testpg.SchemaDSN(t)}
	db, err := (&DBStore{opts: opts}).open()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	if err := db.AutoMigrate(&accountReportPublication{}, &accountFact{}, &accountDay{}); err != nil {
		t.Fatal(err)
	}
	observed := time.Date(2026, 1, 3, 12, 0, 0, 0, time.UTC)
	if err := db.Create(&accountReportPublication{ID: 1, Definition: accountReportDefinition, ObservedAt: observed}).Error; err != nil {
		t.Fatal(err)
	}
	const requests = ReportMaxRows / 2
	at := observed.Add(-time.Hour)
	if err := db.Exec(`INSERT INTO report_account_facts(slot,id,queued_at,requests,owner,repository,spec)
		SELECT 0,i::text,?,1,'owner-'||i,'repo-'||i,'shared' FROM generate_series(1,?) i`, at, requests).Error; err != nil {
		t.Fatal(err)
	}
	q := RunnerReportQuery{IncludeAccountUsage: true, From: "2026-01-03", To: "2026-01-04", Grain: "day", Timezone: "UTC"}
	r, err := readPublishedAccounts(db, q, observed)
	if err != nil {
		t.Fatal(err)
	}
	if r.DetailAvailable == nil || !*r.DetailAvailable || len(r.Rows) != requests || r.RetainedEntries() != 3*requests+1 {
		t.Fatalf("expected %d details and %d groups, got %d details and %d retained entries", requests, 2*requests+1, len(r.Rows), r.RetainedEntries())
	}

	// A second request for each pair keeps the cohort detail-eligible but
	// pushes requests plus groups one entry over the snapshot budget.
	if err := db.Exec(`INSERT INTO report_account_facts(slot,id,queued_at,requests,owner,repository,spec)
		SELECT slot,'second-'||id,queued_at,requests,owner,repository,spec FROM report_account_facts`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := readPublishedAccounts(db, q, observed); !errors.Is(err, ErrReportTooManyGroups) {
		t.Fatalf("oversized detail snapshot: %v", err)
	}
	// One previous-period request switches the same groups to aggregate-only.
	if err := db.Create(&accountFact{Slot: 0, ID: "previous", QueuedAt: at.AddDate(0, 0, -1), Totals: ReportTotals{Requests: 1}}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := readPublishedAccounts(db, q, observed); !errors.Is(err, ErrReportTooManyGroups) {
		t.Fatalf("oversized aggregate snapshot: %v", err)
	}
}

func verifyPublishedAccountScale(t *testing.T, live, published *DBStore, opts Options) {
	t.Helper()
	db, err := live.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	// Spread 510,000 completed requests over the current and previous 30 days.
	if err := db.Exec(`INSERT INTO runner_requests(id,runner_name,source,labels_json,status,repository_full_name,profile_source,profile_name,github_installation_id,queued_at,running_at,completed_at,updated_at)
		SELECT id,id,'fixture','[]','completed','repo-'||(i%161),'global','spec-'||(i%4),i%9,at,at,at + interval '1 second',at
		FROM (SELECT i,'account-scale-'||i AS id, ?::timestamptz-i*interval '10 seconds' AS at FROM generate_series(1,510000) i) source`, now.Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ANALYZE runner_requests").Error; err != nil {
		t.Fatal(err)
	}
	claim := func(n int) time.Duration {
		t.Helper()
		id := fmt.Sprintf("account-report-probe-%d", n)
		_, _, err := live.CreateRequest(RunnerRequest{ID: id, CreatedAt: now.AddDate(-10, 0, -n)}, nil)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		req, _, ok, err := live.ClaimNextRunnable(id, time.Minute)
		if err != nil || !ok || req.ID != id {
			t.Fatalf("claim %q: %q, %v", id, req.ID, err)
		}
		return time.Since(start)
	}
	measure := func(offset int) (time.Duration, time.Duration) {
		var samples []time.Duration
		for i := 0; i < 10; i++ {
			samples = append(samples, claim(offset+i))
		}
		slices.Sort(samples)
		return (samples[4] + samples[5]) / 2, samples[9]
	}
	median, peak := measure(0)
	t.Logf("10 baseline queue claims median %s, max %s", median, peak)
	start := time.Now()
	done := make(chan error, 1)
	go func() { done <- RefreshAccountReports(t.Context(), opts, true) }()
	// Observe real batch progress on the inactive slot before probing.
	for {
		var writing bool
		if err := db.Raw("SELECT EXISTS(SELECT 1 FROM report_account_facts WHERE id LIKE 'account-scale-%' LIMIT 1)").Scan(&writing).Error; err != nil {
			t.Fatal(err)
		}
		if writing {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("rebuild ended before overlap: %v", err)
		case <-time.After(75 * time.Millisecond):
		}
	}
	median, peak = measure(20)
	select {
	case err := <-done:
		t.Fatalf("rebuild ended during overlap: %v", err)
	default:
	}
	t.Logf("10 rebuilding queue claims median %s, max %s", median, peak)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Logf("rebuild %s", time.Since(start))
	q := RunnerReportQuery{IncludeAccountUsage: true, Grain: "day", Timezone: "UTC", From: now.AddDate(0, 0, -29).Format(time.DateOnly), To: now.AddDate(0, 0, 1).Format(time.DateOnly)}
	read := func(s *DBStore, at time.Time) *RunnerReport {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
		defer cancel()
		start := time.Now()
		r, err := s.ReadRunnerReport(ctx, q, at)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("published=%v, %d current + %d previous in %s", s.opts.PublishedAccountReports, r.Totals.Requests, r.Previous.Requests, time.Since(start))
		return r
	}
	got := read(published, time.Now())
	want := read(live, got.ObservedAt)
	if got.Rows != nil || got.Totals != want.Totals || got.Previous != want.Previous || !reflect.DeepEqual(got.Trend, want.Trend) || !reflect.DeepEqual(got.ChartDistributions(), want.ChartDistributions()) {
		t.Fatal("large published report differs")
	}
	start = time.Now()
	if err := RefreshAccountReports(t.Context(), opts, false); err != nil {
		t.Fatal(err)
	}
	t.Logf("second slot populated in %s", time.Since(start))
	start = time.Now()
	go func() { done <- RefreshAccountReports(t.Context(), opts, false) }()
	time.Sleep(75 * time.Millisecond)
	median, peak = measure(40)
	select {
	case err := <-done:
		t.Fatalf("incremental refresh ended during claim sample: %v", err)
	default:
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	t.Logf("incremental refresh %s, 10 queue claims median %s, max %s", time.Since(start), median, peak)
}
