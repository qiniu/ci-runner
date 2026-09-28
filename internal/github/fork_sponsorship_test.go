package github

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForkSponsorshipRepositoryAndEligibilityMetadata(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/member/project":
			_, _ = w.Write([]byte(`{"id":400,"full_name":"member/project","fork":true,"owner":{"id":500,"login":"member","type":"User"},"source":{"id":300,"full_name":"acme/project","owner":{"id":600,"login":"acme","type":"Organization"}}}`))
		case "/repos/acme/project/collaborators/member/permission":
			_, _ = w.Write([]byte(`{"permission":"maintain","user":{"id":500,"login":"member"}}`))
		case "/orgs/acme/memberships/member":
			_, _ = w.Write([]byte(`{"state":"active","role":"member","user":{"id":500,"login":"member"}}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	repository, err := client.GetRepository(t.Context(), "member/project")
	if err != nil {
		t.Fatal(err)
	}
	if !repository.Fork || repository.Owner.ID != 500 || repository.Source == nil || repository.Source.ID != 300 || repository.Source.Owner.ID != 600 {
		t.Fatalf("unexpected fork metadata: %#v", repository)
	}
	permission, err := client.GetRepositoryCollaboratorPermission(t.Context(), "acme/project", "member")
	if err != nil || permission.Permission != "maintain" || permission.UserID != 500 {
		t.Fatalf("unexpected permission: %#v err=%v", permission, err)
	}
	membership, err := client.GetOrganizationMembership(t.Context(), "acme/project", "acme", "member")
	if err != nil || membership.State != "active" || membership.UserID != 500 {
		t.Fatalf("unexpected membership: %#v err=%v", membership, err)
	}
}
