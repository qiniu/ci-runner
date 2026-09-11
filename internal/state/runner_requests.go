package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxRunnerRequestRepositoryAccessQueryParameters = 900

// runnerd enables MigrateOnStart, so startup migration backfills legacy payload
// context before list queries use this projection. Embedders that disable
// migration must migrate the database before serving list reads. List reads
// intentionally never load raw webhook payloads.
// Source and labels_json are omitted because recordToState does not read them.
// Sandbox credential columns are omitted intentionally and remain empty in
// list-derived RunnerState values; mutation paths re-read the full record.
var runnerRequestListSelectColumns = []string{
	"id",
	"workflow_job_id",
	"github_installation_id",
	"workflow_run_id",
	"workflow_name",
	"workflow_run_attempt",
	"head_branch",
	"head_sha",
	"github_job_url",
	"pull_request_number",
	"github_context_backfilled",
	"repository_full_name",
	"requested_labels_json",
	"profile_name",
	"profile_source",
	"profile_scope_type",
	"profile_scope_id",
	"runner_group",
	"runner_name",
	"status",
	"failure_stage",
	"failure_reason",
	"last_error_code",
	"last_error_message",
	"last_error_retryable",
	"retry_count",
	"sandbox_id",
	"sandbox_config_source",
	"process_pid",
	"assigned_job_id",
	"assigned_job_name",
	"error",
	"queued_at",
	"last_attempt_at",
	"next_retry_at",
	"creating_at",
	"running_at",
	"stopping_at",
	"completed_at",
	"failed_at",
	"lease_owner",
	"lease_expires_at",
	"updated_at",
	"version",
}

type githubInstallationRepositoryAccessQueryBatch struct {
	predicates     []string
	args           []any
	parameterCount int
}

func (s *DBStore) CreateRequest(req RunnerRequest, payload []byte) (bool, RunnerState, error) {
	return s.createRequest(req, payload, StatusQueued, "", "", "")
}

func (s *DBStore) CreateRejectedRequest(req RunnerRequest, payload []byte, reason string) (bool, RunnerState, error) {
	return s.createRequest(req, payload, StatusFailed, "admission", strings.TrimSpace(reason), "runner admission rejected")
}

