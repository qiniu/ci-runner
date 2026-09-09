package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/metrics"
	"github.com/qiniu/ci-runner/internal/state"
)

func workflowJobPullRequestNumber(pulls []github.PullRequest) int64 {
	if len(pulls) == 0 {
		return 0
	}
	return pulls[0].Number
}

func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20))
	if err != nil {
		s.logger.Warn("github webhook read body failed", "event", r.Header.Get("X-GitHub-Event"), "delivery", r.Header.Get("X-GitHub-Delivery"), "error", err)
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	if !github.VerifyWebhookSignature(s.cfg.GitHubWebhookSecret.Value(), body, r.Header.Get("X-Hub-Signature-256")) {
		s.logger.Warn("github webhook signature rejected", "event", r.Header.Get("X-GitHub-Event"), "delivery", r.Header.Get("X-GitHub-Delivery"))
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	eventName := r.Header.Get("X-GitHub-Event")
	s.logger.Info("github webhook received", "event", eventName, "delivery", r.Header.Get("X-GitHub-Delivery"), "bytes", len(body))
	if eventName == "workflow_run" {
		s.handleWorkflowRunWebhook(w, r, body)
		return
	}
	if eventName != "workflow_job" {
		s.logger.Info("github webhook ignored", "event", eventName, "delivery", r.Header.Get("X-GitHub-Delivery"))
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
		return
	}
	var event github.WorkflowJobEvent
	if err := json.Unmarshal(body, &event); err != nil {
		s.logger.Warn("github workflow_job payload rejected", "delivery", r.Header.Get("X-GitHub-Delivery"), "error", err)
		writeError(w, http.StatusBadRequest, "invalid workflow_job payload")
		return
	}
	id := strconv.FormatInt(event.WorkflowJob.ID, 10)
	s.logger.Info("workflow_job webhook parsed", "action", event.Action, "job_id", id, "job_name", event.WorkflowJob.Name, "repository", event.Repository.FullName, "runner_name", event.WorkflowJob.RunnerName, "labels", []string(event.WorkflowJob.Labels))
	switch event.Action {
	case "queued":
		workflowName := workflowNameFor(event.WorkflowJob.WorkflowName, event.WorkflowRun.Name)
		st, created, err := s.enqueueWorkflowJob(event.Repository.FullName, event.Installation.ID, workflowName, event.WorkflowJob, body)
		if err != nil {
			s.logger.Error("enqueue workflow_job", "job_id", id, "repository", event.Repository.FullName, "labels", []string(event.WorkflowJob.Labels), "error", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if created || (st.Status == state.StatusFailed && st.FailureStage == "admission") {
			writeJSON(w, http.StatusAccepted, st)
			return
		}
		writeJSON(w, http.StatusOK, st)
	case "in_progress":
		startID, reason := s.completedWorkflowJobStopID(event.WorkflowJob)
		if startID == "" {
			s.logger.Info("in_progress workflow job has no managed runner", "job_id", id, "runner_name", event.WorkflowJob.RunnerName)
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
			return
		}
		st, recorded, err := s.markWorkflowJobInProgress(startID, event.WorkflowJob)
		if err != nil {
			s.logger.Error("mark workflow job in_progress", "id", startID, "job_id", id, "error", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		workflowName := workflowNameFor(event.WorkflowJob.WorkflowName, event.WorkflowRun.Name)
		jobName := workflowJobName(st, event.WorkflowJob)
		if recorded {
			metrics.RecordWorkflowStarted(st.RepositoryFullName, workflowName, jobName, st.ProfileName)
			s.recordWorkflowQueueDuration(st, workflowName, jobName)
		}
		s.logger.Info("workflow_job in_progress handled", "job_id", id, "request_id", startID, "repository", st.RepositoryFullName, "status", st.Status, "matched_by", reason)
		writeJSON(w, http.StatusAccepted, st)
	case "completed":
		stopID, reason := s.completedWorkflowJobStopID(event.WorkflowJob)
		if stopID == "" {
			s.logger.Info("completed workflow job has no managed runner", "job_id", id, "runner_name", event.WorkflowJob.RunnerName)
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
			return
		}
		st, recorded, err := s.stopRunner(context.Background(), stopID, event.WorkflowJob)
		if err != nil {
			s.logger.Error("stop runner", "id", stopID, "error", err)
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		conclusion := workflowConclusion(event.WorkflowJob)
		jobName := workflowJobName(st, event.WorkflowJob)
		workflowName := workflowNameFor(event.WorkflowJob.WorkflowName, event.WorkflowRun.Name)
		if recorded {
			metrics.RecordWorkflowCompleted(st.RepositoryFullName, workflowName, jobName, st.ProfileName, conclusion)
			s.recordWorkflowRunDuration(st, workflowName, jobName, conclusion)
			if isFailureConclusion(conclusion) {
				metrics.RecordWorkflowFailure(st.RepositoryFullName, workflowName, jobName, st.ProfileName, "workflow_job", conclusion)
			}
		}
		s.logger.Info("workflow_job completed handled", "job_id", id, "request_id", stopID, "repository", st.RepositoryFullName, "status", st.Status, "matched_by", reason)
		writeJSON(w, http.StatusAccepted, st)
	default:
		s.logger.Info("workflow_job webhook ignored", "action", event.Action, "job_id", id, "repository", event.Repository.FullName)
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
	}
}

func (s *Server) completedWorkflowJobStopID(job github.WorkflowJob) (string, string) {
	if strings.HasPrefix(job.RunnerName, "e2b-") {
		return strings.TrimPrefix(job.RunnerName, "e2b-"), "runner_name"
	}
	if job.ID == 0 {
		return "", ""
	}
	id := strconv.FormatInt(job.ID, 10)
	if _, err := s.store.ReadState(id); err == nil {
		return id, "workflow_job_id"
	}
	return "", ""
}

func workflowConclusion(job github.WorkflowJob) string {
	if strings.TrimSpace(job.Conclusion) != "" {
		return strings.TrimSpace(job.Conclusion)
	}
	if strings.TrimSpace(job.Status) != "" {
		return strings.TrimSpace(job.Status)
	}
	return "unknown"
}

func workflowNameFor(candidates ...string) string {
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return "unknown"
}

func workflowJobName(st state.RunnerState, job github.WorkflowJob) string {
	if strings.TrimSpace(job.Name) != "" {
		return strings.TrimSpace(job.Name)
	}
	if st.AssignedJobName != "" && st.AssignedJobName != runnerJobStartedMarker {
		return st.AssignedJobName
	}
	if st.RunnerName != "" {
		return st.RunnerName
	}
	return "unknown"
}

func isFailureConclusion(conclusion string) bool {
	return strings.EqualFold(strings.TrimSpace(conclusion), "failure")
}

func (s *Server) recordWorkflowQueueDuration(st state.RunnerState, workflow, job string) {
	start := st.CreatedAt
	if start.IsZero() {
		return
	}
	end := st.RunningAt
	if end.IsZero() {
		end = time.Now().UTC()
	}
	metrics.RecordWorkflowQueueDuration(st.RepositoryFullName, workflowNameFor(workflow), workflowJobName(st, github.WorkflowJob{Name: job}), st.ProfileName, end.Sub(start))
}

func (s *Server) recordWorkflowRunDuration(st state.RunnerState, workflow, job, conclusion string) {
	start := st.RunningAt
	end := st.CompletedAt
	if end.IsZero() && st.Status == state.StatusFailed {
		end = st.FailedAt
		if end.IsZero() {
			end = time.Now().UTC()
		}
	}
	if start.IsZero() || end.IsZero() {
		return
	}
	metrics.RecordWorkflowRunDuration(st.RepositoryFullName, workflowNameFor(workflow), workflowJobName(st, github.WorkflowJob{Name: job}), st.ProfileName, conclusion, end.Sub(start))
}

func (s *Server) handleWorkflowRunWebhook(w http.ResponseWriter, r *http.Request, body []byte) {
	var event github.WorkflowRunEvent
	if err := json.Unmarshal(body, &event); err != nil {
		s.logger.Warn("github workflow_run payload rejected", "delivery", r.Header.Get("X-GitHub-Delivery"), "error", err)
		writeError(w, http.StatusBadRequest, "invalid workflow_run payload")
		return
	}
	s.logger.Info("workflow_run webhook parsed", "action", event.Action, "run_id", event.WorkflowRun.ID, "workflow", event.WorkflowRun.Name, "repository", event.Repository.FullName)
	switch event.Action {
	case "requested", "in_progress":
	default:
		s.logger.Info("workflow_run webhook ignored", "action", event.Action, "run_id", event.WorkflowRun.ID, "repository", event.Repository.FullName, "delivery", r.Header.Get("X-GitHub-Delivery"))
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ignored"})
		return
	}
	jobs, err := s.gh.ListWorkflowRunJobs(r.Context(), event.Repository.FullName, event.WorkflowRun.ID)
	if err != nil {
		s.logger.Error("list workflow run jobs", "run_id", event.WorkflowRun.ID, "repository", event.Repository.FullName, "error", err)
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	created := 0
	existing := 0
	skipped := 0
	failed := 0
	for _, job := range jobs {
		if job.ID == 0 || job.Status != "queued" {
			s.logger.Info("workflow_run job skipped", "run_id", event.WorkflowRun.ID, "job_id", job.ID, "job_name", job.Name, "status", job.Status, "repository", event.Repository.FullName)
			skipped++
			continue
		}
		st, wasCreated, err := s.enqueueWorkflowJob(event.Repository.FullName, event.Installation.ID, event.WorkflowRun.Name, job, body)
		if err != nil {
			s.logger.Error("enqueue workflow_run job", "run_id", event.WorkflowRun.ID, "job_id", job.ID, "error", err)
			failed++
			continue
		}
		if wasCreated {
			created++
		} else if st.Status == state.StatusFailed && st.FailureStage == "admission" {
			skipped++
		} else {
			existing++
		}
	}
	s.logger.Info("workflow_run webhook reconciled jobs", "action", event.Action, "run_id", event.WorkflowRun.ID, "repository", event.Repository.FullName, "created", created, "existing", existing, "skipped", skipped, "failed", failed)
	if failed > 0 {
		writeError(w, http.StatusBadGateway, fmt.Sprintf("failed to enqueue %d workflow_run jobs", failed))
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]int{"created": created, "existing": existing, "skipped": skipped, "failed": failed})
}
