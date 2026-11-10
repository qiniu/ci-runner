package state

import (
	"errors"
	"testing"
)

func TestRepositoryAccessRefreshOwnership(t *testing.T) {
	testRepositoryAccessRefreshOwnership(t, New(t.TempDir()).(*DBStore))
}

func testRepositoryAccessRefreshOwnership(t *testing.T, store *DBStore) {
	t.Helper()
	const accountID = 901
	ctx := t.Context()
	claim := func() string {
		t.Helper()
		token, err := store.ClaimRepositoryAccessRefresh(ctx, accountID)
		if err != nil || token == "" {
			t.Fatalf("claim = %q, %v", token, err)
		}
		return token
	}
	save := func(token string, wantErr error) {
		t.Helper()
		err := store.FinishRepositoryAccessRefresh(ctx, accountID, token, RepositoryAccessResult{
			Access: []GitHubInstallationRepositoryAccess{{InstallationID: 987, Repositories: []string{"a/one"}}},
		})
		if !errors.Is(err, wantErr) {
			t.Fatalf("finish = %v, want %v", err, wantErr)
		}
	}
	old := claim()
	if token, err := store.ClaimRepositoryAccessRefresh(ctx, accountID); err != nil || token != "" {
		t.Fatalf("duplicate claim = %q, %v", token, err)
	}
	secret := AccountSecret{ScopeType: AccountScopeTypeAccount, ScopeID: accountID, KeyType: AccountSecretTypeGitHubOAuthToken, EncryptedValue: "fixture"}
	if _, err := store.UpsertAccountSecret(secret); err != nil {
		t.Fatal(err)
	}
	save(old, ErrConflict)
	current := claim()
	save(current, nil)
	fresh, err := store.ReadRepositoryAccessSnapshot(ctx, accountID)
	if err != nil || !fresh.Valid || fresh.RefreshDue || len(fresh.Access) != 1 || fresh.Access[0].InstallationID != 987 {
		t.Fatalf("unexpected fresh snapshot: %+v %v", fresh, err)
	}
	installation, err := store.UpsertGitHubInstallation(GitHubInstallation{AccountID: accountID, InstallationID: 987})
	if err != nil {
		t.Fatal(err)
	}
	unlinked := claim()
	if err := store.DeleteGitHubInstallation(accountID, installation.ID); err != nil {
		t.Fatal(err)
	}
	save(unlinked, ErrConflict)
	pending := claim()
	if err := store.DeleteAccountSecret(secret.ScopeType, accountID, secret.KeyType); err != nil {
		t.Fatal(err)
	}
	save(pending, ErrConflict)
	expired := claim()
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&repositoryAccessSnapshotRecord{}).Where("account_id = ?", accountID).UpdateColumn("lease_until", 1).Error; err != nil {
		t.Fatal(err)
	}
	save(expired, ErrConflict)
	save(claim(), nil)
}

func TestRepositoryAccessInvalidationRollsBackMutation(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	secret := AccountSecret{ScopeType: AccountScopeTypeAccount, ScopeID: 42, KeyType: AccountSecretTypeGitHubOAuthToken, EncryptedValue: "original"}
	if _, err := store.UpsertAccountSecret(secret); err != nil {
		t.Fatal(err)
	}
	link, err := store.UpsertGitHubInstallation(GitHubInstallation{AccountID: 42, InstallationID: 987})
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("CREATE TRIGGER reject_invalidation BEFORE UPDATE ON repository_access_snapshots BEGIN SELECT RAISE(ABORT,'fixture'); END").Error; err != nil {
		t.Fatal(err)
	}
	secret.EncryptedValue = "replacement"
	if _, err := store.UpsertAccountSecret(secret); err == nil {
		t.Fatal("token changed despite invalidation failure")
	}
	if err := store.DeleteAccountSecret(secret.ScopeType, 42, secret.KeyType); err == nil {
		t.Fatal("token deleted despite invalidation failure")
	}
	if err := store.DeleteGitHubInstallation(42, link.ID); err == nil {
		t.Fatal("unlinked despite invalidation failure")
	}
	stored, err := store.GetAccountSecret(secret.ScopeType, 42, secret.KeyType)
	if err != nil || stored.EncryptedValue != "original" {
		t.Fatalf("rolled back token = %q, %v", stored.EncryptedValue, err)
	}
	links, err := store.ListGitHubInstallations(42)
	if err != nil || len(links) != 1 || links[0].ID != link.ID {
		t.Fatalf("rolled back installations = %+v, %v", links, err)
	}
}
