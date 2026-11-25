package state

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// ReportMaxRows bounds detail snapshots across both cohorts and the number of PostgreSQL aggregate rows.
const ReportMaxRows = 100000

var ErrReportTooLarge = errors.New("report exceeds 100000 requests; narrow the date range or filters")

// RunnerReportQuery uses local calendar dates and an exclusive To boundary.
type RunnerReportQuery struct {
	IncludeExecution    bool   `json:"-"`
	IncludeEfficiency   bool   `json:"-"`
	IncludeAccountUsage bool   `json:"-"`
	MissingRepository   string `json:"repository_missing,omitempty"`
	IncludeSpecUsage    bool   `json:"-"`
	From                string `json:"from"`
	To                  string `json:"to"`
	Timezone            string `json:"timezone"`
	Grain               string `json:"grain"`
	Owner               string `json:"owner,omitempty"`
	Repository          string `json:"repository,omitempty"`
	Spec                string `json:"spec,omitempty"`
}

type ReportWindow struct {
	Start         time.Time      `json:"start"`
	End           time.Time      `json:"end"`
	PreviousStart time.Time      `json:"previous_start"`
	PreviousEnd   time.Time      `json:"previous_end"`
	Location      *time.Location `json:"-"`
}

// DefaultReportDates returns the civil date labels used by the HTTP API and
// by the worker preparing the default dashboard, including a partial today.
func DefaultReportDates(now time.Time, loc *time.Location) (string, string) {
	end := reportCalendarDate(now, loc).AddDate(0, 0, 1)
	return end.AddDate(0, 0, -30).Format(time.DateOnly), end.Format(time.DateOnly)
}

func (q RunnerReportQuery) Window(now time.Time) (ReportWindow, error) {
	return q.windowAt(now, now)
}

func (q RunnerReportQuery) windowAt(now, validationTime time.Time) (ReportWindow, error) {
	loc, err := time.LoadLocation(q.Timezone)
	if err != nil || q.Timezone == "" || q.Timezone == "Local" {
		return ReportWindow{}, errors.New("timezone must be an IANA timezone")
	}
	start, err := time.Parse(time.DateOnly, q.From)
	if err != nil {
		return ReportWindow{}, errors.New("from must be YYYY-MM-DD")
	}
	end, err := time.Parse(time.DateOnly, q.To)
	if err != nil {
		return ReportWindow{}, errors.New("to must be YYYY-MM-DD (exclusive)")
	}
	// These UTC values are civil labels; do arithmetic before resolving offsets.
	days := int(end.Sub(start).Hours() / 24)
	tomorrow := reportCalendarDate(validationTime, loc).AddDate(0, 0, 1)
	if days < 1 || days > 366 || !start.Before(tomorrow) || end.After(tomorrow) {
		return ReportWindow{}, errors.New("report range must be 1–366 calendar days and cannot extend past tomorrow")
	}

	if q.Grain != "day" && q.Grain != "week" && q.Grain != "month" {
		return ReportWindow{}, errors.New("grain must be day, week or month")
	}
	if q.MissingRepository != "" && (q.MissingRepository != "1" || q.Repository != "") {
		return ReportWindow{}, errors.New("repository_missing requires an empty repository and value 1")
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
	previousStart, previousEnd := start.AddDate(0, 0, -days), end.AddDate(0, 0, -days)
	start, err = resolveReportWallTime(start, loc)
	if err != nil {
		return ReportWindow{}, err
	}
	end, err = resolveReportWallTime(end, loc)
	if err != nil {
		return ReportWindow{}, err
	}
	if end.After(now) {
		end = now.In(loc)
		local := now.In(loc)
		previousEnd = time.Date(local.Year(), local.Month(), local.Day()-days, local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), time.UTC)
	}
	if !start.Before(end) || !start.Before(now) {
		return ReportWindow{}, errors.New("report range must contain elapsed local time")
	}
	previousStart, err = resolveReportWallTime(previousStart, loc)
	if err != nil {
		return ReportWindow{}, err
	}
	previousEnd, err = resolveReportWallTime(previousEnd, loc)
	if err != nil {
		return ReportWindow{}, err
	}
	return ReportWindow{Start: start, End: end, PreviousStart: previousStart, PreviousEnd: previousEnd, Location: loc}, nil
}

