package state

import (
	"errors"
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
