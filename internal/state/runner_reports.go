package state

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// ReportMaxRows bounds a report's current and comparison cohorts together.
const ReportMaxRows = 100000

var ErrReportTooLarge = errors.New("report exceeds 100000 requests; narrow the date range or filters")

// RunnerReportQuery uses local calendar dates and an exclusive To boundary.
type RunnerReportQuery struct {
	From       string `json:"from"`
	To         string `json:"to"`
	Timezone   string `json:"timezone"`
	Grain      string `json:"grain"`
	Owner      string `json:"owner,omitempty"`
	Repository string `json:"repository,omitempty"`
	Spec       string `json:"spec,omitempty"`
}

type ReportWindow struct {
	Start         time.Time      `json:"start"`
	End           time.Time      `json:"end"`
	PreviousStart time.Time      `json:"previous_start"`
	PreviousEnd   time.Time      `json:"previous_end"`
	Location      *time.Location `json:"-"`
}

func (q RunnerReportQuery) Window(now time.Time) (ReportWindow, error) {
	loc, err := time.LoadLocation(q.Timezone)
	if err != nil || q.Timezone == "" || q.Timezone == "Local" {
		return ReportWindow{}, errors.New("timezone must be an IANA timezone")
	}
	start, err := time.ParseInLocation(time.DateOnly, q.From, loc)
	if err != nil {
		return ReportWindow{}, errors.New("from must be YYYY-MM-DD")
	}
	end, err := time.ParseInLocation(time.DateOnly, q.To, loc)
	if err != nil {
		return ReportWindow{}, errors.New("to must be YYYY-MM-DD (exclusive)")
	}
	days := int(time.Date(end.Year(), end.Month(), end.Day(), 0, 0, 0, 0, time.UTC).Sub(time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)).Hours() / 24)
	today := now.In(loc)
	tomorrow := time.Date(today.Year(), today.Month(), today.Day()+1, 0, 0, 0, 0, loc)
	if days < 1 || days > 366 || !start.Before(now) || end.After(tomorrow) {
		return ReportWindow{}, errors.New("report range must be 1–366 calendar days and cannot extend past tomorrow")
	}
	if q.Grain != "day" && q.Grain != "week" && q.Grain != "month" {
		return ReportWindow{}, errors.New("grain must be day, week or month")
	}
	if len(q.Repository) > 256 || len(q.Owner) > 100 || len(q.Spec) > 2048 {
		return ReportWindow{}, errors.New("report filter is too long")
	}
	if q.Owner != "" {
		if _, _, err := parseReportOwner(q.Owner); err != nil {
			return ReportWindow{}, err
		}
	}
	if q.Spec != "" {
		if _, err := decodeReportSpec(q.Spec); err != nil {
			return ReportWindow{}, err
		}
	}
	if end.After(now) {
		end = now.In(loc)
	}
	return ReportWindow{Start: start, End: end, PreviousStart: start.AddDate(0, 0, -days), PreviousEnd: end.AddDate(0, 0, -days), Location: loc}, nil
}

type reportSpecIdentity struct {
	Source    string `json:"source"`
	ScopeType string `json:"scope_type"`
	ScopeID   int64  `json:"scope_id"`
	Name      string `json:"name"`
}

func (v reportSpecIdentity) canonical() reportSpecIdentity {
	// Global profiles are platform-wide; saved request scope identifies the
	// consumer, whereas scoped_custom scope is part of the profile identity.
	if v.Source == "global" {
		v.ScopeType, v.ScopeID = "", 0
	}
	return v
}

