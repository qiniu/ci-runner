package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestRunnerGroupLookupErrorContract(t *testing.T) {
	for _, tc := range []struct {
		name, code             string
		upstream, status       int
		rateLimited, retryable bool
	}{
		{"permission", "runner_group_permission_denied", 403, 502, false, false},
		{"not accessible", "runner_group_permission_denied", 404, 502, false, false},
		{"rate limit", "runner_group_rate_limited", 403, 503, true, true},
		{"429 without headers", "runner_group_rate_limited", 429, 503, false, true},
		{"unavailable", "runner_group_unavailable", 500, 502, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, _, _ := runnerGroupTestServer(t, "admin", func(w http.ResponseWriter, r *http.Request) {
				if tc.rateLimited {
					w.Header().Set("Retry-After", "30")
				}
				w.WriteHeader(tc.upstream)
				fmt.Fprint(w, "secret-provider-body")
			})
			rec := runnerGroupUserRequest(srv, http.MethodGet, "/user/github-runner-groups?installation_id=1", "", "hubot-id")
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), tc.code) || strings.Contains(rec.Body.String(), "secret-provider") {
				t.Fatalf("unexpected error %d %s", rec.Code, rec.Body.String())
			}
			err := srv.gh.ValidateRunnerGroup(t.Context(), 987, "octo-org/repo", "Build")
			code, retryable := classifyRetryableError("github_runner_group", newRunnerGroupValidationFailure(err))
			if code != tc.code || retryable != tc.retryable {
				t.Fatalf("retry classification = %s %v", code, retryable)
			}
		})
	}
	status, code, _ := runnerGroupError(context.DeadlineExceeded)
	if status != 504 || code != "runner_group_timeout" {
		t.Fatalf("timeout = %d %s", status, code)
	}
	code, retryable := classifyRetryableError("github_runner_group", newRunnerGroupValidationFailure(context.DeadlineExceeded))
	if code != "runner_group_timeout" || !retryable {
		t.Fatalf("timeout retry = %s %v", code, retryable)
	}
}

func runnerGroupTestServer(t *testing.T, membership string, handler http.HandlerFunc) (*Server, state.Store, state.RunnerProfileScope) {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/user/memberships/orgs" {
			fmt.Fprintf(w, `[{"state":"active","role":%q,"organization":{"id":9001,"login":"octo-org"}}]`, membership)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(api.Close)
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, api.URL, &fakeSandbox{})
	for _, subject := range []string{"hubot-id", "12345"} {
		account, _, err := store.GetAccountByOAuthIdentity("github", subject)
		if err != nil {
			t.Fatal(err)
		}
		saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
		if _, err = store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, GitHubAccountID: 9001, AccountType: "organization", AccountLogin: "octo-org"}); err != nil {
			t.Fatal(err)
		}
	}
	return srv, store, state.RunnerProfileScope{Type: state.AccountScopeTypeGitHubInstall, ID: 987}
}

func runnerGroupUserRequest(srv *Server, method, target, body, subject string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if subject != "" {
		req.AddCookie(testSessionCookie(subject, "viewer", "user"))
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func TestUserGitHubRunnerGroupsAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, role, subject, target string
		status                      int
	}{
		{"anonymous", "admin", "", "/user/github-runner-groups?installation_id=1", 401},
		{"account", "admin", "hubot-id", "/user/github-runner-groups", 400},
		{"member", "member", "hubot-id", "/user/github-runner-groups?installation_id=1", 403},
		{"local admin is not org owner", "member", "12345", "/user/github-runner-groups?installation_id=2", 403},
		{"unknown installation", "admin", "hubot-id", "/user/github-runner-groups?installation_id=999", 400},
		{"owner", "admin", "hubot-id", "/user/github-runner-groups?installation_id=1", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			srv, _, _ := runnerGroupTestServer(t, tc.role, func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/orgs/octo-org/actions/runner-groups" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				fmt.Fprint(w, `{"total_count":2,"runner_groups":[{"id":1,"name":"Build","restricted_to_workflows":true},{"id":2,"name":"Enterprise","inherited":true}]}`)
			})
			rec := runnerGroupUserRequest(srv, http.MethodGet, tc.target, "", tc.subject)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tc.status == 200 {
				if !strings.Contains(rec.Body.String(), `"organization":"octo-org"`) || strings.Contains(rec.Body.String(), "Enterprise") || rec.Header().Get("Cache-Control") != "no-store" {
					t.Fatalf("unexpected list %s", rec.Body.String())
				}
			} else if calls.Load() != 0 {
				t.Fatalf("unauthorized group reads=%d", calls.Load())
			}
		})
	}
}

