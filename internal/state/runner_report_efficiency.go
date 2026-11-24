package state

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
)

// Duration evidence is copied only into efficiency snapshots. A nil duration
// remains distinguishable from an observed zero, including after restoration.
type EfficiencyEvidence struct {
	Startup         *float64 `json:"startup,omitempty"`
	Creation        *float64 `json:"creation,omitempty"`
	Cleanup         *float64 `json:"cleanup,omitempty"`
	FailureClass    string   `json:"failure_class,omitempty"`
	Stage           string   `json:"stage,omitempty"`
	Reason          string   `json:"reason,omitempty"`
	Termination     *string  `json:"termination,omitempty"`
	AdmissionBucket string   `json:"admission_bucket,omitempty"`
}

type EfficiencyGroup struct {
	Dimension string `json:"dimension"`
	Key       string `json:"key"`
	Count     int64  `json:"count"`
}
type EfficiencySummary struct {
	StartupP95  *float64                    `json:"startup_p95"`
	CreationP95 *float64                    `json:"creation_p95"`
	CleanupP95  *float64                    `json:"cleanup_p95"`
	Groups      []EfficiencyGroup           `json:"groups"`
	Rejections  map[string]map[string]int64 `json:"rejections,omitempty"`
}

func efficiencyEvidence(p reportProjection, row ReportRequest) *EfficiencyEvidence {
	e := &EfficiencyEvidence{Startup: row.startup, Creation: row.creation, Cleanup: row.cleanup}
	if row.terminal {
		e.Termination = &p.TerminationSource
	}
	if p.Status == StatusFailed {
		switch {
		case row.rejected:
			e.FailureClass = "admission"
		case row.started:
			e.FailureClass = "started"
		default:
			e.FailureClass = "before_start"
		}
		e.Stage, e.Reason = p.FailureStage, p.FailureReason
	}
	return e
}

func isEfficiencyDimension(d string) bool {
	switch d {
	case "startup", "creation", "cleanup", "admission_bucket_reason", "admission_bucket", "admission_reason", "started_stage", "started_reason", "before_start_stage", "before_start_reason", "termination":
		return true
	}
	return false
}

func (e *EfficiencyEvidence) matches(d, k string) bool {
	if e == nil {
		return false
	}
	switch d {
	case "startup":
		return e.Startup != nil && k == ""
	case "creation":
		return e.Creation != nil && k == ""
	case "cleanup":
		return e.Cleanup != nil && k == ""
	case "termination":
		return e.Termination != nil && *e.Termination == k
	case "admission_bucket_reason":
		return e.FailureClass == "admission" && k == e.AdmissionBucket+":"+e.Reason
	case "admission_bucket":
		return e.FailureClass == "admission" && e.AdmissionBucket == k
	default:
		return e.FailureClass != "" && ((d == e.FailureClass+"_stage" && e.Stage == k) || (d == e.FailureClass+"_reason" && e.Reason == k))
	}
}

func (s *EfficiencySummary) addRejections(reason, bucket string, count int64) {
	if s.Rejections == nil {
		s.Rejections = make(map[string]map[string]int64)
	}
	if s.Rejections[reason] == nil {
		s.Rejections[reason] = make(map[string]int64)
	}
	s.Rejections[reason][bucket] += count
}

