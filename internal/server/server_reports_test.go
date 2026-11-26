package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/state"
	"github.com/qiniu/ci-runner/internal/testpg"
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
	paths := []string{"/reports/execution-usage", "/reports/spec-usage", "/reports/overview", "/reports/active", "/reports/" + value.Snapshot + "/groups?dimension=spec", specPage, "/reports/" + value.Snapshot + "/requests"}
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

func TestRunnerReportSpecRuntimeSortWithMissingSamples(t *testing.T) {
	srv := newTestServerWithoutLoops(t, state.New(t.TempDir()), "http://example.test", &fakeSandbox{}, 10)
	report := &state.RunnerReport{Query: state.RunnerReportQuery{IncludeSpecUsage: true}, SpecItems: []state.ReportSpecUsage{
		{Key: "a-missing"},
		{Key: "z-observed", Runtime: state.ReportDuration{Samples: 1}},
	}}
	srv.reports.put(cachedRunnerReport{id: "test-specs", created: time.Now(), report: report})
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, adminRequest(http.MethodGet, "/reports/test-specs/specs?sort=runtime&limit=1", nil))
	var page struct{ Items []state.ReportSpecUsage }
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil || w.Code != http.StatusOK || len(page.Items) != 1 || page.Items[0].Key != "z-observed" {
		t.Fatalf("expected measured zero on the first page, got %d %s", w.Code, w.Body)
	}
}

func TestRunnerReportSpecSnapshotOnAnotherServer(t *testing.T) {
	dir := t.TempDir()
	store := state.New(dir)
	verifySpecSnapshotOnAnotherServer(t, store, state.New(dir), nil)
}

func TestPublishedAccountSpecSnapshotPostgres(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires a dedicated PostgreSQL test database")
	}
	opts := state.Options{Backend: state.BackendPostgres, DatabaseDSN: testpg.SchemaDSN(t), MigrateOnStart: true}
	verifySpecSnapshotOnAnotherServer(t, state.NewWithOptions(opts), state.NewWithOptions(opts), func() {
		t.Helper()
		if err := state.RefreshAccountReports(t.Context(), opts, false); err != nil {
			t.Fatal(err)
		}
	})
}

