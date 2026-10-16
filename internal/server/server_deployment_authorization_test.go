package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func deploymentAuthCall(s *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func TestSuspendedInstallationDeniesJobsAndDeploymentAccess(t *testing.T) {
	var suspended atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/installations" {
			if suspended.Load() {
				fmt.Fprint(w, `{"installations":[{"id":987,"suspended_at":"2026-10-16T07:10:00Z"}]}`)
			} else {
				serveActiveInstallationFixture(w, r, 987)
			}
			return
		}
		if r.URL.Path == "/user/installations/987/repositories" {
			// Real GitHub continues returning this list during suspension.
			fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"acme/project"}]}`)
			return
		}
		t.Errorf("unexpected upstream call: %s", r.URL.Path)
		http.NotFound(w, r)
	}))
	defer api.Close()
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, api.URL, &fakeSandbox{})
	srv.Close()
	srv.loopCtx = context.Background()
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
	installation, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "organization", AccountLogin: "acme", GitHubAccountID: 600})
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.CreateDeployment(state.Deployment{ID: "suspension-deploy", AccountID: account.ID, ScopeType: state.AccountScopeTypeGitHubInstall, ScopeID: 987, GitHubInstallationID: 987, RepositoryID: 42, RepositoryFullName: "acme/project", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "encrypted", Status: state.DeploymentStatusCompleted, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AppendDeploymentEvent(state.DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: "retained-private-output"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.CreateRequest(state.RunnerRequest{ID: "suspension-job", Source: "test", GitHubInstallationID: 987, RepositoryFullName: "acme/project", Labels: []string{"self-hosted"}}, nil); err != nil {
		t.Fatal(err)
	}
	if rec := deploymentAuthCall(srv, "GET", "/user/deployments/"+d.ID, ""); rec.Code != 200 {
		t.Fatalf("baseline: %d %s", rec.Code, rec.Body.String())
	}
	suspended.Store(true)
	// Advance only this authorization entry past its real hard-expiry boundary.
	srv.userRepositoryAccessMu.Lock()
	entry := srv.userRepositoryAccessCache[account.ID]
	entry.expiresAt = time.Now().Add(-time.Second)
	srv.userRepositoryAccessCache[account.ID] = entry
	srv.userRepositoryAccessMu.Unlock()
	for _, path := range []string{"/user/deployments", "/user/runner_requests"} {
		rec := deploymentAuthCall(srv, "GET", path, "")
		if rec.Code != 200 || (path == "/user/deployments" && !strings.Contains(rec.Body.String(), `"total":0`)) || (path == "/user/runner_requests" && strings.TrimSpace(rec.Body.String()) != "[]") {
			t.Fatalf("suspended list: %d %s", rec.Code, rec.Body.String())
		}
	}
	for _, path := range []string{"/user/deployments/" + d.ID, "/user/deployments/" + d.ID + "/events", "/user/deployments/" + d.ID + "/revisions", "/user/deployments/" + d.ID + "/resources", "/user/deployments/" + d.ID + "/pull-request", "/user/runner_requests/suspension-job", "/user/runner_requests/suspension-job/events"} {
		rec := deploymentAuthCall(srv, "GET", path, "")
		if rec.Code != 404 || strings.Contains(rec.Body.String(), "retained-private-output") {
			t.Fatalf("suspended read: %s %d %s", path, rec.Code, rec.Body.String())
		}
	}
	for _, action := range []struct{ method, path string }{{"POST", "/user/deployments/" + d.ID + "/stop"}, {"POST", "/user/deployments/" + d.ID + "/retry"}, {"DELETE", "/user/deployments/" + d.ID}, {"POST", "/user/deployments/" + d.ID + "/revisions/1/terminal"}} {
		if rec := deploymentAuthCall(srv, action.method, action.path, "{}"); rec.Code != 404 {
			t.Fatalf("suspended mutation: %s %d %s", action.path, rec.Code, rec.Body.String())
		}
	}
	input := fmt.Sprintf(`{"installation_id":%d,"repository_full_name":"acme/project","ref":"main","source":"branch"}`, installation.ID)
	if rec := deploymentAuthCall(srv, "POST", "/user/deployments", input); rec.Code != 404 || !strings.Contains(rec.Body.String(), "github installation is not accessible") {
		t.Fatalf("suspended admission: %d %s", rec.Code, rec.Body.String())
	}
	refsPath := fmt.Sprintf("/user/deployment-refs?installation_id=%d&repository_full_name=acme/project&source=branch", installation.ID)
	if rec := deploymentAuthCall(srv, "GET", refsPath, ""); rec.Code != 404 || !strings.Contains(rec.Body.String(), "github installation is not accessible") {
		t.Fatalf("suspended discovery: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := srv.deploymentSource(t.Context(), d); err == nil {
		t.Fatal("suspended installation reached checkout")
	}
	suspended.Store(false)
	srv.invalidateUserRepositoryAccess(account.ID)
	if rec := deploymentAuthCall(srv, "GET", "/user/deployments/"+d.ID, ""); rec.Code != 200 {
		t.Fatalf("restored: %d %s", rec.Code, rec.Body.String())
	}
	retained, err := store.GetDeployment(d.ID)
	if err != nil || retained.Status != d.Status || retained.Version != d.Version {
		t.Fatal("authorization checks changed deployment lifecycle")
	}
}

func TestDeploymentsShareJobsRepositoryAuthorization(t *testing.T) {
	var calls atomic.Int32
	var revoked atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}

		calls.Add(1)
		if r.URL.Path != "/user/installations/987/repositories" {
			t.Errorf("unexpected GitHub call: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if revoked.Load() {
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"acme/project"}]}`)
	}))
	defer api.Close()
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, api.URL, &fakeSandbox{})
	srv.Close()
	srv.loopCtx = context.Background()
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
	if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "organization", AccountLogin: "acme", GitHubAccountID: 600}); err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		d := state.Deployment{ID: fmt.Sprintf("shared-%d", i), AccountID: account.ID + 1, ScopeType: state.AccountScopeTypeGitHubInstall, ScopeID: 987, GitHubInstallationID: 987, RepositoryID: 42, RepositoryFullName: "acme/project", Ref: "refs/heads/main", CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "private-spec", SandboxAPIKeyEncrypted: "private-key", Status: state.DeploymentStatusCompleted, ExpiresAt: time.Now().Add(time.Hour), CreatedAt: time.Now().Add(time.Duration(i) * time.Second)}
		if i == 1 {
			d.Status = state.DeploymentStatusRunning
		}
		if i == 2 {
			d.GitHubInstallationID = 988
		}
		if i == 3 {
			d.RepositoryFullName = "acme/hidden"
		}
		if _, err := store.CreateDeployment(d); err != nil {
			t.Fatal(err)
		}
		if err := store.AppendDeploymentEvent(state.DeploymentEvent{DeploymentID: d.ID, EventType: "output", Message: "saved-output"}); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{"/user/deployment-repositories", "/user/deployments", "/user/deployments/shared-0", "/user/deployments/shared-0/events", "/user/deployments/shared-0/revisions", "/user/deployments/shared-0/pull-request"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "saved-output") {
			t.Fatalf("anonymous history %d %s", rec.Code, rec.Body.String())
		}
	}
	// Warm the same cache used by Jobs, not a deployment-specific cache.
	if _, err := srv.userAuthorizedRepositoryAccess(context.Background(), account.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/user/deployments?limit=1&offset=1", "/user/deployments?repository_full_name=acme/project&ref=refs/heads/main&limit=1&offset=1"} {
		rec := deploymentAuthCall(srv, "GET", path, "")
		if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"total":2`) || !strings.Contains(rec.Body.String(), "shared-0") || strings.Contains(rec.Body.String(), "shared-1") {
			t.Fatalf("list %d %s", rec.Code, rec.Body.String())
		}
	}
	for _, test := range []struct {
		status string
		total  int64
		id     string
	}{{"all", 2, "shared-1"}, {"running", 1, "shared-1"}, {"processing", 0, ""}, {"stopped", 1, "shared-0"}, {"failed", 0, ""}, {"running,stopped", 2, "shared-1"}, {"stopped,failed", 1, "shared-0"}, {"running,processing,stopped,failed", 2, "shared-1"}} {
		rec := deploymentAuthCall(srv, "GET", "/user/deployments?repository_full_name=ACME/PROJECT&ref=refs/heads/main&limit=1&status="+test.status, "")
		var page struct {
			Items        []deploymentResponse         `json:"items"`
			Total        int64                        `json:"total"`
			StatusCounts state.DeploymentStatusCounts `json:"status_counts"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || rec.Code != 200 || page.Total != test.total || page.StatusCounts != (state.DeploymentStatusCounts{All: 2, Running: 1, Stopped: 1}) {
			t.Fatalf("status %s: %d %s %v", test.status, rec.Code, rec.Body.String(), err)
		}
		if test.id != "" && (len(page.Items) != 1 || page.Items[0].ID != test.id) || test.id == "" && len(page.Items) != 0 {
			t.Fatalf("status %s items: %#v", test.status, page.Items)
		}
	}
	for _, query := range []string{"status=unknown", "status=running&status=failed", "status=running%27%20OR%201%3D1", "status=running,unknown", "status=running,running", "status=all,running", "status=running,"} {
		if rec := deploymentAuthCall(srv, "GET", "/user/deployments?"+query, ""); rec.Code != 400 {
			t.Fatalf("accepted invalid filter %s: %d", query, rec.Code)
		}
	}
	if rec := deploymentAuthCall(srv, "GET", "/user/deployments?repository_full_name=acme/hidden&status=running", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status_counts":{"all":0,"running":0,"processing":0,"stopped":0,"failed":0}`) {
		t.Fatalf("hidden repository counts: %d %s", rec.Code, rec.Body.String())
	}
	for _, suffix := range []string{"", "?repository_full_name=ACME/PROJECT"} {
		rec := deploymentAuthCall(srv, "GET", "/user/deployment-repositories"+suffix, "")
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(rec.Body.String(), `"total":2`) || !strings.Contains(rec.Body.String(), `"active_count":1`) || strings.Contains(rec.Body.String(), "hidden") || strings.Contains(rec.Body.String(), "private-") {
			t.Fatalf("navigation %d %s", rec.Code, rec.Body.String())
		}
	}
	if rec := deploymentAuthCall(srv, "GET", "/user/deployment-repositories?repository_full_name=acme/hidden", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("hidden repository navigation: %d %s", rec.Code, rec.Body.String())
	}
	if rec := deploymentAuthCall(srv, "GET", "/user/deployment-repositories?limit=-1", ""); rec.Code != 400 {
		t.Fatalf("navigation limit: %d", rec.Code)
	}
	for _, suffix := range []string{"", "?revision=1", "/events", "/events?limit=1", "/events?after_id=1", "/events?before_id=2", "/revisions", "/revisions/1/events"} {
		rec := deploymentAuthCall(srv, "GET", "/user/deployments/shared-0"+suffix, "")
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != "no-store" || strings.Contains(rec.Body.String(), "private-") {
			t.Fatalf("history %s: %d %s", suffix, rec.Code, rec.Body.String())
		}
	}
	for _, id := range []string{"shared-2", "shared-3"} {
		for _, suffix := range []string{"", "/pull-request"} {
			if rec := deploymentAuthCall(srv, "GET", "/user/deployments/"+id+suffix, ""); rec.Code != 404 {
				t.Fatalf("wrong pair: %d", rec.Code)
			}
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("polling bypassed Jobs cache: %d calls", calls.Load())
	}
	for _, action := range []struct{ method, path string }{{"POST", "/user/deployments/shared-0/stop"}, {"DELETE", "/user/deployments/shared-1"}} {
		if rec := deploymentAuthCall(srv, action.method, action.path, ""); rec.Code != 200 {
			t.Fatalf("shared action %d %s", rec.Code, rec.Body.String())
		}
	}
	// The queued executor uses the submitting member, even if creator OAuth is absent.
	queued, err := store.GetDeployment("shared-0")
	if err != nil {
		t.Fatal(err)
	}
	queued.ExecutionAccountID = account.ID
	if _, err := srv.deploymentSource(context.Background(), queued); err != nil {
		t.Fatal(err)
	}
	revoked.Store(true)
	if _, err := srv.deploymentSource(context.Background(), queued); err == nil {
		t.Fatal("worker accepted revoked repository access")
	}
	srv.invalidateUserRepositoryAccess(account.ID)
	for _, path := range []string{"/user/deployment-repositories", "/user/deployments", "/user/deployments/shared-0", "/user/deployments/shared-0/events", "/user/deployments/shared-0/revisions", "/user/deployments/shared-0/pull-request"} {
		if rec := deploymentAuthCall(srv, "GET", path, ""); rec.Code != 403 || !strings.Contains(rec.Body.String(), "REAUTH_REQUIRED") {
			t.Fatalf("revoked OAuth %d %s", rec.Code, rec.Body.String())
		}
	}
	if err := store.DeleteAccountSecret(state.AccountScopeTypeAccount, account.ID, state.AccountSecretTypeGitHubOAuthToken); err != nil {
		t.Fatal(err)
	}
	srv.invalidateUserRepositoryAccess(account.ID)
	for _, path := range []string{"/user/deployment-repositories", "/user/deployments/shared-0"} {
		if rec := deploymentAuthCall(srv, "GET", path, ""); rec.Code != 403 {
			t.Fatalf("missing OAuth %d", rec.Code)
		}
	}
}

