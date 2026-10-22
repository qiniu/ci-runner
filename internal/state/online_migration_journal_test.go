package state

import (
	"errors"
	"fmt"
	"os"
	"testing"
)

func testMigrationJournalGroupGap(t *testing.T, source *DBStore, replica *onlineMigrationReplica, missing string) {
	t.Helper()
	ctx := migrationTestContext(t)
	if source.opts.Backend == BackendSQLite {
		finishMigrationBaseline(t, replica, migrationTestSnapshot(t, source), 10)
	}
	for i := int64(1); i <= 3; i++ {
		if err := source.db.Create(&accountRecord{ID: 1000000 + i, Role: "user"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	reader, err := newOnlineMigrationSource(ctx, source)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var groups []migrationTransactionRecord
	if err := reader.db.Where("epoch = ?", reader.control.Epoch).Order("position").Find(&groups).Error; err != nil || len(groups) != 3 {
		t.Fatalf("unexpected source transaction chain: %d %v", len(groups), err)
	}
	removed := groups[1]
	if missing == "tail" {
		removed = groups[2]
	}
	if err := reader.db.Where("position = ?", removed.Position).Delete(&migrationTransactionRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	if err := reader.db.Where("transaction_position = ?", removed.Position).Delete(&migrationChangeRecord{}).Error; err != nil {
		t.Fatal(err)
	}
	for range 4 {
		applied, err := replica.applyNext(ctx, reader)
		if err != nil {
			if !errors.Is(err, errOnlineMigrationNotReady) {
				t.Fatal(err)
			}
			checkpoint, readErr := readMigrationCheckpoint(replica.db, reader.control)
			want := int64(0)
			if missing == "middle" {
				want = groups[0].Position
			}
			if readErr != nil || checkpoint.Position != want {
				t.Fatalf("broken chain advanced replica checkpoint: %+v %v", checkpoint, readErr)
			}
			return
		}
		if !applied {
			t.Fatal("replication silently accepted a missing committed transaction group")
		}
	}
	t.Fatal("replication advanced beyond a broken commit chain")
}

func TestOnlineMigrationJournalGroupGap(t *testing.T) {
	for _, missing := range []string{"middle", "tail"} {
		t.Run(missing, func(t *testing.T) {
			source, _ := newCapturedSQLite(t)
			_, replica := newMigrationReplicaSQLite(t)
			testMigrationJournalGroupGap(t, source, replica, missing)
		})
	}
}

func TestOnlineMigrationJournalSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("set RUNNERD_CATALOG_BACKEND_TESTS=1 with a dedicated PostgreSQL test database")
	}
	dsn := os.Getenv("RUNNERD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("RUNNERD_POSTGRES_TEST_DSN is required")
	}
	for _, missing := range []string{"middle", "tail"} {
		t.Run(missing, func(t *testing.T) {
			source, _ := newCapturedSQLite(t)
			_, replica := newMigrationReplicaPostgres(t, dsn)
			testMigrationJournalGroupGap(t, source, replica, missing)
		})
		t.Run("postgres-source-"+missing, func(t *testing.T) {
			c, _, source := newReadyMigrationCoordinator(t, dsn)
			if err := c.switchPrimary(migrationTestContext(t), migrationHandoffOptions()); err != nil {
				t.Fatal(err)
			}
			testMigrationJournalGroupGap(t, source, c.replica, missing)
		})
	}
	t.Run("rolled-back-sequence-gap", func(t *testing.T) {
		c, _, postgresStore := newReadyMigrationCoordinator(t, dsn)
		ctx := migrationTestContext(t)
		if err := c.switchPrimary(ctx, migrationHandoffOptions()); err != nil {
			t.Fatal(err)
		}
		for i := range 4 {
			if _, _, err := c.store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: fmt.Sprint(i), OAuthLogin: "live"}, "user"); err != nil {
				t.Fatal(err)
			}
			tx := postgresStore.db.Begin()
			if tx.Error != nil {
				t.Fatal(tx.Error)
			}
			if err := tx.Create(&accountRecord{Role: "rolled-back"}).Error; err != nil {
				t.Fatal(err)
			}
			if err := tx.Rollback().Error; err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := c.store.UpsertAccountForOAuthIdentity(OAuthIdentity{OAuthProvider: "github", OAuthSubject: "last", OAuthLogin: "last"}, "user"); err != nil {
			t.Fatal(err)
		}
		if _, err := c.refreshVerification(ctx, 10); err != nil {
			t.Fatalf("legitimate sequence allocation gaps broke replication: %v", err)
		}
	})
}
