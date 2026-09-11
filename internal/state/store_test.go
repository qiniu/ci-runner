package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
)

func closeTestDB(t *testing.T, db *gorm.DB) {
	t.Helper()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
}

// productionRunnerCatalogFixture contains only the non-secret fields that
// influence the runner-spec matcher. It is deliberately independent from the
// state records so the expected catalog cannot be derived from the code under
// test.
type productionRunnerCatalogFixture struct {
	Profiles                    []productionRunnerCatalogProfile `json:"profiles"`
	BriefCompatibilityLabelSets [][]string                       `json:"brief_compatibility_label_sets"`
}

type productionRunnerCatalogProfile struct {
	Name           string   `json:"name"`
	Labels         []string `json:"labels"`
	RequiredLabels []string `json:"required_labels"`
	RunnerGroup    string   `json:"runner_group"`
	Priority       int      `json:"priority"`
	Enabled        bool     `json:"enabled"`
}

func loadProductionRunnerCatalogFixture(t *testing.T) productionRunnerCatalogFixture {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "runner-catalog-production-2026-08-13.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture productionRunnerCatalogFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Profiles) != 10 {
		t.Fatalf("production catalog profile count = %d, want 10", len(fixture.Profiles))
	}
	return fixture
}

func loadProductionRunnerCatalog(t *testing.T, store Store) productionRunnerCatalogFixture {
	t.Helper()
	fixture := loadProductionRunnerCatalogFixture(t)
	for _, profile := range fixture.Profiles {
		if _, err := store.UpsertProfile(RunnerProfile{
			Name:           profile.Name,
			Labels:         append([]string(nil), profile.Labels...),
			RequiredLabels: append([]string(nil), profile.RequiredLabels...),
			RunnerGroup:    profile.RunnerGroup,
			TemplateID:     "fixture-template-" + profile.Name,
			MaxConcurrency: 1,
			Priority:       profile.Priority,
			Enabled:        profile.Enabled,
		}); err != nil {
			t.Fatalf("upsert fixture profile %q: %v", profile.Name, err)
		}
	}
	return fixture
}

func managedProfileForReconciliation(name string, revision int) RunnerProfile {
	return RunnerProfile{
		Name:                name,
		Labels:              []string{"self-hosted", "linux", "x64", "qiniu", "ubuntu-24.04"},
		RequiredLabels:      []string{"qiniu", "ubuntu-24.04"},
		DefaultTemplateName: "github-runner-ubuntu-24-04",
		MaxConcurrency:      10,
		Priority:            100,
		Enabled:             true,
		ManagedBy:           "qiniu/ci-runner",
		CatalogRevision:     revision,
	}
}

type profileReadMutation struct {
	fired bool
	err   error
}

func mutateProfileAfterReconciliationRead(
	t *testing.T,
	store *DBStore,
	profileName string,
	updates map[string]any,
) *profileReadMutation {
	t.Helper()
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	callbackName := "test:mutate-profile-after-reconciliation-read:" + profileName
	mutation := &profileReadMutation{}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(callbackName)
	})
	if err := db.Callback().Query().After("gorm:query").Register(callbackName, func(query *gorm.DB) {
		if mutation.fired {
			return
		}
		record, ok := query.Statement.Dest.(*runnerProfileRecord)
		if !ok || record.Name != profileName {
			return
		}
		mutation.fired = true
		result := query.Session(&gorm.Session{NewDB: true}).
			Model(&runnerProfileRecord{}).
			Where("name = ?", profileName).
			Updates(updates)
		mutation.err = result.Error
	}); err != nil {
		t.Fatal(err)
	}
	return mutation
}

func TestReconcileManagedProfilesCreatesMissingProfile(t *testing.T) {
	store := New(t.TempDir())
	want := managedProfileForReconciliation("qiniu-ubuntu-24.04", 1)

	conflicts, err := store.ReconcileManagedProfiles([]RunnerProfile{want})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want none", conflicts)
	}
	got, err := store.GetProfile(want.Name)
	if err != nil {
		t.Fatal(err)
	}
	want.CreatedAt = got.CreatedAt
	want.UpdatedAt = got.UpdatedAt
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("created profile = %#v, want %#v", got, want)
	}
}

func TestReconcileManagedProfilesRejectsPathUnsafeName(t *testing.T) {
	for _, name := range []string{"managed/spec", ".", ".."} {
		t.Run(name, func(t *testing.T) {
			store := New(t.TempDir())
			valid := managedProfileForReconciliation("qiniu-ubuntu-24.04", 1)
			invalid := managedProfileForReconciliation(name, 1)

			if _, err := store.ReconcileManagedProfiles([]RunnerProfile{valid, invalid}); err == nil ||
				!strings.Contains(err.Error(), "profile name must not contain '/' or be '.' or '..'") {
				t.Fatalf("ReconcileManagedProfiles(%q) error = %v, want path-safe name rejection", name, err)
			}
			if _, err := store.GetProfile(valid.Name); !errors.Is(err, ErrNotFound) {
				t.Fatalf("valid profile survived rejected reconciliation: %v", err)
			}
			if _, err := store.GetProfile(name); !errors.Is(err, ErrNotFound) {
				t.Fatalf("path-unsafe profile was reconciled: %v", err)
			}
			events, err := store.ListAuditEvents(10)
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 0 {
				t.Fatalf("rejected reconciliation audit events = %#v, want none", events)
			}
		})
	}
}

func TestLegacyPathUnsafeProfileRemainsReadableAndMatchable(t *testing.T) {
	for _, legacyName := range []string{"legacy/unsafe", ".", ".."} {
		t.Run(legacyName, func(t *testing.T) {
			databaseURL := filepath.Join(t.TempDir(), "runnerd.db")
			store := NewWithOptions(Options{
				Backend:        BackendSQLite,
				DatabaseDSN:    databaseURL,
				MigrateOnStart: true,
			}).(*DBStore)
			created, err := store.UpsertProfile(RunnerProfile{
				Name:           "legacy-safe-name",
				Labels:         []string{"self-hosted", "legacy-path"},
				RequiredLabels: []string{"legacy-path"},
				TemplateID:     "legacy-template",
				MaxConcurrency: 2,
				Priority:       200,
				Enabled:        true,
			})
			if err != nil {
				t.Fatal(err)
			}
			db, err := store.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			result := db.Model(&runnerProfileRecord{}).
				Where("name = ?", created.Name).
				UpdateColumn("name", legacyName)
			if result.Error != nil {
				t.Fatal(result.Error)
			}
			if result.RowsAffected != 1 {
				t.Fatalf("renamed legacy fixture rows = %d, want 1", result.RowsAffected)
			}
			closeTestDB(t, db)

			restarted := NewWithOptions(Options{
				Backend:        BackendSQLite,
				DatabaseDSN:    databaseURL,
				MigrateOnStart: true,
			}).(*DBStore)
			if err := restarted.Ensure(); err != nil {
				t.Fatalf("restart with legacy path-unsafe profile: %v", err)
			}
			managed := managedProfileForReconciliation("qiniu-ubuntu-24.04", 1)
			if conflicts, err := restarted.ReconcileManagedProfiles([]RunnerProfile{managed}); err != nil || len(conflicts) != 0 {
				t.Fatalf("reconcile with legacy path-unsafe profile: conflicts=%#v error=%v", conflicts, err)
			}

			got, err := restarted.GetProfile(legacyName)
			if err != nil {
				t.Fatalf("GetProfile(%q): %v", legacyName, err)
			}
			if got.Name != legacyName || got.TemplateID != created.TemplateID ||
				!reflect.DeepEqual(got.Labels, created.Labels) ||
				!reflect.DeepEqual(got.RequiredLabels, created.RequiredLabels) ||
				!got.CreatedAt.Equal(created.CreatedAt) || !got.UpdatedAt.Equal(created.UpdatedAt) {
				t.Fatalf("legacy profile changed across restart/reconciliation: got=%#v created=%#v", got, created)
			}
			profiles, err := restarted.ListProfiles()
			if err != nil {
				t.Fatal(err)
			}
			if len(profiles) != 2 {
				t.Fatalf("profiles after reconciliation = %#v, want legacy and managed profiles", profiles)
			}
			match, err := restarted.MatchProfile("owner/repo", []string{"legacy-path"})
			if err != nil {
				t.Fatal(err)
			}
			if match.Profile == nil || match.Profile.Name != legacyName {
				t.Fatalf("legacy path-unsafe profile match = %#v, want %q", match, legacyName)
			}
			events, err := restarted.ListAuditEvents(10)
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range events {
				if event.ResourceID == legacyName {
					t.Fatalf("legacy profile produced an automatic repair audit event: %#v", event)
				}
			}
		})
	}
}

func TestReconcileManagedProfilesUpdatesCatalogFieldsAndPreservesOperatorFields(t *testing.T) {
	store := New(t.TempDir())
	createdAt := time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	existing := managedProfileForReconciliation("qiniu-ubuntu-24.04", 1)
	existing.Labels = []string{"old"}
	existing.RequiredLabels = []string{"old"}
	existing.TemplateID = "old-region-specific-id"
	existing.DefaultTemplateName = "old-template-name"
	existing.RunnerGroup = "old-group"
	existing.MaxConcurrency = 37
	existing.MinIdle = 4
	existing.Priority = 12
	existing.Enabled = false
	existing.CreatedAt = createdAt
	if _, err := store.UpsertProfile(existing); err != nil {
		t.Fatal(err)
	}

	catalog := managedProfileForReconciliation(existing.Name, 2)
	conflicts, err := store.ReconcileManagedProfiles([]RunnerProfile{catalog})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want none", conflicts)
	}
	got, err := store.GetProfile(existing.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Labels, catalog.Labels) ||
		!reflect.DeepEqual(got.RequiredLabels, catalog.RequiredLabels) ||
		got.TemplateID != catalog.TemplateID ||
		got.DefaultTemplateName != catalog.DefaultTemplateName ||
		got.RunnerGroup != catalog.RunnerGroup ||
		got.Priority != catalog.Priority ||
		got.ManagedBy != catalog.ManagedBy ||
		got.CatalogRevision != catalog.CatalogRevision {
		t.Fatalf("catalog-controlled fields were not updated: %#v", got)
	}
	if got.Enabled != existing.Enabled ||
		got.MaxConcurrency != existing.MaxConcurrency ||
		got.MinIdle != existing.MinIdle ||
		!got.CreatedAt.Equal(createdAt) {
		t.Fatalf("operator-controlled or creation fields changed: %#v", got)
	}
}

