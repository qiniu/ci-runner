package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestResolveDefaultTemplateID(t *testing.T) {
	tests := []struct {
		name          string
		requestedName string
		templates     []sandboxrunner.CatalogTemplate
		wantID        string
		wantReason    string
	}{
		{
			name:          "exact ready name",
			requestedName: " github-runner-ubuntu-24-04 ",
			templates: []sandboxrunner.CatalogTemplate{{
				TemplateID:  "tpl-ready",
				Names:       []string{" github-runner-ubuntu-24-04 "},
				BuildStatus: "ready",
				Public:      true,
			}},
			wantID: "tpl-ready",
		},
		{
			name:          "qualified uploaded name",
			requestedName: "github-runner-ubuntu-24-04",
			templates: []sandboxrunner.CatalogTemplate{{
				TemplateID:  "tpl-uploaded",
				Names:       []string{"tenant/github-runner-ubuntu-24-04"},
				BuildStatus: "uploaded",
				Public:      true,
			}},
			wantID: "tpl-uploaded",
		},
		{
			name:          "missing does not match alias or substring",
			requestedName: "github-runner-ubuntu-24-04",
			templates: []sandboxrunner.CatalogTemplate{{
				TemplateID:  "tpl-alias",
				Aliases:     []string{"github-runner-ubuntu-24-04"},
				Names:       []string{"prefix-github-runner-ubuntu-24-04-suffix"},
				BuildStatus: "ready",
				Public:      true,
			}},
			wantReason: defaultTemplateResolutionReasonMissing,
		},
		{
			name:          "blank requested name",
			requestedName: "  ",
			templates: []sandboxrunner.CatalogTemplate{{
				TemplateID:  "tpl-namespace",
				Names:       []string{"tenant/"},
				BuildStatus: "ready",
				Public:      true,
			}},
			wantReason: defaultTemplateResolutionReasonMissing,
		},
		{
			name:          "duplicate matching templates",
			requestedName: "github-runner-ubuntu-24-04",
			templates: []sandboxrunner.CatalogTemplate{
				{
					TemplateID:  "tpl-one",
					Names:       []string{"github-runner-ubuntu-24-04"},
					BuildStatus: "ready",
					Public:      true,
				},
				{
					TemplateID:  "tpl-two",
					Names:       []string{"tenant/github-runner-ubuntu-24-04"},
					BuildStatus: "ready",
					Public:      true,
				},
			},
			wantReason: defaultTemplateResolutionReasonDuplicate,
		},
		{
			name:          "valid and private matches are duplicate",
			requestedName: "github-runner-ubuntu-24-04",
			templates: []sandboxrunner.CatalogTemplate{
				{
					TemplateID:  "tpl-valid",
					Names:       []string{"github-runner-ubuntu-24-04"},
					BuildStatus: "ready",
					Public:      true,
				},
				{
					TemplateID:  "tpl-private",
					Names:       []string{"tenant/github-runner-ubuntu-24-04"},
					BuildStatus: "ready",
					Public:      false,
				},
			},
			wantReason: defaultTemplateResolutionReasonDuplicate,
		},
		{
			name:          "valid and building matches are duplicate",
			requestedName: "github-runner-ubuntu-24-04",
			templates: []sandboxrunner.CatalogTemplate{
				{
					TemplateID:  "tpl-valid",
					Names:       []string{"github-runner-ubuntu-24-04"},
					BuildStatus: "uploaded",
					Public:      true,
				},
				{
					TemplateID:  "tpl-building",
					Names:       []string{"tenant/github-runner-ubuntu-24-04"},
					BuildStatus: "building",
					Public:      true,
				},
			},
			wantReason: defaultTemplateResolutionReasonDuplicate,
		},
		{
			name:          "valid and empty id matches are duplicate",
			requestedName: "github-runner-ubuntu-24-04",
			templates: []sandboxrunner.CatalogTemplate{
				{
					TemplateID:  "tpl-valid",
					Names:       []string{"github-runner-ubuntu-24-04"},
					BuildStatus: "ready",
					Public:      true,
				},
				{
					TemplateID:  "",
					Names:       []string{"tenant/github-runner-ubuntu-24-04"},
					BuildStatus: "ready",
					Public:      true,
				},
			},
			wantReason: defaultTemplateResolutionReasonDuplicate,
		},
		{
			name:          "private template",
			requestedName: "github-runner-ubuntu-24-04",
			templates: []sandboxrunner.CatalogTemplate{{
				TemplateID:  "tpl-private",
				Names:       []string{"github-runner-ubuntu-24-04"},
				BuildStatus: "ready",
				Public:      false,
			}},
			wantReason: defaultTemplateResolutionReasonPrivate,
		},
		{
			name:          "non runnable template",
			requestedName: "github-runner-ubuntu-24-04",
			templates: []sandboxrunner.CatalogTemplate{{
				TemplateID:  "tpl-building",
				Names:       []string{"github-runner-ubuntu-24-04"},
				BuildStatus: "building",
				Public:      true,
			}},
			wantReason: defaultTemplateResolutionReasonNonRunnable,
		},
		{
			name:          "empty template id",
			requestedName: "github-runner-ubuntu-24-04",
			templates: []sandboxrunner.CatalogTemplate{{
				TemplateID:  "  ",
				Names:       []string{"github-runner-ubuntu-24-04"},
				BuildStatus: "ready",
				Public:      true,
			}},
			wantReason: defaultTemplateResolutionReasonEmptyID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveDefaultTemplateID(tt.requestedName, tt.templates)
			if tt.wantReason == "" {
				if err != nil {
					t.Fatalf("resolveDefaultTemplateID: %v", err)
				}
				if got != tt.wantID {
					t.Fatalf("template id = %q, want %q", got, tt.wantID)
				}
				return
			}

			if err == nil {
				t.Fatalf("resolveDefaultTemplateID returned id %q, want error reason %q", got, tt.wantReason)
			}
			var resolutionErr *defaultTemplateResolutionError
			if !errors.As(err, &resolutionErr) {
				t.Fatalf("error type = %T, want *defaultTemplateResolutionError", err)
			}
			if resolutionErr.RequestedName != strings.TrimSpace(tt.requestedName) {
				t.Fatalf("requested name = %q, want %q", resolutionErr.RequestedName, strings.TrimSpace(tt.requestedName))
			}
			if resolutionErr.Reason != tt.wantReason {
				t.Fatalf("reason = %q, want %q", resolutionErr.Reason, tt.wantReason)
			}
		})
	}
}

