package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

type publicTemplatesCountingStore struct {
	state.Store
	calls atomic.Int32
	fail  atomic.Bool
}

func (s *publicTemplatesCountingStore) ListProfiles() ([]state.RunnerProfile, error) {
	s.calls.Add(1)
	if s.fail.Load() {
		return nil, errors.New("database unavailable")
	}
	return s.Store.ListProfiles()
}

func publicTemplatesResponse(srv *Server) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.handlePublicRunnerTemplates(rec, httptest.NewRequest(http.MethodGet, "/api/public/runner-templates", nil))
	return rec
}

func TestPublicRunnerTemplatesCacheCoalescesConcurrentReads(t *testing.T) {
	store := &publicTemplatesCountingStore{Store: state.New(t.TempDir())}
	if _, err := store.UpsertProfile(publicCatalogFixture()[0]); err != nil {
		t.Fatal(err)
	}
	srv := &Server{store: store}
	const readers = 32
	responses := make(chan *httptest.ResponseRecorder, readers)
	var wg sync.WaitGroup
	for range readers {
		wg.Go(func() { responses <- publicTemplatesResponse(srv) })
	}
	wg.Wait()
	close(responses)
	var body string
	for rec := range responses {
		if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=60" {
			t.Fatalf("response: %d %v", rec.Code, rec.Header())
		}
		if body == "" {
			body = rec.Body.String()
		}
		if rec.Body.String() != body || !strings.Contains(body, "qiniu-ubuntu-22.04") {
			t.Fatalf("inconsistent public snapshot: %s", rec.Body.String())
		}
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("profile reads = %d, want one shared fill", got)
	}
}

func TestPublicRunnerTemplatesCacheExpiresAndRetriesFailures(t *testing.T) {
	store := &publicTemplatesCountingStore{Store: state.New(t.TempDir())}
	srv := &Server{store: store}
	store.fail.Store(true)
	if rec := publicTemplatesResponse(srv); rec.Code != http.StatusInternalServerError || rec.Header().Get("Cache-Control") != "" {
		t.Fatalf("failure response: %d %v", rec.Code, rec.Header())
	}
	store.fail.Store(false)
	if rec := publicTemplatesResponse(srv); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Fatalf("recovery response: %d %s", rec.Code, rec.Body.String())
	}
	// A change outside this Server becomes visible on expiry, including when the
	// previous cached projection was empty.
	if _, err := store.UpsertProfile(publicCatalogFixture()[0]); err != nil {
		t.Fatal(err)
	}
	if rec := publicTemplatesResponse(srv); strings.Contains(rec.Body.String(), "qiniu-ubuntu") {
		t.Fatal("snapshot unexpectedly changed before expiry")
	}
	srv.publicTemplatesMu.Lock()
	srv.publicTemplatesCache.expiresAt = time.Now().Add(-time.Second)
	srv.publicTemplatesMu.Unlock()
	if rec := publicTemplatesResponse(srv); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "qiniu-ubuntu-22.04") {
		t.Fatalf("expired snapshot was not refreshed: %d %s", rec.Code, rec.Body.String())
	}
	if got := store.calls.Load(); got != 3 {
		t.Fatalf("profile reads = %d, want failed fill, recovery, expiry", got)
	}
}

func TestPublicRunnerTemplatesCacheInvalidatedByAdminMutations(t *testing.T) {
	srv := newTestServer(t, state.New(t.TempDir()), "", &fakeSandbox{})
	configureAdminProfileTemplateService(t, srv, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"templateID":"physical-secret","names":["public-name"],"public":true,"buildStatus":"ready"}]`))
	}))
	if rec := publicTemplatesResponse(srv); strings.Contains(rec.Body.String(), "new-public") {
		t.Fatal("unexpected pre-create entry")
	}
	for _, tt := range []struct {
		method, target, body string
		status               int
		present              bool
	}{
		{http.MethodPost, "/runner_specs", `{"name":"new-public","labels":["qiniu"],"template_source":"public","default_template_name":"public-name","published":true,"enabled":true}`, http.StatusCreated, true},
		{http.MethodPatch, "/runner_specs/new-public", `{"enabled":false}`, http.StatusOK, false},
		{http.MethodPatch, "/runner_specs/new-public", `{"enabled":true}`, http.StatusOK, true},
		{http.MethodDelete, "/runner_specs/new-public", "", http.StatusOK, false},
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, adminRequest(tt.method, tt.target, strings.NewReader(tt.body)))
		if rec.Code != tt.status {
			t.Fatalf("%s: %d %s", tt.method, rec.Code, rec.Body.String())
		}
		public := publicTemplatesResponse(srv)
		if public.Code != http.StatusOK || strings.Contains(public.Body.String(), "new-public") != tt.present || strings.Contains(public.Body.String(), "physical-secret") {
			t.Fatalf("%s did not refresh safe projection: %s", tt.method, public.Body.String())
		}
	}
}

func TestPublicRunnerTemplatesCacheRetainedAfterRejectedAdminMutation(t *testing.T) {
	srv := newTestServer(t, state.New(t.TempDir()), "", &fakeSandbox{})
	if rec := publicTemplatesResponse(srv); rec.Code != http.StatusOK {
		t.Fatalf("warm cache: %d", rec.Code)
	}
	expiresAt := srv.publicTemplatesCache.expiresAt
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, adminRequest(http.MethodPatch, "/runner_specs/default", strings.NewReader(`{"published":true}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid publication: %d %s", rec.Code, rec.Body.String())
	}
	if !srv.publicTemplatesCache.expiresAt.Equal(expiresAt) {
		t.Fatal("rejected mutation invalidated the snapshot")
	}
}
