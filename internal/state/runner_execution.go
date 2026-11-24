package state

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Only admission creates a history row. Writers upgrading mid-attempt cannot
// turn a mutable legacy request into a supposedly complete execution.
type executionAttempt struct {
	AttemptID             string `gorm:"primaryKey;size:64"`
	RequestID             string `gorm:"index;size:255;not null"`
	Status                string `gorm:"size:24;not null"`
	ProfileSource         string
	ProfileScopeType      string
	ProfileScopeID        int64
	ProfileName           string
	RepositoryFullName    string
	GitHubInstallationID  int64 `gorm:"column:github_installation_id"`
	SandboxConfigSource   string
	SponsorInstallationID int64
	CreatingAt            time.Time
	RunningAt             *time.Time `gorm:"index"`
	StoppingAt            *time.Time `gorm:"index:idx_execution_interval_end,priority:1"`
	EndedAt               *time.Time `gorm:"index:idx_execution_interval_end,priority:2"`
}

func (executionAttempt) TableName() string { return "runner_execution_attempts" }

type executionCollection struct {
	ID        int       `gorm:"primaryKey;autoIncrement:false"`
	StartedAt time.Time `gorm:"not null"`
}

func (executionCollection) TableName() string { return "runner_execution_collection" }
func initializeExecutionCollection(db *gorm.DB) error {
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&executionCollection{ID: 1, StartedAt: time.Now().UTC()}).Error
}

func recordExecutionTransition(tx *gorm.DB, st RunnerState, attemptID string, lease stateWriteLease, now time.Time) error {
	if attemptID == "" {
		return nil
	}
	if lease.queueOwner != "" && lease.newToken != "" && st.Status == StatusCreating {
		return tx.Create(&executionAttempt{
			AttemptID: attemptID, RequestID: st.ID, Status: st.Status, CreatingAt: st.CreatingAt,
			ProfileName: st.ProfileName, ProfileSource: st.ProfileSource,
			ProfileScopeType: st.ProfileScopeType, ProfileScopeID: st.ProfileScopeID,
			RepositoryFullName: st.RepositoryFullName, GitHubInstallationID: st.GitHubInstallationID,
			SandboxConfigSource: st.SandboxConfigSource, SponsorInstallationID: st.SponsorInstallationID,
		}).Error
	}
	changes := map[string]any{"status": st.Status}
	if st.SandboxConfigSource != "" {
		changes["sandbox_config_source"] = gorm.Expr("COALESCE(NULLIF(sandbox_config_source, ''), ?)", st.SandboxConfigSource)
		if st.SponsorInstallationID > 0 {
			changes["sponsor_installation_id"] = gorm.Expr(`CASE WHEN COALESCE(sandbox_config_source, '') IN ('', ?) AND COALESCE(sponsor_installation_id, 0) = 0
				THEN ? ELSE sponsor_installation_id END`, st.SandboxConfigSource, st.SponsorInstallationID)
		}
	}
	switch st.Status {
	case StatusRunning:
		changes["running_at"] = gorm.Expr("COALESCE(running_at, ?)", st.RunningAt)
	case StatusStopping:
		changes["stopping_at"] = gorm.Expr("COALESCE(stopping_at, ?)", st.StoppingAt)
	case StatusCompleted:
		changes["ended_at"] = st.CompletedAt
	case StatusFailed:
		changes["ended_at"] = st.FailedAt
	case StatusQueued:
		changes["ended_at"] = now
		changes["status"] = "requeued"
	}
	return tx.Model(&executionAttempt{}).Where("attempt_id = ? AND request_id = ? AND ended_at IS NULL", attemptID, st.ID).UpdateColumns(changes).Error
}

// JobTimeEvidence belongs to the request's original Job, independent of the
// execution attempt currently assigned to the Runner.
type JobTimeEvidence struct {
	StartedAt       *time.Time `json:"started_at,omitempty"`
	CompletedAt     *time.Time `json:"completed_at,omitempty"`
	StartObservedAt *time.Time `json:"start_observed_at,omitempty"`
	EndObservedAt   *time.Time `json:"end_observed_at,omitempty"`
	StartSource     string     `json:"start_source,omitempty"`
	EndSource       string     `json:"end_source,omitempty"`
	Conclusion      string     `json:"conclusion,omitempty"`
}

func (r *runnerRequestRecord) jobTimeEvidence() *JobTimeEvidence {
	if r.GitHubJobStartedAt == nil && r.GitHubJobCompletedAt == nil {
		return nil
	}
	return &JobTimeEvidence{
		StartedAt: r.GitHubJobStartedAt, CompletedAt: r.GitHubJobCompletedAt,
		StartObservedAt: r.GitHubJobStartObservedAt, EndObservedAt: r.GitHubJobEndObservedAt,
		StartSource: r.GitHubJobStartSource, EndSource: r.GitHubJobEndSource, Conclusion: r.GitHubJobTimeConclusion,
	}
}

type JobTimeObservation struct {
	Conclusion             string
	Start, End, ObservedAt time.Time
	Source                 string
}

// First consistent observation wins independently for each endpoint. Conditional
// writes preserve source and observation time across retries and concurrent deliveries.
func (s *DBStore) RecordJobTimes(ctx context.Context, repo string, jobID int64, obs JobTimeObservation) error {
	if repo == "" || jobID <= 0 || obs.ObservedAt.IsZero() || (obs.Source != "webhook" && obs.Source != "github_api") {
		return errors.New("invalid Job time observation identity")
	}
	if !obs.Start.IsZero() && !obs.End.IsZero() && obs.End.Before(obs.Start) {
		return nil
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		original := func() *gorm.DB {
			return tx.Model(&runnerRequestRecord{}).Where("workflow_job_id = ? AND repository_full_name = ?", jobID, repo)
		}
		if !obs.Start.IsZero() && !obs.Start.After(obs.ObservedAt) {
			start := obs.Start.UTC()
			err := original().Where("github_job_started_at IS NULL AND (github_job_completed_at IS NULL OR github_job_completed_at >= ?)", start).
				UpdateColumns(map[string]any{
					"github_job_started_at": start, "github_job_start_source": obs.Source,
					"github_job_start_observed_at": obs.ObservedAt.UTC(),
				}).Error
			if err != nil {
				return err
			}
		}
		if !obs.End.IsZero() && !obs.End.After(obs.ObservedAt) {
			end := obs.End.UTC()
			err := original().Where("github_job_completed_at IS NULL AND (github_job_started_at IS NULL OR github_job_started_at <= ?)", end).
				UpdateColumns(map[string]any{
					"github_job_completed_at": end, "github_job_end_source": obs.Source,
					"github_job_end_observed_at": obs.ObservedAt.UTC(), "github_job_time_conclusion": obs.Conclusion,
				}).Error
			if err != nil {
				return err
			}
			if obs.Conclusion != "" {
				return original().Where("github_job_completed_at = ? AND (github_job_time_conclusion IS NULL OR github_job_time_conclusion = '')", end).
					UpdateColumn("github_job_time_conclusion", obs.Conclusion).Error
			}
		}
		return nil
	})
}
