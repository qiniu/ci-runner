package state

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestDeploymentNavigationCapsEachAuthorizedRepository(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	testDeploymentNavigationCaps(t, store)
}

func testDeploymentNavigationCaps(t *testing.T, store *DBStore) {
	t.Helper()
	for i := range 67 {
		d := deploymentFixture(fmt.Sprintf("nav-cap-%03d", i), 1)
		d.RepositoryFullName = "owner/navigation-z"
		d.Status = DeploymentStatusRunning
		d.CreatedAt = time.Now().UTC().Add(time.Duration(i) * time.Minute)
		if i == 65 {
			d.RepositoryFullName = "owner/navigation-a"
		}
		if i == 66 {
			d.GitHubInstallationID = 102
		}
		if _, err := store.CreateDeployment(d); err != nil {
			t.Fatal(err)
		}
	}
	access := []GitHubInstallationRepositoryAccess{{InstallationID: 101, Repositories: []string{"owner/navigation-z", "owner/navigation-a"}}}
	items, more, err := store.ListDeploymentRepositories(access, "", 100, 0)
	if err != nil || more || len(items) != 2 {
		t.Fatalf("navigation=%#v more=%v err=%v", items, more, err)
	}
	if items[0].RepositoryFullName != "owner/navigation-a" || len(items[0].ActiveDeployments) != 1 || items[1].ActiveCount != 65 || len(items[1].ActiveDeployments) != 20 {
		t.Fatalf("cap must not hide other repositories or inflate counts: %#v", items)
	}
	for i, d := range items[1].ActiveDeployments {
		if d.ID != fmt.Sprintf("nav-cap-%03d", 64-i) {
			t.Fatalf("unexpected or unauthorized capped row %s", d.ID)
		}
	}
}

func TestDeploymentRepositoryNavigationIgnoresHistoryPageAndFiltersExactAccess(t *testing.T) {
	store := New(t.TempDir())
	access := []GitHubInstallationRepositoryAccess{{InstallationID: 101, Repositories: []string{"owner/project", "owner/retired"}}}
	for i := range 107 {
		d := deploymentFixture(fmt.Sprintf("nav-%03d", i), 2)
		d.GitHubInstallationID = 101
		d.Status = DeploymentStatusCompleted
		d.CreatedAt = time.Now().Add(time.Duration(i) * time.Minute)
		if i < 5 {
			d.Status = []string{DeploymentStatusQueued, DeploymentStatusPlanning, DeploymentStatusCreating, DeploymentStatusRunning, DeploymentStatusStopping}[i]
		}
		if i == 5 {
			d.RepositoryFullName = "owner/retired"
			d.Status = DeploymentStatusFailed
		}
		if i == 6 {
			d.GitHubInstallationID = 102
		}
		if i == 7 {
			d.RepositoryFullName = "owner/hidden"
		}
		if i == 0 {
			d.Ref = "refs/pull/8/head"
			d.PullRequestTitle = "Preview deployment"
		}
		if i == 8 {
			d.RepositoryFullName = "OWNER/PROJECT"
		}
		if _, err := store.CreateDeployment(d); err != nil {
			t.Fatal(err)
		}
	}
	repos, more, err := store.ListDeploymentRepositories(access, "", 1, 0)
	if err != nil || !more || len(repos) != 1 {
		t.Fatalf("first page: %#v %v %v", repos, more, err)
	}
	r := repos[0]
	if strings.ToLower(r.RepositoryFullName) != "owner/project" || r.Total != 104 || r.ActiveCount != 5 || len(r.ActiveDeployments) != 5 {
		t.Fatalf("summary: %#v", r)
	}
	for _, d := range r.ActiveDeployments {
		if d.ID > "nav-004" {
			t.Fatalf("nonactive or unauthorized row: %#v", d)
		}
	}
	var foundTitle bool
	for _, d := range r.ActiveDeployments {
		if d.ID == "nav-000" && d.PullRequestTitle == "Preview deployment" {
			foundTitle = true
		}
	}
	if !foundTitle {
		t.Fatalf("navigation did not retain pull request title: %#v", r.ActiveDeployments)
	}
	repos, more, err = store.ListDeploymentRepositories(access, "", 1, 1)
	if err != nil || more || len(repos) != 1 || repos[0].RepositoryFullName != "owner/retired" || repos[0].ActiveCount != 0 || repos[0].Total != 1 || len(repos[0].ActiveDeployments) != 0 {
		t.Fatalf("terminal repo page: %#v %v %v", repos, more, err)
	}
	repos, _, err = store.ListDeploymentRepositories(access, "OWNER/PROJECT", 100, 0)
	if err != nil || len(repos) != 1 || repos[0].ActiveCount != 5 {
		t.Fatalf("selected repository: %#v %v", repos, err)
	}
	_, total, err := store.ListAuthorizedDeployments(access, DeploymentListOptions{RepositoryFullName: repos[0].RepositoryFullName, Limit: 1})
	if err != nil || total != 104 {
		t.Fatalf("navigation repository does not resolve all case variants: %d %v", total, err)
	}
	for _, denied := range [][]GitHubInstallationRepositoryAccess{nil, {{InstallationID: 0, Repositories: []string{"owner/project"}}}} {
		repos, more, err = store.ListDeploymentRepositories(denied, "", 100, 0)
		if err != nil || more || len(repos) != 0 {
			t.Fatalf("empty access: %#v %v %v", repos, more, err)
		}
	}
	if _, _, err := store.ListDeploymentRepositories(access, "", 501, 0); err == nil {
		t.Fatal("accepted invalid pagination")
	}
}
