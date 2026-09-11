package server

import (
	"context"
	"errors"
	"expvar"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/redact"
	"github.com/qiniu/ci-runner/internal/state"
)

const (
	diagnosticRunnerEventLimit       = 200
	diagnosticGitHubJobCacheTTL      = 30 * time.Second
	maxDiagnosticGitHubJobCacheItems = 1024
)

type diagnosticGitHubJob struct {
	LookupStatus string `json:"lookup_status"`
	ID           int64  `json:"id,omitempty"`
	Name         string `json:"name,omitempty"`
	Status       string `json:"status,omitempty"`
	Conclusion   string `json:"conclusion,omitempty"`
	RunnerName   string `json:"runner_name,omitempty"`
}

type runnerDiagnosticFinding struct {
	Code     string `json:"code"`
	Severity string `json:"severity"`
	Detail   string `json:"detail,omitempty"`
}

type runnerRequestDiagnostics struct {
	State           state.RunnerState         `json:"state"`
	GitHubJob       diagnosticGitHubJob       `json:"github_job"`
	Findings        []runnerDiagnosticFinding `json:"findings"`
	Events          []state.RunnerEvent       `json:"events"`
	EventsTruncated bool                      `json:"events_truncated"`
}

type runnerRequestEventPage struct {
	Events  []state.RunnerEvent `json:"events"`
	HasMore bool                `json:"has_more"`
}

func (s *Server) handleDiagnosticsPprof(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	type artifact struct {
		Address     string `json:"address"`
		AddressFile string `json:"address_file"`
		DumpScript  string `json:"dump_script"`
	}
	addresses, scripts := discoverPprofArtifacts()
	out := make([]artifact, 0, len(addresses))
	for i := range addresses {
		item := artifact{Address: addresses[i].Address, AddressFile: addresses[i].Path}
		if i < len(scripts) {
			item.DumpScript = scripts[i]
		}
		out = append(out, item)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"pprof": out,
		"state": map[string]any{
			"backend":  s.cfg.StateBackend,
			"database": redact.DatabaseDSN(s.cfg.StateDatabaseDSN.Value()),
		},
		"github": map[string]any{
			"auth_mode":       s.cfg.GitHubAuthMode(),
			"installation_id": s.cfg.GitHubAppInstallationID,
			"api_base_url":    s.cfg.GitHubAPIBaseURL,
		},
	})
}

func intValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func (s *Server) handleDiagnosticsVars(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	expvar.Handler().ServeHTTP(w, r)
}

func (s *Server) handleDiagnosticsRunnerRequest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	st, err := s.store.ReadState(r.PathValue("id"))
	if err != nil {
		s.writeRunnerRequestLookupError(w, r.PathValue("id"), err)
		return
	}
	s.writeRunnerRequestDiagnostics(w, r, st)
}

func (s *Server) handleLegacyDiagnosticsRunnerRequest(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	st, err := s.resolveRunnerRequestIdentifier(r.PathValue("id"))
	if err != nil {
		s.writeRunnerRequestLookupError(w, r.PathValue("id"), err)
		return
	}
	s.writeRunnerRequestDiagnostics(w, r, st)
}

func (s *Server) writeRunnerRequestDiagnostics(w http.ResponseWriter, r *http.Request, st state.RunnerState) {
	events, truncated, err := s.store.ListRunnerEvents(st.ID, 0, diagnosticRunnerEventLimit, "control_log")
	if err != nil {
		s.writeRunnerEventReadError(w, st.ID, err)
		return
	}

	job := diagnosticGitHubJob{LookupStatus: "not_applicable"}
	if st.WorkflowJobID != 0 && strings.TrimSpace(st.RepositoryFullName) != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		githubJob, lookupErr := s.diagnosticWorkflowJob(ctx, st.RepositoryFullName, st.WorkflowJobID)
		if lookupErr != nil {
			job.LookupStatus = "unavailable"
			s.logger.Warn("diagnostic github workflow job lookup failed", "id", st.ID, "workflow_job_id", st.WorkflowJobID, "error", lookupErr)
		} else {
			job = diagnosticGitHubJob{
				LookupStatus: "ok",
				ID:           githubJob.ID,
				Name:         githubJob.Name,
				Status:       githubJob.Status,
				Conclusion:   githubJob.Conclusion,
				RunnerName:   githubJob.RunnerName,
			}
		}
	}

	writeJSON(w, http.StatusOK, runnerRequestDiagnostics{
		State:           st,
		GitHubJob:       job,
		Findings:        diagnoseRunnerRequest(st, events, truncated, job),
		Events:          events,
		EventsTruncated: truncated,
	})
}