func TestReconcileManagedProfilesDoesNotDowngradeHigherRevision(t *testing.T) {
	store := New(t.TempDir())
	existing := managedProfileForReconciliation("qiniu-ubuntu-24.04", 7)
	existing.Labels = []string{"higher"}
	existing.RequiredLabels = []string{"higher"}
	existing.TemplateID = "higher-template-id"
	existing.DefaultTemplateName = "higher-template-name"
	existing.RunnerGroup = "higher-group"
	existing.MaxConcurrency = 47
	existing.MinIdle = 6
	existing.Priority = 700
	existing.Enabled = false
	existing.CreatedAt = time.Now().UTC().Add(-time.Hour).Truncate(time.Millisecond)
	if _, err := store.UpsertProfile(existing); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetProfile(existing.Name)
	if err != nil {
		t.Fatal(err)
	}

	conflicts, err := store.ReconcileManagedProfiles([]RunnerProfile{
		managedProfileForReconciliation(existing.Name, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want none", conflicts)
	}
	after, err := store.GetProfile(existing.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("higher revision was changed:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestReconcileManagedProfilesAtomicUpdateRejectsStaleRevision(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	existing := managedProfileForReconciliation("qiniu-ubuntu-24.04", 1)
	if _, err := store.UpsertProfile(existing); err != nil {
		t.Fatal(err)
	}
	beforeRace, err := store.GetProfile(existing.Name)
	if err != nil {
		t.Fatal(err)
	}
	raceUpdatedAt := beforeRace.UpdatedAt.Add(time.Minute)
	mutation := mutateProfileAfterReconciliationRead(t, store, existing.Name, map[string]any{
		"labels_json":           `["higher-race"]`,
		"required_labels_json":  `["higher-race"]`,
		"template_id":           "higher-race-template-id",
		"default_template_name": "higher-race-template-name",
		"runner_group":          "higher-race-group",
		"max_concurrency":       57,
		"min_idle":              7,
		"priority":              800,
		"enabled":               false,
		"default_available":     false,
		"managed_by":            "qiniu/ci-runner",
		"catalog_revision":      8,
		"updated_at":            raceUpdatedAt,
	})

	conflicts, err := store.ReconcileManagedProfiles([]RunnerProfile{
		managedProfileForReconciliation(existing.Name, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if mutation.err != nil {
		t.Fatalf("inject stale-read mutation: %v", mutation.err)
	}
	if !mutation.fired {
		t.Fatal("stale-read mutation did not run")
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want higher revision no-op", conflicts)
	}
	got, err := store.GetProfile(existing.Name)
	if err != nil {
		t.Fatal(err)
	}
	want := RunnerProfile{
		Name:                existing.Name,
		Labels:              []string{"higher-race"},
		RequiredLabels:      []string{"higher-race"},
		TemplateID:          "higher-race-template-id",
		DefaultTemplateName: "higher-race-template-name",
		RunnerGroup:         "higher-race-group",
		MaxConcurrency:      57,
		MinIdle:             7,
		Priority:            800,
		Enabled:             false,
		ManagedBy:           "qiniu/ci-runner",
		CatalogRevision:     8,
		CreatedAt:           beforeRace.CreatedAt,
		UpdatedAt:           raceUpdatedAt,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stale lower-revision write changed raced row:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestReconcileManagedProfilesAtomicUpdateRejectsStaleOwner(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	existing := managedProfileForReconciliation("qiniu-ubuntu-24.04", 1)
	if _, err := store.UpsertProfile(existing); err != nil {
		t.Fatal(err)
	}
	beforeRace, err := store.GetProfile(existing.Name)
	if err != nil {
		t.Fatal(err)
	}
	raceUpdatedAt := beforeRace.UpdatedAt.Add(time.Minute)
	mutation := mutateProfileAfterReconciliationRead(t, store, existing.Name, map[string]any{
		"labels_json":           `["other-owner"]`,
		"required_labels_json":  `["other-owner"]`,
		"template_id":           "other-owner-template-id",
		"default_template_name": "other-owner-template-name",
		"runner_group":          "other-owner-group",
		"max_concurrency":       67,
		"min_idle":              8,
		"priority":              900,
		"enabled":               false,
		"default_available":     false,
		"managed_by":            "another/catalog",
		"catalog_revision":      9,
		"updated_at":            raceUpdatedAt,
	})

	conflicts, err := store.ReconcileManagedProfiles([]RunnerProfile{
		managedProfileForReconciliation(existing.Name, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	if mutation.err != nil {
		t.Fatalf("inject stale-read mutation: %v", mutation.err)
	}
	if !mutation.fired {
		t.Fatal("stale-read mutation did not run")
	}
	wantConflicts := []ManagedProfileConflict{{
		Name:              existing.Name,
		ExistingManagedBy: "another/catalog",
	}}
	if !reflect.DeepEqual(conflicts, wantConflicts) {
		t.Fatalf("conflicts = %#v, want %#v", conflicts, wantConflicts)
	}
	got, err := store.GetProfile(existing.Name)
	if err != nil {
		t.Fatal(err)
	}
	want := RunnerProfile{
		Name:                existing.Name,
		Labels:              []string{"other-owner"},
		RequiredLabels:      []string{"other-owner"},
		TemplateID:          "other-owner-template-id",
		DefaultTemplateName: "other-owner-template-name",
		RunnerGroup:         "other-owner-group",
		MaxConcurrency:      67,
		MinIdle:             8,
		Priority:            900,
		Enabled:             false,
		ManagedBy:           "another/catalog",
		CatalogRevision:     9,
		CreatedAt:           beforeRace.CreatedAt,
		UpdatedAt:           raceUpdatedAt,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stale owner write changed raced row:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestReconcileManagedProfilesReportsCollisionAndContinues(t *testing.T) {
	store := New(t.TempDir())
	custom := RunnerProfile{
		Name:           "qiniu-ubuntu-24.04",
		Labels:         []string{"custom"},
		RequiredLabels: []string{},
		TemplateID:     "custom-template",
		MaxConcurrency: 2,
		Enabled:        true,
	}
	if _, err := store.UpsertProfile(custom); err != nil {
		t.Fatal(err)
	}
	missing := managedProfileForReconciliation("qiniu-ubuntu-22.04", 1)

	conflicts, err := store.ReconcileManagedProfiles([]RunnerProfile{
		managedProfileForReconciliation(custom.Name, 1),
		missing,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantConflicts := []ManagedProfileConflict{{
		Name:              custom.Name,
		ExistingManagedBy: "",
	}}
	if !reflect.DeepEqual(conflicts, wantConflicts) {
		t.Fatalf("conflicts = %#v, want %#v", conflicts, wantConflicts)
	}
	gotCustom, err := store.GetProfile(custom.Name)
	if err != nil {
		t.Fatal(err)
	}
	custom.CreatedAt = gotCustom.CreatedAt
	custom.UpdatedAt = gotCustom.UpdatedAt
	if !reflect.DeepEqual(gotCustom, custom) {
		t.Fatalf("custom collision row changed: %#v", gotCustom)
	}
	if _, err := store.GetProfile(missing.Name); err != nil {
		t.Fatalf("non-conflicting managed profile was not reconciled: %v", err)
	}
}

func TestReconcileManagedProfilesReportsOtherManagerCollision(t *testing.T) {
	store := New(t.TempDir())
	existing := managedProfileForReconciliation("qiniu-ubuntu-24.04", 1)
	existing.ManagedBy = "another/catalog"
	if _, err := store.UpsertProfile(existing); err != nil {
		t.Fatal(err)
	}

	conflicts, err := store.ReconcileManagedProfiles([]RunnerProfile{
		managedProfileForReconciliation(existing.Name, 2),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []ManagedProfileConflict{{
		Name:              existing.Name,
		ExistingManagedBy: "another/catalog",
	}}
	if !reflect.DeepEqual(conflicts, want) {
		t.Fatalf("conflicts = %#v, want %#v", conflicts, want)
	}
	got, err := store.GetProfile(existing.Name)
	if err != nil {
		t.Fatal(err)
	}
	if got.ManagedBy != existing.ManagedBy || got.CatalogRevision != existing.CatalogRevision {
		t.Fatalf("other manager row changed: %#v", got)
	}
}

func TestReconcileManagedProfilesIsIdempotent(t *testing.T) {
	store := New(t.TempDir())
	profile := managedProfileForReconciliation("qiniu-ubuntu-24.04", 1)
	if _, err := store.ReconcileManagedProfiles([]RunnerProfile{profile}); err != nil {
		t.Fatal(err)
	}
	before, err := store.GetProfile(profile.Name)
	if err != nil {
		t.Fatal(err)
	}

	conflicts, err := store.ReconcileManagedProfiles([]RunnerProfile{profile})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %#v, want none", conflicts)
	}
	after, err := store.GetProfile(profile.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("idempotent reconciliation changed profile:\nbefore: %#v\nafter:  %#v", before, after)
	}
}

func TestReconcileManagedProfilesRollsBackAllChangesOnError(t *testing.T) {
	store := New(t.TempDir())
	valid := managedProfileForReconciliation("qiniu-ubuntu-24.04", 1)
	invalid := managedProfileForReconciliation("qiniu-ubuntu-invalid", 1)
	invalid.RequiredLabels = []string{"missing"}

	if _, err := store.ReconcileManagedProfiles([]RunnerProfile{valid, invalid}); err == nil {
		t.Fatal("expected invalid managed profile to fail reconciliation")
	}
	if _, err := store.GetProfile(valid.Name); !errors.Is(err, ErrNotFound) {
		t.Fatalf("first profile survived failed transaction: %v", err)
	}
}

func TestSQLiteStoreUsesWALAndBusyTimeout(t *testing.T) {
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    t.TempDir() + "/runnerd.db",
		MigrateOnStart: true,
	}).(*DBStore)
	if err := store.Ensure(); err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}

	var journalMode string
	if err := db.Raw("PRAGMA journal_mode").Scan(&journalMode).Error; err != nil {
		t.Fatal(err)
	}
	if journalMode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", journalMode)
	}

	var busyTimeout int
	if err := db.Raw("PRAGMA busy_timeout").Scan(&busyTimeout).Error; err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 15000 {
		t.Fatalf("busy_timeout = %d, want 15000", busyTimeout)
	}

	var foreignKeys int
	if err := db.Raw("PRAGMA foreign_keys").Scan(&foreignKeys).Error; err != nil {
		t.Fatal(err)
	}
	if foreignKeys != 1 {
		t.Fatalf("foreign_keys = %d, want 1", foreignKeys)
	}
}

func TestMySQLStoreBackendIsRecognized(t *testing.T) {
	store := NewWithOptions(Options{
		Backend:        "mysql",
		DatabaseDSN:    "not a valid mysql dsn",
		MigrateOnStart: false,
	}).(*DBStore)

	err := store.Ensure()
	if err == nil {
		t.Fatal("expected invalid mysql DSN to fail")
	}
	if strings.Contains(err.Error(), "unsupported state backend") {
		t.Fatalf("mysql backend should be recognized, got %v", err)
	}
}

func TestMySQLDSNWithParseTime(t *testing.T) {
	dsn, err := mysqlDSNWithParseTime("runner:secret@tcp(mysql.example:3306)/runnerd")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "parseTime=true") {
		t.Fatalf("expected parseTime=true in DSN, got %q", dsn)
	}

	dsn, err = mysqlDSNWithParseTime("runner:secret@tcp(mysql.example:3306)/runnerd?parseTime=false&charset=utf8mb4")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(dsn, "parseTime=true") || !strings.Contains(dsn, "charset=utf8mb4") {
		t.Fatalf("expected parseTime=true with existing params preserved, got %q", dsn)
	}
}

func TestSandboxServiceDefaultLifecycle(t *testing.T) {
	store := New(t.TempDir())

	if _, err := store.GetSandboxServiceDefault(); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetSandboxServiceDefault() error = %v, want ErrNotFound", err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	saved, err := store.UpsertSandboxServiceDefault(SandboxServiceDefault{
		Enabled:         true,
		APIURL:          "https://sandbox.example.test",
		APIKeyEncrypted: "encrypted-key",
		APIKeyUpdatedAt: &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.ID != 1 || !saved.Enabled || saved.APIKeyEncrypted != "encrypted-key" {
		t.Fatalf("unexpected saved default: %#v", saved)
	}

	saved, err = store.UpsertSandboxServiceDefault(SandboxServiceDefault{
		Enabled: false,
		APIURL:  "https://sandbox.example.test/v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Enabled || saved.APIURL != "https://sandbox.example.test/v2" || saved.APIKeyEncrypted != "encrypted-key" {
		t.Fatalf("expected update to preserve encrypted key: %#v", saved)
	}
	if saved.APIKeyUpdatedAt == nil || !saved.APIKeyUpdatedAt.Equal(now) {
		t.Fatalf("expected update to preserve key timestamp: %#v", saved.APIKeyUpdatedAt)
	}

	if err := store.DeleteSandboxServiceDefaultAPIKey(); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSandboxServiceDefaultAPIKey(); err != nil {
		t.Fatalf("expected key deletion to be idempotent: %v", err)
	}
	saved, err = store.GetSandboxServiceDefault()
	if err != nil {
		t.Fatal(err)
	}
	if saved.APIKeyEncrypted != "" || saved.APIKeyUpdatedAt != nil || saved.APIURL == "" {
		t.Fatalf("expected only API key metadata to be cleared: %#v", saved)
	}
}

func TestSandboxServiceDefaultAudienceLifecycle(t *testing.T) {
	store := New(t.TempDir())
	saved, err := store.UpsertSandboxServiceDefault(SandboxServiceDefault{
		Enabled: true,
		APIURL:  "https://sandbox.example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.AudienceMode != SandboxServiceDefaultAudienceModeAll {
		t.Fatalf("default audience mode = %q", saved.AudienceMode)
	}
	saved, err = store.UpsertSandboxServiceDefault(SandboxServiceDefault{
		Enabled:      false,
		APIURL:       saved.APIURL,
		AudienceMode: SandboxServiceDefaultAudienceModeSelected,
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.AudienceMode != SandboxServiceDefaultAudienceModeSelected {
		t.Fatalf("saved audience mode = %q", saved.AudienceMode)
	}

	audience, err := store.UpsertSandboxServiceDefaultAudience(SandboxServiceDefaultAudience{
		GitHubAccountID: 9001,
		AccountType:     "Organization",
		AccountLogin:    "Octo-Org",
		AccountName:     "Octo Org",
	})
	if err != nil {
		t.Fatal(err)
	}
	updated, err := store.UpsertSandboxServiceDefaultAudience(SandboxServiceDefaultAudience{
		GitHubAccountID: 9001,
		AccountType:     "organization",
		AccountLogin:    "renamed-org",
		AccountName:     "Renamed Org",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != audience.ID || updated.AccountLogin != "renamed-org" {
		t.Fatalf("expected duplicate identity to update display metadata: first=%#v updated=%#v", audience, updated)
	}
	audiences, err := store.ListSandboxServiceDefaultAudiences()
	if err != nil {
		t.Fatal(err)
	}
	if len(audiences) != 1 || audiences[0].GitHubAccountID != 9001 || audiences[0].AccountType != "organization" {
		t.Fatalf("unexpected audiences: %#v", audiences)
	}
	allowed, err := store.SandboxServiceDefaultAudienceContains(9001, "Organization")
	if err != nil || !allowed {
		t.Fatalf("expected audience membership, allowed=%v err=%v", allowed, err)
	}
	if allowed, err := store.SandboxServiceDefaultAudienceContains(9002, "organization"); err != nil || allowed {
		t.Fatalf("unexpected audience membership, allowed=%v err=%v", allowed, err)
	}
	if err := store.DeleteSandboxServiceDefaultAudience(audience.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteSandboxServiceDefaultAudience(audience.ID); err != nil {
		t.Fatalf("expected audience deletion to be idempotent: %v", err)
	}
}

func TestSandboxServiceDefaultAudiencesSortCaseInsensitively(t *testing.T) {
	store := New(t.TempDir())
	for githubAccountID, accountLogin := range map[int64]string{
		1: "beta",
		2: "Alpha",
		3: "charlie",
	} {
		if _, err := store.UpsertSandboxServiceDefaultAudience(SandboxServiceDefaultAudience{
			GitHubAccountID: githubAccountID,
			AccountType:     "organization",
			AccountLogin:    accountLogin,
		}); err != nil {
			t.Fatal(err)
		}
	}
	audiences, err := store.ListSandboxServiceDefaultAudiences()
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(audiences))
	for _, audience := range audiences {
		got = append(got, audience.AccountLogin)
	}
	if want := []string{"Alpha", "beta", "charlie"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("audience order = %#v, want %#v", got, want)
	}
}

func TestGitHubInstallationOwnerCacheLifecycle(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.GetGitHubInstallationOwner(987); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetGitHubInstallationOwner() error = %v, want ErrNotFound", err)
	}
	owner, err := store.UpsertGitHubInstallationOwner(987, GitHubInstallationAccount{
		GitHubAccountID: 9001,
		AccountType:     "Organization",
		AccountLogin:    "octo-org",
		AccountName:     "Octo Org",
	})
	if err != nil {
		t.Fatal(err)
	}
	if owner.GitHubAccountID != 9001 || owner.AccountType != "organization" || owner.AccountLogin != "octo-org" {
		t.Fatalf("unexpected cached owner: %#v", owner)
	}
	updated, err := store.UpsertGitHubInstallationOwner(987, GitHubInstallationAccount{
		GitHubAccountID: 9001,
		AccountType:     "organization",
		AccountLogin:    "renamed-org",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AccountLogin != "renamed-org" {
		t.Fatalf("expected display metadata update, got %#v", updated)
	}
	loaded, err := store.GetGitHubInstallationOwner(987)
	if err != nil || loaded.AccountLogin != "renamed-org" {
		t.Fatalf("unexpected loaded owner: %#v err=%v", loaded, err)
	}
}

func TestRunnerRequestSandboxConfigSourceRoundTripAndRetryReset(t *testing.T) {
	store := New(t.TempDir())
	_, st, err := store.CreateRequest(RunnerRequest{
		ID:                     "sandbox-source",
		Source:                 "test",
		Labels:                 []string{"self-hosted"},
		RunnerName:             "e2b-sandbox-source",
		SandboxAPIURL:          "https://sandbox.example.test",
		SandboxAPIKeyEncrypted: "encrypted-key",
		SandboxConfigSource:    "admin_default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.SandboxConfigSource != "admin_default" {
		t.Fatalf("created state source = %q", st.SandboxConfigSource)
	}

	req, err := store.ReadRequest("sandbox-source")
	if err != nil {
		t.Fatal(err)
	}
	if req.SandboxConfigSource != "admin_default" {
		t.Fatalf("request source = %q", req.SandboxConfigSource)
	}

	st.Status = StatusFailed
	st.SandboxConfigSource = "account"
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	st, err = store.ReadState("sandbox-source")
	if err != nil {
		t.Fatal(err)
	}
	if st.SandboxConfigSource != "account" {
		t.Fatalf("updated state source = %q", st.SandboxConfigSource)
	}

	retried, err := store.RetryRequest("sandbox-source", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if retried.SandboxConfigSource != "" || retried.SandboxAPIURL != "" || retried.SandboxAPIKeyEncrypted != "" {
		t.Fatalf("expected retry to clear sandbox config snapshot: %#v", retried)
	}
}

func TestCreateRequestIsIdempotent(t *testing.T) {
	store := New(t.TempDir())
	req := RunnerRequest{ID: "123", Source: "test", Labels: []string{"self-hosted"}, RunnerName: "e2b-123"}
	created, st, err := store.CreateRequest(req, []byte(`{"x":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !created || st.Status != StatusQueued {
		t.Fatalf("unexpected first create: created=%v state=%#v", created, st)
	}

	created, st, err = store.CreateRequest(req, nil)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("expected duplicate request to reuse existing row")
	}
	if st.ID != "123" {
		t.Fatalf("unexpected state id: %q", st.ID)
	}
}

func TestCreateRejectedRequestIsNeverRunnable(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	req := RunnerRequest{
		ID:                 "rejected",
		Source:             "github_webhook",
		JobID:              12345,
		RepositoryFullName: "o/r",
		RequestedLabels:    []string{"ubuntu-latest"},
		Labels:             []string{"ubuntu-latest"},
		RunnerName:         "e2b-rejected",
	}
	created, st, err := store.CreateRejectedRequest(req, []byte(`{"workflow_job":{"id":12345,"run_id":67890,"workflow_name":"CI"}}`), "profile_labels_not_matched")
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("expected rejected request to be created")
	}
	if st.Status != StatusFailed || st.FailureStage != "admission" || st.FailureReason != "profile_labels_not_matched" {
		t.Fatalf("unexpected rejected state: %#v", st)
	}
	if st.Error != "runner admission rejected" || st.FailedAt.IsZero() {
		t.Fatalf("rejected state is missing terminal failure details: %#v", st)
	}
	record, err := store.readRecord(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.GitHubPayloadJSON != "" {
		t.Fatal("rejected request must not persist the webhook payload")
	}
	if record.WorkflowRunID != 67890 || record.WorkflowName != "CI" || !record.GitHubContextBackfilled {
		t.Fatalf("rejected request must retain parsed github context: %#v", record)
	}

	_, _, claimed, err := store.ClaimNextRunnable("worker", time.Now().UTC().Add(time.Minute), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("rejected request must never be visible to the runnable queue")
	}

	created, duplicate, err := store.CreateRejectedRequest(req, nil, "different_reason")
	if err != nil {
		t.Fatal(err)
	}
	if created || duplicate.Status != StatusFailed || duplicate.FailureReason != "profile_labels_not_matched" {
		t.Fatalf("duplicate rejection must reuse the original terminal state: created=%v state=%#v", created, duplicate)
	}
}

func TestCreateRequestConflictingWorkflowJobReturnsExistingState(t *testing.T) {
	store := New(t.TempDir())
	_, st, err := store.CreateRequest(RunnerRequest{
		ID:         "first",
		Source:     "test",
		JobID:      12345,
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-first",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	created, conflict, err := store.CreateRequest(RunnerRequest{
		ID:         "second",
		Source:     "test",
		JobID:      12345,
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-second",
	}, nil)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if created {
		t.Fatal("expected conflicting workflow job to reuse existing row")
	}
	if conflict.ID != st.ID || conflict.WorkflowJobID != 12345 {
		t.Fatalf("unexpected conflicting state: %#v", conflict)
	}
}

func TestListMismatchedCompletedStates(t *testing.T) {
	store := New(t.TempDir())
	_, st, err := store.CreateRequest(RunnerRequest{
		ID:         "mismatch",
		Source:     "test",
		JobID:      1001,
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-mismatch",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = StatusCompleted
	st.AssignedJobID = 2002
	st.AssignedJobName = "other"
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	_, matched, err := store.CreateRequest(RunnerRequest{
		ID:         "matched",
		Source:     "test",
		JobID:      3003,
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-matched",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	matched.Status = StatusCompleted
	matched.AssignedJobID = 3003
	if err := store.WriteState(matched); err != nil {
		t.Fatal(err)
	}

	states, err := store.ListMismatchedCompletedStates(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ID != "mismatch" {
		t.Fatalf("expected only mismatched completed state, got %#v", states)
	}
}

func TestListFailedWorkflowJobStates(t *testing.T) {
	store := New(t.TempDir())
	_, st, err := store.CreateRequest(RunnerRequest{
		ID:                 "failed-recovery",
		Source:             "test",
		JobID:              1001,
		RepositoryFullName: "o/r",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-failed-recovery",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = StatusFailed
	st.FailureStage = "recovery"
	st.FailureReason = "cleanup_failed"
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	_, other, err := store.CreateRequest(RunnerRequest{
		ID:                 "failed-start",
		Source:             "test",
		JobID:              2002,
		RepositoryFullName: "o/r",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-failed-start",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	other.Status = StatusFailed
	other.FailureStage = "sandbox_start"
	if err := store.WriteState(other); err != nil {
		t.Fatal(err)
	}

	states, err := store.ListFailedWorkflowJobStates(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ID != "failed-recovery" {
		t.Fatalf("expected only failed recovery workflow job state, got %#v", states)
	}
}

func TestUpsertAccountForOAuthIdentityMaintainsRoleByOAuthIdentity(t *testing.T) {
	store := New(t.TempDir())
	account, identity, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "GitHub", OAuthSubject: "12345", OAuthLogin: "OctoCat"}, "ADMIN")
	if err != nil {
		t.Fatal(err)
	}
	if identity.OAuthProvider != "github" || identity.OAuthSubject != "12345" || identity.OAuthLogin != "octocat" || account.Role != "admin" {
		t.Fatalf("unexpected created account/identity: account=%#v identity=%#v", account, identity)
	}

	updatedAccount, updatedIdentity, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "renamed"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if updatedIdentity.ID != identity.ID || updatedIdentity.OAuthLogin != "renamed" || updatedAccount.Role != "user" {
		t.Fatalf("unexpected updated account/identity: account=%#v identity=%#v firstAccount=%#v firstIdentity=%#v", updatedAccount, updatedIdentity, account, identity)
	}

	gotAccount, gotIdentity, err := store.GetAccountByOAuthIdentity("GITHUB", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if gotIdentity.ID != identity.ID || gotAccount.Role != "user" {
		t.Fatalf("unexpected fetched account/identity: account=%#v identity=%#v", gotAccount, gotIdentity)
	}

	_, gitlabIdentity, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "gitlab", OAuthSubject: "12345", OAuthLogin: "octocat"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	if gitlabIdentity.ID == identity.ID {
		t.Fatalf("expected provider to separate oauth identities: github=%#v gitlab=%#v", identity, gitlabIdentity)
	}

	_, reusedLoginIdentity, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "67890", OAuthLogin: "renamed"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if reusedLoginIdentity.ID == identity.ID {
		t.Fatalf("expected stable subject to separate reused login identities: first=%#v reused=%#v", identity, reusedLoginIdentity)
	}
}

func TestListAccountsSearchesFiltersAndPaginates(t *testing.T) {
	store := New(t.TempDir())
	admin, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "100", OAuthLogin: "alpha"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	user, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "200", OAuthLogin: "bravo"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LinkOAuthIdentityToAccount(user.ID, OAuthIdentity{OAuthProvider: "gitlab", OAuthSubject: "gl-200", OAuthLogin: "Bravo-Lab"}); err != nil {
		t.Fatal(err)
	}
	third, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "300", OAuthLogin: "charlie"}, "user")
	if err != nil {
		t.Fatal(err)
	}

	firstPage, total, err := store.ListAccounts(AccountListOptions{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(firstPage) != 2 {
		t.Fatalf("unexpected first page: total=%d accounts=%#v", total, firstPage)
	}
	secondPage, secondTotal, err := store.ListAccounts(AccountListOptions{Limit: 2, Offset: 2})
	if err != nil {
		t.Fatal(err)
	}
	if secondTotal != 3 || len(secondPage) != 1 || secondPage[0].ID == firstPage[0].ID || secondPage[0].ID == firstPage[1].ID {
		t.Fatalf("unexpected second page: total=%d accounts=%#v first=%#v", secondTotal, secondPage, firstPage)
	}

	users, userTotal, err := store.ListAccounts(AccountListOptions{Role: " USER ", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if userTotal != 2 || len(users) != 2 {
		t.Fatalf("unexpected user filter: total=%d accounts=%#v", userTotal, users)
	}
	for _, item := range users {
		if item.Role != "user" || item.ID == admin.ID {
			t.Fatalf("role filter returned the wrong account: %#v", item)
		}
	}

	searched, searchTotal, err := store.ListAccounts(AccountListOptions{Query: " BRAVO-LAB ", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if searchTotal != 1 || len(searched) != 1 || searched[0].ID != user.ID || len(searched[0].OAuthIdentities) != 2 {
		t.Fatalf("unexpected login search result: total=%d accounts=%#v", searchTotal, searched)
	}
	if searched[0].OAuthIdentities[0].OAuthProvider != "github" || searched[0].OAuthIdentities[1].OAuthProvider != "gitlab" {
		t.Fatalf("expected stable provider ordering, got %#v", searched[0].OAuthIdentities)
	}

	bySubject, subjectTotal, err := store.ListAccounts(AccountListOptions{Query: "300", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if subjectTotal != 1 || len(bySubject) != 1 || bySubject[0].ID != third.ID {
		t.Fatalf("unexpected subject search result: total=%d accounts=%#v", subjectTotal, bySubject)
	}
}

func TestListAccountsRejectsUnexpectedIdentityAccount(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	if _, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "100", OAuthLogin: "alpha"}, "admin"); err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove("test:append-unexpected-account-identity")
	})
	if err := db.Callback().Query().After("gorm:query").Register("test:append-unexpected-account-identity", func(query *gorm.DB) {
		if identities, ok := query.Statement.Dest.(*[]oauthIdentityRecord); ok {
			*identities = append(*identities, oauthIdentityRecord{
				ID:            999,
				AccountID:     999,
				OAuthProvider: "github",
				OAuthSubject:  "unexpected",
				OAuthLogin:    "unexpected",
			})
		}
	}); err != nil {
		t.Fatal(err)
	}

	if _, _, err := store.ListAccounts(AccountListOptions{Limit: 10}); err == nil || !strings.Contains(err.Error(), "unexpected account") {
		t.Fatalf("expected unexpected identity account error, got %v", err)
	}
}

func TestGetAccountStatsCountsAccountsRolesAndIdentities(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	if _, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "100", OAuthLogin: "alpha"}, "admin"); err != nil {
		t.Fatal(err)
	}
	user, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "200", OAuthLogin: "bravo"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LinkOAuthIdentityToAccount(user.ID, OAuthIdentity{OAuthProvider: "gitlab", OAuthSubject: "gl-200", OAuthLogin: "bravo"}); err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	queryCount := 0
	countQuery := func(*gorm.DB) {
		queryCount++
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove("test:count-account-stats-query")
		_ = db.Callback().Row().Remove("test:count-account-stats-row")
	})
	if err := db.Callback().Query().Before("gorm:query").Register("test:count-account-stats-query", countQuery); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Row().Before("gorm:row").Register("test:count-account-stats-row", countQuery); err != nil {
		t.Fatal(err)
	}

	stats, err := store.GetAccountStats()
	if err != nil {
		t.Fatal(err)
	}
	want := AccountStats{TotalAccounts: 2, AdminAccounts: 1, UserAccounts: 1, OAuthIdentities: 3}
	if stats != want {
		t.Fatalf("unexpected account stats: got=%#v want=%#v", stats, want)
	}
	if queryCount != 2 {
		t.Fatalf("GetAccountStats executed %d queries, want 2", queryCount)
	}
}

func TestListAccountsRejectsInvalidRole(t *testing.T) {
	store := New(t.TempDir())
	if _, _, err := store.ListAccounts(AccountListOptions{Role: "owner", Limit: 10}); err == nil {
		t.Fatal("expected invalid role filter to fail")
	}
}

func TestUpdateAccountRoleValidatesAndPersists(t *testing.T) {
	store := New(t.TempDir())
	actor, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "admin", OAuthLogin: "admin"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	account, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "octocat"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := store.GetAccount(account.ID)
	if err != nil || fetched.ID != account.ID || fetched.Role != "user" {
		t.Fatalf("unexpected fetched account: account=%#v err=%v", fetched, err)
	}

	updated, err := store.UpdateAccountRoleWithAudit(AccountRoleUpdate{
		ActorAccountID: actor.ID,
		AccountID:      account.ID,
		Role:           " ADMIN ",
		AuditActor:     "github:admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.ID != account.ID || updated.Role != "admin" || !updated.UpdatedAt.After(account.UpdatedAt) {
		t.Fatalf("unexpected updated account: before=%#v after=%#v", account, updated)
	}
	persisted, _, err := store.GetAccountByOAuthIdentity("github", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Role != "admin" {
		t.Fatalf("expected persisted admin role, got %#v", persisted)
	}
	if _, err := store.UpdateAccountRoleWithAudit(AccountRoleUpdate{ActorAccountID: actor.ID, AccountID: account.ID, Role: "owner", AuditActor: "github:admin"}); err == nil {
		t.Fatal("expected invalid role update to fail")
	}
	if _, err := store.UpdateAccountRoleWithAudit(AccountRoleUpdate{ActorAccountID: actor.ID, AccountID: 0, Role: "user", AuditActor: "github:admin"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing account to return ErrNotFound, got %v", err)
	}
	if _, err := store.GetAccount(0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing account lookup to return ErrNotFound, got %v", err)
	}
}

func TestUpdateAccountRoleWithAuditSkipsNoOpChanges(t *testing.T) {
	store := New(t.TempDir())
	actor, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "actor", OAuthLogin: "actor"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "target", OAuthLogin: "target"}, "user")
	if err != nil {
		t.Fatal(err)
	}

	updated, err := store.UpdateAccountRoleWithAudit(AccountRoleUpdate{
		ActorAccountID: actor.ID,
		AccountID:      target.ID,
		Role:           "user",
		AuditActor:     "github:actor",
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated != target {
		t.Fatalf("expected no-op role update to return the unchanged account: before=%#v after=%#v", target, updated)
	}
	events, err := store.ListAuditEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("expected no audit event for a no-op role update, got %#v", events)
	}
}

func TestUpdateAccountRoleWithAuditRollsBackWhenAuditInsertFails(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	actor, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "actor", OAuthLogin: "actor"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	target, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "target", OAuthLogin: "target"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_account_role_audit
		BEFORE INSERT ON audit_events
		WHEN NEW.action = 'account.role.update'
		BEGIN
			SELECT RAISE(ABORT, 'forced audit failure');
		END`).Error; err != nil {
		t.Fatal(err)
	}

	_, err = store.UpdateAccountRoleWithAudit(AccountRoleUpdate{
		ActorAccountID: actor.ID,
		AccountID:      target.ID,
		Role:           "admin",
		AuditActor:     "github:actor",
	})
	if err == nil || !strings.Contains(err.Error(), "forced audit failure") {
		t.Fatalf("expected forced audit failure, got %v", err)
	}
	persisted, err := store.GetAccount(target.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Role != "user" {
		t.Fatalf("expected failed audit insert to roll back role update, got %#v", persisted)
	}
}

func TestConcurrentAccountRoleUpdatesKeepAnAdministrator(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	first, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "first", OAuthLogin: "first"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "second", OAuthLogin: "second"}, "admin")
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	errorsByUpdate := make(chan error, 2)
	updates := []AccountRoleUpdate{
		{ActorAccountID: first.ID, AccountID: second.ID, Role: "user", AuditActor: "github:first"},
		{ActorAccountID: second.ID, AccountID: first.ID, Role: "user", AuditActor: "github:second"},
	}
	for _, update := range updates {
		update := update
		go func() {
			<-start
			_, updateErr := store.UpdateAccountRoleWithAudit(update)
			errorsByUpdate <- updateErr
		}()
	}
	close(start)
	firstErr := <-errorsByUpdate
	secondErr := <-errorsByUpdate
	if (firstErr == nil) == (secondErr == nil) {
		t.Fatalf("expected exactly one role update to succeed, got errors %v and %v", firstErr, secondErr)
	}
	failedErr := firstErr
	if failedErr == nil {
		failedErr = secondErr
	}
	if !errors.Is(failedErr, ErrConflict) {
		t.Fatalf("expected losing role update to return ErrConflict, got %v", failedErr)
	}
	stats, err := store.GetAccountStats()
	if err != nil {
		t.Fatal(err)
	}
	if stats.AdminAccounts != 1 {
		t.Fatalf("expected one administrator to remain, got %#v", stats)
	}
}

func TestGetOAuthIdentityForAccountByProvider(t *testing.T) {
	store := New(t.TempDir())
	account, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "octocat"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.LinkOAuthIdentityToAccount(account.ID, OAuthIdentity{OAuthProvider: "gitlab", OAuthSubject: "abcde", OAuthLogin: "octocat-gitlab"}); err != nil {
		t.Fatal(err)
	}

	identity, err := store.GetOAuthIdentityForAccount(account.ID, " GITHUB ")
	if err != nil {
		t.Fatal(err)
	}
	if identity.AccountID != account.ID || identity.OAuthProvider != "github" || identity.OAuthLogin != "octocat" {
		t.Fatalf("unexpected github identity: %#v", identity)
	}
	if _, err := store.GetOAuthIdentityForAccount(account.ID, "missing"); err != ErrNotFound {
		t.Fatalf("expected missing provider identity to return ErrNotFound, got %v", err)
	}
}

func TestEnsureAccountForOAuthIdentityCreatesDefaultWithoutOverwritingExistingRole(t *testing.T) {
	store := New(t.TempDir())
	createdAccount, createdIdentity, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "octocat"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if createdAccount.Role != "user" {
		t.Fatalf("unexpected created role: %#v", createdAccount)
	}

	if _, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "octocat"}, "admin"); err != nil {
		t.Fatal(err)
	}
	ensuredAccount, ensuredIdentity, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "renamed"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if ensuredIdentity.ID != createdIdentity.ID || ensuredIdentity.OAuthLogin != "renamed" || ensuredAccount.Role != "admin" {
		t.Fatalf("ensure should preserve existing admin role, got account=%#v identity=%#v createdAccount=%#v createdIdentity=%#v", ensuredAccount, ensuredIdentity, createdAccount, createdIdentity)
	}
}

func TestLinkOAuthIdentityToAccountSharesRoleAcrossProviders(t *testing.T) {
	store := New(t.TempDir())
	githubAccount, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "octocat"}, "admin")
	if err != nil {
		t.Fatal(err)
	}

	gitlabAccount, gitlabIdentity, err := store.LinkOAuthIdentityToAccount(githubAccount.ID, OAuthIdentity{OAuthProvider: "gitlab", OAuthSubject: "abcde", OAuthLogin: "octocat"})
	if err != nil {
		t.Fatal(err)
	}
	if gitlabIdentity.AccountID != githubAccount.ID || gitlabAccount.Role != "admin" {
		t.Fatalf("expected linked identity to share account role, github=%#v gitlabAccount=%#v gitlabIdentity=%#v", githubAccount, gitlabAccount, gitlabIdentity)
	}

	gotGitHubAccount, gotGitHubIdentity, err := store.GetAccountByOAuthIdentity("github", "12345")
	if err != nil {
		t.Fatal(err)
	}
	gotGitLabAccount, gotGitLabIdentity, err := store.GetAccountByOAuthIdentity("gitlab", "abcde")
	if err != nil {
		t.Fatal(err)
	}
	if gotGitHubAccount.ID != gotGitLabAccount.ID || gotGitHubAccount.Role != gotGitLabAccount.Role || gotGitHubIdentity.AccountID != gotGitLabIdentity.AccountID {
		t.Fatalf("expected provider identities to resolve to same account, githubAccount=%#v githubIdentity=%#v gitlabAccount=%#v gitlabIdentity=%#v", gotGitHubAccount, gotGitHubIdentity, gotGitLabAccount, gotGitLabIdentity)
	}
}

func TestLinkOAuthIdentityToAccountRejectsIdentityOnDifferentAccount(t *testing.T) {
	store := New(t.TempDir())
	first, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "octocat"}, "admin")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "gitlab", OAuthSubject: "abcde", OAuthLogin: "octocat"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID {
		t.Fatalf("expected separate accounts before linking, first=%#v second=%#v", first, second)
	}

	_, _, err = store.LinkOAuthIdentityToAccount(first.ID, OAuthIdentity{OAuthProvider: "gitlab", OAuthSubject: "abcde", OAuthLogin: "octocat"})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected conflict linking identity from another account, got %v", err)
	}
}

func TestMigrateDoesNotCreateOAuthIdentityForeignKey(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}

	var foreignKeys []struct {
		ID int `gorm:"column:id"`
	}
	if err := db.Raw("PRAGMA foreign_key_list(oauth_identities)").Scan(&foreignKeys).Error; err != nil {
		t.Fatal(err)
	}
	if len(foreignKeys) != 0 {
		t.Fatalf("expected oauth_identities to have no database foreign keys, got %d", len(foreignKeys))
	}
}

func TestMigrateDropsLegacyOAuthIdentityForeignKey(t *testing.T) {
	dir := t.TempDir()
	databaseURL := dir + "/runnerd.db"
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: false,
	}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Exec(`CREATE TABLE accounts (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		role TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE oauth_identities (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL,
		oauth_provider TEXT NOT NULL,
		oauth_subject TEXT NOT NULL,
		oauth_login TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		CONSTRAINT fk_oauth_identities_account FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE
	);`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO accounts (id, role, created_at, updated_at) VALUES (?, ?, ?, ?)`,
		1, "admin", now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO oauth_identities (id, account_id, oauth_provider, oauth_subject, oauth_login, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		1, 1, "github", "12345", "octocat", now, now).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	migrated := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	db, err = migrated.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}

	var foreignKeys []struct {
		ID int `gorm:"column:id"`
	}
	if err := db.Raw("PRAGMA foreign_key_list(oauth_identities)").Scan(&foreignKeys).Error; err != nil {
		t.Fatal(err)
	}
	if len(foreignKeys) != 0 {
		t.Fatalf("expected legacy oauth_identities foreign keys to be removed, got %d", len(foreignKeys))
	}
}

func TestLinkOAuthIdentityToAccountRequiresExistingAccount(t *testing.T) {
	store := New(t.TempDir())
	_, _, err := store.LinkOAuthIdentityToAccount(999, OAuthIdentity{
		OAuthProvider: "github",
		OAuthSubject:  "12345",
		OAuthLogin:    "octocat",
	})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected missing account to be rejected by store logic, got %v", err)
	}
}

func TestMigrateDoesNotHandleLegacyUsers(t *testing.T) {
	dir := t.TempDir()
	databaseURL := dir + "/runnerd.db"
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: false,
	}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Exec(`CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		oauth_login TEXT NOT NULL,
		role TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO users (id, oauth_login, role, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		42, "octocat", "admin", now, now).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	migrated := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	db, err = migrated.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasTable("users") {
		t.Fatal("expected legacy users table to be left untouched")
	}
	var count int64
	if err := db.Table("users").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected legacy users row to remain untouched, got %d", count)
	}
}

func TestReleaseCFreshSchemaOmitsRetiredCatalogTables(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"runner_groups", "runner_group_specs", "repository_policies"} {
		if db.Migrator().HasTable(table) {
			t.Errorf("fresh Release C schema created retired table %s", table)
		}
	}
}

func TestReleaseCLegacyCatalogTablesRemainUntouched(t *testing.T) {
	databaseURL := filepath.Join(t.TempDir(), "runnerd.db")
	setup := NewWithOptions(Options{
		Backend: BackendSQLite, DatabaseDSN: databaseURL, MigrateOnStart: false,
	}).(*DBStore)
	db, err := setup.open()
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE runner_groups (name TEXT PRIMARY KEY, description TEXT, enabled BOOLEAN NOT NULL, created_at TIMESTAMP NOT NULL, updated_at TIMESTAMP NOT NULL)`,
		`CREATE TABLE runner_group_specs (group_name TEXT NOT NULL, spec_name TEXT NOT NULL, created_at TIMESTAMP NOT NULL, PRIMARY KEY (group_name, spec_name))`,
		`CREATE TABLE repository_policies (id INTEGER PRIMARY KEY AUTOINCREMENT, repository_full_name TEXT NOT NULL, profile_name TEXT NOT NULL, runner_group_name TEXT NOT NULL DEFAULT '', enabled BOOLEAN NOT NULL, created_at TIMESTAMP NOT NULL)`,
		`INSERT INTO runner_groups (name, description, enabled, created_at, updated_at) VALUES ('legacy', 'rollback data', 1, '2026-08-24 10:03:03', '2026-08-24 10:03:03')`,
		`INSERT INTO runner_group_specs (group_name, spec_name, created_at) VALUES ('legacy', 'legacy-spec', '2026-08-24 10:03:03')`,
		`INSERT INTO repository_policies (repository_full_name, profile_name, runner_group_name, enabled, created_at) VALUES ('owner/repo', 'legacy-spec', '', 1, '2026-08-24 10:03:03')`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	closeTestDB(t, db)

	for attempt := 0; attempt < 2; attempt++ {
		migrated := NewWithOptions(Options{
			Backend: BackendSQLite, DatabaseDSN: databaseURL, MigrateOnStart: true,
		}).(*DBStore)
		db, err = migrated.dbOrEnsure()
		if err != nil {
			t.Fatal(err)
		}
		var group struct {
			Name        string
			Description string
			Enabled     bool
		}
		if err := db.Table("runner_groups").Select("name", "description", "enabled").Take(&group).Error; err != nil {
			t.Fatal(err)
		}
		if group.Name != "legacy" || group.Description != "rollback data" || !group.Enabled {
			t.Fatalf("legacy runner group changed after migration %d: %#v", attempt+1, group)
		}
		for table, want := range map[string]int64{"runner_groups": 1, "runner_group_specs": 1, "repository_policies": 1} {
			var count int64
			if err := db.Table(table).Count(&count).Error; err != nil {
				t.Fatal(err)
			}
			if count != want {
				t.Fatalf("%s row count after migration %d = %d, want %d", table, attempt+1, count, want)
			}
		}
		closeTestDB(t, db)
	}
}

func TestMigratePreservesLegacyRunnerProfileAndAddsManagedCatalogColumns(t *testing.T) {
	dir := t.TempDir()
	databaseURL := dir + "/runnerd.db"
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: false,
	}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Exec(`CREATE TABLE runner_profiles (
		name TEXT PRIMARY KEY,
		labels_json TEXT NOT NULL,
		template_id TEXT NOT NULL,
		runner_group TEXT,
		max_concurrency INTEGER NOT NULL,
		min_idle INTEGER NOT NULL DEFAULT 0,
		priority INTEGER NOT NULL DEFAULT 0,
		enabled BOOLEAN NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO runner_profiles (name, labels_json, template_id, runner_group, max_concurrency, min_idle, priority, enabled, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"default", `["self-hosted"]`, "base", "default", 1, 3, 17, true, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE INDEX idx_runner_profiles_legacy_capacity
		ON runner_profiles (priority, min_idle)`).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	migrated := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	profile, err := migrated.GetProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "default" ||
		!reflect.DeepEqual(profile.Labels, []string{"self-hosted"}) ||
		profile.TemplateID != "base" ||
		profile.RunnerGroup != "default" ||
		profile.MaxConcurrency != 1 ||
		profile.MinIdle != 3 ||
		profile.Priority != 17 ||
		!profile.Enabled ||
		!profile.CreatedAt.Equal(now) ||
		!profile.UpdatedAt.Equal(now) {
		t.Fatalf("legacy runner profile fields changed during migration: %#v", profile)
	}
	if profile.RequiredLabels == nil || len(profile.RequiredLabels) != 0 {
		t.Fatalf("legacy profile RequiredLabels = %#v, want non-nil empty slice", profile.RequiredLabels)
	}
	if profile.DefaultTemplateName != "" || profile.ManagedBy != "" || profile.CatalogRevision != 0 {
		t.Fatalf("unexpected legacy managed catalog fields: %#v", profile)
	}

	db, err = migrated.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	var legacyIndexSQL string
	if err := db.Raw(`SELECT sql FROM sqlite_master
		WHERE type = 'index' AND tbl_name = 'runner_profiles' AND name = 'idx_runner_profiles_legacy_capacity'`).
		Scan(&legacyIndexSQL).Error; err != nil {
		t.Fatal(err)
	}
	if legacyIndexSQL == "" {
		t.Fatal("legacy runner_profiles index was lost during migration; table must not be rebuilt")
	}
	var stored struct {
		RequiredLabelsJSON  *string `gorm:"column:required_labels_json"`
		DefaultTemplateName string  `gorm:"column:default_template_name"`
		ManagedBy           string  `gorm:"column:managed_by"`
		CatalogRevision     int     `gorm:"column:catalog_revision"`
		DefaultAvailable    bool    `gorm:"column:default_available"`
	}
	if err := db.Raw(`SELECT required_labels_json, default_template_name, managed_by, catalog_revision, default_available
		FROM runner_profiles WHERE name = ?`, "default").Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.RequiredLabelsJSON != nil {
		t.Fatalf("legacy required_labels_json = %q, want NULL after migration", *stored.RequiredLabelsJSON)
	}
	if stored.DefaultTemplateName != "" || stored.ManagedBy != "" || stored.CatalogRevision != 0 {
		t.Fatalf("unexpected legacy managed catalog values after migration: %#v", stored)
	}
	if !stored.DefaultAvailable {
		t.Fatal("legacy default_available compatibility value = false, want true")
	}

	profile.TemplateID = "base-updated"
	updated, err := migrated.UpsertProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "default" ||
		!reflect.DeepEqual(updated.Labels, []string{"self-hosted"}) ||
		updated.TemplateID != "base-updated" ||
		updated.RunnerGroup != "default" ||
		updated.MaxConcurrency != 1 ||
		updated.MinIdle != 3 ||
		updated.Priority != 17 ||
		!updated.Enabled ||
		!updated.CreatedAt.Equal(now) {
		t.Fatalf("legacy runner profile fields changed during update: %#v", updated)
	}
	if err := db.Raw(`SELECT required_labels_json, default_template_name, managed_by, catalog_revision, default_available
		FROM runner_profiles WHERE name = ?`, "default").Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.RequiredLabelsJSON == nil || *stored.RequiredLabelsJSON != "[]" {
		t.Fatalf("updated required_labels_json = %#v, want non-NULL []", stored.RequiredLabelsJSON)
	}
	if !stored.DefaultAvailable {
		t.Fatal("profile update changed the physical default_available compatibility value")
	}
}

func TestMigrateExistingSQLiteRunnerProfileAddsAllModelColumns(t *testing.T) {
	dir := t.TempDir()
	databaseURL := dir + "/runnerd.db"
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: false,
	}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Exec(`CREATE TABLE runner_profiles (
		name TEXT PRIMARY KEY,
		labels_json TEXT NOT NULL,
		template_id TEXT NOT NULL,
		max_concurrency INTEGER NOT NULL,
		priority INTEGER NOT NULL DEFAULT 0,
		enabled BOOLEAN NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO runner_profiles (
		name, labels_json, template_id, max_concurrency, priority, enabled, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		"minimal", `["self-hosted"]`, "base", 1, 17, true, now, now).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	migrated := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	db, err = migrated.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"RunnerGroup", "MinIdle"} {
		if !db.Migrator().HasColumn(&runnerProfileRecord{}, field) {
			t.Fatalf("runner_profiles missing model column %s after migration", field)
		}
	}
	profile, err := migrated.GetProfile("minimal")
	if err != nil {
		t.Fatal(err)
	}
	if profile.Name != "minimal" ||
		profile.TemplateID != "base" ||
		profile.MinIdle != 0 ||
		profile.RunnerGroup != "" {
		t.Fatalf("migrated minimal runner profile = %#v", profile)
	}
	var defaultAvailable bool
	if err := db.Raw(`SELECT default_available FROM runner_profiles WHERE name = ?`, "minimal").Scan(&defaultAvailable).Error; err != nil {
		t.Fatal(err)
	}
	if !defaultAvailable {
		t.Fatal("minimal legacy profile default_available compatibility value = false, want true")
	}
}

func TestMigrateResetsLegacyAccountPreferencesAndSecrets(t *testing.T) {
	dir := t.TempDir()
	databaseURL := dir + "/runnerd.db"
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: false,
	}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Exec(`CREATE TABLE account_preferences (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL,
		namespace TEXT NOT NULL,
		key TEXT NOT NULL,
		value_json TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE account_secrets (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL,
		key_type TEXT NOT NULL,
		encrypted_value TEXT NOT NULL,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	);`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO account_preferences (account_id, namespace, key, value_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		1, "sandbox", "service", `{"api_url":"https://legacy.example.test"}`, now, now).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO account_secrets (account_id, key_type, encrypted_value, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		1, AccountSecretTypeSandboxAPIKey, "legacy-encrypted-value", now, now).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	migrated := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	if err := migrated.Ensure(); err != nil {
		t.Fatal(err)
	}
	migratedDB, err := migrated.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"account_preferences", "account_secrets"} {
		var count int64
		if err := migratedDB.Table(table).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("expected legacy %s rows to be reset, got %d", table, count)
		}
		for _, column := range []string{"scope_type", "scope_id"} {
			if !migratedDB.Migrator().HasColumn(table, column) {
				t.Fatalf("expected %s.%s after migration", table, column)
			}
		}
	}
}

func TestRunnerRequestListIndexSupportsNewestPageOrder(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}

	if !db.Migrator().HasIndex(&runnerRequestRecord{}, "idx_runner_requests_queued_id") {
		t.Fatal("expected runner request list ordering index")
	}

	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	if err := db.Raw(`
		EXPLAIN QUERY PLAN
		SELECT id, queued_at
		FROM runner_requests
		ORDER BY queued_at DESC, id ASC
		LIMIT 100
	`).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, step := range plan {
		details = append(details, step.Detail)
	}
	joined := strings.Join(details, "\n")
	if !strings.Contains(joined, "idx_runner_requests_queued_id") {
		t.Fatalf("expected list query to use ordering index, plan:\n%s", joined)
	}
	if strings.Contains(joined, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("list query still sorts through a temporary B-tree, plan:\n%s", joined)
	}
}

func TestRunnerRequestProfileListIndexSupportsNewestPageOrder(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}

	const indexName = "idx_runner_requests_profile_queued_id"
	if !db.Migrator().HasIndex(&runnerRequestRecord{}, indexName) {
		t.Fatalf("expected runner request profile ordering index %s", indexName)
	}

	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	if err := db.Raw(`
		EXPLAIN QUERY PLAN
		SELECT id, queued_at
		FROM runner_requests
		WHERE profile_name = ?
		  AND queued_at >= ?
		  AND queued_at < ?
		ORDER BY queued_at DESC, id ASC
		LIMIT 5
	`, "github-runner-ubuntu-24-04", time.Now().Add(-30*24*time.Hour), time.Now()).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, step := range plan {
		details = append(details, step.Detail)
	}
	joined := strings.Join(details, "\n")
	if !strings.Contains(joined, indexName) {
		t.Fatalf("expected profile list query to use ordering index, plan:\n%s", joined)
	}
	if strings.Contains(joined, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("profile list query still sorts through a temporary B-tree, plan:\n%s", joined)
	}
}

func TestRunnerRequestAuthorizedListIndexSupportsNewestPageOrder(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}

	const indexName = "idx_runner_requests_github_installation_queued_id"
	if !db.Migrator().HasIndex(&runnerRequestRecord{}, indexName) {
		t.Fatalf("expected authorized runner request list ordering index %s", indexName)
	}

	var plan []struct {
		Detail string `gorm:"column:detail"`
	}
	if err := db.Raw(`
		EXPLAIN QUERY PLAN
		SELECT id, queued_at
		FROM runner_requests
		WHERE github_installation_id = ?
		  AND LOWER(repository_full_name) IN ?
		ORDER BY queued_at DESC, id ASC
		LIMIT 100
	`, 987, []string{"o/first", "o/second"}).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var details []string
	for _, step := range plan {
		details = append(details, step.Detail)
	}
	joined := strings.Join(details, "\n")
	if !strings.Contains(joined, indexName) {
		t.Fatalf("expected authorized list query to use ordering index, plan:\n%s", joined)
	}
	if strings.Contains(joined, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("authorized list query still sorts through a temporary B-tree, plan:\n%s", joined)
	}
}

func TestMigrateAddsGitHubInstallationLookupIndex(t *testing.T) {
	databaseURL := t.TempDir() + "/runnerd.db"
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: false,
	}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE github_installations (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		account_id INTEGER NOT NULL,
		installation_id INTEGER NOT NULL,
		github_account_id INTEGER,
		account_type TEXT,
		account_login TEXT NOT NULL,
		account_name TEXT,
		account_avatar TEXT,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE UNIQUE INDEX idx_github_installations_account_installation ON github_installations (account_id, installation_id)`).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	migrated := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	migratedDB, err := migrated.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if !migratedDB.Migrator().HasIndex(&githubInstallationRecord{}, "idx_github_installations_installation") {
		t.Fatal("expected installation_id lookup index after migration")
	}
}

func TestMigrateAddsRunnerEventCursorIndexes(t *testing.T) {
	databaseURL := t.TempDir() + "/runnerd.db"
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: false,
	}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE runner_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		request_id TEXT NOT NULL,
		event_type TEXT NOT NULL,
		stage TEXT,
		message TEXT,
		payload_json TEXT,
		created_at TIMESTAMP NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE INDEX idx_runner_events_request_created ON runner_events (request_id, created_at)`).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	migrated := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	migratedDB, err := migrated.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, indexName := range []string{"idx_runner_events_request_id", "idx_runner_events_request_type_id"} {
		if !migratedDB.Migrator().HasIndex(&runnerEventRecord{}, indexName) {
			t.Fatalf("expected runner event cursor index %s after migration", indexName)
		}
	}

	for _, tc := range []struct {
		name      string
		query     string
		args      []any
		indexName string
	}{
		{
			name:      "mixed event page",
			query:     `EXPLAIN QUERY PLAN SELECT * FROM runner_events WHERE request_id = ? AND id < ? ORDER BY id DESC LIMIT 201`,
			args:      []any{"request-1", 5000},
			indexName: "idx_runner_events_request_id",
		},
		{
			name:      "filtered control tail",
			query:     `EXPLAIN QUERY PLAN SELECT * FROM runner_events WHERE request_id = ? AND event_type IN (?) ORDER BY id DESC LIMIT 201`,
			args:      []any{"request-1", "control_log"},
			indexName: "idx_runner_events_request_type_id",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var plan []struct {
				Detail string
			}
			if err := migratedDB.Raw(tc.query, tc.args...).Scan(&plan).Error; err != nil {
				t.Fatal(err)
			}
			var details []string
			for _, step := range plan {
				details = append(details, step.Detail)
			}
			joined := strings.Join(details, "\n")
			if !strings.Contains(joined, tc.indexName) {
				t.Fatalf("expected query to use %s, plan:\n%s", tc.indexName, joined)
			}
			if strings.Contains(joined, "USE TEMP B-TREE FOR ORDER BY") {
				t.Fatalf("runner event query still sorts through a temporary B-tree, plan:\n%s", joined)
			}
		})
	}
}

func TestLargePayloadColumnsUseTextType(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}

	for table, columns := range map[string][]string{
		"runner_requests": {"github_payload_json"},
		"runner_events":   {"message", "payload_json"},
		"audit_events":    {"payload_json"},
	} {
		for _, column := range columns {
			var info struct {
				Type string
			}
			if err := db.Raw("SELECT type FROM pragma_table_info(?) WHERE name = ?", table, column).Scan(&info).Error; err != nil {
				t.Fatal(err)
			}
			if !strings.EqualFold(info.Type, "text") {
				t.Fatalf("%s.%s type = %q, want text", table, column, info.Type)
			}
		}
	}
}

func TestEnsureAccountForOAuthIdentityConcurrentCreateIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	const workers = 12
	stores := make([]Store, 0, workers)
	for i := 0; i < workers; i++ {
		store := New(dir)
		if err := store.Ensure(); err != nil {
			t.Fatal(err)
		}
		stores = append(stores, store)
	}
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	type accountIdentity struct {
		account  Account
		identity OAuthIdentity
	}
	results := make(chan accountIdentity, workers)

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(store Store) {
			defer wg.Done()
			account, identity, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "octocat"}, "user")
			if err != nil {
				errs <- err
				return
			}
			results <- accountIdentity{account: account, identity: identity}
		}(stores[i])
	}
	wg.Wait()
	close(errs)
	close(results)

	for err := range errs {
		t.Fatalf("expected concurrent ensure to be idempotent, got %v", err)
	}
	var first accountIdentity
	for result := range results {
		if first.identity.ID == 0 {
			first = result
			continue
		}
		if result.identity.ID != first.identity.ID || result.account.ID != first.account.ID || result.account.Role != "user" {
			t.Fatalf("expected same identity/account from concurrent ensure, first=%#v result=%#v", first, result)
		}
	}
}

func TestUpsertAccountForOAuthIdentityRejectsInvalidRole(t *testing.T) {
	store := New(t.TempDir())
	if _, _, err := store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "octocat"}, "owner"); err == nil {
		t.Fatal("expected invalid role error")
	}
}

