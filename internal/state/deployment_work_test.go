package state

import (
	"fmt"
	"testing"
	"time"
)

func TestDeploymentWorkPagesKeepSchedulingLanesBoundedAndComplete(t *testing.T) {
	testDeploymentWorkPages(t, New(t.TempDir()).(*DBStore))
}

func testDeploymentWorkPages(t *testing.T, store *DBStore) {
	t.Helper()
	// Use an exact second so every SQL dialect stores the expiry boundary equally.
	now := time.Now().UTC().Truncate(time.Second)
	want := map[string]int{"all": 210}
	for i := range 210 {
		d := deploymentFixture(fmt.Sprintf("work-%03d", i), 1)
		d.Status = []string{DeploymentStatusQueued, DeploymentStatusPlanning, DeploymentStatusCreating, DeploymentStatusRunning, DeploymentStatusStopping, DeploymentStatusFailed}[i%6]
		d.SandboxID = d.ID
		d.ExpiresAt = now.Add(time.Hour)
		if i%2 == 0 {
			d.ExpiresAt = now.Add(-time.Hour)
		}
		if _, err := store.CreateDeployment(d); err != nil {
			t.Fatal(err)
		}
		switch {
		case d.Status == DeploymentStatusStopping || d.Status == DeploymentStatusFailed || d.ExpiresAt.Before(now):
			want["cleanup"]++
		case d.Status == DeploymentStatusRunning:
			want["monitor"]++
		default:
			want["provision"]++
		}
	}
	for _, fixture := range []struct {
		id, status string
		sandbox    string
	}{
		{"work-stopping-empty", DeploymentStatusStopping, ""},
		{"work-exact-expiry", DeploymentStatusRunning, "exact"},
		{"work-failed-clean", DeploymentStatusFailed, ""},
		{"work-completed", DeploymentStatusCompleted, "released"},
		{"work-expired", DeploymentStatusExpired, "released"},
	} {
		d := deploymentFixture(fixture.id, 1)
		d.Status, d.SandboxID, d.ExpiresAt = fixture.status, fixture.sandbox, now
		if _, err := store.CreateDeployment(d); err != nil {
			t.Fatal(err)
		}
	}
	want["all"] += 2
	want["cleanup"] += 2
	for kind, count := range want {
		after := ""
		seen := make(map[string]bool)
		for {
			items, more, err := store.ListDeploymentWork(kind, after, 17, now)
			if err != nil || len(items) > 17 {
				t.Fatalf("%s page=%d err=%v", kind, len(items), err)
			}
			for _, item := range items {
				if item.ID <= after || seen[item.ID] {
					t.Fatalf("%s repeated or reordered %s after %s", kind, item.ID, after)
				}
				seen[item.ID], after = true, item.ID
			}
			if !more {
				break
			}
		}
		if len(seen) != count {
			t.Fatalf("%s scheduled %d, want %d", kind, len(seen), count)
		}
	}
	items, err := store.ListActiveDeployments()
	if err != nil || len(items) != 100 {
		t.Fatalf("active summary must also be bounded: %d %v", len(items), err)
	}
	for _, input := range []struct {
		kind  string
		limit int
	}{{"invalid", 10}, {"all", 0}, {"all", 101}} {
		if _, _, err := store.ListDeploymentWork(input.kind, "", input.limit, now); err == nil {
			t.Fatalf("accepted invalid work page: %#v", input)
		}
	}
}
