package state

import (
	"container/heap"
	"database/sql"
	"sort"
	"time"

	"gorm.io/gorm"
)

type ExecutionUsage struct {
	Total               ExecutionTotals   `json:"total"`
	CollectionStartedAt time.Time         `json:"collection_started_at"`
	Buckets             []ExecutionBucket `json:"buckets"`
	// Incomplete Jobs are attributable to a window only if a source endpoint falls in it.
	IncompleteJobs int64 `json:"incomplete_jobs"`
}

type ExecutionBucket struct {
	Date string    `json:"date"`
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	ExecutionTotals
}

type ExecutionTotals struct {
	Attempts           int     `json:"attempts"`
	AttemptSeconds     float64 `json:"attempt_seconds"`
	ProvisionalSeconds float64 `json:"provisional_seconds"`
	ActiveAttempts     int     `json:"active_attempts"`
	Peak               int     `json:"peak"`
	Jobs               int     `json:"jobs"`
	JobSeconds         float64 `json:"job_seconds"`
}

// Each bucket receives intervals in ascending start order. Drop ends at or
// before the next start, so adjacent intervals never overlap at the boundary.
type executionEnds []time.Time

func (h executionEnds) Len() int           { return len(h) }
func (h executionEnds) Less(i, j int) bool { return h[i].Before(h[j]) }
func (h executionEnds) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *executionEnds) Push(x any)        { *h = append(*h, x.(time.Time)) }
func (h *executionEnds) Pop() any {
	values := *h
	last := values[len(values)-1]
	*h = values[:len(values)-1]
	return last
}

func (u *ExecutionUsage) add(start, end time.Time, provisional bool, peaks []executionEnds) {
	if end.Before(start) {
		return
	}
	i := sort.Search(len(u.Buckets), func(i int) bool { return u.Buckets[i].To.After(start) })
	if i == len(u.Buckets) || end.Before(u.Buckets[i].From) {
		return
	}
	if start.Equal(end) {
		if !start.Before(u.Buckets[i].From) {
			if peaks == nil {
				u.Buckets[i].Jobs++
				u.Total.Jobs++
			} else {
				u.Buckets[i].Attempts++
				u.Total.Attempts++
			}
		}
		return
	}
	if !end.After(u.Buckets[i].From) {
		return
	}
	if peaks == nil {
		u.Total.Jobs++
	} else {
		u.Total.Attempts++
		if provisional {
			u.Total.ActiveAttempts++
		}
	}
	for ; i < len(u.Buckets); i++ {
		b := &u.Buckets[i]
		if !end.After(b.From) {
			break
		}
		lo, hi := start, end
		if lo.Before(b.From) {
			lo = b.From
		}
		if hi.After(b.To) {
			hi = b.To
		}
		seconds := hi.Sub(lo).Seconds()
		if peaks == nil {
			b.Jobs++
			b.JobSeconds += seconds
			u.Total.JobSeconds += seconds
			continue
		}
		b.Attempts++
		if provisional {
			b.ActiveAttempts++
			b.ProvisionalSeconds += seconds
			u.Total.ProvisionalSeconds += seconds
		} else {
			b.AttemptSeconds += seconds
			u.Total.AttemptSeconds += seconds
		}
		h := &peaks[i]
		for h.Len() > 0 && !(*h)[0].After(lo) {
			heap.Pop(h)
		}
		heap.Push(h, hi)
		b.Peak = max(b.Peak, h.Len())
		u.Total.Peak = max(u.Total.Peak, b.Peak)
	}
}

