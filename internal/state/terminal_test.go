package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTerminalSharedOwnershipAndCleanup(t *testing.T) {
	opts := Options{Backend: BackendSQLite, DatabaseDSN: filepath.Join(t.TempDir(), "shared.db"), MigrateOnStart: true}
	testTerminalSharedOwnership(t, opts)
}

func testTerminalSharedOwnership(t *testing.T, opts Options) {
	ctx := context.Background()
	a, b := NewWithOptions(opts), NewWithOptions(opts)
	row := TerminalSession{ID: "session", AccountID: 17, Kind: "runner", ResourceID: "job", BindingKey: "attempt-1", SandboxID: "sb", SandboxAPIKeyEncrypted: "ciphertext"}
	if err := a.CreateTerminal(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := a.ActivateTerminal(ctx, row.ID, 42, "generation", 0); err != nil {
		t.Fatal(err)
	}
	got, err := b.GetTerminal(ctx, row.ID)
	if err != nil || got.StreamID != "generation" {
		t.Fatalf("cross-store read %+v %v", got, err)
	}
	if err := b.RenewTerminal(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	if won, err := a.ClaimTerminalCleanup(ctx, row.ID, "early"); err != nil || won {
		t.Fatalf("live PTY claimed %v %v", won, err)
	}
	if err := b.CloseTerminal(ctx, row.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.RenewTerminal(ctx, row.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("closed session renewed: %v", err)
	}
	if won, err := a.ClaimTerminalCleanup(ctx, row.ID, "owner"); err != nil || !won {
		t.Fatalf("claim %v %v", won, err)
	}
	if won, err := b.ClaimTerminalCleanup(ctx, row.ID, "other"); err != nil || won {
		t.Fatalf("duplicate claim %v %v", won, err)
	}
	if err := b.FinishTerminalCleanup(ctx, row.ID, "other", true); err != nil {
		t.Fatal(err)
	}
	got, _ = a.GetTerminal(ctx, row.ID)
	if got.Status != "closing" {
		t.Fatal("stale worker completed cleanup")
	}
	if err := a.FinishTerminalCleanup(ctx, row.ID, "owner", true); err != nil {
		t.Fatal(err)
	}
	got, _ = b.GetTerminal(ctx, row.ID)
	if got.Status != "closed" || got.SandboxAPIKeyEncrypted != "" {
		t.Fatal("cleanup did not retire saved credentials")
	}
}

func TestTerminalLateCreateCannotReopenAndExpiredSubscriberCannotRenew(t *testing.T) {
	ctx := context.Background()
	s := New(t.TempDir()).(*DBStore)
	for _, id := range []string{"closing", "expired"} {
		if err := s.CreateTerminal(ctx, TerminalSession{ID: id, AccountID: 17, Kind: "runner", BindingKey: "attempt", SandboxID: "sb"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CloseTerminal(ctx, "closing"); err != nil {
		t.Fatal(err)
	}
	if err := s.ActivateTerminal(ctx, "closing", 42, "late", 0); !errors.Is(err, ErrConflict) {
		t.Fatalf("late activation: %v", err)
	}
	row, _ := s.GetTerminal(ctx, "closing")
	if row.PID != 42 || row.Status != "closing" {
		t.Fatal("lost late positive evidence")
	}
	if err := s.ActivateTerminal(ctx, "expired", 43, "other", 0); err != nil {
		t.Fatal(err)
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&TerminalSession{}).Where("id = ?", "expired").UpdateColumn("idle_deadline", time.Now().UTC().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.RenewTerminal(ctx, "expired"); !errors.Is(err, ErrConflict) {
		t.Fatal("expired subscriber reopened PTY", err)
	}
	if won, err := s.ClaimTerminalCleanup(ctx, "expired", "new-node"); err != nil || !won {
		t.Fatalf("takeover %v %v", won, err)
	}
}

func TestTerminalSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated test databases")
	}
	for _, backend := range []struct{ name, dsn string }{{BackendPostgres, os.Getenv("RUNNERD_POSTGRES_TEST_DSN")}, {BackendMySQL, os.Getenv("RUNNERD_MYSQL_TEST_DSN")}} {
		t.Run(backend.name, func(t *testing.T) {
			opts := Options{Backend: backend.name, DatabaseDSN: backend.dsn, MigrateOnStart: false}
			setup := NewWithOptions(opts).(*DBStore)
			db, err := setup.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			requireCatalogMatcherTestDatabase(t, db, backend.name)
			if err := db.AutoMigrate(&TerminalSession{}); err != nil {
				t.Fatal(err)
			}
			defer closeTestDB(t, db)
			if err := db.Where("id = ?", "session").Delete(&TerminalSession{}).Error; err != nil {
				t.Fatal(err)
			}
			defer db.Where("id = ?", "session").Delete(&TerminalSession{})
			testTerminalSharedOwnership(t, opts)
		})
	}
}