func (s *DBStore) createRequest(req RunnerRequest, payload []byte, status, failureStage, failureReason, errorMessage string) (bool, RunnerState, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return false, RunnerState{}, err
	}

	req.ID = sanitizeID(req.ID)
	if req.ID == "" {
		return false, RunnerState{}, fmt.Errorf("request id is empty")
	}
	req.RunnerName = sanitizeRunnerName(req.RunnerName)
	if req.RunnerName == "" {
		req.RunnerName = "e2b-" + req.ID
	}
	now := time.Now().UTC()
	if req.CreatedAt.IsZero() {
		req.CreatedAt = now
	}

	if len(req.RequestedLabels) == 0 {
		req.RequestedLabels = append([]string(nil), req.Labels...)
	}
	labelsJSON, err := json.Marshal(req.Labels)
	if err != nil {
		return false, RunnerState{}, err
	}
	requestedLabelsJSON, err := json.Marshal(req.RequestedLabels)
	if err != nil {
		return false, RunnerState{}, err
	}
	// Extract context in memory to avoid retaining raw webhook data that may be
	// sensitive. Existing github_payload_json values remain available for backfill.
	payloadLinks := githubLinksFromPayloadBytes(payload, req.RepositoryFullName, req.JobID)
	if req.PullRequestNumber > 0 && payloadLinks.pullRequestNumber == 0 {
		payloadLinks.pullRequestNumber = req.PullRequestNumber
	}
	var failedAt *time.Time
	if status == StatusFailed {
		failedAt = &now
	}
	record := runnerRequestRecord{
		ID:                      req.ID,
		Source:                  req.Source,
		GitHubInstallationID:    req.GitHubInstallationID,
		WorkflowRunID:           payloadLinks.workflowRunID,
		WorkflowName:            payloadLinks.workflowName,
		WorkflowRunAttempt:      payloadLinks.workflowRunAttempt,
		HeadBranch:              payloadLinks.headBranch,
		HeadSHA:                 payloadLinks.headSHA,
		GitHubJobURL:            payloadLinks.jobURL,
		PullRequestNumber:       payloadLinks.pullRequestNumber,
		GitHubContextBackfilled: true,
		RepositoryFullName:      req.RepositoryFullName,
		RequestedLabelsJSON:     string(requestedLabelsJSON),
		LabelsJSON:              string(labelsJSON),
		ProfileName:             req.ProfileName,
		ProfileSource:           req.ProfileSource,
		ProfileScopeType:        req.ProfileScopeType,
		ProfileScopeID:          req.ProfileScopeID,
		RunnerGroup:             req.RunnerGroup,
		RunnerName:              req.RunnerName,
		SandboxAPIURL:           strings.TrimSpace(req.SandboxAPIURL),
		SandboxAPIKeyEncrypted:  strings.TrimSpace(req.SandboxAPIKeyEncrypted),
		SandboxConfigSource:     strings.TrimSpace(req.SandboxConfigSource),
		Status:                  status,
		FailureStage:            failureStage,
		FailureReason:           failureReason,
		Error:                   errorMessage,
		QueuedAt:                req.CreatedAt,
		FailedAt:                failedAt,
		UpdatedAt:               now,
		Version:                 0,
	}
	if req.JobID != 0 {
		record.WorkflowJobID = &req.JobID
	}
	result := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&record)
	if result.Error != nil {
		return false, RunnerState{}, result.Error
	}
	if result.RowsAffected == 0 {
		st, err := s.ReadState(req.ID)
		if errors.Is(err, ErrNotFound) && req.JobID != 0 {
			var conflicting runnerRequestRecord
			conflictErr := db.First(&conflicting, "workflow_job_id = ?", req.JobID).Error
			if conflictErr == nil {
				return false, recordToState(conflicting), ErrConflict
			}
			if !errors.Is(conflictErr, gorm.ErrRecordNotFound) {
				return false, RunnerState{}, conflictErr
			}
		}
		return false, st, err
	}
	return true, recordToState(record), nil
}

func (s *DBStore) ReadRequest(id string) (RunnerRequest, error) {
	record, err := s.readRecord(id)
	if err != nil {
		return RunnerRequest{}, err
	}
	return recordToRequest(record)
}

func (s *DBStore) ReadState(id string) (RunnerState, error) {
	record, err := s.readRecord(id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RunnerState{}, ErrNotFound
		}
		return RunnerState{}, err
	}
	return recordToState(record), nil
}

func (s *DBStore) WriteState(st RunnerState) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	st.ID = sanitizeID(st.ID)
	if st.ID == "" {
		return fmt.Errorf("request id is empty")
	}
	now := time.Now().UTC()
	st.UpdatedAt = now
	applyStateTimestamps(&st, now)

	updates := map[string]any{
		"status":                    st.Status,
		"repository_full_name":      st.RepositoryFullName,
		"profile_name":              st.ProfileName,
		"profile_source":            st.ProfileSource,
		"profile_scope_type":        st.ProfileScopeType,
		"profile_scope_id":          st.ProfileScopeID,
		"runner_group":              st.RunnerGroup,
		"runner_name":               sanitizeRunnerName(st.RunnerName),
		"sandbox_id":                st.SandboxID,
		"sandbox_api_url":           strings.TrimSpace(st.SandboxAPIURL),
		"sandbox_api_key_encrypted": strings.TrimSpace(st.SandboxAPIKeyEncrypted),
		"sandbox_config_source":     strings.TrimSpace(st.SandboxConfigSource),
		"process_pid":               st.ProcessPID,
		"assigned_job_id":           st.AssignedJobID,
		"assigned_job_name":         st.AssignedJobName,
		"error":                     st.Error,
		"failure_stage":             st.FailureStage,
		"failure_reason":            st.FailureReason,
		"last_error_code":           st.LastErrorCode,
		"last_error_message":        st.LastErrorMessage,
		"last_error_retryable":      st.LastErrorRetryable,
		"retry_count":               st.RetryCount,
		"updated_at":                st.UpdatedAt,
		"version":                   st.Version + 1,
		"last_attempt_at":           zeroTimeToPointer(st.LastAttemptAt),
		"next_retry_at":             zeroTimeToPointer(st.NextRetryAt),
		"creating_at":               zeroTimeToPointer(st.CreatingAt),
		"running_at":                zeroTimeToPointer(st.RunningAt),
		"stopping_at":               zeroTimeToPointer(st.StoppingAt),
		"completed_at":              zeroTimeToPointer(st.CompletedAt),
		"failed_at":                 zeroTimeToPointer(st.FailedAt),
		"lease_owner":               st.LeaseOwner,
		"lease_expires_at":          zeroTimeToPointer(st.LeaseExpiresAt),
	}
	result := db.Model(&runnerRequestRecord{}).
		Where("id = ? AND version = ?", st.ID, st.Version).
		Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrConflict
	}
	return nil
}

