package state

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/qiniu/ci-runner/internal/testpg"
)

func TestPublishedAccountSpecSnapshotBudgetPostgres(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires a dedicated PostgreSQL test database")
	}
	ctx := t.Context()
	opts := Options{Backend: BackendPostgres, DatabaseDSN: testpg.SchemaDSN(t)}
	legacy := NewWithOptions(opts).(*DBStore)
	legacyDB, err := legacy.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, legacyDB)
	if err := legacyDB.Exec(`CREATE TABLE report_spec_snapshots (
		id varchar(40) PRIMARY KEY, expires_at timestamptz NOT NULL, entries bigint NOT NULL, payload bytea)`).Error; err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(5 * time.Minute)
	if err := legacyDB.Exec(`INSERT INTO report_spec_snapshots (id, expires_at, entries, payload) VALUES (?, ?, ?, ?)`,
		"legacy-spec", expires, 1, []byte("legacy")).Error; err != nil {
		t.Fatal(err)
	}

	opts.MigrateOnStart = true
	current := NewWithOptions(opts).(*DBStore)
	db, err := current.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	if body, err := current.LoadReportSnapshot(ctx, "legacy-spec", time.Now()); err != nil || string(body) != "legacy" {
		t.Fatalf("new reader lost the old snapshot: %q, %v", body, err)
	}

	saved, err := current.SaveSpecReportSnapshot(ctx, "protected", "same query", expires, ReportSnapshotEntries, []byte("protected"))
	if err != nil {
		t.Fatal(err)
	}
	reused, err := current.SaveSpecReportSnapshot(ctx, "reuse", "same query", expires, 1, []byte("new payload"))
	if err != nil || reused.ID != saved.ID || string(reused.ReusedPayload) != "protected" {
		t.Fatalf("same-query reuse at the entry limit: %+v, %v", reused, err)
	}
	if _, err := current.SaveSpecReportSnapshot(ctx, "overflow", "different query", expires, 1, []byte("overflow")); !errors.Is(err, ErrSpecSnapshotCapacity) {
		t.Fatalf("expected a capacity error from the entry budget, got %v", err)
	}
	if err := current.SaveReportSnapshot(ctx, "acct-usage", expires, ReportSnapshotEntries, []byte("account")); err != nil {
		t.Fatalf("full spec budget affected usage admission: %v", err)
	}
	if body, err := legacy.LoadReportSnapshot(ctx, saved.ID, time.Now()); err != nil || string(body) != "protected" {
		t.Fatalf("old reader could not restore the protected snapshot: %q, %v", body, err)
	}
	if _, err := current.LoadReportSnapshot(ctx, saved.ID, expires.Add(time.Second)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected genuine expiry to return ErrNotFound, got %v", err)
	}
	if err := legacyDB.Model(&reportSpecSnapshotRecord{}).Where("id = ?", saved.ID).
		UpdateColumn("expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := current.SaveSpecReportSnapshot(ctx, "replacement", "different query", expires, ReportSnapshotEntries, []byte("replacement")); err != nil {
		t.Fatalf("expiry did not free the spec budget: %v", err)
	}
	var remaining int64
	if err := db.Model(&reportSpecSnapshotRecord{}).Where("id = ?", saved.ID).Count(&remaining).Error; err != nil || remaining != 0 {
		t.Fatalf("old protected snapshot survived expiry cleanup: %d, %v", remaining, err)
	}
}

func TestPublishedAccountLegacyWriterExcludesProtectedSpecsPostgres(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires a dedicated PostgreSQL test database")
	}
	ctx := t.Context()
	store := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: testpg.SchemaDSN(t), MigrateOnStart: true}).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	expires := time.Now().Add(5 * time.Minute)
	if err := store.SaveReportSnapshot(ctx, "acct-first", expires, 1, []byte("account group")); err != nil {
		t.Fatal(err)
	}
	var specs []string
	for i := range ReportSnapshotLimit {
		id := fmt.Sprintf("query-%d", i)
		saved, err := store.SaveSpecReportSnapshot(ctx, id, id, expires.Add(time.Second), 1, []byte(id))
		if err != nil {
			t.Fatal(err)
		}
		specs = append(specs, saved.ID)
	}

	// The released writer reserves a slot for its new snapshot, counts the seven
	// later-expiring Specs rows and attempts to delete the remaining rows.
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(`INSERT INTO report_spec_snapshots (id, expires_at, entries) VALUES ('writer', ?, 0)
			ON CONFLICT (id) DO UPDATE SET expires_at = excluded.expires_at`, expires).Error; err != nil {
			return err
		}
		var candidates []string
		if err := tx.Model(&reportSpecSnapshotRecord{}).Where("id <> 'writer'").Order("expires_at DESC, id ASC").
			Offset(ReportSnapshotLimit-1).Pluck("id", &candidates).Error; err != nil {
			return err
		}
		if len(candidates) != 2 || candidates[0] != specs[7] || candidates[1] != "acct-first" {
			return fmt.Errorf("expected legacy pruning of a protected spec and the first account, got %v", candidates)
		}
		if err := tx.Where("id IN ?", candidates).Delete(&reportSpecSnapshotRecord{}).Error; err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO report_spec_snapshots (id, expires_at, entries, payload) VALUES (?, ?, ?, ?)`,
			"eff-next", expires.Add(2*time.Second), 1, []byte("efficiency")).Error
	})
	if err != nil {
		t.Fatal(err)
	}
	if body, err := store.LoadReportSnapshot(ctx, "acct-first", time.Now()); err != nil || string(body) != "account group" {
		t.Fatalf("legacy writer evicted an account below the legacy budget: %q, %v", body, err)
	}
	for _, id := range specs {
		if _, err := store.LoadReportSnapshot(ctx, id, time.Now()); err != nil {
			t.Fatalf("legacy writer removed protected spec %q: %v", id, err)
		}
	}
	for i := range ReportSnapshotLimit - 1 {
		if err := store.SaveReportSnapshot(ctx, fmt.Sprintf("acct-new-%d", i), expires.Add(3*time.Second), 1, []byte("account")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.LoadReportSnapshot(ctx, "acct-first", time.Now()); !errors.Is(err, ErrNotFound) {
		t.Fatalf("legacy pool did not evict its oldest snapshot at capacity: %v", err)
	}
}
