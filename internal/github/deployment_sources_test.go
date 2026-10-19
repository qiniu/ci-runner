package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDeploymentSourceEscapesRepositorySegments(t *testing.T) {
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.EscapedPath() != "/repos/ow%3Fner/re%23po/pulls" || r.URL.Query().Get("state") != "open" || len(r.URL.Query()) != 3 {
			t.Errorf("repository changed request structure: %s", r.URL)
		}
		fmt.Fprint(w, `[]`)
	}))
	defer api.Close()
	client := NewClient(api.URL, api.Client())
	if _, _, err := client.ListDeploymentPullRequests(t.Context(), "ow?ner/re#po", 1); err != nil {
		t.Fatal(err)
	}
	for _, repository := range []string{"owner", "owner/repo/extra", "owner/..", "./repo", "/repo", "owner/"} {
		if _, _, err := client.ListDeploymentPullRequests(t.Context(), repository, 1); err == nil {
			t.Fatalf("accepted malformed repository %q", repository)
		}
	}
	if calls != 1 {
		t.Fatalf("invalid repository reached provider: %d requests", calls)
	}
}

func TestDeploymentSourceCommitMembershipIsBounded(t *testing.T) {
	const sha = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.HasPrefix(r.URL.Path, "/repos/o/r/compare/") {
			fmt.Fprintf(w, `{"merge_base_commit":{"sha":%q}}`, sha)
			return
		}
		if r.URL.Path == "/repos/o/r/pulls" {
			fmt.Fprint(w, `[{"number":7},{"number":8}]`)
			return
		}
		if r.URL.Path != "/repos/o/r/pulls/7/commits" {
			t.Errorf("wrong scope %s", r.URL.Path)
		}
		w.Header().Set("Link", `<https://untrusted.invalid/next>; rel="next"`)
		fmt.Fprint(w, `[{"sha":"other"}]`)
	}))
	defer api.Close()
	client := NewClient(api.URL, api.Client())
	ok, err := client.PullRequestContainsCommit(context.Background(), "o/r", 7, sha)
	if err != nil || ok || calls != 3 {
		t.Fatalf("unbounded lookup calls=%d ok=%v err=%v", calls, ok, err)
	}
	ok, err = client.IsDeploymentAncestor(context.Background(), "o/r", sha, strings.Repeat("b", 40))
	if err != nil || !ok {
		t.Fatalf("ancestor %v %v", ok, err)
	}
	refs, more, err := client.ListDeploymentPullRequests(context.Background(), "o/r", 1)
	if err != nil || more || strings.Join(refs, ",") != "7,8" {
		t.Fatalf("refs %v %v %v", refs, more, err)
	}
}