func TestPublishedAccountSpecSnapshotRetentionPostgres(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires a dedicated PostgreSQL test database")
	}
	opts := state.Options{Backend: state.BackendPostgres, DatabaseDSN: testpg.SchemaDSN(t), MigrateOnStart: true}
	store := state.NewWithOptions(opts)
	writer := newTestServerWithoutLoops(t, store, "http://example.test", &fakeSandbox{}, 10)
	for i := range 23 {
		_, err := store.UpsertProfile(state.RunnerProfile{
			Name: fmt.Sprintf("spec-%02d", i), Labels: []string{"qiniu"}, TemplateID: "test", MaxConcurrency: 1,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := state.RefreshAccountReports(t.Context(), opts, false); err != nil {
		t.Fatal(err)
	}
	opts.MigrateOnStart = false
	peer := newTestServerWithoutLoops(t, state.NewWithOptions(opts), "http://example.test", &fakeSandbox{}, 10)
	get := func(srv *Server, path string, want int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, adminRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Fatalf("GET %s: got %d %s, want %d", path, w.Code, w.Body, want)
		}
		return w
	}
	var snapshot struct {
		Snapshot string
		Query    state.RunnerReportQuery
		Expires  time.Time `json:"expires_at"`
	}
	before := get(writer, "/reports/spec-usage", http.StatusOK)
	if err := json.Unmarshal(before.Body.Bytes(), &snapshot); err != nil || snapshot.Snapshot == "" || !snapshot.Expires.After(time.Now()) {
		t.Fatalf("invalid spec snapshot: %v", err)
	}
	pageURL := "/reports/" + snapshot.Snapshot + "/specs?limit=20&offset=20&sort=requests"
	page := get(writer, pageURL, http.StatusOK).Body.String()
	var second struct {
		Items  []state.ReportSpecUsage
		Total  int
		Offset int
	}
	if err := json.Unmarshal([]byte(page), &second); err != nil || len(second.Items) != 4 || second.Total != 24 || second.Offset != 20 {
		t.Fatalf("expected four specs on the second page, got %d of %d at %d: %v", len(second.Items), second.Total, second.Offset, err)
	}

	// Account and efficiency use their own budget, even when a ninth save prunes that pool.
	for i := range state.ReportSnapshotLimit + 1 {
		path := "/reports/account-usage"
		if i%2 == 1 {
			path = "/reports/efficiency"
		}
		get(writer, fmt.Sprintf("%s?repository=fixture%%2Frepo-%d", path, i), http.StatusOK)
	}
	var freshURL, freshBody string
	for i := range state.ReportSnapshotLimit - 1 {
		freshURL = fmt.Sprintf("/reports/spec-usage?repository=fixture%%2Frepo-%d", i)
		freshBody = get(writer, freshURL, http.StatusOK).Body.String()
	}
	full := get(writer, "/reports/spec-usage?repository=fixture%2Foverflow", http.StatusServiceUnavailable)
	if full.Header().Get("Retry-After") == "" {
		t.Fatal("capacity response is missing Retry-After")
	}
	if got := get(writer, freshURL, http.StatusOK).Body.String(); got != freshBody {
		t.Fatal("same-query reuse changed after capacity was reached")
	}
	if got := get(peer, "/reports/spec-usage", http.StatusOK).Body.String(); got != before.Body.String() {
		t.Fatal("peer did not reuse the original snapshot with the same query and expiry at capacity")
	}

	query := url.Values{"from": {snapshot.Query.From}, "to": {snapshot.Query.To}, "timezone": {snapshot.Query.Timezone}, "grain": {snapshot.Query.Grain}, "snapshot": {snapshot.Snapshot}}
	snapshotURL := "/reports/spec-usage?" + query.Encode()
	if got := get(peer, snapshotURL, http.StatusOK).Body.String(); got != before.Body.String() {
		t.Fatal("restored dashboard changed after capacity was reached")
	}
	if got := get(peer, pageURL, http.StatusOK).Body.String(); got != page {
		t.Fatal("restored second page changed after capacity was reached")
	}

	// Advancing the stored deadline avoids sleeping for the HTTP snapshot TTL.
	db, err := sql.Open("pgx", opts.DatabaseDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), `UPDATE report_spec_snapshots SET expires_at = clock_timestamp() - interval '1 second' WHERE id = $1`, snapshot.Snapshot); err != nil {
		t.Fatal(err)
	}
	afterExpiry := newTestServerWithoutLoops(t, state.NewWithOptions(opts), "http://example.test", &fakeSandbox{}, 10)
	get(afterExpiry, snapshotURL, http.StatusGone)
	get(afterExpiry, pageURL, http.StatusGone)
	get(writer, "/reports/spec-usage?repository=fixture%2Foverflow", http.StatusOK)
}

