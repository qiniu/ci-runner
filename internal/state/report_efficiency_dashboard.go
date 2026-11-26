package state

import (
	"time"

	"gorm.io/gorm"
)

func (q RunnerReportQuery) isDefaultEfficiency(at time.Time) bool {
	if !q.IncludeEfficiency {
		return false
	}
	def := defaultAccountQuery(at)
	def.IncludeAccountUsage = false
	def.IncludeEfficiency = true
	return q == def
}

// Keep sorting in PostgreSQL so a worker does not retain every duration in memory.
// Each fact contributes at most one sample per metric, including a valid zero.
func publishedEfficiencySummary(db *gorm.DB, slot int, w ReportWindow) (*EfficiencySummary, error) {
	facts := db.Table("report_account_facts").Where("slot = ? AND queued_at >= ? AND queued_at < ?", slot, w.Start.UTC(), w.End.UTC())
	var p struct{ Startup, Creation, Cleanup *float64 }
	err := facts.Session(&gorm.Session{}).Select(`
		percentile_disc(0.95) WITHIN GROUP (ORDER BY startup_seconds) FILTER (WHERE startup_samples > 0) AS startup,
		percentile_disc(0.95) WITHIN GROUP (ORDER BY creation_seconds) FILTER (WHERE creation_samples > 0) AS creation,
		percentile_disc(0.95) WITHIN GROUP (ORDER BY cleanup_seconds) FILTER (WHERE cleanup_samples > 0) AS cleanup`).Scan(&p).Error
	if err != nil {
		return nil, err
	}
	terminal := facts.Where("termination IS NOT NULL").Select(`
		nullif(failure_class,'') COLLATE "C" AS class, stage COLLATE "C" AS stage,
		reason COLLATE "C" AS reason, termination COLLATE "C" AS termination,
		CASE WHEN failure_class='admission' THEN timezone('UTC',queued_at)::date END AS day`)
	s := &EfficiencySummary{StartupP95: p.Startup, CreationP95: p.Creation, CleanupP95: p.Cleanup}
	return s, readEfficiencyGroups(db, terminal, s)
}

func readPublishedEfficiencyRows(query *gorm.DB, fields string, r *RunnerReport) error {
	rows, err := query.Select(fields + `, failure_class, stage, reason, termination,
		CASE WHEN startup_samples > 0 THEN startup_seconds END AS startup,
		CASE WHEN creation_samples > 0 THEN creation_seconds END AS creation,
		CASE WHEN cleanup_samples > 0 THEN cleanup_seconds END AS cleanup`).Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var item struct {
			Request  ReportRequest      `gorm:"embedded"`
			Evidence EfficiencyEvidence `gorm:"embedded"`
		}
		if err := query.ScanRows(rows, &item); err != nil {
			return err
		}
		if item.Evidence.FailureClass == "admission" {
			item.Evidence.AdmissionBucket = item.Request.QueuedAt.UTC().Format(time.DateOnly)
		}
		item.Request.Efficiency = &item.Evidence
		r.Rows = append(r.Rows, item.Request)
	}
	return rows.Err()
}
