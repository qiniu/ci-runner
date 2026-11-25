package state

import (
	"encoding/json"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Rank the dashboard's most common selection once per publication, even when
// the full dimensional tuples in a daily rollup have very high cardinality.
type accountDefaultDashboard struct {
	Slot    int `gorm:"primaryKey;autoIncrement:false"`
	Payload []byte
}

func (accountDefaultDashboard) TableName() string { return "report_account_dashboards" }

type accountDashboardGroups struct {
	Totals, Previous ReportTotals
	Groups           map[string][]ReportGroup
	Outcomes         map[string]int64
	Trend            []ReportBucket
}

func defaultAccountQuery(at time.Time) RunnerReportQuery {
	from, to := DefaultReportDates(at, time.UTC)
	return RunnerReportQuery{IncludeAccountUsage: true, Timezone: "UTC", Grain: "day", From: from, To: to}
}

func prepareAccountDashboard(db *gorm.DB, slot int, observed time.Time) error {
	q := defaultAccountQuery(observed)
	window, err := q.Window(observed)
	if err != nil {
		return err
	}
	report := &RunnerReport{Query: q, Window: window, ObservedAt: observed}
	entry := accountDefaultDashboard{Slot: slot}
	err = aggregateAccountDashboard(db, report, slot)
	if err != nil && !errors.Is(err, ErrReportTooManyGroups) {
		return err
	}
	if err == nil && report.RetainedEntries() <= ReportSnapshotEntries {
		data, err := json.Marshal(accountDashboardGroups{Totals: report.Totals, Previous: report.Previous, Groups: report.GroupTotals, Outcomes: report.Outcomes, Trend: report.Trend})
		if err != nil {
			return err
		}
		if len(data) <= maxReportSnapshotBytes {
			entry.Payload = data
		}
	}
	return db.Clauses(clause.OnConflict{UpdateAll: true}).Create(&entry).Error
}

func readAccountDashboard(db *gorm.DB, r *RunnerReport, slot int) error {
	if r.Query == defaultAccountQuery(r.ObservedAt) {
		var entry accountDefaultDashboard
		if err := db.Where("slot = ?", slot).Take(&entry).Error; err != nil {
			return err
		}
		if len(entry.Payload) > 0 {
			var groups accountDashboardGroups
			if err := json.Unmarshal(entry.Payload, &groups); err != nil {
				return err
			}
			r.Totals, r.Previous = groups.Totals, groups.Previous
			r.GroupTotals, r.Outcomes, r.Trend = groups.Groups, groups.Outcomes, groups.Trend
			return nil
		}
	}
	return aggregateAccountDashboard(db, r, slot)
}
