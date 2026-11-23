package server

import (
	"context"
	"encoding/json"
	"errors"
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
	reportFreshness    = 30 * time.Second
	reportRetention    = 5 * time.Minute
	reportTimeout      = 10 * time.Second
	reportCacheEntries = 8
	reportCacheRows    = 200000
)

type cachedRunnerReport struct {
	id, key       string
	created       time.Time
	report        *state.RunnerReport
	distributions map[string]state.ReportDistribution
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
	if q.IncludeSpecUsage {
		return "specs:" + string(b)
	}
	return string(b)
}
func (c *runnerReportCache) find(id, key string, now time.Time) (cachedRunnerReport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, e := range c.entries {
		if now.Sub(e.created) >= reportRetention {
			continue
		}
		if id != "" {
			if e.id == id && e.key == key {
				return e, true
			}
		} else if e.key == key && now.Sub(e.created) < reportFreshness {
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
		if time.Since(old.created) >= reportRetention || len(kept) >= reportCacheEntries || n+old.report.RetainedEntries() > reportCacheRows {
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

func (s *Server) loadRunnerReport(ctx context.Context, q state.RunnerReportQuery) (cachedRunnerReport, error) {
	key := reportQueryKey(q)
	if e, ok := s.reports.find("", key, time.Now()); ok {
		return e, nil
	}
	result := s.reports.group.DoChan(key, func() (any, error) {
		if e, ok := s.reports.find("", key, time.Now()); ok {
			return e, nil
		}
		queryCtx, cancel := context.WithTimeout(context.Background(), reportTimeout)
		defer cancel()
		release, err := s.reports.acquire(queryCtx)
		if err != nil {
			return nil, err
		}
		defer release()
		now := time.Now().UTC()
		report, err := s.store.ReadRunnerReport(queryCtx, q, now)
		if err != nil {
			return nil, err
		}
		distributions := report.ChartDistributions()
		if err := queryCtx.Err(); err != nil {
			return nil, err
		}
		entry := cachedRunnerReport{
			id: newID(), key: key, created: time.Now(),
			report: report, distributions: distributions,
		}
		s.reports.put(entry)
		return entry, nil
	})
	select {
	case <-ctx.Done():
		return cachedRunnerReport{}, ctx.Err()
	case value := <-result:
		if value.Err != nil {
			return cachedRunnerReport{}, value.Err
		}
		return value.Val.(cachedRunnerReport), nil
	}
}

func parseRunnerReportQuery(values url.Values, now time.Time) (state.RunnerReportQuery, error) {
	for k, v := range values {
		if len(v) != 1 {
			return state.RunnerReportQuery{}, errors.New("repeated report parameters are not supported")
		}
		switch k {
		case "from", "to", "timezone", "grain", "owner", "repository", "spec", "snapshot":
		default:
			return state.RunnerReportQuery{}, errors.New("unsupported report parameter")
		}
	}
	q := state.RunnerReportQuery{From: values.Get("from"), To: values.Get("to"), Timezone: values.Get("timezone"), Grain: values.Get("grain"), Owner: values.Get("owner"), Repository: values.Get("repository"), Spec: values.Get("spec")}
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
		local := now.In(loc)
		end := time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, time.UTC)
		q.To = end.Format(time.DateOnly)
		q.From = end.AddDate(0, 0, -30).Format(time.DateOnly)
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
	q.IncludeSpecUsage = r.URL.Path == "/reports/spec-usage"
	var entry cachedRunnerReport
	if id := r.URL.Query().Get("snapshot"); id != "" {
		var ok bool
		entry, ok = s.reports.find(id, reportQueryKey(q), time.Now())
		if !ok {
			writeError(w, http.StatusGone, "report snapshot expired or filters changed; refresh the report")
			return
		}
	} else {
		entry, err = s.loadRunnerReport(r.Context(), q)
	}
	if err != nil {
		s.writeReportError(w, r, err)
		return
	}
	var dashboard *reportSpecDashboard
	if q.IncludeSpecUsage {
		dashboard = newReportSpecDashboard(entry.report)
	}
	writeJSON(w, http.StatusOK, struct {
		*state.RunnerReport
		Snapshot      string                              `json:"snapshot"`
		ExpiresAt     time.Time                           `json:"expires_at"`
		Distributions map[string]state.ReportDistribution `json:"distributions"`
		SpecDashboard *reportSpecDashboard                `json:"spec_dashboard,omitempty"`
	}{entry.report, entry.id, entry.created.Add(reportRetention), entry.distributions, dashboard})
}

func (s *Server) writeReportError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, state.ErrReportTooLarge) || errors.Is(err, state.ErrReportTooManyGroups) {
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
	entry, ok := s.reports.byID(r.PathValue("snapshot"))
	if !ok {
		writeError(w, http.StatusGone, "report snapshot expired; refresh the report")
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
	// Sorting must never mutate the retained order of the chart snapshot.
	items := append([]state.ReportSpecUsage{}, entry.report.SpecItems...)
	if !entry.report.Query.IncludeSpecUsage {
		ctx, cancel := context.WithTimeout(r.Context(), reportTimeout)
		defer cancel()
		profiles, err := s.store.ListProfilesContext(ctx)
		if err != nil {
			s.writeReportError(w, r, err)
			return
		}
		items = entry.report.SpecUsages(profiles)
	}
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
