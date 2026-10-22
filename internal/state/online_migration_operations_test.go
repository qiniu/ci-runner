package state

import (
	"fmt"
	"os"
	"testing"
)

func TestOnlineMigrationVerificationSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("set RUNNERD_CATALOG_BACKEND_TESTS=1 with a dedicated PostgreSQL test database")
	}
	dsn := os.Getenv("RUNNERD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("RUNNERD_POSTGRES_TEST_DSN is required")
	}
	c, _, _ := newReadyMigrationCoordinator(t, dsn)
	for _, backend := range []string{BackendSQLite, BackendPostgres} {
		t.Run(backend, func(t *testing.T) {
			ctx := migrationTestContext(t)
			for i := range 12 {
				_, _, err := c.store.UpsertAccountForOAuthIdentity(OAuthIdentity{
					OAuthProvider: "github", OAuthSubject: fmt.Sprintf("%s-before-%d", backend, i), OAuthLogin: "before-view",
				}, "user")
				if err != nil {
					t.Fatal(err)
				}
			}
			var late Account
			c.after = func(stage string) error {
				if stage == "verification_view_ready" {
					var err error
					late, _, err = c.store.UpsertAccountForOAuthIdentity(OAuthIdentity{
						OAuthProvider: "github", OAuthSubject: backend + "-late", OAuthLogin: "after-view",
					}, "user")
					return err
				}
				return nil
			}
			report, err := c.refreshVerification(ctx, 2)
			c.after = nil
			if err != nil {
				t.Fatal(err)
			}
			if len(report.Tables) != len(c.inventory.Tables) {
				t.Fatal("verification omitted business tables")
			}
			checkpoint, err := readMigrationCheckpoint(c.replica.db.WithContext(ctx), c.source.control)
			if err != nil || !checkpoint.Verified || checkpoint.VerifiedPosition != report.Checkpoint || checkpoint.Position != report.Checkpoint {
				t.Fatalf("proof does not match exact verification view: %+v %v", checkpoint, err)
			}
			var count int64
			if err := c.replica.db.WithContext(ctx).Model(&accountRecord{}).Where("id = ?", late.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("replay overshot consistent view: count=%d err=%v", count, err)
			}
			if _, err := c.store.GetAccount(late.ID); err != nil {
				t.Fatalf("primary stopped serving after live verification: %v", err)
			}
			// Much more than this budget was written before the proof refreshed.
			// Only the one late mutation belongs to the final verification window.
			options := migrationHandoffOptions()
			options.MaxFinalChanges = 3
			if err := c.switchPrimary(ctx, options); err != nil {
				t.Fatalf("fresh verification anchor did not bound final delta: %v", err)
			}
			if _, err := c.store.GetAccount(late.ID); err != nil {
				t.Fatalf("handoff lost mutation committed after verification view: %v", err)
			}
		})
	}
}