func TestClassifyRetryableErrorPrefersWrappedDefaultTemplateResolutionReason(t *testing.T) {
	err := fmt.Errorf("resolve managed runner template: %w", &defaultTemplateResolutionError{
		RequestedName: "runner status 429 timeout",
		Reason:        defaultTemplateResolutionReasonMissing,
	})

	code, retryable := classifyRetryableError("template_resolution", err)

	if code != defaultTemplateResolutionReasonMissing || retryable {
		t.Fatalf("classification = (%q, %v), want (%q, false)", code, retryable, defaultTemplateResolutionReasonMissing)
	}
}

func TestRunnerLifecycleCustomTemplateUsesStoredIDWithoutCatalog(t *testing.T) {
	events := &lifecycleEventRecorder{}
	ghServer := newLifecycleGitHubServer(t, events)
	defer ghServer.Close()

	store := state.New(t.TempDir())
	upsertLifecycleProfile(t, store, state.RunnerProfile{
		Name:           "custom",
		Labels:         []string{"self-hosted", "custom"},
		TemplateID:     "custom-template-id",
		MaxConcurrency: 10,
		Enabled:        true,
	})
	sandbox := &lifecycleSandboxService{events: events}
	srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, sandbox)
	createLifecycleRequest(t, store, "custom-request", "custom", 0)

	go srv.startRunner(context.Background(), "custom-request", "worker-test")
	waitForState(t, store, "custom-request", state.StatusRunning)

	inputs := sandbox.startInputs()
	if len(inputs) != 1 || inputs[0].TemplateID != "custom-template-id" {
		t.Fatalf("StartRunner inputs = %#v, want stored custom template id", inputs)
	}
	if inputs[0].RequireDocker {
		t.Fatalf("custom StartRunner input requires Docker: %#v", inputs[0])
	}
}

func TestRunnerLifecycleRevalidatesGlobalProfileBeforeStarting(t *testing.T) {
	store := state.New(t.TempDir())
	profile := lifecycleManagedProfile("managed-template-id")
	upsertLifecycleProfile(t, store, profile)
	scope := state.RunnerProfileScope{Type: state.RunnerProfileScopeAccount, ID: 1}
	created, _, err := store.CreateRequest(state.RunnerRequest{ID: "disabled-global-request", Source: "test", RepositoryFullName: "o/r", RequestedLabels: append([]string(nil), profile.Labels...), Labels: append([]string(nil), profile.Labels...), ProfileName: profile.Name, ProfileSource: "global", ProfileScopeType: scope.Type, ProfileScopeID: scope.ID, RunnerName: "e2b-disabled-global-request"}, nil)
	if err != nil || !created {
		t.Fatalf("CreateRequest created=%v err=%v", created, err)
	}
	profile.Enabled = false
	upsertLifecycleProfile(t, store, profile)
	sandbox := &lifecycleSandboxService{}
	srv := newRunnerLifecycleTestServer(t, store, "http://127.0.0.1:1", sandbox)

	srv.startRunner(context.Background(), "disabled-global-request", "worker-test")

	got, err := store.ReadState("disabled-global-request")
	if err != nil {
		t.Fatal(err)
	}
	if got.FailureStage != "profile_validation" || got.Status == state.StatusRunning {
		t.Fatalf("state = %#v, want profile_validation failure", got)
	}
	if inputs := sandbox.startInputs(); len(inputs) != 0 {
		t.Fatalf("disabled global profile started sandbox with inputs %#v", inputs)
	}
}

