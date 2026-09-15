package state

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/labelutil"
)

func recordToRequest(record runnerRequestRecord) (RunnerRequest, error) {
	var labels []string
	if err := json.Unmarshal([]byte(record.LabelsJSON), &labels); err != nil {
		return RunnerRequest{}, err
	}
	var requestedLabels []string
	if record.RequestedLabelsJSON != "" {
		if err := json.Unmarshal([]byte(record.RequestedLabelsJSON), &requestedLabels); err != nil {
			return RunnerRequest{}, err
		}
	}
	return RunnerRequest{
		ID:                     record.ID,
		Source:                 record.Source,
		JobID:                  pointerToInt64(record.WorkflowJobID),
		PullRequestNumber:      record.PullRequestNumber,
		GitHubInstallationID:   record.GitHubInstallationID,
		RepositoryFullName:     record.RepositoryFullName,
		RequestedLabels:        requestedLabels,
		Labels:                 labels,
		ProfileName:            record.ProfileName,
		ProfileSource:          record.ProfileSource,
		ProfileScopeType:       record.ProfileScopeType,
		ProfileScopeID:         record.ProfileScopeID,
		RunnerGroup:            record.RunnerGroup,
		RunnerName:             record.RunnerName,
		SandboxAPIURL:          record.SandboxAPIURL,
		SandboxAPIKeyEncrypted: record.SandboxAPIKeyEncrypted,
		SandboxConfigSource:    record.SandboxConfigSource,
		CreatedAt:              record.QueuedAt,
	}, nil
}

func recordToState(record runnerRequestRecord) RunnerState {
	requestedLabels, _ := labelsFromJSON(record.RequestedLabelsJSON)
	githubLinks := githubLinksFromRecord(record)
	return RunnerState{
		ID:                     record.ID,
		Status:                 record.Status,
		GitHubInstallationID:   record.GitHubInstallationID,
		RepositoryFullName:     record.RepositoryFullName,
		RequestedLabels:        requestedLabels,
		ProfileName:            record.ProfileName,
		ProfileSource:          record.ProfileSource,
		ProfileScopeType:       record.ProfileScopeType,
		ProfileScopeID:         record.ProfileScopeID,
		RunnerGroup:            record.RunnerGroup,
		RunnerName:             record.RunnerName,
		SandboxID:              record.SandboxID,
		SandboxAPIURL:          record.SandboxAPIURL,
		SandboxAPIKeyEncrypted: record.SandboxAPIKeyEncrypted,
		SandboxConfigSource:    record.SandboxConfigSource,
		SandboxRegion:          record.SandboxRegion,
		ResolvedTemplateID:     record.ResolvedTemplateID,
		TemplateVersion:        record.TemplateVersion,
		RunnerVersion:          record.RunnerVersion,
		ProcessPID:             record.ProcessPID,
		WorkflowJobID:          pointerToInt64(record.WorkflowJobID),
		WorkflowRunID:          githubLinks.workflowRunID,
		WorkflowName:           githubLinks.workflowName,
		WorkflowRunAttempt:     githubLinks.workflowRunAttempt,
		HeadBranch:             githubLinks.headBranch,
		HeadSHA:                githubLinks.headSHA,
		GitHubJobURL:           githubLinks.jobURL,
		GitHubJobName:          record.GitHubJobName,
		GitHubJobStatus:        record.GitHubJobStatus,
		GitHubJobConclusion:    record.GitHubJobConclusion,
		GitHubJobRunnerName:    record.GitHubJobRunnerName,
		GitHubJobObservedAt:    pointerToTime(record.GitHubJobObservedAt),
		PullRequestNumber:      githubLinks.pullRequestNumber,
		AssignedJobID:          record.AssignedJobID,
		AssignedJobName:        record.AssignedJobName,
		TerminationSource:      record.TerminationSource,
		RunnerExitCode:         record.RunnerExitCode,
		Error:                  record.Error,
		FailureStage:           record.FailureStage,
		FailureReason:          record.FailureReason,
		LastErrorCode:          record.LastErrorCode,
		LastErrorMessage:       record.LastErrorMessage,
		LastErrorRetryable:     record.LastErrorRetryable,
		RetryCount:             record.RetryCount,
		UpdatedAt:              record.UpdatedAt,
		CreatedAt:              record.QueuedAt,
		LastAttemptAt:          pointerToTime(record.LastAttemptAt),
		NextRetryAt:            pointerToTime(record.NextRetryAt),
		CreatingAt:             pointerToTime(record.CreatingAt),
		RunningAt:              pointerToTime(record.RunningAt),
		StoppingAt:             pointerToTime(record.StoppingAt),
		CompletedAt:            pointerToTime(record.CompletedAt),
		FailedAt:               pointerToTime(record.FailedAt),
		LeaseOwner:             record.LeaseOwner,
		LeaseExpiresAt:         pointerToTime(record.LeaseExpiresAt),
		Version:                record.Version,
	}
}