func reportSpecKey(v reportSpecIdentity) string {
	v = v.canonical()
	b, _ := json.Marshal(v)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeReportSpec(key string) (reportSpecIdentity, error) {
	var value reportSpecIdentity
	data, err := base64.RawURLEncoding.DecodeString(key)
	if err != nil || json.Unmarshal(data, &value) != nil || reportSpecKey(value) != key {
		return value, errors.New("invalid spec identity")
	}
	return value, nil
}

func parseReportOwner(key string) (string, int64, error) {
	parts := strings.Split(key, ":")
	if len(parts) != 2 || (parts[0] != "github" && parts[0] != "installation") {
		return "", 0, errors.New("invalid owner identity")
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || id < 0 || (parts[0] == "github" && id == 0) || strconv.FormatInt(id, 10) != parts[1] {
		return "", 0, errors.New("invalid owner identity")
	}
	return parts[0], id, nil
}

// ReportDuration keeps unavailable samples out of sums and averages.
type ReportDuration struct {
	Seconds  float64 `json:"seconds"`
	Samples  int64   `json:"samples"`
	Eligible int64   `json:"eligible"`
}

type ReportTotals struct {
	Requests           int64          `json:"requests"`
	Started            int64          `json:"started"`
	Rejected           int64          `json:"rejected"`
	Terminal           int64          `json:"terminal"`
	ActiveAccounts     int64          `json:"active_accounts"`
	ActiveRepositories int64          `json:"active_repositories"`
	MappedAccounts     int64          `json:"mapped_accounts"`
	KnownSpecs         int64          `json:"known_specs"`
	CreatingTimes      int64          `json:"creating_times"`
	StoppingTimes      int64          `json:"stopping_times"`
	TerminalTimes      int64          `json:"terminal_times"`
	JobResults         int64          `json:"job_results"`
	JobResultEligible  int64          `json:"job_result_eligible"`
	Runtime            ReportDuration `json:"runtime"`
	Startup            ReportDuration `json:"startup"`
	Creation           ReportDuration `json:"creation"`
	Cleanup            ReportDuration `json:"cleanup"`
	Completion         ReportDuration `json:"completion"`
}

type ReportRequest struct {
	ID             string    `json:"id"`
	QueuedAt       time.Time `json:"queued_at"`
	Repository     string    `json:"repository"`
	Owner          string    `json:"owner"`
	OwnerLabel     string    `json:"owner_label"`
	Spec           string    `json:"spec"`
	SpecLabel      string    `json:"spec_label"`
	Status         string    `json:"status"`
	JobResult      string    `json:"job_result"`
	RuntimeSeconds *float64  `json:"runtime_seconds"`
	// Aggregate-only evidence is not exposed as a raw database row.
	started, rejected, terminal, mappedOwner, knownSpec, creatingTime, stoppingTime, terminalTime, jobEligible bool
	startup, creation, cleanup, completion                                                                     *float64
}

type ReportBucket struct {
	Date string `json:"date"`
	ReportTotals
}
type ReportGroup struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	ReportTotals
}
type RunnerReport struct {
	Query      RunnerReportQuery `json:"query"`
	Window     ReportWindow      `json:"window"`
	ObservedAt time.Time         `json:"observed_at"`
	Totals     ReportTotals      `json:"totals"`
	Previous   ReportTotals      `json:"previous"`
	Trend      []ReportBucket    `json:"trend"`
	// Rows are retained only in the bounded server cache for consistent drilldowns.
	Rows []ReportRequest `json:"-"`
}

type reportProjection struct {
	ID                   string
	QueuedAt             time.Time
	RepositoryFullName   string
	ProfileName          string
	ProfileSource        string
	ProfileScopeType     string
	ProfileScopeID       int64
	GitHubInstallationID int64 `gorm:"column:github_installation_id"`
	OwnerID              int64
	OwnerLogin           string
	Status               string
	FailureStage         string
	WorkflowJobID        int64
	GitHubJobStatus      string     `gorm:"column:github_job_status"`
	GitHubJobConclusion  string     `gorm:"column:github_job_conclusion"`
	GitHubJobObservedAt  *time.Time `gorm:"column:github_job_observed_at"`
	CreatingAt           *time.Time
	RunningAt            *time.Time
	StoppingAt           *time.Time
	CompletedAt          *time.Time
	FailedAt             *time.Time
}

// A unique installation-owner join avoids fan-out through user-installation links.
func reportBase(db *gorm.DB, q RunnerReportQuery) (*gorm.DB, error) {
	query := db.Table("runner_requests AS r").Joins("LEFT JOIN github_installation_owners AS o ON o.installation_id = r.github_installation_id")
	if q.Repository != "" {
		query = query.Where("COALESCE(r.repository_full_name, '') = ?", q.Repository)
	}
	if q.Owner != "" {
		kind, id, err := parseReportOwner(q.Owner)
		if err != nil {
			return nil, err
		}
		if kind == "github" {
			query = query.Where("o.github_account_id = ?", id)
		} else {
			query = query.Where("COALESCE(o.github_account_id, 0) <= 0 AND COALESCE(r.github_installation_id, 0) = ?", id)
		}
	}
	if q.Spec != "" {
		spec, err := decodeReportSpec(q.Spec)
		if err != nil {
			return nil, err
		}
		query = query.Where("COALESCE(r.profile_source, '') = ? AND COALESCE(r.profile_name, '') = ?", spec.Source, spec.Name)
		if spec.Source != "global" {
			query = query.Where("COALESCE(r.profile_scope_type, '') = ? AND COALESCE(r.profile_scope_id, 0) = ?", spec.ScopeType, spec.ScopeID)
		}
	}
	return query, nil
}

// reportDB isolates long SQLite reads from the scheduler's single connection.
// Resolve the already-open main database rather than guessing from a DSN. The
// extra connection cannot create/write files or run migrations. WAL keeps one
// SELECT consistent while the primary connection continues to admit work.
// In-memory databases retain their original connection and identity.
func (s *DBStore) reportDB(ctx context.Context) (*gorm.DB, func(), error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, nil, err
	}
	if db.Dialector.Name() != BackendSQLite {
		return db.WithContext(ctx), func() {}, nil
	}
	var databases []struct {
		Name string
		File string
	}
	if err := db.WithContext(ctx).Raw("PRAGMA database_list").Scan(&databases).Error; err != nil {
		return nil, nil, err
	}
	filename := ""
	for _, database := range databases {
		if database.Name == "main" {
			filename = database.File
		}
	}
	if filename == "" {
		return db.WithContext(ctx), func() {}, nil
	}
	dsn := (&url.URL{Scheme: "file", Path: filename, RawQuery: "mode=ro"}).String()
	reader, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: db.Logger, DisableForeignKeyConstraintWhenMigrating: true})
	if err != nil {
		return nil, nil, err
	}
	pool, err := reader.DB()
	if err != nil {
		return nil, nil, err
	}
	pool.SetMaxOpenConns(1)
	pool.SetMaxIdleConns(1)
	closeReader := func() { _ = pool.Close() }
	reader = reader.WithContext(ctx)
	for _, pragma := range []string{"PRAGMA query_only = ON", "PRAGMA busy_timeout = 1000"} {
		if err := reader.Exec(pragma).Error; err != nil {
			closeReader()
			return nil, nil, err
		}
	}
	return reader, closeReader, nil
}