func (s *DBStore) ListStates() ([]RunnerState, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var records []runnerRequestRecord
	if err := db.
		Select(runnerRequestListSelectColumns).
		Order("queued_at DESC").
		Find(&records).Error; err != nil {
		return nil, err
	}
	states := make([]RunnerState, 0, len(records))
	for _, record := range records {
		states = append(states, recordToState(record))
	}
	return states, nil
}

func (s *DBStore) ListActiveStates() ([]RunnerState, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var records []runnerRequestRecord
	if err := db.
		Select(runnerRequestListSelectColumns).
		Where("status IN ?", activeStatuses()).
		Order("queued_at DESC").
		Find(&records).Error; err != nil {
		return nil, err
	}
	states := make([]RunnerState, 0, len(records))
	for _, record := range records {
		states = append(states, recordToState(record))
	}
	return states, nil
}

func (s *DBStore) ListMismatchedCompletedStates(limit int) ([]RunnerState, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	var records []runnerRequestRecord
	if err := db.
		Select(runnerRequestListSelectColumns).
		Where("status = ?", StatusCompleted).
		Where("workflow_job_id IS NOT NULL AND workflow_job_id != 0").
		Where("assigned_job_id != 0 AND assigned_job_id != workflow_job_id").
		Order("updated_at DESC").
		Limit(limit).
		Find(&records).Error; err != nil {
		return nil, err
	}
	states := make([]RunnerState, 0, len(records))
	for _, record := range records {
		states = append(states, recordToState(record))
	}
	return states, nil
}

func (s *DBStore) ListFailedWorkflowJobStates(limit int) ([]RunnerState, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	var records []runnerRequestRecord
	if err := db.
		Select(runnerRequestListSelectColumns).
		Where("status = ?", StatusFailed).
		Where("workflow_job_id IS NOT NULL AND workflow_job_id != 0").
		Where("failure_stage IN ?", []string{"recovery", "cleanup"}).
		Order("updated_at DESC").
		Limit(limit).
		Find(&records).Error; err != nil {
		return nil, err
	}
	states := make([]RunnerState, 0, len(records))
	for _, record := range records {
		states = append(states, recordToState(record))
	}
	return states, nil
}

func (s *DBStore) ListStatesPage(limit, offset int) ([]RunnerState, int64, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 {
		limit = 100
	}
	if offset < 0 {
		offset = 0
	}
	var total int64
	if err := db.Model(&runnerRequestRecord{}).Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []runnerRequestRecord
	if err := db.
		Select(runnerRequestListSelectColumns).
		Order("queued_at DESC, id ASC").
		Limit(limit).
		Offset(offset).
		Find(&records).Error; err != nil {
		return nil, 0, err
	}
	states := make([]RunnerState, 0, len(records))
	for _, record := range records {
		states = append(states, recordToState(record))
	}
	return states, total, nil
}