func githubLinksFromRecord(record runnerRequestRecord) githubPayloadLinks {
	links := githubPayloadLinks{
		workflowRunID:      record.WorkflowRunID,
		workflowName:       record.WorkflowName,
		workflowRunAttempt: record.WorkflowRunAttempt,
		headBranch:         record.HeadBranch,
		headSHA:            record.HeadSHA,
		jobURL:             record.GitHubJobURL,
		pullRequestNumber:  record.PullRequestNumber,
	}
	if !record.GitHubContextBackfilled {
		links = mergeGitHubPayloadLinks(links, githubLinksFromPayload(record))
	}
	if links.jobURL == "" {
		links.jobURL = githubJobURL(record.RepositoryFullName, links.workflowRunID, effectiveRunnerRequestJobID(record))
	}
	links.jobURL = appendPullRequestQuery(links.jobURL, links.pullRequestNumber)
	return links
}

func mergeGitHubPayloadLinks(links githubPayloadLinks, fallback githubPayloadLinks) githubPayloadLinks {
	if links.workflowRunID == 0 {
		links.workflowRunID = fallback.workflowRunID
	}
	if links.workflowName == "" {
		links.workflowName = fallback.workflowName
	}
	if links.workflowRunAttempt == 0 {
		links.workflowRunAttempt = fallback.workflowRunAttempt
	}
	if links.headBranch == "" {
		links.headBranch = fallback.headBranch
	}
	if links.headSHA == "" {
		links.headSHA = fallback.headSHA
	}
	if links.jobURL == "" {
		links.jobURL = fallback.jobURL
	}
	if links.pullRequestNumber == 0 {
		links.pullRequestNumber = fallback.pullRequestNumber
	}
	return links
}

type githubPayloadLinks struct {
	workflowRunID      int64
	workflowName       string
	workflowRunAttempt int64
	headBranch         string
	headSHA            string
	jobURL             string
	pullRequestNumber  int64
}

type githubPayloadPullRequest struct {
	Number int64 `json:"number"`
}

func githubInstallationIDFromPayload(payloadJSON string) int64 {
	var payload struct {
		Installation struct {
			ID int64 `json:"id"`
		} `json:"installation"`
	}
	if payloadJSON == "" || json.Unmarshal([]byte(payloadJSON), &payload) != nil {
		return 0
	}
	return payload.Installation.ID
}

func githubLinksFromPayload(record runnerRequestRecord) githubPayloadLinks {
	return githubLinksFromPayloadBytes([]byte(record.GitHubPayloadJSON), record.RepositoryFullName, effectiveRunnerRequestJobID(record))
}

func effectiveRunnerRequestJobID(record runnerRequestRecord) int64 {
	if record.AssignedJobID != 0 {
		return record.AssignedJobID
	}
	return pointerToInt64(record.WorkflowJobID)
}

func githubLinksFromPayloadBytes(payloadJSON []byte, repositoryFullName string, jobID int64) githubPayloadLinks {
	var payload struct {
		WorkflowJob struct {
			RunID        int64                      `json:"run_id"`
			WorkflowName string                     `json:"workflow_name"`
			RunAttempt   int64                      `json:"run_attempt"`
			HeadBranch   string                     `json:"head_branch"`
			HeadSHA      string                     `json:"head_sha"`
			HTMLURL      string                     `json:"html_url"`
			PullRequests []githubPayloadPullRequest `json:"pull_requests"`
			WorkflowRun  struct {
				ID int64 `json:"id"`
			} `json:"workflow_run"`
		} `json:"workflow_job"`
		WorkflowRun struct {
			ID           int64                      `json:"id"`
			Name         string                     `json:"name"`
			RunAttempt   int64                      `json:"run_attempt"`
			HeadBranch   string                     `json:"head_branch"`
			HeadSHA      string                     `json:"head_sha"`
			HTMLURL      string                     `json:"html_url"`
			PullRequests []githubPayloadPullRequest `json:"pull_requests"`
		} `json:"workflow_run"`
		PullRequest struct {
			Number int64 `json:"number"`
		} `json:"pull_request"`
	}
	if len(payloadJSON) > 0 {
		_ = json.Unmarshal(payloadJSON, &payload)
	}

	runID := payload.WorkflowJob.RunID
	if runID == 0 {
		runID = payload.WorkflowJob.WorkflowRun.ID
	}
	if runID == 0 {
		runID = payload.WorkflowRun.ID
	}
	workflowName := firstNonEmpty(payload.WorkflowJob.WorkflowName, payload.WorkflowRun.Name)
	runAttempt := payload.WorkflowJob.RunAttempt
	if runAttempt == 0 {
		runAttempt = payload.WorkflowRun.RunAttempt
	}
	headBranch := firstNonEmpty(payload.WorkflowJob.HeadBranch, payload.WorkflowRun.HeadBranch)
	headSHA := firstNonEmpty(payload.WorkflowJob.HeadSHA, payload.WorkflowRun.HeadSHA)

	prNumber := firstPullRequestNumber(payload.WorkflowJob.PullRequests)
	if prNumber == 0 {
		prNumber = firstPullRequestNumber(payload.WorkflowRun.PullRequests)
	}
	if prNumber == 0 {
		prNumber = payload.PullRequest.Number
	}

	jobURL := payload.WorkflowJob.HTMLURL
	if jobURL == "" {
		jobURL = githubJobURL(repositoryFullName, runID, jobID)
	}
	jobURL = appendPullRequestQuery(jobURL, prNumber)

	return githubPayloadLinks{
		workflowRunID:      runID,
		workflowName:       workflowName,
		workflowRunAttempt: runAttempt,
		headBranch:         headBranch,
		headSHA:            headSHA,
		jobURL:             jobURL,
		pullRequestNumber:  prNumber,
	}
}