func TestRunnerLifecycleRejectsNewRequiredLabelsBeforeStarting(t *testing.T) {
	store := state.New(t.TempDir())
	profile := state.RunnerProfile{
		Name:           "platform-custom",
		Labels:         []string{"self-hosted", "base", "new-required"},
		RequiredLabels: []string{"base"},
		TemplateID:     "custom-template-id",
		MaxConcurrency: 10,
		Enabled:        true,
	}
	upsertLifecycleProfile(t, store, profile)
	created, _, err := store.CreateRequest(state.RunnerRequest{ID: "changed-required-labels", Source: "test", RepositoryFullName: "o/r", RequestedLabels: []string{"self-hosted", "base"}, Labels: append([]string(nil), profile.Labels...), ProfileName: profile.Name, ProfileSource: "global", RunnerName: "e2b-changed-required-labels"}, nil)
	if err != nil || !created {
		t.Fatalf("CreateRequest created=%v err=%v", created, err)
	}
	profile.RequiredLabels = []string{"base", "new-required"}
	upsertLifecycleProfile(t, store, profile)
	sandbox := &lifecycleSandboxService{}
	srv := newRunnerLifecycleTestServer(t, store, "http://127.0.0.1:1", sandbox)

	srv.startRunner(context.Background(), "changed-required-labels", "worker-test")

	got, err := store.ReadState("changed-required-labels")
	if err != nil {
		t.Fatal(err)
	}
	if got.FailureStage != "profile_validation" || got.Status == state.StatusRunning {
		t.Fatalf("state = %#v, want profile_validation failure", got)
	}
	if inputs := sandbox.startInputs(); len(inputs) != 0 {
		t.Fatalf("changed global profile started sandbox with inputs %#v", inputs)
	}
}

