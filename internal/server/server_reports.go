package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
	"golang.org/x/sync/singleflight"
)

const (
	efficiencySnapshotPrefix = "eff-"
	accountSnapshotPrefix    = "acct-"
	reportFreshness          = 30 * time.Second
	reportRetention          = 5 * time.Minute
	reportTimeout            = 10 * time.Second
	reportCacheEntries       = state.ReportSnapshotLimit
	reportCacheRows          = state.ReportSnapshotEntries
)

func isUsageSnapshot(id string) bool {
	return strings.HasPrefix(id, accountSnapshotPrefix) || strings.HasPrefix(id, efficiencySnapshotPrefix)
}

type cachedRunnerReport struct {
	id, key       string
	created       time.Time
	report        *state.RunnerReport
	distributions map[string]state.ReportDistribution
}

func (e cachedRunnerReport) checkRestoreDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if time.Since(e.created) >= reportRetention {
		return state.ErrNotFound
	}
	return nil
}

type timedRunnerReport struct {
	entry  cachedRunnerReport
	timing runnerReportTiming
}

type runnerReportTiming struct {
	cache, readPath string
	shared          bool

	slotWait, read, charts                        time.Duration
	snapshotEncode, snapshotWrite, snapshotLookup time.Duration
	snapshotDecode, specDashboard, responseEncode time.Duration
}

func (t runnerReportTiming) writeHeader(w http.ResponseWriter) {
	var metrics []string
	if t.cache != "" {
		metrics = append(metrics, fmt.Sprintf("report_cache;desc=%q", t.cache))
	}
	if t.readPath != "" {
		metrics = append(metrics, fmt.Sprintf("report_read_path;desc=%q", t.readPath))
	}
	if t.shared {
		metrics = append(metrics, "report_shared")
	}
	for _, phase := range []struct {
		name     string
		duration time.Duration
	}{
		{"report_slot_wait", t.slotWait},
		{"report_read", t.read},
		{"report_charts", t.charts},
		{"report_snapshot_encode", t.snapshotEncode},
		{"report_snapshot_write", t.snapshotWrite},
		{"report_snapshot_lookup", t.snapshotLookup},
		{"report_snapshot_decode", t.snapshotDecode},
		{"report_spec_dashboard", t.specDashboard},
		{"report_response_encode", t.responseEncode},
	} {
		if phase.duration > 0 {
			metrics = append(metrics, fmt.Sprintf("%s;dur=%.3f", phase.name, float64(phase.duration)/float64(time.Millisecond)))
		}
	}
	if len(metrics) != 0 {
		w.Header().Add("Server-Timing", strings.Join(metrics, ", "))
	}
}

