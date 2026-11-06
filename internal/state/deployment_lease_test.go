package state

import (
	"errors"
	"testing"
	"time"
)

func TestDeploymentWorkLease(t *testing.T) {
	testDeploymentWorkLease(t, New(t.TempDir()).(*DBStore))
}

func testDeploymentWorkLease(t *testing.T, store *DBStore) {
	t.Helper()
	d, err := store.CreateDeployment(deploymentFixture("lease-deployment", 1))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if ok, err := store.ClaimDeploymentWork(ctx, d, "original", time.Minute); !ok || err != nil {
		t.Fatal("claim", err)
	}
	if ok, err := store.ClaimDeploymentWork(ctx, d, "peer", time.Minute); ok || err != nil {
		t.Fatal("duplicate claim", err)
	}
	if ok, err := store.RenewDeploymentWork(ctx, d.ID, "original", time.Minute); !ok || err != nil {
		t.Fatal("renew", err)
	}
	event := DeploymentEvent{DeploymentID: d.ID, RevisionNumber: d.RevisionNumber, Attempt: d.Attempt, EventType: "output", Message: "before takeover", WorkToken: "original"}
	if err := store.AppendDeploymentEvent(event); err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&deploymentRecord{}).Where("id = ?", d.ID).UpdateColumn("work_expires_at", time.Unix(1, 0).UTC()).Error; err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateClaimedDeployment(d, "original"); !errors.Is(err, ErrConflict) {
		t.Fatalf("expired writer: %v", err)
	}
	if ok, err := store.RenewDeploymentWork(ctx, d.ID, "original", time.Minute); ok || err != nil {
		t.Fatal("revived expired claim", err)
	}
	if ok, err := store.ClaimDeploymentWork(ctx, d, "peer", time.Minute); !ok || err != nil {
		t.Fatal("takeover", err)
	}
	if err := store.ReleaseDeploymentWork(ctx, d.ID, "original"); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDeploymentEvent(event); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale output: %v", err)
	}
	if err := store.UpdateClaimedDeployment(d, "original"); !errors.Is(err, ErrConflict) {
		t.Fatalf("superseded writer: %v", err)
	}
	d.Status = DeploymentStatusCreating
	d.Attempt++
	if err := store.UpdateClaimedDeployment(d, "peer"); err != nil {
		t.Fatal("takeover lost its claim", err)
	}
	d.Version++
	d.Status = DeploymentStatusStopping
	if err := store.UpdateDeployment(d); err != nil {
		t.Fatal("explicit stop", err)
	}
	d.Version++
	if err := store.UpdateClaimedDeployment(d, "peer"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stop failed to fence creator: %v", err)
	}
	if ok, err := store.ClaimDeploymentWork(ctx, d, "stop", time.Minute); !ok || err != nil {
		t.Fatal("stop did not obtain work", err)
	}
	old := d
	old.Attempt++
	old.SandboxID = "wrong"
	if err := store.RecordDeploymentSandbox(ctx, old); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted wrong attempt", err)
	}
	old = d
	old.RevisionNumber++
	old.SandboxID = "wrong"
	if err := store.RecordDeploymentSandbox(ctx, old); !errors.Is(err, ErrConflict) {
		t.Fatal("accepted wrong revision", err)
	}
	d.SandboxID, d.RuntimeID = "original-sandbox", "original-runtime"
	if err := store.RecordDeploymentSandbox(ctx, d); err != nil {
		t.Fatal(err)
	}
	d.SandboxID = "another-sandbox"
	if err := store.RecordDeploymentSandbox(ctx, d); !errors.Is(err, ErrConflict) {
		t.Fatal("rebound Sandbox", err)
	}
	revision, err := store.GetDeploymentRevision(d.ID, d.RevisionNumber)
	if err != nil || revision.SandboxID != "original-sandbox" || revision.Status != DeploymentStatusStopping {
		t.Fatal("lost late evidence or stop", err)
	}
	if err := store.ReleaseDeploymentWork(ctx, d.ID, "stop"); err != nil {
		t.Fatal(err)
	}
}