// ReadRunnerReport reads one narrow, statement-consistent snapshot. It never loads
// payloads, logs, provider configuration, credentials, or current spec catalogs.
func (s *DBStore) ReadRunnerReport(ctx context.Context, q RunnerReportQuery, now time.Time) (*RunnerReport, error) {
	window, err := q.Window(now)
	if err != nil {
		return nil, err
	}
	db, closeReader, err := s.reportDB(ctx)
	if err != nil {
		return nil, err
	}
	defer closeReader()
	query, err := reportBase(db, q)
	if err != nil {
		return nil, err
	}
	query = query.Where("(r.queued_at >= ? AND r.queued_at < ?) OR (r.queued_at >= ? AND r.queued_at < ?)", window.Start.UTC(), window.End.UTC(), window.PreviousStart.UTC(), window.PreviousEnd.UTC())
	rows, err := query.Select(`r.id, r.queued_at, r.repository_full_name, r.profile_name, r.profile_source, r.profile_scope_type, r.profile_scope_id,
 r.github_installation_id, o.github_account_id AS owner_id, o.account_login AS owner_login, r.status, r.failure_stage,
 r.workflow_job_id, r.github_job_status, r.github_job_conclusion, r.github_job_observed_at,
 r.creating_at, r.running_at, r.stopping_at, r.completed_at, r.failed_at`).Order("r.queued_at DESC, r.id ASC").Limit(ReportMaxRows + 1).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	report := &RunnerReport{Query: q, Window: window, ObservedAt: now.UTC(), Rows: []ReportRequest{}, Trend: []ReportBucket{}}
	current, previous := newReportAccumulator(), newReportAccumulator()
	buckets := map[string]*reportAccumulator{}
	count := 0
	for rows.Next() {
		count++
		if count > ReportMaxRows {
			return nil, ErrReportTooLarge
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var p reportProjection
		if err := db.ScanRows(rows, &p); err != nil {
			return nil, err
		}
		row := p.request(now)
		if !row.QueuedAt.Before(window.Start) && row.QueuedAt.Before(window.End) {
			report.Rows = append(report.Rows, row)
			current.add(row)
			day := reportBucketDate(row.QueuedAt, window.Location, q.Grain)
			if buckets[day] == nil {
				buckets[day] = newReportAccumulator()
			}
			buckets[day].add(row)
		} else {
			previous.add(row)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	report.Totals = current.totals()
	report.Previous = previous.totals()
	// Include empty periods, with week buckets starting on Monday.
	seen := map[string]bool{}
	for day := window.Start; day.Before(window.End); day = day.AddDate(0, 0, 1) {
		key := reportBucketDate(day, window.Location, q.Grain)
		if seen[key] {
			continue
		}
		seen[key] = true
		total := ReportTotals{}
		if buckets[key] != nil {
			total = buckets[key].totals()
		}
		report.Trend = append(report.Trend, ReportBucket{Date: key, ReportTotals: total})
	}
	return report, nil
}

func reportBucketDate(t time.Time, loc *time.Location, grain string) string {
	t = t.In(loc)
	switch grain {
	case "week":
		t = t.AddDate(0, 0, -(int(t.Weekday())+6)%7)
	case "month":
		t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, loc)
	}
	return t.Format(time.DateOnly)
}

func validReportTime(t *time.Time, now time.Time) bool {
	return t != nil && !t.IsZero() && t.Year() > 1 && !t.After(now)
}

func reportSeconds(start, end *time.Time, now time.Time) *float64 {
	if !validReportTime(start, now) || !validReportTime(end, now) || end.Before(*start) {
		return nil
	}
	v := end.Sub(*start).Seconds()
	return &v
}

func (p reportProjection) request(now time.Time) ReportRequest {
	identity := (reportSpecIdentity{p.ProfileSource, p.ProfileScopeType, p.ProfileScopeID, p.ProfileName}).canonical()
	label := p.ProfileName
	if identity.ScopeType != "" {
		label += fmt.Sprintf(" · %s:%d", identity.ScopeType, identity.ScopeID)
	}
	if p.ProfileSource != "" {
		label += " · " + p.ProfileSource
	}
	row := ReportRequest{ID: p.ID, QueuedAt: p.QueuedAt, Repository: p.RepositoryFullName, Spec: reportSpecKey(identity), SpecLabel: label, Status: p.Status, JobResult: "unknown"}
	row.Owner = "installation:" + strconv.FormatInt(p.GitHubInstallationID, 10)
	row.OwnerLabel = row.Owner
	if p.OwnerID > 0 {
		row.Owner = "github:" + strconv.FormatInt(p.OwnerID, 10)
		row.OwnerLabel = p.OwnerLogin
		if row.OwnerLabel == "" {
			row.OwnerLabel = row.Owner
		}
		row.mappedOwner = true
	}
	row.knownSpec = p.ProfileName != "" && (p.ProfileSource == "global" || (p.ProfileSource == "scoped_custom" && p.ProfileScopeID > 0 && (p.ProfileScopeType == "account" || p.ProfileScopeType == "installation")))
	row.started = validReportTime(p.RunningAt, now)
	row.creatingTime = validReportTime(p.CreatingAt, now)
	row.stoppingTime = validReportTime(p.StoppingAt, now)
	row.rejected = p.Status == StatusFailed && p.FailureStage == "admission"
	row.terminal = p.Status == StatusCompleted || p.Status == StatusFailed
	row.jobEligible = row.terminal && p.WorkflowJobID > 0
	if p.WorkflowJobID > 0 && p.GitHubJobStatus == "completed" && p.GitHubJobConclusion != "" && validReportTime(p.GitHubJobObservedAt, now) {
		row.JobResult = p.GitHubJobConclusion
	}
	var end *time.Time
	switch p.Status {
	case StatusCompleted:
		end = p.CompletedAt
	case StatusFailed:
		end = p.FailedAt
	}
	row.terminalTime = validReportTime(end, now)
	row.startup = reportSeconds(&p.QueuedAt, p.RunningAt, now)
	row.creation = reportSeconds(p.CreatingAt, p.RunningAt, now)
	if row.terminal {
		runtimeEnd := end
		if p.StoppingAt != nil && !p.StoppingAt.IsZero() {
			runtimeEnd = p.StoppingAt
		}
		if row.terminalTime && (runtimeEnd == nil || !runtimeEnd.After(*end)) {
			row.RuntimeSeconds = reportSeconds(p.RunningAt, runtimeEnd, now)
		}
		row.cleanup = reportSeconds(p.StoppingAt, end, now)
		row.completion = reportSeconds(&p.QueuedAt, end, now)
	}
	return row
}

type reportAccumulator struct {
	total                ReportTotals
	owners, repositories map[string]struct{}
}

func newReportAccumulator() *reportAccumulator {
	return &reportAccumulator{owners: map[string]struct{}{}, repositories: map[string]struct{}{}}
}

func addReportDuration(metric *ReportDuration, eligible bool, value *float64) {
	if eligible {
		metric.Eligible++
		if value != nil {
			metric.Samples++
			metric.Seconds += *value
		}
	}
}

func (a *reportAccumulator) add(r ReportRequest) {
	a.total.Requests++
	if r.started {
		a.total.Started++
	}
	if r.rejected {
		a.total.Rejected++
	}
	if r.terminal {
		a.total.Terminal++
	}
	if r.mappedOwner {
		a.total.MappedAccounts++
		a.owners[r.Owner] = struct{}{}
	}
	if r.Repository != "" {
		a.repositories[r.Repository] = struct{}{}
	}
	if r.knownSpec {
		a.total.KnownSpecs++
	}
	if r.creatingTime {
		a.total.CreatingTimes++
	}
	if r.stoppingTime {
		a.total.StoppingTimes++
	}
	if r.terminalTime {
		a.total.TerminalTimes++
	}
	if r.jobEligible {
		a.total.JobResultEligible++
		if r.JobResult != "unknown" {
			a.total.JobResults++
		}
	}
	addReportDuration(&a.total.Runtime, r.terminal && r.started, r.RuntimeSeconds)
	addReportDuration(&a.total.Startup, r.started, r.startup)
	addReportDuration(&a.total.Creation, r.started, r.creation)
	addReportDuration(&a.total.Cleanup, r.terminal && r.stoppingTime, r.cleanup)
	addReportDuration(&a.total.Completion, r.terminal, r.completion)
}

func (a *reportAccumulator) totals() ReportTotals {
	t := a.total
	t.ActiveAccounts = int64(len(a.owners))
	t.ActiveRepositories = int64(len(a.repositories))
	return t
}

// Groups aggregates the same immutable cohort used by the summary and requests.
func (r *RunnerReport) Groups(dimension, sortBy string) ([]ReportGroup, error) {
	groups := map[string]*reportAccumulator{}
	labels := map[string]string{}
	for _, row := range r.Rows {
		key, label, err := row.dimension(dimension)
		if err != nil {
			return nil, err
		}
		if groups[key] == nil {
			groups[key] = newReportAccumulator()
		}
		groups[key].add(row)
		if labels[key] == "" || label < labels[key] {
			labels[key] = label
		}
	}
	if dimension != "spec" && dimension != "owner" && dimension != "repository" {
		return nil, errors.New("invalid report dimension")
	}
	if sortBy != "requests" && sortBy != "runtime" {
		return nil, errors.New("invalid report sort")
	}
	result := make([]ReportGroup, 0, len(groups))
	for key, a := range groups {
		result = append(result, ReportGroup{key, labels[key], a.totals()})
	}
	sort.Slice(result, func(i, j int) bool {
		if sortBy == "runtime" && result[i].Runtime.Seconds != result[j].Runtime.Seconds {
			return result[i].Runtime.Seconds > result[j].Runtime.Seconds
		}
		if result[i].Requests != result[j].Requests {
			return result[i].Requests > result[j].Requests
		}
		return result[i].Key < result[j].Key
	})
	return result, nil
}

func (r ReportRequest) dimension(dimension string) (string, string, error) {
	switch dimension {
	case "spec":
		return r.Spec, r.SpecLabel, nil
	case "owner":
		return r.Owner, r.OwnerLabel, nil
	case "repository":
		return r.Repository, r.Repository, nil
	default:
		return "", "", errors.New("invalid report dimension")
	}
}

func (r *RunnerReport) Requests(dimension, key string) ([]ReportRequest, error) {
	if dimension == "" {
		return r.Rows, nil
	}
	if dimension != "spec" && dimension != "owner" && dimension != "repository" {
		return nil, errors.New("invalid report dimension")
	}
	result := []ReportRequest{}
	for _, row := range r.Rows {
		k, _, _ := row.dimension(dimension)
		if k == key {
			result = append(result, row)
		}
	}
	return result, nil
}

// ReadRunnerReportActive is a separate current-state read without a date filter.
func (s *DBStore) ReadRunnerReportActive(ctx context.Context, q RunnerReportQuery) (map[string]int64, error) {
	db, closeReader, err := s.reportDB(ctx)
	if err != nil {
		return nil, err
	}
	defer closeReader()
	query, err := reportBase(db, q)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Status string
		Count  int64
	}
	err = query.Select("r.status, COUNT(*) AS count").Where("r.status IN ?", []string{StatusQueued, StatusCreating, StatusRunning, StatusStopping}).Group("r.status").Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	result := map[string]int64{StatusQueued: 0, StatusCreating: 0, StatusRunning: 0, StatusStopping: 0}
	for _, row := range rows {
		result[row.Status] = row.Count
	}
	return result, nil
}