func TestRunnerLifecycleSerializesProfileReloadWithMutations(t *testing.T) {
	baseStore := state.New(t.TempDir())
	profile := state.RunnerProfile{Name: "platform-custom", Labels: []string{"self-hosted", "custom"}, RequiredLabels: []string{"custom"}, TemplateID: "custom-template-id", MaxConcurrency: 10, Enabled: true}
	upsertLifecycleProfile(t, baseStore, profile)
	created, _, err := baseStore.CreateRequest(state.RunnerRequest{ID: "serialized-profile-reload", Source: "test", RepositoryFullName: "o/r", RequestedLabels: append([]string(nil), profile.Labels...), Labels: append([]string(nil), profile.Labels...), ProfileName: profile.Name, ProfileSource: "global", RunnerName: "e2b-serialized-profile-reload"}, nil)
	if err != nil || !created {
		t.Fatalf("CreateRequest created=%v err=%v", created, err)
	}
	store := &blockingProfileLookupStore{Store: baseStore, entered: make(chan struct{}), release: make(chan struct{})}
	srv := newRunnerLifecycleTestServer(t, store, "http://127.0.0.1:1", &lifecycleSandboxService{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.startRunner(context.Background(), "serialized-profile-reload", "worker-test")
	}()

	<-store.entered
	mutationCouldInterleave := srv.admissionMu.TryLock()
	if mutationCouldInterleave {
		srv.admissionMu.Unlock()
	}
	close(store.release)
	<-done
	if mutationCouldInterleave {
		t.Fatal("runner profile reload did not hold the mutation serialization lock")
	}
}

func TestRunnerLifecycleRevalidatesScopedProfileBeforeStarting(t *testing.T) {
	store := state.New(t.TempDir())
	scope := state.RunnerProfileScope{Type: state.RunnerProfileScopeAccount, ID: 1}
	profile, err := store.UpsertScopedProfileIfUnchanged(state.ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: "custom", WorkflowLabels: []string{"self-hosted", "custom"}, TemplateID: "custom-template-id", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	created, _, err := store.CreateRequest(state.RunnerRequest{ID: "disabled-scoped-request", Source: "test", RepositoryFullName: "o/r", RequestedLabels: []string{"self-hosted", "custom"}, Labels: []string{"self-hosted", "custom"}, ProfileName: profile.Name, ProfileSource: "scoped_custom", ProfileScopeType: scope.Type, ProfileScopeID: scope.ID, RunnerName: "e2b-disabled-scoped-request"}, nil)
	if err != nil || !created {
		t.Fatalf("CreateRequest created=%v err=%v", created, err)
	}
	profile.Enabled = false
	if _, err := store.UpsertScopedProfileIfUnchanged(profile, &profile.UpdatedAt); err != nil {
		t.Fatal(err)
	}
	sandbox := &lifecycleSandboxService{}
	srv := newRunnerLifecycleTestServer(t, store, "http://127.0.0.1:1", sandbox)

	srv.startRunner(context.Background(), "disabled-scoped-request", "worker-test")

	got, err := store.ReadState("disabled-scoped-request")
	if err != nil {
		t.Fatal(err)
	}
	if got.FailureStage != "profile_validation" || got.Status == state.StatusRunning {
		t.Fatalf("state = %#v, want profile_validation failure", got)
	}
	if inputs := sandbox.startInputs(); len(inputs) != 0 {
		t.Fatalf("disabled scoped profile started sandbox with inputs %#v", inputs)
	}
}

type blockingProfileLookupStore struct {
	state.Store
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (s *blockingProfileLookupStore) GetProfile(name string) (state.RunnerProfile, error) {
	profile, err := s.Store.GetProfile(name)
	s.once.Do(func() {
		close(s.entered)
		<-s.release
	})
	return profile, err
}

type effectiveProfileLookupRejectingStore struct {
	state.Store
}

func (s *effectiveProfileLookupRejectingStore) GetEffectiveProfile(state.RunnerProfileScope, string, string) (state.EffectiveRunnerProfile, error) {
	return state.EffectiveRunnerProfile{}, errors.New("full effective catalog lookup is unavailable")
}

func TestProfileForRunnerRequestUsesDirectScopedLookup(t *testing.T) {
	baseStore := state.New(t.TempDir())
	scope := state.RunnerProfileScope{Type: state.RunnerProfileScopeAccount, ID: 1}
	want, err := baseStore.UpsertScopedProfileIfUnchanged(state.ScopedRunnerProfile{
		ScopeType: scope.Type, ScopeID: scope.ID, Name: "custom",
		WorkflowLabels: []string{"self-hosted", "custom"}, TemplateID: "custom-template-id", Enabled: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: &effectiveProfileLookupRejectingStore{Store: baseStore}}

	got, err := srv.profileForRunnerRequest(state.RunnerRequest{
		ProfileName: want.Name, ProfileSource: "scoped_custom",
		ProfileScopeType: scope.Type, ProfileScopeID: scope.ID,
	})
	if err != nil {
		t.Fatalf("profileForRunnerRequest: %v", err)
	}
	if got.Name != want.Name || got.TemplateID != want.TemplateID || !got.Enabled {
		t.Fatalf("profile = %#v, want direct scoped profile %#v", got, want)
	}
}

func TestRunnerLifecycleRetryUsesPersistedSpecWithoutPolicyOrGroupReads(t *testing.T) {
	// Characterization test: a retry starts from its admitted Runner Spec. It
	// catches a migration that rematches a stored request through retired policy
	// or internal-group tables.
	baseStore := state.New(t.TempDir())
	upsertLifecycleProfile(t, baseStore, state.RunnerProfile{
		Name:           "legacy-admitted-custom",
		Labels:         []string{"self-hosted", "e2b", "legacy-admitted-custom"},
		TemplateID:     "persisted-custom-template",
		RunnerGroup:    "Default",
		MaxConcurrency: 1,
		Enabled:        true,
	})
	created, failed, err := baseStore.CreateRequest(state.RunnerRequest{
		ID:                 "retry-persisted-spec",
		Source:             "test",
		RepositoryFullName: "o/r",
		RequestedLabels:    []string{"self-hosted", "e2b"},
		Labels:             []string{"self-hosted", "e2b", "legacy-admitted-custom"},
		ProfileName:        "legacy-admitted-custom",
		RunnerGroup:        "Default",
		RunnerName:         "e2b-retry-persisted-spec",
	}, nil)
	if err != nil || !created {
		t.Fatalf("CreateRequest created=%v err=%v", created, err)
	}
	failed.Status = state.StatusFailed
	failed.LastErrorRetryable = true
	if err := baseStore.WriteState(failed); err != nil {
		t.Fatal(err)
	}
	if _, err := baseStore.RetryRequest("retry-persisted-spec", time.Date(2026, time.August, 13, 9, 11, 12, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}

	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/orgs/o/actions/runners/registration-token" {
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"runner-token","expires_at":"2026-08-13T10:00:00Z"}`))
	}))
	defer ghServer.Close()

	store := &catalogReadRejectingStore{Store: baseStore}
	sandbox := &lifecycleSandboxService{}
	srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, sandbox)
	go srv.startRunner(context.Background(), "retry-persisted-spec", "worker-test")
	waitForState(t, baseStore, "retry-persisted-spec", state.StatusRunning)

	got, err := baseStore.ReadState("retry-persisted-spec")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusRunning || got.ProfileName != "legacy-admitted-custom" || got.RunnerGroup != "Default" || !equalStrings(got.RequestedLabels, []string{"self-hosted", "e2b"}) {
		t.Fatalf("retry state = %#v, want running request with persisted admission fields", got)
	}
	inputs := sandbox.startInputs()
	if len(inputs) != 1 || inputs[0].TemplateID != "persisted-custom-template" || inputs[0].RunnerGroup != "Default" || !equalStrings(inputs[0].Labels, []string{"self-hosted", "e2b", "legacy-admitted-custom"}) {
		t.Fatalf("retry StartRunner inputs = %#v, want persisted custom profile", inputs)
	}
	if store.matchReads != 0 {
		t.Fatalf("retry rematched the persisted runner spec: match reads=%d", store.matchReads)
	}
}

func TestRunnerLifecycleManagedDefaultResolvesBeforeRegistration(t *testing.T) {
	events := &lifecycleEventRecorder{}
	ghServer := newLifecycleGitHubServer(t, events)
	defer ghServer.Close()

	baseStore := state.New(t.TempDir())
	upsertLifecycleProfile(t, baseStore, lifecycleManagedProfile("catalog-bootstrap-id"))
	store := &profileLoadRecordingStore{Store: baseStore, events: events}
	sandbox := &managedLifecycleSandboxService{
		lifecycleSandboxService: &lifecycleSandboxService{events: events},
		templates: []sandboxrunner.CatalogTemplate{{
			TemplateID:  "scoped-template-id",
			Names:       []string{"region/github-runner-ubuntu-24-04"},
			BuildStatus: "uploaded",
			Public:      true,
		}},
	}
	srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, sandbox)
	createLifecycleRequest(t, store, "managed-request", "managed", 987)

	go srv.startRunner(context.Background(), "managed-request", "worker-test")
	waitForState(t, store, "managed-request", state.StatusRunning)

	inputs := sandbox.startInputs()
	if len(inputs) != 1 || inputs[0].TemplateID != "scoped-template-id" {
		t.Fatalf("StartRunner inputs = %#v, want resolved scoped template id", inputs)
	}
	if !inputs[0].RequireDocker {
		t.Fatalf("managed StartRunner input does not require Docker: %#v", inputs[0])
	}
	if got := events.snapshot(); !equalStrings(got, []string{"profile", "catalog", "token", "start"}) {
		t.Fatalf("lifecycle order = %#v, want [profile catalog token start]", got)
	}
}

func TestRunnerLifecycleManagedResolutionFailuresAreNonRetryable(t *testing.T) {
	tests := []struct {
		name          string
		requestedName string
		templates     []sandboxrunner.CatalogTemplate
		reason        string
	}{
		{
			name:          "missing with retry-like requested name",
			requestedName: "runner status 429 timeout",
			reason:        defaultTemplateResolutionReasonMissing,
		},
		{
			name:          "non runnable",
			requestedName: "github-runner-ubuntu-24-04",
			templates: []sandboxrunner.CatalogTemplate{{
				TemplateID:  "template-building",
				Names:       []string{"github-runner-ubuntu-24-04"},
				BuildStatus: "building",
				Public:      true,
			}},
			reason: defaultTemplateResolutionReasonNonRunnable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tokenCalls int
			ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tokenCalls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"token":"runner-token","expires_at":"2026-05-18T10:00:00Z"}`))
			}))
			defer ghServer.Close()

			store := state.New(t.TempDir())
			profile := lifecycleManagedProfile("catalog-bootstrap-id")
			profile.DefaultTemplateName = tt.requestedName
			upsertLifecycleProfile(t, store, profile)
			sandbox := &managedLifecycleSandboxService{
				lifecycleSandboxService: &lifecycleSandboxService{},
				templates:               tt.templates,
			}
			srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, sandbox)
			createLifecycleRequest(t, store, "resolution-failure", "managed", 987)

			go srv.startRunner(context.Background(), "resolution-failure", "worker-test")
			waitForLifecycleOutcome(t, store, "resolution-failure")

			got, err := store.ReadState("resolution-failure")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != state.StatusFailed ||
				got.FailureStage != "template_resolution" ||
				got.FailureReason != tt.reason ||
				got.LastErrorCode != tt.reason ||
				got.LastErrorRetryable {
				t.Fatalf("state = %#v, want non-retryable template_resolution/%s failure", got, tt.reason)
			}
			var resolutionErr *defaultTemplateResolutionError
			_, directErr := resolveDefaultTemplateID(tt.requestedName, tt.templates)
			if !errors.As(directErr, &resolutionErr) || resolutionErr.Reason != tt.reason {
				t.Fatalf("resolver error = %#v, want reason %q", directErr, tt.reason)
			}
			if tokenCalls != 0 {
				t.Fatalf("registration token calls = %d, want 0", tokenCalls)
			}
			if len(sandbox.startInputs()) != 0 {
				t.Fatalf("StartRunner inputs = %#v, want none", sandbox.startInputs())
			}
		})
	}
}