func (s *Server) writeTimedRunnerReport(w http.ResponseWriter, r *http.Request, timing runnerReportTiming, report any) {
	start := time.Now()
	body, err := json.Marshal(report)
	timing.responseEncode = time.Since(start)
	timing.writeHeader(w)
	if err != nil {
		s.writeReportError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(append(body, '\n'))
}

// Only aggregated data needed by the Specs UI is shared. In particular the
// request rows and request-detail indexes in RunnerReport are JSON-excluded.
type sharedSpecSnapshot struct {
	Report        *state.RunnerReport
	Items         []state.ReportSpecUsage
	Summary       *state.ReportSpecUsage
	Distributions map[string]state.ReportDistribution
	Created       time.Time
}

func (s *Server) saveSpecSnapshot(ctx context.Context, e cachedRunnerReport, timing *runnerReportTiming) (cachedRunnerReport, error) {
	start := time.Now()
	payload, err := json.Marshal(sharedSpecSnapshot{e.report, e.report.SpecItems, e.report.SpecSummary, e.distributions, e.created})
	timing.snapshotEncode = time.Since(start)
	if err != nil {
		return cachedRunnerReport{}, err
	}
	start = time.Now()
	saved, err := s.store.SaveSpecReportSnapshot(ctx, e.id, e.key, e.created.Add(reportRetention), e.report.RetainedEntries()-len(e.report.Rows), payload)
	timing.snapshotWrite = time.Since(start)
	if err != nil {
		return cachedRunnerReport{}, err
	}
	if saved.ReusedPayload != nil {
		timing.cache = "snapshot_reuse"
		start = time.Now()
		entry, err := decodeSpecSnapshot(saved.ID, saved.ReusedPayload)
		timing.snapshotDecode = time.Since(start)
		return entry, err
	}
	e.id = saved.ID
	return e, nil
}

func (s *Server) restoreSpecSnapshot(ctx context.Context, id string) (cachedRunnerReport, runnerReportTiming, error) {
	ctx, cancel := context.WithTimeout(ctx, reportTimeout)
	defer cancel()
	timing := runnerReportTiming{cache: "snapshot_lookup"}
	start := time.Now()
	raw, err := s.store.LoadReportSnapshot(ctx, id, start)
	timing.snapshotLookup = time.Since(start)
	if err != nil {
		return cachedRunnerReport{}, timing, err
	}
	start = time.Now()
	entry, err := decodeSpecSnapshot(id, raw)
	timing.snapshotDecode = time.Since(start)
	if err != nil {
		return cachedRunnerReport{}, timing, err
	}
	if err := entry.checkRestoreDeadline(ctx); err != nil {
		return cachedRunnerReport{}, timing, err
	}
	return entry, timing, nil
}

func decodeSpecSnapshot(id string, payload []byte) (cachedRunnerReport, error) {
	var saved sharedSpecSnapshot
	if err := json.Unmarshal(payload, &saved); err != nil {
		return cachedRunnerReport{}, err
	}
	if saved.Report == nil {
		return cachedRunnerReport{}, errors.New("invalid stored spec snapshot")
	}
	report := saved.Report
	report.Query.IncludeSpecUsage = true
	report.SpecItems, report.SpecSummary = saved.Items, saved.Summary
	return cachedRunnerReport{id: id, key: reportQueryKey(report.Query), created: saved.Created, report: report, distributions: saved.Distributions}, nil
}

type runnerReportCache struct {
	mu          sync.Mutex
	entries     []cachedRunnerReport
	active      []cachedReportActive
	group       singleflight.Group
	build       chan struct{}
	activeBuild chan struct{}
}

type cachedReportActive struct {
	key        string
	ObservedAt time.Time        `json:"observed_at"`
	Counts     map[string]int64 `json:"counts"`
}

func (c *runnerReportCache) findActive(key string) (cachedReportActive, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.active {
		if e.key == key && time.Since(e.ObservedAt) < 5*time.Second {
			return e, true
		}
	}
	return cachedReportActive{}, false
}

func (s *Server) loadReportActive(ctx context.Context, q state.RunnerReportQuery) (cachedReportActive, error) {
	// Date and grain do not change the current backlog.
	q.From, q.To, q.Timezone, q.Grain = "", "", "", ""
	key := reportQueryKey(q)
	if entry, ok := s.reports.findActive(key); ok {
		return entry, nil
	}
	result := s.reports.group.DoChan("active:"+key, func() (any, error) {
		if entry, ok := s.reports.findActive(key); ok {
			return entry, nil
		}
		queryCtx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		defer cancel()
		release, err := s.reports.acquireActive(queryCtx)
		if err != nil {
			return nil, err
		}
		defer release()
		counts, err := s.store.ReadRunnerReportActive(queryCtx, q)
		if err != nil {
			return nil, err
		}
		entry := cachedReportActive{key: key, ObservedAt: time.Now().UTC(), Counts: counts}
		s.reports.mu.Lock()
		s.reports.active = append([]cachedReportActive{entry}, s.reports.active...)
		if len(s.reports.active) > reportCacheEntries {
			s.reports.active = s.reports.active[:reportCacheEntries]
		}
		s.reports.mu.Unlock()
		return entry, nil
	})
	select {
	case <-ctx.Done():
		return cachedReportActive{}, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			return cachedReportActive{}, result.Err
		}
		return result.Val.(cachedReportActive), nil
	}
}

func reportQueryKey(q state.RunnerReportQuery) string {
	b, _ := json.Marshal(q)
	if q.IncludeExecution {
		return "execution:" + string(b)
	}
	if q.IncludeEfficiency {
		return "efficiency:" + string(b)
	}
	if q.IncludeAccountUsage {
		return "accounts:" + string(b)
	}
	if q.IncludeSpecUsage {
		return "specs:" + string(b)
	}
	return string(b)
}

func (c *runnerReportCache) fresh(key string) (cachedRunnerReport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if e.key == key && time.Since(e.created) < reportFreshness {
			return e, true
		}
	}
	return cachedRunnerReport{}, false
}