func TestIsTransientStoreErrorRecognizesPostgresSQLSTATE(t *testing.T) {
	for _, message := range []string{
		"ERROR: transaction failed (SQLSTATE 40001)",
		"ERROR: transaction failed (SQLSTATE 40P01)",
		"Error 1213 (40001): Deadlock found when trying to get lock; try restarting transaction",
		"Error 1205 (HY000): Lock wait timeout exceeded; try restarting transaction",
	} {
		t.Run(message, func(t *testing.T) {
			if !isTransientStoreError(errors.New(message)) {
				t.Fatalf("expected transient store error for %q", message)
			}
		})
	}
}

func TestWriteStateUsesVersionCAS(t *testing.T) {
	store := New(t.TempDir())
	_, st, err := store.CreateRequest(RunnerRequest{
		ID:         "123",
		Source:     "test",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-123",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	st.Status = StatusRunning
	st.ProcessPID = 42
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	st.Status = StatusFailed
	err = store.WriteState(st)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected version conflict, got %v", err)
	}
}

func TestListStatesAndActiveCount(t *testing.T) {
	store := New(t.TempDir())
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID:         "active",
		Source:     "test",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-active",
	}, nil); err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if _, st, err := store.CreateRequest(RunnerRequest{
		ID:         "done",
		Source:     "test",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-done",
	}, nil); err != nil {
		t.Fatal(err)
	} else {
		st.Status = StatusCompleted
		if err := store.WriteState(st); err != nil {
			t.Fatal(err)
		}
	}

	states, err := store.ListStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 2 {
		t.Fatalf("expected 2 states, got %d", len(states))
	}
	if states[0].ID != "done" {
		t.Fatalf("expected newest state first, got %q", states[0].ID)
	}
	active, err := store.ActiveCount()
	if err != nil {
		t.Fatal(err)
	}
	if active != 1 {
		t.Fatalf("expected 1 active runner, got %d", active)
	}
}

