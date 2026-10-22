package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func resetMigrationRuntimeDatabase(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	store := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn}).(*DBStore)
	if err := openMigrationEndpoint(migrationTestContext(t), store); err != nil {
		t.Fatal(err)
	}
	db := store.db
	requireCatalogMatcherTestDatabase(t, db, BackendPostgres)
	clean := func() {
		resetSQLBackendTestTables(t, db)
		for _, model := range []any{&migrationBaselineRecord{}, &migrationCheckpointRecord{}, &migrationChangeRecord{}, &migrationTransactionRecord{}, &migrationRetentionRecord{}, &migrationFrontierRecord{}, &migrationControlRecord{}} {
			if err := db.Migrator().DropTable(model); err != nil {
				t.Error(err)
			}
		}
		inventory, _ := newOnlineMigrationInventory()
		for _, table := range inventory.Tables {
			if err := db.Exec("DROP FUNCTION IF EXISTS " + migrationQuoted("runnerd_mig_"+table.Name) + "()").Error; err != nil {
				t.Error(err)
			}
		}
	}
	clean()
	t.Cleanup(func() { clean(); closeTestDB(t, db) })
	return db
}

func TestOnlineMigrationRuntimeSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("set RUNNERD_CATALOG_BACKEND_TESTS=1 with a dedicated PostgreSQL test database")
	}
	dsn := os.Getenv("RUNNERD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("RUNNERD_POSTGRES_TEST_DSN is required")
	}
	for _, outcome := range []string{"finish-postgres", "rollback-finish", "abort", "terminal-crash"} {
		t.Run(outcome, func(t *testing.T) {
			resetMigrationRuntimeDatabase(t, dsn)
			source := New(t.TempDir()).(*DBStore)
			if _, _, err := source.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "initial", OAuthLogin: "initial"}, "admin"); err != nil {
				t.Fatal(err)
			}
			stamp := time.Date(2026, 10, 22, 10, 0, 0, 123456789, time.UTC)
			if err := source.db.Create(&runnerProfileRecord{
				Name: "time-compat", TemplateSource: TemplateSourcePublic,
				DefaultTemplateName: "public", LabelsJSON: `["qiniu"]`, CreatedAt: stamp, UpdatedAt: stamp,
			}).Error; err != nil {
				t.Fatal(err)
			}
			closeTestDB(t, source.db)
			options := OnlineMigrationOptions{
				Directory: filepath.Join(t.TempDir(), "authority"), SQLiteDSN: source.opts.DatabaseDSN,
				PostgresDSN: dsn, Initialize: true, PauseBudget: 3 * time.Second,
			}
			ctx := migrationTestContext(t)
			runtime, err := OpenOnlineMigration(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = runtime.Close() }()
			if _, err := OpenOnlineMigration(ctx, options); err == nil {
				t.Fatal("second process acquired the same authority")
			}
			ordinary := NewWithOptions(Options{Backend: BackendSQLite, DatabaseDSN: options.SQLiteDSN, MigrateOnStart: true})
			if err := ordinary.Ensure(); !errors.Is(err, errOnlineMigrationNotReady) {
				t.Fatalf("ordinary startup bypassed migration authority: %v", err)
			}
			wrong := options
			wrong.Directory = filepath.Join(t.TempDir(), "wrong-authority")
			if _, err := OpenOnlineMigration(ctx, wrong); err == nil {
				t.Fatal("captured database accepted a newly created authority")
			}
			options.Initialize = false
			if err := runtime.Close(); err != nil {
				t.Fatal(err)
			}
			runtime, err = OpenOnlineMigration(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			if outcome == "abort" {
				if err := runtime.Execute(ctx, "abort"); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := runtime.Execute(ctx, "prepare"); err != nil {
					t.Fatal(err)
				}
				// Restart with partial baseline and recover the same artifact.
				if _, err := runtime.coordinator.baseline(ctx, 1, 1); err != nil {
					t.Fatal(err)
				}
				if err := runtime.Close(); err != nil {
					t.Fatal(err)
				}
				runtime, err = OpenOnlineMigration(ctx, options)
				if err != nil {
					t.Fatal(err)
				}
				for range 10 {
					if err := runtime.Execute(ctx, "baseline"); err != nil {
						t.Fatal(err)
					}
					status, err := runtime.Status(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if status.Phase == string(onlineMigrationCatchingUp) {
						break
					}
				}
				if err := runtime.Execute(ctx, "verify"); err != nil {
					t.Fatal(err)
				}
				facade := runtime.Store()
				if err := runtime.Execute(ctx, "cutover"); err != nil {
					t.Fatal(err)
				}
				if err := runtime.Execute(ctx, "cutover"); err == nil {
					t.Fatal("repeated cutover rolled authority back")
				}
				account, _, err := facade.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "pg-new", OAuthLogin: "pg-new"}, "user")
				if err != nil {
					t.Fatal(err)
				}
				if outcome == "rollback-finish" {
					if err := runtime.Execute(ctx, "verify"); err != nil {
						t.Fatal(err)
					}
					if err := runtime.Execute(ctx, "rollback"); err != nil {
						t.Fatal(err)
					}
					if _, err := facade.GetAccount(account.ID); err != nil {
						t.Fatal("rollback lost PostgreSQL-only account")
					}
				}
				if outcome == "terminal-crash" {
					runtime.coordinator.after = func(stage string) error {
						if stage == "terminal_authority_committed" {
							return errors.New("injected terminal crash")
						}
						return nil
					}
				}
				err = runtime.Execute(ctx, "finish")
				if (outcome == "terminal-crash") != (err != nil) {
					t.Fatalf("unexpected terminal result: %v", err)
				}
				if outcome != "terminal-crash" && facade != runtime.Store() {
					t.Fatal("finish replaced Store facade")
				}
			}
			if err := runtime.Close(); err != nil {
				t.Fatal(err)
			}
			runtime, err = OpenOnlineMigration(ctx, options)
			if err != nil {
				t.Fatal(err)
			}
			status, err := runtime.Status(ctx)
			if err != nil || (status.Phase != "completed" && status.Phase != "aborted") {
				t.Fatalf("terminal recovery failed: %+v %v", status, err)
			}
			if err := runtime.Execute(ctx, "replay"); err == nil {
				t.Fatal("completed migration still accepts replication commands")
			}
			if err := runtime.Close(); err != nil {
				t.Fatal(err)
			}
			backend, primaryDSN := BackendPostgres, dsn
			if outcome == "abort" || outcome == "rollback-finish" {
				backend, primaryDSN = BackendSQLite, options.SQLiteDSN
			}
			standalone := NewWithOptions(Options{Backend: backend, DatabaseDSN: primaryDSN, MigrateOnStart: true}).(*DBStore)
			if err := standalone.Ensure(); err != nil {
				t.Fatalf("final authoritative database cannot start without migration configuration: %v", err)
			}
			defer closeTestDB(t, standalone.db)
			if _, _, err := standalone.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "standalone", OAuthLogin: "standalone"}, "user"); err != nil {
				t.Fatal(err)
			}
			if backend == BackendSQLite {
				profile, err := standalone.GetProfile("time-compat")
				if err != nil {
					t.Fatal(err)
				}
				expected := stamp.Truncate(time.Microsecond)
				profile.Priority++
				if _, err := standalone.UpsertProfileIfUnchanged(profile, &expected); err != nil {
					t.Fatalf("finalized SQLite lost compatibility with an unchanged PG revision: %v", err)
				}
			}
		})
	}
}

func TestOnlineMigrationRuntimeRequiresDeadlineAndExistingFile(t *testing.T) {
	if _, err := OpenOnlineMigration(context.Background(), OnlineMigrationOptions{}); err == nil {
		t.Fatal("unbounded unconfigured startup accepted")
	}
}
