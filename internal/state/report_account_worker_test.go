package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/qiniu/ci-runner/internal/testpg"
)

func readAccountSourceForTest(t *testing.T, s *DBStore, q RunnerReportQuery, at time.Time) (*RunnerReport, error) {
	t.Helper()
	window, err := q.Window(at)
	if err != nil {
		return nil, err
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	var r *RunnerReport
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		r, err = readSourceReport(ctx, tx, q, window, at)
		return err
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	return r, err
}

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
	opts.MigrateOnStart = false
	published := NewWithOptions(opts).(*DBStore)
	pdb, err := published.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, pdb)
	now := time.Now().UTC()
	if r, err := published.ReadRunnerReport(t.Context(), q, now); err != nil || r.Totals.Requests == 0 || !r.ObservedAt.Equal(now) {
		t.Fatalf("expected live report before first publication: %v", err)
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
		want, err := readAccountSourceForTest(t, live, q, got.ObservedAt)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(got.Rows)
		b, _ := json.Marshal(want.Rows)
		if !reflect.DeepEqual(got.Efficiency, want.Efficiency) {
			t.Fatalf("published efficiency differs: %+v / %+v", got.Efficiency, want.Efficiency)
		}
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
	// Independent replicas starting close together must publish only once.
	for range 2 {
		if err := refreshAccountReports(t.Context(), opts, false, true); err != nil {
			t.Fatal(err)
		}
	}
	var first accountReportPublication
	if err := db.First(&first, 1).Error; err != nil || first.Slot != 1 || first.PublishedAt.IsZero() {
		t.Fatalf("expected a single initial publication: %+v, %v", first, err)
	}
	if err := db.Model(&first).UpdateColumn("published_at", gorm.Expr("clock_timestamp() - interval '6 minutes'")).Error; err != nil {
		t.Fatal(err)
	}
	if err := refreshAccountReports(t.Context(), opts, false, true); err != nil {
		t.Fatal(err)
	}
	var next accountReportPublication
	if err := db.First(&next, 1).Error; err != nil || next.Slot == first.Slot || !next.PublishedAt.After(first.PublishedAt) {
		t.Fatalf("expected another replica to refresh a due publication: %+v, %v", next, err)
	}
	later := next.ObservedAt.AddDate(0, 0, 3)
	future := defaultAccountQuery(later)
	future.From = later.UTC().Format(time.DateOnly)
	r, err := published.ReadRunnerReport(t.Context(), future, later)
	if err != nil || !r.ObservedAt.Equal(later) {
		t.Fatalf("expected live report beyond the last publication: %v", err)
	}
	eff := defaultAccountQuery(next.ObservedAt)
	eff.IncludeAccountUsage, eff.IncludeEfficiency = false, true
	gotEfficiency := check(eff)
	if !gotEfficiency.ObservedAt.Equal(next.ObservedAt) || gotEfficiency.Efficiency.CleanupP95 == nil {
		t.Fatal("default efficiency did not use the publication")
	}
	for _, group := range gotEfficiency.Efficiency.Groups {
		rows, err := gotEfficiency.Requests(group.Dimension, group.Key)
		if err != nil || int64(len(rows)) != group.Count {
			t.Fatalf("efficiency drilldown %v: %d, %v", group, len(rows), err)
		}
	}
	for _, change := range []func(*RunnerReportQuery){
		func(q *RunnerReportQuery) { q.Repository = "team/one" },
		func(q *RunnerReportQuery) { q.From = endDay.Format(time.DateOnly) },
		func(q *RunnerReportQuery) { q.Grain = "week" },
		func(q *RunnerReportQuery) { q.Timezone = "Asia/Shanghai" },
	} {
		filtered := eff
		change(&filtered)
		at := time.Now().UTC()
		if r, err := published.ReadRunnerReport(t.Context(), filtered, at); err != nil || !r.ObservedAt.Equal(at) {
			t.Fatalf("nondefault efficiency must use the source: %v", err)
		}
	}
	t.Run("UTC midnight", func(t *testing.T) {
		observed := next.ObservedAt.UTC()
		midnight := observed.Truncate(24 * time.Hour).Add(24 * time.Hour)
		// Change both the current and comparison denominators when midnight
		// shifts the default window, and exercise exact inclusive boundaries.
		start := midnight.AddDate(0, 0, -29)
		for i, at := range []time.Time{start.Add(-time.Second), start, start.AddDate(0, 0, -30).Add(-time.Second), start.AddDate(0, 0, -30)} {
			id := fmt.Sprintf("midnight-%d", i)
			if err := db.Create(&runnerRequestRecord{ID: id, RunnerName: id, QueuedAt: at, Status: StatusFailed, FailureStage: "admission", FailureReason: id, FailedAt: &at}).Error; err != nil {
				t.Fatal(err)
			}
		}
		refresh(false)
		reportBefore := check(eff)
		query := defaultAccountQuery(midnight)
		query.IncludeAccountUsage, query.IncludeEfficiency = false, true
		observed = reportBefore.ObservedAt
		window, err := query.windowAt(observed, midnight)
		if err != nil {
			t.Fatal(err)
		}
		want, err := readSourceReport(t.Context(), db, query, window, observed)
		if err != nil {
			t.Fatal(err)
		}
		report, err := published.ReadRunnerReport(t.Context(), query, midnight)
		if err != nil {
			t.Fatal(err)
		}
		if !report.ObservedAt.Equal(observed) || report.Totals == reportBefore.Totals || report.Totals != want.Totals ||
			report.Previous != want.Previous || !reflect.DeepEqual(report.Efficiency, want.Efficiency) ||
			!reflect.DeepEqual(report.Trend, want.Trend) || !reflect.DeepEqual(report.ChartDistributions(), want.ChartDistributions()) ||
			len(report.Efficiency.Rejections["midnight-1"]) != 1 || len(report.Efficiency.Rejections["midnight-0"]) != 0 {
			t.Fatal("shifted default dashboard differs from the source at the published observation time")
		}
		gotRows, err := json.Marshal(report.Rows)
		if err != nil {
			t.Fatal(err)
		}
		wantRows, err := json.Marshal(want.Rows)
		if err != nil || string(gotRows) != string(wantRows) {
			t.Fatal("shifted request details differ from source", err)
		}
		// Simulate a query with the business table unavailable.
		err = db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Exec("ALTER TABLE runner_requests RENAME TO hidden_source").Error; err != nil {
				return err
			}
			got, err := readPublishedUsageReport(tx, query, midnight)
			if err != nil {
				return err
			}
			if got == nil || got.Totals != want.Totals {
				return errors.New("lost midnight publication")
			}
			return gorm.ErrInvalidTransaction
		})
		if !errors.Is(err, gorm.ErrInvalidTransaction) {
			t.Fatal(err)
		}
	})
	t.Run("efficiency failure preserves account refresh", func(t *testing.T) {
		// The exact overload takes precedence over PostgreSQL's polymorphic
		// percentile_disc in this isolated schema. Only the efficiency SELECT fails.
		if err := db.Exec(`CREATE FUNCTION reject_sample(float8, float8) RETURNS float8 LANGUAGE plpgsql AS
			$$ BEGIN RAISE EXCEPTION 'injected percentile failure'; END $$`).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Exec(`CREATE AGGREGATE percentile_disc(float8 ORDER BY float8)
			(SFUNC=reject_sample, STYPE=float8)`).Error; err != nil {
			t.Fatal(err)
		}

		for i := range 2 {
			before := check(defaultAccountQuery(time.Now().UTC()))
			id := fmt.Sprintf("summary-failure-%d", i)
			if err := db.Create(&runnerRequestRecord{ID: id, RunnerName: id, QueuedAt: endDay, Status: StatusQueued}).Error; err != nil {
				t.Fatal(err)
			}
			refresh(false)
			after := check(defaultAccountQuery(time.Now().UTC()))
			if !after.ObservedAt.After(before.ObservedAt) || after.Totals.Requests != before.Totals.Requests+1 {
				t.Fatal("account publication did not advance")
			}
			if r, err := readPublishedUsageReport(db, eff, time.Now().UTC()); err != nil || r != nil {
				t.Fatalf("served stale efficiency summary: %v", err)
			}
			at := time.Now().UTC()
			if r, err := published.ReadRunnerReport(t.Context(), eff, at); err != nil || !r.ObservedAt.Equal(at) || r.Efficiency == nil {
				t.Fatalf("efficiency source fallback: %v", err)
			}
		}
		if err := db.Exec("DROP AGGREGATE percentile_disc(float8 ORDER BY float8)").Error; err != nil {
			t.Fatal(err)
		}
		refresh(false)
		recovered, err := readPublishedUsageReport(db, eff, time.Now().UTC())
		if err != nil || recovered == nil || !recovered.ObservedAt.Equal(check(eff).ObservedAt) {
			t.Fatal("missing recovered summary")
		}
	})
	// Both slots are warm. Updates that change only anomaly evidence must
	// invalidate the fact even when the lifecycle and business version stand still.
	if err := db.Model(&runnerRequestRecord{}).Where("id='rejected'").UpdateColumn("failure_reason", "labels_changed").Error; err != nil {
		t.Fatal(err)
	}
	refresh(false)
	updated := check(eff)
	if len(updated.Efficiency.Rejections["labels_changed"]) == 0 {
		t.Fatal("late rejection reason was not published")
	}
	if err := db.Model(&runnerRequestRecord{}).Where("id='success'").UpdateColumn("termination_source", "process_exit").Error; err != nil {
		t.Fatal(err)
	}
	refresh(false)
	check(eff)
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
		if err := refreshAccountReports(t.Context(), opts, false, true); err != nil {
			return fmt.Errorf("background replica should skip a busy writer: %w", err)
		}
		if err := RefreshAccountReports(t.Context(), opts, false); err == nil {
			return errors.New("second writer acquired the report lock")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// A disconnected coordinator cannot write another batch after takeover.
	stale := accountBatchWriter{db: db}
	if err := db.Transaction(func(tx *gorm.DB) error {
		stale.leader = tx
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := stale.batch(func(*gorm.DB) error {
		t.Fatal("a finished coordinator entered a write batch")
		return nil
	}); err == nil {
		t.Fatal("expected a stale coordinator error")
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
	if still.PendingEpoch <= old.Epoch || still.Definition != 2 || still.PendingDefinition != 2 {
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
	// Recreate the released v2 payload and nullable-column
	// boundary on both slots. The account reader must already work before DDL.
	if err := db.Exec(`UPDATE report_account_dashboards SET payload=convert_to(
		(convert_from(payload,'UTF8')::jsonb - 'Efficiency' - 'EfficiencyDefinition')::text,'UTF8')`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE report_account_facts DROP COLUMN failure_class, DROP COLUMN stage, DROP COLUMN reason, DROP COLUMN termination, DROP COLUMN efficiency_fingerprint").Error; err != nil {
		t.Fatal(err)
	}
	account := defaultAccountQuery(time.Now().UTC())
	at := time.Now().UTC()
	if r, err := published.ReadRunnerReport(t.Context(), account, at); err != nil || r.ObservedAt.Equal(at) {
		t.Fatalf("lost v2 account report during upgrade: %v", err)
	}
	if r, err := published.ReadRunnerReport(t.Context(), eff, at); err != nil || !r.ObservedAt.Equal(at) {
		t.Fatalf("expected original efficiency query during upgrade: %v", err)
	}
	refresh(false)
	check(eff)
	refresh(false)
	check(eff)
	// A change to the account metric definition still rebuilds both slots
	// even when their source fingerprints are already current.
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
	check(eff)
	// Readers use only the published generation.
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("ALTER TABLE runner_requests RENAME TO hidden_source").Error; err != nil {
			return err
		}
		r, err := readPublishedUsageReport(tx, q, time.Now().UTC())
		if err != nil {
			return err
		}
		if r == nil {
			return errors.New("lost compatible publication")
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

// The released executable reads and updates only its original v2 columns.
// Running it here also catches differences from a simulated legacy writer.
func TestPublishedAccountRollbackPostgres(t *testing.T) {
	legacy := os.Getenv("RUNNERD_TEST_ACCOUNT_V2_BINARY")
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" || legacy == "" {
		t.Skip("requires a dedicated PostgreSQL test database and the released v2 runnerd")
	}
	opts := Options{Backend: BackendPostgres, DatabaseDSN: testpg.SchemaDSN(t), MigrateOnStart: true}
	store := NewWithOptions(opts).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	config := filepath.Join(t.TempDir(), "runnerd.yaml")
	if err := os.WriteFile(config, []byte(fmt.Sprintf("database:\n  backend: postgres\n  dsn: %q\n", opts.DatabaseDSN)), 0o600); err != nil {
		t.Fatal(err)
	}
	oldRefresh := func() {
		t.Helper()
		out, err := exec.CommandContext(t.Context(), legacy, "--config", config, "--account-reports=refresh").CombinedOutput()
		if err != nil {
			t.Fatalf("released worker: %v\n%s", err, out)
		}
	}
	add := func(id string) {
		t.Helper()
		at := time.Now().UTC().Add(-24 * time.Hour)
		if err := db.Create(&runnerRequestRecord{ID: id, RunnerName: id, QueuedAt: at, Status: StatusFailed, FailureStage: "admission", FailureReason: id, FailedAt: &at}).Error; err != nil {
			t.Fatal(err)
		}
	}
	publication := func() accountReportPublication {
		t.Helper()
		var pub accountReportPublication
		if err := db.Take(&pub, 1).Error; err != nil {
			t.Fatal(err)
		}
		return pub
	}
	add("first")
	oldRefresh()
	first := publication()
	for range 2 {
		if err := RefreshAccountReports(t.Context(), opts, false); err != nil {
			t.Fatal(err)
		}
	}
	add("before-interrupt")
	if err := db.Exec("ALTER TABLE report_account_days ADD CONSTRAINT interrupt_days CHECK(false) NOT VALID").Error; err != nil {
		t.Fatal(err)
	}
	if err := RefreshAccountReports(t.Context(), opts, true); err == nil || !strings.Contains(err.Error(), "interrupt_days") {
		t.Fatalf("expected interruption after fact batches: %v", err)
	}
	pending := publication()
	if pending.PendingEpoch <= first.Epoch || pending.PendingDefinition != 2 || pending.Definition != 2 {
		t.Fatalf("released worker cannot resume %+v", pending)
	}
	if err := db.Migrator().DropConstraint(&accountDay{}, "interrupt_days"); err != nil {
		t.Fatal(err)
	}
	add("after-interrupt")
	readXmin := func() string {
		t.Helper()
		var factXmin string
		if err := db.Raw("SELECT xmin::text FROM report_account_facts WHERE slot=? AND id='before-interrupt'", 1-pending.Slot).Row().Scan(&factXmin); err != nil {
			t.Fatal(err)
		}
		return factXmin
	}
	before := readXmin()
	oldRefresh()
	resumed := publication()
	if resumed.Epoch != pending.PendingEpoch || resumed.PendingEpoch != 0 || !resumed.ObservedAt.After(pending.ObservedAt) || readXmin() != before {
		t.Fatalf("v2 failed to resume committed batches: %+v", resumed)
	}
	q := defaultAccountQuery(resumed.ObservedAt)
	account, err := store.ReadRunnerReport(t.Context(), q, time.Now())
	if err != nil || !account.ObservedAt.Equal(resumed.ObservedAt) || account.Totals.Requests != 3 {
		t.Fatalf("rollback account publication: %v", err)
	}
	q.IncludeAccountUsage, q.IncludeEfficiency = false, true
	if r, err := readPublishedUsageReport(db, q, time.Now()); err != nil || r != nil {
		t.Fatalf("old writer left stale efficiency data: %v", err)
	}
	if err := RefreshAccountReports(t.Context(), opts, false); err != nil {
		t.Fatal(err)
	}
	got, err := readPublishedUsageReport(db, q, time.Now())
	if err != nil || got == nil {
		t.Fatalf("missing republished efficiency report: %v", err)
	}
	want, err := readAccountSourceForTest(t, store, q, got.ObservedAt)
	if err != nil || got.Totals != want.Totals || !reflect.DeepEqual(got.Efficiency, want.Efficiency) {
		t.Fatalf("re-upgraded report differs from source: %v", err)
	}
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
	r, err := readPublishedUsageReport(db, q, observed)
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
	if _, err := readPublishedUsageReport(db, q, observed); !errors.Is(err, ErrReportTooManyGroups) {
		t.Fatalf("oversized detail snapshot: %v", err)
	}
	// One previous-period request switches the same groups to aggregate-only.
	if err := db.Create(&accountFact{Slot: 0, ID: "previous", QueuedAt: at.AddDate(0, 0, -1), Totals: ReportTotals{Requests: 1}}).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := readPublishedUsageReport(db, q, observed); !errors.Is(err, ErrReportTooManyGroups) {
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
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	got, err := published.ReadRunnerReport(ctx, q, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want, err := readAccountSourceForTest(t, live, q, got.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rows != nil || got.Totals != want.Totals || got.Previous != want.Previous || !reflect.DeepEqual(got.Trend, want.Trend) || !reflect.DeepEqual(got.ChartDistributions(), want.ChartDistributions()) {
		t.Fatal("large published report differs")
	}
	q.IncludeAccountUsage, q.IncludeEfficiency = false, true
	got, err = published.ReadRunnerReport(t.Context(), q, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	want, err = readAccountSourceForTest(t, live, q, got.ObservedAt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Rows != nil || *got.DetailAvailable || got.Totals != want.Totals || got.Previous != want.Previous ||
		!reflect.DeepEqual(got.Trend, want.Trend) || !reflect.DeepEqual(got.Efficiency, want.Efficiency) ||
		!reflect.DeepEqual(got.ChartDistributions(), want.ChartDistributions()) {
		t.Fatal("large published efficiency report differs from the source at the publication time")
	}
	midnight := got.ObservedAt.UTC().Truncate(24 * time.Hour).Add(24 * time.Hour)
	q = defaultAccountQuery(midnight)
	q.IncludeAccountUsage, q.IncludeEfficiency = false, true
	start = time.Now()
	shiftCtx, shiftCancel := context.WithTimeout(t.Context(), 10*time.Second)
	shifted, err := published.ReadRunnerReport(shiftCtx, q, midnight)
	shiftCancel()
	if err != nil || !shifted.ObservedAt.Equal(got.ObservedAt) {
		t.Fatalf("large midnight publication: %v", err)
	}
	t.Logf("midnight published efficiency %s, %d + %d requests", time.Since(start), shifted.Totals.Requests, shifted.Previous.Requests)
	sourceWindow, err := q.windowAt(got.ObservedAt, midnight)
	if err != nil {
		t.Fatal(err)
	}
	shiftWant, err := readSourceReport(t.Context(), db, q, sourceWindow, got.ObservedAt)
	if err != nil || shifted.Totals != shiftWant.Totals || shifted.Previous != shiftWant.Previous || !reflect.DeepEqual(shifted.Efficiency, shiftWant.Efficiency) ||
		!reflect.DeepEqual(shifted.Trend, shiftWant.Trend) || !reflect.DeepEqual(shifted.ChartDistributions(), shiftWant.ChartDistributions()) {
		t.Fatalf("large cross-midnight reconciliation: %v", err)
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