func TestRunnerLifecycleManagedServiceWithoutDefaultTemplateCatalogFailsBeforeRegistration(t *testing.T) {
	var tokenCalls int
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"runner-token","expires_at":"2026-05-18T10:00:00Z"}`))
	}))
	defer ghServer.Close()

	store := state.New(t.TempDir())
	profile := lifecycleManagedProfile("catalog-bootstrap-id")
	profile.DefaultTemplateName = "runner status 429 timeout"
	upsertLifecycleProfile(t, store, profile)
	sandbox := &lifecycleSandboxService{}
	srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, sandbox)
	createLifecycleRequest(t, store, "catalog-capability-missing", "managed", 987)

	srv.startRunner(context.Background(), "catalog-capability-missing", "worker-test")

	got, err := store.ReadState("catalog-capability-missing")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusFailed ||
		got.FailureStage != "template_resolution" ||
		got.FailureReason != defaultTemplateResolutionReasonNoCatalog ||
		got.LastErrorCode != defaultTemplateResolutionReasonNoCatalog ||
		got.LastErrorRetryable {
		t.Fatalf("state = %#v, want non-retryable template_resolution/%s failure", got, defaultTemplateResolutionReasonNoCatalog)
	}
	if tokenCalls != 0 {
		t.Fatalf("registration token calls = %d, want 0", tokenCalls)
	}
	if len(sandbox.startInputs()) != 0 {
		t.Fatalf("StartRunner inputs = %#v, want none", sandbox.startInputs())
	}
}

func TestRunnerLifecycleDefaultCatalogErrorsKeepRetryClassification(t *testing.T) {
	tests := []struct {
		name     string
		listErr  error
		wantCode string
	}{
		{name: "rate limit", listErr: errors.New("status 429 too many requests"), wantCode: "http_rate_limited"},
		{name: "server error", listErr: errors.New("status 503 service unavailable"), wantCode: "backend_server_error"},
		{name: "network", listErr: errors.New("connection reset by peer"), wantCode: "temporary_network_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var tokenCalls int
			ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				tokenCalls++
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"token":"runner-token","expires_at":"2026-05-18T10:00:00Z"}`))
			}))
			defer ghServer.Close()

			store := state.New(t.TempDir())
			upsertLifecycleProfile(t, store, lifecycleManagedProfile("catalog-bootstrap-id"))
			sandbox := &managedLifecycleSandboxService{
				lifecycleSandboxService: &lifecycleSandboxService{},
				listErr:                 tt.listErr,
			}
			srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, sandbox)
			createLifecycleRequest(t, store, "catalog-error", "managed", 987)

			go srv.startRunner(context.Background(), "catalog-error", "worker-test")
			waitForLifecycleOutcome(t, store, "catalog-error")

			got, err := store.ReadState("catalog-error")
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != state.StatusQueued ||
				got.FailureStage != "template_resolution" ||
				got.LastErrorCode != tt.wantCode ||
				!got.LastErrorRetryable {
				t.Fatalf("state = %#v, want retryable %q template_resolution failure", got, tt.wantCode)
			}
			if tokenCalls != 0 || len(sandbox.startInputs()) != 0 {
				t.Fatalf("token calls = %d StartRunner inputs = %#v, want neither", tokenCalls, sandbox.startInputs())
			}
		})
	}
}

