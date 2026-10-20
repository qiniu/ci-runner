package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestDeploymentSandboxServiceMatchesJobEligibility(t *testing.T) {
	for _, tc := range []struct {
		name, scopeConfig, audience, source      string
		enabled, incomplete, corruptKey, missing bool
	}{
		{name: "enabled platform", enabled: true, source: sandboxConfigSourceAdminDefault},
		{name: "disabled platform"},
		{name: "missing platform", missing: true},
		{name: "incomplete platform", enabled: true, incomplete: true},
		{name: "selected organization", enabled: true, audience: "allowed", source: sandboxConfigSourceAdminDefault},
		{name: "unselected organization", enabled: true, audience: "missing"},
		{name: "wrong audience type", enabled: true, audience: "user"},
		{name: "scoped priority", enabled: true, scopeConfig: "custom", source: sandboxConfigSourceInstallation},
		{name: "scoped without platform", scopeConfig: "custom", source: sandboxConfigSourceInstallation},
		{name: "inherited priority", enabled: true, scopeConfig: "inherit", source: sandboxConfigSourceInheritedAccount},
		{name: "corrupt scope fails closed", enabled: true, scopeConfig: "corrupt"},
		{name: "corrupt scoped key fails closed", enabled: true, scopeConfig: "custom", corruptKey: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := state.New(t.TempDir())
			srv := newTestServer(t, store, "", nil)
			srv.Close()
			srv.sandbox = nil // Exercise credential resolution rather than the helper's fake service.
			account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, GitHubAccountID: 600, AccountType: "organization", AccountLogin: "acme"}); err != nil {
				t.Fatal(err)
			}
			key, err := encryptSecret("fixture-platform-key", srv.cfg.AuthEncryptionKey.Value())
			if err != nil {
				t.Fatal(err)
			}
			platform := state.SandboxServiceDefault{Enabled: tc.enabled, APIURL: "https://platform.example", APIKeyEncrypted: key}
			if tc.incomplete {
				platform.APIKeyEncrypted = ""
			}
			if tc.audience != "" {
				platform.AudienceMode = state.SandboxServiceDefaultAudienceModeSelected
				if tc.audience != "missing" {
					kind := "organization"
					if tc.audience == "user" {
						kind = "user"
					}
					if _, err := store.UpsertSandboxServiceDefaultAudience(state.SandboxServiceDefaultAudience{GitHubAccountID: 600, AccountType: kind, AccountLogin: "acme"}); err != nil {
						t.Fatal(err)
					}
				}
			}
			if !tc.missing {
				if _, err := store.UpsertSandboxServiceDefault(platform); err != nil {
					t.Fatal(err)
				}
			}
			scope := accountPreferenceScope{Type: state.AccountScopeTypeGitHubInstall, ID: 987}
			if tc.scopeConfig != "" {
				value := `{"api_url":"https://scoped.example"}`
				if tc.scopeConfig == "corrupt" {
					value = "{"
				} else if tc.scopeConfig == "inherit" {
					value = `{"mode":"inherit","source_account_id":` + strconv.FormatInt(account.ID, 10) + `}`
				}
				if _, err := store.UpsertAccountPreference(state.AccountPreference{ScopeType: scope.Type, ScopeID: scope.ID, Namespace: accountPreferenceNamespaceSandbox, Key: accountPreferenceKeySandboxService, ValueJSON: value}); err != nil {
					t.Fatal(err)
				}
				secretScope := scope
				if tc.scopeConfig == "inherit" {
					secretScope = accountPreferenceScope{Type: state.AccountScopeTypeAccount, ID: account.ID}
					if _, err := store.UpsertAccountPreference(state.AccountPreference{ScopeType: secretScope.Type, ScopeID: secretScope.ID, Namespace: accountPreferenceNamespaceSandbox, Key: accountPreferenceKeySandboxService, ValueJSON: `{"api_url":"https://scoped.example"}`}); err != nil {
						t.Fatal(err)
					}
				}
				if tc.corruptKey {
					key = "invalid-encrypted-key"
				}
				if _, err := store.UpsertAccountSecret(state.AccountSecret{ScopeType: secretScope.Type, ScopeID: secretScope.ID, KeyType: state.AccountSecretTypeSandboxAPIKey, EncryptedValue: key}); err != nil {
					t.Fatal(err)
				}
			}
			service, snapshot, deployErr := srv.sandboxServiceForDeployment(context.Background(), scope, 987)
			jobService, jobSnapshot, jobErr := srv.sandboxServiceAndConfigForRunnerRequestContext(context.Background(), state.RunnerRequest{ID: "eligibility", GitHubInstallationID: 987})
			if tc.source == "" {
				if deployErr == nil || service != nil || jobErr == nil || jobService != nil {
					t.Fatal("unavailable or corrupt configuration must deny both Deploy and Job")
				}
				if tc.scopeConfig == "corrupt" || tc.corruptKey {
					if errors.Is(deployErr, errSandboxServiceNotConfigured) {
						t.Fatal("corruption must not be treated as missing configuration")
					}
				} else if !errors.Is(deployErr, errSandboxServiceNotConfigured) {
					t.Fatalf("expected not configured error, got %v", deployErr)
				}
				return
			}
			if deployErr != nil || jobErr != nil || service == nil || jobService == nil || snapshot.Source != tc.source || snapshot != jobSnapshot {
				t.Fatalf("Deploy/Job service selection differs: sources=%q/%q errors=%v/%v", snapshot.Source, jobSnapshot.Source, deployErr, jobErr)
			}
			// Legacy records without saved credentials use the same eligible service.
			factory := serverDeploymentRuntimeFactory{server: srv}
			if _, _, err := factory.service(context.Background(), state.Deployment{ScopeType: scope.Type, ScopeID: scope.ID, GitHubInstallationID: 987}); err != nil {
				t.Fatalf("legacy deployment service: %v", err)
			}
		})
	}
}