func (c *runnerReportCache) byID(id string) (cachedRunnerReport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if e.id == id && time.Since(e.created) < reportRetention {
			return e, true
		}
	}
	return cachedRunnerReport{}, false
}

func (c *runnerReportCache) put(e cachedRunnerReport) {
	c.mu.Lock()
	defer c.mu.Unlock()
	kept := make([]cachedRunnerReport, 0, reportCacheEntries)
	n := e.report.RetainedEntries()
	kept = append(kept, e)
	for _, old := range c.entries {
		if old.id == e.id || time.Since(old.created) >= reportRetention || len(kept) >= reportCacheEntries || n+old.report.RetainedEntries() > reportCacheRows {
			continue
		}
		kept = append(kept, old)
		n += old.report.RetainedEntries()
	}
	c.entries = kept
}

func (c *runnerReportCache) acquire(ctx context.Context) (func(), error) {
	c.mu.Lock()
	if c.build == nil {
		c.build = make(chan struct{}, 1)
	}
	ch := c.build
	c.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *runnerReportCache) acquireActive(ctx context.Context) (func(), error) {
	c.mu.Lock()
	if c.activeBuild == nil {
		c.activeBuild = make(chan struct{}, 1)
	}
	ch := c.activeBuild
	c.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *Server) loadRunnerReport(ctx context.Context, q state.RunnerReportQuery) (cachedRunnerReport, runnerReportTiming, error) {
	key := reportQueryKey(q)
	hit := runnerReportTiming{cache: "hit"}
	if e, ok := s.reports.fresh(key); ok {
		return e, hit, nil
	}
	result := s.reports.group.DoChan(key, func() (any, error) {
		if e, ok := s.reports.fresh(key); ok {
			return timedRunnerReport{entry: e, timing: hit}, nil
		}
		build := timedRunnerReport{timing: runnerReportTiming{cache: "build"}}
		queryCtx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		defer cancel()
		start := time.Now()
		release, err := s.reports.acquire(queryCtx)
		build.timing.slotWait = time.Since(start)
		if err != nil {
			return build, err
		}
		defer release()
		queryCtx = state.WithRunnerReportReadPath(queryCtx, func(path string) { build.timing.readPath = path })
		start = time.Now()
		report, err := s.store.ReadRunnerReport(queryCtx, q, start.UTC())
		build.timing.read = time.Since(start)
		if err != nil {
			return build, err
		}
		var distributions map[string]state.ReportDistribution
		if !q.IncludeExecution {
			start := time.Now()
			distributions = report.ChartDistributions()
			build.timing.charts = time.Since(start)
		}
		if err := queryCtx.Err(); err != nil {
			return build, err
		}
		entry := cachedRunnerReport{
			id: newID(), key: key, created: time.Now(),
			report: report, distributions: distributions,
		}
		if q.IncludeAccountUsage || q.IncludeEfficiency {
			prefix := accountSnapshotPrefix
			if q.IncludeEfficiency {
				prefix = efficiencySnapshotPrefix
			}
			entry.id = prefix + entry.id
			if err := s.saveUsageSnapshot(queryCtx, entry, &build.timing); err != nil {
				return build, err
			}
		}
		if q.IncludeSpecUsage {
			entry, err = s.saveSpecSnapshot(queryCtx, entry, &build.timing)
			if err != nil {
				return build, err
			}
		}
		s.reports.put(entry)
		build.entry = entry
		return build, nil
	})
	select {
	case <-ctx.Done():
		return cachedRunnerReport{}, runnerReportTiming{}, ctx.Err()
	case value := <-result:
		build := value.Val.(timedRunnerReport)
		build.timing.shared = value.Shared
		return build.entry, build.timing, value.Err
	}
}

func parseRunnerReportQuery(values url.Values, now time.Time) (state.RunnerReportQuery, error) {
	for k, v := range values {
		if len(v) != 1 {
			return state.RunnerReportQuery{}, errors.New("repeated report parameters are not supported")
		}
		switch k {
		case "from", "to", "timezone", "grain", "owner", "repository", "repository_missing", "spec", "snapshot":
		default:
			return state.RunnerReportQuery{}, errors.New("unsupported report parameter")
		}
	}
	q := state.RunnerReportQuery{From: values.Get("from"), To: values.Get("to"), Timezone: values.Get("timezone"), Grain: values.Get("grain"), Owner: values.Get("owner"), Repository: values.Get("repository"), MissingRepository: values.Get("repository_missing"), Spec: values.Get("spec")}
	if q.Timezone == "" {
		q.Timezone = "UTC"
	}
	loc, err := time.LoadLocation(q.Timezone)
	if err != nil {
		return q, errors.New("invalid timezone")
	}
	if q.Grain == "" {
		q.Grain = "day"
	}
	if q.From == "" && q.To == "" {
		q.From, q.To = state.DefaultReportDates(now, loc)
	}
	_, err = q.Window(now)
	return q, err
}