func TestRunnerLifecycleManagedResolutionUsesScopedSandboxServiceWithoutLeakingKey(t *testing.T) {
	const apiKey = "scoped-sandbox-secret-key"
	var catalogCalls int
	sandboxAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/default-templates" {
			http.Error(w, "unexpected sandbox request", http.StatusNotFound)
			return
		}
		catalogCalls++
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("sandbox authorization = %q, want scoped credential", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer sandboxAPI.Close()

	var tokenCalls int
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCalls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"runner-token","expires_at":"2026-05-18T10:00:00Z"}`))
	}))
	defer ghServer.Close()

	store := state.New(t.TempDir())
	upsertLifecycleProfile(t, store, lifecycleManagedProfile("catalog-bootstrap-id"))
	srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, nil)
	srv.sandboxHTTP = sandboxAPI.Client()
	valueJSON, err := json.Marshal(accountSandboxServicePreferenceValue{APIURL: sandboxAPI.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountPreference(state.AccountPreference{
		ScopeType: state.AccountScopeTypeGitHubInstall,
		ScopeID:   987,
		Namespace: accountPreferenceNamespaceSandbox,
		Key:       accountPreferenceKeySandboxService,
		ValueJSON: string(valueJSON),
	}); err != nil {
		t.Fatal(err)
	}
	encryptedKey, err := encryptSecret(apiKey, srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountSecret(state.AccountSecret{
		ScopeType:      state.AccountScopeTypeGitHubInstall,
		ScopeID:        987,
		KeyType:        state.AccountSecretTypeSandboxAPIKey,
		EncryptedValue: encryptedKey,
	}); err != nil {
		t.Fatal(err)
	}
	createLifecycleRequest(t, store, "scoped-resolution", "managed", 987)

	srv.startRunner(context.Background(), "scoped-resolution", "worker-test")

	got, err := store.ReadState("scoped-resolution")
	if err != nil {
		t.Fatal(err)
	}
	logData, err := store.ReadLog("scoped-resolution", "control.log", 0)
	if err != nil {
		t.Fatal(err)
	}
	if catalogCalls != 1 {
		t.Fatalf("scoped default catalog calls = %d, want 1", catalogCalls)
	}
	if tokenCalls != 0 {
		t.Fatalf("registration token calls = %d, want 0", tokenCalls)
	}
	if got.FailureStage != "template_resolution" || got.LastErrorRetryable {
		t.Fatalf("state = %#v, want non-retryable template_resolution failure", got)
	}
	if strings.Contains(got.Error, apiKey) ||
		strings.Contains(got.LastErrorMessage, apiKey) ||
		strings.Contains(string(logData), apiKey) {
		t.Fatalf("user-visible failure exposed scoped Sandbox API key: state=%#v log=%q", got, logData)
	}
}

func TestRunnerLifecycleManagedResolutionDoesNotCacheOrRewriteProfile(t *testing.T) {
	ghServer := newLifecycleGitHubServer(t, nil)
	defer ghServer.Close()

	store := state.New(t.TempDir())
	original := lifecycleManagedProfile("catalog-bootstrap-id")
	upsertLifecycleProfile(t, store, original)

	firstSandbox := &managedLifecycleSandboxService{
		lifecycleSandboxService: &lifecycleSandboxService{},
		templates: []sandboxrunner.CatalogTemplate{{
			TemplateID:  "region-a-template-id",
			Names:       []string{"github-runner-ubuntu-24-04"},
			BuildStatus: "ready",
			Public:      true,
		}},
	}
	firstServer := newRunnerLifecycleTestServer(t, store, ghServer.URL, firstSandbox)
	createLifecycleRequest(t, store, "region-a", "managed", 101)
	go firstServer.startRunner(context.Background(), "region-a", "worker-a")
	waitForState(t, store, "region-a", state.StatusRunning)

	secondSandbox := &managedLifecycleSandboxService{
		lifecycleSandboxService: &lifecycleSandboxService{},
		templates: []sandboxrunner.CatalogTemplate{{
			TemplateID:  "region-b-template-id",
			Names:       []string{"github-runner-ubuntu-24-04"},
			BuildStatus: "ready",
			Public:      true,
		}},
	}
	secondServer := newRunnerLifecycleTestServer(t, store, ghServer.URL, secondSandbox)
	createLifecycleRequest(t, store, "region-b", "managed", 202)
	go secondServer.startRunner(context.Background(), "region-b", "worker-b")
	waitForState(t, store, "region-b", state.StatusRunning)

	firstInputs := firstSandbox.startInputs()
	secondInputs := secondSandbox.startInputs()
	if len(firstInputs) != 1 || firstInputs[0].TemplateID != "region-a-template-id" {
		t.Fatalf("region A inputs = %#v, want its resolved template id", firstInputs)
	}
	if len(secondInputs) != 1 || secondInputs[0].TemplateID != "region-b-template-id" {
		t.Fatalf("region B inputs = %#v, want its resolved template id", secondInputs)
	}
	if firstSandbox.catalogCallCount() != 1 || secondSandbox.catalogCallCount() != 1 {
		t.Fatalf("catalog calls = (%d, %d), want one per runner without cache", firstSandbox.catalogCallCount(), secondSandbox.catalogCallCount())
	}
	saved, err := store.GetProfile("managed")
	if err != nil {
		t.Fatal(err)
	}
	if saved.TemplateID != original.TemplateID ||
		saved.DefaultTemplateName != original.DefaultTemplateName ||
		saved.ManagedBy != original.ManagedBy {
		t.Fatalf("managed profile was rewritten: got %#v want stable fields from %#v", saved, original)
	}
}

type lifecycleEventRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *lifecycleEventRecorder) add(event string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *lifecycleEventRecorder) snapshot() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

type profileLoadRecordingStore struct {
	state.Store
	events *lifecycleEventRecorder
}

type catalogReadRejectingStore struct {
	state.Store
	matchReads int
}

func (s *catalogReadRejectingStore) MatchProfile(string, []string) (state.ProfileMatch, error) {
	s.matchReads++
	return state.ProfileMatch{}, errors.New("retry must not rematch the persisted runner spec")
}

func (s *profileLoadRecordingStore) GetProfile(name string) (state.RunnerProfile, error) {
	s.events.add("profile")
	return s.Store.GetProfile(name)
}

type lifecycleSandboxService struct {
	mu     sync.Mutex
	events *lifecycleEventRecorder
	inputs []sandboxrunner.StartInput
}

func (s *lifecycleSandboxService) ValidateTemplate(context.Context, string) error {
	return nil
}

func (s *lifecycleSandboxService) StartRunner(_ context.Context, input sandboxrunner.StartInput) (sandboxrunner.StartResult, error) {
	s.mu.Lock()
	s.inputs = append(s.inputs, input)
	s.mu.Unlock()
	s.events.add("start")
	return sandboxrunner.StartResult{SandboxID: "sandbox-" + input.RequestID, PID: 42}, nil
}

func (s *lifecycleSandboxService) RecoverRunner(_ context.Context, input sandboxrunner.RecoverInput) (sandboxrunner.StartResult, error) {
	return sandboxrunner.StartResult{SandboxID: input.SandboxID, PID: input.PID}, nil
}

func (s *lifecycleSandboxService) StopRunner(context.Context, string, uint32) error {
	return nil
}

func (s *lifecycleSandboxService) StartTerminal(context.Context, string, sandboxrunner.PtySize, func([]byte)) (sandboxrunner.TerminalSession, error) {
	return nil, nil
}

func (s *lifecycleSandboxService) startInputs() []sandboxrunner.StartInput {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]sandboxrunner.StartInput(nil), s.inputs...)
}

type managedLifecycleSandboxService struct {
	*lifecycleSandboxService

	mu           sync.Mutex
	templates    []sandboxrunner.CatalogTemplate
	listErr      error
	catalogCalls int
}

func (s *managedLifecycleSandboxService) ListDefaultTemplates(context.Context) ([]sandboxrunner.CatalogTemplate, error) {
	s.mu.Lock()
	s.catalogCalls++
	templates := append([]sandboxrunner.CatalogTemplate(nil), s.templates...)
	err := s.listErr
	s.mu.Unlock()
	s.events.add("catalog")
	return templates, err
}

func (s *managedLifecycleSandboxService) catalogCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalogCalls
}

func newRunnerLifecycleTestServer(t *testing.T, store state.Store, ghURL string, sandbox sandboxrunner.Service) *Server {
	t.Helper()
	cfg := config.Config{
		AuthEncryptionKey:    "lifecycle-encryption-key",
		SandboxTimeout:       time.Hour,
		SandboxCreateTimeout: time.Second,
		SandboxStopTimeout:   time.Second,
		WorkerLeaseTTL:       time.Minute,
		RetryBaseDelay:       time.Minute,
		RetryMaxDelay:        time.Minute,
		RetryMaxAttempts:     3,
		MaxConcurrentRunners: 10,
		GitHubAPIBaseURL:     ghURL,
	}
	srv := New(cfg, store, github.NewClient(ghURL, http.DefaultClient), sandbox, nil)
	t.Cleanup(srv.Close)
	return srv
}

func newLifecycleGitHubServer(t *testing.T, events *lifecycleEventRecorder) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/repos/o/r/actions/runners/registration-token" {
			t.Errorf("unexpected GitHub request: %s %s", r.Method, r.URL.Path)
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}
		events.add("token")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"token":"runner-token","expires_at":"2026-05-18T10:00:00Z"}`))
	}))
}

