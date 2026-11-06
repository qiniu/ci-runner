package server

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
	"golang.org/x/sync/singleflight"
)

type publicTemplatesCountingStore struct {
	state.Store
	calls  atomic.Int32
	before func(context.Context)
	fail   atomic.Bool
}

func (s *publicTemplatesCountingStore) ListProfilesContext(ctx context.Context) ([]state.RunnerProfile, error) {
	s.calls.Add(1)
	if s.before != nil {
		s.before(ctx)
	}
	if s.fail.Load() {
		return nil, errors.New("database unavailable")
	}
	return s.Store.ListProfilesContext(ctx)
}

func publicTemplatesResponse(srv *Server) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.handlePublicRunnerTemplates(rec, httptest.NewRequest(http.MethodGet, "/api/public/runner-templates", nil))
	return rec
}

func TestPublicRunnerTemplatesReadsEachRequest(t *testing.T) {
	store := &publicTemplatesCountingStore{Store: state.New(t.TempDir())}
	srv := &Server{store: store, logger: slog.Default()}
	for _, fail := range []bool{false, true, false} {
		store.fail.Store(fail)
		rec := publicTemplatesResponse(srv)
		if fail {
			if rec.Code != http.StatusInternalServerError || rec.Header().Get("Cache-Control") != "" {
				t.Fatalf("failure response: %d %v", rec.Code, rec.Header())
			}
		} else if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "public, max-age=60" || strings.TrimSpace(rec.Body.String()) != "[]" {
			t.Fatalf("catalog response: %d %v %s", rec.Code, rec.Header(), rec.Body.String())
		}
	}
	if store.calls.Load() != 3 {
		t.Fatalf("profile reads = %d, want one per request", store.calls.Load())
	}
}

func TestPublicRunnerTemplatesSanitizesSharedQueryPanic(t *testing.T) {
	store := &publicTemplatesCountingStore{Store: state.New(t.TempDir())}
	store.before = func(ctx context.Context) { panic("private driver evidence") }
	var logs bytes.Buffer
	srv := &Server{store: store, logger: slog.New(slog.NewTextHandler(&logs, nil))}
	rec := publicTemplatesResponse(srv)
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "private driver evidence") ||
		strings.Count(logs.String(), "category=query_panicked") != 1 || strings.Contains(logs.String(), "private driver evidence") {
		t.Fatalf("unexpected sanitized error: status=%d body=%q log=%q", rec.Code, rec.Body, &logs)
	}
}

func TestPublicRunnerTemplatesShareOnlyInFlightReads(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	store := &publicTemplatesCountingStore{Store: state.New(t.TempDir())}
	store.before = func(ctx context.Context) {
		if store.calls.Load() != 1 {
			return
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
			t.Error("missing shared query deadline")
		}
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
		}
	}
	srv := &Server{store: store, logger: slog.Default()}
	leaderCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	leaderDone := make(chan struct{})
	leaderResponse := httptest.NewRecorder()
	go func() {
		defer close(leaderDone)
		r := httptest.NewRequestWithContext(leaderCtx, http.MethodGet, "/api/public/runner-templates", nil)
		srv.handlePublicRunnerTemplates(leaderResponse, r)
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("catalog read did not start")
	}
	var readers []<-chan singleflight.Result
	for range 32 {
		readers = append(readers, srv.publicTemplatesRead())
	}
	cancel()
	select {
	case <-leaderDone:
		if leaderResponse.Code != 499 {
			t.Errorf("disconnected caller status = %d", leaderResponse.Code)
		}
	case <-time.After(time.Second):
		t.Error("disconnected leader kept waiting for the shared read")
	}
	close(release)
	await := func(ch <-chan singleflight.Result) singleflight.Result {
		t.Helper()
		select {
		case result := <-ch:
			return result
		case <-time.After(5 * time.Second):
			t.Fatal("catalog read did not complete")
			return singleflight.Result{}
		}
	}
	for _, result := range readers {
		if r := await(result); r.Err != nil || !r.Shared {
			t.Fatalf("shared read: %+v", r)
		}
	}
	if result := await(srv.publicTemplatesRead()); result.Err != nil || result.Shared || store.calls.Load() != 2 {
		t.Fatalf("subsequent read: calls=%d result=%+v", store.calls.Load(), result)
	}
}

func TestPublicRunnerTemplatesReflectOtherServerMutations(t *testing.T) {
	dbDir := t.TempDir()
	srv := newTestServer(t, state.New(dbDir), "", &fakeSandbox{})
	reader := &Server{store: state.New(dbDir), logger: slog.Default()}
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
		{http.MethodPatch, "/runner_specs/new-public", `{"enabled":false}`, http.StatusOK, false},
		{http.MethodPatch, "/runner_specs/new-public", `{"enabled":true}`, http.StatusOK, true},
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