func (s *Server) handleRunnerReport(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	q, err := parseRunnerReportQuery(r.URL.Query(), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	q.IncludeExecution = r.URL.Path == "/reports/execution-usage"
	q.IncludeEfficiency = r.URL.Path == "/reports/efficiency"
	q.IncludeAccountUsage = r.URL.Path == "/reports/account-usage"
	q.IncludeSpecUsage = r.URL.Path == "/reports/spec-usage"
	var entry cachedRunnerReport
	var timing runnerReportTiming
	if id := r.URL.Query().Get("snapshot"); id != "" {
		var ok bool
		entry, ok = s.reports.byID(id)
		switch {
		case ok:
			timing.cache = "snapshot_hit"
		case (q.IncludeAccountUsage && strings.HasPrefix(id, accountSnapshotPrefix)) || (q.IncludeEfficiency && strings.HasPrefix(id, efficiencySnapshotPrefix)):
			entry, timing, err = s.restoreUsageSnapshot(r.Context(), id)
		case q.IncludeSpecUsage && !isUsageSnapshot(id):
			entry, timing, err = s.restoreSpecSnapshot(r.Context(), id)
		}
		if err == nil && (entry.report == nil || entry.key != reportQueryKey(q)) {
			timing.writeHeader(w)
			writeError(w, http.StatusGone, "report snapshot expired or filters changed; refresh the report")
			return
		}
	} else {
		entry, timing, err = s.loadRunnerReport(r.Context(), q)
	}
	if err != nil {
		timing.writeHeader(w)
		s.writeReportError(w, r, err)
		return
	}
	if q.IncludeExecution {
		s.writeTimedRunnerReport(w, r, timing, map[string]any{
			"query": q, "observed_at": entry.report.ObservedAt, "execution": entry.report.Execution,
			"snapshot": entry.id, "expires_at": entry.created.Add(reportRetention),
		})
		return
	}
	var dashboard *reportSpecDashboard
	if q.IncludeSpecUsage {
		start := time.Now()
		dashboard = newReportSpecDashboard(entry.report)
		timing.specDashboard = time.Since(start)
	}
	s.writeTimedRunnerReport(w, r, timing, struct {
		*state.RunnerReport
		Snapshot      string                              `json:"snapshot"`
		ExpiresAt     time.Time                           `json:"expires_at"`
		Distributions map[string]state.ReportDistribution `json:"distributions"`
		SpecDashboard *reportSpecDashboard                `json:"spec_dashboard,omitempty"`
	}{entry.report, entry.id, entry.created.Add(reportRetention), entry.distributions, dashboard})
}

func (s *Server) writeReportError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusGone, "report snapshot expired; refresh the report")
		return
	}
	if errors.Is(err, state.ErrSpecSnapshotCapacity) {
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if errors.Is(err, state.ErrReportTooLarge) || errors.Is(err, state.ErrReportTooManyGroups) || errors.Is(err, state.ErrReportSnapshotTooLarge) {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}
	s.logAdminReadFailure(r.Context(), "runner_reports", err)
	if errors.Is(err, context.DeadlineExceeded) {
		writeError(w, http.StatusGatewayTimeout, "report query timed out; narrow the date range or retry")
		return
	}
	writeError(w, http.StatusInternalServerError, "report query failed")
}

func (s *Server) handleRunnerReportActive(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	q, err := parseRunnerReportQuery(r.URL.Query(), time.Now())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	entry, err := s.loadReportActive(r.Context(), q)
	if err != nil {
		s.writeReportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entry)
}