func upsertLifecycleProfile(t *testing.T, store state.Store, profile state.RunnerProfile) {
	t.Helper()
	if _, err := store.UpsertProfile(profile); err != nil {
		t.Fatal(err)
	}
}

func lifecycleManagedProfile(templateID string) state.RunnerProfile {
	return state.RunnerProfile{
		Name:                "managed",
		Labels:              []string{"self-hosted", "managed"},
		RequiredLabels:      []string{"managed"},
		TemplateID:          templateID,
		DefaultTemplateName: "github-runner-ubuntu-24-04",
		MaxConcurrency:      10,
		Enabled:             true,
		ManagedBy:           "qiniu/ci-runner",
		CatalogRevision:     1,
	}
}

func createLifecycleRequest(t *testing.T, store state.Store, id, profileName string, installationID int64) {
	t.Helper()
	created, _, err := store.CreateRequest(state.RunnerRequest{
		ID:                   id,
		Source:               "test",
		GitHubInstallationID: installationID,
		RepositoryFullName:   "o/r",
		Labels:               []string{"self-hosted", profileName},
		ProfileName:          profileName,
		RunnerName:           "e2b-" + id,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatalf("runner request %q was not created", id)
	}
}

func waitForLifecycleOutcome(t *testing.T, store state.Store, id string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, err := store.ReadState(id)
		if err == nil && (current.Status == state.StatusRunning || current.FailureStage != "") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	current, _ := store.ReadState(id)
	t.Fatalf("state %q did not reach running or failure: %#v", id, current)
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

var (
	_ sandboxrunner.Service                = (*lifecycleSandboxService)(nil)
	_ sandboxrunner.Service                = (*managedLifecycleSandboxService)(nil)
	_ sandboxrunner.DefaultTemplateCatalog = (*managedLifecycleSandboxService)(nil)
)

func TestDefaultTemplateResolutionErrorIncludesRequestedNameAndMachineReason(t *testing.T) {
	err := &defaultTemplateResolutionError{
		RequestedName: "github-runner-ubuntu-24-04",
		Reason:        defaultTemplateResolutionReasonMissing,
	}
	if got := err.Error(); !strings.Contains(got, err.RequestedName) || !strings.Contains(got, err.Reason) {
		t.Fatalf("error message = %q, want requested name and reason", got)
	}
}
