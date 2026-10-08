package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestCreatePrivateSpecWithoutTemplateSourcePreservesPreparationPolicy(t *testing.T) {
	for _, tt := range []struct {
		policy, want string
		status       int
	}{
		{state.RunnerUpdatePreinstalled, state.RunnerUpdatePreinstalled, http.StatusCreated},
		{state.RunnerUpdateOfficial, state.RunnerUpdateOfficial, http.StatusCreated},
		{"", state.RunnerUpdateOfficial, http.StatusCreated},
		{"invalid", "", http.StatusBadRequest},
	} {
		t.Run(tt.policy, func(t *testing.T) {
			store := state.New(t.TempDir())
			srv := newTestServer(t, store, "", &fakeSandbox{})
			configureAdminProfileTemplateService(t, srv, nil, "valid-id")
			before, err := store.ListAuditEvents(100)
			if err != nil {
				t.Fatal(err)
			}
			payload := map[string]any{"name": "custom", "labels": []string{"qiniu"}, "template_id": "valid-id"}
			if tt.policy != "" {
				payload["runner_update_policy"] = tt.policy
			}
			body, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, adminRequest(http.MethodPost, "/runner_specs", strings.NewReader(string(body))))
			if rec.Code != tt.status {
				t.Fatalf("create status %d: %s", rec.Code, rec.Body.String())
			}
			profile, err := store.GetProfile("custom")
			if tt.status == http.StatusBadRequest {
				if !errors.Is(err, state.ErrNotFound) {
					t.Fatalf("invalid policy persisted: %v", err)
				}
				after, auditErr := store.ListAuditEvents(100)
				if auditErr != nil || len(after) != len(before) {
					t.Fatalf("rejected policy changed audit events: %v", auditErr)
				}
				return
			}
			if err != nil || profile.TemplateSource != state.TemplateSourcePrivate || profile.RunnerUpdatePolicy != tt.want {
				t.Fatalf("saved private policy: %#v %v", profile, err)
			}
			var response state.RunnerProfile
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.RunnerUpdatePolicy != tt.want {
				t.Fatalf("response policy: %s %v", rec.Body.String(), err)
			}
		})
	}
}

func TestPublicSpecPublicationValidatesProviderAndUpdatesDatabaseCatalog(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
	}{
		{"public", `[{"templateID":"physical-secret","names":["public-name"],"public":true,"buildStatus":"ready"}]`, 201},
		{"private", `[{"templateID":"physical-secret","names":["public-name"],"public":false,"buildStatus":"ready"}]`, 400},
		{"missing", `[]`, 400},
		{"building", `[{"templateID":"physical-secret","names":["public-name"],"public":true,"buildStatus":"building"}]`, 400},
		{"ambiguous", `[{"templateID":"one","names":["public-name"],"public":true,"buildStatus":"ready"},{"templateID":"two","names":["public-name"],"public":true,"buildStatus":"ready"}]`, 400},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := state.New(t.TempDir())
			srv := newTestServer(t, store, "", &fakeSandbox{})
			var calls atomic.Int32
			configureAdminProfileTemplateService(t, srv, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/default-templates" || r.Header.Get("X-API-Key") != "admin-validation-key" {
					t.Errorf("unexpected provider request: %s", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Write([]byte(tt.body))
			}))
			before, _ := store.ListAuditEvents(100)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, adminRequest(http.MethodPost, "/runner_specs", strings.NewReader(`{"name":"new-public","labels":["qiniu","public"],"required_labels":["qiniu","public"],"template_source":"public","default_template_name":"public-name","published":true,"enabled":true,"runner_update_policy":"official"}`)))
			if rec.Code != tt.status {
				t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
			}
			after, _ := store.ListAuditEvents(100)
			public := httptest.NewRecorder()
			srv.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/api/public/runner-templates", nil))
			if calls.Load() != 1 {
				t.Fatalf("validation calls=%d", calls.Load())
			}
			if tt.status != 201 {
				if len(before) != len(after) {
					t.Fatal("rejected publication recorded an audit")
				}
				if _, err := store.GetProfile("new-public"); !errors.Is(err, state.ErrNotFound) {
					t.Fatalf("rejected profile saved: %v", err)
				}
				if strings.Contains(public.Body.String(), "new-public") {
					t.Fatal("rejected profile published")
				}
				return
			}
			if !strings.Contains(public.Body.String(), "new-public") || strings.Contains(public.Body.String(), "physical-secret") {
				t.Fatalf("public catalog: %s", public.Body.String())
			}
			// Unpublishing works even without provider access and immediately removes the catalog entry.
			rec = httptest.NewRecorder()
			srv.ServeHTTP(rec, adminRequest(http.MethodPatch, "/runner_specs/new-public", strings.NewReader(`{"published":false}`)))
			if rec.Code != 200 || calls.Load() != 1 {
				t.Fatalf("unpublish: %d calls=%d", rec.Code, calls.Load())
			}
			public = httptest.NewRecorder()
			srv.ServeHTTP(public, httptest.NewRequest(http.MethodGet, "/api/public/runner-templates", nil))
			if strings.Contains(public.Body.String(), "new-public") {
				t.Fatal("unpublished entry remains")
			}
		})
	}
}

func TestPrivateSpecCannotPublishAndActiveSpecCannotChangeExecutionOrDelete(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "", &fakeSandbox{})
	before, _ := store.GetProfile("default")
	for _, body := range []string{`{"published":true}`, `{"fork_sponsorship":true}`} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, adminRequest(http.MethodPatch, "/runner_specs/default", strings.NewReader(body)))
		if rec.Code != 400 {
			t.Fatalf("private policy: %d %s", rec.Code, rec.Body.String())
		}
	}
	if _, _, err := store.CreateRequest(state.RunnerRequest{ID: "active-profile", ProfileName: "default", Labels: before.Labels, RunnerName: "active-profile"}, nil); err != nil {
		t.Fatal(err)
	}
	auditBefore, _ := store.ListAuditEvents(100)
	for _, action := range []struct{ method, body string }{{http.MethodPatch, `{"required_labels":["e2b"]}`}, {http.MethodPost, `{"name":"default","labels":["e2b"],"template_id":"base"}`}, {http.MethodDelete, ""}} {
		// POST validates first; provide a local configured Sandbox to reach the audited guard.
		if action.method == http.MethodPost {
			configureAdminProfileTemplateService(t, srv, nil, "base")
		}
		target := "/runner_specs/default"
		if action.method == http.MethodPost {
			target = "/runner_specs"
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, adminRequest(action.method, target, strings.NewReader(action.body)))
		if rec.Code != 409 || !strings.Contains(rec.Body.String(), "runner_spec_in_use") {
			t.Fatalf("active guard: %d %s", rec.Code, rec.Body.String())
		}
	}
	auditAfter, _ := store.ListAuditEvents(100)
	if len(auditBefore) != len(auditAfter) {
		t.Fatal("rejected active mutation audited")
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, adminRequest(http.MethodPatch, "/runner_specs/default", strings.NewReader(`{"enabled":false,"max_concurrency":2}`)))
	if rec.Code != 200 {
		t.Fatalf("active policy edit: %d %s", rec.Code, rec.Body.String())
	}
	var got state.RunnerProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	stale := httptest.NewRecorder()
	srv.ServeHTTP(stale, adminRequest(http.MethodPatch, "/runner_specs/default", strings.NewReader(`{"priority":7,"expected_updated_at":"2000-01-01T00:00:00Z"}`)))
	if stale.Code != 409 {
		t.Fatalf("stale save: %d", stale.Code)
	}
}
