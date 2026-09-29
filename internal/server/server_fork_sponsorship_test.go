package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestUserForkSponsorshipPolicyAPIRequiresOrganizationOwnerAndAuditsMutation(t *testing.T) {
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/user/memberships/orgs":
			_, _ = w.Write([]byte(`[{"state":"active","role":"admin","organization":{"id":600,"login":"acme"}}]`))
		case "/user/installations/200/repositories":
			if r.Header.Get("Authorization") != "Bearer user-token" {
				t.Fatalf("repository list authorization = %q", r.Header.Get("Authorization"))
			}
			_, _ = w.Write([]byte(`{"repositories":[{"id":300,"name":"project","full_name":"acme/project","fork":false,"owner":{"id":600,"login":"acme","type":"Organization"}},{"id":301,"name":"forked","full_name":"acme/forked","fork":true,"owner":{"id":600,"login":"acme","type":"Organization"}},{"id":302,"name":"other","full_name":"other/project","fork":false,"owner":{"id":700,"login":"other","type":"Organization"}}]}`))
		case "/repos/acme/project":
			_, _ = w.Write([]byte(`{"id":300,"full_name":"acme/project","fork":false,"owner":{"id":600,"login":"acme","type":"Organization"}}`))
		default:
			t.Fatalf("unexpected GitHub API request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer githubAPI.Close()

	store := state.New(t.TempDir())
	srv := newTestServer(t, store, githubAPI.URL, &fakeSandbox{})
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
	installation, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 200, GitHubAccountID: 600, AccountType: "organization", AccountLogin: "acme"})
	if err != nil {
		t.Fatal(err)
	}
	target := "/user/fork-sponsorship-policies?installation_id=" + strconv.FormatInt(installation.ID, 10)
	repositoriesTarget := "/user/fork-sponsorship-repositories?installation_id=" + strconv.FormatInt(installation.ID, 10)
	req := httptest.NewRequest(http.MethodGet, repositoriesTarget, nil)
	req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"full_name":"acme/project"`) || strings.Contains(rec.Body.String(), "acme/forked") || strings.Contains(rec.Body.String(), "other/project") {
		t.Fatalf("list source repositories status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(`{"source_repository_full_name":"acme/project","mode":"approval_required","enabled":true,"max_concurrency":2}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"source_repository_id":300`) {
		t.Fatalf("create policy status=%d body=%s", rec.Code, rec.Body.String())
	}
	events, err := store.ListAuditEvents(10)
	if err != nil || len(events) == 0 || events[0].Action != "fork_sponsorship_policy.create" {
		t.Fatalf("policy audit events=%#v err=%v", events, err)
	}

	req = httptest.NewRequest(http.MethodPost, target, bytes.NewBufferString(`{"source_repository_full_name":"acme/project","mode":"organization_member","enabled":false,"max_concurrency":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create policy status=%d body=%s", rec.Code, rec.Body.String())
	}
	policy, err := store.GetForkSponsorshipPolicy(installation.InstallationID, 300)
	if err != nil || policy.Mode != state.ForkSponsorshipModeApprovalRequired || !policy.Enabled || policy.MaxConcurrency != 2 {
		t.Fatalf("duplicate create changed policy: policy=%#v err=%v", policy, err)
	}
	events, err = store.ListAuditEvents(10)
	if err != nil || len(events) != 1 {
		t.Fatalf("duplicate create audit events=%#v err=%v", events, err)
	}

	req = httptest.NewRequest(http.MethodGet, target, nil)
	req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"source_repository_full_name":"acme/project"`) {
		t.Fatalf("list policy status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/user/fork-sponsorship-policies", nil)
	req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("personal scope status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSandboxServiceUsesEligibleForkSponsorshipModes(t *testing.T) {
	for _, mode := range []string{
		state.ForkSponsorshipModeApprovalRequired,
		state.ForkSponsorshipModeWritePermission,
		state.ForkSponsorshipModeOrganizationMember,
	} {
		t.Run(mode, func(t *testing.T) {
			githubServer := forkSponsorshipGitHubServer(t)
			defer githubServer.Close()
			store := state.New(t.TempDir())
			srv := New(config.Config{AuthEncryptionKey: "encryption-key", MaxConcurrentRunners: 10}, store, github.NewClient(githubServer.URL, githubServer.Client()), nil, nil)
			configureForkSponsorshipTestState(t, srv, store, mode, 2)

			svc, snapshot, err := srv.sandboxServiceAndConfigForRunnerRequestContext(t.Context(), state.RunnerRequest{
				ID:                   "fork-request",
				GitHubInstallationID: 100,
				RepositoryFullName:   "member/project",
				ProfileName:          "ubuntu-managed",
				ProfileSource:        "global",
			})
			if err != nil || svc == nil {
				t.Fatalf("resolve sponsored service: service=%T snapshot=%#v err=%v", svc, snapshot, err)
			}
			if snapshot.Source != sandboxConfigSourceForkSponsorship || snapshot.SponsorInstallationID != 200 || snapshot.SponsorSourceRepositoryID != 300 || snapshot.SponsorAuthorizationReason != mode {
				t.Fatalf("unexpected sponsorship snapshot: %#v", snapshot)
			}
		})
	}
}

func TestSandboxServiceUsesEligiblePlatformDefaultForForkSponsorship(t *testing.T) {
	githubServer := forkSponsorshipGitHubServer(t)
	defer githubServer.Close()
	store := state.New(t.TempDir())
	srv := New(config.Config{AuthEncryptionKey: "encryption-key", MaxConcurrentRunners: 10}, store, github.NewClient(githubServer.URL, githubServer.Client()), nil, nil)
	configureForkSponsorshipTestState(t, srv, store, state.ForkSponsorshipModeOrganizationMember, 2)
	removeForkSponsorshipSandboxService(t, store)

	encrypted, err := encryptSecret("platform-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSandboxServiceDefault(state.SandboxServiceDefault{
		Enabled:         true,
		AudienceMode:    state.SandboxServiceDefaultAudienceModeSelected,
		APIURL:          "https://platform-sandbox.example.test",
		APIKeyEncrypted: encrypted,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSandboxServiceDefaultAudience(state.SandboxServiceDefaultAudience{
		GitHubAccountID: 600,
		AccountType:     "organization",
		AccountLogin:    "acme",
	}); err != nil {
		t.Fatal(err)
	}

	svc, snapshot, err := srv.sandboxServiceAndConfigForRunnerRequestContext(t.Context(), state.RunnerRequest{
		ID:                   "platform-sponsored-request",
		GitHubInstallationID: 100,
		RepositoryFullName:   "member/project",
		ProfileName:          "ubuntu-managed",
		ProfileSource:        "global",
	})
	if err != nil || svc == nil {
		t.Fatalf("resolve platform-sponsored service: service=%T snapshot=%#v err=%v", svc, snapshot, err)
	}
	if snapshot.APIURL != "https://platform-sandbox.example.test" || snapshot.EncryptedAPIKey != encrypted {
		t.Fatalf("unexpected platform Sandbox snapshot: %#v", snapshot)
	}
	if snapshot.Source != sandboxConfigSourceForkSponsorship || snapshot.SponsorInstallationID != 200 || snapshot.SponsorSourceRepositoryID != 300 || snapshot.SponsorAuthorizationReason != state.ForkSponsorshipModeOrganizationMember {
		t.Fatalf("unexpected sponsorship provenance: %#v", snapshot)
	}
}

func TestForkSponsorshipRejectsPlatformDefaultOutsideSponsorAudience(t *testing.T) {
	githubServer := forkSponsorshipGitHubServer(t)
	defer githubServer.Close()
	store := state.New(t.TempDir())
	srv := New(config.Config{AuthEncryptionKey: "encryption-key", MaxConcurrentRunners: 10}, store, github.NewClient(githubServer.URL, githubServer.Client()), nil, nil)
	configureForkSponsorshipTestState(t, srv, store, state.ForkSponsorshipModeOrganizationMember, 2)
	removeForkSponsorshipSandboxService(t, store)

	encrypted, err := encryptSecret("platform-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSandboxServiceDefault(state.SandboxServiceDefault{
		Enabled:         true,
		AudienceMode:    state.SandboxServiceDefaultAudienceModeSelected,
		APIURL:          "https://platform-sandbox.example.test",
		APIKeyEncrypted: encrypted,
	}); err != nil {
		t.Fatal(err)
	}

	_, _, err = srv.sandboxServiceAndConfigForRunnerRequestContext(t.Context(), state.RunnerRequest{
		ID:                   "ineligible-platform-sponsored-request",
		GitHubInstallationID: 100,
		RepositoryFullName:   "member/project",
		ProfileName:          "ubuntu-managed",
		ProfileSource:        "global",
	})
	if !errors.Is(err, errSandboxServiceNotConfigured) {
		t.Fatalf("platform default outside sponsor audience error = %v, want Sandbox service not configured", err)
	}
}

func TestForkSponsorshipDoesNotHideCorruptSponsorConfigurationWithPlatformDefault(t *testing.T) {
	githubServer := forkSponsorshipGitHubServer(t)
	defer githubServer.Close()
	store := state.New(t.TempDir())
	srv := New(config.Config{AuthEncryptionKey: "encryption-key", MaxConcurrentRunners: 10}, store, github.NewClient(githubServer.URL, githubServer.Client()), nil, nil)
	configureForkSponsorshipTestState(t, srv, store, state.ForkSponsorshipModeOrganizationMember, 2)
	if _, err := store.UpsertAccountPreference(state.AccountPreference{
		ScopeType: state.AccountScopeTypeGitHubInstall,
		ScopeID:   200,
		Namespace: accountPreferenceNamespaceSandbox,
		Key:       accountPreferenceKeySandboxService,
		ValueJSON: "{",
	}); err != nil {
		t.Fatal(err)
	}
	encrypted, err := encryptSecret("platform-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSandboxServiceDefault(state.SandboxServiceDefault{
		Enabled:         true,
		AudienceMode:    state.SandboxServiceDefaultAudienceModeAll,
		APIURL:          "https://platform-sandbox.example.test",
		APIKeyEncrypted: encrypted,
	}); err != nil {
		t.Fatal(err)
	}

	_, _, err = srv.sandboxServiceAndConfigForRunnerRequestContext(t.Context(), state.RunnerRequest{
		ID:                   "corrupt-sponsor-request",
		GitHubInstallationID: 100,
		RepositoryFullName:   "member/project",
		ProfileName:          "ubuntu-managed",
		ProfileSource:        "global",
	})
	if err == nil || errors.Is(err, errSandboxServiceNotConfigured) {
		t.Fatalf("corrupt sponsor configuration error = %v, want fail-closed configuration error", err)
	}
}

func TestForkSponsorshipCapacityAndManagedOnlyBoundary(t *testing.T) {
	githubServer := forkSponsorshipGitHubServer(t)
	defer githubServer.Close()
	store := state.New(t.TempDir())
	srv := New(config.Config{AuthEncryptionKey: "encryption-key", MaxConcurrentRunners: 10}, store, github.NewClient(githubServer.URL, githubServer.Client()), nil, nil)
	configureForkSponsorshipTestState(t, srv, store, state.ForkSponsorshipModeApprovalRequired, 1)
	created, active, err := store.CreateRequest(state.RunnerRequest{ID: "active-sponsored", Source: "test", Labels: []string{"qiniu"}, RunnerName: "active-sponsored"}, nil)
	if err != nil || !created {
		t.Fatalf("create active sponsored request: created=%v err=%v", created, err)
	}
	active.Status = state.StatusRunning
	active.SponsorInstallationID = 200
	active.SponsorSourceRepositoryID = 300
	if err := store.WriteState(active); err != nil {
		t.Fatal(err)
	}
	created, _, err = store.CreateRequest(state.RunnerRequest{ID: "at-capacity", Source: "test", GitHubInstallationID: 100, RepositoryFullName: "member/project", ProfileName: "ubuntu-managed", ProfileSource: "global", Labels: []string{"qiniu"}, RunnerName: "at-capacity"}, nil)
	if err != nil || !created {
		t.Fatalf("create capacity request: created=%v err=%v", created, err)
	}
	_, snapshot, err := srv.sandboxServiceAndConfigForRunnerRequestContext(t.Context(), state.RunnerRequest{ID: "at-capacity", GitHubInstallationID: 100, RepositoryFullName: "member/project", ProfileName: "ubuntu-managed", ProfileSource: "global"})
	if err != nil {
		t.Fatal(err)
	}
	err = srv.persistSandboxServiceSnapshot("at-capacity", snapshot)
	if !errors.Is(err, errForkSponsorshipAtCapacity) {
		t.Fatalf("capacity error = %v", err)
	}
	if _, err := store.UpsertProfile(state.RunnerProfile{Name: "platform-custom", Labels: []string{"self-hosted"}, RequiredLabels: []string{"self-hosted"}, TemplateID: "private-template", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	_, _, err = srv.sandboxServiceAndConfigForRunnerRequestContext(t.Context(), state.RunnerRequest{ID: "custom", GitHubInstallationID: 100, RepositoryFullName: "member/project", ProfileName: "platform-custom", ProfileSource: "global"})
	if !errors.Is(err, errSandboxServiceNotConfigured) {
		t.Fatalf("platform custom profile unexpectedly used sponsorship: %v", err)
	}
}

func TestSandboxServicePreservesForkSponsorshipProvenanceFromRequestSnapshot(t *testing.T) {
	store := state.New(t.TempDir())
	srv := New(config.Config{AuthEncryptionKey: "encryption-key", MaxConcurrentRunners: 10}, store, nil, nil, nil)
	encrypted, err := encryptSecret("sponsor-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}

	svc, snapshot, err := srv.sandboxServiceAndConfigForRunnerRequest(state.RunnerRequest{
		ID:                              "saved-sponsored-request",
		SandboxAPIURL:                   "https://sandbox.example.test",
		SandboxAPIKeyEncrypted:          encrypted,
		SandboxConfigSource:             sandboxConfigSourceForkSponsorship,
		SponsorInstallationID:           200,
		SponsorSourceRepositoryID:       300,
		SponsorSourceRepositoryFullName: " acme/project ",
		SponsorAuthorizationReason:      " approval_required ",
	})
	if err != nil || svc == nil {
		t.Fatalf("resolve saved sponsored service: service=%T snapshot=%#v err=%v", svc, snapshot, err)
	}
	if snapshot.Source != sandboxConfigSourceForkSponsorship || snapshot.SponsorInstallationID != 200 || snapshot.SponsorSourceRepositoryID != 300 || snapshot.SponsorSourceRepositoryFullName != "acme/project" || snapshot.SponsorAuthorizationReason != state.ForkSponsorshipModeApprovalRequired {
		t.Fatalf("saved sponsorship provenance was not preserved: %#v", snapshot)
	}
}

func TestForkSponsorshipReservationRechecksExactApproval(t *testing.T) {
	githubServer := forkSponsorshipGitHubServer(t)
	defer githubServer.Close()
	store := state.New(t.TempDir())
	srv := New(config.Config{AuthEncryptionKey: "encryption-key", MaxConcurrentRunners: 10}, store, github.NewClient(githubServer.URL, githubServer.Client()), nil, nil)
	configureForkSponsorshipTestState(t, srv, store, state.ForkSponsorshipModeApprovalRequired, 2)
	created, _, err := store.CreateRequest(state.RunnerRequest{ID: "approval-revoked", Source: "test", GitHubInstallationID: 100, RepositoryFullName: "member/project", ProfileName: "ubuntu-managed", ProfileSource: "global", Labels: []string{"qiniu"}, RunnerName: "approval-revoked"}, nil)
	if err != nil || !created {
		t.Fatalf("create request: created=%v err=%v", created, err)
	}
	_, snapshot, err := srv.sandboxServiceAndConfigForRunnerRequestContext(t.Context(), state.RunnerRequest{ID: "approval-revoked", GitHubInstallationID: 100, RepositoryFullName: "member/project", ProfileName: "ubuntu-managed", ProfileSource: "global"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteForkSponsorshipApproval(200, 300, 400); err != nil {
		t.Fatal(err)
	}
	if err := srv.persistSandboxServiceSnapshot("approval-revoked", snapshot); !errors.Is(err, errForkSponsorshipPolicyChanged) {
		t.Fatalf("reservation after approval revocation = %v, want policy changed", err)
	}
	got, err := store.ReadState("approval-revoked")
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxAPIURL != "" || got.SandboxAPIKeyEncrypted != "" || got.SponsorInstallationID != 0 {
		t.Fatalf("revoked approval persisted sponsorship snapshot: %#v", got)
	}
}

func forkSponsorshipGitHubServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/repos/member/project":
			_, _ = w.Write([]byte(`{"id":400,"full_name":"member/project","fork":true,"owner":{"id":500,"login":"member","type":"User"},"source":{"id":300,"full_name":"acme/project","owner":{"id":600,"login":"acme","type":"Organization"}}}`))
		case "/repos/acme/project/collaborators/member/permission":
			_, _ = w.Write([]byte(`{"permission":"write","user":{"id":500,"login":"member"}}`))
		case "/orgs/acme/memberships/member":
			_, _ = w.Write([]byte(`{"state":"active","role":"member","user":{"id":500,"login":"member"}}`))
		default:
			t.Fatalf("unexpected GitHub path: %s", r.URL.Path)
		}
	}))
}

func configureForkSponsorshipTestState(t *testing.T, srv *Server, store state.Store, mode string, maxConcurrency int) {
	t.Helper()
	if _, err := store.UpsertProfile(state.RunnerProfile{Name: "ubuntu-managed", Labels: []string{"self-hosted", "qiniu"}, RequiredLabels: []string{"qiniu"}, DefaultTemplateName: "github-runner-ubuntu-24-04", MaxConcurrency: 10, Enabled: true, ManagedBy: "qiniu/ci-runner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertGitHubInstallationOwner(200, state.GitHubInstallationAccount{GitHubAccountID: 600, AccountType: "organization", AccountLogin: "acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertForkSponsorshipPolicy(state.ForkSponsorshipPolicy{SponsorInstallationID: 200, SourceRepositoryID: 300, SourceRepositoryFullName: "acme/project", Mode: mode, Enabled: true, MaxConcurrency: maxConcurrency}); err != nil {
		t.Fatal(err)
	}
	if mode == state.ForkSponsorshipModeApprovalRequired {
		if _, err := store.UpsertForkSponsorshipApproval(state.ForkSponsorshipApproval{SponsorInstallationID: 200, SourceRepositoryID: 300, ForkRepositoryID: 400, ForkRepositoryFullName: "member/project", ForkOwnerID: 500, ForkOwnerLogin: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	preferenceJSON, err := json.Marshal(accountSandboxServicePreferenceValue{APIURL: "https://sandbox.example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountPreference(state.AccountPreference{ScopeType: state.AccountScopeTypeGitHubInstall, ScopeID: 200, Namespace: accountPreferenceNamespaceSandbox, Key: accountPreferenceKeySandboxService, ValueJSON: string(preferenceJSON)}); err != nil {
		t.Fatal(err)
	}
	encrypted, err := encryptSecret("sponsor-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountSecret(state.AccountSecret{ScopeType: state.AccountScopeTypeGitHubInstall, ScopeID: 200, KeyType: state.AccountSecretTypeSandboxAPIKey, EncryptedValue: encrypted}); err != nil {
		t.Fatal(err)
	}
}

func removeForkSponsorshipSandboxService(t *testing.T, store state.Store) {
	t.Helper()
	if err := store.DeleteAccountPreference(state.AccountScopeTypeGitHubInstall, 200, accountPreferenceNamespaceSandbox, accountPreferenceKeySandboxService); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteAccountSecret(state.AccountScopeTypeGitHubInstall, 200, state.AccountSecretTypeSandboxAPIKey); err != nil {
		t.Fatal(err)
	}
}
