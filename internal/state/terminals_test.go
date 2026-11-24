package state

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestTerminalOwnershipAndExpiration(t *testing.T) {
	s := New(t.TempDir()).(*DBStore)
	testTerminalOwnership(t, s)
}

func testTerminalOwnership(t *testing.T, s *DBStore) {
	t.Helper()
	ctx := context.Background()
	row := TerminalSession{ID: "terminal-" + time.Now().Format("150405.000000000"), Owner: "a", AccountID: 1, ResourceKind: "runner", ResourceID: "request", Binding: "attempt-1", SandboxID: "sandbox", RepositoryFullName: "owner/repo", GitHubInstallationID: 42}
	if err := s.CreateTerminalSession(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTerminalSession(ctx, row); err == nil {
		t.Fatal("duplicate creation accepted")
	}
	if ok, err := s.TouchTerminalSession(ctx, row.ID, "b", true); err != nil || ok {
		t.Fatal("foreign owner activated", err)
	}
	if ok, err := s.TouchTerminalSession(ctx, row.ID, "a", true); err != nil || !ok {
		t.Fatal("activation", err)
	}
	if ok, _ := s.TouchTerminalSession(ctx, row.ID, "a", true); ok {
		t.Fatal("second activation accepted")
	}
	if err := s.FinishTerminalSession(ctx, row.ID, "b", "confirmed", "disconnect"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTerminalSession(ctx, row.ID)
	if err != nil || got.Status != "active" || got.Cleanup != "unknown" {
		t.Fatalf("foreign finish %+v %v", got, err)
	}
	db, _ := s.dbOrEnsure()
	if db.Dialector.Name() == BackendMySQL {
		testUnchangedTerminalRenewal(t, s, db, row.ID)
	}
	if err := db.Model(&TerminalSession{}).Where("id = ?", row.ID).UpdateColumn("lease_until", 1).Error; err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.TouchTerminalSession(ctx, row.ID, "a", false); ok {
		t.Fatal("expired lease renewed")
	}
	if err := s.ExpireTerminalSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTerminalSession(ctx, row.ID, "a", "confirmed", "late"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTerminalSession(ctx, row.ID)
	if got.Status != "closed" || got.Cleanup != "unknown" || got.CloseReason != "connection_owner_lost" {
		t.Fatalf("late completion overwrote recovery: %+v", got)
	}
	row.ID += "-normal"
	if err := s.CreateTerminalSession(ctx, row); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTerminalSession(ctx, row.ID, "a", "confirmed", "disconnected"); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetTerminalSession(ctx, row.ID)
	if got.Status != "closed" || got.Cleanup != "confirmed" {
		t.Fatalf("normal close %+v", got)
	}
}

// Pin both clocks on one MySQL connection so the second UPDATE changes no columns.
func testUnchangedTerminalRenewal(t *testing.T, s *DBStore, db *gorm.DB, id string) {
	t.Helper()
	fixed := time.Now().UTC().Truncate(time.Second)
	err := db.Connection(func(conn *gorm.DB) error {
		if err := conn.Exec("SET timestamp = ?", fixed.Unix()).Error; err != nil {
			return err
		}
		defer func() {
			if err := conn.Exec("SET timestamp = 0").Error; err != nil {
				t.Error(err)
			}
		}()
		pinned := &DBStore{opts: s.opts, db: conn.Session(&gorm.Session{NowFunc: func() time.Time { return fixed }}), migrated: true}
		for range 2 {
			ok, err := pinned.TouchTerminalSession(t.Context(), id, "a", false)
			if err != nil {
				return err
			}
			if !ok {
				t.Error("unchanged live renewal lost ownership")
			}
		}
		ok, err := pinned.TouchTerminalSession(t.Context(), id, "b", false)
		if ok {
			t.Error("foreign owner renewed a lease")
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTerminalSessionUpgradePreservesRunner(t *testing.T) {
	dir := t.TempDir()
	old := New(dir).(*DBStore)
	_, st, err := old.CreateRequest(RunnerRequest{ID: "upgrade-console", RunnerName: "runner", GitHubInstallationID: 42, RepositoryFullName: "owner/repo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.SandboxID = "saved-sandbox"
	st.LastAttemptAt = time.Now().UTC().Truncate(time.Millisecond)
	if err = old.WriteState(st); err != nil {
		t.Fatal(err)
	}
	before, _ := old.ReadState(st.ID)
	db, _ := old.dbOrEnsure()
	if err = db.Migrator().DropTable(&TerminalSession{}); err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)
	upgraded := New(dir).(*DBStore)
	if err = upgraded.Ensure(); err != nil {
		t.Fatal(err)
	}
	after, err := upgraded.ReadState(st.ID)
	if err != nil || after.SandboxID != before.SandboxID || after.GitHubInstallationID != 42 || !after.LastAttemptAt.Equal(before.LastAttemptAt) || !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("upgrade changed runner: %+v %v", after, err)
	}
	db, _ = upgraded.dbOrEnsure()
	if !db.Migrator().HasTable(&TerminalSession{}) {
		t.Fatal("new table missing")
	}
	closeTestDB(t, db)
}