func (s *Server) handleRunnerRequestEvents(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	afterValue := strings.TrimSpace(r.URL.Query().Get("after_id"))
	beforeValue := strings.TrimSpace(r.URL.Query().Get("before_id"))
	if afterValue != "" && beforeValue != "" {
		writeError(w, http.StatusBadRequest, "after_id and before_id cannot be combined")
		return
	}
	var afterID int64
	if afterValue != "" {
		parsed, err := strconv.ParseInt(afterValue, 10, 64)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, "invalid after_id")
			return
		}
		afterID = parsed
	}
	var beforeID int64
	if beforeValue != "" {
		parsed, err := strconv.ParseInt(beforeValue, 10, 64)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid before_id")
			return
		}
		beforeID = parsed
	}
	st, err := s.store.ReadState(r.PathValue("id"))
	if err != nil {
		s.writeRunnerRequestLookupError(w, r.PathValue("id"), err)
		return
	}
	var events []state.RunnerEvent
	var hasMore bool
	if afterValue != "" {
		events, hasMore, err = s.store.ListRunnerEventsAfter(st.ID, afterID, diagnosticRunnerEventLimit)
	} else {
		events, hasMore, err = s.store.ListRunnerEvents(st.ID, beforeID, diagnosticRunnerEventLimit)
	}
	if err != nil {
		s.writeRunnerEventReadError(w, st.ID, err)
		return
	}
	writeJSON(w, http.StatusOK, runnerRequestEventPage{Events: events, HasMore: hasMore})
}

func (s *Server) resolveRunnerRequestIdentifier(identifier string) (state.RunnerState, error) {
	id := strings.TrimSpace(identifier)
	if strings.HasPrefix(id, "e2b-") {
		st, err := s.store.ReadState(strings.TrimPrefix(id, "e2b-"))
		if err == nil && st.RunnerName == id {
			return st, nil
		}
		if err != nil && !errors.Is(err, state.ErrNotFound) {
			return state.RunnerState{}, err
		}
	}
	return s.store.ReadState(id)
}

func (s *Server) writeRunnerRequestLookupError(w http.ResponseWriter, identifier string, err error) {
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusNotFound, "runner request not found")
		return
	}
	s.logger.Error("read runner request for diagnostics", "identifier", identifier, "error", err)
	writeError(w, http.StatusInternalServerError, "failed to read runner request")
}

func (s *Server) writeRunnerEventReadError(w http.ResponseWriter, requestID string, err error) {
	s.logger.Error("read runner request events", "id", requestID, "error", err)
	writeError(w, http.StatusInternalServerError, "failed to read runner request events")
}

func (s *Server) diagnosticWorkflowJob(ctx context.Context, repository string, jobID int64) (github.WorkflowJob, error) {
	key := strings.ToLower(strings.TrimSpace(repository)) + "#" + strconv.FormatInt(jobID, 10)
	now := time.Now()
	s.diagnosticJobMu.Lock()
	if s.diagnosticJobCache == nil {
		s.diagnosticJobCache = make(map[string]cachedDiagnosticJob)
	}
	if cached, ok := s.diagnosticJobCache[key]; ok && now.Before(cached.expiresAt) {
		s.diagnosticJobMu.Unlock()
		return cached.job, nil
	}
	s.diagnosticJobMu.Unlock()

	value, err, _ := s.diagnosticJobGroup.Do(key, func() (any, error) {
		job, err := s.gh.GetWorkflowJob(ctx, repository, jobID)
		if err != nil {
			return github.WorkflowJob{}, err
		}
		s.diagnosticJobMu.Lock()
		now := time.Now()
		for cachedKey, cached := range s.diagnosticJobCache {
			if !now.Before(cached.expiresAt) {
				delete(s.diagnosticJobCache, cachedKey)
			}
		}
		if len(s.diagnosticJobCache) >= maxDiagnosticGitHubJobCacheItems {
			var oldestKey string
			var oldestExpiry time.Time
			for cachedKey, cached := range s.diagnosticJobCache {
				if oldestKey == "" || cached.expiresAt.Before(oldestExpiry) {
					oldestKey, oldestExpiry = cachedKey, cached.expiresAt
				}
			}
			if oldestKey != "" {
				delete(s.diagnosticJobCache, oldestKey)
			}
		}
		s.diagnosticJobCache[key] = cachedDiagnosticJob{job: job, expiresAt: now.Add(diagnosticGitHubJobCacheTTL)}
		s.diagnosticJobMu.Unlock()
		return job, nil
	})
	if err != nil {
		return github.WorkflowJob{}, err
	}
	return value.(github.WorkflowJob), nil
}

