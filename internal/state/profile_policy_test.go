package state

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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
	for _, row := range []struct{ name, owner, template string }{{"public", "qiniu/ci-runner", "public-name"}, {"private", "", ""}} {
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
	if public.TemplateSource != TemplateSourcePublic || !public.Published || !public.ForkSponsorship || !public.RequireDocker || public.RunnerUpdatePolicy != RunnerUpdatePreinstalled || public.TemplateID != "" {
		t.Fatalf("legacy public policy: %#v", public)
	}
	if public.Enabled || public.MaxConcurrency != 7 || public.MinIdle != 2 || public.Priority != 91 || public.RunnerGroup != "group" || !public.UpdatedAt.Equal(now) || !public.CreatedAt.Equal(now) {
		t.Fatalf("operator controls changed: %#v", public)
	}
	private, err := store.GetProfile("private")
	if err != nil {
		t.Fatal(err)
	}
	if private.TemplateSource != TemplateSourcePrivate || private.Published || private.TemplateID != "old-id" || private.RunnerUpdatePolicy != RunnerUpdateOfficial {
		t.Fatalf("legacy private policy: %#v", private)
	}
	events, err := store.ListAuditEvents(100)
	if err != nil || len(events) != 2 {
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
	public.RequireDocker = false
	public.ForkSponsorship = false
	public.RunnerUpdatePolicy = RunnerUpdateOfficial
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
	if got.Published || got.RequireDocker || got.ForkSponsorship || got.RunnerUpdatePolicy != RunnerUpdateOfficial || !got.UpdatedAt.Equal(public.UpdatedAt) {
		t.Fatalf("restart restored legacy policy: %#v", got)
	}
	events, err = restarted.ListAuditEvents(100)
	if err != nil || len(events) != 2 {
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

func TestProfilePolicyRejectsPrivatePublicationAndInvalidModes(t *testing.T) {
	for _, p := range []RunnerProfile{
		{TemplateSource: TemplateSourcePrivate, Published: true},
		{TemplateSource: TemplateSourcePrivate, ForkSponsorship: true},
		{TemplateSource: TemplateSourcePublic, DefaultTemplateName: "public", TemplateID: "region-id"},
		{TemplateSource: TemplateSourcePublic},
		{TemplateSource: "unknown"},
		{TemplateSource: TemplateSourcePrivate, RunnerUpdatePolicy: "unknown"},
	} {
		p.Name = "spec"
		p.Labels = []string{"qiniu"}
		if _, err := New(t.TempDir()).UpsertProfile(p); err == nil {
			t.Fatalf("invalid policy accepted: %#v", p)
		}
	}
}

func TestOmittedTemplateSourcePreservesExplicitRunnerUpdatePolicy(t *testing.T) {
	for _, owner := range []string{"", "qiniu/ci-runner"} {
		for _, policy := range []string{RunnerUpdateOfficial, RunnerUpdatePreinstalled, "invalid"} {
			profile := NormalizeProfilePolicy(RunnerProfile{ManagedBy: owner, RunnerUpdatePolicy: policy})
			if profile.RunnerUpdatePolicy != policy {
				t.Fatalf("owner %q: policy %q overwritten with %q", owner, policy, profile.RunnerUpdatePolicy)
			}
		}
	}
	store := New(t.TempDir())
	if _, err := store.UpsertProfile(RunnerProfile{Name: "invalid", Labels: []string{"qiniu"}, TemplateID: "private-id", RunnerUpdatePolicy: "invalid"}); err == nil {
		t.Fatal("omitted template source allowed invalid preparation policy")
	}
}

func TestRunnerProfilePolicyMigrationRollsBackWhenAuditFails(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	// Raw rows model a pre-policy database; migration must not partially publish one.
	if err := db.Create(&runnerProfileRecord{Name: "legacy", LabelsJSON: `["qiniu"]`, TemplateID: "old", DefaultTemplateName: "public-name", ManagedBy: "qiniu/ci-runner", CreatedAt: time.Now(), UpdatedAt: time.Now()}).Error; err != nil {
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
	if row.TemplateSource != "" || row.Published || row.TemplateID != "old" {
		t.Fatalf("partial migration persisted: %#v", row)
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
			stamp := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
			required := `["qiniu"]`
			row := runnerProfileRecord{Name: "legacy", LabelsJSON: `["qiniu","ubuntu"]`, RequiredLabelsJSON: &required, DefaultTemplateName: "public-name", TemplateID: "obsolete-id", ManagedBy: "qiniu/ci-runner", MaxConcurrency: 7, Enabled: true, CreatedAt: stamp, UpdatedAt: stamp}
			if err := db.Create(&row).Error; err != nil {
				t.Fatal(err)
			}
			if err := migrateRunnerProfilePolicies(db); err != nil {
				t.Fatal(err)
			}
			var got runnerProfileRecord
			if err := db.First(&got, "name = ?", row.Name).Error; err != nil {
				t.Fatal(err)
			}
			if got.TemplateSource != TemplateSourcePublic || !got.Published || got.TemplateID != "" || !got.ForkSponsorship || !got.RequireDocker || got.RunnerUpdatePolicy != RunnerUpdatePreinstalled || got.MaxConcurrency != 7 || !got.UpdatedAt.Equal(stamp) {
				t.Fatalf("migration mismatch: %#v", got)
			}
			if err := db.Model(&got).Updates(map[string]any{"published": false, "require_docker": false, "runner_update_policy": RunnerUpdateOfficial}).Error; err != nil {
				t.Fatal(err)
			}
			if err := store.migrate(db); err != nil {
				t.Fatal(err)
			}
			if err := db.First(&got, "name = ?", row.Name).Error; err != nil {
				t.Fatal(err)
			}
			if got.Published || got.RequireDocker || got.RunnerUpdatePolicy != RunnerUpdateOfficial {
				t.Fatalf("restart restored policy: %#v", got)
			}
			var count int64
			if err := db.Model(&auditEventRecord{}).Where("action = ?", "profile.policy_migrate").Count(&count).Error; err != nil || count != 1 {
				t.Fatalf("migration audit count %d: %v", count, err)
			}
			if err := db.Delete(&runnerProfileRecord{}, "name = ?", row.Name).Error; err != nil {
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