func reportPagination(values url.Values) (int, int, error) {
	limit, offset := 20, 0
	for key := range values {
		switch key {
		case "limit", "offset", "dimension", "key", "sort", "q":
		default:
			return 0, 0, errors.New("unsupported report parameter")
		}
		if len(values[key]) > 1 {
			return 0, 0, errors.New("repeated report parameters are not supported")
		}
	}
	var err error
	if raw := values.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, errors.New("invalid report limit")
		}
	}
	if raw := values.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil {
			return 0, 0, errors.New("invalid report offset")
		}
	}
	if limit < 1 || limit > 100 || offset < 0 || offset > state.ReportMaxRows {
		return 0, 0, errors.New("report limit must be 1–100 and offset 0–100000")
	}
	if len(values.Get("q")) > 256 || len(values.Get("key")) > 2048 {
		return 0, 0, errors.New("report filter is too long")
	}
	return limit, offset, nil
}

func (s *Server) reportDetail(w http.ResponseWriter, r *http.Request) (cachedRunnerReport, int, int, bool) {
	if !s.requireAdminAuth(w, r) {
		return cachedRunnerReport{}, 0, 0, false
	}
	w.Header().Set("Cache-Control", "no-store")
	limit, offset, err := reportPagination(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return cachedRunnerReport{}, 0, 0, false
	}
	id := r.PathValue("snapshot")
	entry, ok := s.reports.byID(id)
	var timing runnerReportTiming
	switch {
	case ok:
		timing.cache = "snapshot_hit"
	case isUsageSnapshot(id):
		entry, timing, err = s.restoreUsageSnapshot(r.Context(), id)
	case r.Pattern == "GET /reports/{snapshot}/specs":
		entry, timing, err = s.restoreSpecSnapshot(r.Context(), id)
	default:
		writeError(w, http.StatusGone, "report snapshot expired; refresh the report")
		return entry, 0, 0, false
	}
	timing.writeHeader(w)
	if err != nil {
		s.writeReportError(w, r, err)
		return entry, 0, 0, false
	}
	if entry.report.Execution != nil {
		writeError(w, http.StatusBadRequest, "period usage snapshots contain calendar buckets only")
		return entry, 0, 0, false
	}
	return entry, limit, offset, true
}

func (s *Server) handleRunnerReportGroups(w http.ResponseWriter, r *http.Request) {
	entry, limit, offset, ok := s.reportDetail(w, r)
	if !ok {
		return
	}
	sortBy := r.URL.Query().Get("sort")
	if sortBy == "" {
		sortBy = "requests"
	}
	groups, err := entry.report.Groups(r.URL.Query().Get("dimension"), sortBy)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if search := strings.ToLower(r.URL.Query().Get("q")); search != "" {
		filtered := []state.ReportGroup{}
		for _, g := range groups {
			if strings.Contains(strings.ToLower(g.Label), search) {
				filtered = append(filtered, g)
			}
		}
		groups = filtered
	}
	total := len(groups)
	start := min(offset, total)
	end := min(start+limit, total)
	writeJSON(w, http.StatusOK, struct {
		Items    []state.ReportGroup `json:"items"`
		Total    int                 `json:"total"`
		Offset   int                 `json:"offset"`
		Snapshot string              `json:"snapshot"`
	}{groups[start:end], total, start, entry.id})
}

type reportSpecDashboard struct {
	Summary          *state.ReportSpecUsage  `json:"summary"`
	Catalog          int                     `json:"catalog"`
	UsedCurrent      int                     `json:"used_current"`
	HistoricalGroups int                     `json:"historical_groups"`
	Total            int                     `json:"total"`
	Items            []state.ReportSpecUsage `json:"items"`
}

func newReportSpecDashboard(report *state.RunnerReport) *reportSpecDashboard {
	items := report.SpecItems
	page := &reportSpecDashboard{Summary: report.SpecSummary, Total: len(items), Items: items[:min(20, len(items))]}
	for _, item := range items {
		if item.Current {
			page.Catalog++
			if item.Requests > 0 {
				page.UsedCurrent++
			}
		} else {
			page.HistoricalGroups++
		}
	}
	return page
}

