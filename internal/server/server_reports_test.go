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
	srv := newTestServer(t, state.New(t.TempDir()), "http://example.test", &fakeSandbox{})
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
		Snapshot string                  `json:"snapshot"`
		Query    state.RunnerReportQuery `json:"query"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value.Snapshot == "" || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("missing snapshot or cache boundary")
	}
	paths := []string{"/reports/overview", "/reports/active", "/reports/" + value.Snapshot + "/groups?dimension=spec", "/reports/" + value.Snapshot + "/requests"}
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
	for _, path := range []string{"/reports/overview?from=invalid", "/reports/overview?grain=year", "/reports/overview?timezone=Local", "/reports/overview?owner=github:0", "/reports/overview?grain=day&grain=week", "/reports/" + value.Snapshot + "/requests?limit=101", "/reports/" + value.Snapshot + "/groups?dimension=invalid"} {
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
	srv.reports.entries[0].created = time.Now().Add(-reportRetention)
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
	}{{state.ErrReportTooLarge, 422}, {state.ErrReportTooManyGroups, 422}, {context.DeadlineExceeded, 504}, {errors.New("private db detail"), 500}} {
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
