package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestDeploymentID(t *testing.T) {
	id := newDeploymentID(1, "request-1")
	if replay := newDeploymentID(1, "request-1"); replay != id {
		t.Fatalf("idempotent request changed ID: %q != %q", replay, id)
	}
	seen := make(map[string]bool)
	for _, candidate := range []string{
		id,
		newDeploymentID(2, "request-1"),
		newDeploymentID(1, "request-2"),
		newDeploymentID(1, ""),
		newDeploymentID(1, ""),
	} {
		assertCompactDeploymentID(t, candidate)
		if seen[candidate] {
			t.Fatalf("different accounts, keys, or unkeyed requests share ID %q", candidate)
		}
		seen[candidate] = true
	}
}

func assertCompactDeploymentID(t *testing.T, id string) {
	t.Helper()
	if !regexp.MustCompile(`^[a-z2-7]{26}$`).MatchString(id) {
		t.Fatalf("deployment ID must be 26 lowercase Base32 characters: %q", id)
	}
}

func TestUserCreateDeploymentPinsCommitAndAuditsPlan(t *testing.T) {
	for _, source := range []string{"branch", "tag"} {
		t.Run(source, func(t *testing.T) { testUserCreateDeploymentPinsCommitAndAuditsPlan(t, source) })
	}
}

