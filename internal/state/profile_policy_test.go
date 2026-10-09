package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func addRetiredRunnerPolicyColumns(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, column := range []struct{ name, definition string }{
		{"runner_update_policy", "VARCHAR(32) NOT NULL DEFAULT 'official'"},
		{"require_docker", "BOOLEAN NOT NULL DEFAULT FALSE"},
		{"fork_sponsorship", "BOOLEAN NOT NULL DEFAULT FALSE"},
	} {
		if db.Migrator().HasColumn(&runnerProfileRecord{}, column.name) {
			t.Fatalf("fresh schema unexpectedly contains %s", column.name)
		}
		if err := db.Exec("ALTER TABLE runner_profiles ADD COLUMN " + column.name + " " + column.definition).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func assertRetiredRunnerPolicyValues(t *testing.T, db *gorm.DB) {
	t.Helper()
	var policy string
	if err := db.Raw("SELECT runner_update_policy FROM runner_profiles WHERE name = ?", "legacy").Scan(&policy).Error; err != nil || policy != "official" {
		t.Fatalf("retired column was altered: %q %v", policy, err)
	}
}

func TestRetiredRunnerPolicyColumnsRemainInert(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	addRetiredRunnerPolicyColumns(t, db)
	if err := db.Create(&runnerProfileRecord{Name: "legacy", LabelsJSON: `["qiniu"]`, ManagedBy: "runnerd", DefaultTemplateName: "public-name"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.migrate(db); err != nil {
		t.Fatal(err)
	}
	p, err := store.GetProfile("legacy")
	if err != nil || p.TemplateSource != TemplateSourcePublic || !p.Published {
		t.Fatalf("retired columns affected public binding: %#v %v", p, err)
	}
	assertRetiredRunnerPolicyValues(t, db)
}

func TestLegacyRunnerProfilePolicyMigrationPreservesControlsAndDoesNotReseed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	store := NewWithOptions(Options{Backend: BackendSQLite, DatabaseDSN: path, MigrateOnStart: true}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE runner_profiles (name TEXT PRIMARY KEY, labels_json TEXT NOT NULL, required_labels_json TEXT, template_id TEXT NOT NULL, default_template_name TEXT, runner_group TEXT, max_concurrency INTEGER NOT NULL, min_idle INTEGER NOT NULL, priority INTEGER NOT NULL, enabled NUMERIC NOT NULL, default_available NUMERIC NOT NULL, managed_by TEXT, catalog_revision INTEGER NOT NULL, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC)
	for _, row := range []struct{ name, owner, template string }{{"public", "qiniu/ci-runner", "public-name"}, {"private", "", "stray-public-name"}, {"legacy-incomplete", "qiniu/ci-runner", ""}} {
		if err := db.Exec(`INSERT INTO runner_profiles VALUES (?, '["qiniu","ubuntu"]','["qiniu","ubuntu"]','old-id',?,'group',7,2,91,0,1,?,1,?,?)`, row.name, row.template, row.owner, now, now).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Exec(`CREATE INDEX legacy_profile_index ON runner_profiles(max_concurrency)`).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)
	public, err := store.GetProfile("public")
	if err != nil {
		t.Fatal(err)
	}
	if public.TemplateSource != TemplateSourcePublic || !public.Published || public.TemplateID != "" {
		t.Fatalf("legacy public policy: %#v", public)
	}
	if public.Enabled || public.MaxConcurrency != 7 || public.MinIdle != 2 || public.Priority != 91 || public.RunnerGroup != "group" || !public.UpdatedAt.Equal(now) || !public.CreatedAt.Equal(now) {
		t.Fatalf("operator controls changed: %#v", public)
	}
	private, err := store.GetProfile("private")
	if err != nil {
		t.Fatal(err)
	}
	if private.TemplateSource != TemplateSourcePrivate || private.Published || private.TemplateID != "old-id" || private.DefaultTemplateName != "" || !private.UpdatedAt.Equal(now) {
		t.Fatalf("legacy private policy: %#v", private)
	}
	private.MaxConcurrency = 8
	if _, err := store.UpsertProfileIfUnchanged(private, &private.UpdatedAt); err != nil {
		t.Fatalf("migrated private spec cannot be edited: %v", err)
	}
	// An incomplete historical managed row must not become a runnable private spec.
	incomplete, err := store.GetProfile("legacy-incomplete")
	if err != nil || incomplete.TemplateSource != TemplateSourcePublic || incomplete.Published || incomplete.DefaultTemplateName != "" {
		t.Fatalf("incomplete legacy binding changed: %#v %v", incomplete, err)
	}
	events, err := store.ListAuditEvents(100)
	if err != nil || len(events) != 3 {
		t.Fatalf("migration audit: %#v %v", events, err)
	}
	db, err = store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasIndex(&runnerProfileRecord{}, "legacy_profile_index") {
		t.Fatal("legacy index lost")
	}
	public.Published = false
	public, err = store.UpsertProfile(public)
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)
	restarted := NewWithOptions(store.opts).(*DBStore)
	got, err := restarted.GetProfile("public")
	if err != nil {
		t.Fatal(err)
	}
	if got.Published || !got.UpdatedAt.Equal(public.UpdatedAt) {
		t.Fatalf("restart restored legacy policy: %#v", got)
	}
	events, err = restarted.ListAuditEvents(100)
	if err != nil || len(events) != 3 {
		t.Fatalf("migration repeated: %#v %v", events, err)
	}
	if err := restarted.DeleteProfile("public"); err != nil {
		t.Fatal(err)
	}
	db, err = restarted.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)
	again := NewWithOptions(store.opts)
	if _, err := again.GetProfile("public"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted profile reseeded: %v", err)
	}
}

func TestProfilePolicyRejectsInvalidBindings(t *testing.T) {
	for _, p := range []RunnerProfile{
		{TemplateSource: TemplateSourcePublic, DefaultTemplateName: "public", TemplateID: "region-id"},
		{TemplateSource: TemplateSourcePublic},
		{TemplateSource: "unknown"},
	} {
		p.Name = "spec"
		p.Labels = []string{"qiniu"}
		if _, err := New(t.TempDir()).UpsertProfile(p); err == nil {
			t.Fatalf("invalid policy accepted: %#v", p)
		}
	}
}

func TestRunnerProfilePolicyMigrationRollsBackWhenAuditFails(t *testing.T) {
	for _, owner := range []string{"qiniu/ci-runner", ""} {
		t.Run("owner="+owner, func(t *testing.T) {
			store := New(t.TempDir()).(*DBStore)
			db, err := store.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			// Raw rows model pre-policy public and private bindings. Each
			// binding change must roll back when its audit insert fails.
			if err := db.Create(&runnerProfileRecord{Name: "legacy", LabelsJSON: `["qiniu"]`, TemplateID: "old", DefaultTemplateName: "public-name", ManagedBy: owner, CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Exec(`CREATE TRIGGER reject_policy_audit BEFORE INSERT ON audit_events BEGIN SELECT RAISE(ABORT, 'audit failure'); END`).Error; err != nil {
				t.Fatal(err)
			}
			if err := migrateRunnerProfilePolicies(db); err == nil {
				t.Fatal("audit failure allowed migration")
			}
			var row runnerProfileRecord
			if err := db.First(&row, "name = ?", "legacy").Error; err != nil {
				t.Fatal(err)
			}
			if row.TemplateSource != "" || row.Published || row.TemplateID != "old" || row.DefaultTemplateName != "public-name" {
				t.Fatalf("partial migration persisted: %#v", row)
			}
		})
	}
}

func TestRunnerProfilePolicySQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("dedicated SQL backend tests disabled")
	}
	for _, backend := range []struct{ name, dsn string }{{BackendPostgres, os.Getenv("RUNNERD_POSTGRES_TEST_DSN")}, {BackendMySQL, os.Getenv("RUNNERD_MYSQL_TEST_DSN")}} {
		t.Run(backend.name, func(t *testing.T) {
			if backend.dsn == "" {
				t.Fatal("dedicated test DSN required")
			}
			store := NewWithOptions(Options{Backend: backend.name, DatabaseDSN: backend.dsn, MigrateOnStart: false}).(*DBStore)
			db, err := store.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			requireCatalogMatcherTestDatabase(t, db, backend.name)
			resetSQLBackendTestTables(t, db)
			defer func() { resetSQLBackendTestTables(t, db); closeTestDB(t, db) }()
			if err := store.migrate(db); err != nil {
				t.Fatal(err)
			}
			addRetiredRunnerPolicyColumns(t, db)
			// Model an upgrade from a schema changed before this process started.
			// Reopen so PostgreSQL does not reuse a pre-ALTER SELECT * plan.
			closeTestDB(t, db)
			store = NewWithOptions(store.opts).(*DBStore)
			db, err = store.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			stamp := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
			required := `["qiniu"]`
			row := runnerProfileRecord{Name: "legacy", LabelsJSON: `["qiniu","ubuntu"]`, RequiredLabelsJSON: &required, DefaultTemplateName: "public-name", TemplateID: "obsolete-id", ManagedBy: "qiniu/ci-runner", MaxConcurrency: 7, Enabled: true, CreatedAt: stamp, UpdatedAt: stamp}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			private := runnerProfileRecord{Name: "legacy-private", LabelsJSON: `["custom"]`, TemplateID: "private-id", DefaultTemplateName: "stray-name", MaxConcurrency: 4, CreatedAt: stamp, UpdatedAt: stamp}
			if err := db.Create(&private).Error; err != nil {
				t.Fatal(err)
			}
			if err := migrateRunnerProfilePolicies(db); err != nil {
				t.Fatal(err)
			}
			migratedPrivate, err := store.GetProfile(private.Name)
			if err != nil || migratedPrivate.TemplateSource != TemplateSourcePrivate || migratedPrivate.Published || migratedPrivate.TemplateID != "private-id" || migratedPrivate.DefaultTemplateName != "" || migratedPrivate.MaxConcurrency != 4 || !migratedPrivate.CreatedAt.Equal(stamp) || !migratedPrivate.UpdatedAt.Equal(stamp) {
				t.Fatalf("private migration mismatch: %#v %v", migratedPrivate, err)
			}
			migratedPrivate.MaxConcurrency = 5
			if _, err := store.UpsertProfileIfUnchanged(migratedPrivate, &migratedPrivate.UpdatedAt); err != nil {
				t.Fatalf("migrated private spec cannot be edited: %v", err)
			}
			var got runnerProfileRecord
			if err := db.First(&got, "name = ?", row.Name).Error; err != nil {
				t.Fatal(err)
			}
			if got.TemplateSource != TemplateSourcePublic || !got.Published || got.TemplateID != "" || got.MaxConcurrency != 7 || !got.UpdatedAt.Equal(stamp) {
				t.Fatalf("migration mismatch: %#v", got)
			}
			if err := db.Model(&got).Updates(map[string]any{"published": false, "max_concurrency": 3}).Error; err != nil {
				t.Fatal(err)
			}
			if err := store.migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := db.First(&got, "name = ?", row.Name).Error; err != nil {
				t.Fatal(err)
			}
			if got.Published || got.MaxConcurrency != 3 {
				t.Fatalf("restart restored policy: %#v", got)
			}
			assertRetiredRunnerPolicyValues(t, db)
			var count int64
			if err := db.Model(&auditEventRecord{}).Where("action = ?", "profile.policy_migrate").Count(&count).Error; err != nil || count != 2 {
				t.Fatalf("migration audit count %d: %v", count, err)
			}
			if err := db.Delete(&runnerProfileRecord{}, "name = ?", row.Name).Error; err != nil {
				t.Fatal(err)
			}
			if err := db.Delete(&runnerProfileRecord{}, "name = ?", private.Name).Error; err != nil {
				t.Fatal(err)
			}
			if err := store.migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := db.Model(&runnerProfileRecord{}).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("deleted row restored %d: %v", count, err)
			}
		})
	}
}

func TestGlobalProfileActiveGuardExcludesScopedAttempts(t *testing.T) {
	store := New(t.TempDir())
	for _, source := range []string{"", "global", "scoped_custom"} {
		id := "request-" + source
		if _, _, err := store.CreateRequest(RunnerRequest{ID: id, RunnerName: id, ProfileName: "same-name", ProfileSource: source, ProfileScopeType: AccountScopeTypeAccount, ProfileScopeID: 7, Labels: []string{"qiniu"}}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if count, err := store.ActiveCountForProfile("same-name"); err != nil || count != 2 {
		t.Fatalf("global active attempts %d: %v", count, err)
	}
}

func TestPublishedIDReferenceSurvivesRestart(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	p, err := store.UpsertProfile(RunnerProfile{Name: "public-id", Labels: []string{"qiniu"}, TemplateID: "fixed-id", Published: true, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)
	got, err := NewWithOptions(store.opts).GetProfile(p.Name)
	if err != nil || !got.Published || got.TemplateSource != TemplateSourcePrivate || got.TemplateID != p.TemplateID || !got.UpdatedAt.Equal(p.UpdatedAt) {
		t.Fatalf("publication changed reference/runtime on restart: %#v %v", got, err)
	}
}
