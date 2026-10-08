package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestUserRunnerEventsMatchesAdminPagination(t *testing.T) {
	store := state.New(t.TempDir())
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/user/installations/987/repositories" || r.Header.Get("Authorization") != "Bearer user-token" {
			t.Errorf("unexpected GitHub authorization request: %s", r.URL.Path)
		}
		writeJSON(w, http.StatusOK, map[string]any{"repositories": []map[string]string{{"full_name": "o/r"}}})
	}))
	defer gh.Close()
	srv := newTestServer(t, store, gh.URL, &fakeSandbox{})
	account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountLogin: "o"}); err != nil {
		t.Fatal(err)
	}
	saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), "user-token")
	if _, _, err := store.CreateRequest(state.RunnerRequest{ID: "events-job", GitHubInstallationID: 987, RepositoryFullName: "o/r"}, nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 205; i++ {
		store.AppendStagedLog("events-job", []string{"control.log", "stdout.log", "stderr.log"}[i%3], "runner_hook", []byte(fmt.Sprintf("event %d\n", i)))
	}
	all, _, err := store.ListRunnerEvents("events-job", 0, 500)
	if err != nil {
		t.Fatal(err)
	}
	queries := []struct {
		query        string
		start, count int
		more         bool
	}{
		{"", 5, 200, true},
		{fmt.Sprintf("?before_id=%d", all[5].ID), 0, 5, false},
		{fmt.Sprintf("?after_id=%d", all[1].ID), 2, 200, true},
		{fmt.Sprintf("?after_id=%d", all[201].ID), 202, 3, false},
	}
	for _, tc := range queries {
		t.Run(tc.query, func(t *testing.T) {
			userReq := httptest.NewRequest(http.MethodGet, "/user/runner_requests/events-job/events"+tc.query, nil)
			userReq.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
			userRec := httptest.NewRecorder()
			srv.ServeHTTP(userRec, userReq)
			adminRec := httptest.NewRecorder()
			srv.ServeHTTP(adminRec, adminRequest(http.MethodGet, "/runner_requests/events-job/events"+tc.query, nil))
			if userRec.Code != http.StatusOK || adminRec.Code != http.StatusOK || userRec.Body.String() != adminRec.Body.String() {
				t.Fatalf("event pages differ: user %d %s; admin %d %s", userRec.Code, userRec.Body, adminRec.Code, adminRec.Body)
			}
			if userRec.Header().Get("Cache-Control") != "no-store" || adminRec.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("event pages must not be cached")
			}
			var page runnerRequestEventPage
			if err := json.Unmarshal(userRec.Body.Bytes(), &page); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(page.Events, all[tc.start:tc.start+tc.count]) || page.HasMore != tc.more {
				t.Fatalf("unexpected page: count %d, more %v", len(page.Events), page.HasMore)
			}
		})
	}
	for _, query := range []string{"?after_id=1&before_id=2", "?after_id=-1", "?before_id=0", "?before_id=bad"} {
		req := httptest.NewRequest(http.MethodGet, "/user/runner_requests/events-job/events"+query, nil)
		req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("query %s: status %d", query, rec.Code)
		}
	}
}

func TestUserRunnerEventsAuthorization(t *testing.T) {
	for _, tc := range []struct {
		name, token, repository string
		installation            int64
		session                 bool
		want                    int
	}{
		{"anonymous", "user-token", "o/r", 987, false, 401},
		{"missing token", "", "o/r", 987, true, 403},
		{"rejected token", "expired", "o/r", 987, true, 403},
		{"other repository", "user-token", "o/private", 987, true, 404},
		{"other installation", "user-token", "o/r", 456, true, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := state.New(t.TempDir())
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.token == "expired" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				writeJSON(w, http.StatusOK, map[string]any{"repositories": []map[string]string{{"full_name": "o/r"}}})
			}))
			defer gh.Close()
			srv := newTestServer(t, store, gh.URL, &fakeSandbox{})
			account, _, err := store.GetAccountByOAuthIdentity("github", "hubot-id")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := store.UpsertGitHubInstallation(state.GitHubInstallation{AccountID: account.ID, InstallationID: 987, AccountLogin: "o"}); err != nil {
				t.Fatal(err)
			}
			if tc.token != "" {
				saveTestGitHubOAuthToken(t, store, account.ID, srv.cfg.AuthEncryptionKey.Value(), tc.token)
			}
			if _, _, err := store.CreateRequest(state.RunnerRequest{ID: "private-events", GitHubInstallationID: tc.installation, RepositoryFullName: tc.repository}, nil); err != nil {
				t.Fatal(err)
			}
			store.AppendLog("private-events", "stdout.log", []byte("private output"))
			req := httptest.NewRequest(http.MethodGet, "/user/runner_requests/private-events/events", nil)
			if tc.session {
				req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			if rec.Code != tc.want || strings.Contains(rec.Body.String(), "private output") {
				t.Fatalf("status %d body %s", rec.Code, rec.Body)
			}
		})
	}
}
