package state

import (
	"testing"
	"time"
)

func TestFillDeploymentPullRequestTitlePreservesSnapshotsAndLifecycle(t *testing.T) {
	store := New(t.TempDir())
	d := deploymentFixture("legacy-title", 1)
	d.Ref = "refs/pull/1/head"
	d.Status = DeploymentStatusCompleted
	d.PullRequestTitle = "Original PR title"
	d, err := store.CreateDeployment(d)
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.GetDeploymentRevision(d.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	// Retain a different PR in the same deployment's history.
	for _, ref := range []string{"refs/pull/2/head", "refs/pull/1/head"} {
		d.Ref, d.PullRequestTitle = ref, ""
		if err := store.RetryDeployment(d, time.Now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		d, err = store.GetDeployment(d.ID)
		if err != nil {
			t.Fatal(err)
		}
		d.Status = DeploymentStatusCompleted
		if err := store.UpdateDeployment(d); err != nil {
			t.Fatal(err)
		}
		d, err = store.GetDeployment(d.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	before := d
	for range 2 {
		if err := store.FillDeploymentPullRequestTitle(d.ID, d.Ref, "Current PR title"); err != nil {
			t.Fatal(err)
		}
	}
	after, err := store.GetDeployment(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.PullRequestTitle != "Current PR title" || after.Version != before.Version || !after.UpdatedAt.Equal(before.UpdatedAt) || after.Status != before.Status || !after.CompletedAt.Equal(before.CompletedAt) {
		t.Fatalf("title repair changed lifecycle: before=%#v after=%#v", before, after)
	}
	for number, want := range map[int64]string{1: "Original PR title", 2: "", 3: "Current PR title"} {
		revision, err := store.GetDeploymentRevision(d.ID, number)
		if err != nil || revision.PullRequestTitle != want {
			t.Fatalf("revision %d: %#v %v", number, revision, err)
		}
		if number == 1 && !revision.UpdatedAt.Equal(first.UpdatedAt) {
			t.Fatal("repair changed historical timestamp")
		}
	}
	// A late result for a different source may fill that source's history only.
	if err := store.FillDeploymentPullRequestTitle(d.ID, "refs/pull/2/head", "Other PR"); err != nil {
		t.Fatal(err)
	}
	after, err = store.GetDeployment(d.ID)
	if err != nil || after.PullRequestTitle != "Current PR title" {
		t.Fatalf("late result replaced current title: %#v %v", after, err)
	}
	if err := store.FillDeploymentPullRequestTitle(d.ID, "refs/pull/1/head", "Renamed title"); err != nil {
		t.Fatal(err)
	}
	after, err = store.GetDeployment(d.ID)
	if err != nil || after.PullRequestTitle != "Current PR title" {
		t.Fatal("repair overwrote populated title")
	}
}