func verifySpecSnapshotOnAnotherServer(t *testing.T, store, peer state.Store, publish func()) {
	t.Helper()
	first := newTestServerWithoutLoops(t, store, "http://example.test", &fakeSandbox{}, 10)
	second := newTestServerWithoutLoops(t, peer, "http://example.test", &fakeSandbox{}, 10)
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
	if publish != nil {
		_, _, err := store.CreateRequest(state.RunnerRequest{
			ID: "published-spec-request", Source: "fixture", RepositoryFullName: "team/one",
			ProfileName: "another-spec", ProfileSource: "global", CreatedAt: time.Now().UTC().Add(-time.Hour),
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		publish()
	}
	body := get(first, "/reports/spec-usage", http.StatusOK)
	var dashboard struct {
		Snapshot string
		Query    state.RunnerReportQuery
		Totals   state.ReportTotals
	}
	if err := json.Unmarshal([]byte(body), &dashboard); err != nil {
		t.Fatal(err)
	}
	if publish != nil && dashboard.Totals.Requests != 1 {
		t.Fatalf("published spec dashboard missed the fixture: %s", body)
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
	if publish != nil {
		publish()
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
	if _, err := store.LoadReportSnapshot(t.Context(), dashboard.Snapshot, time.Now().Add(reportRetention)); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("expired snapshot: %v", err)
	}
}

func TestRunnerReportAccountPagesAfterRestart(t *testing.T) {
	dir := t.TempDir()
	verifyAccountPagesAfterRestart(t, state.New(dir), state.New(dir), nil)
}

func TestPublishedAccountHTTPPostgres(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL _test database")
	}
	opts := state.Options{Backend: state.BackendPostgres, DatabaseDSN: testpg.SchemaDSN(t), MigrateOnStart: true}
	publish := func() {
		t.Helper()
		if err := state.RefreshAccountReports(t.Context(), opts, false); err != nil {
			t.Fatal(err)
		}
	}
	verifyAccountPagesAfterRestart(t, state.NewWithOptions(opts), state.NewWithOptions(opts), publish)
	verifyEfficiencySnapshotAndBacklog(t, state.NewWithOptions(opts), state.NewWithOptions(opts), "/reports/efficiency", publish)
	if os.Getenv("RUNNERD_REPORT_SCALE_TEST") == "1" {
		t.Run("default_30_days", func(t *testing.T) { verifyPublishedAccountHTTPScale(t, opts) })
	}
}

func TestPublishedAccountServerStartupPostgres(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL _test database")
	}
	opts := state.Options{Backend: state.BackendPostgres, DatabaseDSN: testpg.SchemaDSN(t), MigrateOnStart: true}
	pool, err := sql.Open("pgx", opts.DatabaseDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for range 2 {
		srv := newTestServerWithoutLoops(t, state.NewWithOptions(opts), "http://example.test", &fakeSandbox{}, 10)
		srv.cfg.StateBackend = opts.Backend
		srv.cfg.StateDatabaseDSN = config.Secret(opts.DatabaseDSN)
		srv.Start()
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var exists bool
		if err := pool.QueryRowContext(ctx, "SELECT to_regclass('report_account_publications') IS NOT NULL").Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists {
			var published bool
			err := pool.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM report_account_publications WHERE slot=1 AND published_at IS NOT NULL)").Scan(&published)
			if err != nil {
				t.Fatal(err)
			}
			if published {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("normal server startup did not publish account reports")
}

// A fresh server for each request exercises authentication, query selection,
// persisted snapshot encoding and publication as well as reading the dashboard.
func verifyPublishedAccountHTTPScale(t *testing.T, opts state.Options) {
	t.Helper()
	pool, err := sql.Open("pgx", opts.DatabaseDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	// Fixed daily timestamps keep the default current/prior cohorts comparable
	// across the two independently observed HTTP requests.
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -59)
	_, err = pool.ExecContext(t.Context(), `WITH fixture AS (
		SELECT i,'http-scale-'||i AS id,$1::timestamptz + (i%60)*interval '24 hours' AS at,
			(i/60)%10 AS kind,
			CASE WHEN i%60=59 THEN interval '0' ELSE ((i/60)%100)*interval '1 second' END AS duration
		FROM generate_series(1,510000) i)
		INSERT INTO runner_requests(id,runner_name,source,labels_json,status,repository_full_name,profile_source,profile_name,
			github_installation_id,queued_at,creating_at,running_at,stopping_at,completed_at,failed_at,
			failure_stage,failure_reason,termination_source,updated_at)
		SELECT id,id,'http-fixture','[]',CASE WHEN kind<3 THEN 'failed' ELSE 'completed' END,
			'repo-'||(i%161),'global','spec-'||(i%4),1+i%9,at,
			CASE WHEN kind>0 THEN at+duration END, CASE WHEN kind>=2 THEN at+2*duration END,
			CASE WHEN kind>=2 THEN at+3*duration END, CASE WHEN kind>=3 THEN at+4*duration END,
			CASE WHEN kind<3 THEN at+4*duration END,
			CASE kind WHEN 0 THEN 'admission' WHEN 1 THEN 'creating' WHEN 2 THEN 'runner' END,
			CASE kind WHEN 0 THEN 'labels' WHEN 1 THEN 'setup' WHEN 2 THEN 'exit' END,
			CASE WHEN kind>=2 THEN 'process_exit' END, at FROM fixture`, start)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a rolling upgrade before a compatible publication exists.
	if _, err := pool.ExecContext(t.Context(), "UPDATE report_account_publications SET definition=0"); err != nil {
		t.Fatal(err)
	}
	type result struct {
		Totals, Previous state.ReportTotals
		Distributions    map[string]state.ReportDistribution
		Trend            []state.ReportBucket
		Efficiency       *state.EfficiencySummary
	}
	read := func(path string, usePublished bool) result {
		t.Helper()
		opts.MigrateOnStart = false
		srv := newTestServerWithoutLoops(t, state.NewWithOptions(opts), "http://example.test", &fakeSandbox{}, 10)
		w := httptest.NewRecorder()
		begin := time.Now()
		srv.ServeHTTP(w, adminRequest(http.MethodGet, path, nil))
		elapsed := time.Since(begin)
		if path == "/reports/efficiency" && !usePublished && w.Code == http.StatusGatewayTimeout {
			// The synchronous path can exceed the real HTTP deadline. The state
			// scale test separately reconciles both reads at exactly the same instant.
			t.Logf("source efficiency HTTP 504 in %s", elapsed)
			return result{}
		}
		if usePublished && elapsed >= reportTimeout {
			t.Fatalf("%s exceeded the published response deadline: %s", path, elapsed)
		}
		if w.Code != 200 {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
		var r result
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		if r.Totals.Requests < 250000 || len(r.Distributions) != 3 {
			t.Fatal("default dashboard missed the fixture")
		}
		if path == "/reports/efficiency" {
			var details struct {
				Available bool `json:"request_detail_available"`
				Snapshot  string
			}
			if err := json.Unmarshal(w.Body.Bytes(), &details); err != nil || r.Efficiency == nil || r.Efficiency.StartupP95 == nil || r.Efficiency.CleanupP95 == nil || r.Efficiency.CreationP95 == nil || len(r.Efficiency.Rejections["labels"]) == 0 || details.Available || !strings.HasPrefix(details.Snapshot, efficiencySnapshotPrefix) {
				t.Fatal("large efficiency dashboard lost summary or aggregate-only snapshot")
			}
			t.Logf("P95 seconds startup/creation/cleanup: %g/%g/%g, samples %d/%d/%d", *r.Efficiency.StartupP95, *r.Efficiency.CreationP95, *r.Efficiency.CleanupP95, r.Totals.Startup.Samples, r.Totals.Creation.Samples, r.Totals.Cleanup.Samples)
			peer := newTestServerWithoutLoops(t, state.NewWithOptions(opts), "http://example.test", &fakeSandbox{}, 10)
			for _, test := range []struct {
				suffix string
				status int
			}{{"/groups?dimension=repository&offset=20", 200}, {"/requests?offset=20", 422}} {
				w := httptest.NewRecorder()
				peer.ServeHTTP(w, adminRequest(http.MethodGet, "/reports/"+details.Snapshot+test.suffix, nil))
				if w.Code != test.status {
					t.Fatalf("restored efficiency %s: %d", test.suffix, w.Code)
				}
			}
		}
		t.Logf("%s default 30 days, published=%v, %d + %d requests, cold HTTP %d in %s", path, usePublished, r.Totals.Requests, r.Previous.Requests, w.Code, elapsed)
		return r
	}
	old := read("/reports/account-usage", false)
	oldEfficiency := read("/reports/efficiency", false)
	if err := state.RefreshAccountReports(t.Context(), opts, true); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if got := read("/reports/account-usage", true); !reflect.DeepEqual(got, old) {
			t.Fatal("published HTTP metrics differ from source")
		}
		if got := read("/reports/efficiency", true); oldEfficiency.Efficiency != nil && !reflect.DeepEqual(got, oldEfficiency) {
			t.Fatal("published efficiency HTTP metrics differ from source")
		}
	}
}

func verifyAccountPagesAfterRestart(t *testing.T, store, peer state.Store, publish func()) {
	first := newTestServerWithoutLoops(t, store, "http://example.test", &fakeSandbox{}, 10)
	second := newTestServerWithoutLoops(t, peer, "http://example.test", &fakeSandbox{}, 10)
	get := func(srv *Server, path string, want int) string {
		t.Helper()
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, adminRequest(http.MethodGet, path, nil))
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w.Body.String()
	}
	add := func(i int) {
		t.Helper()
		id := fmt.Sprintf("fixture-%03d", i)
		if _, _, err := store.CreateRequest(state.RunnerRequest{ID: id, Source: "fixture", RepositoryFullName: "fork/" + id, GitHubInstallationID: 12}, nil); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 24; i++ {
		add(i)
	}
	if publish != nil {
		get(first, "/reports/account-usage", 200)
		publish()
		get(first, "/reports/account-usage", 200)
	}
	body := get(first, "/reports/account-usage?owner=installation:12", 200)
	var dashboard struct {
		Snapshot string
		Query    state.RunnerReportQuery
		Totals   state.ReportTotals
	}
	if err := json.Unmarshal([]byte(body), &dashboard); err != nil {
		t.Fatal(err)
	}
	from, _ := time.Parse(time.DateOnly, dashboard.Query.From)
	to, _ := time.Parse(time.DateOnly, dashboard.Query.To)
	if to.Sub(from) != 30*24*time.Hour || dashboard.Totals.Requests != 24 {
		t.Fatalf("default account cohort %+v", dashboard)
	}
	pin := url.Values{"from": {dashboard.Query.From}, "to": {dashboard.Query.To}, "owner": {"installation:12"}, "snapshot": {dashboard.Snapshot}}
	base := "/reports/" + dashboard.Snapshot
	groupPath := base + "/groups?dimension=repository&sort=runtime&offset=20"
	requestPath := base + "/requests?offset=20"
	// Each endpoint must be able to restore first, before a dashboard has warmed the process.
	for i, path := range []string{groupPath, requestPath, "/reports/account-usage?" + pin.Encode()} {
		second.reports.entries = nil
		before := get(first, path, 200)
		add(25 + i)
		if publish != nil {
			publish()
		}
		if got := get(second, path, 200); got != before {
			t.Fatalf("restored %s changed", path)
		}
		if path == groupPath || path == requestPath {
			var page struct {
				Items         []json.RawMessage
				Total, Offset int
			}
			if err := json.Unmarshal([]byte(before), &page); err != nil || len(page.Items) != 4 || page.Total != 24 || page.Offset != 20 {
				t.Fatalf("second page %s", before)
			}
		} else if before != body {
			t.Fatal("dashboard changed")
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		w := httptest.NewRecorder()
		second.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("ordinary account read %s: %d", path, w.Code)
		}
	}
	get(second, "/reports/spec-usage?"+pin.Encode(), 410)
	pin.Set("owner", "installation:13")
	get(second, "/reports/account-usage?"+pin.Encode(), 410)
	get(second, "/reports/account-usage?repository_missing=1&repository=x", 400)
	get(second, "/reports/account-usage?snapshot=acct-missing", 410)
}

func TestRunnerReportEfficiencySnapshotAndBacklog(t *testing.T) {
	dir := t.TempDir()
	verifyEfficiencySnapshotAndBacklog(t, state.New(dir), state.New(dir), "/reports/efficiency?repository=org%2Frepo", nil)
}

func verifyEfficiencySnapshotAndBacklog(t *testing.T, store, peerStore state.Store, path string, publish func()) {
	t.Helper()
	srv := newTestServerWithoutLoops(t, store, "http://example.test", &fakeSandbox{}, 10)
	peer := newTestServerWithoutLoops(t, peerStore, "http://example.test", &fakeSandbox{}, 10)
	if _, _, err := store.CreateRequest(state.RunnerRequest{ID: "eff-sample", Source: "fixture", RepositoryFullName: "org/repo"}, nil); err != nil {
		t.Fatal(err)
	}
	reject := func(id string) {
		t.Helper()
		if _, _, err := store.CreateRejectedRequest(state.RunnerRequest{ID: id, Source: "fixture", RepositoryFullName: "org/repo"}, nil, "profile_labels_not_matched"); err != nil {
			t.Fatal(err)
		}
	}
	reject("rejected-before-snapshot")
	get := func(s *Server, path string, admin bool, want int) []byte {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		if admin {
			r = adminRequest(http.MethodGet, path, nil)
		} else {
			r.AddCookie(testSessionCookie("hubot-id", "hubot", "user"))
		}
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		return w.Body.Bytes()
	}
	if publish != nil {
		publish()
	}
	var report struct {
		Snapshot   string
		Efficiency *state.EfficiencySummary
	}
	body := get(srv, path, true, 200)
	if err := json.Unmarshal(body, &report); err != nil || report.Efficiency == nil {
		t.Fatal("missing efficiency summary")
	}
	reject("rejected-after-snapshot")
	if publish != nil {
		publish()
	}
	pinned, err := url.Parse(path)
	if err != nil {
		t.Fatal(err)
	}
	params := pinned.Query()
	params.Set("snapshot", report.Snapshot)
	pinned.RawQuery = params.Encode()
	if got := get(peer, pinned.String(), true, 200); string(got) != string(body) {
		t.Fatal("restored efficiency dashboard changed")
	}
	details := "/reports/" + report.Snapshot + "/requests?dimension=admission_reason&key=profile_labels_not_matched&limit=20"
	var page struct {
		Items []state.ReportRequest
		Total int
	}
	if err := json.Unmarshal(get(peer, details, true, 200), &page); err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != "rejected-before-snapshot" {
		t.Fatalf("restored request evidence: %+v, %v", page, err)
	}
	get(peer, "/reports/account-usage?snapshot="+report.Snapshot, true, 410)
	get(peer, "/reports/efficiency?snapshot=eff-missing", true, 410)
	const backlog = "/reports/backlog?status=queued&minutes=30"
	get(peer, backlog, true, 200)
	get(peer, backlog+"&from=2020-01-01", true, 400)
	get(peer, "/reports/backlog?status=running&minutes=30", true, 400)
	for _, p := range []string{path, details, backlog} {
		get(peer, p, false, 401)
	}
}