func summarizeEfficiency(rows []ReportRequest) *EfficiencySummary {
	s := &EfficiencySummary{Groups: []EfficiencyGroup{}}
	counts := make(map[[2]string]int64)
	var startup, creation, cleanup []float64
	for _, r := range rows {
		e := r.Efficiency
		if e.Startup != nil {
			startup = append(startup, *e.Startup)
		}
		if e.Creation != nil {
			creation = append(creation, *e.Creation)
		}
		if e.Cleanup != nil {
			cleanup = append(cleanup, *e.Cleanup)
		}
		if e.Termination != nil {
			counts[[2]string{"termination", *e.Termination}]++
		}
		if e.FailureClass != "" {
			counts[[2]string{e.FailureClass + "_reason", e.Reason}]++
			if e.FailureClass == "admission" {
				s.addRejections(e.Reason, e.AdmissionBucket, 1)
			} else {
				counts[[2]string{e.FailureClass + "_stage", e.Stage}]++
			}
		}
	}
	s.StartupP95, s.CreationP95, s.CleanupP95 = reportP95(startup), reportP95(creation), reportP95(cleanup)
	for k, n := range counts {
		s.Groups = append(s.Groups, EfficiencyGroup{k[0], k[1], n})
	}
	sort.Slice(s.Groups, func(i, j int) bool {
		a, b := s.Groups[i], s.Groups[j]
		if a.Dimension != b.Dimension {
			return a.Dimension < b.Dimension
		}
		return a.Key < b.Key
	})
	return s
}

// Use the same repeatable-read transaction as the PostgreSQL overview rollup.
// Percentiles are nearest-rank over individual valid samples, never averages of buckets.
func readPostgresEfficiency(db *gorm.DB, r *RunnerReport) error {
	base, err := reportBase(db, r.Query)
	if err != nil {
		return err
	}
	base = base.Where("r.queued_at >= ? AND r.queued_at < ?", r.Window.Start.UTC(), r.Window.End.UTC())
	current := base.Session(&gorm.Session{}).Select(`r.queued_at,r.creating_at,r.running_at,r.stopping_at,
 CASE r.status WHEN 'completed' THEN r.completed_at WHEN 'failed' THEN r.failed_at END AS ended_at`)
	var p95 struct{ Startup, Creation, Cleanup *float64 }
	var cols []string
	for _, m := range []struct{ name, start, end string }{{"startup", "queued_at", "running_at"}, {"creation", "creating_at", "running_at"}, {"cleanup", "stopping_at", "ended_at"}} {
		cols = append(cols, fmt.Sprintf("percentile_disc(0.95) WITHIN GROUP (ORDER BY least(extract(epoch FROM (%[3]s-%[2]s)),9223372036.854775807)) FILTER (WHERE %[2]s >= timestamptz '0002-01-01 UTC' AND %[3]s BETWEEN %[2]s AND bounds.observed) AS %[1]s", m.name, m.start, m.end))
	}
	if err := db.Raw("SELECT "+strings.Join(cols, ",")+" FROM (?) samples CROSS JOIN (SELECT ?::timestamptz AS observed) bounds", current, r.ObservedAt).Scan(&p95).Error; err != nil {
		return err
	}
	failures := base.Session(&gorm.Session{}).Where("r.status IN ?", []string{StatusFailed, StatusCompleted}).Select(`r.status, coalesce(r.failure_stage,'') AS stage, coalesce(r.failure_reason,'') AS reason,
 coalesce(r.termination_source,'') AS termination, CASE WHEN r.failure_stage='admission' THEN 'admission' WHEN r.running_at BETWEEN timestamptz '0002-01-01 UTC' AND ? THEN 'started' ELSE 'before_start' END AS class`, r.ObservedAt)
	groups := []EfficiencyGroup{}
	err = db.Raw(`SELECT dim COLLATE "C" AS dimension, value COLLATE "C" AS key, count(*) AS count FROM (?) t
 CROSS JOIN LATERAL (VALUES ('termination',termination),
 (CASE WHEN status='failed' THEN class||'_reason' END,reason),
 (CASE WHEN status='failed' AND class<>'admission' THEN class||'_stage' END,stage)) kinds(dim,value)
 WHERE dim IS NOT NULL GROUP BY 1,2 ORDER BY 1,2 LIMIT ?`, failures, ReportMaxRows+1).Scan(&groups).Error
	if err != nil {
		return err
	}
	if len(groups) > ReportMaxRows {
		return ErrReportTooManyGroups
	}
	summary := &EfficiencySummary{StartupP95: p95.Startup, CreationP95: p95.Creation, CleanupP95: p95.Cleanup, Groups: groups}
	rejected := base.Session(&gorm.Session{}).Where("r.status = ? AND r.failure_stage = ?", StatusFailed, "admission").Select("r.queued_at, r.failure_reason")
	var buckets []struct {
		Reason, Bucket string
		Count          int64
	}
	err = db.Raw(`SELECT coalesce(failure_reason,'') COLLATE "C" AS reason,
  to_char(date_trunc(?, timezone(make_interval(secs => ?), queued_at)), 'YYYY-MM-DD') AS bucket,
  count(*) AS count FROM (?) rejected GROUP BY 1,2 LIMIT ?`, r.Query.Grain, postgresTrendOffset(r.Window), rejected, ReportMaxRows-len(groups)+1).Scan(&buckets).Error
	if err != nil {
		return err
	}
	if len(groups)+len(buckets) > ReportMaxRows {
		return ErrReportTooManyGroups
	}
	for _, b := range buckets {
		summary.addRejections(b.Reason, b.Bucket, b.Count)
	}
	r.Efficiency = summary
	return nil
}

