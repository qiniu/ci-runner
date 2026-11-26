package state

import (
	"strings"
	"time"
)

// These projections belong to the reporting worker. Runner startup and lifecycle
// transactions never open, migrate, or populate these tables.
// Bump for changes to persisted metric meaning. The next worker run rebuilds
// from source before publishing a generation with this definition.
const accountReportDefinition = 2

// Version the optional efficiency projection independently of the account writer fence.
const efficiencyReportDefinition = 1

type accountReportPublication struct {
	Definition        int
	PendingDefinition int
	ID                int `gorm:"primaryKey;autoIncrement:false"`
	Slot              int
	ObservedAt        time.Time
	PublishedAt       time.Time
	Epoch             int64
	InactiveEpoch     int64
	PendingEpoch      int64
}

func (accountReportPublication) TableName() string { return "report_account_publications" }

type accountDimensions struct {
	Owner      string
	OwnerLabel string
	Repository string
	Spec       string
	SpecLabel  string
	JobResult  string
}

type accountFact struct {
	Slot                  int       `gorm:"primaryKey;autoIncrement:false;index:idx_report_fact_queue,priority:1"`
	ID                    string    `gorm:"primaryKey"`
	QueuedAt              time.Time `gorm:"index:idx_report_fact_queue,priority:2"`
	Status                string
	Epoch                 int64
	Fingerprint           []byte
	EfficiencyFingerprint []byte
	RecheckAt             *time.Time
	Dimensions            accountDimensions `gorm:"embedded"`
	Totals                ReportTotals      `gorm:"embedded"`
	Anomaly               EfficiencyAnomaly `gorm:"embedded"`
}

func (accountFact) TableName() string { return "report_account_facts" }

type accountDay struct {
	Slot         int       `gorm:"index:idx_report_day,priority:1"`
	Day          time.Time `gorm:"index:idx_report_day,priority:2"`
	LastQueuedAt time.Time
	Dimensions   accountDimensions `gorm:"embedded"`
	Totals       ReportTotals      `gorm:"embedded"`
}

func (accountDay) TableName() string { return "report_account_days" }

type accountDirtyDay struct {
	Slot int       `gorm:"primaryKey;autoIncrement:false"`
	Day  time.Time `gorm:"primaryKey"`
}

func (accountDirtyDay) TableName() string { return "report_account_dirty_days" }

type factSource struct {
	Projection            reportProjection `gorm:"embedded"`
	Fingerprint           []byte
	EfficiencyFingerprint []byte
	OldQueue              *time.Time
}

func (s factSource) fact(slot int, observed time.Time) accountFact {
	p := s.Projection
	row := p.request(observed)
	a := newReportAccumulator()
	a.add(row)
	f := accountFact{
		Slot: slot, ID: row.ID, QueuedAt: row.QueuedAt, Status: row.Status,
		Fingerprint: s.Fingerprint, EfficiencyFingerprint: s.EfficiencyFingerprint, Totals: a.totals(), Anomaly: efficiencyEvidence(p, row).EfficiencyAnomaly,
		Dimensions: accountDimensions{Owner: row.Owner, OwnerLabel: row.OwnerLabel, Repository: row.Repository, Spec: row.Spec, SpecLabel: row.SpecLabel, JobResult: row.JobResult},
	}
	for _, t := range []*time.Time{p.CreatingAt, p.RunningAt, p.StoppingAt, p.CompletedAt, p.FailedAt, p.GitHubJobObservedAt} {
		if t != nil && t.After(observed) && (f.RecheckAt == nil || t.Before(*f.RecheckAt)) {
			f.RecheckAt = t
		}
	}
	return f
}

// request restores the inputs of the row-based accumulator from one fact.
func (f accountFact) request() ReportRequest {
	sample := func(d ReportDuration) *float64 {
		if d.Samples == 0 {
			return nil
		}
		return &d.Seconds
	}
	return ReportRequest{
		ID: f.ID, QueuedAt: f.QueuedAt.UTC(), Status: f.Status,
		Owner: f.Dimensions.Owner, OwnerLabel: f.Dimensions.OwnerLabel, Repository: f.Dimensions.Repository,
		Spec: f.Dimensions.Spec, SpecLabel: f.Dimensions.SpecLabel, JobResult: f.Dimensions.JobResult,
		started: f.Totals.Started > 0, rejected: f.Totals.Rejected > 0, terminal: f.Totals.Terminal > 0,
		mappedOwner: f.Totals.MappedAccounts > 0, knownSpec: f.Totals.KnownSpecs > 0,
		creatingTime: f.Totals.CreatingTimes > 0, stoppingTime: f.Totals.StoppingTimes > 0,
		terminalTime: f.Totals.TerminalTimes > 0, jobEligible: f.Totals.JobResultEligible > 0,
		RuntimeSeconds: sample(f.Totals.Runtime), startup: sample(f.Totals.Startup),
		creation: sample(f.Totals.Creation), cleanup: sample(f.Totals.Cleanup), completion: sample(f.Totals.Completion),
	}
}

const accountDims = "owner, owner_label, repository, spec, spec_label, job_result"

func accountTextDimensions() string {
	names := strings.Split(accountDims, ", ")
	for i, name := range names {
		names[i] = name + ` COLLATE "C"`
	}
	return strings.Join(names, ", ")
}