func diagnoseRunnerRequest(st state.RunnerState, events []state.RunnerEvent, truncated bool, job diagnosticGitHubJob) []runnerDiagnosticFinding {
	findings := make([]runnerDiagnosticFinding, 0, 5)
	hasAcceptedJob := st.AssignedJobID != 0 || st.AssignedJobName == runnerJobStartedMarker || controlEventMessageContains(events, "runner accepted a job")
	hasRunnerExit := controlEventMessageContains(events, "runner process exited")
	hasCompletedHook := controlEventMessageContains(events, "runner completed job hook received")
	hasSandboxGone := controlEventMessageContains(events, "sandbox already gone")
	githubFailed := job.LookupStatus == "ok" && isFailureConclusion(job.Conclusion)

	if st.Status == state.StatusFailed {
		if st.FailureStage == "admission" && st.FailureReason == "profile_labels_not_matched" {
			findings = append(findings, runnerDiagnosticFinding{Code: "runner_request_unmatched", Severity: "warning", Detail: runnerRequestFailureDetail(st)})
		} else {
			findings = append(findings, runnerDiagnosticFinding{Code: "runner_request_failed", Severity: "critical", Detail: runnerRequestFailureDetail(st)})
		}
	}
	if githubFailed {
		findings = append(findings, runnerDiagnosticFinding{Code: "github_job_failed", Severity: "critical", Detail: job.Conclusion})
	}
	if githubFailed && st.Status == state.StatusCompleted {
		findings = append(findings, runnerDiagnosticFinding{Code: "request_completed_after_github_failure", Severity: "warning"})
	}
	if hasAcceptedJob && isTerminalRunnerStatus(st.Status) && !hasRunnerExit && !hasCompletedHook {
		detail := ""
		if len(events) > 0 {
			detail = events[len(events)-1].CreatedAt.Format(time.RFC3339)
		}
		severity := "warning"
		if githubFailed {
			severity = "critical"
		}
		findings = append(findings, runnerDiagnosticFinding{Code: "runner_termination_unobserved", Severity: severity, Detail: detail})
	}
	if hasSandboxGone {
		findings = append(findings, runnerDiagnosticFinding{Code: "sandbox_gone_before_cleanup", Severity: "critical"})
	}
	if truncated {
		findings = append(findings, runnerDiagnosticFinding{Code: "event_history_truncated", Severity: "warning"})
	}
	if job.LookupStatus == "unavailable" {
		findings = append(findings, runnerDiagnosticFinding{Code: "github_lookup_unavailable", Severity: "warning"})
	}
	if len(findings) == 0 {
		findings = append(findings, runnerDiagnosticFinding{Code: "no_anomaly_detected", Severity: "ok"})
	}
	return findings
}

func runnerRequestFailureDetail(st state.RunnerState) string {
	parts := make([]string, 0, 5)
	for _, value := range []string{st.FailureStage, st.FailureReason, st.LastErrorCode, st.LastErrorMessage, st.Error} {
		detail := strings.TrimSpace(value)
		if detail == "" {
			continue
		}
		duplicate := false
		for _, existing := range parts {
			if detail == existing {
				duplicate = true
				break
			}
		}
		if !duplicate {
			parts = append(parts, detail)
		}
	}
	return strings.Join(parts, ": ")
}

func controlEventMessageContains(events []state.RunnerEvent, fragment string) bool {
	for _, event := range events {
		if event.EventType == "control_log" && strings.Contains(strings.ToLower(event.Message), fragment) {
			return true
		}
	}
	return false
}

func isTerminalRunnerStatus(status string) bool {
	return status == state.StatusCompleted || status == state.StatusFailed
}
