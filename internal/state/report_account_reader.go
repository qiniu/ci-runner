package state

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"
)

func accountFilters(query *gorm.DB, q RunnerReportQuery, slot int) *gorm.DB {
	query = query.Where("slot = ?", slot)
	if q.Repository != "" || q.MissingRepository == "1" {
		query = query.Where("repository = ?", reportTextKey(query, q.Repository))
	}
	if q.Owner != "" {
		query = query.Where("owner = ?", reportTextKey(query, q.Owner))
	}
	if q.Spec != "" {
		query = query.Where("spec = ?", reportTextKey(query, q.Spec))
	}
	return query
}

// Whole UTC days already contain the additive contribution for each exact
// dimension tuple. For clipped dates and other calendars, group only normalized
// report facts. Both sources yield the same columns to the final grouping.
func accountPeriodParts(db *gorm.DB, q RunnerReportQuery, slot int, start, end time.Time, current bool, columns []string, window ReportWindow) []*gorm.DB {
	var parts []*gorm.DB
	metrics := strings.Join(columns, ",")
	facts := func(from, to time.Time) {
		if !from.Before(to) {
			return
		}
		offset := postgresTrendOffset(window)
		part := accountFilters(db.Table("report_account_facts"), q, slot).
			Where("queued_at >= ? AND queued_at < ?", from.UTC(), to.UTC()).
			Select("?::boolean AS current_period, timezone(make_interval(secs => ?),queued_at)::date AS local_day, queued_at AS last_queued_at,"+accountTextDimensions()+","+metrics, current, offset)
		parts = append(parts, part)
	}
	if q.Timezone != "UTC" {
		facts(start, end)
		return parts
	}
	first := start.UTC().Truncate(24 * time.Hour)
	if first.Before(start) {
		first = first.Add(24 * time.Hour)
	}
	last := end.UTC().Truncate(24 * time.Hour)
	if !first.Before(last) {
		facts(start, end)
		return parts
	}
	facts(start, first)
	parts = append(parts, accountFilters(db.Table("report_account_days"), q, slot).
		Select("?::boolean AS current_period, timezone('UTC',day)::date AS local_day,last_queued_at,"+accountTextDimensions()+","+metrics, current).
		Where("day >= ? AND day < ?", first, last))
	facts(last, end)
	return parts
}

// Probe before querying the table: an undefined relation would abort the
// repeatable-read transaction needed for the first-publication fallback.
func accountPublicationExists(db *gorm.DB) (bool, error) {
	var exists bool
	err := db.Raw("SELECT to_regclass(?) IS NOT NULL", (accountReportPublication{}).TableName()).Scan(&exists).Error
	return exists, err
}

