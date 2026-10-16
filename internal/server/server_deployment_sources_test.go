package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestPullRequestDeploymentPinsSourceAndRejectsInvalidContext(t *testing.T) {
	const head = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const old = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	baseID, headID := int64(42), int64(43)
	currentHead := head
	expectedConfigSHA := old
	pullReads, contentsReads := 0, 0
	allowAccess := true
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveActiveInstallationFixture(w, r, 987) {
			return
		}

		switch r.URL.Path {
		case "/user/installations/987/repositories":
			if !allowAccess {
				fmt.Fprint(w, `{"repositories":[]}`)
				return
			}
			fmt.Fprint(w, `{"repositories":[{"id":42,"full_name":"hubot/project","owner":{"login":"hubot"}}]}`)
		case "/repos/hubot/project/pulls":
			fmt.Fprint(w, `[{"number":7,"title":"Preview"}]`)
		case "/repos/hubot/project/pulls/7":
			pullReads++
			fmt.Fprintf(w, `{"number":7,"title":"Preview deployment","base":{"repo":{"id":%d,"full_name":"hubot/project"}},"head":{"sha":%q,"ref":"feature/fork","repo":{"id":%d,"full_name":"contributor/fork"}}}`, baseID, currentHead, headID)
		case "/repos/hubot/project/commits/refs/pull/7/head":
			fmt.Fprintf(w, `{"sha":%q}`, head)
		case "/repos/hubot/project/pulls/7/commits":
			fmt.Fprintf(w, `[{"sha":%q},{"sha":%q}]`, old, head)
		case "/repos/hubot/project/contents/.qiniu/deploy.yaml":
			contentsReads++
			if r.URL.Query().Get("ref") != expectedConfigSHA {
				t.Errorf("config must use pinned SHA: %s", r.URL.RawQuery)
			}
			fmt.Fprintf(w, `{"type":"file","encoding":"base64","content":%q}`, base64.StdEncoding.EncodeToString([]byte("version: 1\ninstall_command: npm ci\nstart_command: npm start\nport: 3000\n")))
		default:
			t.Errorf("unexpected repository access %s", r.URL.Path)
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
	key, err := encryptSecret("sandbox-test-key", srv.cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountPreference(state.AccountPreference{ScopeType: state.AccountScopeTypeAccount, ScopeID: account.ID, Namespace: accountPreferenceNamespaceSandbox, Key: accountPreferenceKeySandboxService, ValueJSON: `{"api_url":"https://us-south-1-sandbox.qiniuapi.com"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertAccountSecret(state.AccountSecret{ScopeType: state.AccountScopeTypeAccount, ScopeID: account.ID, KeyType: state.AccountSecretTypeSandboxAPIKey, EncryptedValue: key}); err != nil {
		t.Fatal(err)
	}
	installation, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountType: "user", AccountLogin: "hubot"})
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body, key string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		req.Header.Set("Idempotency-Key", key)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	payload := fmt.Sprintf(`{"installation_id":%d,"repository_full_name":"hubot/project","source":"pull_request","ref":"7","commit_sha":%q,"template_id":"deploy-template-a"}`, installation.ID, old)
	refs := call("GET", fmt.Sprintf("/user/deployment-refs?installation_id=%d&repository_full_name=hubot/project&source=pull_request", installation.ID), "", "")
	if refs.Code != 200 || !strings.Contains(refs.Body.String(), `"7"`) {
		t.Fatalf("refs %d %s", refs.Code, refs.Body.String())
	}
	rec := call("POST", "/user/deployments", payload, "pr-create")
	if rec.Code != 201 {
		t.Fatalf("create %d %s", rec.Code, rec.Body.String())
	}
	var created deploymentResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Source != "pull_request" || created.PullRequestNumber != 7 || created.PullRequestTitle != "Preview deployment" || created.CommitSHA != old || created.SourceRepositoryID != 43 || created.SourceRepositoryFullName != "contributor/fork" || created.SourceRef != "feature/fork" {
		t.Fatalf("missing trusted source: %#v", created)
	}
	first, err := store.GetDeploymentRevision(created.ID, 1)
	if err != nil || first.SourceRepositoryID != 43 || first.PullRequestTitle != "Preview deployment" || first.CommitSHA != old {
		t.Fatalf("revision %#v %v", first, err)
	}
	previousReads := pullReads
	if replay := call("POST", "/user/deployments", payload, "pr-create"); replay.Code != 200 || pullReads != previousReads {
		t.Fatalf("moving PR replay: %d", replay.Code)
	}
	// Old deployments render local details immediately, then fill missing titles
	// through the separately authorized metadata endpoint.
	legacy, err := store.GetDeployment(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	legacy.ID, legacy.PullRequestTitle = "legacy-pr-title", ""
	if _, err := store.CreateDeployment(legacy); err != nil {
		t.Fatal(err)
	}
	previousReads = pullReads
	if detail := call("GET", "/user/deployments/legacy-pr-title", "", ""); detail.Code != 200 || pullReads != previousReads {
		t.Fatalf("local detail fetched GitHub: %d %s", detail.Code, detail.Body.String())
	}
	for range 2 {
		meta := call("GET", "/user/deployments/legacy-pr-title/pull-request", "", "")
		if meta.Code != 200 || !strings.Contains(meta.Body.String(), `"pull_request_title":"Preview deployment"`) || meta.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("legacy title: %d %s", meta.Code, meta.Body.String())
		}
	}
	if pullReads != previousReads+1 {
		t.Fatalf("metadata repeated GitHub reads: before=%d after=%d", previousReads, pullReads)
	}
	legacyRevision, err := store.GetDeploymentRevision(legacy.ID, 1)
	if err != nil || legacyRevision.PullRequestTitle != "Preview deployment" {
		t.Fatalf("missing repaired revision title: %#v %v", legacyRevision, err)
	}
	for _, scenario := range []string{"base mismatch", "deleted fork", "moved head", "unrelated commit", "revoked"} {
		body := payload
		switch scenario {
		case "base mismatch":
			baseID = 99
		case "deleted fork":
			headID = 0
		case "moved head":
			currentHead = old
		case "unrelated commit":
			body = strings.ReplaceAll(payload, old, strings.Repeat("c", 40))
		case "revoked":
			allowAccess = false
		}
		before := contentsReads
		rejected := call("POST", "/user/deployments", body, scenario)
		if rejected.Code < 400 || contentsReads != before {
			t.Fatalf("%s: %d %s", scenario, rejected.Code, rejected.Body.String())
		}
		baseID, headID, currentHead, allowAccess = 42, 43, head, true
	}
	saved, err := store.GetDeployment(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved.Status = state.DeploymentStatusCompleted
	if err := store.UpdateDeployment(saved); err != nil {
		t.Fatal(err)
	}
	retry := call("POST", "/user/deployments/"+created.ID+"/retry", `{"mode":"revision","revision_number":1}`, "")
	if retry.Code != 202 {
		t.Fatalf("retry %d %s", retry.Code, retry.Body.String())
	}
	second, err := store.GetDeploymentRevision(created.ID, 2)
	if err != nil || second.SourceRepositoryID != 43 || second.CommitSHA != old || !second.ExpiresAt.After(time.Now()) {
		t.Fatalf("retry snapshot %#v %v", second, err)
	}

	saved, err = store.GetDeployment(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved.Status = state.DeploymentStatusCompleted
	if err := store.UpdateDeployment(saved); err != nil {
		t.Fatal(err)
	}
	expectedConfigSHA = head
	latest := call("POST", "/user/deployments/"+created.ID+"/retry", `{"mode":"latest"}`, "")
	if latest.Code != 202 {
		t.Fatalf("latest PR %d %s", latest.Code, latest.Body.String())
	}
	third, err := store.GetDeploymentRevision(created.ID, 3)
	if err != nil || third.CommitSHA != head || third.SourceRepositoryID != 43 {
		t.Fatalf("latest snapshot %#v %v", third, err)
	}
	history := call("GET", "/user/deployments?repository_full_name=hubot/project&pull_request_number=7", "", "")
	if history.Code != 200 || !strings.Contains(history.Body.String(), created.ID) {
		t.Fatalf("PR history %d %s", history.Code, history.Body.String())
	}
}