func (s *DBStore) ListStatesForRepositories(repositories []string, limit int) ([]RunnerState, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	repositories = uniqueTrimmed(repositories)
	if len(repositories) == 0 {
		return []RunnerState{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	var records []runnerRequestRecord
	if err := db.
		Select(runnerRequestListSelectColumns).
		Where("repository_full_name IN ?", repositories).
		Order("queued_at DESC").
		Limit(limit).
		Find(&records).Error; err != nil {
		return nil, err
	}
	states := make([]RunnerState, 0, len(records))
	for _, record := range records {
		states = append(states, recordToState(record))
	}
	return states, nil
}

func (s *DBStore) ListStatesForGitHubInstallationRepositories(access []GitHubInstallationRepositoryAccess, limit, offset int) ([]RunnerState, int64, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, 0, err
	}
	batches := githubInstallationRepositoryAccessQueryBatches(access, maxRunnerRequestRepositoryAccessQueryParameters)
	if len(batches) == 0 {
		return []RunnerState{}, 0, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	if offset < 0 {
		offset = 0
	}
	var total int64
	for _, batch := range batches {
		var batchTotal int64
		if err := db.Model(&runnerRequestRecord{}).
			Where(strings.Join(batch.predicates, " OR "), batch.args...).
			Count(&batchTotal).Error; err != nil {
			return nil, 0, err
		}
		total += batchTotal
	}
	if int64(offset) >= total {
		return []RunnerState{}, total, nil
	}
	queryLimit64 := total
	if int64(limit) <= total-int64(offset) {
		queryLimit64 = int64(offset) + int64(limit)
	}
	maxInt := int64(^uint(0) >> 1)
	if queryLimit64 > maxInt {
		queryLimit64 = maxInt
	}
	queryLimit := int(queryLimit64)
	recordsByID := make(map[string]runnerRequestRecord)
	for _, batch := range batches {
		var batchRecords []runnerRequestRecord
		if err := db.
			Select(runnerRequestListSelectColumns).
			Where(strings.Join(batch.predicates, " OR "), batch.args...).
			Order("queued_at DESC, id ASC").
			Limit(queryLimit).
			Find(&batchRecords).Error; err != nil {
			return nil, 0, err
		}
		for _, record := range batchRecords {
			recordsByID[record.ID] = record
		}
	}
	records := make([]runnerRequestRecord, 0, len(recordsByID))
	for _, record := range recordsByID {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].QueuedAt.Equal(records[j].QueuedAt) {
			return records[i].ID < records[j].ID
		}
		return records[i].QueuedAt.After(records[j].QueuedAt)
	})
	start := offset
	if start > len(records) {
		start = len(records)
	}
	end := start + limit
	if end > len(records) {
		end = len(records)
	}
	page := records[start:end]
	states := make([]RunnerState, 0, len(page))
	for _, record := range page {
		states = append(states, recordToState(record))
	}
	return states, total, nil
}

func githubInstallationRepositoryAccessQueryBatches(access []GitHubInstallationRepositoryAccess, maxParameters int) []githubInstallationRepositoryAccessQueryBatch {
	if maxParameters < 2 {
		maxParameters = 2
	}
	batches := make([]githubInstallationRepositoryAccessQueryBatch, 0, len(access))
	for _, item := range normalizeGitHubInstallationRepositoryAccess(access) {
		repositories := item.Repositories
		for len(repositories) > 0 {
			repositoryCount := min(len(repositories), maxParameters-1)
			repositoryChunk := append([]string(nil), repositories[:repositoryCount]...)
			batches = append(batches, githubInstallationRepositoryAccessQueryBatch{
				predicates:     []string{"(github_installation_id = ? AND LOWER(repository_full_name) IN ?)"},
				args:           []any{item.InstallationID, repositoryChunk},
				parameterCount: 1 + repositoryCount,
			})
			repositories = repositories[repositoryCount:]
		}
	}
	return batches
}

func normalizeGitHubInstallationRepositoryAccess(access []GitHubInstallationRepositoryAccess) []GitHubInstallationRepositoryAccess {
	repositoriesByInstallation := make(map[int64]map[string]struct{})
	for _, item := range access {
		if item.InstallationID <= 0 {
			continue
		}
		if repositoriesByInstallation[item.InstallationID] == nil {
			repositoriesByInstallation[item.InstallationID] = make(map[string]struct{})
		}
		for _, repository := range uniqueLowerTrimmed(item.Repositories) {
			repositoriesByInstallation[item.InstallationID][repository] = struct{}{}
		}
	}
	installationIDs := make([]int64, 0, len(repositoriesByInstallation))
	for installationID, repositories := range repositoriesByInstallation {
		if len(repositories) > 0 {
			installationIDs = append(installationIDs, installationID)
		}
	}
	sort.Slice(installationIDs, func(i, j int) bool { return installationIDs[i] < installationIDs[j] })
	normalized := make([]GitHubInstallationRepositoryAccess, 0, len(installationIDs))
	for _, installationID := range installationIDs {
		repositories := make([]string, 0, len(repositoriesByInstallation[installationID]))
		for repository := range repositoriesByInstallation[installationID] {
			repositories = append(repositories, repository)
		}
		sort.Strings(repositories)
		normalized = append(normalized, GitHubInstallationRepositoryAccess{
			InstallationID: installationID,
			Repositories:   repositories,
		})
	}
	return normalized
}