func (s *Server) handleRunnerReportSpecs(w http.ResponseWriter, r *http.Request) {
	entry, limit, offset, ok := s.reportDetail(w, r)
	if !ok {
		return
	}
	if !entry.report.Query.IncludeSpecUsage {
		writeError(w, http.StatusBadRequest, "spec pagination requires a snapshot from /reports/spec-usage")
		return
	}
	// Sorting must never mutate the retained order of the chart snapshot.
	items := append([]state.ReportSpecUsage{}, entry.report.SpecItems...)
	key := r.URL.Query().Get("key")
	search := strings.ToLower(r.URL.Query().Get("q"))
	if key != "" || search != "" {
		filtered := items[:0]
		for _, item := range items {
			if (key == "" || item.Key == key) && strings.Contains(strings.ToLower(item.Label), search) {
				filtered = append(filtered, item)
			}
		}
		items = filtered
	}
	switch sortBy := r.URL.Query().Get("sort"); sortBy {
	case "", "requests":
		sort.Slice(items, func(i, j int) bool {
			if items[i].Requests != items[j].Requests {
				return items[i].Requests > items[j].Requests
			}
			return items[i].Key < items[j].Key
		})
	case "runtime":
		sort.Slice(items, func(i, j int) bool {
			if items[i].Runtime.Seconds != items[j].Runtime.Seconds {
				return items[i].Runtime.Seconds > items[j].Runtime.Seconds
			}
			if (items[i].Runtime.Samples > 0) != (items[j].Runtime.Samples > 0) {
				return items[i].Runtime.Samples > 0
			}
			return items[i].Key < items[j].Key
		})
	case "average_runtime":
		sort.Slice(items, func(i, j int) bool {
			left, right := reportOptionalFloat(items[i].AverageRuntimeSeconds), reportOptionalFloat(items[j].AverageRuntimeSeconds)
			if left != right {
				return left > right
			}
			return items[i].Key < items[j].Key
		})
	case "repositories":
		sort.Slice(items, func(i, j int) bool {
			if items[i].Repositories != items[j].Repositories {
				return items[i].Repositories > items[j].Repositories
			}
			return items[i].Key < items[j].Key
		})
	default:
		writeError(w, http.StatusBadRequest, "invalid report spec sort")
		return
	}
	total := len(items)
	start := min(offset, total)
	end := min(start+limit, total)
	writeJSON(w, http.StatusOK, struct {
		Items    []state.ReportSpecUsage `json:"items"`
		Total    int                     `json:"total"`
		Offset   int                     `json:"offset"`
		Snapshot string                  `json:"snapshot"`
	}{items[start:end], total, start, entry.id})
}

func reportOptionalFloat(value *float64) float64 {
	if value == nil {
		return -1
	}
	return *value
}

func (s *Server) handleRunnerReportRequests(w http.ResponseWriter, r *http.Request) {
	entry, limit, offset, ok := s.reportDetail(w, r)
	if !ok {
		return
	}
	rows, err := entry.report.Requests(r.URL.Query().Get("dimension"), r.URL.Query().Get("key"))
	if errors.Is(err, state.ErrReportTooLarge) {
		s.writeReportError(w, r, err)
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	total := len(rows)
	start := min(offset, total)
	end := min(start+limit, total)
	writeJSON(w, http.StatusOK, struct {
		Items    []state.ReportRequest `json:"items"`
		Total    int                   `json:"total"`
		Offset   int                   `json:"offset"`
		Snapshot string                `json:"snapshot"`
	}{rows[start:end], total, start, entry.id})
}

func (s *Server) handleReportBacklog(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	values := r.URL.Query()
	status := values.Get("status")
	minutes, err := strconv.Atoi(values.Get("minutes"))
	if len(values["status"]) != 1 || len(values["minutes"]) != 1 || err != nil || minutes < 1 || minutes > 10080 || (status != state.StatusQueued && status != state.StatusCreating && status != state.StatusStopping) {
		writeError(w, http.StatusBadRequest, "expected queued, creating or stopping and a threshold of 1–10080 minutes")
		return
	}
	values.Del("status")
	values.Del("minutes")
	for key := range values {
		switch key {
		case "owner", "repository", "repository_missing", "spec":
		default:
			writeError(w, http.StatusBadRequest, "unsupported backlog parameter")
			return
		}
	}
	now := time.Now().UTC()
	q, err := parseRunnerReportQuery(values, now)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), reportTimeout)
	defer cancel()
	release, err := s.reports.acquireActive(ctx)
	if err != nil {
		s.writeReportError(w, r, err)
		return
	}
	defer release()
	now = time.Now().UTC()
	rows, more, err := s.store.ReadReportBacklog(ctx, q, status, minutes, now)
	if err != nil {
		s.writeReportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Items      []state.ReportBacklogItem `json:"items"`
		ObservedAt time.Time                 `json:"observed_at"`
		More       bool                      `json:"more"`
	}{rows, now, more})
}
