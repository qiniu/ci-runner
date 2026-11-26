package state

import (
	"database/sql"
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
func readPublishedUsageReport(db *gorm.DB, q RunnerReportQuery, now time.Time) (*RunnerReport, error) {
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
	if q.isOverview() || q.IncludeSpecUsage {
		return readPublishedOverviewOrSpecs(db, r, pub.Slot)
	}
	if err := readPublishedDashboard(db, r, pub.Slot); err != nil {
		return nil, err
	}
	if q.IncludeEfficiency && r.Efficiency == nil {
		return nil, nil
	}
	detail := r.Totals.Requests+r.Previous.Requests <= ReportMaxRows
	r.DetailAvailable = &detail
	if detail {
		r.Rows = []ReportRequest{}
		// Only the projection exposed in the immutable request drilldown is needed.
		rows := accountFilters(db.Table("report_account_facts"), q, pub.Slot).
			Where("queued_at >= ? AND queued_at < ?", w.Start.UTC(), w.End.UTC()).Order("queued_at DESC, id ASC")
		const fields = "id, queued_at, repository, owner, owner_label, spec, spec_label, status, job_result, CASE WHEN runtime_samples > 0 THEN runtime_seconds END AS runtime_seconds"
		if q.IncludeEfficiency {
			err = readPublishedEfficiencyRows(rows, fields, r)
		} else {
			err = rows.Select(fields).Scan(&r.Rows).Error
		}
		if err != nil {
			return nil, err
		}
	}
	if r.RetainedEntries() > ReportSnapshotEntries {
		return nil, ErrReportTooManyGroups
	}
	return r, nil
}

func readPublishedOverviewOrSpecs(db *gorm.DB, r *RunnerReport, slot int) (*RunnerReport, error) {
	q, w := r.Query, r.Window
	facts := accountFilters(db.Table("report_account_facts"), q, slot).
		Where("((queued_at >= ? AND queued_at < ?) OR (queued_at >= ? AND queued_at < ?))",
			w.Start.UTC(), w.End.UTC(), w.PreviousStart.UTC(), w.PreviousEnd.UTC())
	var overflow int
	if err := facts.Session(&gorm.Session{}).Select("1").Offset(ReportMaxRows).Limit(1).Scan(&overflow).Error; err != nil {
		return nil, err
	}
	if overflow == 0 {
		cols, err := reportMetricColumns(db)
		if err != nil {
			return nil, err
		}
		fields := "id, queued_at, status, " + accountDims + ", " + strings.Join(cols, ", ")
		r, err = readReportRows(db, facts.Select(fields).Order("queued_at DESC, id ASC").Limit(ReportMaxRows+1),
			q, w, r.ObservedAt, func(rows *sql.Rows) (ReportRequest, error) {
				var fact accountFact
				if err := db.ScanRows(rows, &fact); err != nil {
					return ReportRequest{}, err
				}
				return fact.request(), nil
			})
		if err != nil {
			return nil, err
		}
	} else {
		if err := readPublishedDashboard(db, r, slot); err != nil {
			return nil, err
		}
		if q.IncludeSpecUsage && r.Totals.Requests > 0 {
			if err := readPublishedSpecGroups(db, r, slot); err != nil {
				return nil, err
			}
		}
	}
	if q.IncludeSpecUsage {
		return finishSpecReport(db, r)
	}
	return r, nil
}

func publishedReportBuckets(db *gorm.DB, grain string, parts []*gorm.DB) *gorm.DB {
	args := []any{grain}
	for _, part := range parts {
		args = append(args, part)
	}
	return db.Raw(`SELECT p.*,to_char(date_trunc(?,local_day::timestamp),'YYYY-MM-DD') AS bucket FROM (`+
		strings.TrimSuffix(strings.Repeat("(?) UNION ALL ", len(parts)), " UNION ALL ")+") p", args...)
}

func publishedReportGroups(db *gorm.DB, q RunnerReportQuery, slot int, w ReportWindow) (*gorm.DB, error) {
	cols, err := reportMetricColumns(db)
	if err != nil {
		return nil, err
	}
	parts := accountPeriodParts(db, q, slot, w.Start, w.End, true, cols, w)
	parts = append(parts, accountPeriodParts(db, q, slot, w.PreviousStart, w.PreviousEnd, false, cols, w)...)
	// Distinct totals use stable keys across the entire cohort.
	fields := `current_period, CASE WHEN grouping(bucket)=0 THEN 'trend'
		WHEN grouping(owner)=0 THEN 'owner' WHEN grouping(repository)=0 THEN 'repository'
		WHEN grouping(spec)=0 THEN 'spec' ELSE 'total' END AS dimension,
		bucket,owner,min(owner_label) AS owner_label,repository,spec,min(spec_label) AS spec_label,
		count(DISTINCT owner) FILTER(WHERE mapped_accounts>0) AS active_accounts,
		count(DISTINCT repository) FILTER(WHERE repository<>'') AS active_repositories, ` + sumReportColumns(additiveReportColumns(cols))
	limit := ReportMaxRows + 1
	if q.IncludeAccountUsage {
		var outcomes []string
		for _, result := range reportAccountOutcomes {
			outcomes = append(outcomes, "'"+result+"', sum(requests) FILTER(WHERE job_result='"+result+"')")
		}
		fields += `, count(DISTINCT local_day) AS active_days, count(DISTINCT spec) FILTER(WHERE known_specs>0) AS used_specs,
			max(last_queued_at) AS last_queued_at, json_strip_nulls(json_build_object(` + strings.Join(outcomes, ",") + ")) AS outcomes"
		limit = ReportSnapshotEntries + 3
	}
	// Totals arrive first, so the reader knows which budget applies.
	statement := "SELECT " + fields + ` FROM (?) AS src
		GROUP BY current_period, GROUPING SETS ((),(owner),(repository),(spec),(bucket))
		HAVING current_period OR grouping(owner,repository,spec,bucket)=15
		ORDER BY grouping(owner,repository,spec,bucket) DESC LIMIT ?`
	return db.Raw(statement, publishedReportBuckets(db, q.Grain, parts), limit), nil
}

func aggregatePublishedDashboard(db *gorm.DB, r *RunnerReport, slot int) error {
	query, err := publishedReportGroups(db, r.Query, slot, r.Window)
	if err != nil {
		return err
	}
	rows, err := query.Rows()
	if err != nil {
		return err
	}
	defer rows.Close()
	if r.Query.IncludeAccountUsage {
		r.Outcomes = map[string]int64{}
	}
	r.GroupTotals = map[string][]ReportGroup{}
	buckets := map[string]ReportTotals{}
	var groups int64
	for n := 0; rows.Next(); n++ {
		if n >= ReportMaxRows && (!r.Query.IncludeAccountUsage || r.Totals.Requests+r.Previous.Requests > ReportMaxRows) {
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
		if r.Query.IncludeAccountUsage && item.Dimension != "spec" {
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