type ReportBacklogItem struct {
	ID         string     `json:"id"`
	Status     string     `json:"status"`
	Repository string     `json:"repository"`
	Since      *time.Time `json:"since"`
	Seconds    *float64   `json:"seconds"`
}

// Select directly on the stored timestamp of the current state. Missing or
// inconsistent evidence remains visible independently of the age threshold.
func (s *DBStore) ReadReportBacklog(ctx context.Context, q RunnerReportQuery, status string, minutes int, now time.Time) ([]ReportBacklogItem, bool, error) {
	field, ok := map[string]string{StatusQueued: "queue_entered_at", StatusCreating: "creating_at", StatusStopping: "stopping_at"}[status]
	if !ok || minutes < 1 || minutes > 10080 {
		return nil, false, fmt.Errorf("invalid backlog status or threshold")
	}
	db, closeReader, err := s.reportDB(ctx)
	if err != nil {
		return nil, false, err
	}
	defer closeReader()
	query, err := reportBase(db, q)
	if err != nil {
		return nil, false, err
	}
	field = "r." + field
	query = query.Where("r.status = ?", status).Where("("+field+" IS NULL OR "+field+" <= ? OR "+field+" > ? OR "+field+" < r.queued_at)", now.Add(-time.Duration(minutes)*time.Minute).UTC(), now.UTC())
	// Limit on the server, but distinguish a full result from a truncated list.
	const limit = 100
	var rows []struct {
		ID, Status, RepositoryFullName                   string
		QueuedAt, QueueEnteredAt, CreatingAt, StoppingAt *time.Time
	}
	query = query.Order(field + " ASC, r.id ASC").Limit(limit + 1)
	if db.Dialector.Name() == BackendSQLite {
		if err := materializeSQLiteReport(db, query.Select("r.id AS request_id")); err != nil {
			return nil, false, err
		}
		query = db.Table("temp.report_result AS b CROSS JOIN runner_requests AS r ON r.id=b.request_id").Order("b.rowid")
	}
	if err := query.Select("r.id,r.status,r.repository_full_name,r.queued_at,r.queue_entered_at,r.creating_at,r.stopping_at").Scan(&rows).Error; err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	result := make([]ReportBacklogItem, 0, min(limit, len(rows)))
	for _, r := range rows[:min(limit, len(rows))] {
		since := r.QueueEnteredAt
		switch status {
		case StatusCreating:
			since = r.CreatingAt
		case StatusStopping:
			since = r.StoppingAt
		}
		item := ReportBacklogItem{ID: r.ID, Status: r.Status, Repository: r.RepositoryFullName}
		if validReportTime(since, now) && (r.QueuedAt == nil || !since.Before(*r.QueuedAt)) {
			utc := since.UTC()
			item.Since = &utc
			item.Seconds = reportSeconds(since, &now, now)
		}
		result = append(result, item)
	}
	return result, more, nil
}