func testUserCreateDeploymentPinsCommitAndAuditsPlan(t *testing.T, source string) {
	const commit = "0123456789abcdef0123456789abcdef01234567"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}

		switch {
		case r.URL.Path == "/user/installations/987/repositories":
			if r.Header.Get("Authorization") != "Bearer user-token" {
				t.Fatalf("repository access authorization = %q", r.Header.Get("Authorization"))
			}
			_, _ = fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"hubot/project","owner":{"login":"hubot"}},{"id":43,"full_name":"hubot/no-contents","owner":{"login":"hubot"}},{"id":44,"full_name":"hubot/rate-limited","owner":{"login":"hubot"}}]}`)
		case r.URL.Path == "/repos/hubot/no-contents/branches":
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"Resource not accessible by integration"}`)
		case r.URL.Path == "/repos/hubot/rate-limited/branches":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
		case r.URL.Path == "/repos/hubot/project/branches":
			_, _ = fmt.Fprint(w, `[{"name":"main"},{"name":"feature/test"}]`)
		case r.URL.Path == "/repos/hubot/project/tags":
			_, _ = fmt.Fprint(w, `[{"name":"v1.0"}]`)
		case r.URL.Path == "/repos/hubot/project/commits/main" || r.URL.Path == "/repos/hubot/project/commits/refs/heads/main" || r.URL.Path == "/repos/hubot/project/commits/refs/tags/main":
			_, _ = fmt.Fprintf(w, `{"sha":%q}`, commit)
		case r.URL.Path == "/repos/hubot/project/contents/.qiniu/deploy.yaml":
			config := "version: 1\ninstall_command: npm ci\nstart_command: npm start\nport: 3000\n"
			_, _ = fmt.Fprintf(w, `{"type":"file","encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte(config)))
		default:
			http.NotFound(w, r)
		}
	}))
	defer api.Close()

	store := state.New(t.TempDir())
	srv := newTestServer(t, store, api.URL, &fakeSandbox{})
	srv.Close()
	srv.loopCtx = context.Background() // Admission tests do not run provider execution in the background.
	srv.sandboxHTTP = deploymentTemplateTestClient()
	srv.cfg.DeploymentMaxConcurrent = 1
	if srv.deploymentWorker == nil {
		t.Fatal("worker must be available without a configured default template")
	}
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
	key, err := encryptSecret("provider-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountPreference(state.AccountPreference{
		ScopeType: state.AccountScopeTypeAccount, ScopeID: account.ID,
		Namespace: accountPreferenceNamespaceSandbox, Key: accountPreferenceKeySandboxService,
		ValueJSON: `{"api_url":"https://us-south-1-sandbox.qiniuapi.com"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountSecret(state.AccountSecret{
		ScopeType: state.AccountScopeTypeAccount, ScopeID: account.ID,
		KeyType: state.AccountSecretTypeSandboxAPIKey, EncryptedValue: key,
	}); err != nil {
		t.Fatal(err)
	}
	installation, err := store.UpsertGitHubInstallation(state.GitHubInstallation{
		AccountID: account.ID, InstallationID: 987, AccountType: "user", AccountLogin: "hubot",
	})
	if err != nil {
		t.Fatal(err)
	}

	payload := fmt.Sprintf(`{"installation_id":%d,"repository_full_name":"hubot/project","ref":"main","source":%q,"template_id":"deploy-template-a","template_name":"base","template_cpu":4,"template_memory_mb":8192,"template_disk_mb":40960}`, installation.ID, source)
	req := httptest.NewRequest(http.MethodPost, "/user/deployments", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "deploy-1")
	req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("create Cache-Control=%q", rec.Header().Get("Cache-Control"))
	}
	var created deploymentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	assertCompactDeploymentID(t, created.ID)
	if created.CommitSHA != commit || created.Status != state.DeploymentStatusQueued || created.RepositoryID != 42 || created.SpecHash == "" {
		t.Fatalf("created deployment=%#v", created)
	}
	if strings.Contains(rec.Body.String(), "npm ci") || strings.Contains(rec.Body.String(), "npm start") {
		t.Fatalf("deployment response leaked command: %s", rec.Body.String())
	}
	snapshot, err := store.GetDeployment(created.ID)
	wantRef := "refs/heads/main"
	if source == "tag" {
		wantRef = "refs/tags/main"
	}
	if err != nil || snapshot.DeploymentTemplateID != "deploy-template-a" || snapshot.DeploymentTemplateName != "base" || snapshot.DeploymentTemplateCPU != 4 || snapshot.DeploymentTemplateMemoryMB != 8192 || snapshot.DeploymentTemplateDiskMB != 40960 || created.TemplateName != "base" || created.TemplateCPU != 4 || created.TemplateMemoryMB != 8192 || created.TemplateDiskMB != 40960 || snapshot.Ref != wantRef || created.Source != source || created.Ref != "main" || created.SandboxRegion != "us-south-1" {
		t.Fatalf("target snapshot = %#v response=%#v err=%v", snapshot, created, err)
	}
	if created.TemplateID != "deploy-template-a" || len(srv.deploymentNotify) != 1 {
		t.Fatalf("template response or queue notification missing: %#v", created)
	}
	for _, tc := range []struct {
		template, key, code string
		status              int
	}{
		{"", "missing-template", "deployment_template_required", http.StatusBadRequest},
		{"missing", "unavailable-template", "deployment_template_unavailable", http.StatusConflict},
		{"deploy-template-b", "deploy-1", "deployment_request_conflict", http.StatusConflict},
	} {
		invalid := httptest.NewRequest(http.MethodPost, "/user/deployments", strings.NewReader(fmt.Sprintf(`{"installation_id":%d,"repository_full_name":"hubot/project","ref":"main","source":%q,"template_id":%q}`, installation.ID, source, tc.template)))
		invalid.Header.Set("Idempotency-Key", tc.key)
		invalid.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		rejected := httptest.NewRecorder()
		srv.ServeHTTP(rejected, invalid)
		if rejected.Code != tc.status || !strings.Contains(rejected.Body.String(), tc.code) {
			t.Fatalf("invalid template %q: %d %s", tc.template, rejected.Code, rejected.Body.String())
		}
	}
	otherSource := "tag"
	if source == "tag" {
		otherSource = "branch"
	}
	conflict := httptest.NewRequest(http.MethodPost, "/user/deployments", strings.NewReader(strings.Replace(payload, `"source":"`+source+`"`, `"source":"`+otherSource+`"`, 1)))
	conflict.Header.Set("Idempotency-Key", "deploy-1")
	conflict.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	conflictRec := httptest.NewRecorder()
	srv.ServeHTTP(conflictRec, conflict)
	if conflictRec.Code != http.StatusConflict || !strings.Contains(conflictRec.Body.String(), "deployment_request_conflict") {
		t.Fatalf("branch/tag replay conflict = %d %s", conflictRec.Code, conflictRec.Body.String())
	}
	catalogReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/user/deployment-templates?installation_id=%d", installation.ID), nil)
	catalogReq.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	catalogRec := httptest.NewRecorder()
	srv.ServeHTTP(catalogRec, catalogReq)
	if catalogRec.Code != http.StatusOK || !strings.Contains(catalogRec.Body.String(), "public-base") || !strings.Contains(catalogRec.Body.String(), "custom") || strings.Contains(catalogRec.Body.String(), "unbuilt") || strings.Contains(catalogRec.Body.String(), "provider-key") {
		t.Fatalf("scoped template catalog=%d %s", catalogRec.Code, catalogRec.Body.String())
	}
	for _, tc := range []struct {
		repo, source, want string
		status             int
	}{
		{"hubot/project", "branch", "feature/test", http.StatusOK},
		{"hubot/project", "tag", "v1.0", http.StatusOK},
		{"hubot/hidden", "branch", "repository_access_forbidden", http.StatusForbidden},
		{"hubot/no-contents", "branch", "deployment_repository_contents_required", http.StatusForbidden},
		{"hubot/rate-limited", "branch", "deployment_refs_unavailable", http.StatusBadGateway},
		{"hubot/project", "commit", "invalid deployment ref request", http.StatusBadRequest},
	} {
		refsReq := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/user/deployment-refs?installation_id=%d&repository_full_name=%s&source=%s", installation.ID, tc.repo, tc.source), nil)
		refsReq.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		refsRec := httptest.NewRecorder()
		srv.ServeHTTP(refsRec, refsReq)
		if refsRec.Code != tc.status || !strings.Contains(refsRec.Body.String(), tc.want) {
			t.Fatalf("ref discovery %s/%s: %d %s", tc.repo, tc.source, refsRec.Code, refsRec.Body.String())
		}
		if strings.Contains(refsRec.Body.String(), "Resource not accessible by integration") || strings.Contains(refsRec.Body.String(), "API rate limit exceeded") {
			t.Fatalf("ref discovery leaked upstream error: %s", refsRec.Body.String())
		}
	}
	srv.cfg.DeploymentTemplateID = "deploy-template-b"

	replay := httptest.NewRequest(http.MethodPost, "/user/deployments", strings.NewReader(payload))
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("Idempotency-Key", "deploy-1")
	replay.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	replayRec := httptest.NewRecorder()
	srv.ServeHTTP(replayRec, replay)
	if replayRec.Code != http.StatusOK || !strings.Contains(replayRec.Body.String(), `"id":"`+created.ID+`"`) {
		t.Fatalf("replay status=%d body=%s", replayRec.Code, replayRec.Body.String())
	}
	if got, err := store.GetDeployment(created.ID); err != nil || got.DeploymentTemplateID != "deploy-template-a" {
		t.Fatalf("replay changed target binding: %#v %v", got, err)
	}
	quota := httptest.NewRequest(http.MethodPost, "/user/deployments", strings.NewReader(`{"installation_id":`+fmt.Sprint(installation.ID)+`,"repository_full_name":"hubot/project","ref":"main"}`))
	quota.Header.Set("Content-Type", "application/json")
	quota.Header.Set("Idempotency-Key", "deploy-2")
	quota.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	quotaRec := httptest.NewRecorder()
	srv.ServeHTTP(quotaRec, quota)
	if quotaRec.Code != http.StatusTooManyRequests || !strings.Contains(quotaRec.Body.String(), "deployment_quota_exceeded") {
		t.Fatalf("quota status=%d body=%s", quotaRec.Code, quotaRec.Body.String())
	}
	list := httptest.NewRequest(http.MethodGet, "/user/deployments", nil)
	list.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	listRec := httptest.NewRecorder()
	srv.ServeHTTP(listRec, list)
	if listRec.Code != http.StatusOK || !strings.Contains(listRec.Body.String(), created.ID) {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	for _, query := range []string{"", "?after_id=1", "?before_id=2", "?before_id=1&after_id=1"} {
		eventReq := httptest.NewRequest(http.MethodGet, "/user/deployments/"+created.ID+"/events"+query, nil)
		eventReq.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		eventRec := httptest.NewRecorder()
		srv.ServeHTTP(eventRec, eventReq)
		want := http.StatusOK
		if strings.Contains(query, "&") {
			want = http.StatusBadRequest
		}
		if eventRec.Code != want {
			t.Fatalf("event cursor %s: %d %s", query, eventRec.Code, eventRec.Body.String())
		}
	}
	stop := httptest.NewRequest(http.MethodDelete, "/user/deployments/"+created.ID, nil)
	stop.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	stopRec := httptest.NewRecorder()
	srv.ServeHTTP(stopRec, stop)
	if stopRec.Code != http.StatusOK || !strings.Contains(stopRec.Body.String(), `"status":"completed"`) {
		t.Fatalf("stop status=%d body=%s", stopRec.Code, stopRec.Body.String())
	}
	retry := httptest.NewRequest(http.MethodPost, "/user/deployments/"+created.ID+"/retry", nil)
	retry.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
	retryRec := httptest.NewRecorder()
	srv.ServeHTTP(retryRec, retry)
	if retryRec.Code != http.StatusAccepted || !strings.Contains(retryRec.Body.String(), `"status":"queued"`) {
		t.Fatalf("retry status=%d body=%s", retryRec.Code, retryRec.Body.String())
	}

	events, err := store.ListAuditEvents(10)
	if err != nil || len(events) != 3 || events[0].Action != "deployment.retry" {
		t.Fatalf("deployment audit events=%#v err=%v", events, err)
	}

	// API clients may omit the key; each submission creates a compact new ID.
	srv.cfg.DeploymentMaxConcurrent = 3
	previousID := created.ID
	for range 2 {
		unkeyed := httptest.NewRequest(http.MethodPost, "/user/deployments", strings.NewReader(payload))
		unkeyed.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		response := httptest.NewRecorder()
		srv.ServeHTTP(response, unkeyed)
		if response.Code != http.StatusCreated {
			t.Fatalf("unkeyed create: %d %s", response.Code, response.Body.String())
		}
		var deployment deploymentResponse
		if err := json.Unmarshal(response.Body.Bytes(), &deployment); err != nil {
			t.Fatal(err)
		}
		assertCompactDeploymentID(t, deployment.ID)
		if deployment.ID == created.ID || deployment.ID == previousID {
			t.Fatalf("unkeyed submission reused ID %q", deployment.ID)
		}
		previousID = deployment.ID
	}
}

type deploymentTemplateTransport struct{}

func (deploymentTemplateTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{"templateID":"deploy-template-a","isOwner":true,"public":false}`
	if strings.HasSuffix(r.URL.Path, "/public-base") {
		body = `{"templateID":"public-base","public":true}`
	} else if r.URL.Path == "/templates" {
		body = `[{"templateID":"deploy-template-a","buildID":"build-1","aliases":["custom"]},{"templateID":"deploy-template-b","buildID":"build-2"}]`
	} else if strings.HasSuffix(r.URL.Path, "/deploy-template-b") {
		body = `{"templateID":"deploy-template-b","isOwner":true,"public":false}`
	} else if r.URL.Path == "/default-templates" {
		body = `[{"templateID":"public-base","buildID":"build-public","names":["base"],"public":true},{"templateID":"unbuilt","buildID":"00000000-0000-0000-0000-000000000000","public":true}]`
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

func deploymentTemplateTestClient() *http.Client {
	return &http.Client{Transport: deploymentTemplateTransport{}}
}

func TestUserDeploymentRefsRejectUnavailableAuthorization(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}

		if r.URL.Path != "/user/installations/987/repositories" {
			t.Errorf("ref discovery must not run after rejected OAuth: %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer api.Close()
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, api.URL, &fakeSandbox{})
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "expired-token")
	installation, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "user", AccountLogin: "hubot"})
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name     string
		id       int64
		signedIn bool
		status   int
	}{
		{"signed out", installation.ID, false, http.StatusUnauthorized},
		{"unlinked installation", installation.ID + 1, true, http.StatusNotFound},
		{"rejected OAuth", installation.ID, true, http.StatusForbidden},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/user/deployment-refs?installation_id=%d&repository_full_name=hubot/project&source=tag", scenario.id), nil)
			if scenario.signedIn {
				req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != scenario.status || strings.Contains(rec.Body.String(), `"refs"`) {
				t.Fatalf("ref discovery: %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestDeploymentRefLengthIncludesSourcePrefix(t *testing.T) {
	for _, tc := range []struct {
		source string
		length int
		valid  bool
	}{
		{"branch", 245, true},
		{"branch", 246, false},
		{"tag", 246, true},
		{"tag", 247, false},
		{"", 256, true},
		{"", 257, false},
	} {
		input := createDeploymentRequest{InstallationID: 1, RepositoryFullName: "owner/repo", Source: tc.source, Ref: strings.Repeat("a", tc.length)}
		if err := validateCreateDeploymentRequest(input); (err == nil) != tc.valid {
			t.Errorf("source=%q length=%d valid=%t err=%v", tc.source, tc.length, tc.valid, err)
		}
	}
}

func TestDeploymentResponseDeclaresPublicEndpointAccess(t *testing.T) {
	response := deploymentResponseFromState(state.Deployment{ID: "deployment", Endpoint: "https://sandbox.example.test"})
	if response.EndpointAccess != "public" {
		t.Fatalf("endpoint access = %q, want public", response.EndpointAccess)
	}
}

func TestDeploymentReadsAndMutationsRejectRevokedRepositoryAccess(t *testing.T) {
	for _, scenario := range []string{"different account", "repository revoked", "organization member"} {
		t.Run(scenario, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveActiveInstallationFixture(w, r, 987) {
					return
				}

				switch r.URL.Path {
				case "/user/memberships/orgs":
					fmt.Fprint(w, `[{"state":"active","role":"member","organization":{"id":600,"login":"acme"}}]`)
				case "/user/installations/987/repositories":
					fmt.Fprint(w, `{"repositories":[]}`)
				default:
					t.Errorf("unexpected upstream request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer api.Close()
			store := state.New(t.TempDir())
			srv := newTestServer(t, store, api.URL, &fakeSandbox{})
			account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
			if err != nil {
				t.Fatal(err)
			}
			saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
			inst := state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "user", AccountLogin: "hubot"}
			if scenario == "organization member" {
				inst.AccountType, inst.AccountLogin, inst.GitHubAccountID = "organization", "acme", 600
			}
			if _, err := store.UpsertGitHubInstallation(inst); err != nil {
				t.Fatal(err)
			}
			fixture := state.Deployment{ID: "private-deploy", AccountID: account.ID, ScopeType: state.AccountScopeTypeAccount, ScopeID: account.ID, GitHubInstallationID: 987, RepositoryID: 42, RepositoryFullName: "hubot/project", Ref: "main", CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "encrypted", ExpiresAt: time.Now().Add(time.Hour)}
			if scenario == "different account" {
				fixture.AccountID++
			}
			if scenario == "organization member" {
				fixture.ScopeType, fixture.ScopeID, fixture.RepositoryFullName = state.AccountScopeTypeGitHubInstall, 987, "acme/project"
			}
			created, err := store.CreateDeployment(fixture)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.AppendDeploymentEvent(state.DeploymentEvent{DeploymentID: fixture.ID, EventType: "output", Message: "private-output"}); err != nil {
				t.Fatal(err)
			}
			for _, action := range []struct{ method, path string }{
				{"GET", ""}, {"GET", "/events"}, {"GET", "/revisions"}, {"GET", "/revisions/1/events"}, {"POST", "/stop"}, {"POST", "/retry"}, {"DELETE", ""}, {"POST", "/revisions/1/terminal"}, {"GET", "/revisions/1/terminal/other/events"}, {"POST", "/revisions/1/terminal/other/input"}, {"POST", "/revisions/1/terminal/other/resize"},
			} {
				req := httptest.NewRequest(action.method, "/user/deployments/"+fixture.ID+action.path, nil)
				req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, req)
				want := http.StatusNotFound
				if rec.Code != want || (want != http.StatusOK && strings.Contains(rec.Body.String(), "private-output")) {
					t.Fatalf("%s %s: %d %s", action.method, action.path, rec.Code, rec.Body.String())
				}
			}
			got, err := store.GetDeployment(fixture.ID)
			if err != nil || got.Version != created.Version || got.Status != created.Status {
				t.Fatalf("unauthorized mutation: %#v %v", got, err)
			}
			audit, err := store.ListAuditEvents(10)
			if err != nil || len(audit) != 0 {
				t.Fatalf("unauthorized audit: %#v %v", audit, err)
			}
		})
	}
}

func TestDeployAdminFallbackRequiresExplicitOptIn(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "", &fakeSandbox{})
	encrypted, err := encryptSecret("fixture-provider-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertSandboxServiceDefault(state.SandboxServiceDefault{Enabled: true, APIURL: "https://provider.example", APIKeyEncrypted: encrypted}); err != nil {
		t.Fatal(err)
	}
	scope := accountPreferenceScope{Type: state.AccountScopeTypeAccount, ID: 1}
	if service, _, err := srv.sandboxServiceForDeployment(context.Background(), scope); service != nil || !errors.Is(err, errSandboxServiceNotConfigured) {
		t.Fatalf("implicit Deploy fallback = %T %v", service, err)
	}
	srv.cfg.DeploymentAllowAdminFallback = true
	if service, snapshot, err := srv.sandboxServiceForDeployment(context.Background(), scope); err != nil || service == nil || snapshot.Source != sandboxConfigSourceAdminDefault {
		t.Fatalf("explicit Deploy fallback = %T %#v %v", service, snapshot, err)
	}
}

func TestDeploymentReplayUsesImmutableRequestIdentity(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "", &fakeSandbox{})
	srv.Close()
	input := createDeploymentRequest{Ref: "refs/heads/main", Source: "branch", ConfigPath: ".qiniu/deploy.yaml", TemplateID: "base", TTLSeconds: 600}
	original := deploymentCreateRequestHash(input, 987, 42)
	now := time.Now().UTC()
	d, err := store.CreateDeployment(state.Deployment{ID: "replay-identity", AccountID: 1, CreateRequestHash: original, ScopeType: state.AccountScopeTypeAccount, ScopeID: 1, GitHubInstallationID: 987, RepositoryID: 42, RepositoryFullName: "hubot/project", Ref: input.Ref, CommitSHA: strings.Repeat("a", 40), ConfigPath: input.ConfigPath, SnapshotVersion: 1, SpecHash: "hash", SpecJSONEncrypted: "encrypted", DeploymentTemplateID: "base", Status: state.DeploymentStatusCompleted, CreatedAt: now, ExpiresAt: now.Add(600*time.Second - 593*time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	check := func(input createDeploymentRequest, want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		if !srv.writeDeploymentReplay(rec, d.ID, 1, 987, 42, input) || rec.Code != want {
			t.Fatalf("replay=%d %s", rec.Code, rec.Body.String())
		}
	}
	check(input, 200)
	changed := input
	changed.TTLSeconds++
	check(changed, 409)
	changed = input
	changed.Ref = "refs/tags/main"
	changed.Source = "tag"
	check(changed, 409)
	changed = input
	changed.TemplateID = "another"
	check(changed, 409)
	// A later revision can have a new source, template and expiry. Repeating the
	// original creation request returns that resource, never launches a revision.
	d.CommitSHA = strings.Repeat("b", 40)
	d.Ref = "refs/heads/next"
	d.DeploymentTemplateID = "another"
	if err := store.RetryDeployment(d, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	check(input, 200)
	saved, err := store.GetDeployment(d.ID)
	if err != nil || saved.CreateRequestHash != original || saved.RevisionNumber != 2 {
		t.Fatal("replay changed the resource or lost original identity")
	}
	old := d
	old.ID = "historical-replay"
	old.CreateRequestHash = ""
	if _, err := store.CreateDeployment(old); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	if !srv.writeDeploymentReplay(rec, old.ID, 1, 987, 42, input) || rec.Code != 409 {
		t.Fatal("historical request identity was inferred from mutable fields")
	}
	defaultInput := createDeploymentRequest{Ref: "refs/heads/main", Source: "branch"}
	explicit := defaultInput
	explicit.Ref = "refs/heads/main"
	explicit.ConfigPath = ".qiniu/deploy.yaml"
	explicit.TTLSeconds = 3600
	if deploymentCreateRequestHash(defaultInput, 987, 42) != deploymentCreateRequestHash(explicit, 987, 42) {
		t.Fatal("equivalent default request fields have different identities")
	}
}