func TestListStatesPage(t *testing.T) {
	store := New(t.TempDir())
	for i := 0; i < 5; i++ {
		if _, _, err := store.CreateRequest(RunnerRequest{
			ID:         fmt.Sprintf("runner-%d", i),
			Source:     "test",
			Labels:     []string{"self-hosted"},
			RunnerName: fmt.Sprintf("e2b-runner-%d", i),
			CreatedAt:  time.Unix(int64(i), 0).UTC(),
		}, nil); err != nil {
			t.Fatal(err)
		}
	}

	paged, total, err := store.ListStatesPage(2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if len(paged) != 2 {
		t.Fatalf("len = %d, want 2", len(paged))
	}
	if paged[0].ID != "runner-3" || paged[1].ID != "runner-2" {
		t.Fatalf("unexpected page order: %#v", []string{paged[0].ID, paged[1].ID})
	}
}

func TestRunnerRequestListProjectionExcludesHeavyAndSecretColumns(t *testing.T) {
	projection := "," + strings.Join(runnerRequestListSelectColumns, ",") + ","
	for _, excluded := range []string{
		"github_payload_json",
		"labels_json",
		"sandbox_api_url",
		"sandbox_api_key_encrypted",
	} {
		if strings.Contains(projection, ","+excluded+",") {
			t.Fatalf("list projection must not read %s: %s", excluded, projection)
		}
	}
	for _, required := range []string{
		"id",
		"status",
		"github_installation_id",
		"repository_full_name",
		"workflow_run_id",
		"github_job_url",
		"queued_at",
		"updated_at",
	} {
		if !strings.Contains(projection, ","+required+",") {
			t.Fatalf("list projection must retain %s: %s", required, projection)
		}
	}
}

func TestGitHubInstallationsAndRepositoryRunnerList(t *testing.T) {
	store := New(t.TempDir())
	account, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "octocat"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	installation, err := store.UpsertGitHubInstallation(GitHubInstallation{
		AccountID:      account.ID,
		InstallationID: 987,
		AccountLogin:   "o",
		AccountName:    "Octo Org",
		AccountAvatar:  "https://avatars.example/o.png",
		Repositories:   []string{"o/r", "o/another", "bad*", "missing-slash"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(installation.Repositories) != 0 {
		t.Fatalf("upsert should not persist the full installation repository scope, got %#v", installation.Repositories)
	}
	if _, err := store.UpsertGitHubInstallation(GitHubInstallation{
		AccountID:      account.ID,
		InstallationID: 987,
		AccountLogin:   "renamed",
		AccountName:    "Renamed Org",
		AccountAvatar:  "https://avatars.example/renamed.png",
		Repositories:   []string{"o/r"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID:                   "visible",
		Source:               "test",
		GitHubInstallationID: 987,
		RepositoryFullName:   "o/r",
		Labels:               []string{"self-hosted"},
		RunnerName:           "e2b-visible",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID:                   "hidden",
		Source:               "test",
		GitHubInstallationID: 456,
		RepositoryFullName:   "other/r",
		Labels:               []string{"self-hosted"},
		RunnerName:           "e2b-hidden",
	}, nil); err != nil {
		t.Fatal(err)
	}
	states, err := store.ListStatesForRepositories([]string{"o/r"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ID != "visible" {
		t.Fatalf("unexpected filtered states: %#v", states)
	}
	states, err = store.ListStatesForGitHubInstallations([]int64{987}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ID != "visible" {
		t.Fatalf("unexpected installation-filtered states: %#v", states)
	}
	installations, err := store.ListGitHubInstallations(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 1 ||
		installations[0].AccountLogin != "renamed" ||
		installations[0].AccountName != "Renamed Org" ||
		installations[0].AccountAvatar != "https://avatars.example/renamed.png" ||
		!reflect.DeepEqual(installations[0].Repositories, []string{"o/r"}) {
		t.Fatalf("unexpected installations: %#v", installations)
	}
	if err := store.DeleteGitHubInstallation(account.ID, installations[0].ID); err != nil {
		t.Fatal(err)
	}
	installations, err = store.ListGitHubInstallations(account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 0 {
		t.Fatalf("expected installation to be deleted, got %#v", installations)
	}
}

func TestListStatesForGitHubInstallationRepositoriesPreservesPairs(t *testing.T) {
	store := New(t.TempDir())
	requests := []RunnerRequest{
		{ID: "first-visible", Source: "test", GitHubInstallationID: 987, RepositoryFullName: "o/first"},
		{ID: "first-cross-pair", Source: "test", GitHubInstallationID: 987, RepositoryFullName: "other/second"},
		{ID: "second-visible", Source: "test", GitHubInstallationID: 654, RepositoryFullName: "other/second"},
		{ID: "second-cross-pair", Source: "test", GitHubInstallationID: 654, RepositoryFullName: "o/first"},
	}
	for _, request := range requests {
		if _, _, err := store.CreateRequest(request, nil); err != nil {
			t.Fatal(err)
		}
	}

	states, _, err := store.ListStatesForGitHubInstallationRepositories([]GitHubInstallationRepositoryAccess{
		{InstallationID: 987, Repositories: []string{"o/first"}},
		{InstallationID: 654, Repositories: []string{"other/second"}},
	}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(states))
	for _, runnerState := range states {
		ids = append(ids, runnerState.ID)
	}
	sort.Strings(ids)
	if !reflect.DeepEqual(ids, []string{"first-visible", "second-visible"}) {
		t.Fatalf("unexpected pair-filtered states: %#v", ids)
	}
}

func TestListStatesForGitHubInstallationRepositoriesMatchesRepositoryCaseInsensitively(t *testing.T) {
	store := New(t.TempDir())
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID:                   "visible",
		Source:               "test",
		GitHubInstallationID: 987,
		RepositoryFullName:   "Octo/Visible",
	}, nil); err != nil {
		t.Fatal(err)
	}

	states, _, err := store.ListStatesForGitHubInstallationRepositories([]GitHubInstallationRepositoryAccess{
		{InstallationID: 987, Repositories: []string{"octo/visible"}},
	}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ID != "visible" {
		t.Fatalf("expected case-insensitive repository match, got %#v", states)
	}
}

func TestListStatesForGitHubInstallationRepositoriesFiltersBeforeLimit(t *testing.T) {
	store := New(t.TempDir())
	now := time.Now().UTC()
	for _, request := range []RunnerRequest{
		{
			ID:                   "authorized-older",
			Source:               "test",
			GitHubInstallationID: 987,
			RepositoryFullName:   "o/visible",
			CreatedAt:            now.Add(-time.Minute),
		},
		{
			ID:                   "unauthorized-newer",
			Source:               "test",
			GitHubInstallationID: 987,
			RepositoryFullName:   "o/hidden",
			CreatedAt:            now,
		},
	} {
		if _, _, err := store.CreateRequest(request, nil); err != nil {
			t.Fatal(err)
		}
	}

	states, _, err := store.ListStatesForGitHubInstallationRepositories([]GitHubInstallationRepositoryAccess{
		{InstallationID: 987, Repositories: []string{"o/visible"}},
	}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ID != "authorized-older" {
		t.Fatalf("expected authorization filter before limit, got %#v", states)
	}
}

func TestGitHubInstallationRepositoryAccessQueryBatchesStayWithinParameterLimit(t *testing.T) {
	repositories := make([]string, 0, 1200)
	for index := range 1200 {
		repositories = append(repositories, fmt.Sprintf("o/repo-%d", index))
	}

	batches := githubInstallationRepositoryAccessQueryBatches([]GitHubInstallationRepositoryAccess{
		{InstallationID: 987, Repositories: repositories},
	}, maxRunnerRequestRepositoryAccessQueryParameters)
	if len(batches) < 2 {
		t.Fatalf("expected large repository access to be split, got %d batch", len(batches))
	}
	for index, batch := range batches {
		if batch.parameterCount > maxRunnerRequestRepositoryAccessQueryParameters {
			t.Fatalf("batch %d has %d parameters, limit %d", index, batch.parameterCount, maxRunnerRequestRepositoryAccessQueryParameters)
		}
	}
}

func TestGitHubInstallationRepositoryAccessQueryBatchesSeparateInstallations(t *testing.T) {
	batches := githubInstallationRepositoryAccessQueryBatches([]GitHubInstallationRepositoryAccess{
		{InstallationID: 987, Repositories: []string{"o/first", "o/second"}},
		{InstallationID: 654, Repositories: []string{"other/visible"}},
	}, maxRunnerRequestRepositoryAccessQueryParameters)
	if len(batches) != 2 {
		t.Fatalf("expected one ordered query per installation, got %d batches", len(batches))
	}
	for index, batch := range batches {
		if len(batch.predicates) != 1 {
			t.Fatalf("batch %d combines %d authorization predicates", index, len(batch.predicates))
		}
	}
}

func TestListStatesForGitHubInstallationRepositoriesHandlesLargeAccessSet(t *testing.T) {
	store := New(t.TempDir())
	now := time.Now().UTC()
	for _, request := range []RunnerRequest{
		{
			ID:                   "older-in-first-batch",
			Source:               "test",
			GitHubInstallationID: 987,
			RepositoryFullName:   "o/repo-0",
			CreatedAt:            now.Add(-time.Minute),
		},
		{
			ID:                   "newer-in-later-batch",
			Source:               "test",
			GitHubInstallationID: 987,
			RepositoryFullName:   "o/repo-1199",
			CreatedAt:            now,
		},
	} {
		if _, _, err := store.CreateRequest(request, nil); err != nil {
			t.Fatal(err)
		}
	}
	repositories := make([]string, 0, 1200)
	for index := range 1200 {
		repositories = append(repositories, fmt.Sprintf("o/repo-%d", index))
	}

	states, _, err := store.ListStatesForGitHubInstallationRepositories([]GitHubInstallationRepositoryAccess{
		{InstallationID: 987, Repositories: repositories},
	}, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].ID != "newer-in-later-batch" {
		t.Fatalf("expected global newest state from a later query batch, got %#v", states)
	}
}

func TestListStatesForGitHubInstallationRepositoriesPaginatesAcrossBatches(t *testing.T) {
	store := New(t.TempDir())
	now := time.Now().UTC()
	for _, request := range []RunnerRequest{
		{
			ID:                   "older-first-batch",
			Source:               "test",
			GitHubInstallationID: 987,
			RepositoryFullName:   "o/repo-0",
			CreatedAt:            now.Add(-2 * time.Minute),
		},
		{
			ID:                   "middle-other-installation",
			Source:               "test",
			GitHubInstallationID: 654,
			RepositoryFullName:   "other/visible",
			CreatedAt:            now.Add(-time.Minute),
		},
		{
			ID:                   "newer-later-batch",
			Source:               "test",
			GitHubInstallationID: 987,
			RepositoryFullName:   "o/repo-900",
			CreatedAt:            now,
		},
	} {
		if _, _, err := store.CreateRequest(request, nil); err != nil {
			t.Fatal(err)
		}
	}
	repositories := make([]string, 0, 901)
	for index := range 901 {
		repositories = append(repositories, fmt.Sprintf("o/repo-%d", index))
	}

	states, total, err := store.ListStatesForGitHubInstallationRepositories([]GitHubInstallationRepositoryAccess{
		{InstallationID: 987, Repositories: repositories},
		{InstallationID: 654, Repositories: []string{"other/visible"}},
	}, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if len(states) != 1 || states[0].ID != "middle-other-installation" {
		t.Fatalf("unexpected second page: %#v", states)
	}
}

func TestGitHubInstallationsAllowSharedOrgInstallationAcrossAccounts(t *testing.T) {
	store := New(t.TempDir())
	first, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "100", OAuthLogin: "alice"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "200", OAuthLogin: "bob"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range []Account{first, second} {
		if _, err := store.UpsertGitHubInstallation(GitHubInstallation{
			AccountID:      account.ID,
			InstallationID: 987,
			AccountLogin:   "shared-org",
			Repositories:   []string{"shared-org/repo"},
		}); err != nil {
			t.Fatal(err)
		}
	}
	firstInstallations, err := store.ListGitHubInstallations(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondInstallations, err := store.ListGitHubInstallations(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstInstallations) != 1 || len(secondInstallations) != 1 {
		t.Fatalf("expected both users to keep their own installation link, first=%#v second=%#v", firstInstallations, secondInstallations)
	}
	if firstInstallations[0].ID == secondInstallations[0].ID {
		t.Fatalf("expected separate local installation rows for shared GitHub installation, got %#v", firstInstallations[0])
	}
	if err := store.DeleteGitHubInstallation(first.ID, firstInstallations[0].ID); err != nil {
		t.Fatal(err)
	}
	firstInstallations, err = store.ListGitHubInstallations(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	secondInstallations, err = store.ListGitHubInstallations(second.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstInstallations) != 0 || len(secondInstallations) != 1 {
		t.Fatalf("expected deleting one user's link to preserve collaborator link, first=%#v second=%#v", firstInstallations, secondInstallations)
	}
}

func TestGitHubInstallationAccountIdentityRoundTripAndLookup(t *testing.T) {
	store := New(t.TempDir())
	first, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "100", OAuthLogin: "alice"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "200", OAuthLogin: "bob"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	for _, accountID := range []int64{first.ID, second.ID} {
		if _, err := store.UpsertGitHubInstallation(GitHubInstallation{
			AccountID:       accountID,
			InstallationID:  987,
			GitHubAccountID: 9001,
			AccountType:     "Organization",
			AccountLogin:    "Octo-Org",
			AccountName:     "Octo Org",
			AccountAvatar:   "https://avatars.example/o.png",
		}); err != nil {
			t.Fatal(err)
		}
	}

	installations, err := store.ListGitHubInstallations(first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 1 || installations[0].GitHubAccountID != 9001 || installations[0].AccountType != "organization" {
		t.Fatalf("unexpected installation identity: %#v", installations)
	}
	accounts, err := store.ListGitHubInstallationAccounts()
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 || accounts[0].GitHubAccountID != 9001 || accounts[0].AccountLogin != "Octo-Org" {
		t.Fatalf("unexpected installation accounts: %#v", accounts)
	}
	byInstallation, err := store.GitHubInstallationAccountForInstallation(987)
	if err != nil || byInstallation.GitHubAccountID != 9001 {
		t.Fatalf("lookup by installation: account=%#v err=%v", byInstallation, err)
	}
	byLogin, err := store.GitHubInstallationAccountForLogin("octo-org")
	if err != nil || byLogin.GitHubAccountID != 9001 {
		t.Fatalf("lookup by login: account=%#v err=%v", byLogin, err)
	}
}

func TestUpsertGitHubInstallationRollsBackWhenOwnerCacheFails(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	account, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "100", OAuthLogin: "alice"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropTable(&githubInstallationOwnerRecord{}); err != nil {
		t.Fatal(err)
	}

	if _, err := store.UpsertGitHubInstallation(GitHubInstallation{
		AccountID:       account.ID,
		InstallationID:  987,
		GitHubAccountID: 9001,
		AccountType:     "organization",
		AccountLogin:    "octo-org",
	}); err == nil {
		t.Fatal("UpsertGitHubInstallation() error = nil, want owner cache failure")
	}
	var count int64
	if err := db.Model(&githubInstallationRecord{}).
		Where("account_id = ? AND installation_id = ?", account.ID, 987).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("installation rows = %d, want transaction rollback", count)
	}
}

func TestAccountScopeForPersonalGitHubInstallation(t *testing.T) {
	store := New(t.TempDir())
	account, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "100", OAuthLogin: "alice"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertGitHubInstallation(GitHubInstallation{
		AccountID:      account.ID,
		InstallationID: 987,
		AccountLogin:   "alice",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertGitHubInstallation(GitHubInstallation{
		AccountID:      account.ID,
		InstallationID: 456,
		AccountLogin:   "alice-org",
	}); err != nil {
		t.Fatal(err)
	}

	accountID, ok, err := store.AccountScopeForPersonalGitHubInstallation(987)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || accountID != account.ID {
		t.Fatalf("expected personal installation to resolve account scope, account_id=%d ok=%v", accountID, ok)
	}
	if accountID, ok, err := store.AccountScopeForPersonalGitHubInstallation(456); err != nil || ok || accountID != 0 {
		t.Fatalf("expected org installation not to resolve account scope, account_id=%d ok=%v err=%v", accountID, ok, err)
	}
	installationID, ok, err := store.GitHubInstallationScopeForAccountLogin("ALICE-ORG")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || installationID != 456 {
		t.Fatalf("expected account login to resolve installation scope, installation_id=%d ok=%v", installationID, ok)
	}
	if installationID, ok, err := store.GitHubInstallationScopeForAccountLogin("missing"); err != nil || ok || installationID != 0 {
		t.Fatalf("expected missing account login not to resolve, installation_id=%d ok=%v err=%v", installationID, ok, err)
	}
}

func TestAccountSecretsAreScopedToAccountAndType(t *testing.T) {
	store := New(t.TempDir())
	first, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "100", OAuthLogin: "alice"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "200", OAuthLogin: "bob"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountSecret(AccountSecret{
		ScopeType:      AccountScopeTypeAccount,
		ScopeID:        first.ID,
		KeyType:        AccountSecretTypeSandboxAPIKey,
		EncryptedValue: "v1:first",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountSecret(AccountSecret{
		ScopeType:      AccountScopeTypeAccount,
		ScopeID:        second.ID,
		KeyType:        AccountSecretTypeSandboxAPIKey,
		EncryptedValue: "v1:second",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountSecret(AccountSecret{
		ScopeType:      AccountScopeTypeAccount,
		ScopeID:        first.ID,
		KeyType:        "other_api_key",
		EncryptedValue: "v1:first-other",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountSecret(AccountSecret{
		ScopeType:      AccountScopeTypeAccount,
		ScopeID:        first.ID,
		KeyType:        AccountSecretTypeSandboxAPIKey,
		EncryptedValue: "v1:first-updated",
	}); err != nil {
		t.Fatal(err)
	}
	firstKey, err := store.GetAccountSecret(AccountScopeTypeAccount, first.ID, AccountSecretTypeSandboxAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := store.GetAccountSecret(AccountScopeTypeAccount, second.ID, AccountSecretTypeSandboxAPIKey)
	if err != nil {
		t.Fatal(err)
	}
	otherKey, err := store.GetAccountSecret(AccountScopeTypeAccount, first.ID, "other_api_key")
	if err != nil {
		t.Fatal(err)
	}
	if firstKey.EncryptedValue != "v1:first-updated" || secondKey.EncryptedValue != "v1:second" || otherKey.EncryptedValue != "v1:first-other" {
		t.Fatalf("unexpected sandbox api keys: first=%#v second=%#v", firstKey, secondKey)
	}
	if err := store.DeleteAccountSecret(AccountScopeTypeAccount, first.ID, AccountSecretTypeSandboxAPIKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAccountSecret(AccountScopeTypeAccount, first.ID, AccountSecretTypeSandboxAPIKey); err != ErrNotFound {
		t.Fatalf("expected first key to be deleted, got %v", err)
	}
	if _, err := store.GetAccountSecret(AccountScopeTypeAccount, second.ID, AccountSecretTypeSandboxAPIKey); err != nil {
		t.Fatalf("second key should remain: %v", err)
	}
	if _, err := store.GetAccountSecret(AccountScopeTypeAccount, first.ID, "other_api_key"); err != nil {
		t.Fatalf("other key should remain: %v", err)
	}
}

func TestAccountPreferencesAreScopedToAccountNamespaceAndKey(t *testing.T) {
	store := New(t.TempDir())
	first, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "100", OAuthLogin: "alice"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "200", OAuthLogin: "bob"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountPreference(AccountPreference{
		ScopeType: AccountScopeTypeAccount,
		ScopeID:   first.ID,
		Namespace: "Sandbox",
		Key:       "Service",
		ValueJSON: `{"api_url":"https://sandbox-one.example.test"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountPreference(AccountPreference{
		ScopeType: AccountScopeTypeAccount,
		ScopeID:   second.ID,
		Namespace: "sandbox",
		Key:       "service",
		ValueJSON: `{"api_url":"https://sandbox-two.example.test"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountPreference(AccountPreference{
		ScopeType: AccountScopeTypeAccount,
		ScopeID:   first.ID,
		Namespace: "sandbox",
		Key:       "other",
		ValueJSON: `{"enabled":true}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountPreference(AccountPreference{
		ScopeType: AccountScopeTypeAccount,
		ScopeID:   first.ID,
		Namespace: "sandbox",
		Key:       "service",
		ValueJSON: `{"api_url":"https://sandbox-one-updated.example.test"}`,
	}); err != nil {
		t.Fatal(err)
	}

	firstConfig, err := store.GetAccountPreference(AccountScopeTypeAccount, first.ID, "sandbox", "service")
	if err != nil {
		t.Fatal(err)
	}
	secondConfig, err := store.GetAccountPreference(AccountScopeTypeAccount, second.ID, "sandbox", "service")
	if err != nil {
		t.Fatal(err)
	}
	otherPreference, err := store.GetAccountPreference(AccountScopeTypeAccount, first.ID, "sandbox", "other")
	if err != nil {
		t.Fatal(err)
	}
	if firstConfig.ValueJSON != `{"api_url":"https://sandbox-one-updated.example.test"}` || secondConfig.ValueJSON != `{"api_url":"https://sandbox-two.example.test"}` || otherPreference.ValueJSON != `{"enabled":true}` {
		t.Fatalf("unexpected account preferences: first=%#v second=%#v other=%#v", firstConfig, secondConfig, otherPreference)
	}
}

func TestUpsertAccountPreferenceAndSecretRollsBackTogether(t *testing.T) {
	store := New(t.TempDir())
	account, _, err := store.EnsureAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "100", OAuthLogin: "alice"}, "user")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = store.UpsertAccountPreferenceAndSecret(AccountPreference{
		ScopeType: AccountScopeTypeAccount,
		ScopeID:   account.ID,
		Namespace: "sandbox",
		Key:       "service",
		ValueJSON: `{"api_url":"https://sandbox.example.test"}`,
	}, &AccountSecret{
		ScopeType:      AccountScopeTypeAccount,
		ScopeID:        account.ID,
		KeyType:        AccountSecretTypeSandboxAPIKey,
		EncryptedValue: "",
	})
	if err == nil {
		t.Fatal("expected invalid secret to fail")
	}
	if _, err := store.GetAccountPreference(AccountScopeTypeAccount, account.ID, "sandbox", "service"); err != ErrNotFound {
		t.Fatalf("expected preference rollback, got %v", err)
	}
	if _, err := store.GetAccountSecret(AccountScopeTypeAccount, account.ID, AccountSecretTypeSandboxAPIKey); err != ErrNotFound {
		t.Fatalf("expected secret rollback, got %v", err)
	}
}

func TestListActiveStatesExcludesTerminalStates(t *testing.T) {
	store := New(t.TempDir())
	for _, tc := range []struct {
		id     string
		status string
	}{
		{id: "queued", status: StatusQueued},
		{id: "running", status: StatusRunning},
		{id: "completed", status: StatusCompleted},
		{id: "failed", status: StatusFailed},
	} {
		if _, st, err := store.CreateRequest(RunnerRequest{
			ID:         tc.id,
			Source:     "test",
			Labels:     []string{"self-hosted"},
			RunnerName: "e2b-" + tc.id,
		}, nil); err != nil {
			t.Fatal(err)
		} else if tc.status != StatusQueued {
			st.Status = tc.status
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
		}
	}

	states, err := store.ListActiveStates()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, st := range states {
		got[st.ID] = true
	}
	if !got["queued"] || !got["running"] {
		t.Fatalf("active states missing expected rows: %#v", got)
	}
	if got["completed"] || got["failed"] {
		t.Fatalf("active states included terminal rows: %#v", got)
	}
}

func TestReadStateReturnsErrNotFound(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.ReadState("missing-request"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ReadState missing request error = %v, want ErrNotFound", err)
	}
}

func TestReadLogCanReturnTail(t *testing.T) {
	store := New(t.TempDir())
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID:         "123",
		Source:     "test",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-123",
	}, nil); err != nil {
		t.Fatal(err)
	}
	store.AppendLog("123", "control.log", []byte("line-1\n"))
	store.AppendLog("123", "control.log", []byte("line-2\n"))
	data, err := store.ReadLog("123", "control.log", 7)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "line-2\n" {
		t.Fatalf("unexpected log tail: %q", string(data))
	}
}

func TestAppendLogSanitizesRequestIDConsistentlyWithReaders(t *testing.T) {
	store := New(t.TempDir())
	requestID := " ../unsafe/request "
	store.AppendLog(requestID, "stdout.log", []byte("runner output\n"))

	events, _, err := store.ListRunnerEvents(requestID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Message != "runner output\n" {
		t.Fatalf("events = %#v, want the event written under the sanitized request ID", events)
	}
	logData, err := store.ReadLog(requestID, "stdout.log", 0)
	if err != nil {
		t.Fatal(err)
	}
	if string(logData) != "runner output\n" {
		t.Fatalf("log = %q, want runner output", logData)
	}
}

func TestListRunnerEventsReturnsBoundedChronologicalTail(t *testing.T) {
	store := New(t.TempDir())
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID:         "diagnostic-events",
		Source:     "test",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-diagnostic-events",
	}, nil); err != nil {
		t.Fatal(err)
	}
	store.AppendLog("diagnostic-events", "control.log", []byte("created\n"))
	store.AppendLog("diagnostic-events", "stdout.log", []byte("connected\n"))
	store.AppendLog("diagnostic-events", "control.log", []byte("accepted\n"))

	events, truncated, err := store.ListRunnerEvents("diagnostic-events", 0, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("expected truncated event history")
	}
	if len(events) != 2 || events[0].Message != "connected\n" || events[1].Message != "accepted\n" {
		t.Fatalf("events = %#v, want chronological two-event tail", events)
	}
}

func TestListRunnerEventsFiltersBeforeApplyingLimit(t *testing.T) {
	store := New(t.TempDir())
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID:         "diagnostic-control-events",
		Source:     "test",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-diagnostic-control-events",
	}, nil); err != nil {
		t.Fatal(err)
	}
	store.AppendLog("diagnostic-control-events", "control.log", []byte("created\n"))
	store.AppendLog("diagnostic-control-events", "stdout.log", []byte("noisy output 1\n"))
	store.AppendLog("diagnostic-control-events", "stdout.log", []byte("noisy output 2\n"))
	store.AppendLog("diagnostic-control-events", "control.log", []byte("accepted\n"))
	store.AppendLog("diagnostic-control-events", "control.log", []byte("completed\n"))

	events, truncated, err := store.ListRunnerEvents("diagnostic-control-events", 0, 2, "control_log")
	if err != nil {
		t.Fatal(err)
	}
	if !truncated {
		t.Fatal("expected filtered control event history to be truncated")
	}
	if len(events) != 2 || events[0].Message != "accepted\n" || events[1].Message != "completed\n" {
		t.Fatalf("events = %#v, want chronological control-event tail", events)
	}
}

func TestListRunnerEventsUsesExclusiveBeforeIDCursor(t *testing.T) {
	store := New(t.TempDir())
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID:         "diagnostic-event-pages",
		Source:     "test",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-diagnostic-event-pages",
	}, nil); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		name    string
		message string
	}{
		{name: "control.log", message: "one\n"},
		{name: "stdout.log", message: "two\n"},
		{name: "stderr.log", message: "three\n"},
		{name: "control.log", message: "four\n"},
		{name: "stdout.log", message: "five\n"},
	} {
		store.AppendLog("diagnostic-event-pages", entry.name, []byte(entry.message))
	}

	allEvents, _, err := store.ListRunnerEvents("diagnostic-event-pages", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(allEvents) != 5 {
		t.Fatalf("all events = %#v, want five records", allEvents)
	}

	events, hasMore, err := store.ListRunnerEvents("diagnostic-event-pages", allEvents[4].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore {
		t.Fatal("expected older event page to report more records")
	}
	if len(events) != 2 || events[0].Message != "three\n" || events[1].Message != "four\n" {
		t.Fatalf("events = %#v, want chronological page before exclusive cursor", events)
	}
}

func TestListRunnerEventsAfterUsesExclusiveCursor(t *testing.T) {
	store := New(t.TempDir())
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID:         "diagnostic-forward-event-pages",
		Source:     "test",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-diagnostic-forward-event-pages",
	}, nil); err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"one\n", "two\n", "three\n", "four\n", "five\n"} {
		store.AppendLog("diagnostic-forward-event-pages", "stdout.log", []byte(message))
	}

	allEvents, _, err := store.ListRunnerEvents("diagnostic-forward-event-pages", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	events, hasMore, err := store.ListRunnerEventsAfter("diagnostic-forward-event-pages", allEvents[1].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !hasMore {
		t.Fatal("expected newer event page to report more records")
	}
	if len(events) != 2 || events[0].Message != "three\n" || events[1].Message != "four\n" {
		t.Fatalf("events = %#v, want first chronological page after exclusive cursor", events)
	}

	events, hasMore, err = store.ListRunnerEventsAfter("diagnostic-forward-event-pages", events[1].ID, 2)
	if err != nil {
		t.Fatal(err)
	}
	if hasMore {
		t.Fatal("final newer event page unexpectedly reports more records")
	}
	if len(events) != 1 || events[0].Message != "five\n" {
		t.Fatalf("events = %#v, want final chronological event", events)
	}
}

func TestProfileConditionalSave(t *testing.T) {
	testProfileConditionalSave(t, New(t.TempDir()))
}

func testProfileConditionalSave(t *testing.T, store Store) {
	t.Helper()
	initial, err := store.UpsertProfileIfUnchanged(RunnerProfile{Name: "conditional-spec", TemplateID: "old", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	beforeAudit, err := store.ListAuditEvents(100)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.ApplyMutationWithAudit(AuditEvent{Actor: "test", Action: "profile.create", ResourceType: "runner_profile", ResourceID: initial.Name}, func(tx Store) error {
		_, err := tx.UpsertProfileIfUnchanged(RunnerProfile{Name: initial.Name, TemplateID: "overwrite"}, nil)
		return err
	})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate create = %v, want conflict", err)
	}
	unchanged, err := store.GetProfile(initial.Name)
	if err != nil || !reflect.DeepEqual(initial, unchanged) {
		t.Fatalf("duplicate create overwrote existing profile: %#v %v", unchanged, err)
	}
	afterAudit, err := store.ListAuditEvents(100)
	if err != nil || !reflect.DeepEqual(beforeAudit, afterAudit) {
		t.Fatal("duplicate create persisted audit")
	}
	// Put the stored revision just ahead of the clock so a conditional save
	// must explicitly advance it, including at MySQL's millisecond precision.
	db, err := store.(*DBStore).dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	revision := time.Now().UTC().Add(time.Minute).Truncate(time.Millisecond)
	if err := db.Model(&runnerProfileRecord{}).Where("name = ?", initial.Name).UpdateColumn("updated_at", revision).Error; err != nil {
		t.Fatal(err)
	}
	initial, err = store.GetProfile(initial.Name)
	if err != nil {
		t.Fatal(err)
	}
	updated := initial
	updated.Enabled = false
	updated, err = store.UpsertProfileIfUnchanged(updated, &initial.UpdatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if updated.UpdatedAt.Before(initial.UpdatedAt.Add(time.Millisecond)) {
		t.Fatalf("revision did not advance at database precision: before=%s after=%s", initial.UpdatedAt, updated.UpdatedAt)
	}
	previous := updated
	updated, err = store.UpsertProfileIfUnchanged(updated, &previous.UpdatedAt)
	if err != nil || !updated.UpdatedAt.After(previous.UpdatedAt) {
		t.Fatalf("unchanged values must still advance the revision: %#v %v", updated, err)
	}
	initial.TemplateID = "stale"
	if _, err := store.UpsertProfileIfUnchanged(initial, &initial.UpdatedAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update = %v, want conflict", err)
	}
	got, err := store.GetProfile(initial.Name)
	if err != nil || got.Enabled || got.TemplateID != "old" {
		t.Fatalf("stale update changed record: %#v %v", got, err)
	}
	if err := store.DeleteProfile(initial.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertProfileIfUnchanged(updated, &updated.UpdatedAt); !errors.Is(err, ErrConflict) {
		t.Fatalf("deleted update = %v, want conflict", err)
	}
	if _, err := store.GetProfile(initial.Name); !errors.Is(err, ErrNotFound) {
		t.Fatal("conditional update recreated deleted record")
	}
}

func TestApplyMutationWithAuditSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("set RUNNERD_CATALOG_BACKEND_TESTS=1 with dedicated Postgres and MySQL test databases")
	}
	for _, backend := range []struct {
		name string
		dsn  string
	}{
		{name: BackendPostgres, dsn: os.Getenv("RUNNERD_POSTGRES_TEST_DSN")},
		{name: BackendMySQL, dsn: os.Getenv("RUNNERD_MYSQL_TEST_DSN")},
	} {
		t.Run(backend.name, func(t *testing.T) {
			if strings.TrimSpace(backend.dsn) == "" {
				t.Fatalf("dedicated %s test DSN is required", backend.name)
			}
			setupStore := NewWithOptions(Options{
				Backend: backend.name, DatabaseDSN: backend.dsn, MigrateOnStart: false,
			}).(*DBStore)
			setupDB, err := setupStore.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			requireCatalogMatcherTestDatabase(t, setupDB, backend.name)
			resetSQLBackendTestTables(t, setupDB)
			defer func() {
				resetSQLBackendTestTables(t, setupDB)
				closeTestDB(t, setupDB)
			}()

			store := NewWithOptions(Options{
				Backend: backend.name, DatabaseDSN: backend.dsn, MigrateOnStart: true,
			}).(*DBStore)
			if err := store.Ensure(); err != nil {
				t.Fatalf("migrate %s state schema: %v", backend.name, err)
			}
			db, err := store.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestDB(t, db)
			if _, err := store.UpsertProfile(RunnerProfile{
				Name: "atomic-spec", Labels: []string{"self-hosted", "atomic"},
				RequiredLabels: []string{"atomic"}, TemplateID: "atomic-template", Enabled: true,
			}); err != nil {
				t.Fatal(err)
			}

			event, err := store.ApplyMutationWithAudit(AuditEvent{
				Actor: "admin_api", Action: "profile.update", ResourceType: "runner_profile", ResourceID: "atomic-spec",
			}, func(tx Store) error {
				profile, mutationErr := tx.GetProfile("atomic-spec")
				if mutationErr != nil {
					return mutationErr
				}
				profile.MaxConcurrency = 7
				_, mutationErr = tx.UpsertProfileIfUnchanged(profile, &profile.UpdatedAt)
				return mutationErr
			})
			if err != nil || event.ID == 0 {
				t.Fatalf("%s atomic profile mutation: event=%#v err=%v", backend.name, event, err)
			}
			profile, err := store.GetProfile("atomic-spec")
			if err != nil || profile.MaxConcurrency != 7 {
				t.Fatalf("%s atomic profile mutation not committed: profile=%#v err=%v", backend.name, profile, err)
			}

			testProfileConditionalSave(t, store)

			if err := db.Migrator().DropTable(&auditEventRecord{}); err != nil {
				t.Fatal(err)
			}
			_, err = store.ApplyMutationWithAudit(AuditEvent{
				Actor: "admin_api", Action: "profile.create", ResourceType: "runner_profile", ResourceID: "rolled-back-spec",
			}, func(tx Store) error {
				_, mutationErr := tx.UpsertProfileIfUnchanged(RunnerProfile{
					Name: "rolled-back-spec", Labels: []string{"self-hosted", "rollback"},
					RequiredLabels: []string{"rollback"}, TemplateID: "rollback-template", Enabled: true,
				}, nil)
				return mutationErr
			})
			if !errors.Is(err, ErrAuditEventPersistence) {
				t.Fatalf("%s audit failure = %v", backend.name, err)
			}
			if _, err := store.GetProfile("rolled-back-spec"); !errors.Is(err, ErrNotFound) {
				t.Fatalf("%s mutation committed despite audit failure: %v", backend.name, err)
			}
		})
	}
}

func TestFreshSchemaSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("set RUNNERD_CATALOG_BACKEND_TESTS=1 with dedicated Postgres and MySQL test databases")
	}
	for _, backend := range []struct {
		name string
		dsn  string
	}{
		{name: BackendPostgres, dsn: os.Getenv("RUNNERD_POSTGRES_TEST_DSN")},
		{name: BackendMySQL, dsn: os.Getenv("RUNNERD_MYSQL_TEST_DSN")},
	} {
		t.Run(backend.name, func(t *testing.T) {
			if strings.TrimSpace(backend.dsn) == "" {
				t.Fatalf("dedicated %s test DSN is required", backend.name)
			}
			setupStore := NewWithOptions(Options{
				Backend: backend.name, DatabaseDSN: backend.dsn, MigrateOnStart: false,
			}).(*DBStore)
			setupDB, err := setupStore.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			requireCatalogMatcherTestDatabase(t, setupDB, backend.name)
			resetSQLBackendTestTables(t, setupDB)
			defer func() {
				resetSQLBackendTestTables(t, setupDB)
				closeTestDB(t, setupDB)
			}()

			migrated := NewWithOptions(Options{
				Backend: backend.name, DatabaseDSN: backend.dsn, MigrateOnStart: true,
			}).(*DBStore)
			if err := migrated.Ensure(); err != nil {
				t.Fatalf("fresh %s migration failed: %v", backend.name, err)
			}
			migratedDB, err := migrated.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestDB(t, migratedDB)
			for _, table := range sqlBackendTestTables() {
				if table == "runner_groups" || table == "runner_group_specs" || table == "repository_policies" {
					if migratedDB.Migrator().HasTable(table) {
						t.Fatalf("fresh %s Release C migration unexpectedly created %s", backend.name, table)
					}
					continue
				}
				if !migratedDB.Migrator().HasTable(table) {
					t.Fatalf("fresh %s migration did not create %s", backend.name, table)
				}
			}
			for _, index := range []struct {
				model any
				name  string
			}{
				{model: &oauthIdentityRecord{}, name: "idx_oauth_identities_provider_subject"},
				{model: &accountSecretRecord{}, name: "idx_account_secrets_scope_type"},
				{model: &accountPreferenceRecord{}, name: "idx_account_preferences_scope_key"},
				{model: &sandboxServiceDefaultAudienceRecord{}, name: "idx_sandbox_default_audience_identity"},
			} {
				if !migratedDB.Migrator().HasIndex(index.model, index.name) {
					t.Fatalf("fresh %s migration did not create %s", backend.name, index.name)
				}
			}
			if _, err := migrated.UpsertProfile(RunnerProfile{
				Name: "fresh-default", Labels: []string{"self-hosted", "fresh-default"},
				RequiredLabels: []string{"fresh-default"}, TemplateID: "fresh-template",
				MaxConcurrency: 1, Enabled: true,
			}); err != nil {
				t.Fatal(err)
			}
			match, err := migrated.MatchProfile("owner/repo", []string{"fresh-default"})
			if err != nil || match.Profile == nil || match.Profile.Name != "fresh-default" {
				t.Fatalf("fresh %s matcher = %#v, err=%v", backend.name, match, err)
			}
			if err := migrated.migrate(migratedDB); err != nil {
				t.Fatalf("second %s migration failed: %v", backend.name, err)
			}
			match, err = migrated.MatchProfile("owner/repo", []string{"fresh-default"})
			if err != nil || match.Profile == nil || match.Profile.Name != "fresh-default" {
				t.Fatalf("second %s migration matcher = %#v, err=%v", backend.name, match, err)
			}
		})
	}
}

func TestFreshSchemaIncludesScopedRunnerCatalog(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"scoped_runner_profiles"} {
		if !db.Migrator().HasTable(table) {
			t.Fatalf("expected fresh schema table %s", table)
		}
	}
	if db.Migrator().HasTable("runner_profile_scope_controls") {
		t.Fatal("fresh schema must not create removed runner_profile_scope_controls")
	}
	for _, index := range []struct {
		model any
		name  string
	}{
		{model: "scoped_runner_profiles", name: "idx_scoped_runner_profiles_scope_labels"},
		{model: "scoped_runner_profiles", name: "idx_scoped_runner_profiles_scope"},
	} {
		if !db.Migrator().HasIndex(index.model, index.name) {
			t.Fatalf("expected fresh schema index %s", index.name)
		}
	}
	for _, column := range []string{"profile_source", "profile_scope_type", "profile_scope_id"} {
		if !db.Migrator().HasColumn(&runnerRequestRecord{}, column) {
			t.Fatalf("expected runner_requests.%s", column)
		}
	}
}

func TestMigrateSQLiteRunnerRequestAddsProfileScopeWithoutLosingRows(t *testing.T) {
	databaseURL := filepath.Join(t.TempDir(), "runnerd.db")
	setup := NewWithOptions(Options{Backend: BackendSQLite, DatabaseDSN: databaseURL, MigrateOnStart: false}).(*DBStore)
	db, err := setup.open()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE runner_requests (
		id TEXT PRIMARY KEY,
		source TEXT NOT NULL,
		labels_json TEXT NOT NULL,
		profile_name TEXT,
		runner_name TEXT NOT NULL,
		status TEXT NOT NULL,
		queued_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		version INTEGER NOT NULL DEFAULT 0
	);`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE INDEX idx_runner_requests_profile_queued_id ON runner_requests(profile_name, queued_at DESC, id ASC)`).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	if err := db.Exec(`INSERT INTO runner_requests (id, source, labels_json, runner_name, status, queued_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"legacy-request", "github", `[]`, "runner", StatusCompleted, now, now).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	migrated := NewWithOptions(Options{Backend: BackendSQLite, DatabaseDSN: databaseURL, MigrateOnStart: true}).(*DBStore)
	if err := migrated.Ensure(); err != nil {
		t.Fatal(err)
	}
	db, err = migrated.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	var row struct {
		ID     string
		Status string
	}
	if err := db.Table("runner_requests").Select("id, status").Where("id = ?", "legacy-request").Scan(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.ID != "legacy-request" || row.Status != StatusCompleted {
		t.Fatalf("legacy row changed during migration: %#v", row)
	}
	for _, column := range []string{"profile_source", "profile_scope_type", "profile_scope_id"} {
		if !db.Migrator().HasColumn(&runnerRequestRecord{}, column) {
			t.Fatalf("expected migrated runner_requests.%s", column)
		}
	}
	if !db.Migrator().HasIndex(&runnerRequestRecord{}, "idx_runner_requests_profile_scope_status") {
		t.Fatal("expected profile scope status index")
	}
}

func TestMigrateSQLiteScopedRunnerCatalogIsIdempotent(t *testing.T) {
	databaseURL := filepath.Join(t.TempDir(), "runnerd.db")
	store := NewWithOptions(Options{Backend: BackendSQLite, DatabaseDSN: databaseURL, MigrateOnStart: true}).(*DBStore)
	if err := store.Ensure(); err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	var before []string
	if err := db.Raw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'scoped_runner_profiles' ORDER BY name`).Scan(&before).Error; err != nil {
		t.Fatal(err)
	}
	if len(before) != 1 {
		t.Fatalf("expected scoped catalog tables before repeat migration, got %v", before)
	}
	if err := store.migrate(db); err != nil {
		t.Fatal(err)
	}
	var after []string
	if err := db.Raw(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'scoped_runner_profiles' ORDER BY name`).Scan(&after).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("repeated migration changed scoped catalog tables: before=%v after=%v", before, after)
	}
}

func TestScopedRunnerCatalogFreshSchemaSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("set RUNNERD_CATALOG_BACKEND_TESTS=1 with dedicated Postgres and MySQL test databases")
	}
	for _, backend := range []struct {
		name string
		dsn  string
	}{
		{name: BackendPostgres, dsn: os.Getenv("RUNNERD_POSTGRES_TEST_DSN")},
		{name: BackendMySQL, dsn: os.Getenv("RUNNERD_MYSQL_TEST_DSN")},
	} {
		t.Run(backend.name, func(t *testing.T) {
			if strings.TrimSpace(backend.dsn) == "" {
				t.Skip("dedicated SQL backend test DSN not configured")
			}
			store := NewWithOptions(Options{Backend: backend.name, DatabaseDSN: backend.dsn, MigrateOnStart: true}).(*DBStore)
			db, err := store.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = db.Exec("DROP TABLE scoped_runner_profiles").Error
				closeTestDB(t, db)
			}()
			for _, table := range []string{"scoped_runner_profiles"} {
				if !db.Migrator().HasTable(table) {
					t.Fatalf("expected %s table", table)
				}
			}
			for _, index := range []string{
				"idx_scoped_runner_profiles_scope",
				"idx_scoped_runner_profiles_scope_labels",
			} {
				if !db.Migrator().HasIndex("scoped_runner_profiles", index) {
					t.Fatalf("expected %s index", index)
				}
			}
			if db.Migrator().HasTable("runner_profile_scope_controls") {
				t.Fatal("fresh schema must not create removed runner_profile_scope_controls")
			}
		})
	}
}

func TestScopedRunnerProfilesAreIsolatedByScope(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	a := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	b := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 2}
	profile := ScopedRunnerProfile{Name: "custom", WorkflowLabels: []string{"qiniu", "linux"}, TemplateID: "template-a", Enabled: true}
	profile.ScopeType, profile.ScopeID = a.Type, a.ID
	if _, err := store.UpsertScopedProfileIfUnchanged(profile, nil); err != nil {
		t.Fatal(err)
	}
	profile.ScopeType, profile.ScopeID, profile.TemplateID = b.Type, b.ID, "template-b"
	if _, err := store.UpsertScopedProfileIfUnchanged(profile, nil); err != nil {
		t.Fatal(err)
	}
	gotA, err := store.GetScopedProfile(a, "custom")
	if err != nil || gotA.TemplateID != "template-a" {
		t.Fatalf("scope A read = %#v, err=%v", gotA, err)
	}
	gotB, err := store.GetScopedProfile(b, "custom")
	if err != nil || gotB.TemplateID != "template-b" {
		t.Fatalf("scope B read = %#v, err=%v", gotB, err)
	}
}

func TestScopedRunnerProfileConditionalCreateRejectsExistingName(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	scope := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	profile := ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: "custom", WorkflowLabels: []string{"qiniu", "linux"}, TemplateID: "template-a", Enabled: true}
	if _, err := store.UpsertScopedProfileIfUnchanged(profile, nil); err != nil {
		t.Fatal(err)
	}
	profile.TemplateID = "template-b"
	if _, err := store.UpsertScopedProfileIfUnchanged(profile, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate scoped create = %v, want ErrConflict", err)
	}
	saved, err := store.GetScopedProfile(scope, profile.Name)
	if err != nil || saved.TemplateID != "template-a" {
		t.Fatalf("duplicate create overwrote scoped profile = %#v, err=%v", saved, err)
	}
}

func TestScopedRunnerProfileRejectsDuplicateNormalizedLabels(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	scope := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	base := ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: "one", WorkflowLabels: []string{"linux", "qiniu"}, TemplateID: "template", Enabled: true}
	if _, err := store.UpsertScopedProfileIfUnchanged(base, nil); err != nil {
		t.Fatal(err)
	}
	duplicate := base
	duplicate.Name = "two"
	duplicate.WorkflowLabels = []string{" qiniu ", "linux", "qiniu"}
	if _, err := store.UpsertScopedProfileIfUnchanged(duplicate, nil); !errors.Is(err, ErrRunnerProfileLabelsConflict) {
		t.Fatalf("duplicate normalized labels = %v, want ErrRunnerProfileLabelsConflict", err)
	}
}

func TestScopedRunnerProfileUpdateRejectsDuplicateNormalizedLabels(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	scope := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	first := ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: "one", WorkflowLabels: []string{"linux", "qiniu"}, TemplateID: "template-one", Enabled: true}
	if _, err := store.UpsertScopedProfileIfUnchanged(first, nil); err != nil {
		t.Fatal(err)
	}
	second, err := store.UpsertScopedProfileIfUnchanged(ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: "two", WorkflowLabels: []string{"qiniu", "gpu"}, TemplateID: "template-two", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second.WorkflowLabels = []string{" qiniu ", "linux", "qiniu"}
	if _, err := store.UpsertScopedProfileIfUnchanged(second, &second.UpdatedAt); !errors.Is(err, ErrRunnerProfileLabelsConflict) {
		t.Fatalf("duplicate normalized labels on update = %v, want ErrRunnerProfileLabelsConflict", err)
	}
}

func TestListEffectiveProfilesReportsOnlyExactGlobalLabelOverrides(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	if _, err := store.UpsertProfile(RunnerProfile{Name: "global", Labels: []string{"qiniu", "linux"}, RequiredLabels: []string{"qiniu"}, TemplateID: "global-template", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	scope := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	for _, profile := range []ScopedRunnerProfile{
		{ScopeType: scope.Type, ScopeID: scope.ID, Name: "override", WorkflowLabels: []string{"linux", "qiniu"}, TemplateID: "override-template", Enabled: true},
		{ScopeType: scope.Type, ScopeID: scope.ID, Name: "distinct", WorkflowLabels: []string{"qiniu", "gpu"}, TemplateID: "distinct-template", Enabled: true},
	} {
		if _, err := store.UpsertScopedProfileIfUnchanged(profile, nil); err != nil {
			t.Fatal(err)
		}
	}
	items, err := store.ListEffectiveProfiles(scope)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]bool{}
	for _, item := range items {
		if item.Source == "scoped_custom" {
			got[item.Profile.Name] = item.OverridesGlobal
		}
	}
	if !got["override"] || got["distinct"] {
		t.Fatalf("override flags = %#v, want override=true and distinct=false", got)
	}
}

func TestScopedRunnerProfileConditionalWritesRejectStaleRevision(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	scope := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	profile := ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: "custom", WorkflowLabels: []string{"qiniu"}, TemplateID: "template", Enabled: true}
	saved, err := store.UpsertScopedProfileIfUnchanged(profile, nil)
	if err != nil {
		t.Fatal(err)
	}
	saved.TemplateID = "template-new"
	if _, err := store.UpsertScopedProfileIfUnchanged(saved, &profile.UpdatedAt); err == nil {
		t.Fatal("expected stale scoped profile revision conflict")
	}
}

func TestMatchProfileForScopeIgnoresLegacyManagedScopeControls(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	if _, err := store.UpsertProfile(RunnerProfile{Name: "managed", Labels: []string{"qiniu"}, RequiredLabels: []string{"qiniu"}, TemplateID: "template", ManagedBy: "runnerd", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	scope := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS runner_profile_scope_controls (
		scope_type TEXT NOT NULL,
		scope_id INTEGER NOT NULL,
		profile_name TEXT NOT NULL,
		enabled BOOLEAN NOT NULL,
		max_concurrency INTEGER NOT NULL DEFAULT 0,
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL,
		PRIMARY KEY (scope_type, scope_id, profile_name)
	)`).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Exec(`INSERT INTO runner_profile_scope_controls (scope_type, scope_id, profile_name, enabled, max_concurrency, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, scope.Type, scope.ID, "managed", false, 1, now, now).Error; err != nil {
		t.Fatal(err)
	}
	match, err := store.MatchProfileForScope(scope, "owner/repo", []string{"qiniu"})
	if err != nil || match.Profile == nil || match.Profile.Name != "managed" {
		t.Fatalf("legacy control changed global match: %#v, err=%v", match, err)
	}
}

func TestMatchProfileForScopePrefersExactScopedProfileAndShadowsWhenDisabled(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	if _, err := store.UpsertProfile(RunnerProfile{Name: "global", Labels: []string{"qiniu", "linux"}, RequiredLabels: []string{"qiniu"}, TemplateID: "global-template", Enabled: true, Priority: 100}); err != nil {
		t.Fatal(err)
	}
	scope := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	custom := ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: "custom", WorkflowLabels: []string{"qiniu", "linux"}, TemplateID: "custom-template", Enabled: true}
	if _, err := store.UpsertScopedProfileIfUnchanged(custom, nil); err != nil {
		t.Fatal(err)
	}
	match, err := store.MatchProfileForScope(scope, "owner/repo", []string{"linux", "qiniu"})
	if err != nil || match.Profile == nil || match.Profile.Name != "custom" || match.Source != "scoped_custom" {
		t.Fatalf("custom match = %#v, err=%v", match, err)
	}
	custom.Enabled = false
	saved, err := store.GetScopedProfile(scope, "custom")
	if err != nil {
		t.Fatal(err)
	}
	custom.UpdatedAt = saved.UpdatedAt
	if _, err := store.UpsertScopedProfileIfUnchanged(custom, &saved.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	match, err = store.MatchProfileForScope(scope, "owner/repo", []string{"qiniu", "linux"})
	if err != nil || match.Profile != nil || match.Reason != "profile_scope_disabled" || match.Source != "scoped_custom" {
		t.Fatalf("disabled custom match = %#v, err=%v", match, err)
	}
}

func TestMatchProfileForScopeMatchesScopedLabelsCaseInsensitively(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	if _, err := store.UpsertProfile(RunnerProfile{Name: "global", Labels: []string{"qiniu", "gpu"}, RequiredLabels: []string{"qiniu"}, TemplateID: "global-template", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	scope := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	if _, err := store.UpsertScopedProfileIfUnchanged(ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: "custom", WorkflowLabels: []string{"QINIU", "GPU"}, TemplateID: "custom-template", Enabled: false}, nil); err != nil {
		t.Fatal(err)
	}

	match, err := store.MatchProfileForScope(scope, "owner/repo", []string{"qiniu", "gpu"})
	if err != nil || match.Profile != nil || match.Source != "scoped_custom" || match.Reason != "profile_scope_disabled" {
		t.Fatalf("case-insensitive disabled custom match = %#v, err=%v", match, err)
	}
}

func TestMatchProfileForScopeFallsBackToGlobalAndCountsStayScoped(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	if _, err := store.UpsertProfile(RunnerProfile{Name: "global", Labels: []string{"qiniu"}, RequiredLabels: []string{"qiniu"}, TemplateID: "template", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	scopeA := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	scopeB := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 2}
	match, err := store.MatchProfileForScope(scopeA, "owner/repo", []string{"qiniu"})
	if err != nil || match.Profile == nil || match.Source != "global" || match.ScopeID != scopeA.ID {
		t.Fatalf("global fallback = %#v, err=%v", match, err)
	}
	for _, req := range []RunnerRequest{
		{ID: "a", ProfileName: "global", ProfileSource: "global", ProfileScopeType: scopeA.Type, ProfileScopeID: scopeA.ID, Labels: []string{"qiniu"}, RunnerName: "a"},
		{ID: "b", ProfileName: "global", ProfileSource: "global", ProfileScopeType: scopeB.Type, ProfileScopeID: scopeB.ID, Labels: []string{"qiniu"}, RunnerName: "b"},
	} {
		if _, _, err := store.CreateRequest(req, nil); err != nil {
			t.Fatal(err)
		}
	}
	count, err := store.ActiveCountForProfileScope("global", scopeA, "global")
	if err != nil || count != 1 {
		t.Fatalf("scope A count = %d, err=%v", count, err)
	}
}

func TestScopedRunnerProfileNameConflictsWithEnabledGlobalProfile(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	if _, err := store.UpsertProfile(RunnerProfile{Name: "ubuntu", Labels: []string{"qiniu"}, RequiredLabels: []string{"qiniu"}, TemplateID: "global", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	scope := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	if _, err := store.UpsertScopedProfileIfUnchanged(ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: "ubuntu", WorkflowLabels: []string{"custom"}, TemplateID: "scoped", Enabled: true}, nil); err == nil {
		t.Fatal("expected scoped profile name conflict with enabled global profile")
	}
}

func TestScopedRunnerProfileNameCanMatchDisabledGlobalProfile(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	if _, err := store.UpsertProfile(RunnerProfile{Name: "ubuntu", Labels: []string{"qiniu"}, RequiredLabels: []string{"qiniu"}, TemplateID: "global", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	scope := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
	if _, err := store.UpsertScopedProfileIfUnchanged(ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: "ubuntu", WorkflowLabels: []string{"custom"}, TemplateID: "scoped", Enabled: true}, nil); err != nil {
		t.Fatalf("disabled global profile should not block scoped name: %v", err)
	}
}

func sqlBackendTestTables() []string {
	return []string{
		"runner_events",
		"runner_requests",
		"scoped_runner_profiles",
		"runner_group_specs",
		"repository_policies",
		"runner_groups",
		"runner_profiles",
		"audit_events",
		"oauth_identities",
		"github_installations",
		"github_installation_owners",
		"account_secrets",
		"account_preferences",
		"sandbox_service_default_audiences",
		"sandbox_service_defaults",
		"accounts",
	}
}

func resetSQLBackendTestTables(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, table := range sqlBackendTestTables() {
		if err := db.Migrator().DropTable(table); err != nil {
			t.Fatal(err)
		}
	}
}

func requireCatalogMatcherTestDatabase(t *testing.T, db *gorm.DB, backend string) {
	t.Helper()
	var databaseName string
	var query string
	switch backend {
	case BackendPostgres:
		query = `SELECT CURRENT_DATABASE()`
	case BackendMySQL:
		query = `SELECT DATABASE()`
	default:
		t.Fatalf("unsupported catalog matcher test backend %q", backend)
	}
	if err := db.Raw(query).Scan(&databaseName).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(strings.ToLower(databaseName), "_test") {
		t.Fatalf("refusing destructive catalog matcher setup in database %q; dedicated database name must end in _test", databaseName)
	}
}

func TestMatchProfileUsesEnabledSpecWithoutPolicy(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.UpsertProfile(RunnerProfile{
		Name:           "private-before-release-b",
		Labels:         []string{"self-hosted", "e2b"},
		TemplateID:     "base",
		MaxConcurrency: 10,
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}
	match, err := store.MatchProfile("owner/repo", []string{"self-hosted", "e2b"})
	if err != nil {
		t.Fatal(err)
	}
	if match.Profile == nil || match.Profile.Name != "private-before-release-b" || match.Reason != "" {
		t.Fatalf("enabled-Spec match = %#v, want private-before-release-b", match)
	}
}

func TestEnabledProfileMatchesWithoutRetiredCatalogModels(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.UpsertProfile(RunnerProfile{
		Name:           "default",
		Labels:         []string{"self-hosted", "e2b"},
		TemplateID:     "base",
		MaxConcurrency: 10,
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}
	match, err := store.MatchProfile("owner/any-repo", []string{"self-hosted", "e2b"})
	if err != nil {
		t.Fatal(err)
	}
	if match.Profile == nil || match.Profile.Name != "default" {
		t.Fatalf("expected default profile match, got %#v", match.Profile)
	}
}

func TestMatchProfileAllowsRunnerSpecWithAdditionalLabels(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.UpsertProfile(RunnerProfile{
		Name:           "default",
		Labels:         []string{"self-hosted", "e2b", "las-sandbox", "github-runner-ubuntu-24-04"},
		TemplateID:     "base",
		MaxConcurrency: 10,
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}
	match, err := store.MatchProfile("owner/any-repo", []string{"github-runner-ubuntu-24-04"})
	if err != nil {
		t.Fatal(err)
	}
	if match.Profile == nil || match.Profile.Name != "default" {
		t.Fatalf("expected default profile match, got %#v", match.Profile)
	}
}

func TestProductionRunnerCatalogFixtureFreezesLegacyWorkflowMatches(t *testing.T) {
	// Characterization test: this intentionally passes against the policy-aware
	// baseline. It catches a future matcher that changes production workflow
	// label compatibility while Groups and Policies are removed.
	store := New(t.TempDir())
	loadProductionRunnerCatalog(t, store)

	tests := []struct {
		name       string
		repository string
		labels     []string
		wantSpec   string
	}{
		{"managed slim canonical", "outside/example", []string{"qiniu", "ubuntu-slim"}, "qiniu-ubuntu-slim"},
		{"managed 2204 canonical", "outside/example", []string{"qiniu", "ubuntu-22.04"}, "qiniu-ubuntu-22.04"},
		{"managed 2404 canonical", "outside/example", []string{"qiniu", "ubuntu-24.04"}, "qiniu-ubuntu-24.04"},
		{"managed 2604 canonical", "outside/example", []string{"qiniu", "ubuntu-26.04"}, "qiniu-ubuntu-26.04"},
		{"managed latest canonical", "outside/example", []string{"qiniu", "ubuntu-latest"}, "qiniu-ubuntu-latest"},
		{"managed 2404 advertised", "outside/example", []string{"self-hosted", "linux", "x64", "qiniu", "ubuntu-24.04"}, "qiniu-ubuntu-24.04"},
		{"legacy generic custom", "qbox/example", []string{"self-hosted", "e2b"}, "github-runner-ubuntu-24-04"},
		{"legacy public custom", "goplus/example", []string{"self-hosted", "e2b", "github-runner-ubuntu-24-04"}, "github-runner-ubuntu-24-04"},
		{"legacy public custom outside policy", "outside/example", []string{"self-hosted", "e2b", "github-runner-ubuntu-24-04"}, "github-runner-ubuntu-24-04"},
		{"qbox dora 1604", "qbox/example", []string{"self-hosted", "e2b", "qbox-dora-ubuntu-16-04"}, "qbox-dora-ubuntu-16-04"},
		{"qbox dora 2404", "qbox/example", []string{"self-hosted", "e2b", "qbox-dora-ubuntu-24-04"}, "qbox-dora-ubuntu-24-04"},
		{"qbox kodo 1604", "qbox/example", []string{"self-hosted", "e2b", "qbox-kodo-ubuntu-16-04"}, "qbox-kodo-ubuntu-16-04"},
		{"qbox kodo web", "qbox/example", []string{"self-hosted", "e2b", "qbox-kodo-web-ubuntu-20-04"}, "qbox-kodo-web-ubuntu-20-04"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, err := store.MatchProfile(tt.repository, tt.labels)
			if err != nil {
				t.Fatal(err)
			}
			if match.Profile == nil || match.Profile.Name != tt.wantSpec {
				t.Fatalf("MatchProfile(%q, %#v) = %#v, want spec %q", tt.repository, tt.labels, match, tt.wantSpec)
			}
		})
	}
}

func TestMatchProfileProductionOrderingAndNegativeContract(t *testing.T) {
	// This test catches a policy-free selector that accidentally admits a
	// disabled spec, changes the no-match reason, or forks the existing sort.
	store := New(t.TempDir())
	loadProductionRunnerCatalog(t, store)

	disabled, err := store.GetProfile("qiniu-ubuntu-24.04")
	if err != nil {
		t.Fatal(err)
	}
	disabled.Enabled = false
	if _, err := store.UpsertProfile(disabled); err != nil {
		t.Fatal(err)
	}
	match, err := store.MatchProfile("outside/example", []string{"qiniu", "ubuntu-24.04"})
	if err != nil {
		t.Fatal(err)
	}
	if match.Profile != nil || match.Reason != "profile_labels_not_matched" {
		t.Fatalf("disabled spec match = %#v, want profile_labels_not_matched", match)
	}
	match, err = store.MatchProfile("outside/example", []string{"self-hosted", "e2b", "not-in-production-catalog"})
	if err != nil {
		t.Fatal(err)
	}
	if match.Profile != nil || match.Reason != "profile_labels_not_matched" {
		t.Fatalf("unmatched labels result = %#v, want profile_labels_not_matched", match)
	}

	ordering := New(t.TempDir())
	for _, profile := range []RunnerProfile{
		{Name: "z-name", Labels: []string{"self-hosted", "e2b"}, TemplateID: "z", MaxConcurrency: 1, Priority: 30, Enabled: true},
		{Name: "a-name", Labels: []string{"self-hosted", "e2b"}, TemplateID: "a", MaxConcurrency: 1, Priority: 30, Enabled: true},
		{Name: "longer-label-set", Labels: []string{"self-hosted", "e2b", "optional"}, TemplateID: "long", MaxConcurrency: 1, Priority: 30, Enabled: true},
		{Name: "higher-priority", Labels: []string{"self-hosted", "e2b"}, TemplateID: "high", MaxConcurrency: 1, Priority: 31, Enabled: true},
	} {
		if _, err := ordering.UpsertProfile(profile); err != nil {
			t.Fatal(err)
		}
	}
	match, err = ordering.MatchProfile("outside/example", []string{"self-hosted", "e2b"})
	if err != nil {
		t.Fatal(err)
	}
	if match.Profile == nil || match.Profile.Name != "higher-priority" {
		t.Fatalf("priority ordering selected %#v, want higher-priority", match.Profile)
	}
	higher, err := ordering.GetProfile("higher-priority")
	if err != nil {
		t.Fatal(err)
	}
	higher.Enabled = false
	if _, err := ordering.UpsertProfile(higher); err != nil {
		t.Fatal(err)
	}
	match, err = ordering.MatchProfile("outside/example", []string{"self-hosted", "e2b"})
	if err != nil {
		t.Fatal(err)
	}
	if match.Profile == nil || match.Profile.Name != "longer-label-set" {
		t.Fatalf("label-count ordering selected %#v, want longer-label-set", match.Profile)
	}
	longer, err := ordering.GetProfile("longer-label-set")
	if err != nil {
		t.Fatal(err)
	}
	longer.Enabled = false
	if _, err := ordering.UpsertProfile(longer); err != nil {
		t.Fatal(err)
	}
	match, err = ordering.MatchProfile("outside/example", []string{"self-hosted", "e2b"})
	if err != nil {
		t.Fatal(err)
	}
	if match.Profile == nil || match.Profile.Name != "a-name" {
		t.Fatalf("name ordering selected %#v, want a-name", match.Profile)
	}
}

func TestMatchProfileProductionRequestSurvivesMigrationAndRetry(t *testing.T) {
	// This test catches a catalog migration that rewrites an already-admitted
	// request or a retry path that rematches it instead of retaining its spec.
	databaseURL := filepath.Join(t.TempDir(), "runnerd.db")
	store := NewWithOptions(Options{Backend: BackendSQLite, DatabaseDSN: databaseURL, MigrateOnStart: true}).(*DBStore)
	loadProductionRunnerCatalog(t, store)

	match, err := store.MatchProfile("qbox/example", []string{"self-hosted", "e2b"})
	if err != nil {
		t.Fatal(err)
	}
	if match.Profile == nil || match.Profile.Name != "github-runner-ubuntu-24-04" {
		t.Fatalf("legacy match = %#v, want github-runner-ubuntu-24-04", match)
	}
	createdAt := time.Date(2026, time.August, 13, 9, 10, 11, 0, time.UTC)
	request := RunnerRequest{
		ID:                     "legacy-admitted-request",
		Source:                 "test",
		RepositoryFullName:     "qbox/example",
		RequestedLabels:        []string{"self-hosted", "e2b"},
		Labels:                 append([]string(nil), match.Profile.Labels...),
		ProfileName:            match.Profile.Name,
		RunnerGroup:            match.Profile.RunnerGroup,
		RunnerName:             "e2b-legacy-admitted-request",
		SandboxAPIURL:          "https://sandbox.example.test",
		SandboxAPIKeyEncrypted: "fixture-encrypted-snapshot",
		SandboxConfigSource:    "account",
		CreatedAt:              createdAt,
	}
	if created, _, err := store.CreateRequest(request, nil); err != nil || !created {
		t.Fatalf("CreateRequest created=%v err=%v", created, err)
	}
	beforeRequest, err := store.ReadRequest(request.ID)
	if err != nil {
		t.Fatal(err)
	}
	beforeState, err := store.ReadState(request.ID)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	restarted := NewWithOptions(Options{Backend: BackendSQLite, DatabaseDSN: databaseURL, MigrateOnStart: true}).(*DBStore)
	afterRequest, err := restarted.ReadRequest(request.ID)
	if err != nil {
		t.Fatal(err)
	}
	afterState, err := restarted.ReadState(request.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterRequest.ProfileName != beforeRequest.ProfileName ||
		afterRequest.RunnerGroup != beforeRequest.RunnerGroup ||
		!reflect.DeepEqual(afterRequest.RequestedLabels, beforeRequest.RequestedLabels) ||
		afterRequest.SandboxAPIURL != beforeRequest.SandboxAPIURL ||
		afterRequest.SandboxAPIKeyEncrypted != beforeRequest.SandboxAPIKeyEncrypted ||
		afterRequest.SandboxConfigSource != beforeRequest.SandboxConfigSource ||
		afterState.SandboxAPIURL != beforeState.SandboxAPIURL ||
		afterState.SandboxAPIKeyEncrypted != beforeState.SandboxAPIKeyEncrypted ||
		afterState.SandboxConfigSource != beforeState.SandboxConfigSource ||
		!afterRequest.CreatedAt.Equal(beforeRequest.CreatedAt) ||
		!afterState.CreatedAt.Equal(beforeState.CreatedAt) ||
		!afterState.UpdatedAt.Equal(beforeState.UpdatedAt) {
		t.Fatalf("migration rewrote admitted request: before request=%#v state=%#v after request=%#v state=%#v", beforeRequest, beforeState, afterRequest, afterState)
	}

	afterState.Status = StatusFailed
	afterState.LastErrorRetryable = true
	if err := restarted.WriteState(afterState); err != nil {
		t.Fatal(err)
	}
	retried, err := restarted.RetryRequest(request.ID, time.Date(2026, time.August, 13, 9, 11, 12, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if retried.ProfileName != "github-runner-ubuntu-24-04" ||
		retried.RunnerGroup != "Default" ||
		!reflect.DeepEqual(retried.RequestedLabels, []string{"self-hosted", "e2b"}) {
		t.Fatalf("retry changed persisted admission: %#v", retried)
	}
}

func TestClaimNextRunnableHonorsLeaseAndRetryWindow(t *testing.T) {
	store := New(t.TempDir())
	now := time.Now().UTC()
	if _, st, err := store.CreateRequest(RunnerRequest{
		ID:              "retryable",
		Source:          "test",
		RequestedLabels: []string{"self-hosted"},
		Labels:          []string{"self-hosted"},
		RunnerName:      "e2b-retryable",
	}, nil); err != nil {
		t.Fatal(err)
	} else {
		st.NextRetryAt = now.Add(-time.Second)
		if err := store.WriteState(st); err != nil {
			t.Fatal(err)
		}
	}

	req, st, claimed, err := store.ClaimNextRunnable("worker-1", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed || req.ID != "retryable" || st.LeaseOwner != "worker-1" || st.LastAttemptAt.IsZero() {
		t.Fatalf("unexpected claim result: req=%#v state=%#v claimed=%v", req, st, claimed)
	}

	_, _, claimed, err = store.ClaimNextRunnable("worker-2", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if claimed {
		t.Fatal("expected active lease to block second claim")
	}
}

func TestRetryRequestClearsFailureFields(t *testing.T) {
	store := New(t.TempDir())
	_, st, err := store.CreateRequest(RunnerRequest{
		ID:              "retry-me",
		Source:          "test",
		RequestedLabels: []string{"self-hosted"},
		Labels:          []string{"self-hosted"},
		RunnerName:      "e2b-retry-me",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = StatusFailed
	st.FailureStage = "sandbox_start"
	st.FailureReason = "backend_server_error"
	st.LastErrorCode = "backend_server_error"
	st.LastErrorMessage = "temporary failure"
	st.LastErrorRetryable = true
	st.NextRetryAt = time.Now().Add(time.Minute).UTC()
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	retried, err := store.RetryRequest("retry-me", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != StatusQueued || retried.FailureStage != "" || retried.LastErrorCode != "" || !retried.NextRetryAt.IsZero() {
		t.Fatalf("unexpected retried state: %#v", retried)
	}
}

func TestRetryRequestRejectsActiveState(t *testing.T) {
	store := New(t.TempDir())
	_, st, err := store.CreateRequest(RunnerRequest{
		ID:              "running",
		Source:          "test",
		RequestedLabels: []string{"self-hosted"},
		Labels:          []string{"self-hosted"},
		RunnerName:      "e2b-running",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = StatusRunning
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	if _, err := store.RetryRequest("running", time.Now().UTC()); !errors.Is(err, ErrRetryNotAllowed) {
		t.Fatalf("expected retry guard, got %v", err)
	}
}

func TestAuditEventsCanBeListed(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.AppendAuditEvent(AuditEvent{
		Actor:        "admin_api",
		Action:       "runner.retry",
		ResourceType: "runner_request",
		ResourceID:   "retry-me",
		PayloadJSON:  `{"status":"queued"}`,
		CreatedAt:    time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	events, err := store.ListAuditEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "runner.retry" {
		t.Fatalf("unexpected audit events: %#v", events)
	}
}

func TestApplyMutationWithAuditCommitsMutationAndAuditTogether(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	var saved RunnerProfile
	event, err := store.ApplyMutationWithAudit(AuditEvent{
		Actor: "admin_api", Action: "profile.create", ResourceType: "runner_profile", ResourceID: "atomic-profile",
	}, func(tx Store) error {
		var mutationErr error
		saved, mutationErr = tx.UpsertProfile(RunnerProfile{
			Name: "atomic-profile", Labels: []string{"self-hosted", "atomic"},
			RequiredLabels: []string{"atomic"}, TemplateID: "atomic-template", Enabled: true,
		})
		return mutationErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if saved.Name != "atomic-profile" || event.ID == 0 {
		t.Fatalf("atomic mutation result: profile=%#v event=%#v", saved, event)
	}
	events, err := store.ListAuditEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0] != event {
		t.Fatalf("audit events = %#v, want %#v", events, event)
	}
}

func TestApplyMutationWithAuditDoesNotAuditRejectedMutation(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	_, err := store.ApplyMutationWithAudit(AuditEvent{
		Actor: "admin_api", Action: "profile.create", ResourceType: "runner_profile", ResourceID: "invalid-profile",
	}, func(tx Store) error {
		_, mutationErr := tx.UpsertProfile(RunnerProfile{
			Name: "invalid-profile", Labels: []string{"self-hosted"},
			RequiredLabels: []string{"missing"}, TemplateID: "invalid-template", Enabled: true,
		})
		return mutationErr
	})
	if err == nil || !strings.Contains(err.Error(), "required labels must be a subset") {
		t.Fatalf("rejected mutation error = %v", err)
	}
	if _, err := store.GetProfile("invalid-profile"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected profile persisted: %v", err)
	}
	events, err := store.ListAuditEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("rejected mutation audit events = %#v", events)
	}
}

func TestApplyMutationWithAuditRollsBackMutationWhenAuditFails(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER fail_profile_mutation_audit
		BEFORE INSERT ON audit_events
		WHEN NEW.action = 'profile.create'
		BEGIN
			SELECT RAISE(ABORT, 'forced mutation audit failure');
		END`).Error; err != nil {
		t.Fatal(err)
	}

	_, err = store.ApplyMutationWithAudit(AuditEvent{
		Actor: "admin_api", Action: "profile.create", ResourceType: "runner_profile", ResourceID: "rolled-back-profile",
	}, func(tx Store) error {
		_, mutationErr := tx.UpsertProfile(RunnerProfile{
			Name: "rolled-back-profile", Labels: []string{"self-hosted", "rollback"},
			RequiredLabels: []string{"rollback"}, TemplateID: "rollback-template", Enabled: true,
		})
		return mutationErr
	})
	if !errors.Is(err, ErrAuditEventPersistence) || !strings.Contains(err.Error(), "forced mutation audit failure") {
		t.Fatalf("audit persistence error = %v", err)
	}
	if _, err := store.GetProfile("rolled-back-profile"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("profile committed despite audit failure: %v", err)
	}
}

func TestRunnerStateDerivesGitHubJobLinkFromWorkflowJobPayload(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	payload := []byte(`{
		"workflow_job": {
			"id": 77684492230,
			"run_id": 26392225417,
			"workflow_name": "CI",
			"run_attempt": 2,
			"head_branch": "feature/group-jobs",
			"head_sha": "abc123def456",
			"html_url": "https://github.com/qbox/las/actions/runs/26392225417/job/77684492230",
			"pull_requests": [{"number": 3335}]
		}
	}`)
	_, st, err := store.CreateRequest(RunnerRequest{
		ID:                   "77684492230",
		Source:               "github",
		JobID:                77684492230,
		GitHubInstallationID: 42,
		RepositoryFullName:   "qbox/las",
		RequestedLabels:      []string{"github-runner-ubuntu-24-04"},
		Labels:               []string{"github-runner-ubuntu-24-04"},
		RunnerName:           "e2b-77684492230",
	}, payload)
	if err != nil {
		t.Fatal(err)
	}
	if st.WorkflowRunID != 26392225417 || st.WorkflowJobID != 77684492230 || st.PullRequestNumber != 3335 {
		t.Fatalf("unexpected github metadata: %#v", st)
	}
	if st.WorkflowName != "CI" || st.WorkflowRunAttempt != 2 || st.HeadBranch != "feature/group-jobs" || st.HeadSHA != "abc123def456" {
		t.Fatalf("unexpected github context: %#v", st)
	}
	wantURL := "https://github.com/qbox/las/actions/runs/26392225417/job/77684492230?pr=3335"
	if st.GitHubJobURL != wantURL {
		t.Fatalf("unexpected github job url: %q", st.GitHubJobURL)
	}

	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	var record runnerRequestRecord
	if err := db.First(&record, "id = ?", "77684492230").Error; err != nil {
		t.Fatal(err)
	}
	if record.WorkflowRunID != 26392225417 || record.WorkflowName != "CI" || record.WorkflowRunAttempt != 2 {
		t.Fatalf("expected github context to be persisted on runner_requests: %#v", record)
	}
	if record.HeadBranch != "feature/group-jobs" || record.HeadSHA != "abc123def456" || record.PullRequestNumber != 3335 {
		t.Fatalf("expected grouping fields to be persisted on runner_requests: %#v", record)
	}
	if record.GitHubJobURL != wantURL {
		t.Fatalf("expected github job url to be persisted, got %q", record.GitHubJobURL)
	}
	if !record.GitHubContextBackfilled {
		t.Fatalf("expected github context backfill marker to be persisted")
	}
	if record.GitHubPayloadJSON != "" {
		t.Fatal("accepted request must not persist the webhook payload")
	}
	closeTestDB(t, db)
	restarted := NewWithOptions(store.opts).(*DBStore)
	restartedState, err := restarted.ReadState(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restartedState, st) {
		t.Fatalf("state changed after restart without a payload: got %#v want %#v", restartedState, st)
	}
	req, err := restarted.ReadRequest(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if req.GitHubInstallationID != 42 || req.JobID != 77684492230 || req.RepositoryFullName != "qbox/las" ||
		!reflect.DeepEqual(req.Labels, []string{"github-runner-ubuntu-24-04"}) {
		t.Fatalf("request lost runner recovery fields: %#v", req)
	}
}

func TestGitHubLinksFromPayloadBytesFallbacks(t *testing.T) {
	tests := []struct {
		name       string
		payload    string
		wantRunID  int64
		wantPR     int64
		wantJobURL string
	}{
		{
			name:       "workflow job nested run id",
			payload:    `{"workflow_job":{"workflow_run":{"id":26392225417}}}`,
			wantRunID:  26392225417,
			wantJobURL: "https://github.com/qbox/las/actions/runs/26392225417/job/77684492230",
		},
		{
			name:       "top level pull request number",
			payload:    `{"workflow_job":{"run_id":26392225417},"pull_request":{"number":3335}}`,
			wantRunID:  26392225417,
			wantPR:     3335,
			wantJobURL: "https://github.com/qbox/las/actions/runs/26392225417/job/77684492230?pr=3335",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			links := githubLinksFromPayloadBytes([]byte(tt.payload), "qbox/las", 77684492230)
			if links.workflowRunID != tt.wantRunID || links.pullRequestNumber != tt.wantPR || links.jobURL != tt.wantJobURL {
				t.Fatalf("github links = %#v, want run id %d, PR %d, URL %q", links, tt.wantRunID, tt.wantPR, tt.wantJobURL)
			}
		})
	}
}

func TestAppendPullRequestQueryMatchesActualQueryParameter(t *testing.T) {
	baseURL := "https://github.com/qbox/las/actions/runs/1/job/2"
	for _, prNumber := range []int64{0, -5} {
		if got := appendPullRequestQuery(baseURL, prNumber); got != baseURL {
			t.Fatalf("appendPullRequestQuery(%q, %d) = %q, want unchanged URL", baseURL, prNumber, got)
		}
	}

	tests := []struct {
		name   string
		rawURL string
		want   string
	}{
		{
			name:   "existing pull request query",
			rawURL: "https://github.com/qbox/las/actions/runs/1/job/2?pr=3335",
			want:   "https://github.com/qbox/las/actions/runs/1/job/2?pr=3335",
		},
		{
			name:   "existing valueless pull request query",
			rawURL: "https://github.com/qbox/las/actions/runs/1/job/2?pr",
			want:   "https://github.com/qbox/las/actions/runs/1/job/2?pr",
		},
		{
			name:   "path containing query-like text",
			rawURL: "https://github.com/qbox/pr=/actions/runs/1/job/2",
			want:   "https://github.com/qbox/pr=/actions/runs/1/job/2?pr=3335",
		},
		{
			name:   "unrelated query value containing query-like text",
			rawURL: "https://github.com/qbox/las/actions/runs/1/job/2?expr=pr=value",
			want:   "https://github.com/qbox/las/actions/runs/1/job/2?expr=pr=value&pr=3335",
		},
		{
			name:   "unrelated query",
			rawURL: "https://github.com/qbox/las/actions/runs/1/job/2?attempt=2",
			want:   "https://github.com/qbox/las/actions/runs/1/job/2?attempt=2&pr=3335",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := appendPullRequestQuery(tt.rawURL, 3335); got != tt.want {
				t.Fatalf("appendPullRequestQuery(%q) = %q, want %q", tt.rawURL, got, tt.want)
			}
		})
	}
}

func TestEffectiveRunnerRequestJobID(t *testing.T) {
	workflowJobID := int64(22)
	tests := []struct {
		name   string
		record runnerRequestRecord
		want   int64
	}{
		{name: "assigned job takes precedence", record: runnerRequestRecord{AssignedJobID: 11, WorkflowJobID: &workflowJobID}, want: 11},
		{name: "workflow job fallback", record: runnerRequestRecord{WorkflowJobID: &workflowJobID}, want: 22},
		{name: "missing job id", record: runnerRequestRecord{}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := effectiveRunnerRequestJobID(tt.record); got != tt.want {
				t.Fatalf("effective runner request job ID = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRunnerStateBuildsGitHubJobLinkFromWorkflowRunPayload(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	payload := []byte(`{
		"workflow_run": {
			"id": 26392225417,
			"name": "CI",
			"run_attempt": 2,
			"head_branch": "feature/group-jobs",
			"head_sha": "abc123def456",
			"pull_requests": [{"number": 3335}]
		}
	}`)
	_, st, err := store.CreateRequest(RunnerRequest{
		ID:                 "77684492230",
		Source:             "github_reconcile",
		JobID:              77684492230,
		RepositoryFullName: "qbox/las",
		RequestedLabels:    []string{"github-runner-ubuntu-24-04"},
		Labels:             []string{"github-runner-ubuntu-24-04"},
		RunnerName:         "e2b-77684492230",
	}, payload)
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "https://github.com/qbox/las/actions/runs/26392225417/job/77684492230?pr=3335"
	if st.WorkflowRunID != 26392225417 || st.GitHubJobURL != wantURL {
		t.Fatalf("unexpected github metadata: %#v", st)
	}
	if st.WorkflowName != "CI" || st.WorkflowRunAttempt != 2 || st.HeadBranch != "feature/group-jobs" || st.HeadSHA != "abc123def456" || st.PullRequestNumber != 3335 {
		t.Fatalf("unexpected workflow_run context: %#v", st)
	}
	record, err := store.readRecord(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if record.GitHubPayloadJSON != "" || !record.GitHubContextBackfilled {
		t.Fatal("workflow_run request must persist parsed context without the webhook payload")
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)
	restarted := NewWithOptions(store.opts)
	restartedState, err := restarted.ReadState(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restartedState, st) {
		t.Fatalf("workflow_run context changed after restart: got %#v want %#v", restartedState, st)
	}
}

func TestRunnerStateUsesBackfilledGitHubContextWithoutParsingPayload(t *testing.T) {
	workflowJobID := int64(77684492230)
	st := recordToState(runnerRequestRecord{
		ID:                      "77684492230",
		Source:                  "github",
		WorkflowJobID:           &workflowJobID,
		GitHubInstallationID:    42,
		WorkflowRunID:           26392225417,
		WorkflowName:            "CI",
		WorkflowRunAttempt:      2,
		HeadBranch:              "feature/group-jobs",
		HeadSHA:                 "abc123def456",
		GitHubJobURL:            "https://github.com/qbox/las/actions/runs/26392225417/job/77684492230",
		PullRequestNumber:       3335,
		GitHubContextBackfilled: true,
		RepositoryFullName:      "qbox/las",
		RequestedLabelsJSON:     `["self-hosted"]`,
		LabelsJSON:              `["self-hosted"]`,
		RunnerName:              "e2b-77684492230",
		Status:                  StatusQueued,
		GitHubPayloadJSON:       `{"workflow_job":{"run_id":111,"workflow_name":"stale","run_attempt":9,"head_branch":"stale","head_sha":"stale","pull_requests":[{"number":1}]}}`,
	})
	if st.WorkflowRunID != 26392225417 || st.WorkflowName != "CI" || st.WorkflowRunAttempt != 2 {
		t.Fatalf("expected state to use denormalized github context, got %#v", st)
	}
	if st.HeadBranch != "feature/group-jobs" || st.HeadSHA != "abc123def456" || st.PullRequestNumber != 3335 {
		t.Fatalf("expected state to use denormalized grouping fields, got %#v", st)
	}
	wantURL := "https://github.com/qbox/las/actions/runs/26392225417/job/77684492230?pr=3335"
	if st.GitHubJobURL != wantURL {
		t.Fatalf("expected denormalized github job url, got %q", st.GitHubJobURL)
	}
}

func TestMigrateBackfillsLegacyRunnerRequestGitHubContext(t *testing.T) {
	dir := t.TempDir()
	databaseURL := dir + "/runnerd.db"
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: false,
	}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := db.Exec(`CREATE TABLE runner_requests (
		id TEXT PRIMARY KEY,
		source TEXT NOT NULL,
		workflow_job_id INTEGER,
		github_installation_id INTEGER,
		repository_full_name TEXT,
		requested_labels_json TEXT,
		labels_json TEXT NOT NULL,
		profile_name TEXT,
		runner_group TEXT,
		runner_name TEXT NOT NULL,
		status TEXT NOT NULL,
		failure_stage TEXT,
		failure_reason TEXT,
		last_error_code TEXT,
		last_error_message TEXT,
		last_error_retryable BOOLEAN NOT NULL DEFAULT FALSE,
		retry_count INTEGER NOT NULL DEFAULT 0,
		sandbox_id TEXT,
		process_pid INTEGER,
		assigned_job_id INTEGER,
		assigned_job_name TEXT,
		error TEXT,
		github_payload_json TEXT,
		queued_at TIMESTAMP NOT NULL,
		last_attempt_at TIMESTAMP,
		next_retry_at TIMESTAMP,
		creating_at TIMESTAMP,
		running_at TIMESTAMP,
		stopping_at TIMESTAMP,
		completed_at TIMESTAMP,
		failed_at TIMESTAMP,
		lease_owner TEXT,
		lease_expires_at TIMESTAMP,
		updated_at TIMESTAMP NOT NULL,
		version INTEGER NOT NULL DEFAULT 0
	);`).Error; err != nil {
		t.Fatal(err)
	}
	payload := `{"workflow_job":{"run_id":26392225417,"workflow_name":"CI","run_attempt":2,"head_branch":"feature/group-jobs","head_sha":"abc123def456","pull_requests":[{"number":3335}]}}`
	if err := db.Exec(`INSERT INTO runner_requests (
		id, source, workflow_job_id, github_installation_id, repository_full_name,
		requested_labels_json, labels_json, runner_name, status, github_payload_json,
		queued_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"77684492230", "github", 77684492230, 42, "qbox/las",
		`["self-hosted"]`, `["self-hosted"]`, "e2b-77684492230", StatusQueued, payload,
		now, now).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	migrated := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	states, err := migrated.ListStatesForGitHubInstallations([]int64{42}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 {
		t.Fatalf("expected one migrated runner request, got %d", len(states))
	}
	st := states[0]
	if st.WorkflowRunID != 26392225417 || st.PullRequestNumber != 3335 {
		t.Fatalf("expected github context after migration, got %#v", st)
	}
	if st.WorkflowName != "CI" || st.WorkflowRunAttempt != 2 || st.HeadBranch != "feature/group-jobs" || st.HeadSHA != "abc123def456" {
		t.Fatalf("unexpected github context after migration: %#v", st)
	}

	db, err = migrated.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{
		"workflow_run_id",
		"workflow_name",
		"workflow_run_attempt",
		"head_branch",
		"head_sha",
		"github_job_url",
		"pull_request_number",
		"github_context_backfilled",
	} {
		if !db.Migrator().HasColumn(&runnerRequestRecord{}, column) {
			t.Fatalf("expected migrated runner_requests.%s column", column)
		}
	}
	var record runnerRequestRecord
	if err := db.First(&record, "id = ?", "77684492230").Error; err != nil {
		t.Fatal(err)
	}
	if record.WorkflowRunID != 26392225417 || record.PullRequestNumber != 3335 {
		t.Fatalf("expected migrated runner request columns to be backfilled: %#v", record)
	}
	if record.WorkflowName != "CI" || record.WorkflowRunAttempt != 2 || record.HeadBranch != "feature/group-jobs" || record.HeadSHA != "abc123def456" {
		t.Fatalf("unexpected backfilled runner request columns: %#v", record)
	}
	wantURL := "https://github.com/qbox/las/actions/runs/26392225417/job/77684492230?pr=3335"
	if record.GitHubJobURL != wantURL {
		t.Fatalf("expected github job url to be backfilled, got %q", record.GitHubJobURL)
	}
	if !record.GitHubContextBackfilled {
		t.Fatalf("expected github context backfill marker to be set")
	}
	var pendingBackfills int64
	if err := db.Model(&runnerRequestRecord{}).
		Where("github_payload_json <> ''").
		Where("github_context_backfilled IS NULL OR github_context_backfilled = ?", false).
		Count(&pendingBackfills).Error; err != nil {
		t.Fatal(err)
	}
	if pendingBackfills != 0 {
		t.Fatalf("expected no pending github context backfills, got %d", pendingBackfills)
	}
	if record.GitHubPayloadJSON != payload {
		t.Fatal("migration must preserve the historical webhook payload")
	}
	if err := migrated.migrate(db); err != nil {
		t.Fatal(err)
	}
	var afterSecondMigration runnerRequestRecord
	if err := db.First(&afterSecondMigration, "id = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(afterSecondMigration, record) {
		t.Fatal("repeated migration must preserve the historical payload and backfilled record")
	}
}

func TestMigratePreservesAdditiveRunnerRequestColumns(t *testing.T) {
	dir := t.TempDir()
	databaseURL := dir + "/runnerd.db"
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: false,
	}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE runner_requests (
		id TEXT PRIMARY KEY,
		source TEXT NOT NULL,
		workflow_job_id INTEGER,
		repository_full_name TEXT,
		requested_labels_json TEXT,
		labels_json TEXT NOT NULL,
		profile_name TEXT,
		runner_group TEXT,
		runner_name TEXT NOT NULL,
		status TEXT NOT NULL,
		failure_stage TEXT,
		failure_reason TEXT,
		last_error_code TEXT,
		last_error_message TEXT,
		last_error_retryable BOOLEAN NOT NULL DEFAULT FALSE,
		retry_count INTEGER NOT NULL DEFAULT 0,
		sandbox_id TEXT,
		process_pid INTEGER,
		assigned_job_id INTEGER,
		assigned_job_name TEXT,
		error TEXT,
		github_payload_json TEXT,
		queued_at DATETIME NOT NULL,
		last_attempt_at DATETIME,
		next_retry_at DATETIME,
		creating_at DATETIME,
		running_at DATETIME,
		stopping_at DATETIME,
		completed_at DATETIME,
		failed_at DATETIME,
		lease_owner TEXT,
		lease_expires_at DATETIME,
		updated_at DATETIME NOT NULL,
		version INTEGER NOT NULL DEFAULT 0
	);`).Error; err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		"ALTER TABLE runner_requests ADD COLUMN github_installation_id INTEGER",
		"ALTER TABLE runner_requests ADD COLUMN workflow_run_id INTEGER",
		"ALTER TABLE runner_requests ADD COLUMN workflow_name TEXT",
		"ALTER TABLE runner_requests ADD COLUMN workflow_run_attempt INTEGER",
		"ALTER TABLE runner_requests ADD COLUMN head_branch TEXT",
		"ALTER TABLE runner_requests ADD COLUMN head_sha TEXT",
		"ALTER TABLE runner_requests ADD COLUMN github_job_url TEXT",
		"ALTER TABLE runner_requests ADD COLUMN pull_request_number INTEGER",
		"ALTER TABLE runner_requests ADD COLUMN github_context_backfilled NUMERIC NOT NULL DEFAULT FALSE",
		"ALTER TABLE runner_requests ADD COLUMN sandbox_api_url TEXT",
		"ALTER TABLE runner_requests ADD COLUMN sandbox_api_key_encrypted TEXT",
		"ALTER TABLE runner_requests ADD COLUMN sandbox_config_source TEXT",
	} {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	originalUpdatedAt := time.Date(2026, time.July, 15, 15, 49, 3, 0, time.UTC)
	payload := `{"installation":{"id":135340026},"workflow_job":{"run_id":29401048027,"workflow_name":"CI Check And Test","run_attempt":2,"head_branch":"feature/migration-integrity","head_sha":"abc123"}}`
	if err := db.Exec(
		`INSERT INTO runner_requests (
		id, source, workflow_job_id, repository_full_name, requested_labels_json,
		labels_json, profile_name, runner_group, runner_name, status,
		github_payload_json, queued_at, updated_at, version,
		github_installation_id, workflow_run_id, workflow_name, workflow_run_attempt,
		head_branch, head_sha, github_job_url, github_context_backfilled,
		sandbox_api_url, sandbox_api_key_encrypted, sandbox_config_source
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"87401142867", "github_webhook", 87401142867, "qbox/las", `["github-runner-ubuntu-24-04"]`,
		`["self-hosted","e2b","github-runner-ubuntu-24-04"]`, "github-runner-ubuntu-24-04", "Default",
		"e2b-87401142867", StatusCompleted, payload, originalUpdatedAt.Add(-time.Minute), originalUpdatedAt, 7,
		135340026, 29401048027, "CI Check And Test", 2, "feature/migration-integrity", "abc123",
		"https://github.com/qbox/las/actions/runs/29401048027/job/87401142867", true,
		"https://sandbox.example.test", "encrypted-api-key", "admin_default",
	).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	migrated := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	db, err = migrated.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	var record runnerRequestRecord
	if err := db.First(&record, "id = ?", "87401142867").Error; err != nil {
		t.Fatal(err)
	}
	if record.GitHubInstallationID != 135340026 {
		t.Fatalf("github installation id changed during migration: %d", record.GitHubInstallationID)
	}
	if record.SandboxAPIURL != "https://sandbox.example.test" ||
		record.SandboxAPIKeyEncrypted != "encrypted-api-key" ||
		record.SandboxConfigSource != "admin_default" {
		t.Fatalf("sandbox configuration changed during migration: %#v", record)
	}
	if !record.UpdatedAt.Equal(originalUpdatedAt) {
		t.Fatalf("updated_at changed during migration: got %s want %s", record.UpdatedAt, originalUpdatedAt)
	}
	for _, indexName := range []string{
		"idx_runner_requests_queued_id",
		"idx_runner_requests_github_installation_queued_id",
		"idx_runner_requests_profile_queued_id",
	} {
		if !db.Migrator().HasIndex(&runnerRequestRecord{}, indexName) {
			t.Fatalf("expected runner request list ordering index %s after additive migration", indexName)
		}
	}
}

func TestMigrateRepairsMissingRunnerRequestInstallationID(t *testing.T) {
	dir := t.TempDir()
	databaseURL := dir + "/runnerd.db"
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	workflowJobID := int64(87401142867)
	originalUpdatedAt := time.Date(2026, time.July, 15, 15, 49, 3, 0, time.UTC)
	record := runnerRequestRecord{
		ID:                      "87401142867",
		Source:                  "github_webhook",
		WorkflowJobID:           &workflowJobID,
		GitHubInstallationID:    0,
		WorkflowRunID:           29401048027,
		WorkflowName:            "CI Check And Test",
		WorkflowRunAttempt:      2,
		HeadBranch:              "feature/migration-integrity",
		HeadSHA:                 "abc123",
		GitHubContextBackfilled: true,
		RepositoryFullName:      "qbox/las",
		RequestedLabelsJSON:     `["github-runner-ubuntu-24-04"]`,
		LabelsJSON:              `["self-hosted","e2b","github-runner-ubuntu-24-04"]`,
		ProfileName:             "github-runner-ubuntu-24-04",
		RunnerGroup:             "Default",
		RunnerName:              "e2b-87401142867",
		Status:                  StatusCompleted,
		GitHubPayloadJSON:       `{"installation":{"id":135340026},"workflow_job":{"run_id":29401048027}}`,
		QueuedAt:                originalUpdatedAt.Add(-time.Minute),
		UpdatedAt:               originalUpdatedAt,
		Version:                 7,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	migrated := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	db, err = migrated.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	var repaired runnerRequestRecord
	if err := db.First(&repaired, "id = ?", record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if repaired.GitHubInstallationID != 135340026 {
		t.Fatalf("github installation id was not repaired: %d", repaired.GitHubInstallationID)
	}
	if repaired.GitHubPayloadJSON != record.GitHubPayloadJSON {
		t.Fatal("installation id repair must preserve the historical webhook payload")
	}
	if !repaired.UpdatedAt.Equal(originalUpdatedAt) {
		t.Fatalf("updated_at changed during repair: got %s want %s", repaired.UpdatedAt, originalUpdatedAt)
	}
}

func TestMigrateDoesNotRewriteUnrecoverableRunnerRequestInstallationID(t *testing.T) {
	dir := t.TempDir()
	databaseURL := dir + "/runnerd.db"
	store := NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    databaseURL,
		MigrateOnStart: true,
	}).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	workflowJobID := int64(87401142868)
	now := time.Now().UTC()
	record := runnerRequestRecord{
		ID:                      "87401142868",
		Source:                  "github_webhook",
		WorkflowJobID:           &workflowJobID,
		GitHubContextBackfilled: true,
		RepositoryFullName:      "qbox/las",
		RequestedLabelsJSON:     `["github-runner-ubuntu-24-04"]`,
		LabelsJSON:              `["self-hosted","e2b","github-runner-ubuntu-24-04"]`,
		ProfileName:             "github-runner-ubuntu-24-04",
		RunnerGroup:             "Default",
		RunnerName:              "e2b-87401142868",
		Status:                  StatusCompleted,
		GitHubPayloadJSON:       `{"installation":null}`,
		QueuedAt:                now.Add(-time.Minute),
		UpdatedAt:               now,
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TRIGGER reject_unrecoverable_installation_rewrite
		BEFORE UPDATE ON runner_requests
		WHEN OLD.id = '87401142868'
		BEGIN
			SELECT RAISE(ABORT, 'unrecoverable installation row was rewritten');
		END`).Error; err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	for start := 1; start <= 2; start++ {
		migrated := NewWithOptions(Options{
			Backend:        BackendSQLite,
			DatabaseDSN:    databaseURL,
			MigrateOnStart: true,
		}).(*DBStore)
		db, err = migrated.dbOrEnsure()
		if err != nil {
			t.Fatalf("migration start %d rewrote unrecoverable installation row: %v", start, err)
		}
		closeTestDB(t, db)
	}
}

func copySQLiteSnapshot(t *testing.T, sourcePath string) string {
	t.Helper()
	source, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	destinationPath := filepath.Join(t.TempDir(), "runnerd.db")
	destination, err := os.Create(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(destination, source); err != nil {
		_ = destination.Close()
		t.Fatal(err)
	}
	if err := destination.Close(); err != nil {
		t.Fatal(err)
	}
	return destinationPath
}

func TestMigrateSQLiteRunnerRequestSnapshot(t *testing.T) {
	sourcePath := strings.TrimSpace(os.Getenv("RUNNERD_SQLITE_SNAPSHOT"))
	if sourcePath == "" {
		t.Skip("set RUNNERD_SQLITE_SNAPSHOT to verify a disposable copy of a production SQLite database")
	}
	databaseURL := copySQLiteSnapshot(t, sourcePath)

	type counts struct {
		Total                            int64 `gorm:"column:total"`
		GitHubInstallationIDs            int64 `gorm:"column:github_installation_ids"`
		RecoverableGitHubInstallationIDs int64 `gorm:"column:recoverable_github_installation_ids"`
		SandboxAPIURLs                   int64 `gorm:"column:sandbox_api_urls"`
		SandboxAPIKeys                   int64 `gorm:"column:sandbox_api_keys"`
		SandboxConfigSources             int64 `gorm:"column:sandbox_config_sources"`
	}
	readCounts := func(db *gorm.DB) counts {
		t.Helper()
		var result counts
		if err := db.Raw(`SELECT
			COUNT(*) AS total,
			SUM(CASE WHEN github_installation_id > 0 THEN 1 ELSE 0 END) AS github_installation_ids,
			SUM(CASE
				WHEN github_installation_id > 0 THEN 0
				WHEN json_valid(github_payload_json) THEN
					CASE WHEN CAST(json_extract(github_payload_json, '$.installation.id') AS INTEGER) > 0 THEN 1 ELSE 0 END
				ELSE 0
			END) AS recoverable_github_installation_ids,
			SUM(CASE WHEN sandbox_api_url <> '' THEN 1 ELSE 0 END) AS sandbox_api_urls,
			SUM(CASE WHEN sandbox_api_key_encrypted <> '' THEN 1 ELSE 0 END) AS sandbox_api_keys,
			SUM(CASE WHEN sandbox_config_source <> '' THEN 1 ELSE 0 END) AS sandbox_config_sources
			FROM runner_requests`).Scan(&result).Error; err != nil {
			t.Fatal(err)
		}
		return result
	}
	openStore := func(migrate bool) *gorm.DB {
		t.Helper()
		store := NewWithOptions(Options{
			Backend:        BackendSQLite,
			DatabaseDSN:    databaseURL,
			MigrateOnStart: migrate,
		}).(*DBStore)
		db, err := store.dbOrEnsure()
		if err != nil {
			t.Fatal(err)
		}
		return db
	}

	db := openStore(false)
	before := readCounts(db)
	closeTestDB(t, db)

	db = openStore(true)
	afterFirstStart := readCounts(db)
	closeTestDB(t, db)
	if afterFirstStart.Total != before.Total {
		t.Fatalf("runner request count changed after migration: before=%d after=%d", before.Total, afterFirstStart.Total)
	}
	expectedGitHubInstallationIDs := before.GitHubInstallationIDs + before.RecoverableGitHubInstallationIDs
	if afterFirstStart.GitHubInstallationIDs != expectedGitHubInstallationIDs ||
		afterFirstStart.RecoverableGitHubInstallationIDs != 0 {
		t.Fatalf(
			"github installation ids were not fully repaired: before=%#v after=%#v expected_ids=%d",
			before,
			afterFirstStart,
			expectedGitHubInstallationIDs,
		)
	}
	if afterFirstStart.SandboxAPIURLs != before.SandboxAPIURLs ||
		afterFirstStart.SandboxAPIKeys != before.SandboxAPIKeys ||
		afterFirstStart.SandboxConfigSources != before.SandboxConfigSources {
		t.Fatalf("sandbox snapshot counts changed after migration: before=%#v after=%#v", before, afterFirstStart)
	}

	db = openStore(true)
	afterSecondStart := readCounts(db)
	closeTestDB(t, db)
	if afterSecondStart != afterFirstStart {
		t.Fatalf("second migration changed runner request data: first=%#v second=%#v", afterFirstStart, afterSecondStart)
	}
	t.Logf("runner request migration counts: before=%#v after=%#v", before, afterSecondStart)
}

func TestReconcileManagedProfilesRecordsCatalogMutationAudit(t *testing.T) {
	store := New(t.TempDir())
	profile := managedProfileForReconciliation("managed-audit", 1)
	if conflicts, err := store.ReconcileManagedProfiles([]RunnerProfile{profile}); err != nil || len(conflicts) != 0 {
		t.Fatalf("reconcile managed profile: conflicts=%#v err=%v", conflicts, err)
	}
	events, err := store.ListAuditEvents(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "profile.reconcile" || events[0].ResourceID != profile.Name {
		t.Fatalf("managed reconciliation audit events = %#v", events)
	}
}
