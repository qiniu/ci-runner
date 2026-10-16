package state

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

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
