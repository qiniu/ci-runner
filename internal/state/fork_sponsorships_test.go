package state

import (
	"errors"
	"testing"
	"time"
)

func TestForkSponsorshipPolicyApprovalAndRequestProvenance(t *testing.T) {
	store := New(t.TempDir())
	policy := ForkSponsorshipPolicy{
		SponsorInstallationID:    200,
		SourceRepositoryID:       300,
		SourceRepositoryFullName: "acme/project",
		Mode:                     ForkSponsorshipModeApprovalRequired,
		Enabled:                  true,
		MaxConcurrency:           2,
	}
	var saved ForkSponsorshipPolicy
	if _, err := store.ApplyMutationWithAudit(AuditEvent{Actor: "github:1", Action: "fork_sponsorship_policy.create", ResourceType: "fork_sponsorship_policy", ResourceID: "200:300"}, func(tx Store) error {
		var err error
		saved, err = tx.UpsertForkSponsorshipPolicy(policy)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if saved.SourceRepositoryFullName != policy.SourceRepositoryFullName || saved.CreatedAt.IsZero() {
		t.Fatalf("unexpected saved policy: %#v", saved)
	}
	approval, err := store.UpsertForkSponsorshipApproval(ForkSponsorshipApproval{
		SponsorInstallationID:  200,
		SourceRepositoryID:     300,
		ForkRepositoryID:       400,
		ForkRepositoryFullName: "member/project",
		ForkOwnerID:            500,
		ForkOwnerLogin:         "member",
	})
	if err != nil {
		t.Fatal(err)
	}
	if approval.ForkRepositoryID != 400 {
		t.Fatalf("unexpected approval: %#v", approval)
	}
	created, st, err := store.CreateRequest(RunnerRequest{ID: "sponsored", Source: "test", ProfileName: "ubuntu", ProfileSource: "global", Labels: []string{"qiniu"}, RunnerName: "sponsored"}, nil)
	if err != nil || !created {
		t.Fatalf("create request: created=%v state=%#v err=%v", created, st, err)
	}
	st.Status = StatusRunning
	st.SponsorInstallationID = 200
	st.SponsorSourceRepositoryID = 300
	st.SponsorSourceRepositoryFullName = "acme/project"
	st.SponsorAuthorizationReason = ForkSponsorshipModeApprovalRequired
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	count, err := store.InFlightCountForForkSponsorship(200, 300)
	if err != nil || count != 1 {
		t.Fatalf("in-flight count = %d, err=%v", count, err)
	}
	st, err = store.ReadState("sponsored")
	if err != nil {
		t.Fatal(err)
	}
	st.Status = StatusFailed
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	retried, err := store.RetryRequest("sponsored", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if retried.SponsorInstallationID != 0 || retried.SponsorSourceRepositoryID != 0 || retried.SponsorSourceRepositoryFullName != "" || retried.SponsorAuthorizationReason != "" {
		t.Fatalf("retry retained sponsorship provenance: %#v", retried)
	}
	if err := store.DeleteForkSponsorshipPolicy(200, 300); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertForkSponsorshipApproval(ForkSponsorshipApproval{
		SponsorInstallationID:  200,
		SourceRepositoryID:     300,
		ForkRepositoryID:       401,
		ForkRepositoryFullName: "member/other-project",
		ForkOwnerID:            501,
		ForkOwnerLogin:         "other-member",
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("approval inserted after policy deletion: %v", err)
	}
	approvals, err := store.ListForkSponsorshipApprovals(200, 300)
	if err != nil || len(approvals) != 0 {
		t.Fatalf("approvals after policy delete = %#v, err=%v", approvals, err)
	}
}

func TestForkSponsorshipPolicyValidation(t *testing.T) {
	store := New(t.TempDir())
	for _, policy := range []ForkSponsorshipPolicy{
		{SponsorInstallationID: 1, SourceRepositoryID: 2, SourceRepositoryFullName: "acme/repo", Mode: "unknown", MaxConcurrency: 1},
		{SponsorInstallationID: 1, SourceRepositoryID: 2, SourceRepositoryFullName: "acme/repo", Mode: ForkSponsorshipModeApprovalRequired, MaxConcurrency: 0},
	} {
		if _, err := store.UpsertForkSponsorshipPolicy(policy); err == nil {
			t.Fatalf("expected validation error for %#v", policy)
		}
	}
	if _, err := store.UpsertForkSponsorshipPolicy(ForkSponsorshipPolicy{SponsorInstallationID: 10, SourceRepositoryID: 20, SourceRepositoryFullName: "acme/repo", Mode: ForkSponsorshipModeApprovalRequired, MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateForkSponsorshipPolicy(ForkSponsorshipPolicy{SponsorInstallationID: 10, SourceRepositoryID: 20, SourceRepositoryFullName: "acme/repo", Mode: ForkSponsorshipModeOrganizationMember, MaxConcurrency: 3}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate policy create error = %v, want conflict", err)
	}
	if _, err := store.UpdateForkSponsorshipPolicy(ForkSponsorshipPolicy{SponsorInstallationID: 10, SourceRepositoryID: 21, SourceRepositoryFullName: "acme/missing", Mode: ForkSponsorshipModeApprovalRequired, MaxConcurrency: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing policy update error = %v, want not found", err)
	}
	updated, err := store.UpdateForkSponsorshipPolicy(ForkSponsorshipPolicy{SponsorInstallationID: 10, SourceRepositoryID: 20, SourceRepositoryFullName: "acme/repo", Mode: ForkSponsorshipModeWritePermission, Enabled: true, MaxConcurrency: 2})
	if err != nil || updated.Mode != ForkSponsorshipModeWritePermission || !updated.Enabled || updated.MaxConcurrency != 2 {
		t.Fatalf("policy update = %#v, err=%v", updated, err)
	}
	if _, err := store.UpsertForkSponsorshipPolicy(ForkSponsorshipPolicy{SponsorInstallationID: 11, SourceRepositoryID: 20, SourceRepositoryFullName: "acme/repo", Mode: ForkSponsorshipModeWritePermission, MaxConcurrency: 2}); !errors.Is(err, ErrConflict) {
		t.Fatalf("cross-sponsor source upsert error = %v, want conflict", err)
	}
	policy, err := store.GetForkSponsorshipPolicy(10, 20)
	if err != nil || policy.Mode != ForkSponsorshipModeWritePermission || !policy.Enabled || policy.MaxConcurrency != 2 {
		t.Fatalf("conflicting upsert changed original policy: %#v err=%v", policy, err)
	}
}