func githubJobURL(repositoryFullName string, runID, jobID int64) string {
	if repositoryFullName == "" || runID <= 0 || jobID <= 0 {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/actions/runs/%d/job/%d", repositoryFullName, runID, jobID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func firstPullRequestNumber(pulls []githubPayloadPullRequest) int64 {
	if len(pulls) == 0 {
		return 0
	}
	return pulls[0].Number
}

func appendPullRequestQuery(rawURL string, prNumber int64) string {
	if rawURL == "" || prNumber <= 0 {
		return rawURL
	}
	if hasRawQueryParameter(rawURL, "pr") {
		return rawURL
	}
	separator := "?"
	if strings.Contains(rawURL, "?") {
		separator = "&"
	}
	return fmt.Sprintf("%s%spr=%d", rawURL, separator, prNumber)
}

func hasRawQueryParameter(rawURL, name string) bool {
	queryStart := strings.IndexByte(rawURL, '?')
	fragmentStart := strings.IndexByte(rawURL, '#')
	if queryStart < 0 || (fragmentStart >= 0 && fragmentStart < queryStart) {
		return false
	}
	queryEnd := len(rawURL)
	if fragmentStart >= 0 {
		queryEnd = fragmentStart
	}
	rawQuery := rawURL[queryStart+1 : queryEnd]
	for rawQuery != "" {
		field, rest, found := strings.Cut(rawQuery, "&")
		fieldName, _, _ := strings.Cut(field, "=")
		if fieldName == name {
			return true
		}
		if !found {
			break
		}
		rawQuery = rest
	}
	return false
}

func recordToProfile(record runnerProfileRecord) (RunnerProfile, error) {
	var labels []string
	if err := json.Unmarshal([]byte(record.LabelsJSON), &labels); err != nil {
		return RunnerProfile{}, err
	}
	requiredLabels := []string{}
	if record.RequiredLabelsJSON != nil {
		data := strings.TrimSpace(*record.RequiredLabelsJSON)
		if data != "" && data != "null" {
			if err := json.Unmarshal([]byte(data), &requiredLabels); err != nil {
				return RunnerProfile{}, err
			}
			if requiredLabels == nil {
				requiredLabels = []string{}
			}
		}
	}
	return RunnerProfile{
		Name:                record.Name,
		Labels:              labels,
		RequiredLabels:      requiredLabels,
		TemplateID:          record.TemplateID,
		DefaultTemplateName: record.DefaultTemplateName,
		RunnerGroup:         record.RunnerGroup,
		MaxConcurrency:      record.MaxConcurrency,
		MinIdle:             record.MinIdle,
		Priority:            record.Priority,
		Enabled:             record.Enabled,
		ManagedBy:           record.ManagedBy,
		CatalogRevision:     record.CatalogRevision,
		CreatedAt:           record.CreatedAt,
		UpdatedAt:           record.UpdatedAt,
	}, nil
}

func uniqueTrimmed(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func uniqueLowerTrimmed(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func uniquePositiveInt64s(values []int64) []int64 {
	seen := map[int64]bool{}
	out := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func zeroTimeToPointer(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	tt := t.UTC()
	return &tt
}

func pointerToTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}

func pointerToInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func labelsFromJSON(data string) ([]string, error) {
	if strings.TrimSpace(data) == "" {
		return nil, nil
	}
	var labels []string
	if err := json.Unmarshal([]byte(data), &labels); err != nil {
		return nil, err
	}
	return labels, nil
}

func labelsMatch(jobLabels, required []string) bool {
	return labelutil.Match(jobLabels, required)
}

func logEventType(name string) (string, error) {
	switch filepath.Base(name) {
	case "control.log":
		return "control_log", nil
	case "stdout.log":
		return "stdout_log", nil
	case "stderr.log":
		return "stderr_log", nil
	default:
		return "", fmt.Errorf("unsupported log name")
	}
}

func sanitizeID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.ReplaceAll(id, "/", "-")
	id = strings.ReplaceAll(id, "\\", "-")
	id = strings.ReplaceAll(id, "..", "-")
	return id
}

func sanitizeRunnerName(name string) string {
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "-")
	name = strings.ReplaceAll(name, "\\", "-")
	return name
}

func normalizeOAuthProvider(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

func normalizeOAuthSubject(subject string) string {
	return strings.TrimSpace(subject)
}

func normalizeOAuthLogin(login string) string {
	return strings.ToLower(strings.TrimSpace(login))
}

func normalizePlatformRole(role string) string {
	role = strings.ToLower(strings.TrimSpace(role))
	switch role {
	case "admin", "user":
		return role
	default:
		return ""
	}
}
