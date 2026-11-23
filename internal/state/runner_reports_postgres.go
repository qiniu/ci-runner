package state

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrReportTooManyGroups = errors.New("report exceeds 100000 aggregate groups; narrow the date range or filters")

// Use the same zone rules as reportBucketDate. PostgreSQL can interpret an
// IANA name such as CET as a fixed abbreviation even during summer time.
// Only the current period contributes trend buckets.
func postgresTrendOffset(window ReportWindow) clause.Expr {
	at := window.End.Add(-time.Nanosecond).In(window.Location)
	_, offset := at.Zone()
	seconds := gorm.Expr("?::int", offset)
	for start, _ := at.ZoneBounds(); start.After(window.Start); start, _ = at.ZoneBounds() {
		at = start.Add(-time.Nanosecond)
		_, previous := at.Zone()
		if previous != offset {
			seconds = gorm.Expr("CASE WHEN queued_at < ? THEN ?::int ELSE ? END", start.UTC(), previous, seconds)
			offset = previous
		}
	}
	return seconds
}

// PostgreSQL computes all five rollups in one query. Only aggregate rows cross
// the database connection, regardless of the number of requests in a cohort.
func postgresAggregateReport(ctx context.Context, db, cohort *gorm.DB, q RunnerReportQuery, window ReportWindow, now time.Time) (*RunnerReport, error) {
	cohort = cohort.Session(&gorm.Session{}).Select(`r.queued_at, r.creating_at, r.running_at, r.stopping_at,
		CASE r.status WHEN 'completed' THEN r.completed_at WHEN 'failed' THEN r.failed_at END AS terminal_at,
		r.status IN ('completed', 'failed') AS terminal, r.status = 'failed' AND r.failure_stage = 'admission' AS rejected,
		r.workflow_job_id > 0 AS has_job, r.github_job_status = 'completed' AND
			coalesce(r.github_job_conclusion, '') NOT IN ('', 'unknown') AS saved_result, r.github_job_conclusion, r.github_job_observed_at,
		coalesce(r.profile_name, '') COLLATE "C" AS spec_name, coalesce(r.profile_source, '') COLLATE "C" AS spec_source,
		CASE WHEN r.profile_source = 'global' THEN '' ELSE coalesce(r.profile_scope_type, '') END COLLATE "C" AS scope_type,
		CASE WHEN r.profile_source = 'global' THEN 0 ELSE coalesce(r.profile_scope_id, 0) END AS scope_id,
		coalesce(r.repository_full_name, '') COLLATE "C" AS repo, o.github_account_id AS account_id,
		CASE WHEN o.github_account_id > 0 THEN 'github:' || o.github_account_id
			ELSE 'installation:' || coalesce(r.github_installation_id, 0) END COLLATE "C" AS owner_key,
		nullif(o.account_login, '') COLLATE "C" AS account_login`)
	const aggregates = `
	WITH bounds AS (SELECT ?::timestamptz AS observed, ?::timestamptz AS start_at),
	validated AS (
		SELECT c.*, queued_at >= start_at AS current_period,
			to_char(date_trunc(?, timezone(make_interval(secs => ?), queued_at)), 'YYYY-MM-DD') AS bucket,
			queued_at >= timestamptz '0002-01-01 UTC' AS valid_queue,
			creating_at BETWEEN timestamptz '0002-01-01 UTC' AND observed AS valid_create,
			running_at BETWEEN timestamptz '0002-01-01 UTC' AND observed AS started,
			stopping_at BETWEEN timestamptz '0002-01-01 UTC' AND observed AS valid_stop,
			terminal_at BETWEEN timestamptz '0002-01-01 UTC' AND observed AS valid_terminal,
			github_job_observed_at BETWEEN timestamptz '0002-01-01 UTC' AND observed AS valid_result,
			coalesce(nullif(stopping_at, timestamptz '0001-01-01 UTC'), terminal_at) AS runtime_end
		FROM (?) AS c CROSS JOIN bounds
	), intervals AS (
		SELECT v.*,
   CASE WHEN has_job AND saved_result AND valid_result THEN github_job_conclusion ELSE 'unknown' END COLLATE "C" AS job_result,
			CASE WHEN started AND valid_terminal AND runtime_end BETWEEN running_at AND terminal_at
				THEN runtime_end - running_at END AS runtime,
			CASE WHEN started AND valid_queue AND running_at >= queued_at THEN running_at - queued_at END AS startup,
			CASE WHEN started AND valid_create AND running_at >= creating_at THEN running_at - creating_at END AS creation,
			CASE WHEN valid_terminal AND valid_stop AND terminal_at >= stopping_at THEN terminal_at - stopping_at END AS cleanup,
			CASE WHEN valid_terminal AND valid_queue AND terminal_at >= queued_at THEN terminal_at - queued_at END AS completion
		FROM validated v
	)
	SELECT current_period,
		CASE %s WHEN grouping(bucket) = 0 THEN 'trend'  WHEN grouping(spec_name) = 0 THEN 'spec'
			WHEN grouping(owner_key) = 0 THEN 'owner' WHEN grouping(repo) = 0 THEN 'repository' ELSE 'total' END AS dimension,
		bucket, spec_source, scope_type, scope_id, spec_name, owner_key, repo, %s
		min(CASE WHEN account_id > 0 THEN coalesce(account_login, owner_key) ELSE owner_key END) AS owner_label,
		count(*) AS requests, count(*) FILTER (WHERE started) AS started, count(*) FILTER (WHERE terminal) AS terminal,
		count(*) FILTER (WHERE rejected) AS rejected, count(DISTINCT account_id) FILTER (WHERE account_id > 0) AS active_accounts,
		count(DISTINCT nullif(repo, '')) AS active_repositories, count(*) FILTER (WHERE account_id > 0) AS mapped_accounts,
		count(*) FILTER (WHERE spec_name <> '' AND (spec_source = 'global' OR
			(spec_source = 'scoped_custom' AND scope_id > 0 AND scope_type IN ('account', 'installation')))) AS known_specs,
		count(*) FILTER (WHERE valid_create) AS creating_times, count(*) FILTER (WHERE valid_stop) AS stopping_times,
		count(*) FILTER (WHERE valid_terminal) AS terminal_times,
		count(*) FILTER (WHERE terminal AND has_job) AS job_result_eligible,
		count(*) FILTER (WHERE terminal AND has_job AND saved_result AND valid_result) AS job_results,
		count(*) FILTER (WHERE terminal AND started) AS runtime_eligible, count(*) FILTER (WHERE started) AS startup_eligible,
		count(*) FILTER (WHERE started) AS creation_eligible, count(*) FILTER (WHERE terminal AND valid_stop) AS cleanup_eligible,
		count(*) FILTER (WHERE terminal) AS completion_eligible, `
	specRepoSet, classify, resultField := "", "", ""
	if q.IncludeSpecUsage {
		for _, dim := range []struct{ column, kind string }{{"repo", "spec_repo"}, {"owner_key", "spec_owner"}, {"bucket", "spec_trend"}, {"job_result", "spec_result"}} {
			specRepoSet += ", (spec_source, scope_type, scope_id, spec_name, " + dim.column + ")"
			classify += " WHEN grouping(spec_name, " + dim.column + ") = 0 THEN '" + dim.kind + "'"
		}
		resultField = "job_result,"
	}
	rollups := `
	FROM intervals GROUP BY current_period, GROUPING SETS ((), (bucket), (spec_source, scope_type, scope_id, spec_name), (owner_key), (repo)` + specRepoSet + `)
	HAVING current_period OR grouping(bucket, spec_name, owner_key, repo) = 15
	LIMIT ?`
	var durations []string
	for _, metric := range []string{"runtime", "startup", "creation", "cleanup", "completion"} {
		// Match Go's time.Duration saturation for exceptionally long valid intervals.
		durations = append(durations, "count("+metric+") AS "+metric+"_samples, coalesce(sum(least(extract(epoch FROM "+metric+"), 9223372036.854775807)) FILTER (WHERE "+metric+" IS NOT NULL), 0) AS "+metric+"_seconds")
	}
	query := db.Raw(fmt.Sprintf(aggregates, classify, resultField)+strings.Join(durations, ", ")+rollups, now.UTC(), window.Start.UTC(), q.Grain, postgresTrendOffset(window), cohort, ReportMaxRows+1)
	rows, err := query.Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	report := &RunnerReport{Query: q, Window: window, ObservedAt: now.UTC(), groupTotals: make(map[string][]ReportGroup)}
	buckets := make(map[string]ReportTotals)
	for n := 0; rows.Next(); n++ {
		if n >= ReportMaxRows {
			return nil, ErrReportTooManyGroups
		}
		var row struct {
			CurrentPeriod                                                                             bool
			Dimension, Bucket, SpecSource, ScopeType, SpecName, OwnerKey, OwnerLabel, Repo, JobResult string
			ScopeID                                                                                   int64
			ReportTotals                                                                              `gorm:"embedded"`
		}
		if err := db.ScanRows(rows, &row); err != nil {
			return nil, err
		}
		var key, label string
		switch row.Dimension {
		case "total":
			if row.CurrentPeriod {
				report.Totals = row.ReportTotals
			} else {
				report.Previous = row.ReportTotals
			}
			continue
		case "trend":
			buckets[row.Bucket] = row.ReportTotals
			continue
		case "spec_repo", "spec_owner", "spec_trend", "spec_result":
			if report.specGroups == nil {
				report.specGroups = make(map[string]*specBreakdown)
			}
			id := reportSpecKey(reportSpecIdentity{row.SpecSource, row.ScopeType, row.ScopeID, row.SpecName})
			if report.specGroups[id] == nil {
				report.specGroups[id] = &specBreakdown{}
			}
			group := ReportSpecSegment{Key: row.Repo, Label: row.Repo, Started: row.Started, Requests: row.Requests, Runtime: row.Runtime}
			switch row.Dimension {
			case "spec_owner":
				group.Key, group.Label = row.OwnerKey, row.OwnerLabel
			case "spec_trend":
				group.Key, group.Label = row.Bucket, row.Bucket
			case "spec_result":
				group.Key = row.JobResult
			}
			report.specGroups[id].addGroup(row.Dimension, group)
			continue
		case "spec":
			id := reportSpecIdentity{row.SpecSource, row.ScopeType, row.ScopeID, row.SpecName}
			key, label = reportSpecKey(id), id.label()
		case "owner":
			key, label = row.OwnerKey, row.OwnerLabel
		case "repository":
			key, label = row.Repo, row.Repo
		}
		report.groupTotals[row.Dimension] = append(report.groupTotals[row.Dimension], ReportGroup{key, label, row.ReportTotals})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	report.fillTrend(buckets)
	return report, nil
}