// Resolve a civil wall-clock label to its earliest occurrence. For a gap, use
// the first valid instant after the missing time; for a repeated time, use the
// first occurrence. Zone intervals avoid time.Date's unspecified DST choice.
func resolveReportWallTime(civil time.Time, loc *time.Location) (time.Time, error) {
	// IANA offsets and date-line jumps fit within this surrounding four-day span.
	limit := civil.Add(48 * time.Hour)
	for cursor := civil.Add(-48 * time.Hour); cursor.Before(limit); {
		local := cursor.In(loc)
		_, offset := local.Zone()
		_, zoneEnd := local.ZoneBounds()
		candidate := civil.Add(-time.Duration(offset) * time.Second)
		if candidate.Before(cursor) {
			candidate = cursor
		}
		if zoneEnd.IsZero() || candidate.Before(zoneEnd) {
			return candidate.In(loc), nil
		}
		cursor = zoneEnd
	}
	return time.Time{}, errors.New("cannot resolve report date in timezone")
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

func (identity reportSpecIdentity) label() string {
	label := identity.Name
	if identity.Name != "" && identity.ScopeType != "" {
		label += fmt.Sprintf(" · %s:%d", identity.ScopeType, identity.ScopeID)
	}
	if identity.Name != "" && identity.Source != "" {
		label += " · " + identity.Source
	}
	return label
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
	Runtime            ReportDuration `json:"runtime" gorm:"embedded;embeddedPrefix:runtime_"`
	Startup            ReportDuration `json:"startup" gorm:"embedded;embeddedPrefix:startup_"`
	Creation           ReportDuration `json:"creation" gorm:"embedded;embeddedPrefix:creation_"`
	Cleanup            ReportDuration `json:"cleanup" gorm:"embedded;embeddedPrefix:cleanup_"`
	Completion         ReportDuration `json:"completion" gorm:"embedded;embeddedPrefix:completion_"`
}

type ReportRequest struct {
	Efficiency     *EfficiencyEvidence `json:"efficiency,omitempty" gorm:"-"`
	ID             string              `json:"id"`
	QueuedAt       time.Time           `json:"queued_at"`
	Repository     string              `json:"repository"`
	Owner          string              `json:"owner"`
	OwnerLabel     string              `json:"owner_label"`
	Spec           string              `json:"spec"`
	SpecLabel      string              `json:"spec_label"`
	Status         string              `json:"status"`
	JobResult      string              `json:"job_result"`
	RuntimeSeconds *float64            `json:"runtime_seconds"`
	// Aggregate-only evidence is not exposed as a raw database row.
	started, rejected, terminal, mappedOwner, knownSpec, creatingTime, stoppingTime, terminalTime, jobEligible bool
	startup, creation, cleanup, completion                                                                     *float64
}

type ReportBucket struct {
	Date string `json:"date"`
	ReportTotals
}
type ReportGroup struct {
	*ReportActivity `json:",omitempty"`
	Key             string `json:"key"`
	Label           string `json:"label"`
	ReportTotals
}

// ReportSpecUsage combines a report cohort with the current platform catalog.
// Current identifies an exact match in the captured catalog.
// Percentile fields are unavailable for aggregate-only snapshots.
type ReportSpecUsage struct {
	Owners                 *ReportSpecMix      `json:"owner_mix,omitempty"`
	Trend                  []ReportSpecSegment `json:"spec_trend,omitempty"`
	RepositoryMix          *ReportSpecMix      `json:"repository_mix,omitempty"`
	Key                    string              `json:"key"`
	Label                  string              `json:"label"`
	Name                   string              `json:"name"`
	Source                 string              `json:"source"`
	ScopeType              string              `json:"scope_type,omitempty"`
	ScopeID                int64               `json:"scope_id,omitempty"`
	Current                bool                `json:"current"`
	Enabled                bool                `json:"enabled"`
	Published              bool                `json:"published"`
	TemplateSource         string              `json:"template_source,omitempty"`
	Requests               int64               `json:"requests"`
	Started                int64               `json:"started"`
	Repositories           int64               `json:"repositories"`
	Runtime                ReportDuration      `json:"runtime"`
	Creation               ReportDuration      `json:"creation"`
	AverageRuntimeSeconds  *float64            `json:"average_runtime_seconds,omitempty"`
	P95RuntimeSeconds      *float64            `json:"p95_runtime_seconds,omitempty"`
	AverageCreationSeconds *float64            `json:"average_creation_seconds,omitempty"`
	P95CreationSeconds     *float64            `json:"p95_creation_seconds,omitempty"`
	JobResults             map[string]int64    `json:"job_results,omitempty"`
}
type RunnerReport struct {
	Execution       *ExecutionUsage    `json:"execution,omitempty"`
	Efficiency      *EfficiencySummary `json:"efficiency,omitempty"`
	DetailAvailable *bool              `json:"request_detail_available,omitempty"`
	Outcomes        map[string]int64   `json:"outcomes,omitempty"`
	SpecSummary     *ReportSpecUsage   `json:"-"`
	SpecItems       []ReportSpecUsage  `json:"-"`
	specGroups      map[string]*specBreakdown

	Query      RunnerReportQuery `json:"query"`
	Window     ReportWindow      `json:"window"`
	ObservedAt time.Time         `json:"observed_at"`
	Totals     ReportTotals      `json:"totals"`
	Previous   ReportTotals      `json:"previous"`
	Trend      []ReportBucket    `json:"trend"`
	// Bounded detail snapshots retain this narrow projection for consistent drilldowns.
	Rows []ReportRequest `json:"-"`
	// Account snapshots freeze all group pages. Large PostgreSQL cohorts retain only groups.
	GroupTotals map[string][]ReportGroup `json:"-"`
}

const reportProjectionFields = `r.id, r.queued_at, r.repository_full_name, r.profile_name, r.profile_source, r.profile_scope_type, r.profile_scope_id,
 r.github_installation_id, o.github_account_id AS owner_id, o.account_login AS owner_login, r.status, r.failure_stage,
 r.workflow_job_id, r.github_job_status, r.github_job_conclusion, r.github_job_observed_at,
 r.creating_at, r.running_at, r.stopping_at, r.completed_at, r.failed_at`

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
	FailureReason        string
	TerminationSource    string
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

// Keep SQL membership identical to the byte-exact keys used by Groups and
// Requests, including when the database's text collation ignores case.
func reportTextKey(db *gorm.DB, key string) clause.Expr {
	switch db.Dialector.Name() {
	case BackendMySQL:
		return gorm.Expr("CAST(? AS BINARY)", key)
	case BackendPostgres:
		return gorm.Expr(`? COLLATE "C"`, key)
	default:
		return gorm.Expr("? COLLATE BINARY", key)
	}
}

// A unique installation-owner join avoids fan-out through user-installation links.
func reportBase(db *gorm.DB, q RunnerReportQuery) (*gorm.DB, error) {
	return reportDimensionQuery(db, q, "runner_requests AS r")
}

func reportDimensionQuery(db *gorm.DB, q RunnerReportQuery, table string) (*gorm.DB, error) {
	query := db.Table(table).Joins("LEFT JOIN github_installation_owners AS o ON o.installation_id = r.github_installation_id")
	if q.Repository != "" || q.MissingRepository == "1" {
		query = query.Where("COALESCE(r.repository_full_name, '') = ?", reportTextKey(db, q.Repository))
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
		query = query.Where("COALESCE(r.profile_source, '') = ? AND COALESCE(r.profile_name, '') = ?", reportTextKey(db, spec.Source), reportTextKey(db, spec.Name))
		if spec.Source != "global" {
			query = query.Where("COALESCE(r.profile_scope_type, '') = ? AND COALESCE(r.profile_scope_id, 0) = ?", reportTextKey(db, spec.ScopeType), spec.ScopeID)
		}
	}
	return query, nil
}

// reportDB isolates long SQLite reads from the scheduler's single connection.
// Resolve the already-open main database rather than guessing from a DSN. The
// extra connection opens the main database read-only and runs no migrations. WAL keeps one
// transaction consistent while the primary connection continues to admit work.
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
		return sqliteReportTransaction(db.WithContext(ctx), func() {})
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
	for _, pragma := range []string{"PRAGMA temp_store = MEMORY", "PRAGMA busy_timeout = 1000"} {
		if err := reader.Exec(pragma).Error; err != nil {
			closeReader()
			return nil, nil, err
		}
	}
	return sqliteReportTransaction(reader, closeReader)
}

