package state

import (
	"slices"
	"time"
)

var reportAccountOutcomes = []string{"success", "failure", "timed_out", "cancelled", "skipped", "unknown"}

// Activity uses queued_at inside the selected period, irrespective of trend grain.
type ReportActivity struct {
	ActiveDays   int64            `json:"active_days"`
	UsedSpecs    int64            `json:"used_specs"`
	LastQueuedAt time.Time        `json:"last_queued_at"`
	Outcomes     map[string]int64 `json:"outcomes"`
}

type activityAccumulator struct {
	ReportActivity
	days, specs map[string]struct{}
}

func newActivityAccumulator() *activityAccumulator {
	return &activityAccumulator{ReportActivity: ReportActivity{Outcomes: make(map[string]int64)}, days: make(map[string]struct{}), specs: make(map[string]struct{})}
}

func (a *activityAccumulator) add(row ReportRequest, loc *time.Location) {
	a.days[row.QueuedAt.In(loc).Format(time.DateOnly)] = struct{}{}
	a.ActiveDays = int64(len(a.days))
	if row.knownSpec {
		a.specs[row.Spec] = struct{}{}
		a.UsedSpecs = int64(len(a.specs))
	}
	if row.QueuedAt.After(a.LastQueuedAt) {
		a.LastQueuedAt = row.QueuedAt.UTC()
	}
	result := row.JobResult
	if !slices.Contains(reportAccountOutcomes, result) {
		result = "other"
	}
	a.Outcomes[result]++
}