func TestUserGitHubRunnerGroupValidationAndCompatibility(t *testing.T) {
	var status atomic.Int32
	status.Store(200)
	var calls atomic.Int32
	srv, store, scope := runnerGroupTestServer(t, "admin", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
		if status.Load() != 200 {
			fmt.Fprint(w, "secret-provider-body")
			return
		}
		fmt.Fprint(w, `{"total_count":1,"runner_groups":[{"id":1,"name":"Build"}]}`)
	})
	profile, err := store.UpsertScopedProfileIfUnchanged(state.ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: "gpu", WorkflowLabels: []string{"qiniu", "gpu"}, TemplateID: "template", Enabled: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	target := "/user/runner-specs/gpu?installation_id=1"
	patch := func(body string) *httptest.ResponseRecorder {
		return runnerGroupUserRequest(srv, http.MethodPatch, target, fmt.Sprintf(`{%s,"expected_updated_at":%q}`, body, profile.UpdatedAt.Format(time.RFC3339Nano)), "hubot-id")
	}
	auditBefore, _ := store.ListAuditEvents(100)
	rec := patch(`"runner_group":"Missing"`)
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "runner_group_not_found") {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body.String())
	}
	after, _ := store.ListAuditEvents(100)
	if !reflect.DeepEqual(auditBefore, after) {
		t.Fatal("rejected group mutation audited")
	}
	got, _ := store.GetScopedProfile(scope, "gpu")
	if got.RunnerGroup != "" {
		t.Fatal("rejected mutation changed profile")
	}
	rec = patch(`"runner_group":"Build"`)
	if rec.Code != 200 {
		t.Fatalf("valid: %d %s", rec.Code, rec.Body.String())
	}
	profile, _ = store.GetScopedProfile(scope, "gpu")
	status.Store(403)
	callsBefore := calls.Load()
	rec = patch(`"max_concurrency":2,"runner_group":"Build"`)
	if rec.Code != 200 || calls.Load() != callsBefore {
		t.Fatalf("unchanged group edit: %d %s calls=%d", rec.Code, rec.Body.String(), calls.Load())
	}
	profile, _ = store.GetScopedProfile(scope, "gpu")
	rec = patch(`"runner_group":"Other","max_concurrency":-1`)
	if rec.Code != 400 || calls.Load() != callsBefore {
		t.Fatal("invalid local fields reached provider")
	}
	rec = patch(`"runner_group":"Other"`)
	if rec.Code != 502 || !strings.Contains(rec.Body.String(), "runner_group_permission_denied") || strings.Contains(rec.Body.String(), "secret-provider") {
		t.Fatalf("permission: %d %s", rec.Code, rec.Body.String())
	}
	// Disabling and clearing a stale group must remain possible during an outage.
	rec = patch(`"enabled":false`)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	profile, _ = store.GetScopedProfile(scope, "gpu")
	rec = patch(`"enabled":true`)
	if rec.Code != 502 {
		t.Fatalf("re-enable skipped validation: %d %s", rec.Code, rec.Body.String())
	}
	rec = patch(`"runner_group":""`)
	if rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	rec = runnerGroupUserRequest(srv, http.MethodPost, "/user/runner-specs?installation_id=1", `{"name":"new","workflow_labels":["qiniu","new"],"template_id":"template","runner_group":"Other","enabled":true}`, "hubot-id")
	if rec.Code != 502 {
		t.Fatalf("create skipped group validation: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetScopedProfile(scope, "new"); err == nil {
		t.Fatal("rejected create persisted")
	}
}

func TestRunnerGroupRuntimeRejectsBeforeSandbox(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		visible bool
		code    string
	}{
		{"repository denied", 200, false, "runner_group_repository_denied"},
		{"permission denied", 403, false, "runner_group_permission_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, _ := runnerGroupTestServer(t, "admin", func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/orgs/octo-org/actions/runner-groups" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				if tc.status != 200 {
					fmt.Fprint(w, "secret-provider-body")
					return
				}
				if r.URL.Query().Get("visible_to_repository") == "repo" && !tc.visible {
					fmt.Fprint(w, `{"total_count":0,"runner_groups":[]}`)
					return
				}
				fmt.Fprint(w, `{"total_count":1,"runner_groups":[{"id":1,"name":"Build"}]}`)
			})
			_, err := store.UpsertProfile(state.RunnerProfile{Name: "org", Labels: []string{"qiniu", "org"}, TemplateID: "template", RunnerGroup: "Build", Enabled: true})
			if err != nil {
				t.Fatal(err)
			}
			req := adminRequest(http.MethodPost, "/runner_requests", strings.NewReader(`{"id":"group-reject","repository_full_name":"octo-org/repo","runner_spec_name":"org"}`))
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != 202 {
				t.Fatal(rec.Body.String())
			}
			waitForState(t, store, "group-reject", state.StatusFailed)
			st, _ := store.ReadState("group-reject")
			fake := srv.sandbox.(*fakeSandbox)
			if st.LastErrorCode != tc.code || st.LastErrorRetryable || st.SandboxID != "" || fake.startedCount() != 0 || strings.Contains(st.LastErrorMessage, "secret-provider") {
				t.Fatalf("unexpected failure %+v started=%d", st, fake.startedCount())
			}
		})
	}
}
