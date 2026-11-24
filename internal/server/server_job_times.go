package server

import (
	"context"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
)

func (s *Server) observeJobTimes(ctx context.Context, repo string, job github.WorkflowJob, status, source string, observedAt time.Time) {
	if repo == "" || job.ID <= 0 || (status != "in_progress" && status != "completed") {
		return
	}
	start, _ := time.Parse(time.RFC3339Nano, job.StartedAt)
	var end time.Time
	if status == "completed" {
		end, _ = time.Parse(time.RFC3339Nano, job.CompletedAt)
	}
	if start.IsZero() && end.IsZero() {
		return
	}
	conclusion := job.Conclusion
	switch conclusion {
	case "", "success", "failure", "cancelled", "skipped", "timed_out", "action_required", "neutral", "stale", "startup_failure":
	default:
		conclusion = "other"
	}
	if err := s.store.RecordJobTimes(ctx, repo, job.ID, state.JobTimeObservation{
		Start: start, End: end, Source: source, Conclusion: conclusion, ObservedAt: observedAt,
	}); err != nil {
		s.logger.Warn("record GitHub Job times", "job_id", job.ID, "source", source, "error", err)
	}
}

// Reuse lifecycle lookups for missing webhook evidence without polling GitHub for reports.
func (s *Server) lookupLifecycleJob(ctx context.Context, repo string, id int64) (github.WorkflowJob, error) {
	job, err := s.gh.GetWorkflowJob(ctx, repo, id)
	if err == nil && job.ID == id {
		s.observeJobTimes(ctx, repo, job, job.Status, "github_api", time.Now().UTC())
	}
	return job, err
}