// Acquire the connection under the report deadline, but own rollback explicitly.
// database/sql otherwise discards this driver's connection on transaction
// cancellation, which would erase an entire :memory: database.
func sqliteReportTransaction(reader *gorm.DB, closeReader func()) (*gorm.DB, func(), error) {
	ctx := reader.Statement.Context
	pool, err := reader.DB()
	if err != nil {
		closeReader()
		return nil, nil, err
	}
	conn, err := pool.Conn(ctx)
	if err != nil {
		closeReader()
		return nil, nil, err
	}
	tx, err := conn.BeginTx(context.WithoutCancel(ctx), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		conn.Close()
		closeReader()
		return nil, nil, err
	}
	reader = reader.WithContext(ctx)
	reader.Statement.ConnPool = tx
	return reader, func() {
		_ = tx.Rollback() // also removes the temporary table
		_ = conn.Close()
		closeReader()
	}, nil
}

// The SQLite driver keeps its interrupt watcher active during ExecContext,
// but not during Rows.Next. Finish all filtering, sorting and grouping in one
// interruptible statement; subsequent reads walk only the bounded result.
func materializeSQLiteReport(db, selection *gorm.DB) error {
	err := db.Exec("CREATE TEMP TABLE report_result AS ?", selection).Error
	if ctxErr := db.Statement.Context.Err(); ctxErr != nil {
		return ctxErr
	}
	return err
}