func (s *DBStore) ListStatesForGitHubInstallations(installationIDs []int64, limit int) ([]RunnerState, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	installationIDs = uniquePositiveInt64s(installationIDs)
	if len(installationIDs) == 0 {
		return []RunnerState{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	var records []runnerRequestRecord
	if err := db.
		Select(runnerRequestListSelectColumns).
		Where("github_installation_id IN ?", installationIDs).
		Order("queued_at DESC").
		Limit(limit).
		Find(&records).Error; err != nil {
		return nil, err
	}
	states := make([]RunnerState, 0, len(records))
	for _, record := range records {
		states = append(states, recordToState(record))
	}
	return states, nil
}

func activeStatuses() []string {
	return []string{StatusQueued, StatusCreating, StatusRunning, StatusStopping}
}

func (s *DBStore) ActiveCount() (int, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return 0, err
	}
	var count int64
	if err := db.Model(&runnerRequestRecord{}).
		Where("status IN ?", activeStatuses()).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

func (s *DBStore) InFlightCount() (int, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return 0, err
	}
	var count int64
	if err := db.Model(&runnerRequestRecord{}).
		Where("status IN ?", []string{StatusCreating, StatusRunning, StatusStopping}).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

func (s *DBStore) ActiveCountForProfile(name string) (int, error) {
	return s.countStatesForProfile(name, []string{StatusQueued, StatusCreating, StatusRunning, StatusStopping})
}

func (s *DBStore) InFlightCountForProfile(name string) (int, error) {
	return s.countStatesForProfileSource(name, []string{"", "global"}, []string{StatusCreating, StatusRunning, StatusStopping})
}

func (s *DBStore) ClaimNextRunnable(workerID string, now time.Time, leaseTTL time.Duration) (RunnerRequest, RunnerState, bool, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return RunnerRequest{}, RunnerState{}, false, err
	}
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return RunnerRequest{}, RunnerState{}, false, fmt.Errorf("worker id is required")
	}
	leaseExpiresAt := now.UTC().Add(leaseTTL)
	var claimed runnerRequestRecord
	err = db.Transaction(func(tx *gorm.DB) error {
		var candidate runnerRequestRecord
		if err := tx.
			Where("status = ?", StatusQueued).
			Where("(next_retry_at IS NULL OR next_retry_at <= ?)", now.UTC()).
			Where("(lease_expires_at IS NULL OR lease_expires_at <= ?)", now.UTC()).
			Order("queued_at ASC").
			First(&candidate).Error; err != nil {
			return err
		}
		result := tx.Model(&runnerRequestRecord{}).
			Where("id = ?", candidate.ID).
			Where("status = ?", StatusQueued).
			Where("(lease_expires_at IS NULL OR lease_expires_at <= ?)", now.UTC()).
			Updates(map[string]any{
				"lease_owner":      workerID,
				"lease_expires_at": leaseExpiresAt,
				"last_attempt_at":  now.UTC(),
				"updated_at":       now.UTC(),
				"version":          candidate.Version + 1,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrConflict
		}
		if err := tx.First(&claimed, "id = ?", candidate.ID).Error; err != nil {
			return err
		}
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RunnerRequest{}, RunnerState{}, false, nil
	}
	if errors.Is(err, ErrConflict) {
		return RunnerRequest{}, RunnerState{}, false, nil
	}
	if err != nil {
		return RunnerRequest{}, RunnerState{}, false, err
	}
	req, err := recordToRequest(claimed)
	if err != nil {
		return RunnerRequest{}, RunnerState{}, false, err
	}
	return req, recordToState(claimed), true, nil
}

func (s *DBStore) ReleaseLease(id, workerID string) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.Model(&runnerRequestRecord{}).
		Where("id = ? AND lease_owner = ?", sanitizeID(id), strings.TrimSpace(workerID)).
		Updates(map[string]any{
			"lease_owner":      "",
			"lease_expires_at": nil,
		}).Error
}

func (s *DBStore) RetryRequest(id string, now time.Time) (RunnerState, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return RunnerState{}, err
	}
	id = sanitizeID(id)
	var saved runnerRequestRecord
	if err := db.Transaction(func(tx *gorm.DB) error {
		var record runnerRequestRecord
		if err := tx.First(&record, "id = ?", id).Error; err != nil {
			return err
		}
		switch record.Status {
		case StatusFailed:
		case StatusQueued:
			if record.NextRetryAt == nil || !record.LastErrorRetryable {
				return ErrRetryNotAllowed
			}
		default:
			return ErrRetryNotAllowed
		}
		record.Status = StatusQueued
		record.Error = ""
		record.FailureStage = ""
		record.FailureReason = ""
		record.LastErrorCode = ""
		record.LastErrorMessage = ""
		record.LastErrorRetryable = false
		record.SandboxID = ""
		record.ProcessPID = 0
		record.AssignedJobID = 0
		record.AssignedJobName = ""
		record.NextRetryAt = nil
		record.LeaseOwner = ""
		record.LeaseExpiresAt = nil
		record.CreatingAt = nil
		record.RunningAt = nil
		record.StoppingAt = nil
		record.CompletedAt = nil
		record.FailedAt = nil
		record.SandboxAPIURL = ""
		record.SandboxAPIKeyEncrypted = ""
		record.SandboxConfigSource = ""
		record.UpdatedAt = now.UTC()
		record.Version++
		if err := tx.Model(&runnerRequestRecord{}).
			Where("id = ? AND version = ?", record.ID, record.Version-1).
			Updates(map[string]any{
				"status":                    record.Status,
				"error":                     record.Error,
				"failure_stage":             record.FailureStage,
				"failure_reason":            record.FailureReason,
				"last_error_code":           record.LastErrorCode,
				"last_error_message":        record.LastErrorMessage,
				"last_error_retryable":      record.LastErrorRetryable,
				"sandbox_id":                record.SandboxID,
				"process_pid":               record.ProcessPID,
				"assigned_job_id":           record.AssignedJobID,
				"assigned_job_name":         record.AssignedJobName,
				"next_retry_at":             nil,
				"lease_owner":               record.LeaseOwner,
				"lease_expires_at":          nil,
				"creating_at":               nil,
				"running_at":                nil,
				"stopping_at":               nil,
				"completed_at":              nil,
				"failed_at":                 nil,
				"sandbox_api_url":           record.SandboxAPIURL,
				"sandbox_api_key_encrypted": record.SandboxAPIKeyEncrypted,
				"sandbox_config_source":     record.SandboxConfigSource,
				"updated_at":                record.UpdatedAt,
				"version":                   record.Version,
			}).Error; err != nil {
			return err
		}
		if err := tx.First(&saved, "id = ?", id).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		return RunnerState{}, err
	}
	return recordToState(saved), nil
}

func (s *DBStore) AppendLog(id, name string, data []byte) {
	if len(data) == 0 {
		return
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		slog.Default().Warn("append runner log failed", "id", id, "name", name, "error", err)
		return
	}
	eventType, err := logEventType(name)
	if err != nil {
		slog.Default().Warn("append runner log failed", "id", id, "name", name, "error", err)
		return
	}
	if err := db.Create(&runnerEventRecord{
		RequestID: sanitizeID(id),
		EventType: eventType,
		Message:   string(data),
		CreatedAt: time.Now().UTC(),
	}).Error; err != nil {
		slog.Default().Warn("append runner log failed", "id", id, "name", name, "error", err)
	}
}

func (s *DBStore) ReadLog(id, name string, maxBytes int64) ([]byte, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	eventType, err := logEventType(name)
	if err != nil {
		return nil, err
	}
	var events []runnerEventRecord
	if err := db.Where("request_id = ? AND event_type = ?", sanitizeID(id), eventType).
		Order("id ASC").
		Find(&events).Error; err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, os.ErrNotExist
	}
	var buf bytes.Buffer
	for _, event := range events {
		buf.WriteString(event.Message)
	}
	data := buf.Bytes()
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		data = data[len(data)-int(maxBytes):]
	}
	return append([]byte(nil), data...), nil
}

