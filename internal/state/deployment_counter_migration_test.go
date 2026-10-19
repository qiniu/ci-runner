package state

import (
	"errors"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
)

func TestDeploymentCounterUpgradePreservesHistoryAndRollsBackFailures(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	d, err := store.CreateDeployment(deploymentFixture("counter-upgrade", 1))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: "旧日志"}); err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	for _, column := range []string{"event_count", "event_bytes", "event_budget_initialized"} {
		if err := db.Migrator().DropColumn(&deploymentRevisionRecord{}, column); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := store.migrate(db); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: "new"}); err != nil {
		t.Fatal(err)
	}
	var revision deploymentRevisionRecord
	if err := db.Where("deployment_id = ? AND number = 1", d.ID).First(&revision).Error; err != nil {
		t.Fatal(err)
	}
	if revision.EventCount != 2 || revision.EventBytes != 12 || !revision.UpdatedAt.Equal(d.UpdatedAt) || revision.SpecJSONEncrypted != d.SpecJSONEncrypted {
		t.Fatalf("upgrade changed retained history or missed bytes: %#v", revision)
	}
	failure := errors.New("counter persistence failure")
	if err := db.Callback().Update().Before("gorm:update").Register("test:counter-failure", func(tx *gorm.DB) {
		if values, ok := tx.Statement.Dest.(map[string]any); ok && values["event_budget_initialized"] == true {
			tx.AddError(failure)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: "must roll back"}); !errors.Is(err, failure) {
		t.Fatalf("append error=%v", err)
	}
	if err := db.Callback().Update().Remove("test:counter-failure"); err != nil {
		t.Fatal(err)
	}
	events, _, err := store.ListDeploymentEvents(d.ID, 0, 10)
	if err != nil || len(events) != 2 || events[0].Message != "旧日志" || events[1].Message != "new" {
		t.Fatalf("failed counter write leaked an event: %#v %v", events, err)
	}
	if err := db.Where("deployment_id = ? AND number = 1", d.ID).First(&revision).Error; err != nil || revision.EventCount != 2 || revision.EventBytes != 12 {
		t.Fatalf("counter drift after rollback: %#v %v", revision, err)
	}
	d.Status = DeploymentStatusCompleted
	if err := store.UpdateDeployment(d); err != nil {
		t.Fatal(err)
	}
	d, err = store.GetDeployment(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RetryDeployment(d, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: "revision 2"}); err != nil {
		t.Fatal(err)
	}
	var next deploymentRevisionRecord
	if err := db.Where("deployment_id = ? AND number = 2", d.ID).First(&next).Error; err != nil || next.EventCount != 1 || next.EventBytes != 10 {
		t.Fatalf("new revision inherited old counters: %#v %v", next, err)
	}
}

func TestDeploymentCounterRollbackReupgradeRebuildsBudgets(t *testing.T) {
	testDeploymentCounterRollbackReupgrade(t, New(t.TempDir()).(*DBStore))
}

func testDeploymentCounterRollbackReupgrade(t *testing.T, store *DBStore) {
	t.Helper()
	for _, mode := range []string{"legacy_append", "legacy_delete"} {
		t.Run(mode, func(t *testing.T) {
			d, err := store.CreateDeployment(deploymentFixture("counter-rollback-"+mode, 1))
			if err != nil {
				t.Fatal(err)
			}
			appendEvent := func(message string) {
				t.Helper()
				if err := store.AppendDeploymentEvent(DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: message}); err != nil {
					t.Fatal(err)
				}
			}
			appendEvent("first")
			if mode == "legacy_delete" {
				appendEvent("retained")
			}
			db, err := store.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			var before deploymentRevisionRecord
			if err := db.Where("deployment_id = ? AND number = 1", d.ID).First(&before).Error; err != nil {
				t.Fatal(err)
			}
			// v0.2.12 maintains the event rows, but knows nothing about these
			// newer counter columns. Cover both undercount and overcount drift.
			if mode == "legacy_append" {
				legacy := make([]deploymentEventRecord, maxDeploymentEvents-1)
				for i := range legacy {
					legacy[i] = deploymentEventRecord{DeploymentID: d.ID, RevisionNumber: 1, EventType: "output", Message: "旧", CreatedAt: time.Now().UTC()}
				}
				if err := db.CreateInBatches(&legacy, 100).Error; err != nil {
					t.Fatal(err)
				}
			} else if err := db.Where("deployment_id = ? AND message = ?", d.ID, "first").Delete(&deploymentEventRecord{}).Error; err != nil {
				t.Fatal(err)
			}
			var retained int64
			if err := db.Model(&deploymentEventRecord{}).Where("deployment_id = ?", d.ID).Count(&retained).Error; err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := store.migrate(db); err != nil {
					t.Fatal(err)
				}
			}
			var invalidated deploymentRevisionRecord
			if err := db.Where("deployment_id = ? AND number = 1", d.ID).First(&invalidated).Error; err != nil {
				t.Fatal(err)
			}
			if invalidated.EventBudgetInitialized || invalidated.EventCount != before.EventCount || invalidated.EventBytes != before.EventBytes || !invalidated.UpdatedAt.Equal(before.UpdatedAt) || invalidated.SpecJSONEncrypted != before.SpecJSONEncrypted {
				t.Fatalf("startup changed history or failed to invalidate stale counters: %#v", invalidated)
			}
			var afterMigration int64
			if err := db.Model(&deploymentEventRecord{}).Where("deployment_id = ?", d.ID).Count(&afterMigration).Error; err != nil || afterMigration != retained {
				t.Fatalf("startup removed event history: before=%d after=%d err=%v", retained, afterMigration, err)
			}
			aggregates := 0
			if err := db.Callback().Row().After("gorm:row").Register("test:rollback-counter-bootstrap", func(tx *gorm.DB) {
				if strings.Contains(tx.Statement.SQL.String(), "SUM(") {
					aggregates++
				}
			}); err != nil {
				t.Fatal(err)
			}
			defer db.Callback().Row().Remove("test:rollback-counter-bootstrap")
			appendEvent("after-reupgrade")
			appendEvent("next")
			if aggregates != 1 {
				t.Fatalf("re-upgrade must bootstrap once, got %d aggregates", aggregates)
			}
			var events []deploymentEventRecord
			if err := db.Where("deployment_id = ?", d.ID).Order("id ASC").Find(&events).Error; err != nil {
				t.Fatal(err)
			}
			var bytes int64
			for _, event := range events {
				bytes += int64(len(event.Message))
			}
			var revision deploymentRevisionRecord
			if err := db.Where("deployment_id = ? AND number = 1", d.ID).First(&revision).Error; err != nil {
				t.Fatal(err)
			}
			if len(events) > maxDeploymentEvents || bytes > maxDeploymentEventBytes || revision.EventCount != int64(len(events)) || revision.EventBytes != bytes || !revision.EventBudgetInitialized || !revision.UpdatedAt.Equal(before.UpdatedAt) || events[len(events)-1].Message != "next" {
				t.Fatalf("re-upgrade left drift or changed timestamps: count=%d bytes=%d revision=%#v", len(events), bytes, revision)
			}
		})
	}
}
