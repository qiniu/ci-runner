package state

import (
	"reflect"
	"strings"
	"testing"
	"time"
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

// ---------- ReleaseLease ----------

func TestReleaseLeaseClears(t *testing.T) {
	store := New(t.TempDir())
	now := time.Now().UTC()

	if _, _, err := store.CreateRequest(RunnerRequest{
		ID: "lease-test", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-lease-test",
	}, nil); err != nil {
		t.Fatal(err)
	}

	_, _, claimed, err := store.ClaimNextRunnable("worker-A", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected claim to succeed")
	}

	if err := store.ReleaseLease("lease-test", "worker-A"); err != nil {
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
	now := time.Now().UTC()

	if _, _, err := store.CreateRequest(RunnerRequest{
		ID: "lease-other", Source: "test", Labels: []string{"sl"}, RunnerName: "e2b-lease-other",
	}, nil); err != nil {
		t.Fatal(err)
	}

	_, _, claimed, err := store.ClaimNextRunnable("worker-A", now, 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Fatal("expected initial claim to succeed")
	}

	// worker-B tries to release worker-A's lease — should be a no-op
	if err := store.ReleaseLease("lease-other", "worker-B"); err != nil {
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
