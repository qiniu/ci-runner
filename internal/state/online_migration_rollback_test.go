package state

import (
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestOnlineMigrationRollbackSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("set RUNNERD_CATALOG_BACKEND_TESTS=1 with a dedicated PostgreSQL test database")
	}
	dsn := os.Getenv("RUNNERD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("RUNNERD_POSTGRES_TEST_DSN is required")
	}
	for _, stage := range []string{"authority_prepared", "authority_committed", "target_primary_enabled"} {
		t.Run(stage, func(t *testing.T) {
			c, sqliteStore, postgresStore := newReadyMigrationCoordinator(t, dsn)
			ctx := migrationTestContext(t)
			if err := c.switchPrimary(ctx, migrationHandoffOptions()); err != nil {
				t.Fatal(err)
			}
			account, _, err := c.store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "PG-only", OAuthLogin: "PG-only"}, "user")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.refreshVerification(ctx, 10); err != nil {
				t.Fatal(err)
			}
			_ = c.source.Close()
			if err := c.authority.Close(); err != nil {
				t.Fatal(err)
			}
			command := exec.Command(os.Args[0], "-test.run=^TestOnlineMigrationHandoffProcessHelper$")
			command.Env = append(os.Environ(), "RUNNERD_TEST_HANDOFF_DIRECTORY="+c.authority.directory,
				"RUNNERD_TEST_HANDOFF_SQLITE="+sqliteStore.opts.DatabaseDSN, "RUNNERD_TEST_HANDOFF_POSTGRES="+dsn,
				"RUNNERD_TEST_HANDOFF_STAGE="+stage)
			output, err := command.CombinedOutput()
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 23 {
				t.Fatalf("rollback child did not stop at its durable boundary: %v %s", err, output)
			}
			authority, err := openOnlineMigrationAuthority(c.authority.directory)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()
			if err := recoverOnlineMigrationEndpoints(ctx, authority, sqliteStore, postgresStore); err != nil {
				t.Fatal(err)
			}
			manifest, _, err := authority.read()
			if err != nil {
				t.Fatal(err)
			}
			wantPrimary, wantEpoch := BackendSQLite, int64(3)
			if stage == "authority_prepared" {
				wantPrimary, wantEpoch = BackendPostgres, 2
			}
			if manifest.Primary != wantPrimary || manifest.Epoch != wantEpoch {
				t.Fatalf("rollback recovery reopened wrong authority: %s/%d", manifest.Primary, manifest.Epoch)
			}
			recovered, err := newOnlineMigrationCoordinator(ctx, authority, sqliteStore, postgresStore)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.source.Close()
			if _, err := recovered.store.GetAccount(account.ID); err != nil {
				t.Fatalf("rollback crash lost a PostgreSQL-only commit: %v", err)
			}
		})
	}
}