func readExecutionReport(db *gorm.DB, q RunnerReportQuery, w ReportWindow, now time.Time) (*RunnerReport, error) {
	var capture executionCollection
	if err := db.First(&capture, 1).Error; err != nil {
		return nil, err
	}
	u := &ExecutionUsage{CollectionStartedAt: capture.StartedAt, Buckets: []ExecutionBucket{}}
	report := &RunnerReport{Query: q, ObservedAt: now, Window: w, Execution: u}
	start := w.Start
	if start.Before(capture.StartedAt) {
		start = capture.StartedAt
	}
	if !start.Before(w.End) {
		return report, nil
	}
	// Resolve successive local dates independently across DST and skipped midnights.
	last := reportCalendarDate(w.End.Add(-time.Nanosecond), w.Location)
	for d := reportCalendarDate(start, w.Location); !d.After(last); d = d.AddDate(0, 0, 1) {
		next, err := resolveReportWallTime(d.AddDate(0, 0, 1), w.Location)
		if err != nil {
			return nil, err
		}
		if next.After(w.End) {
			next = w.End
		}
		if !next.After(start) {
			continue
		}
		key := reportBucketDate(d, time.UTC, q.Grain)
		n := len(u.Buckets)
		if n > 0 && u.Buckets[n-1].Date == key {
			u.Buckets[n-1].To = next
		} else {
			u.Buckets = append(u.Buckets, ExecutionBucket{Date: key, From: start, To: next})
		}
		start = next
	}
	from, to := u.Buckets[0].From.UTC(), w.End.UTC()
	attempts, err := executionIntervals(db, q, from, to)
	if err != nil {
		return nil, err
	}
	peaks := make([]executionEnds, len(u.Buckets))
	if err := scanExecutionIntervals(db, attempts, func(start time.Time, stop, end *time.Time) {
		if stop != nil {
			u.add(start, *stop, false, peaks)
		} else if end != nil {
			u.add(start, *end, false, peaks)
		} else {
			u.add(start, w.End, true, peaks)
		}
	}); err != nil {
		return nil, err
	}
	jobs, err := reportBase(db, q)
	if err != nil {
		return nil, err
	}
	complete := jobs.Session(&gorm.Session{}).Select("r.github_job_started_at, r.github_job_completed_at, NULL").Where("r.github_job_started_at < ? AND r.github_job_completed_at >= ?", to, from)
	if err := scanExecutionIntervals(db, complete, func(start time.Time, end, _ *time.Time) { u.add(start, *end, false, nil) }); err != nil {
		return nil, err
	}
	incomplete := jobs.Where(`(r.github_job_started_at >= ? AND r.github_job_started_at < ? AND r.github_job_completed_at IS NULL)
		OR (r.github_job_completed_at >= ? AND r.github_job_completed_at < ? AND r.github_job_started_at IS NULL)`, from, to, from, to)
	if db.Dialector.Name() == BackendSQLite {
		if err := materializeSQLiteReport(db, incomplete.Select("COUNT(*) AS missing")); err != nil {
			return nil, err
		}
		err = db.Table("temp.report_result").Select("missing").Scan(&u.IncompleteJobs).Error
	} else {
		err = incomplete.Count(&u.IncompleteJobs).Error
	}
	return report, err
}

// Split the three disjoint ways an interval can overlap the window. The
// (stopping_at, ended_at) index provides a range or equality seek in each
// branch. Merge by start only after excluding history outside the window.
func executionIntervals(db *gorm.DB, q RunnerReportQuery, from, to time.Time) (*gorm.DB, error) {
	table := "runner_execution_attempts AS r"
	if db.Dialector.Name() == BackendSQLite {
		// SQLite may otherwise walk the running_at index from the beginning
		// of history just to deliver the compound result in start order.
		table += " INDEXED BY idx_execution_interval_end"
	}
	base, err := reportDimensionQuery(db, q, table)
	if err != nil {
		return nil, err
	}
	base = base.Select("r.running_at, r.stopping_at, r.ended_at").Where("r.running_at < ?", to)
	stopped := base.Session(&gorm.Session{}).Where("r.stopping_at >= ?", from)
	ended := base.Session(&gorm.Session{}).Where("r.stopping_at IS NULL AND r.ended_at >= ?", from)
	running := base.Session(&gorm.Session{}).Joins("JOIN runner_requests AS live ON live.id = r.request_id AND live.provision_attempt_id = r.attempt_id").
		Where("r.stopping_at IS NULL AND r.ended_at IS NULL AND r.status = ? AND live.status = ?", StatusRunning, StatusRunning)
	return db.Table("(? UNION ALL ? UNION ALL ?) AS intervals", stopped, ended, running).Select("running_at, stopping_at, ended_at").Order("running_at"), nil
}

func scanExecutionIntervals(db, query *gorm.DB, visit func(time.Time, *time.Time, *time.Time)) error {
	if db.Dialector.Name() == BackendSQLite {
		// Materialize the selection under ExecContext where SQLite can interrupt
		// expensive filtering. Declared datetime types preserve native decoding.
		if err := db.Exec("CREATE TEMP TABLE IF NOT EXISTS execution_intervals (started datetime, stopped datetime, ended datetime)").Error; err != nil {
			return err
		}
		if err := db.Exec("DELETE FROM temp.execution_intervals").Error; err != nil {
			return err
		}
		if err := db.Exec("INSERT INTO temp.execution_intervals ?", query.Limit(ReportMaxRows+1)).Error; err != nil {
			if ctxErr := db.Statement.Context.Err(); ctxErr != nil {
				return ctxErr
			}
			return err
		}
		query = db.Table("temp.execution_intervals").Select("started, stopped, ended").Order("rowid")
	}
	rows, err := query.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	var count int
	for rows.Next() {
		count++
		if count > ReportMaxRows && db.Dialector.Name() == BackendSQLite {
			return ErrReportTooLarge
		}
		if err := db.Statement.Context.Err(); err != nil {
			return err
		}
		var start time.Time
		var stop, end sql.NullTime
		if err := rows.Scan(&start, &stop, &end); err != nil {
			return err
		}
		var a, b *time.Time
		if stop.Valid {
			a = &stop.Time
		}
		if end.Valid {
			b = &end.Time
		}
		visit(start, a, b)
	}
	return rows.Err()
}
