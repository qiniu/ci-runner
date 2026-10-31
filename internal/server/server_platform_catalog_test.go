package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestLegacyPrivateSpecCreateRetainsContract(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "", &fakeSandbox{})
	configureAdminProfileTemplateService(t, srv, nil, "valid-id")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, adminRequest(http.MethodPost, "/runner_specs", strings.NewReader(`{"name":"custom","labels":["qiniu"],"template_id":"valid-id"}`)))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status %d: %s", rec.Code, rec.Body.String())
	}
	profile, err := store.GetProfile("custom")
	if err != nil || profile.TemplateSource != state.TemplateSourcePrivate || profile.TemplateID != "valid-id" || profile.Published {
		t.Fatalf("legacy create contract: %#v %v", profile, err)
	}
	for _, field := range []string{"runner_update_policy", "require_docker", "fork_sponsorship"} {
		if strings.Contains(rec.Body.String(), field) {
			t.Fatalf("unexpected execution option %q: %s", field, rec.Body.String())
		}
	}
}

func TestPublicSpecPublicationValidatesProviderAndUpdatesDatabaseCatalog(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		status     int
	}{
		{"public", `[{"templateID":"physical-secret","names":["public-name"],"public":true,"buildID":"00000000-0000-0000-0000-000000000001","buildStatus":"ready"}]`, 201},
		{"private", `[{"templateID":"physical-secret","names":["public-name"],"public":false,"buildID":"00000000-0000-0000-0000-000000000001","buildStatus":"ready"}]`, 400},
		{"missing", `[]`, 400},
		{"building", `[{"templateID":"physical-secret","names":["public-name"],"public":true,"buildStatus":"building"}]`, 400},
		{"ambiguous", `[{"templateID":"one","names":["public-name"],"public":true,"buildID":"00000000-0000-0000-0000-000000000001","buildStatus":"ready"},{"templateID":"two","names":["public-name"],"public":true,"buildID":"00000000-0000-0000-0000-000000000001","buildStatus":"ready"}]`, 400},
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
			srv.ServeHTTP(rec, adminRequest(http.MethodPost, "/runner_specs", strings.NewReader(`{"name":"new-public","labels":["qiniu","public"],"required_labels":["qiniu","public"],"template_source":"public","default_template_name":"public-name","published":true,"enabled":true}`)))
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

func TestNonPublicSpecCannotPublishAndActiveSpecCannotChangeExecutionOrDelete(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "", &fakeSandbox{})
	before, _ := store.GetProfile("default")
	configureAdminProfileTemplateService(t, srv, nil, "base")
	for _, body := range []string{`{"published":true}`} {
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

// The browser echoes the list response verbatim. PostgreSQL can return a
// non-UTC offset; equivalent instants must not be rejected as stale revisions.
func TestPlatformSpecRevisionTimezone(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "", &fakeSandbox{})
	before, err := store.GetProfile("default")
	if err != nil {
		t.Fatal(err)
	}
	audits, _ := store.ListAuditEvents(100)
	patch := func(revision string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]any{"expected_updated_at": revision, "priority": before.Priority})
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, adminRequest(http.MethodPatch, "/runner_specs/default", strings.NewReader(string(body))))
		return rec
	}
	revision := before.UpdatedAt.In(time.FixedZone("CST", 8*3600)).Format(time.RFC3339Nano)
	if rec := patch(revision); rec.Code != http.StatusOK {
		t.Fatalf("equivalent offset rejected: %d %s", rec.Code, rec.Body.String())
	}
	if rec := patch(revision); rec.Code != http.StatusConflict {
		t.Fatalf("stale revision accepted: %d", rec.Code)
	}
	if rec := patch("invalid"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid revision accepted: %d", rec.Code)
	}
	after, _ := store.ListAuditEvents(100)
	if len(after) != len(audits)+1 {
		t.Fatalf("expected exactly one committed audit, got %d", len(after)-len(audits))
	}
}

func TestPlatformSpecRevisionPostgresSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL test database")
	}
	store := state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: catalogServerTestDSN(t), MigrateOnStart: true})
	srv := newTestServer(t, store, "", &fakeSandbox{})
	listed := httptest.NewRecorder()
	srv.ServeHTTP(listed, adminRequest(http.MethodGet, "/runner_specs", nil))
	var profiles []state.RunnerProfile
	if err := json.Unmarshal(listed.Body.Bytes(), &profiles); err != nil {
		t.Fatal(err)
	}
	if len(profiles) == 0 {
		t.Fatal("no test profiles")
	}
	body, _ := json.Marshal(map[string]any{"expected_updated_at": profiles[0].UpdatedAt, "priority": profiles[0].Priority})
	saved := httptest.NewRecorder()
	srv.ServeHTTP(saved, adminRequest(http.MethodPatch, "/runner_specs/"+profiles[0].Name, strings.NewReader(string(body))))
	if saved.Code != http.StatusOK {
		t.Fatalf("PG list/save failed: %d %s", saved.Code, saved.Body.String())
	}
}

// A private schema keeps this server test independent of the state package's
// real-dialect fixtures when packages run concurrently in the same test DB.
func catalogServerTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("RUNNERD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("RUNNERD_POSTGRES_TEST_DSN is required")
	}
	pool, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var database string
	if err := pool.QueryRowContext(ctx, "SELECT current_database()").Scan(&database); err != nil || !strings.HasSuffix(database, "_test") {
		t.Fatal("server migration test requires a dedicated database ending in _test")
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal(err)
	}
	schema := "runnerd_server_" + hex.EncodeToString(nonce[:])
	if _, err := pool.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := pool.ExecContext(ctx, `DROP SCHEMA "`+schema+`" CASCADE`); err != nil {
			t.Error(err)
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return dsn + " search_path=" + schema
}
