package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestFixedIDPublicationUsesActualVisibilityAndPreservesExecution(t *testing.T) {
	for _, tc := range []struct {
		name          string
		public, ready bool
		status        int
	}{
		{"public", true, true, 200}, {"private", false, true, 400}, {"no-default-build", true, false, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := state.New(t.TempDir())
			srv := newTestServer(t, store, "", &fakeSandbox{})
			configureAdminProfileTemplateService(t, srv, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-API-Key") != "admin-validation-key" {
					t.Error("wrong credentials")
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/templates/fixed-id":
					fmt.Fprintf(w, `{"templateID":"fixed-id","isOwner":true,"public":%t}`, tc.public)
				case "/templates":
					build := "00000000-0000-0000-0000-000000000001"
					if !tc.ready {
						build = "00000000-0000-0000-0000-000000000000"
					}
					fmt.Fprintf(w, `[{"templateID":"fixed-id","buildID":%q,"public":%t}]`, build, tc.public)
				default:
					t.Errorf("unexpected provider call %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			before, err := store.UpsertProfile(state.RunnerProfile{Name: "xxx-large", TemplateID: "fixed-id", Labels: []string{"qiniu", "xxx-large"}, RequiredLabels: []string{"qiniu", "xxx-large"}, RunnerGroup: "private-group", Enabled: true, MaxConcurrency: 100, Priority: 100})
			if err != nil {
				t.Fatal(err)
			}
			// Directory changes must remain available while a request is active.
			if _, _, err = store.CreateRequest(state.RunnerRequest{ID: "active", ProfileName: before.Name, Labels: before.Labels, RunnerName: "active"}, nil); err != nil {
				t.Fatal(err)
			}
			audits, _ := store.ListAuditEvents(100)
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, adminRequest(http.MethodPatch, "/runner_specs/xxx-large", strings.NewReader(`{"published":true}`)))
			if rec.Code != tc.status {
				t.Fatalf("publish %d %s", rec.Code, rec.Body.String())
			}
			got, _ := store.GetProfile(before.Name)
			afterAudits, _ := store.ListAuditEvents(100)
			if tc.status != 200 {
				if !reflect.DeepEqual(got, before) || len(afterAudits) != len(audits) {
					t.Fatal("rejected save changed data or audit")
				}
				return
			}
			if !got.Published || profileExecutionChanged(got, before) || got.MaxConcurrency != before.MaxConcurrency || got.Priority != before.Priority {
				t.Fatalf("publication changed runtime: %#v", got)
			}
			if len(afterAudits) != len(audits)+1 {
				t.Fatal("publication audit missing")
			}
			user := httptest.NewRecorder()
			srv.writeUserRunnerSpecList(user, state.RunnerProfileScope{Type: state.RunnerProfileScopeAccount, ID: 1}, accountPreferenceScope{Type: state.AccountScopeTypeAccount, ID: 1})
			if !strings.Contains(user.Body.String(), "xxx-large") || strings.Contains(user.Body.String(), "fixed-id") || strings.Contains(user.Body.String(), "private-group") {
				t.Fatalf("directory projection: %s", user.Body.String())
			}
			// The anonymous stable-name API cannot turn a physical ID into a public name.
			anon := publicTemplatesResponse(srv)
			if strings.Contains(anon.Body.String(), "fixed-id") || strings.Contains(anon.Body.String(), "xxx-large") {
				t.Fatalf("stable name contract changed: %s", anon.Body.String())
			}
			rec = httptest.NewRecorder()
			srv.ServeHTTP(rec, adminRequest(http.MethodPatch, "/runner_specs/xxx-large", strings.NewReader(`{"published":false}`)))
			if rec.Code != 200 {
				t.Fatalf("unpublish %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestAdminTemplateStatusSeparatesReferenceAndVisibility(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "", &fakeSandbox{})
	configureAdminProfileTemplateService(t, srv, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/templates/public-id", "/templates/private-id", "/templates/not-ready":
			fmt.Fprintf(w, `{"templateID":%q,"names":["team/template"],"createdAt":"2026-01-02T03:04:05Z","updatedAt":"2026-10-09T06:00:00Z","isOwner":true,"public":%t}`, strings.TrimPrefix(r.URL.Path, "/templates/"), r.URL.Path != "/templates/private-id")
		case "/templates/public-name":
			w.Write([]byte(`{"templateID":"public-id","public":true}`))
		case "/templates/building-name", "/templates/private-name":
			http.NotFound(w, r)
		case "/templates/unavailable":
			w.WriteHeader(503)
			w.Write([]byte(`{"message":"provider-secret"}`))
		case "/templates":
			w.Write([]byte(`[{"templateID":"public-id","buildID":"00000000-0000-0000-0000-000000000001","cpuCount":8,"memoryMB":16384,"diskSizeMB":81920,"envdVersion":"0.5.1"},{"templateID":"private-id","buildID":"00000000-0000-0000-0000-000000000001"},{"templateID":"not-ready","buildID":"00000000-0000-0000-0000-000000000000"}]`))
		case "/default-templates":
			w.Write([]byte(`[{"templateID":"public-id","names":["public-name"],"public":true,"buildID":"00000000-0000-0000-0000-000000000001","buildStatus":"ready"},{"templateID":"building-id","names":["building-name"],"public":true,"buildStatus":"building"},{"templateID":"private-name-id","names":["private-name"],"public":false,"buildID":"00000000-0000-0000-0000-000000000001","buildStatus":"ready"}]`))
		default:
			t.Errorf("unexpected provider call %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	for _, tc := range []struct {
		ref, source   string
		public, ready bool
		status        int
	}{
		{"public-id", "private", true, true, 200}, {"private-id", "private", false, true, 200}, {"not-ready", "private", true, false, 200}, {"public-name", "public", true, true, 200}, {"building-name", "public", true, false, 200}, {"private-name", "public", false, true, 200}, {"unavailable", "", false, false, 502},
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, adminRequest(http.MethodGet, "/runner_specs/templates/status?template="+tc.ref, nil))
		if rec.Code != tc.status || strings.Contains(rec.Body.String(), "provider-secret") {
			t.Fatalf("%s %d %s", tc.ref, rec.Code, rec.Body.String())
		}
		if tc.status == 200 {
			var info struct {
				Template struct {
					TemplateID  string   `json:"template_id"`
					Names       []string `json:"names"`
					CPUCount    int      `json:"cpu_count"`
					EnvdVersion string   `json:"envd_version"`
					CreatedAt   string   `json:"created_at"`
				} `json:"template"`
				Public, Runnable bool
				TemplateSource   string `json:"template_source"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
				t.Fatal(err)
			}
			if info.Public != tc.public || info.Runnable != tc.ready || info.TemplateSource != tc.source {
				t.Fatalf("%s: %#v", tc.ref, info)
			}
			if tc.ref == "public-id" && (info.Template.TemplateID != "public-id" || info.Template.CPUCount != 8 || info.Template.EnvdVersion != "0.5.1" || len(info.Template.Names) != 1 || info.Template.CreatedAt != "2026-01-02T03:04:05Z") {
				t.Fatalf("provider details lost: %#v", info.Template)
			}
		}
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/runner_specs/templates/status?template=public-id", nil))
	if rec.Code != 401 {
		t.Fatalf("unauthenticated metadata: %d", rec.Code)
	}
	audits, _ := store.ListAuditEvents(100)
	if len(audits) != 0 {
		t.Fatalf("metadata inspection mutated audit: %#v", audits)
	}
}

func TestAdminTemplateStatusRequiresAdminServiceConfiguration(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "", &fakeSandbox{})
	for _, reference := range []string{"github-runner-ubuntu-slim", "physical-id"} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, adminRequest(http.MethodGet, "/runner_specs/templates/status?template="+reference, nil))
		if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), `"code":"sandbox_service_not_configured"`) {
			t.Fatalf("%s: %d %s", reference, rec.Code, rec.Body.String())
		}
	}
}

func TestAdminTemplateDefaultBuildParity(t *testing.T) {
	for _, tc := range []struct {
		name, buildID, status string
		runnable              bool
	}{
		{"failed rebuild", "00000000-0000-0000-0000-000000000001", "failed", true},
		{"active rebuild", "00000000-0000-0000-0000-000000000001", "building", true},
		{"uploaded without default", "", "uploaded", false},
		{"ready with zero default", "00000000-0000-0000-0000-000000000000", "ready", false},
	} {
		for _, kind := range []string{"name", "id"} {
			t.Run(tc.name+"/"+kind, func(t *testing.T) {
				store := state.New(t.TempDir())
				srv := newTestServer(t, store, "", &fakeSandbox{})
				configureAdminProfileTemplateService(t, srv, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					switch r.URL.Path {
					case "/templates/physical-id":
						fmt.Fprint(w, `{"templateID":"physical-id","public":true,"isOwner":true}`)
					case "/templates", "/default-templates":
						fmt.Fprintf(w, `[{"templateID":"physical-id","names":["team/public-name"],"public":true,"buildID":%q,"buildStatus":%q}]`, tc.buildID, tc.status)
					default:
						t.Errorf("unexpected provider path %s", r.URL.Path)
						http.NotFound(w, r)
					}
				}))
				reference := "public-name"
				profile := state.RunnerProfile{Name: "existing", TemplateSource: state.TemplateSourcePublic, DefaultTemplateName: reference, Enabled: true, Labels: []string{"qiniu"}}
				if kind == "id" {
					reference = "physical-id"
					profile.TemplateSource = state.TemplateSourcePrivate
					profile.DefaultTemplateName = ""
					profile.TemplateID = reference
				}
				rec := httptest.NewRecorder()
				srv.ServeHTTP(rec, adminRequest(http.MethodGet, "/runner_specs/templates/status?template="+reference+"&reference_type="+kind, nil))
				var info sandboxrunner.TemplateInspection
				if rec.Code != 200 {
					t.Fatalf("status %d %s", rec.Code, rec.Body.String())
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &info); err != nil {
					t.Fatal(err)
				}
				if !info.Public || info.Runnable != tc.runnable || info.Template == nil || info.Template.BuildStatus != tc.status {
					t.Fatalf("inspection: %#v", info)
				}
				// Creation/binding must use the same effective default build as inspection.
				create := profile
				create.Name = "new"
				create.Published = true
				body, _ := json.Marshal(create)
				rec = httptest.NewRecorder()
				srv.ServeHTTP(rec, adminRequest(http.MethodPost, "/runner_specs", strings.NewReader(string(body))))
				want := 400
				if tc.runnable {
					want = 201
				}
				if rec.Code != want {
					t.Fatalf("create %d want %d: %s", rec.Code, want, rec.Body.String())
				}
				if !tc.runnable {
					audits, _ := store.ListAuditEvents(100)
					if len(audits) != 0 {
						t.Fatal("rejected creation wrote an audit")
					}
				}
				before, err := store.UpsertProfile(profile)
				if err != nil {
					t.Fatal(err)
				}
				// Publicness alone controls an existing spec's display setting.
				audits, _ := store.ListAuditEvents(100)
				rec = httptest.NewRecorder()
				srv.ServeHTTP(rec, adminRequest(http.MethodPatch, "/runner_specs/existing", strings.NewReader(`{"published":true}`)))
				if rec.Code != 200 {
					t.Fatalf("publish %d %s", rec.Code, rec.Body.String())
				}
				after, _ := store.GetProfile("existing")
				afterAudits, _ := store.ListAuditEvents(100)
				if !after.Published || profileExecutionChanged(before, after) || len(afterAudits) != len(audits)+1 {
					t.Fatal("publication changed execution or omitted audit")
				}
				rec = httptest.NewRecorder()
				srv.ServeHTTP(rec, adminRequest(http.MethodPatch, "/runner_specs/existing", strings.NewReader(`{"enabled":false}`)))
				if rec.Code != 200 {
					t.Fatalf("disable %d", rec.Code)
				}
				disabled, _ := store.GetProfile("existing")
				audits, _ = store.ListAuditEvents(100)
				rec = httptest.NewRecorder()
				srv.ServeHTTP(rec, adminRequest(http.MethodPatch, "/runner_specs/existing", strings.NewReader(`{"enabled":true}`)))
				want = 400
				if tc.runnable {
					want = 200
				}
				if rec.Code != want {
					t.Fatalf("enable %d want %d: %s", rec.Code, want, rec.Body.String())
				}
				if !tc.runnable {
					after, _ = store.GetProfile("existing")
					afterAudits, _ = store.ListAuditEvents(100)
					if !reflect.DeepEqual(after, disabled) || len(afterAudits) != len(audits) {
						t.Fatal("failed enable mutated data/audit")
					}
				}
			})
		}
	}
}
