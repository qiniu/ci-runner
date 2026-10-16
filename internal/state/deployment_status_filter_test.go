package state

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDeploymentStatusFiltersAndCountsUseAuthorizedSourceBeforePagination(t *testing.T) {
	store := New(t.TempDir())
	access := []GitHubInstallationRepositoryAccess{{InstallationID: 101, Repositories: []string{"owner/project"}}}
	statuses := []string{DeploymentStatusQueued, DeploymentStatusPlanning, DeploymentStatusCreating, DeploymentStatusRunning, DeploymentStatusStopping, DeploymentStatusCompleted, DeploymentStatusExpired, DeploymentStatusFailed}
	for i := range 113 {
		d := deploymentFixture(fmt.Sprintf("status-%03d", i), 2)
		d.GitHubInstallationID = 101
		d.Ref = "refs/heads/feature/preview"
		d.Status = DeploymentStatusCompleted
		if i < len(statuses) {
			d.Status = statuses[i]
		}
		switch i {
		case 109:
			d.GitHubInstallationID = 102
		case 110:
			d.RepositoryFullName = "owner/hidden"
		case 111:
			d.Ref = "refs/tags/feature/preview"
		case 112:
			d.Ref = "refs/heads/other"
		}
		if _, err := store.CreateDeployment(d); err != nil {
			t.Fatal(err)
		}
	}
	options := DeploymentListOptions{RepositoryFullName: "OWNER/PROJECT", Ref: "refs/heads/feature/preview", CommitSHA: strings.Repeat("a", 40), Limit: 1, Offset: 1, Status: "failed"}
	want := DeploymentStatusCounts{All: 109, Running: 1, Processing: 4, Stopped: 103, Failed: 1}
	counts, err := store.CountAuthorizedDeploymentsByStatus(access, options)
	if err != nil || counts != want {
		t.Fatalf("counts include another source, pair or page: %+v %v", counts, err)
	}
	for filter, total := range map[string]int64{"": 109, "all": 109, "running": 1, "processing": 4, "stopped": 103, "failed": 1, "running,processing": 5, "stopped,failed": 104, "failed,running": 2, "running,processing,stopped,failed": 109} {
		options.Status, options.Offset = filter, 0
		if filter == "processing" || filter == "stopped" {
			options.Offset = 1
		}
		items, got, err := store.ListAuthorizedDeployments(access, options)
		if err != nil || got != total || len(items) != 1 {
			t.Fatalf("%s: items=%v total=%d err=%v", filter, items, got, err)
		}
		allowed, _ := DeploymentListStatusValues(filter)
		if len(allowed) > 0 && !slices.Contains(allowed, items[0].Status) {
			t.Fatalf("%s includes %s", filter, items[0].Status)
		}
	}
	options.Status = "running"
	options.Offset = 1
	items, total, err := store.ListAuthorizedDeployments(access, options)
	if err != nil || total != 1 || len(items) != 0 {
		t.Fatalf("filtered offset: %#v %d %v", items, total, err)
	}
	if counts, err := store.CountAuthorizedDeploymentsByStatus(nil, options); err != nil || counts != (DeploymentStatusCounts{}) {
		t.Fatalf("unauthorized counts: %+v %v", counts, err)
	}
	for _, status := range []string{"running' OR 1=1", "running,unknown", ",running", "running,", "running,running", "all,running"} {
		options.Status = status
		if _, _, err := store.ListAuthorizedDeployments(access, options); err == nil {
			t.Fatalf("accepted invalid status filter %q", status)
		}
	}
}

func TestDeploymentStatusCountsUseCurrentStateForHistoricalSourceMatch(t *testing.T) {
	store := New(t.TempDir())
	d := deploymentFixture("historical-status", 1)
	d.GitHubInstallationID = 101
	d.Ref = "refs/pull/7/head"
	d.Status = DeploymentStatusCompleted
	d, err := store.CreateDeployment(d)
	if err != nil {
		t.Fatal(err)
	}
	d.Ref = "refs/heads/main"
	d.CommitSHA = strings.Repeat("b", 40)
	if err := store.RetryDeployment(d, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	access := []GitHubInstallationRepositoryAccess{{InstallationID: 101, Repositories: []string{"owner/project"}}}
	options := DeploymentListOptions{Ref: "refs/pull/7/head", Status: "processing", Limit: 1}
	counts, err := store.CountAuthorizedDeploymentsByStatus(access, options)
	if err != nil || counts.All != 1 || counts.Processing != 1 || counts.Stopped != 0 {
		t.Fatalf("historical source counts: %+v %v", counts, err)
	}
	items, total, err := store.ListAuthorizedDeployments(access, options)
	if err != nil || total != 1 || len(items) != 1 || items[0].ContextRevision == nil || items[0].ContextRevision.RevisionNumber != 1 || items[0].ContextRevision.Status != DeploymentStatusCompleted {
		t.Fatalf("historical source list: %#v %d %v", items, total, err)
	}
}
