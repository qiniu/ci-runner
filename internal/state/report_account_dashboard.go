package state

import (
	"encoding/json"
	"errors"
	"log/slog"
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

// An account-only worker replaces the whole JSON payload, removing this
// capability marker even when it reuses a slot populated by a newer worker.
type efficiencyDashboardGroups struct {
	accountDashboardGroups
	EfficiencyDefinition int
	Efficiency           *EfficiencySummary
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
	save := func(tx *gorm.DB, payload []byte) error {
		entry.Payload = payload
		return tx.Clauses(clause.OnConflict{UpdateAll: true}).Create(&entry).Error
	}
	if err != nil || report.RetainedEntries() > ReportSnapshotEntries {
		return save(db, nil)
	}
	groups := accountDashboardGroups{Totals: report.Totals, Previous: report.Previous, Groups: report.GroupTotals, Outcomes: report.Outcomes, Trend: report.Trend}
	// PostgreSQL query and write failures abort the current transaction.
	// Roll back just the optional summary before publishing the account groups.
	err = db.Transaction(func(tx *gorm.DB) error {
		summary, err := publishedEfficiencySummary(tx, slot, window)
		if err != nil {
			return err
		}
		report.Efficiency = summary
		if report.RetainedEntries() > ReportSnapshotEntries {
			return ErrReportTooManyGroups
		}
		payload, err := boundedDashboardPayload(efficiencyDashboardGroups{accountDashboardGroups: groups, EfficiencyDefinition: efficiencyReportDefinition, Efficiency: summary})
		if err != nil {
			return err
		}
		if len(payload) == 0 {
			return ErrReportTooManyGroups
		}
		return save(tx, payload)
	})
	if err == nil {
		return nil
	}
	if !errors.Is(err, ErrReportTooManyGroups) {
		slog.Warn("efficiency summary refresh failed", "slot", slot, "error", err)
	}
	payload, err := boundedDashboardPayload(groups)
	if err != nil {
		return err
	}
	return save(db, payload)
}

func boundedDashboardPayload(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if len(data) > maxReportSnapshotBytes {
		return nil, nil
	}
	return data, err
}

func readAccountDashboard(db *gorm.DB, r *RunnerReport, slot int) error {
	sameWindow := r.Query == defaultAccountQuery(r.ObservedAt) || r.Query.isDefaultEfficiency(r.ObservedAt)
	if !sameWindow && !r.Query.IncludeEfficiency {
		return aggregateAccountDashboard(db, r, slot)
	}
	var entry accountDefaultDashboard
	if err := db.Where("slot = ?", slot).Take(&entry).Error; err != nil {
		return err
	}
	if len(entry.Payload) == 0 {
		if r.Query.IncludeEfficiency {
			return nil // The caller uses the source query.
		}
		return aggregateAccountDashboard(db, r, slot)
	}
	var groups accountDashboardGroups
	if r.Query.IncludeEfficiency {
		var published efficiencyDashboardGroups
		if err := json.Unmarshal(entry.Payload, &published); err != nil {
			return err
		}
		if published.EfficiencyDefinition != efficiencyReportDefinition || published.Efficiency == nil {
			return nil
		}
		if !sameWindow {
			// Midnight moved the requested cohort since this slot was published.
			summary, err := publishedEfficiencySummary(db, slot, r.Window)
			if err != nil {
				return err
			}
			r.Efficiency = summary
			return aggregateAccountDashboard(db, r, slot)
		}
		groups, r.Efficiency = published.accountDashboardGroups, published.Efficiency
	} else if err := json.Unmarshal(entry.Payload, &groups); err != nil {
		return err
	}
	r.Totals, r.Previous = groups.Totals, groups.Previous
	r.GroupTotals, r.Outcomes, r.Trend = groups.Groups, groups.Outcomes, groups.Trend
	return nil
}
