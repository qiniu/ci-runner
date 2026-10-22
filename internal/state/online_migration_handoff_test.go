package state

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"gorm.io/gorm"
)

func newReadyMigrationCoordinator(t *testing.T, dsn string) (*onlineMigrationCoordinator, *DBStore, *DBStore) {
	t.Helper()
	source, _ := newCapturedSQLite(t)
	target, replica := newMigrationReplicaPostgres(t, dsn)
	db, _ := source.dbOrEnsure()
	stamp := time.Date(2026, 10, 22, 12, 0, 0, 123456789, time.UTC)
	if err := db.Transaction(func(tx *gorm.DB) error {
		for _, model := range []any{
			&accountRecord{ID: 1, Role: "user", CreatedAt: stamp, UpdatedAt: stamp},
			&accountRecord{ID: 100000, Role: "deleted", CreatedAt: stamp, UpdatedAt: stamp},
			&runnerProfileRecord{Name: "public", LabelsJSON: `["qiniu","ubuntu"]`, DefaultTemplateName: "public-template", TemplateSource: TemplateSourcePublic, CreatedAt: stamp, UpdatedAt: stamp},
			&scopedRunnerProfileRecord{ScopeType: RunnerProfileScopeAccount, ScopeID: 1, Name: "custom-update", WorkflowLabelsJSON: `["custom-update"]`, LabelKey: "update-key", TemplateID: "custom", CreatedAt: stamp, UpdatedAt: stamp},
			&scopedRunnerProfileRecord{ScopeType: RunnerProfileScopeAccount, ScopeID: 1, Name: "custom-delete", WorkflowLabelsJSON: `["custom-delete"]`, LabelKey: "delete-key", TemplateID: "custom", CreatedAt: stamp, UpdatedAt: stamp},
			&runnerRequestRecord{ID: "runner", Source: "test", LabelsJSON: "[]", RunnerName: "runner", Status: StatusRunning, ProcessPID: 123, SandboxID: "saved-sandbox", Version: 7, QueuedAt: stamp, UpdatedAt: stamp},
		} {
			if err := tx.Create(model).Error; err != nil {
				return err
			}
		}
		return tx.Delete(&accountRecord{}, 100000).Error
	}); err != nil {
		t.Fatal(err)
	}
	deployment, err := source.CreateDeployment(deploymentFixture("deploy", 1))
	if err != nil {
		t.Fatal(err)
	}
	deployment.Status, deployment.SandboxID = DeploymentStatusRunning, "saved-deploy"
	if err := source.UpdateDeployment(deployment); err != nil {
		t.Fatal(err)
	}
	snapshot := migrationTestSnapshot(t, source)
	finishMigrationBaseline(t, replica, snapshot, 10)
	if _, err := replica.verifySnapshot(migrationTestContext(t), snapshot, 10); err != nil {
		t.Fatal(err)
	}
	authority, err := openOnlineMigrationAuthority(filepath.Join(t.TempDir(), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = authority.Close() })
	manifest := testMigrationManifest(t)
	commitTestManifest(t, authority, manifest)
	for _, phase := range []onlineMigrationPhase{onlineMigrationCapturing, onlineMigrationBaseline, onlineMigrationCatchingUp, onlineMigrationReady} {
		manifest.Revision++
		manifest.Phase = phase
		commitTestManifest(t, authority, manifest)
	}
	coordinator, err := newOnlineMigrationCoordinator(migrationTestContext(t), authority, source, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = coordinator.source.Close() })
	return coordinator, source, target
}

func migrationHandoffOptions() onlineMigrationSwitchOptions {
	return onlineMigrationSwitchOptions{QueueBudget: 100, MaxFinalChanges: 10000, RecoveryTimeout: 5 * time.Second}
}

func TestOnlineMigrationHandoffSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("set RUNNERD_CATALOG_BACKEND_TESTS=1 with a dedicated PostgreSQL test database")
	}
	dsn := os.Getenv("RUNNERD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("RUNNERD_POSTGRES_TEST_DSN is required")
	}
	t.Run("roundtrip", func(t *testing.T) {
		c, sqliteStore, postgresStore := newReadyMigrationCoordinator(t, dsn)
		ctx := migrationTestContext(t)
		originalFacade := c.store
		oldSQLite, _ := sqliteStore.dbOrEnsure()
		oldPostgres, _ := postgresStore.dbOrEnsure()
		c.store.AppendLog("runner", "stdout.log", []byte("before-cutover\n"))
		if err := c.switchPrimary(ctx, migrationHandoffOptions()); err != nil {
			t.Fatal(err)
		}
		manifest, _, err := c.authority.read()
		if err != nil || manifest.Primary != BackendPostgres || manifest.Epoch != 2 || c.store != originalFacade {
			t.Fatalf("forward handoff lost authority or replaced facade: %v", err)
		}
		for name, old := range map[string]*gorm.DB{"old SQLite primary": oldSQLite, "old PG replica": oldPostgres} {
			if err := old.Create(&accountRecord{Role: "stale"}).Error; err == nil {
				t.Fatalf("%s wrote in a different epoch", name)
			}
		}
		account, _, err := c.store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "new-user", OAuthLogin: "new-user"}, "user")
		if err != nil || account.ID <= 100000 {
			t.Fatalf("historical SQLite allocation was reused: id=%d err=%v", account.ID, err)
		}
		var burnedID int64
		rollback := errors.New("rollback generated account")
		_, err = c.store.ApplyMutationWithAudit(AuditEvent{Actor: "test", Action: "rollback"}, func(tx Store) error {
			burned, _, err := tx.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "rolled-back", OAuthLogin: "rolled-back"}, "user")
			burnedID = burned.ID
			if err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) || burnedID <= account.ID {
			t.Fatalf("PG sequence rollback fixture failed: burned=%d err=%v", burnedID, err)
		}
		c.store.AppendStagedLog("runner", "stdout.log", "runner_hook", []byte("on-postgres\n"))
		deployment, err := c.store.GetDeployment("deploy")
		if err != nil {
			t.Fatal(err)
		}
		deployment.ProcessID = "pg-process"
		if err := c.store.UpdateDeployment(deployment); err != nil {
			t.Fatal(err)
		}
		pgPrimaryHandle, _ := postgresStore.dbOrEnsure()
		pgProfile, err := c.store.GetProfile("public")
		if err != nil {
			t.Fatal(err)
		}
		scope := RunnerProfileScope{Type: RunnerProfileScopeAccount, ID: 1}
		pgCustom, err := c.store.GetScopedProfile(scope, "custom-update")
		if err != nil {
			t.Fatal(err)
		}
		pgDelete, err := c.store.GetScopedProfile(scope, "custom-delete")
		if err != nil {
			t.Fatal(err)
		}
		if err := c.switchPrimary(ctx, migrationHandoffOptions()); err != nil {
			t.Fatal(err)
		}
		manifest, _, err = c.authority.read()
		if err != nil || manifest.Primary != BackendSQLite || manifest.Epoch != 3 || c.store != originalFacade {
			t.Fatalf("rollback lost authority or replaced facade: %v", err)
		}
		if err := pgPrimaryHandle.Create(&accountRecord{Role: "stale PG"}).Error; err == nil {
			t.Fatal("old PG primary wrote after rollback")
		}
		if got, err := c.store.GetAccount(account.ID); err != nil || got.ID != account.ID {
			t.Fatalf("new PG account lost on rollback: %v", err)
		}
		if _, err := c.store.GetAccount(burnedID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("rolled-back PG account survived: %v", err)
		}
		if got, err := c.store.GetDeployment("deploy"); err != nil || got.ProcessID != "pg-process" || got.SandboxID != "saved-deploy" {
			t.Fatalf("deployment evidence lost on rollback: %v", err)
		}
		if got, err := c.store.ReadState("runner"); err != nil || got.SandboxID != "saved-sandbox" || got.ProcessPID != 123 || got.Version != 7 {
			t.Fatalf("runner attempt changed during handoff: %v", err)
		}
		if log, err := c.store.ReadLog("runner", "stdout.log", 4096); err != nil || string(log) != "before-cutover\non-postgres\n" {
			t.Fatalf("log stream lost continuity: %v", err)
		}
		// A validation begun while PG was primary must still recognize the
		// unchanged revision after rollback to original SQLite nanoseconds.
		_, err = c.store.ApplyMutationWithAudit(AuditEvent{Actor: "test", Action: "profile.after_rollback"}, func(tx Store) error {
			pgProfile.Priority++
			if _, err := tx.UpsertProfileIfUnchanged(pgProfile, &pgProfile.UpdatedAt); err != nil {
				return err
			}
			pgCustom.MaxConcurrency++
			if _, err := tx.UpsertScopedProfileIfUnchanged(pgCustom, &pgCustom.UpdatedAt); err != nil {
				return err
			}
			return tx.DeleteScopedProfileIfUnchanged(scope, pgDelete.Name, &pgDelete.UpdatedAt)
		})
		if err != nil {
			t.Fatalf("unchanged PG revision failed SQLite compare-and-swap: %v", err)
		}
		if _, err := c.store.UpsertProfileIfUnchanged(pgProfile, &pgProfile.UpdatedAt); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale profile update was accepted after timestamp translation: %v", err)
		}
		if _, err := c.store.UpsertScopedProfileIfUnchanged(pgCustom, &pgCustom.UpdatedAt); !errors.Is(err, ErrConflict) {
			t.Fatalf("stale custom update was accepted after timestamp translation: %v", err)
		}
		next, _, err := c.store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "after-rollback", OAuthLogin: "after-rollback"}, "user")
		if err != nil || next.ID <= burnedID {
			t.Fatalf("SQLite reused a burned PG allocation: next=%d burned=%d err=%v", next.ID, burnedID, err)
		}
		drainMigrationSource(t, c.replica, c.source)
		if _, err := c.replica.verifySnapshot(ctx, migrationTestSnapshot(t, sqliteStore), 10); err != nil {
			t.Fatal(err)
		}
	})
	for _, stage := range []string{"source_frozen", "final_verified", "authority_prepared", "target_epoch_prepared", "source_epoch_prepared", "reverse_baseline_ready", "authority_committed", "target_primary_enabled"} {
		t.Run("failure_"+stage, func(t *testing.T) {
			c, sqliteStore, postgresStore := newReadyMigrationCoordinator(t, dsn)
			c.after = func(at string) error {
				if at == stage {
					return errors.New("injected handoff failure")
				}
				return nil
			}
			if err := c.switchPrimary(migrationTestContext(t), migrationHandoffOptions()); err == nil {
				t.Fatal("handoff ignored injected failure")
			}
			postCommit := stage == "authority_committed" || stage == "target_primary_enabled"
			manifest, _, err := c.authority.read()
			if err != nil {
				t.Fatal(err)
			}
			if postCommit {
				if manifest.Primary != BackendPostgres || manifest.Epoch != 2 {
					t.Fatal("committed handoff returned to old authority")
				}
				if _, err := c.store.GetAccount(1); !errors.Is(err, errOnlineMigrationNotReady) {
					t.Fatalf("unrecovered committed handoff served calls: %v", err)
				}
			} else {
				if manifest.Primary != BackendSQLite || manifest.Epoch != 1 {
					t.Fatal("uncommitted handoff changed authority")
				}
				if _, err := c.store.GetAccount(1); err != nil {
					t.Fatalf("cancelled handoff failed to release old primary: %v", err)
				}
			}
			if err := recoverOnlineMigrationEndpoints(migrationTestContext(t), c.authority, sqliteStore, postgresStore); err != nil {
				t.Fatal(err)
			}
			recovered, err := newOnlineMigrationCoordinator(migrationTestContext(t), c.authority, sqliteStore, postgresStore)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.source.Close()
			if _, err := recovered.store.GetAccount(1); err != nil {
				t.Fatalf("authority recovery did not reopen correct primary: %v", err)
			}
		})
	}
	for _, stage := range []string{"source_frozen", "authority_prepared", "target_epoch_prepared", "source_epoch_prepared", "reverse_baseline_ready", "authority_committed", "target_primary_enabled"} {
		t.Run("process_exit_"+stage, func(t *testing.T) {
			c, sqliteStore, postgresStore := newReadyMigrationCoordinator(t, dsn)
			directory := c.authority.directory
			if err := c.authority.Close(); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestOnlineMigrationHandoffProcessHelper$")
			command.Env = append(os.Environ(), "RUNNERD_TEST_HANDOFF_DIRECTORY="+directory,
				"RUNNERD_TEST_HANDOFF_SQLITE="+sqliteStore.opts.DatabaseDSN, "RUNNERD_TEST_HANDOFF_POSTGRES="+dsn, "RUNNERD_TEST_HANDOFF_STAGE="+stage)
			if output, err := command.CombinedOutput(); err == nil {
				t.Fatal("handoff child did not exit at the injected boundary")
			} else {
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != 23 {
					t.Fatalf("unexpected child failure: %v %s", err, output)
				}
			}
			authority, err := openOnlineMigrationAuthority(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()
			if err := recoverOnlineMigrationEndpoints(migrationTestContext(t), authority, sqliteStore, postgresStore); err != nil {
				t.Fatal(err)
			}
			manifest, _, err := authority.read()
			if err != nil {
				t.Fatal(err)
			}
			wantPrimary, wantEpoch := BackendSQLite, int64(1)
			if stage == "authority_committed" || stage == "target_primary_enabled" {
				wantPrimary, wantEpoch = BackendPostgres, 2
			}
			if manifest.Primary != wantPrimary || manifest.Epoch != wantEpoch {
				t.Fatalf("process recovery chose %s/%d, want %s/%d", manifest.Primary, manifest.Epoch, wantPrimary, wantEpoch)
			}
			recovered, err := newOnlineMigrationCoordinator(migrationTestContext(t), authority, sqliteStore, postgresStore)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.source.Close()
			if _, err := recovered.store.GetAccount(1); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, failCommit := range []int{1, 2} {
		name := "uncertain_prepare"
		if failCommit == 2 {
			name = "uncertain_commit"
		}
		t.Run(name, func(t *testing.T) {
			c, sqliteStore, postgresStore := newReadyMigrationCoordinator(t, dsn)
			calls := 0
			c.authority.syncDirectory = func(path string) error {
				calls++
				if calls == failCommit {
					return errors.New("injected directory sync error")
				}
				return syncMigrationDirectory(path)
			}
			if err := c.switchPrimary(migrationTestContext(t), migrationHandoffOptions()); err == nil {
				t.Fatal("uncertain publication was accepted")
			}
			if _, err := c.store.GetAccount(1); !errors.Is(err, errOnlineMigrationNotReady) {
				t.Fatalf("uncertain authority served calls: %v", err)
			}
			for _, endpoint := range c.endpoints {
				var control migrationControlRecord
				if err := endpoint.db.First(&control, 1).Error; err != nil || control.Role == "primary" {
					t.Fatalf("uncertain authority left a primary open: %v", err)
				}
			}
			directory := c.authority.directory
			_ = c.authority.Close()
			authority, err := openOnlineMigrationAuthority(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()
			if err := recoverOnlineMigrationEndpoints(migrationTestContext(t), authority, sqliteStore, postgresStore); err != nil {
				t.Fatal(err)
			}
			manifest, _, err := authority.read()
			if err != nil || (manifest.Primary == BackendPostgres) != (failCommit == 2) {
				t.Fatalf("uncertain publication recovered wrong authority: %v", err)
			}
		})
	}
	t.Run("cancellation_after_commit", func(t *testing.T) {
		c, _, _ := newReadyMigrationCoordinator(t, dsn)
		ctx, cancel := context.WithCancel(migrationTestContext(t))
		defer cancel()
		c.after = func(stage string) error {
			if stage == "authority_committed" {
				cancel()
			}
			return nil
		}
		if err := c.switchPrimary(ctx, migrationHandoffOptions()); err != nil {
			t.Fatal(err)
		}
		if _, err := c.store.GetAccount(1); err != nil {
			t.Fatalf("cancelled committed handoff did not finish new primary: %v", err)
		}
	})
}

func TestOnlineMigrationHandoffProcessHelper(t *testing.T) {
	directory := os.Getenv("RUNNERD_TEST_HANDOFF_DIRECTORY")
	if directory == "" {
		t.Skip("handoff crash subprocess only")
	}
	authority, err := openOnlineMigrationAuthority(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	// Explicitly omit MigrateOnStart: startup must resolve authority before
	// ordinary migrations or backfills can touch either business database.
	sqliteStore := NewWithOptions(Options{Backend: BackendSQLite, DatabaseDSN: os.Getenv("RUNNERD_TEST_HANDOFF_SQLITE")}).(*DBStore)
	postgresStore := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: os.Getenv("RUNNERD_TEST_HANDOFF_POSTGRES")}).(*DBStore)
	for _, store := range []*DBStore{sqliteStore, postgresStore} {
		if err := openMigrationEndpoint(migrationTestContext(t), store); err != nil {
			t.Fatal(err)
		}
		db, _ := store.dbOrEnsure()
		if store.opts.Backend == BackendPostgres {
			requireCatalogMatcherTestDatabase(t, db, BackendPostgres)
		}
		defer closeTestDB(t, db)
	}
	if err := recoverOnlineMigrationEndpoints(migrationTestContext(t), authority, sqliteStore, postgresStore); err != nil {
		t.Fatal(err)
	}
	coordinator, err := newOnlineMigrationCoordinator(migrationTestContext(t), authority, sqliteStore, postgresStore)
	if err != nil {
		t.Fatal(err)
	}
	defer coordinator.source.Close()
	coordinator.after = func(stage string) error {
		if stage == os.Getenv("RUNNERD_TEST_HANDOFF_STAGE") {
			os.Exit(23)
		}
		return nil
	}
	if err := coordinator.switchPrimary(migrationTestContext(t), migrationHandoffOptions()); err != nil {
		t.Fatal(err)
	}
	t.Fatal("handoff child did not reach its crash boundary")
}

func TestOnlineMigrationFinalVerificationBudgetAndDamage(t *testing.T) {
	for _, failure := range []string{"budget", "damage", "missing anchor"} {
		t.Run(failure, func(t *testing.T) {
			source, _ := newCapturedSQLite(t)
			_, replica := newMigrationReplicaSQLite(t)
			snapshot := migrationTestSnapshot(t, source)
			finishMigrationBaseline(t, replica, snapshot, 10)
			ctx := migrationTestContext(t)
			if failure != "missing anchor" {
				if _, err := replica.verifySnapshot(ctx, snapshot, 10); err != nil {
					t.Fatal(err)
				}
			}
			db, _ := source.dbOrEnsure()
			if err := db.Create(&[]accountRecord{{ID: 1, Role: "user"}, {ID: 2, Role: "admin"}}).Error; err != nil {
				t.Fatal(err)
			}
			reader, err := newOnlineMigrationSource(ctx, source)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			frozen := reader.control
			frozen.Role = "frozen"
			if err := changeMigrationControl(ctx, reader.db, reader.control, frozen); err != nil {
				t.Fatal(err)
			}
			reader.control = frozen
			drainMigrationSource(t, replica, reader)
			if failure == "damage" {
				if err := replica.transaction(ctx, func(tx *gorm.DB) error { return tx.Table("accounts").Where("id = 2").Update("role", "damaged").Error }); err != nil {
					t.Fatal(err)
				}
			}
			budget := int64(100)
			if failure == "budget" {
				budget = 1
			}
			if err := replica.verifyFinalChanges(ctx, reader, budget); !errors.Is(err, errOnlineMigrationNotReady) {
				t.Fatalf("final verifier accepted %s: %v", failure, err)
			}
			checkpoint, err := readMigrationCheckpoint(replica.db, frozen)
			if err != nil || checkpoint.VerifiedPosition != 0 {
				t.Fatalf("failed final check advanced proof: %v", err)
			}
		})
	}
}