func TestDeploymentRuntimePreservesSavedService(t *testing.T) {
	var calls atomic.Int32
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("X-API-Key") != "fixture-saved-key" {
			t.Error("saved provider credential was replaced")
		}
		http.NotFound(w, r)
	}))
	defer provider.Close()
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "", nil)
	srv.Close()
	key, err := encryptSecret("fixture-saved-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	// No currently enabled platform default: saved runtime access must survive.
	d := state.Deployment{SandboxID: "saved-sandbox", SandboxAPIURL: provider.URL, SandboxAPIKeyEncrypted: key, SandboxConfigSource: sandboxConfigSourceAdminDefault, ExpiresAt: time.Now().Add(time.Hour)}
	factory := serverDeploymentRuntimeFactory{server: srv}
	if _, err := factory.Connect(context.Background(), d); err != nil {
		t.Fatalf("saved provider lookup: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("saved provider calls=%d", calls.Load())
	}
	d.SandboxAPIKeyEncrypted = "invalid-encrypted-key"
	if _, err := factory.Connect(context.Background(), d); err == nil || calls.Load() != 1 {
		t.Fatal("corrupt saved credentials must fail without fallback or provider I/O")
	}
}

func TestLegacyDeploymentRejectsPlatformPrivateTemplate(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "", nil)
	srv.Close()
	srv.sandboxHTTP = &http.Client{Transport: profileValidationTransportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/default-templates" {
			t.Errorf("legacy template rejection must precede validation/creation: %s", r.URL.Path)
		}
		return (deploymentTemplateTransport{}).RoundTrip(r)
	})}
	key, err := encryptSecret("fixture-platform-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSandboxServiceDefault(state.SandboxServiceDefault{Enabled: true, APIURL: "https://platform.example", APIKeyEncrypted: key}); err != nil {
		t.Fatal(err)
	}
	factory := serverDeploymentRuntimeFactory{server: srv}
	for _, template := range []string{"deploy-template-a", "unbuilt", "missing"} {
		d := state.Deployment{ID: "legacy", ScopeType: state.AccountScopeTypeAccount, ScopeID: 1, DeploymentTemplateID: template, ExpiresAt: time.Now().Add(time.Hour)}
		if _, _, err := factory.Create(context.Background(), d, 1); err == nil || !strings.Contains(err.Error(), "must be public") {
			t.Fatalf("legacy private/unbuilt template %q was not rejected: %v", template, err)
		}
	}
}