func (s *DBStore) ListRunnerEvents(id string, beforeID int64, limit int, eventTypes ...string) ([]RunnerEvent, bool, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, false, err
	}
	limit = min(max(limit, 1), 500)
	var records []runnerEventRecord
	query := db.Where("request_id = ?", sanitizeID(id))
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	if len(eventTypes) > 0 {
		query = query.Where("event_type IN ?", eventTypes)
	}
	if err := query.
		Order("id DESC").
		Limit(limit + 1).
		Find(&records).Error; err != nil {
		return nil, false, err
	}
	truncated := len(records) > limit
	if truncated {
		records = records[:limit]
	}
	events := make([]RunnerEvent, len(records))
	for i := range records {
		record := records[len(records)-1-i]
		events[i] = RunnerEvent{
			ID:        record.ID,
			EventType: record.EventType,
			Stage:     record.Stage,
			Message:   record.Message,
			CreatedAt: record.CreatedAt,
		}
	}
	return events, truncated, nil
}

func (s *DBStore) ListRunnerEventsAfter(id string, afterID int64, limit int, eventTypes ...string) ([]RunnerEvent, bool, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, false, err
	}
	limit = min(max(limit, 1), 500)
	query := db.Where("request_id = ? AND id > ?", sanitizeID(id), afterID)
	if len(eventTypes) > 0 {
		query = query.Where("event_type IN ?", eventTypes)
	}
	var records []runnerEventRecord
	if err := query.
		Order("id ASC").
		Limit(limit + 1).
		Find(&records).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	events := make([]RunnerEvent, len(records))
	for i, record := range records {
		events[i] = RunnerEvent{
			ID:        record.ID,
			EventType: record.EventType,
			Stage:     record.Stage,
			Message:   record.Message,
			CreatedAt: record.CreatedAt,
		}
	}
	return events, hasMore, nil
}

