package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/qiniu/ci-runner/internal/state"
)

type publicTemplatesCountingStore struct {
	state.Store
	calls int
	fail  bool
}

func (s *publicTemplatesCountingStore) ListProfiles() ([]state.RunnerProfile, error) {
	s.calls++
	if s.fail {
		return nil, errors.New("database unavailable")
	}
	return s.Store.ListProfiles()
}

func publicTemplatesResponse(srv *Server) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.handlePublicRunnerTemplates(rec, httptest.NewRequest(http.MethodGet, "/api/public/runner-templates", nil))
	return rec
}

func TestPublicRunnerTemplatesReadsEachRequest(t *testing.T) {
	store := &publicTemplatesCountingStore{Store: state.New(t.TempDir())}
	srv := &Server{store: store}
	for _, fail := range []bool{false, true, false} {
		store.fail = fail
		rec := publicTemplatesResponse(srv)
		if fail {
			if rec.Code != http.StatusInternalServerError || rec.Header().Get("Cache-Control") != "" {
				t.Fatalf("failure response: %d %v", rec.Code, rec.Header())
			}
		} else if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=60" || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("catalog response: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
		}
	}
	if store.calls != 3 {
		t.Fatalf("profile reads = %d, want one per request", store.calls)
	}
}

func TestPublicRunnerTemplatesReflectOtherServerMutations(t *testing.T) {
	dbDir := t.TempDir()
	srv := newTestServer(t, state.New(dbDir), "", &fakeSandbox{})
	reader := &Server{store: state.New(dbDir)}
	configureAdminProfileTemplateService(t, srv, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"templateID":"physical-secret","names":["public-name"],"public":true,"buildID":"00000000-0000-0000-0000-000000000001","buildStatus":"ready"}]`))
	}))
	if rec := publicTemplatesResponse(reader); strings.Contains(rec.Body.String(), "new-public") {
		t.Fatal("unexpected pre-create entry")
	}
	for _, tt := range []struct {
		method, target, body string
		status               int
		present              bool
	}{
		{http.MethodPost, "/runner_specs", `{"name":"new-public","labels":["qiniu"],"template_source":"public","default_template_name":"public-name","published":true,"enabled":true}`, http.StatusCreated, true},
		{http.MethodPatch, "/runner_specs/new-public", `{"published":false}`, http.StatusOK, false},
		{http.MethodPatch, "/runner_specs/new-public", `{"published":true}`, http.StatusOK, true},
		{http.MethodDelete, "/runner_specs/new-public", "", http.StatusOK, false},
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, adminRequest(tt.method, tt.target, strings.NewReader(tt.body)))
		if rec.Code != tt.status {
			t.Fatalf("%s: %d %s", tt.method, rec.Code, rec.Body.String())
		}
		public := publicTemplatesResponse(reader)
		if public.Code != http.StatusOK || strings.Contains(public.Body.String(), "new-public") != tt.present || strings.Contains(public.Body.String(), "physical-secret") {
			t.Fatalf("%s did not refresh safe projection: %s", tt.method, public.Body.String())
		}
	}
}