func TestRepositoryMemberCanDeployWithoutPrivateCatalogAccess(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}

		switch r.URL.Path {
		case "/user/memberships/orgs":
			fmt.Fprint(w, `[{"state":"active","role":"member","organization":{"id":600,"login":"acme"}}]`)
		case "/user/installations/987/repositories":
			fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"acme/project","owner":{"id":600,"login":"acme"}}]}`)
		case "/repos/acme/project/branches":
			fmt.Fprint(w, `[{"name":"main"}]`)
		case "/repos/acme/project/commits/refs/heads/main":
			fmt.Fprintf(w, `{"sha":%q}`, strings.Repeat("a", 40))
		case "/repos/acme/project/contents/.qiniu/deploy.yaml":
			fmt.Fprintf(w, `{"type":"file","encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte("version: 1\ninstall_command: npm ci\nstart_command: npm start\nport: 3000\n")))
		default:
			t.Errorf("unexpected GitHub request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, api.URL, &fakeSandbox{})
	srv.Close()
	srv.loopCtx = context.Background()
	srv.sandboxHTTP = deploymentTemplateTestClient()
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
	inst, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "organization", AccountLogin: "acme", GitHubAccountID: 600})
	if err != nil {
		t.Fatal(err)
	}
	key, err := encryptSecret("provider-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountPreference(state.AccountPreference{ScopeType: state.AccountScopeTypeGitHubInstall, ScopeID: 987, Namespace: accountPreferenceNamespaceSandbox, Key: accountPreferenceKeySandboxService, ValueJSON: `{"api_url":"https://us-south-1-sandbox.qiniuapi.com"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountSecret(state.AccountSecret{ScopeType: state.AccountScopeTypeGitHubInstall, ScopeID: 987, KeyType: state.AccountSecretTypeSandboxAPIKey, EncryptedValue: key}); err != nil {
		t.Fatal(err)
	}
	catalogPath := fmt.Sprintf("/user/deployment-templates?installation_id=%d", inst.ID)
	for _, repo := range []string{"", "acme/hidden"} {
		if rec := deploymentAuthCall(srv, "GET", catalogPath+"&repository_full_name="+repo, ""); rec.Code != 403 {
			t.Fatalf("ungated catalog %d", rec.Code)
		}
	}
	rec := deploymentAuthCall(srv, "GET", catalogPath+"&repository_full_name=acme/project", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"template_id":"public-base"`) {
		t.Fatalf("public choices %d %s", rec.Code, rec.Body.String())
	}
	for _, private := range []string{"custom", "deploy-template-a", "region", "provider-key"} {
		if strings.Contains(rec.Body.String(), private) {
			t.Fatalf("catalog leaked %s", private)
		}
	}
	if rec := deploymentAuthCall(srv, "GET", fmt.Sprintf("/user/deployment-refs?installation_id=%d&repository_full_name=acme/project&source=branch", inst.ID), ""); rec.Code != 200 {
		t.Fatalf("member refs %d %s", rec.Code, rec.Body.String())
	}
	payload := func(template string) string {
		return fmt.Sprintf(`{"installation_id":%d,"repository_full_name":"acme/project","source":"branch","ref":"main","template_id":%q}`, inst.ID, template)
	}
	for _, template := range []string{"deploy-template-a", "unbuilt"} {
		if rec := deploymentAuthCall(srv, "POST", "/user/deployments", payload(template)); rec.Code != 403 {
			t.Fatalf("non-public choice %d %s", rec.Code, rec.Body.String())
		}
	}
	rec = deploymentAuthCall(srv, "POST", "/user/deployments", payload("public-base"))
	if rec.Code != 201 {
		t.Fatalf("member create %d %s", rec.Code, rec.Body.String())
	}
	var created deploymentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	d, err := store.GetDeployment(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if d.ExecutionAccountID != account.ID || d.ScopeType != state.AccountScopeTypeGitHubInstall || d.ScopeID != 987 {
		t.Fatal("incorrect execution scope")
	}
	// Reuse an immutable revision created by a different account.
	d.AccountID = account.ID + 10
	d.ExecutionAccountID = d.AccountID
	d.ID = "other-creator"
	d.Status = state.DeploymentStatusCompleted
	d, err = store.CreateDeployment(d)
	if err != nil {
		t.Fatal(err)
	}
	rec = deploymentAuthCall(srv, "POST", "/user/deployments/"+d.ID+"/retry", `{"mode":"revision","revision_number":1}`)
	if rec.Code != 202 {
		t.Fatalf("shared retry %d %s", rec.Code, rec.Body.String())
	}
	saved, err := store.GetDeployment(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AccountID != d.AccountID || saved.ExecutionAccountID != account.ID || saved.RevisionNumber != 2 {
		t.Fatal("retry replaced creator or retained wrong execution actor")
	}
}