func (s *DBStore) readRecord(id string) (runnerRequestRecord, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return runnerRequestRecord{}, err
	}
	var record runnerRequestRecord
	if err := db.First(&record, "id = ?", sanitizeID(id)).Error; err != nil {
		return runnerRequestRecord{}, err
	}
	return record, nil
}

func (s *DBStore) countStatesForProfile(name string, statuses []string) (int, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return 0, err
	}
	var count int64
	if err := db.Model(&runnerRequestRecord{}).
		Where("profile_name = ?", strings.TrimSpace(name)).
		Where("status IN ?", statuses).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

func (s *DBStore) countStatesForProfileSource(name string, sources, statuses []string) (int, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return 0, err
	}
	var count int64
	if err := db.Model(&runnerRequestRecord{}).
		Where("profile_name = ?", strings.TrimSpace(name)).
		Where("profile_source IN ?", sources).
		Where("status IN ?", statuses).
		Count(&count).Error; err != nil {
		return 0, err
	}
	return int(count), nil
}

func applyStateTimestamps(st *RunnerState, now time.Time) {
	switch st.Status {
	case StatusCreating:
		if st.CreatingAt.IsZero() {
			st.CreatingAt = now
		}
	case StatusRunning:
		if st.CreatingAt.IsZero() {
			st.CreatingAt = now
		}
		if st.RunningAt.IsZero() {
			st.RunningAt = now
		}
	case StatusStopping:
		if st.StoppingAt.IsZero() {
			st.StoppingAt = now
		}
	case StatusCompleted:
		if st.StoppingAt.IsZero() {
			st.StoppingAt = now
		}
		if st.CompletedAt.IsZero() {
			st.CompletedAt = now
		}
	case StatusFailed:
		if st.FailedAt.IsZero() {
			st.FailedAt = now
		}
	}
}