// A nil report means the caller can serve the validated query from source.
func readPublishedAccounts(db *gorm.DB, q RunnerReportQuery, now time.Time) (*RunnerReport, error) {
	exists, err := accountPublicationExists(db)
	if err != nil || !exists {
		return nil, err
	}
	var pub accountReportPublication
	err = db.Select("slot", "observed_at", "definition").Take(&pub, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pub.ObservedAt.IsZero() || pub.Definition != accountReportDefinition {
		return nil, nil
	}
	w, err := q.windowAt(pub.ObservedAt, now)
	if err != nil {
		return nil, nil // The requested window starts after this publication.
	}
	r := &RunnerReport{Query: q, Window: w, ObservedAt: pub.ObservedAt.UTC()}
	if err := readAccountDashboard(db, r, pub.Slot); err != nil {
		return nil, err
	}
	detail := r.Totals.Requests+r.Previous.Requests <= ReportMaxRows
	r.DetailAvailable = &detail
	if detail {
		r.Rows = []ReportRequest{}
		// Only the projection exposed in the immutable request drilldown is needed.
		err := accountFilters(db.Table("report_account_facts"), q, pub.Slot).
			Select("id, queued_at, repository, owner, owner_label, spec, spec_label, status, job_result, CASE WHEN runtime_samples > 0 THEN runtime_seconds END AS runtime_seconds").
			Where("queued_at >= ? AND queued_at < ?", w.Start.UTC(), w.End.UTC()).Order("queued_at DESC, id ASC").Scan(&r.Rows).Error
		if err != nil {
			return nil, err
		}
	}
	if r.RetainedEntries() > ReportSnapshotEntries {
		return nil, ErrReportTooManyGroups
	}
	return r, nil
}

func publishedAccountGroups(db *gorm.DB, q RunnerReportQuery, slot int, w ReportWindow) (*gorm.DB, error) {
	cols, err := reportMetricColumns(db)
	if err != nil {
		return nil, err
	}
	parts := accountPeriodParts(db, q, slot, w.Start, w.End, true, cols, w)
	parts = append(parts, accountPeriodParts(db, q, slot, w.PreviousStart, w.PreviousEnd, false, cols, w)...)
	args := []any{q.Grain}
	for _, part := range parts {
		args = append(args, part)
	}
	sumFields := sumReportColumns(additiveReportColumns(cols))
	// PostgreSQL computes distinct counts over the full selection. In particular
	// one repository or active date can appear in several stored daily rows.
	fields := `current_period, CASE WHEN grouping(bucket)=0 THEN 'trend'
		WHEN grouping(owner)=0 THEN 'owner' WHEN grouping(repository)=0 THEN 'repository'
		WHEN grouping(spec)=0 THEN 'spec' ELSE 'total' END AS dimension,
		bucket,owner,min(owner_label) AS owner_label,repository,spec,min(spec_label) AS spec_label,
		count(DISTINCT owner) FILTER(WHERE mapped_accounts>0) AS active_accounts,
		count(DISTINCT repository) FILTER(WHERE repository<>'') AS active_repositories,
		count(DISTINCT local_day) AS active_days, count(DISTINCT spec) FILTER(WHERE known_specs>0) AS used_specs,
		max(last_queued_at) AS last_queued_at, ` + sumFields
	var outcomes []string
	for _, result := range reportAccountOutcomes {
		outcomes = append(outcomes, "'"+result+"', sum(requests) FILTER(WHERE job_result='"+result+"')")
	}
	fields += ", json_strip_nulls(json_build_object(" + strings.Join(outcomes, ",") + ")) AS outcomes"
	statement := "SELECT " + fields + ` FROM (SELECT p.*,to_char(date_trunc(?,local_day::timestamp),'YYYY-MM-DD') AS bucket FROM (` +
		strings.TrimSuffix(strings.Repeat("(?) UNION ALL ", len(parts)), " UNION ALL ") + `) p) src
		GROUP BY current_period, GROUPING SETS ((),(owner),(repository),(spec),(bucket))
		HAVING current_period OR grouping(owner,repository,spec,bucket)=15
		ORDER BY grouping(owner,repository,spec,bucket) DESC LIMIT ?`
	// Totals (mask 15) arrive first, so the reader knows which budget applies.
	// Each trend row represents at least one current request. A valid detail
	// snapshot can therefore yield at most its entry budget plus two totals.
	args = append(args, ReportSnapshotEntries+2+1)
	return db.Raw(statement, args...), nil
}

func aggregateAccountDashboard(db *gorm.DB, r *RunnerReport, slot int) error {
	query, err := publishedAccountGroups(db, r.Query, slot, r.Window)
	if err != nil {
		return err
	}
	rows, err := query.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	r.Outcomes = map[string]int64{}
	r.GroupTotals = map[string][]ReportGroup{}
	buckets := map[string]ReportTotals{}
	var groups int64
	for n := 0; rows.Next(); n++ {
		if n >= ReportMaxRows && r.Totals.Requests+r.Previous.Requests > ReportMaxRows {
			return ErrReportTooManyGroups
		}
		var item struct {
			CurrentPeriod                                                     bool
			Dimension, Bucket, Owner, OwnerLabel, Repository, Spec, SpecLabel string
			Totals                                                            ReportTotals `gorm:"embedded"`
			ActiveDays, UsedSpecs                                             int64
			LastQueuedAt                                                      time.Time
			Outcomes                                                          string
		}
		if err := db.ScanRows(rows, &item); err != nil {
			return err
		}
		group := ReportGroup{ReportTotals: item.Totals}
		switch item.Dimension {
		case "total":
			if item.CurrentPeriod {
				r.Totals = item.Totals
			} else {
				r.Previous = item.Totals
			}
			continue
		case "trend":
			buckets[item.Bucket] = item.Totals
			continue
		case "spec":
			group.Key, group.Label = item.Spec, item.SpecLabel
		case "owner":
			group.Key, group.Label = item.Owner, item.OwnerLabel
		case "repository":
			group.Key, group.Label = item.Repository, item.Repository
		}
		groups++
		if r.Totals.Requests+r.Previous.Requests <= ReportMaxRows && groups+r.Totals.Requests > ReportSnapshotEntries {
			return ErrReportTooManyGroups
		}
		if item.Dimension != "spec" {
			a := &ReportActivity{ActiveDays: item.ActiveDays, UsedSpecs: item.UsedSpecs, LastQueuedAt: item.LastQueuedAt.UTC()}
			if err := json.Unmarshal([]byte(item.Outcomes), &a.Outcomes); err != nil {
				return err
			}
			other := group.Requests
			for _, count := range a.Outcomes {
				other -= count
			}
			if other > 0 {
				a.Outcomes["other"] = other
			}
			if item.Dimension == "repository" {
				for result, count := range a.Outcomes {
					r.Outcomes[result] += count
				}
			}
			group.ReportActivity = a
		}
		r.GroupTotals[item.Dimension] = append(r.GroupTotals[item.Dimension], group)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	rows.Close()
	r.fillTrend(buckets)
	return nil
}
