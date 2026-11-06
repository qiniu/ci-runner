package state

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

func TestWriteStateCompletedWithoutCleanupLeavesStoppingTimeUnset(t *testing.T) {
	store := New(t.TempDir())
	_, st, err := store.CreateRequest(RunnerRequest{
		ID:         "completed-timestamps",
		Source:     "test",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-completed-timestamps",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	completedAt := time.Date(2026, time.September, 11, 11, 29, 13, 0, time.UTC)
	st.Status = StatusCompleted
	st.CompletedAt = completedAt
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	got, err := store.ReadState(st.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.CompletedAt.Equal(completedAt) {
		t.Fatalf("CompletedAt = %s, want %s", got.CompletedAt, completedAt)
	}
	if !got.StoppingAt.IsZero() {
		t.Fatalf("StoppingAt = %s, want zero without a cleanup transition", got.StoppingAt)
	}
}

// ---------- InFlightCount ----------

func TestInFlightCountExcludesQueuedAndCompleted(t *testing.T) {
	store := New(t.TempDir())

	// queued (should not be counted)
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID: "if-queued", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-if-queued",
	}, nil); err != nil {
		t.Fatal(err)
	}

	// running (should be counted)
	_, stRunning, err := store.CreateRequest(RunnerRequest{
		ID: "if-running", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-if-running",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stRunning.Status = StatusRunning
	if err := store.WriteState(stRunning); err != nil {
		t.Fatal(err)
	}

	// creating (should be counted)
	_, stCreating, err := store.CreateRequest(RunnerRequest{
		ID: "if-creating", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-if-creating",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stCreating.Status = StatusCreating
	if err := store.WriteState(stCreating); err != nil {
		t.Fatal(err)
	}

	// completed (should not be counted)
	_, stDone, err := store.CreateRequest(RunnerRequest{
		ID: "if-done", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-if-done",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stDone.Status = StatusCompleted
	if err := store.WriteState(stDone); err != nil {
		t.Fatal(err)
	}

	count, err := store.InFlightCount()
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("InFlightCount: got %d, want 2 (running + creating only)", count)
	}
}

func TestInFlightCountIncludesStopping(t *testing.T) {
	store := New(t.TempDir())

	_, st, err := store.CreateRequest(RunnerRequest{
		ID: "if-stopping", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-if-stopping",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = StatusRunning
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	st, err = store.ReadState("if-stopping")
	if err != nil {
		t.Fatal(err)
	}
	st.Status = StatusStopping
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	count, err := store.InFlightCount()
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("InFlightCount: got %d, want 1 (stopping counts)", count)
	}
}

// ---------- ActiveCountForProfile / InFlightCountForProfile ----------

func TestActiveCountForProfileCountsAllActiveStatuses(t *testing.T) {
	store := New(t.TempDir())

	// alpha: 1 queued + 1 running
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID: "acp-alpha-queued", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-acp-alpha-queued", ProfileName: "alpha",
	}, nil); err != nil {
		t.Fatal(err)
	}
	_, stRun, err := store.CreateRequest(RunnerRequest{
		ID: "acp-alpha-running", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-acp-alpha-running", ProfileName: "alpha",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stRun.Status = StatusRunning
	if err := store.WriteState(stRun); err != nil {
		t.Fatal(err)
	}

	// beta: 1 queued
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID: "acp-beta-queued", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-acp-beta-queued", ProfileName: "beta",
	}, nil); err != nil {
		t.Fatal(err)
	}

	alphaCount, err := store.ActiveCountForProfile("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if alphaCount != 2 {
		t.Errorf("ActiveCountForProfile(alpha): got %d, want 2", alphaCount)
	}

	betaCount, err := store.ActiveCountForProfile("beta")
	if err != nil {
		t.Fatal(err)
	}
	if betaCount != 1 {
		t.Errorf("ActiveCountForProfile(beta): got %d, want 1", betaCount)
	}
}

func TestInFlightCountForProfileExcludesQueued(t *testing.T) {
	store := New(t.TempDir())

	// queued: should NOT be counted by InFlightCountForProfile
	if _, _, err := store.CreateRequest(RunnerRequest{
		ID: "icp-queued", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-icp-queued", ProfileName: "gamma",
	}, nil); err != nil {
		t.Fatal(err)
	}

	// running: should be counted
	_, stRun, err := store.CreateRequest(RunnerRequest{
		ID: "icp-running", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-icp-running", ProfileName: "gamma",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stRun.Status = StatusRunning
	if err := store.WriteState(stRun); err != nil {
		t.Fatal(err)
	}

	count, err := store.InFlightCountForProfile("gamma")
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("InFlightCountForProfile(gamma): got %d, want 1 (only running, not queued)", count)
	}
}

func TestInFlightCountForProfileExcludesScopedCustomRequestsWithSameName(t *testing.T) {
	store := New(t.TempDir())
	requests := []RunnerRequest{
		{ID: "legacy-global", ProfileName: "shared", Labels: []string{"global"}, RunnerName: "legacy-global"},
		{ID: "explicit-global", ProfileName: "shared", ProfileSource: "global", Labels: []string{"global"}, RunnerName: "explicit-global"},
		{ID: "scoped-custom", ProfileName: "shared", ProfileSource: "scoped_custom", ProfileScopeType: RunnerProfileScopeAccount, ProfileScopeID: 1, Labels: []string{"custom"}, RunnerName: "scoped-custom"},
	}
	for _, request := range requests {
		_, runnerState, err := store.CreateRequest(request, nil)
		if err != nil {
			t.Fatal(err)
		}
		runnerState.Status = StatusRunning
		if err := store.WriteState(runnerState); err != nil {
			t.Fatal(err)
		}
	}

	count, err := store.InFlightCountForProfile("shared")
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("InFlightCountForProfile(shared) = %d, want 2 legacy/global requests", count)
	}
}

func TestInFlightCountForRunnerAccountAndRepository(t *testing.T) {
	dir := t.TempDir()
	store := New(dir)
	requests := []RunnerRequest{
		{ID: "account-one-a", GitHubInstallationID: 101, RepositoryFullName: "Acme/One"},
		{ID: "account-one-b", GitHubInstallationID: 101, RepositoryFullName: "acme/two"},
		{ID: "account-two", GitHubInstallationID: 202, RepositoryFullName: "acme/one"},
	}
	for _, request := range requests {
		_, state, err := store.CreateRequest(request, nil)
		if err != nil {
			t.Fatal(err)
		}
		state.Status = StatusRunning
		if err := store.WriteState(state); err != nil {
			t.Fatal(err)
		}
	}
	accountCount, err := store.InFlightCountForRepositoryOwner("ACME")
	if err != nil || accountCount != 3 {
		t.Fatalf("owner acme count = %d, err=%v; want 3", accountCount, err)
	}
	repositoryCount, err := store.InFlightCountForRepository("ACME/ONE")
	if err != nil || repositoryCount != 2 {
		t.Fatalf("repository acme/one count = %d, err=%v; want 2", repositoryCount, err)
	}
	for _, status := range []string{StatusQueued, StatusCreating, StatusRunning, StatusStopping, StatusCompleted, StatusFailed} {
		_, st, err := store.CreateRequest(RunnerRequest{ID: "lifecycle-" + status, RepositoryFullName: "owner/repo"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		st.Status = status
		if err := store.WriteState(st); err != nil {
			t.Fatal(err)
		}
	}
	// Reopening cannot reset durable capacity or count queued/terminal rows.
	reopened := New(dir)
	for _, count := range []func() (int, error){
		func() (int, error) { return reopened.InFlightCountForRepositoryOwner("owner") },
		func() (int, error) { return reopened.InFlightCountForRepository("OWNER/REPO") },
	} {
		got, err := count()
		if err != nil || got != 3 {
			t.Fatalf("reopened in-flight count=%d, err=%v, want 3", got, err)
		}
	}
	for _, owner := range []string{"own", "owner%", "owner_", "", "acme%"} {
		got, err := store.InFlightCountForRepositoryOwner(owner)
		if err != nil || got != 0 {
			t.Fatalf("owner %q unexpectedly matched %d requests: %v", owner, got, err)
		}
	}
}

// ---------- ReleaseLease ----------

func TestReleaseLeaseClears(t *testing.T) {
	store := New(t.TempDir())

	if _, _, err := store.CreateRequest(RunnerRequest{
		ID: "lease-test", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-lease-test",
	}, nil); err != nil {
		t.Fatal(err)
	}

	_, claim, claimed, err := store.ClaimNextRunnable("worker-A", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected claim to succeed")
	}

	if err := store.ReleaseQueuedClaim(claim); err != nil {
		t.Fatal(err)
	}

	got, err := store.ReadState("lease-test")
	if err != nil {
		t.Fatal(err)
	}
	if got.LeaseOwner != "" {
		t.Errorf("expected empty lease owner after release, got %q", got.LeaseOwner)
	}
	if !got.LeaseExpiresAt.IsZero() {
		t.Errorf("expected zero lease expiry after release, got %v", got.LeaseExpiresAt)
	}
}

func TestReleaseLeaseDoesNotClearOtherWorkerLease(t *testing.T) {
	store := New(t.TempDir())

	if _, _, err := store.CreateRequest(RunnerRequest{
		ID: "lease-other", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-lease-other",
	}, nil); err != nil {
		t.Fatal(err)
	}

	_, claim, claimed, err := store.ClaimNextRunnable("worker-A", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected initial claim to succeed")
	}

	// worker-B tries to release worker-A's lease — should be a no-op
	claim.LeaseOwner = "worker-B"
	if err := store.ReleaseQueuedClaim(claim); err != nil {
		t.Fatal(err)
	}

	got, err := store.ReadState("lease-other")
	if err != nil {
		t.Fatal(err)
	}
	// Lease should still be held by worker-A
	if got.LeaseOwner != "worker-A" {
		t.Errorf("expected lease still owned by worker-A after wrong-worker release, got %q", got.LeaseOwner)
	}
}

func TestMySQLQueueTimeWithoutDSNLocation(t *testing.T) {
	db := &gorm.DB{Config: &gorm.Config{Dialector: gormmysql.New(gormmysql.Config{})}}
	expr := queueTime(db, time.Minute)
	if len(expr.Vars) != 1 || expr.Vars[0] != time.Minute.Microseconds() || !strings.Contains(expr.SQL, "UTC_TIMESTAMP(3)") {
		t.Fatalf("expected a one-minute UTC database deadline, got %+v", expr)
	}
}

func TestQueuedRetryIntentOnlyAppliesToScheduledVersion(t *testing.T) {
	store := New(t.TempDir())
	_, st, err := store.CreateRequest(RunnerRequest{ID: "retry-intent"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.ScheduleQueuedRetry(time.Hour)
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	saved, err := store.ReadState(st.ID)
	if err != nil || saved.NextRetryAt.IsZero() {
		t.Fatalf("initial retry missing: %v", err)
	}
	// A caller reusing its original struct must be able to explicitly clear
	// the deadline on the next version without rearming the previous intent.
	st.Version = saved.Version
	st.NextRetryAt = time.Time{}
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	saved, err = store.ReadState(st.ID)
	if err != nil || !saved.NextRetryAt.IsZero() {
		t.Fatalf("old retry rearmed: %v, %v", saved.NextRetryAt, err)
	}
}

func TestQueuedClaimOwnership(t *testing.T) {
	testQueuedClaimOwnership(t, New(t.TempDir()).(*DBStore))
}

func testQueuedClaimOwnership(t *testing.T, store *DBStore) {
	t.Helper()
	const id = "claim-handoff"
	if _, _, err := store.CreateRequest(RunnerRequest{ID: id}, nil); err != nil {
		t.Fatal(err)
	}
	claim := func(stage string) RunnerState {
		t.Helper()
		// The same worker name can be reused by a later process.
		_, st, ok, err := store.ClaimNextRunnable("worker", time.Minute)
		if err != nil || !ok || st.ID != id {
			t.Fatalf("%s claim: id=%q ok=%v err=%v", stage, st.ID, ok, err)
		}
		return st
	}
	old := claim("initial")
	if delta := time.Until(old.LeaseExpiresAt); delta <= 0 || delta > 2*time.Minute {
		t.Fatalf("one-minute lease has unexpected deadline %v", old.LeaseExpiresAt)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Delete(&runnerRequestRecord{}, "id = ?", id).Error; err != nil {
			t.Error(err)
		}
	}()
	if err := db.Model(&runnerRequestRecord{}).Where("id = ?", id).Update("lease_expires_at", time.Unix(1, 0).UTC()).Error; err != nil {
		t.Fatal(err)
	}
	rejectOldWrites := func() {
		t.Helper()
		for _, status := range []string{StatusCreating, StatusQueued, StatusFailed} {
			next := old
			next.Status = status
			if err := store.WriteQueuedClaim(next, old.LeaseOwner); !errors.Is(err, ErrConflict) {
				t.Fatalf("expired or superseded claim wrote %s: %v", status, err)
			}
		}
	}
	rejectOldWrites()
	current := claim("takeover")
	rejectOldWrites()
	if err := store.ReleaseQueuedClaim(old); err != nil {
		t.Fatal(err)
	}
	owner := current.LeaseOwner
	current.LeaseOwner, current.LeaseExpiresAt = "", time.Time{}
	current.ScheduleQueuedRetry(time.Minute)
	if err := store.WriteQueuedClaim(current, owner); err != nil {
		t.Fatalf("current claim could not defer after stale release: %v", err)
	}
	delayed, err := store.ReadState(id)
	if err != nil {
		t.Fatal(err)
	}
	// LastAttemptAt was assigned by the database when it granted the claim.
	if delta := delayed.NextRetryAt.Sub(current.LastAttemptAt); delta < time.Minute || delta > 2*time.Minute {
		t.Fatalf("queued retry is out of step with the database claim clock: %v", delta)
	}
	if _, _, ok, err := store.ClaimNextRunnable("early", time.Minute); err != nil || ok {
		t.Fatalf("deferred request was immediately eligible: ok=%v err=%v", ok, err)
	}
	delayed.ScheduleQueuedRetry(0)
	if err := store.WriteState(delayed); err != nil {
		t.Fatal(err)
	}
	current = claim("zero-delay")
	current.Status = StatusCreating
	if err := store.WriteQueuedClaim(current, current.LeaseOwner); err != nil {
		t.Fatalf("current claim could not start after deferral: %v", err)
	}
}

// ---------- DeleteProfile ----------

func TestDeleteProfileRemovesProfile(t *testing.T) {
	store := New(t.TempDir())

	if _, err := store.UpsertProfile(RunnerProfile{
		Name:           "to-delete",
		Labels:         []string{"self-hosted"},
		TemplateID:     "base",
		MaxConcurrency: 5,
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteProfile("to-delete"); err != nil {
		t.Fatal(err)
	}

	if _, err := store.GetProfile("to-delete"); err == nil {
		t.Error("expected error reading deleted profile, got nil")
	}
}

func TestDeleteProfileIsIdempotent(t *testing.T) {
	store := New(t.TempDir())

	if _, err := store.UpsertProfile(RunnerProfile{
		Name:           "del-idem",
		Labels:         []string{"sl"},
		TemplateID:     "base",
		MaxConcurrency: 1,
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}

	if err := store.DeleteProfile("del-idem"); err != nil {
		t.Fatal(err)
	}
	// deleting again should not error
	if err := store.DeleteProfile("del-idem"); err != nil {
		t.Errorf("second DeleteProfile should not error, got: %v", err)
	}
}

// ---------- ListProfiles / GetProfile ----------

func TestListProfilesReturnsAllProfiles(t *testing.T) {
	store := New(t.TempDir())

	for _, name := range []string{"list-p1", "list-p2", "list-p3"} {
		if _, err := store.UpsertProfile(RunnerProfile{
			Name:           name,
			Labels:         []string{"sl"},
			TemplateID:     "base",
			MaxConcurrency: 1,
			Enabled:        true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	profiles, err := store.ListProfiles()
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, p := range profiles {
		found[p.Name] = true
	}
	for _, name := range []string{"list-p1", "list-p2", "list-p3"} {
		if !found[name] {
			t.Errorf("ListProfiles: expected profile %q not found in results", name)
		}
	}
}

func TestGetProfileReturnsCorrectProfile(t *testing.T) {
	store := New(t.TempDir())

	if _, err := store.UpsertProfile(RunnerProfile{
		Name:           "get-prof",
		Labels:         []string{"self-hosted", "e2b"},
		TemplateID:     "template-xyz",
		MaxConcurrency: 7,
		Priority:       5,
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.GetProfile("get-prof")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "get-prof" || got.TemplateID != "template-xyz" || got.MaxConcurrency != 7 || got.Priority != 5 {
		t.Errorf("GetProfile returned unexpected profile: %#v", got)
	}
}

func TestRunnerProfileManagedCatalogFieldsRoundTrip(t *testing.T) {
	store := New(t.TempDir())
	profile := RunnerProfile{
		Name:                "managed-ubuntu-24.04",
		Labels:              []string{"self-hosted", "linux", "x64", "qiniu", "ubuntu-24.04"},
		RequiredLabels:      []string{"qiniu", "ubuntu-24.04"},
		TemplateID:          "template-24.04",
		DefaultTemplateName: "ubuntu-24.04",
		MaxConcurrency:      5,
		Enabled:             true,
		ManagedBy:           "runnerd",
		CatalogRevision:     7,
	}

	saved, err := store.UpsertProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(saved.RequiredLabels, profile.RequiredLabels) ||
		saved.DefaultTemplateName != profile.DefaultTemplateName ||
		saved.ManagedBy != profile.ManagedBy ||
		saved.CatalogRevision != profile.CatalogRevision {
		t.Fatalf("created managed catalog fields = %#v, want %#v", saved, profile)
	}

	saved.RequiredLabels = []string{"qiniu"}
	saved.DefaultTemplateName = "ubuntu-24.04-r8"
	saved.ManagedBy = "catalog-sync"
	saved.CatalogRevision = 8
	updated, err := store.UpsertProfile(saved)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(updated.RequiredLabels, saved.RequiredLabels) ||
		updated.DefaultTemplateName != saved.DefaultTemplateName ||
		updated.ManagedBy != saved.ManagedBy ||
		updated.CatalogRevision != saved.CatalogRevision {
		t.Fatalf("updated managed catalog fields = %#v, want %#v", updated, saved)
	}
}

func TestRecordToProfileTreatsMissingRequiredLabelsAsEmpty(t *testing.T) {
	empty := ""
	jsonNull := "null"
	tests := []struct {
		name               string
		requiredLabelsJSON *string
	}{
		{name: "nil", requiredLabelsJSON: nil},
		{name: "empty string", requiredLabelsJSON: &empty},
		{name: "json null", requiredLabelsJSON: &jsonNull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			profile, err := recordToProfile(runnerProfileRecord{
				Name:               "legacy",
				LabelsJSON:         `["self-hosted"]`,
				RequiredLabelsJSON: tt.requiredLabelsJSON,
			})
			if err != nil {
				t.Fatal(err)
			}
			if profile.RequiredLabels == nil || len(profile.RequiredLabels) != 0 {
				t.Fatalf("RequiredLabels = %#v, want non-nil empty slice", profile.RequiredLabels)
			}
		})
	}
}

func TestUpsertProfilePersistsNilRequiredLabelsAsEmptyJSONArray(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	if _, err := store.UpsertProfile(RunnerProfile{
		Name:           "empty-required-labels",
		Labels:         []string{"self-hosted"},
		TemplateID:     "base",
		MaxConcurrency: 1,
		Enabled:        true,
	}); err != nil {
		t.Fatal(err)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	var requiredLabelsJSON *string
	if err := db.Raw(`SELECT required_labels_json FROM runner_profiles WHERE name = ?`, "empty-required-labels").
		Scan(&requiredLabelsJSON).Error; err != nil {
		t.Fatal(err)
	}
	if requiredLabelsJSON == nil || *requiredLabelsJSON != "[]" {
		t.Fatalf("required_labels_json = %#v, want non-NULL []", requiredLabelsJSON)
	}
}

func TestManagedRunnerProfileRequiredLabelsMatch(t *testing.T) {
	store := New(t.TempDir())
	profile := RunnerProfile{
		Name:           "managed-ubuntu-24.04",
		Labels:         []string{"self-hosted", "linux", "x64", "qiniu", "ubuntu-24.04"},
		RequiredLabels: []string{"qiniu", "ubuntu-24.04"},
		TemplateID:     "template-24.04",
		MaxConcurrency: 5,
		Enabled:        true,
	}
	if _, err := store.UpsertProfile(profile); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		labels []string
		want   bool
	}{
		{name: "required labels only", labels: []string{"qiniu", "ubuntu-24.04"}, want: true},
		{name: "full advertised labels", labels: []string{"self-hosted", "linux", "x64", "qiniu", "ubuntu-24.04"}, want: true},
		{name: "normalized case and whitespace", labels: []string{" QINIU ", "Ubuntu-24.04"}, want: true},
		{name: "missing qiniu", labels: []string{"ubuntu-24.04"}, want: false},
		{name: "missing ubuntu version", labels: []string{"qiniu"}, want: false},
		{name: "unsupported ubuntu version", labels: []string{"qiniu", "ubuntu-22.04"}, want: false},
		{name: "unsupported gpu label", labels: []string{"qiniu", "ubuntu-24.04", "gpu"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			match, err := store.MatchProfile("owner/repo", tt.labels)
			if err != nil {
				t.Fatal(err)
			}
			if got := match.Profile != nil; got != tt.want {
				t.Fatalf("MatchProfile(%v) matched = %v, want %v; result = %#v", tt.labels, got, tt.want, match)
			}
		})
	}
}

func TestUpsertProfileRejectsRequiredLabelsOutsideAdvertisedLabels(t *testing.T) {
	store := New(t.TempDir())
	profile := RunnerProfile{
		Name:           "invalid-managed-profile",
		Labels:         []string{"qiniu", "ubuntu-24.04"},
		RequiredLabels: []string{"qiniu", "gpu"},
		TemplateID:     "template-24.04",
		MaxConcurrency: 5,
		Enabled:        true,
	}

	if _, err := store.UpsertProfile(profile); err == nil {
		t.Fatal("expected required labels outside advertised labels to be rejected")
	}
}

func TestUpsertProfileRejectsPathUnsafeName(t *testing.T) {
	for _, name := range []string{"owner/spec", ".", ".."} {
		t.Run(name, func(t *testing.T) {
			store := New(t.TempDir())
			_, err := store.UpsertProfile(RunnerProfile{
				Name:           name,
				Labels:         []string{"self-hosted", "path-name"},
				RequiredLabels: []string{"path-name"},
				TemplateID:     "path-name-template",
				Enabled:        true,
			})
			if err == nil || !strings.Contains(err.Error(), "profile name must not contain '/' or be '.' or '..'") {
				t.Fatalf("UpsertProfile(%q) error = %v, want path-safe name rejection", name, err)
			}
			if _, err := store.GetProfile(name); err != ErrNotFound {
				t.Fatalf("GetProfile(%q) after rejected upsert error = %v, want ErrNotFound", name, err)
			}
		})
	}
}

func TestUpsertProfileAllowsOtherSingleSegmentNames(t *testing.T) {
	for _, name := range []string{"custom spec", `custom\spec`, "自定义规格"} {
		t.Run(name, func(t *testing.T) {
			store := New(t.TempDir())
			saved, err := store.UpsertProfile(RunnerProfile{
				Name:           name,
				Labels:         []string{"self-hosted", "single-segment"},
				RequiredLabels: []string{"single-segment"},
				TemplateID:     "single-segment-template",
				Enabled:        true,
			})
			if err != nil {
				t.Fatalf("UpsertProfile(%q): %v", name, err)
			}
			if saved.Name != name {
				t.Fatalf("saved name = %q, want %q", saved.Name, name)
			}
		})
	}
}

// ---------- sanitizeID / sanitizeRunnerName ----------

func TestSanitizeIDReplacesPathSeparators(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"simple-id", "simple-id"},
		{"with/slash", "with-slash"},
		{`with\backslash`, "with-backslash"},
		{"with/../traversal", "with-----traversal"},
		{"  spaces  ", "spaces"},
	}
	for _, tt := range tests {
		got := sanitizeID(tt.input)
		// Just verify the dangerous characters are gone
		if strings.Contains(got, "/") || strings.Contains(got, `\`) {
			t.Errorf("sanitizeID(%q) = %q still contains path separators", tt.input, got)
		}
	}
}

func TestSanitizeRunnerNameReplacesPathSeparators(t *testing.T) {
	tests := []struct {
		input    string
		wantSafe bool
	}{
		{"e2b-runner-1", true},
		{"runner/with/slash", true},
		{`runner\with\backslash`, true},
		{"  trimmed  ", true},
	}
	for _, tt := range tests {
		got := sanitizeRunnerName(tt.input)
		if strings.Contains(got, "/") || strings.Contains(got, `\`) {
			t.Errorf("sanitizeRunnerName(%q) = %q still contains path separators", tt.input, got)
		}
	}
}

// ---------- ReadRequest ----------

func TestReadRequestReturnsRequestAfterCreate(t *testing.T) {
	store := New(t.TempDir())
	req := RunnerRequest{
		ID:                 "rr-read-1",
		Source:             "manual",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-rr-read-1",
		RepositoryFullName: "org/repo",
		ProfileName:        "default",
	}
	if _, _, err := store.CreateRequest(req, nil); err != nil {
		t.Fatal(err)
	}
	got, err := store.ReadRequest("rr-read-1")
	if err != nil {
		t.Fatalf("ReadRequest: %v", err)
	}
	if got.ID != req.ID {
		t.Errorf("ReadRequest ID: got %q, want %q", got.ID, req.ID)
	}
	if got.RepositoryFullName != req.RepositoryFullName {
		t.Errorf("ReadRequest RepositoryFullName: got %q, want %q", got.RepositoryFullName, req.RepositoryFullName)
	}
}

func TestReadRequestReturnsErrorForMissingID(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.ReadRequest("nonexistent-id"); err == nil {
		t.Error("ReadRequest: expected error for missing id, got nil")
	}
}

func TestClaimNextRunnableOrdersByEligibilityTime(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	now := time.Now().UTC()
	for _, tc := range []struct {
		id            string
		queued, retry time.Time
	}{
		{"old-deferred", now.Add(-time.Hour), now.Add(-time.Second)},
		{"fresh", now.Add(-time.Minute), time.Time{}},
		{"due-retry", now.Add(-2 * time.Hour), now.Add(-30 * time.Second)},
		{"future", now.Add(-3 * time.Hour), now.Add(time.Minute)},
	} {
		_, st, err := store.CreateRequest(RunnerRequest{ID: tc.id}, nil)
		if err != nil {
			t.Fatal(err)
		}
		st.NextRetryAt = tc.retry
		if err := store.WriteState(st); err != nil {
			t.Fatal(err)
		}
		db, err := store.dbOrEnsure()
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&runnerRequestRecord{}).Where("id = ?", tc.id).Update("queued_at", tc.queued).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, want := range []string{"fresh", "due-retry", "old-deferred"} {
		req, _, claimed, err := store.ClaimNextRunnable("worker", time.Minute)
		if err != nil || !claimed || req.ID != want {
			t.Fatalf("claim=%s claimed=%t err=%v, want %s", req.ID, claimed, err, want)
		}
	}
	if _, _, claimed, err := store.ClaimNextRunnable("worker", time.Minute); err != nil || claimed {
		t.Fatalf("claimed future or leased request: claimed=%t err=%v", claimed, err)
	}
}
