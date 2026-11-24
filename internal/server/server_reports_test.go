package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

type reportsReadStore struct {
	state.Store
	read func(context.Context, state.RunnerReportQuery, time.Time) (*state.RunnerReport, error)
}

func (s *reportsReadStore) ReadRunnerReport(ctx context.Context, q state.RunnerReportQuery, now time.Time) (*state.RunnerReport, error) {
	return s.read(ctx, q, now)
}

func TestRunnerReportHTTPAuthorizationAndSnapshot(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	get := func(path string, admin bool) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if admin {
			r = adminRequest(http.MethodGet, path, nil)
		}
		srv.ServeHTTP(w, r)
		return w
	}
	w := get("/reports/overview", true)
	if w.Code != 200 {
		t.Fatalf("overview: %d %s", w.Code, w.Body)
	}
	var value struct {
		Snapshot      string                              `json:"snapshot"`
		Distributions map[string]state.ReportDistribution `json:"distributions"`
		Query         state.RunnerReportQuery             `json:"query"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Snapshot == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing snapshot or cache boundary")
	}
	for _, dimension := range []string{"spec", "owner", "repository"} {
		ranking, ok := value.Distributions[dimension]
		if !ok || ranking.Requests == nil || ranking.Runtime == nil || ranking.Total != 0 {
			t.Fatalf("empty %s rankings missing from overview: %+v", dimension, ranking)
		}
	}
	w = get("/reports/spec-usage", true)
	var specs struct {
		Snapshot string
	}
	if w.Code != http.StatusOK {
		t.Fatalf("spec dashboard: %d %s", w.Code, w.Body)
	}
	if err := json.Unmarshal(w.Body.Bytes(), &specs); err != nil || specs.Snapshot == "" {
		t.Fatalf("missing spec dashboard snapshot: %s", w.Body)
	}
	specPage := "/reports/" + specs.Snapshot + "/specs"
	before := get(specPage, true)
	if before.Code != http.StatusOK {
		t.Fatalf("spec page: %d %s", before.Code, before.Body)
	}
	if _, err := store.UpsertProfile(state.RunnerProfile{Name: "added-after-snapshot", Labels: []string{"qiniu"}, TemplateID: "test", MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	if after := get(specPage, true); after.Code != http.StatusOK || after.Body.String() != before.Body.String() {
		t.Fatalf("spec page changed after catalog update: before %s, after %d %s", before.Body, after.Code, after.Body)
	}
	paths := []string{"/reports/spec-usage", "/reports/overview", "/reports/active", "/reports/" + value.Snapshot + "/groups?dimension=spec", specPage, "/reports/" + value.Snapshot + "/requests"}
	for _, path := range paths {
		ordinary := httptest.NewRequest(http.MethodGet, path, nil)
		ordinary.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		denied := httptest.NewRecorder()
		srv.ServeHTTP(denied, ordinary)
		if denied.Code != http.StatusUnauthorized {
			t.Errorf("ordinary user %s: %d", path, denied.Code)
		}
		if w := get(path, true); w.Code != 200 {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
		if w := get(path, false); w.Code != 401 {
			t.Errorf("unauthenticated %s: %d", path, w.Code)
		}
	}
	for _, path := range []string{"/reports/overview?from=invalid", "/reports/overview?grain=year", "/reports/overview?timezone=Local", "/reports/overview?owner=github:0", "/reports/overview?grain=day&grain=week", "/reports/" + value.Snapshot + "/requests?limit=101", "/reports/" + value.Snapshot + "/groups?dimension=invalid", "/reports/" + value.Snapshot + "/specs", specPage + "?sort=invalid"} {
		if w := get(path, true); w.Code != 400 {
			t.Errorf("invalid %s: %d %s", path, w.Code, w.Body)
		}
	}
	q := url.Values{"from": {value.Query.From}, "to": {value.Query.To}, "timezone": {value.Query.Timezone}, "grain": {value.Query.Grain}, "snapshot": {value.Snapshot}}
	if w := get("/reports/overview?"+q.Encode(), true); w.Code != 200 {
		t.Fatal(w.Body)
	}
	q.Set("repository", "other/repo")
	if w := get("/reports/overview?"+q.Encode(), true); w.Code != 410 {
		t.Fatalf("filter mismatch: %d", w.Code)
	}
	srv.reports.mu.Lock()
	for i := range srv.reports.entries {
		if srv.reports.entries[i].id == value.Snapshot {
			srv.reports.entries[i].created = time.Now().Add(-reportRetention)
		}
	}
	srv.reports.mu.Unlock()
	if w := get("/reports/"+value.Snapshot+"/requests", true); w.Code != 410 {
		t.Fatalf("expired: %d", w.Code)
	}
}

func TestRunnerReportSharedBuildAndCancelledWaiter(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	srv := &Server{store: &reportsReadStore{read: func(ctx context.Context, q state.RunnerReportQuery, now time.Time) (*state.RunnerReport, error) {
		calls.Add(1)
		entered <- ctx
		<-release
		return &state.RunnerReport{Query: q, ObservedAt: now}, nil
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := srv.loadRunnerReport(ctx, state.RunnerReportQuery{}); done <- err }()
	queryCtx := <-entered
	if _, ok := queryCtx.Deadline(); !ok {
		t.Fatal("unbounded build")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if queryCtx.Err() != nil {
		t.Fatal("waiter cancelled shared query")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := srv.loadRunnerReport(context.Background(), state.RunnerReportQuery{}); err != nil {
				t.Error(err)
			}
		}()
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("reads %d", calls.Load())
	}
}

func TestRunnerReportCacheBoundsAndErrorMapping(t *testing.T) {
	c := runnerReportCache{}
	for i := range 12 {
		c.put(cachedRunnerReport{id: fmt.Sprint(i), created: time.Now(), report: &state.RunnerReport{Rows: make([]state.ReportRequest, 30000)}})
	}
	total := 0
	for _, e := range c.entries {
		total += len(e.report.Rows)
	}
	if len(c.entries) > reportCacheEntries || total > reportCacheRows {
		t.Fatal("cache unbounded")
	}
	srv := newTestServer(t, state.New(t.TempDir()), "http://example.test", &fakeSandbox{})
	for _, tc := range []struct {
		err  error
		code int
	}{{state.ErrReportTooLarge, 422}, {state.ErrReportTooManyGroups, 422}, {state.ErrReportSnapshotTooLarge, 422}, {context.DeadlineExceeded, 504}, {errors.New("private db detail"), 500}} {
		w := httptest.NewRecorder()
		srv.writeReportError(w, httptest.NewRequest(http.MethodGet, "/reports/overview", nil), tc.err)
		if w.Code != tc.code {
			t.Fatalf("code %d: %s", w.Code, w.Body)
		}
		if tc.code == 500 && w.Body.String() == tc.err.Error() {
			t.Fatal("raw error exposed")
		}
	}
}

func TestRunnerReportDefaultsBeforeMidnightGap(t *testing.T) {
	now, err := time.Parse(time.RFC3339, "2026-03-07T17:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	q, err := parseRunnerReportQuery(url.Values{"timezone": {"America/Havana"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if q.From != "2026-02-06" || q.To != "2026-03-08" {
		t.Fatalf("default dates: %+v", q)
	}
	w, err := q.Window(now)
	if err != nil || !w.End.Equal(now) {
		t.Fatalf("default end=%+v err=%v", w, err)
	}
}

func TestRunnerReportSpecSnapshotOnAnotherServer(t *testing.T) {
	dir := t.TempDir()
	store := state.New(dir)
	first := newTestServerWithoutLoops(t, store, "http://example.test", &fakeSandbox{}, 10)
	second := newTestServerWithoutLoops(t, state.New(dir), "http://example.test", &fakeSandbox{}, 10)
	get := func(srv *Server, path string, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, adminRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Fatalf("GET %s: %d %s, want %d", path, w.Code, w.Body, want)
		}
		return w.Body.String()
	}
	if _, err := store.UpsertProfile(state.RunnerProfile{Name: "another-spec", Labels: []string{"qiniu"}, TemplateID: "test", MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	body := get(first, "/reports/spec-usage", http.StatusOK)
	var dashboard struct {
		Snapshot string
		Query    state.RunnerReportQuery
	}
	if err := json.Unmarshal([]byte(body), &dashboard); err != nil {
		t.Fatal(err)
	}
	page := "/reports/" + dashboard.Snapshot + "/specs?sort=runtime&limit=1&offset=1"
	before := get(first, page, http.StatusOK)
	var paged struct {
		Items  []state.ReportSpecUsage
		Offset int
	}
	if err := json.Unmarshal([]byte(before), &paged); err != nil || len(paged.Items) != 1 || paged.Offset != 1 {
		t.Fatalf("expected one item on the second page: %s", before)
	}
	if _, err := store.UpsertProfile(state.RunnerProfile{Name: "new-spec", Labels: []string{"qiniu"}, TemplateID: "test", MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}
	q := url.Values{"from": {dashboard.Query.From}, "to": {dashboard.Query.To}, "timezone": {dashboard.Query.Timezone}, "grain": {dashboard.Query.Grain}, "snapshot": {dashboard.Snapshot}}
	if got := get(second, "/reports/spec-usage?"+q.Encode(), http.StatusOK); got != body {
		t.Fatal("restored dashboard differs from the original response")
	}
	if got := get(second, page, http.StatusOK); got != before {
		t.Fatal("restored page differs after catalog change")
	}
	get(second, "/reports/overview?"+q.Encode(), http.StatusGone)
	q.Set("repository", "different/repo")
	get(second, "/reports/spec-usage?"+q.Encode(), http.StatusGone)
	if _, err := store.LoadReportSpecSnapshot(t.Context(), dashboard.Snapshot, time.Now().Add(reportRetention)); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("expired snapshot: %v", err)
	}
}