// ReadRunnerReport reads the current and prior cohorts from one consistent snapshot. It never loads
// payloads, logs, provider configuration or credentials. The spec dashboard also reads narrow catalog fields.
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
	if db.Dialector.Name() == BackendPostgres || ((q.IncludeSpecUsage || q.IncludeExecution) && db.Dialector.Name() == BackendMySQL) {
		// The size probe and whichever representation it selects must see the same rows.
		db = db.Begin(&sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if db.Error != nil {
			return nil, db.Error
		}
		defer db.Rollback()
	}
	if q.IncludeAccountUsage && db.Dialector.Name() == BackendPostgres {
		if report, err := readPublishedAccounts(db, q, now); err != nil || report != nil {
			return report, err
		}
	}
	return readSourceReport(ctx, db, q, window, now)
}

func readSourceReport(ctx context.Context, db *gorm.DB, q RunnerReportQuery, window ReportWindow, now time.Time) (*RunnerReport, error) {
	if q.IncludeExecution {
		return readExecutionReport(db, q, window, now)
	}
	report, err := readReportCohort(ctx, db, q, window, now)
	if err != nil {
		return nil, err
	}
	if q.IncludeEfficiency {
		if report.Rows != nil {
			report.Efficiency = summarizeEfficiency(report.Rows)
		} else if err := readPostgresEfficiency(db, report); err != nil {
			return nil, err
		}
	}
	if q.IncludeAccountUsage || q.IncludeEfficiency {
		report.DetailAvailable = new(report.Rows != nil)
		if report.GroupTotals == nil {
			groups := make(map[string][]ReportGroup)
			for _, dim := range []string{"owner", "repository", "spec"} {
				groups[dim] = report.groupRows(dim)
			}
			report.GroupTotals = groups
		}
		if q.IncludeAccountUsage {
			report.Outcomes = make(map[string]int64)
			for _, group := range report.GroupTotals["repository"] {
				for outcome, n := range group.Outcomes {
					report.Outcomes[outcome] += n
				}
			}
		}
		if report.RetainedEntries() > ReportSnapshotEntries {
			return nil, ErrReportTooManyGroups
		}
		return report, ctx.Err()
	}
	if !q.IncludeSpecUsage {
		return report, nil
	}
	catalog, err := readReportSpecCatalog(db, q.Spec)
	if err != nil {
		return nil, err
	}
	report.SpecItems = report.specUsages(catalog)
	report.SpecSummary = report.specUsageSummary()
	report.specGroups = nil
	if len(report.SpecItems) > ReportMaxRows || report.RetainedEntries() > 2*ReportMaxRows {
		return nil, ErrReportTooManyGroups
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return report, nil
}

func readReportCohort(ctx context.Context, db *gorm.DB, q RunnerReportQuery, window ReportWindow, now time.Time) (*RunnerReport, error) {
	query, err := reportBase(db, q)
	if err != nil {
		return nil, err
	}
	query = query.Where("(r.queued_at >= ? AND r.queued_at < ?) OR (r.queued_at >= ? AND r.queued_at < ?)", window.Start.UTC(), window.End.UTC(), window.PreviousStart.UTC(), window.PreviousEnd.UTC())
	if db.Dialector.Name() == BackendPostgres {
		var overflow int
		probe := query.Session(&gorm.Session{}).Select("1").Offset(ReportMaxRows).Limit(1).Scan(&overflow)
		if probe.Error != nil {
			return nil, probe.Error
		}
		if overflow != 0 {
			return postgresAggregateReport(ctx, db, query, q, window, now)
		}
	}
	query = query.Order("r.queued_at DESC, r.id ASC").Limit(ReportMaxRows + 1)
	if db.Dialector.Name() == BackendSQLite {
		if err := materializeSQLiteReport(db, query.Select("r.id AS request_id, o.github_account_id, o.account_login")); err != nil {
			return nil, err
		}
		// CTAS assigns rowids in SELECT order. Drive indexed request lookups
		// from that sequence to retain native timestamp types and avoid a sort.
		query = db.Table("temp.report_result AS o CROSS JOIN runner_requests AS r ON r.id = o.request_id").Order("o.rowid")
	}
	fields := reportProjectionFields
	if q.IncludeEfficiency {
		fields += ", r.failure_reason, r.termination_source"
	}
	rows, err := query.Select(fields).Rows()
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
			day := reportBucketDate(row.QueuedAt, window.Location, q.Grain)
			if q.IncludeEfficiency {
				row.Efficiency = efficiencyEvidence(p, row)
				if row.rejected {
					row.Efficiency.AdmissionBucket = day
				}
			}
			report.Rows = append(report.Rows, row)
			current.add(row)
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	report.Totals = current.totals()
	report.Previous = previous.totals()
	bucketTotals := make(map[string]ReportTotals, len(buckets))
	for key, bucket := range buckets {
		bucketTotals[key] = bucket.totals()
	}
	report.fillTrend(bucketTotals)
	return report, nil
}

func (report *RunnerReport) fillTrend(buckets map[string]ReportTotals) {
	window := report.Window
	// Iterate civil dates in UTC so a missing local midnight cannot normalize
	// to the previous date and shift all later buckets. The last included
	// instant preserves the exclusive end while including an unfinished date.
	firstDate := reportCalendarDate(window.Start, window.Location)
	lastDate := reportCalendarDate(window.End.Add(-time.Nanosecond), window.Location)
	seen := map[string]bool{}
	for day := firstDate; !day.After(lastDate); day = day.AddDate(0, 0, 1) {
		key := reportBucketDate(day, time.UTC, report.Query.Grain)
		if seen[key] {
			continue
		}
		seen[key] = true
		total := buckets[key]
		report.Trend = append(report.Trend, ReportBucket{Date: key, ReportTotals: total})
	}
}

// The UTC value represents a local calendar label, not an execution instant.
func reportCalendarDate(t time.Time, loc *time.Location) time.Time {
	year, month, day := t.In(loc).Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

func reportBucketDate(t time.Time, loc *time.Location, grain string) string {
	t = reportCalendarDate(t, loc)
	switch grain {
	case "week":
		t = t.AddDate(0, 0, -(int(t.Weekday())+6)%7)
	case "month":
		t = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
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
	row := ReportRequest{ID: p.ID, QueuedAt: p.QueuedAt, Repository: p.RepositoryFullName, Spec: reportSpecKey(identity), SpecLabel: identity.label(), Status: p.Status, JobResult: "unknown"}
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
	if dimension != "spec" && dimension != "owner" && dimension != "repository" {
		return nil, errors.New("invalid report dimension")
	}
	if sortBy != "requests" && sortBy != "runtime" {
		return nil, errors.New("invalid report sort")
	}
	groups := r.reportGroups(dimension)
	sortReportGroups(groups, sortBy)
	return groups, nil
}

func (r *RunnerReport) reportGroups(dimension string) []ReportGroup {
	if r.GroupTotals != nil {
		return append([]ReportGroup{}, r.GroupTotals[dimension]...)
	}
	return r.groupRows(dimension)
}

func sortReportGroups(result []ReportGroup, sortBy string) {
	sort.Slice(result, func(i, j int) bool {
		if sortBy == "runtime" {
			if result[i].Runtime.Seconds != result[j].Runtime.Seconds {
				return result[i].Runtime.Seconds > result[j].Runtime.Seconds
			}
			if (result[i].Runtime.Samples > 0) != (result[j].Runtime.Samples > 0) {
				return result[i].Runtime.Samples > 0
			}
		}
		if result[i].Requests != result[j].Requests {
			return result[i].Requests > result[j].Requests
		}
		return result[i].Key < result[j].Key
	})
}

type ReportDistribution struct {
	Total    int           `json:"total"`
	Requests []ReportGroup `json:"requests"`
	Runtime  []ReportGroup `json:"runtime"`
}

// ChartDistributions freezes both Top 5 rankings for the dashboard in the
// overview response. Each dimension is grouped once, including for row snapshots.
func (r *RunnerReport) ChartDistributions() map[string]ReportDistribution {
	distributions := make(map[string]ReportDistribution, 3)
	for _, dimension := range []string{"spec", "owner", "repository"} {
		groups := r.reportGroups(dimension)
		n := min(5, len(groups))
		sortReportGroups(groups, "requests")
		requests := append([]ReportGroup{}, groups[:n]...)
		sortReportGroups(groups, "runtime")
		distributions[dimension] = ReportDistribution{
			Total: len(groups), Requests: requests, Runtime: append([]ReportGroup{}, groups[:n]...),
		}
	}
	return distributions
}

func (r *RunnerReport) groupRows(dimension string) []ReportGroup {
	groups := map[string]*reportAccumulator{}
	labels := map[string]string{}
	activities := map[string]*activityAccumulator{}
	for _, row := range r.Rows {
		key, label, _ := row.dimension(dimension)
		if groups[key] == nil {
			groups[key] = newReportAccumulator()
		}
		groups[key].add(row)
		if r.Query.IncludeAccountUsage && dimension != "spec" {
			if activities[key] == nil {
				activities[key] = newActivityAccumulator()
			}
			activities[key].add(row, r.Window.Location)
		}
		if labels[key] == "" || label < labels[key] {
			labels[key] = label
		}
	}
	result := make([]ReportGroup, 0, len(groups))
	for key, a := range groups {
		group := ReportGroup{Key: key, Label: labels[key], ReportTotals: a.totals()}
		if activity := activities[key]; activity != nil {
			stats := activity.ReportActivity
			group.ReportActivity = &stats
		}
		result = append(result, group)
	}
	return result
}

// RetainedEntries counts both snapshot representations against the cache budget.
func (r *RunnerReport) RetainedEntries() int {
	entries := func(item ReportSpecUsage) int {
		n := 1 + len(item.Trend) + len(item.JobResults)
		if item.Owners != nil {
			n += len(item.Owners.Requests) + len(item.Owners.Runtime)
		}
		if item.RepositoryMix != nil {
			n += len(item.RepositoryMix.Requests) + len(item.RepositoryMix.Runtime)
		}
		return n
	}
	n := len(r.Rows)
	if r.Execution != nil {
		n += len(r.Execution.Buckets)
	}
	if r.Efficiency != nil {
		n += len(r.Efficiency.Groups)
		for _, buckets := range r.Efficiency.Rejections {
			n += len(buckets)
		}
	}
	for _, item := range r.SpecItems {
		n += entries(item)
	}
	if r.SpecSummary != nil {
		n += entries(*r.SpecSummary)
	}
	for _, groups := range r.GroupTotals {
		n += len(groups)
	}
	return n
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
	efficiency := r.Query.IncludeEfficiency && isEfficiencyDimension(dimension)
	if !efficiency && dimension != "" && dimension != "spec" && dimension != "owner" && dimension != "repository" {
		return nil, errors.New("invalid report dimension")
	}
	if r.GroupTotals != nil && r.Rows == nil {
		return nil, ErrReportTooLarge
	}
	if dimension == "" {
		return r.Rows, nil
	}
	result := []ReportRequest{}
	for _, row := range r.Rows {
		if efficiency {
			if row.Efficiency.matches(dimension, key) {
				result = append(result, row)
			}
			continue
		}
		k, _, _ := row.dimension(dimension)
		if k == key {
			result = append(result, row)
		}
	}
	return result, nil
}

func (r *RunnerReport) specUsages(catalog []ReportSpecUsage) []ReportSpecUsage {
	byKey := make(map[string]*ReportSpecUsage)
	for _, item := range catalog {
		identity := reportSpecIdentity{item.Source, item.ScopeType, item.ScopeID, item.Name}
		item.Key, item.Label, item.Current = reportSpecKey(identity), identity.label(), true
		if r.Query.Spec != "" && r.Query.Spec != item.Key {
			continue
		}
		item.JobResults = map[string]int64{}
		byKey[item.Key] = &item
	}
	for _, group := range r.reportGroups("spec") {
		identity, err := decodeReportSpec(group.Key)
		if err != nil {
			continue
		}
		usage := byKey[group.Key]
		if usage == nil {
			usage = &ReportSpecUsage{
				Key: group.Key, Label: group.Label, Name: identity.Name, Source: identity.Source,
				ScopeType: identity.ScopeType, ScopeID: identity.ScopeID,
				JobResults: map[string]int64{},
			}
			byKey[group.Key] = usage
		}
		usage.Requests = group.Requests
		usage.Started = group.Started
		usage.Repositories = group.ActiveRepositories
		usage.Runtime = group.Runtime
		usage.Creation = group.Creation
		usage.AverageRuntimeSeconds = reportMean(group.Runtime)
		usage.AverageCreationSeconds = reportMean(group.Creation)
	}
	for _, row := range r.Rows {
		usage := byKey[row.Spec]
		if usage == nil {
			continue
		}
		usage.JobResults[row.JobResult]++
	}
	if len(r.Rows) > 0 {
		samples := make(map[string]*specUsageSamples)
		for _, row := range r.Rows {
			entry := samples[row.Spec]
			if entry == nil {
				entry = &specUsageSamples{}
				samples[row.Spec] = entry
			}
			if row.RuntimeSeconds != nil {
				entry.runtime = append(entry.runtime, *row.RuntimeSeconds)
			}
			if row.creation != nil {
				entry.creation = append(entry.creation, *row.creation)
			}
		}
		for key, entry := range samples {
			usage := byKey[key]
			if usage == nil {
				continue
			}
			usage.P95RuntimeSeconds = reportP95(entry.runtime)
			usage.P95CreationSeconds = reportP95(entry.creation)
		}
	}
	if r.Query.IncludeSpecUsage {
		for key, b := range r.specBreakdowns() {
			usage := byKey[key]
			usage.RepositoryMix = rankSpecGroups(b.repos)
			usage.Owners = rankSpecGroups(b.owners)
			sort.Slice(b.trend, func(i, j int) bool { return b.trend[i].Key < b.trend[j].Key })
			usage.Trend = b.trend
			if b.outcomes != nil {
				usage.JobResults = b.outcomes
			}
		}
	}
	result := make([]ReportSpecUsage, 0, len(byKey))
	for _, usage := range byKey {
		result = append(result, *usage)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Requests != result[j].Requests {
			return result[i].Requests > result[j].Requests
		}
		return result[i].Key < result[j].Key
	})
	return result
}

type specUsageSamples struct {
	runtime, creation []float64
}

func reportMean(duration ReportDuration) *float64 {
	if duration.Samples == 0 {
		return nil
	}
	mean := duration.Seconds / float64(duration.Samples)
	return &mean
}

func reportP95(values []float64) *float64 {
	if len(values) == 0 {
		return nil
	}
	sort.Float64s(values)
	value := values[int(math.Ceil(float64(len(values))*0.95))-1]
	return &value
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
	query = query.Select("r.status, COUNT(*) AS count").Where("r.status IN ?", []string{StatusQueued, StatusCreating, StatusRunning, StatusStopping}).Group("r.status")
	if db.Dialector.Name() == BackendSQLite {
		if err := materializeSQLiteReport(db, query); err != nil {
			return nil, err
		}
		query = db.Table("temp.report_result")
	}
	if err := query.Scan(&rows).Error; err != nil {
		return nil, err
	}
	result := map[string]int64{StatusQueued: 0, StatusCreating: 0, StatusRunning: 0, StatusStopping: 0}
	for _, row := range rows {
		result[row.Status] = row.Count
	}
	return result, nil
}
