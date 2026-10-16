package state

import (
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	maxDeploymentEventMessage = 64 << 10
	maxDeploymentEvents       = 2000
	maxDeploymentEventBytes   = 8 << 20
)

var (
	deploymentRepositoryPattern = regexp.MustCompile(`^[^/\s]+/[^/\s]+$`)
	deploymentPullRefPattern    = regexp.MustCompile(`^refs/pull/[1-9][0-9]*/head$`)
)

func (s *DBStore) CreateDeployment(deployment Deployment) (Deployment, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return Deployment{}, err
	}
	return s.createDeployment(db, deployment)
}

// CreateDeploymentWithScopeLimit inserts a deployment while holding the
// durable scope anchor row. This keeps concurrent admissions serialized across
// runnerd processes that share a database.
func (s *DBStore) CreateDeploymentWithScopeLimit(deployment Deployment, maxConcurrent int) (Deployment, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return Deployment{}, err
	}
	var created Deployment
	err = db.Transaction(func(tx *gorm.DB) error {
		if err := checkDeploymentQuota(tx, deployment, maxConcurrent, ""); err != nil {
			return err
		}
		var createErr error
		created, createErr = s.createDeployment(tx, deployment)
		return createErr
	})
	return created, err
}

func (s *DBStore) createDeployment(db *gorm.DB, deployment Deployment) (Deployment, error) {
	deployment.RevisionNumber = 1
	deployment, err := normalizeDeployment(deployment)
	if err != nil {
		return Deployment{}, err
	}
	now := time.Now().UTC()
	if deployment.CreatedAt.IsZero() {
		deployment.CreatedAt = now
	}
	if deployment.UpdatedAt.IsZero() {
		deployment.UpdatedAt = deployment.CreatedAt
	}
	if deployment.Status == "" {
		deployment.Status = DeploymentStatusQueued
	}
	record := deploymentRecord{
		CreateRequestHash:  deployment.CreateRequestHash,
		ExecutionAccountID: deployment.ExecutionAccountID,
		SourceRepositoryID: deployment.SourceRepositoryID, SourceRepositoryFullName: deployment.SourceRepositoryFullName, SourceRef: deployment.SourceRef, PullRequestTitle: deployment.PullRequestTitle,
		RevisionNumber:             deployment.RevisionNumber,
		ID:                         deployment.ID,
		AccountID:                  deployment.AccountID,
		ScopeType:                  deployment.ScopeType,
		ScopeID:                    deployment.ScopeID,
		GitHubInstallationID:       deployment.GitHubInstallationID,
		RepositoryID:               deployment.RepositoryID,
		RepositoryFullName:         deployment.RepositoryFullName,
		Ref:                        deployment.Ref,
		CommitSHA:                  deployment.CommitSHA,
		ConfigPath:                 deployment.ConfigPath,
		SnapshotVersion:            deployment.SnapshotVersion,
		SpecHash:                   deployment.SpecHash,
		SpecJSONEncrypted:          deployment.SpecJSONEncrypted,
		Status:                     deployment.Status,
		SandboxID:                  deployment.SandboxID,
		Attempt:                    deployment.Attempt,
		RuntimeID:                  deployment.RuntimeID,
		ProcessID:                  deployment.ProcessID,
		ProcessTag:                 deployment.ProcessTag,
		SandboxAPIURL:              deployment.SandboxAPIURL,
		SandboxAPIKeyEncrypted:     deployment.SandboxAPIKeyEncrypted,
		SandboxConfigSource:        deployment.SandboxConfigSource,
		SandboxRegion:              deployment.SandboxRegion,
		DeploymentTemplateID:       deployment.DeploymentTemplateID,
		DeploymentTemplateName:     deployment.DeploymentTemplateName,
		DeploymentTemplateCPU:      deployment.DeploymentTemplateCPU,
		DeploymentTemplateMemoryMB: deployment.DeploymentTemplateMemoryMB,
		DeploymentTemplateDiskMB:   deployment.DeploymentTemplateDiskMB,
		Endpoint:                   deployment.Endpoint,
		FailureStage:               deployment.FailureStage,
		FailureReason:              deployment.FailureReason,
		Error:                      deployment.Error,
		ExpiresAt:                  deployment.ExpiresAt,
		CreatedAt:                  deployment.CreatedAt,
		UpdatedAt:                  deployment.UpdatedAt,
		Version:                    deployment.Version,
	}
	applyDeploymentTimestamps(&record, now)
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		revision := revisionRecordFromDeployment(record, record.CreatedAt)
		return tx.Create(&revision).Error
	}); err != nil {
		return Deployment{}, err
	}
	return deploymentFromRecord(record), nil
}

// checkDeploymentQuota uses current locking reads, including under MySQL's
// repeatable-read isolation after an earlier idempotency lookup.
func checkDeploymentQuota(db *gorm.DB, deployment Deployment, maxConcurrent int, excludeID string) error {
	if maxConcurrent < 1 {
		maxConcurrent = 3
	}
	if err := lockDeploymentScope(db, deployment); err != nil {
		return err
	}
	query := db.Model(&deploymentRecord{}).Select("id").Clauses(clause.Locking{Strength: "UPDATE"}).Where("scope_type = ? AND scope_id = ? AND (status IN ? OR (status = ? AND sandbox_id <> ''))", deployment.ScopeType, deployment.ScopeID, []string{DeploymentStatusQueued, DeploymentStatusPlanning, DeploymentStatusCreating, DeploymentStatusRunning, DeploymentStatusStopping}, DeploymentStatusFailed)
	if excludeID != "" {
		query = query.Where("id <> ?", excludeID)
	}
	var active []deploymentRecord
	if err := query.Limit(maxConcurrent).Find(&active).Error; err != nil {
		return err
	}
	if len(active) >= maxConcurrent {
		return ErrDeploymentQuotaExceeded
	}
	return nil
}

func lockDeploymentScope(db *gorm.DB, deployment Deployment) error {
	var err error
	switch deployment.ScopeType {
	case AccountScopeTypeAccount:
		var account accountRecord
		err = db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&account, "id = ?", deployment.ScopeID).Error
	case AccountScopeTypeGitHubInstall:
		var installation githubInstallationRecord
		err = db.Clauses(clause.Locking{Strength: "UPDATE"}).First(&installation, "installation_id = ?", deployment.ScopeID).Error
	default:
		return fmt.Errorf("invalid deployment scope")
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func (s *DBStore) GetDeployment(id string) (Deployment, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return Deployment{}, err
	}
	var record deploymentRecord
	if err := db.First(&record, "id = ?", strings.TrimSpace(id)).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return Deployment{}, ErrNotFound
		}
		return Deployment{}, err
	}
	return deploymentFromRecord(record), nil
}

// FillDeploymentPullRequestTitle fills legacy display metadata for one exact
// source without replacing saved titles or changing lifecycle timestamps.
func (s *DBStore) FillDeploymentPullRequestTitle(id, ref, title string) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	id, ref, title = strings.TrimSpace(id), strings.TrimSpace(ref), strings.TrimSpace(title)
	if id == "" || !deploymentPullRefPattern.MatchString(ref) || title == "" || len(title) > 1024 || strings.IndexFunc(title, unicode.IsControl) >= 0 {
		return fmt.Errorf("deployment pull request title is invalid")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&deploymentRecord{}).Where("id = ? AND ref = ? AND (pull_request_title IS NULL OR pull_request_title = '')", id, ref).UpdateColumn("pull_request_title", title).Error; err != nil {
			return err
		}
		return tx.Model(&deploymentRevisionRecord{}).Where("deployment_id = ? AND ref = ? AND (pull_request_title IS NULL OR pull_request_title = '')", id, ref).UpdateColumn("pull_request_title", title).Error
	})
}

func (s *DBStore) ListDeploymentsForAccount(accountID int64, options DeploymentListOptions) ([]Deployment, int64, error) {
	return s.listDeploymentsForAccount(accountID, nil, false, options)
}

func (s *DBStore) ListAuthorizedDeployments(access []GitHubInstallationRepositoryAccess, options DeploymentListOptions) ([]Deployment, int64, error) {
	return s.listDeploymentsForAccount(0, access, true, options)
}

// ListDeploymentRepositories counts all retained authorized history, then reads
// only credential-free active summaries for this repository page. Historical
// list limits never hide an active deployment or a repository with no activity.
func (s *DBStore) ListDeploymentRepositories(access []GitHubInstallationRepositoryAccess, repository string, limit, offset int) ([]DeploymentRepository, bool, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, false, err
	}
	limit, offset, err = normalizeDeploymentPagination(DeploymentListOptions{Limit: limit, Offset: offset})
	if err != nil {
		return nil, false, err
	}
	active := []string{DeploymentStatusQueued, DeploymentStatusPlanning, DeploymentStatusCreating, DeploymentStatusRunning, DeploymentStatusStopping}
	query := db.Model(&deploymentRecord{}).Where(authorizedDeploymentRepositories(db, access))
	if repository != "" {
		query = query.Where("LOWER(repository_full_name) = ?", strings.ToLower(repository))
	}
	var repositories []DeploymentRepository
	err = query.Select("MIN(repository_full_name) AS repository_full_name, COUNT(*) AS total, SUM(CASE WHEN status IN ? THEN 1 ELSE 0 END) AS active_count", active).
		Group("LOWER(repository_full_name)").Order("LOWER(repository_full_name) ASC").Limit(limit + 1).Offset(offset).Scan(&repositories).Error
	if err != nil {
		return nil, false, err
	}
	more := len(repositories) > limit
	if more {
		repositories = repositories[:limit]
	}
	if len(repositories) == 0 {
		return []DeploymentRepository{}, false, nil
	}
	names := make([]string, 0, len(repositories))
	indices := make(map[string]int, len(repositories))
	for i := range repositories {
		name := strings.ToLower(repositories[i].RepositoryFullName)
		names = append(names, name)
		indices[name] = i
		repositories[i].ActiveDeployments = []DeploymentNavigationItem{}
	}
	var deployments []DeploymentNavigationItem
	err = db.Model(&deploymentRecord{}).Select("id, repository_full_name, ref, pull_request_title, commit_sha, status, updated_at").
		Where(authorizedDeploymentRepositories(db, access)).Where("LOWER(repository_full_name) IN ? AND status IN ?", names, active).
		Order("created_at DESC, id DESC").Scan(&deployments).Error
	if err != nil {
		return nil, false, err
	}
	for _, deployment := range deployments {
		i := indices[strings.ToLower(deployment.RepositoryFullName)]
		repositories[i].ActiveDeployments = append(repositories[i].ActiveDeployments, deployment)
	}
	return repositories, more, nil
}

func authorizedDeploymentRepositories(db *gorm.DB, access []GitHubInstallationRepositoryAccess) *gorm.DB {
	allowed := db.Where("1 = 0")
	for _, item := range normalizeGitHubInstallationRepositoryAccess(access) {
		allowed = allowed.Or("github_installation_id = ? AND LOWER(repository_full_name) IN ?", item.InstallationID, item.Repositories)
	}
	return allowed
}

// DeploymentListStatusValues validates the public list filter without accepting SQL expressions.
func DeploymentListStatusValues(status string) ([]string, error) {
	if status == "" || status == "all" {
		return nil, nil
	}
	seen := make(map[string]bool)
	var statuses []string
	for _, group := range strings.Split(status, ",") {
		if seen[group] {
			return nil, fmt.Errorf("invalid deployment status filter")
		}
		seen[group] = true
		switch group {
		case "running":
			statuses = append(statuses, DeploymentStatusRunning)
		case "processing":
			statuses = append(statuses, DeploymentStatusQueued, DeploymentStatusPlanning, DeploymentStatusCreating, DeploymentStatusStopping)
		case "stopped":
			statuses = append(statuses, DeploymentStatusCompleted, DeploymentStatusExpired)
		case "failed":
			statuses = append(statuses, DeploymentStatusFailed)
		default:
			return nil, fmt.Errorf("invalid deployment status filter")
		}
	}
	return statuses, nil
}

func deploymentRevisionListQuery(db *gorm.DB, options DeploymentListOptions) *gorm.DB {
	query := db.Model(&deploymentRevisionRecord{})
	if options.Ref != "" {
		query = query.Where("ref = ?", options.Ref)
	}
	if options.CommitSHA != "" {
		query = query.Where("commit_sha = ?", options.CommitSHA)
	}
	return query
}

func deploymentListQuery(db *gorm.DB, accountID int64, access []GitHubInstallationRepositoryAccess, authorized bool, options DeploymentListOptions) *gorm.DB {
	query := db.Model(&deploymentRecord{})
	if !authorized {
		query = query.Where("account_id = ?", accountID)
	}
	if options.RepositoryFullName != "" {
		query = query.Where("LOWER(repository_full_name) = ?", strings.ToLower(options.RepositoryFullName))
	}
	if options.Ref != "" || options.CommitSHA != "" {
		query = query.Where("id IN (?)", deploymentRevisionListQuery(db, options).Select("deployment_id"))
	}
	if authorized {
		query = query.Where(authorizedDeploymentRepositories(db, access))
	}
	return query
}

func (s *DBStore) CountAuthorizedDeploymentsByStatus(access []GitHubInstallationRepositoryAccess, options DeploymentListOptions) (DeploymentStatusCounts, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return DeploymentStatusCounts{}, err
	}
	var groups []struct {
		Status string
		Total  int64
	}
	err = deploymentListQuery(db, 0, access, true, options).Select("status, COUNT(*) AS total").Group("status").Scan(&groups).Error
	var counts DeploymentStatusCounts
	for _, group := range groups {
		counts.All += group.Total
		switch group.Status {
		case DeploymentStatusRunning:
			counts.Running += group.Total
		case DeploymentStatusQueued, DeploymentStatusPlanning, DeploymentStatusCreating, DeploymentStatusStopping:
			counts.Processing += group.Total
		case DeploymentStatusCompleted, DeploymentStatusExpired:
			counts.Stopped += group.Total
		case DeploymentStatusFailed:
			counts.Failed += group.Total
		}
	}
	return counts, err
}

func (s *DBStore) listDeploymentsForAccount(accountID int64, access []GitHubInstallationRepositoryAccess, authorized bool, options DeploymentListOptions) ([]Deployment, int64, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, 0, err
	}
	if !authorized && accountID <= 0 {
		return nil, 0, fmt.Errorf("account id must be positive")
	}
	limit, offset, err := normalizeDeploymentPagination(options)
	if err != nil {
		return nil, 0, err
	}
	statuses, err := DeploymentListStatusValues(options.Status)
	if err != nil {
		return nil, 0, err
	}
	query := deploymentListQuery(db, accountID, access, authorized, options)
	if len(statuses) > 0 {
		query = query.Where("status IN ?", statuses)
	}
	matchingRevision := options.Ref != "" || options.CommitSHA != ""
	var total int64
	if err := query.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var records []deploymentRecord
	if err := query.Order("created_at DESC, id DESC").Limit(limit).Offset(offset).Find(&records).Error; err != nil {
		return nil, 0, err
	}
	deployments := make([]Deployment, 0, len(records))
	for _, record := range records {
		deployment := deploymentFromRecord(record)
		if matchingRevision {
			var revision deploymentRevisionRecord
			if err := deploymentRevisionListQuery(db, options).Where("deployment_id = ?", record.ID).Order("number DESC").First(&revision).Error; err != nil {
				return nil, 0, err
			}
			matched := revisionFromRecord(revision, record)
			deployment.ContextRevision = &matched
		}
		deployments = append(deployments, deployment)
	}
	return deployments, total, nil
}

func (s *DBStore) ListActiveDeployments() ([]Deployment, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var records []deploymentRecord
	if err := db.Where("status IN ? OR (status = ? AND sandbox_id <> '')", []string{
		DeploymentStatusQueued,
		DeploymentStatusPlanning,
		DeploymentStatusCreating,
		DeploymentStatusRunning,
		DeploymentStatusStopping,
	}, DeploymentStatusFailed).Order("created_at ASC, id ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	deployments := make([]Deployment, 0, len(records))
	for _, record := range records {
		deployments = append(deployments, deploymentFromRecord(record))
	}
	return deployments, nil
}

func (s *DBStore) UpdateDeployment(deployment Deployment) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	deployment, err = normalizeDeployment(deployment)
	if err != nil {
		return err
	}
	if deployment.Status == "" {
		return fmt.Errorf("deployment status is required")
	}
	if deployment.Version < 0 {
		return ErrConflict
	}
	now := time.Now().UTC()
	deployment.UpdatedAt = now
	timestampRecord := deploymentRecord{
		Status:      deployment.Status,
		PlanningAt:  zeroTimeToPointer(deployment.PlanningAt),
		CreatingAt:  zeroTimeToPointer(deployment.CreatingAt),
		RunningAt:   zeroTimeToPointer(deployment.RunningAt),
		StoppingAt:  zeroTimeToPointer(deployment.StoppingAt),
		CompletedAt: zeroTimeToPointer(deployment.CompletedAt),
		FailedAt:    zeroTimeToPointer(deployment.FailedAt),
	}
	applyDeploymentTimestamps(&timestampRecord, now)
	updates := map[string]any{
		"status":                        deployment.Status,
		"sandbox_id":                    strings.TrimSpace(deployment.SandboxID),
		"attempt":                       deployment.Attempt,
		"runtime_id":                    strings.TrimSpace(deployment.RuntimeID),
		"process_id":                    strings.TrimSpace(deployment.ProcessID),
		"process_tag":                   strings.TrimSpace(deployment.ProcessTag),
		"sandbox_api_url":               strings.TrimSpace(deployment.SandboxAPIURL),
		"sandbox_api_key_encrypted":     strings.TrimSpace(deployment.SandboxAPIKeyEncrypted),
		"sandbox_config_source":         strings.TrimSpace(deployment.SandboxConfigSource),
		"sandbox_region":                strings.TrimSpace(deployment.SandboxRegion),
		"deployment_template_id":        strings.TrimSpace(deployment.DeploymentTemplateID),
		"deployment_template_name":      strings.TrimSpace(deployment.DeploymentTemplateName),
		"deployment_template_cpu":       deployment.DeploymentTemplateCPU,
		"deployment_template_memory_mb": deployment.DeploymentTemplateMemoryMB,
		"deployment_template_disk_mb":   deployment.DeploymentTemplateDiskMB,
		"endpoint":                      strings.TrimSpace(deployment.Endpoint),
		"failure_stage":                 strings.TrimSpace(deployment.FailureStage),
		"failure_reason":                strings.TrimSpace(deployment.FailureReason),
		"error":                         deployment.Error,
		"expires_at":                    deployment.ExpiresAt,
		"planning_at":                   timestampRecord.PlanningAt,
		"creating_at":                   timestampRecord.CreatingAt,
		"running_at":                    timestampRecord.RunningAt,
		"stopping_at":                   timestampRecord.StoppingAt,
		"completed_at":                  timestampRecord.CompletedAt,
		"failed_at":                     timestampRecord.FailedAt,
		"updated_at":                    deployment.UpdatedAt,
		"version":                       deployment.Version + 1,
	}
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&deploymentRecord{}).Where("id = ? AND version = ?", deployment.ID, deployment.Version).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrConflict
		}
		var saved deploymentRecord
		if err := tx.First(&saved, "id = ?", deployment.ID).Error; err != nil {
			return err
		}
		return updateCurrentDeploymentRevision(tx, saved)
	})
}

// RetryDeployment requeues a terminal deployment while preserving its
// immutable source and plan evidence. Any persisted process/Sandbox evidence
// must already have been cleaned before this method is called.
func (s *DBStore) RetryDeployment(deployment Deployment, expiresAt time.Time) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error { return retryDeployment(tx, deployment, expiresAt) })
}

// RetryDeploymentWithScopeLimit applies the same admission limit used for a
// fresh deployment before re-queueing a terminal record.
func (s *DBStore) RetryDeploymentWithScopeLimit(deployment Deployment, expiresAt time.Time, maxConcurrent int) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := checkDeploymentQuota(tx, deployment, maxConcurrent, deployment.ID); err != nil {
			return err
		}
		return retryDeployment(tx, deployment, expiresAt)
	})
}

func retryDeployment(db *gorm.DB, deployment Deployment, expiresAt time.Time) error {
	if deployment.Status != DeploymentStatusFailed && deployment.Status != DeploymentStatusCompleted && deployment.Status != DeploymentStatusExpired {
		return ErrRetryNotAllowed
	}
	if strings.TrimSpace(deployment.SandboxID) != "" || strings.TrimSpace(deployment.RuntimeID) != "" || strings.TrimSpace(deployment.ProcessID) != "" || strings.TrimSpace(deployment.ProcessTag) != "" {
		return ErrConflict
	}
	if !deployment.ExpiresAt.IsZero() && expiresAt.IsZero() {
		return fmt.Errorf("deployment expiry is required")
	}
	if expiresAt.IsZero() {
		expiresAt = time.Now().UTC().Add(time.Hour)
	}
	now := time.Now().UTC()
	result := db.Model(&deploymentRecord{}).Where("id = ? AND version = ? AND status IN ?", deployment.ID, deployment.Version, []string{DeploymentStatusFailed, DeploymentStatusCompleted, DeploymentStatusExpired}).Updates(map[string]any{
		"execution_account_id": deployment.ExecutionAccountID,
		"revision_number":      gorm.Expr("revision_number + 1"),
		"source_repository_id": deployment.SourceRepositoryID, "source_repository_full_name": deployment.SourceRepositoryFullName, "source_ref": deployment.SourceRef, "pull_request_title": deployment.PullRequestTitle,
		"ref":                           deployment.Ref,
		"commit_sha":                    deployment.CommitSHA,
		"config_path":                   deployment.ConfigPath,
		"snapshot_version":              deployment.SnapshotVersion,
		"spec_hash":                     deployment.SpecHash,
		"spec_json_encrypted":           deployment.SpecJSONEncrypted,
		"deployment_template_id":        deployment.DeploymentTemplateID,
		"deployment_template_name":      deployment.DeploymentTemplateName,
		"deployment_template_cpu":       deployment.DeploymentTemplateCPU,
		"deployment_template_memory_mb": deployment.DeploymentTemplateMemoryMB,
		"deployment_template_disk_mb":   deployment.DeploymentTemplateDiskMB,
		"sandbox_api_url":               deployment.SandboxAPIURL,
		"sandbox_api_key_encrypted":     deployment.SandboxAPIKeyEncrypted,
		"sandbox_config_source":         deployment.SandboxConfigSource,
		"sandbox_region":                deployment.SandboxRegion,
		"status":                        DeploymentStatusQueued,
		"sandbox_id":                    "",
		"runtime_id":                    "",
		"process_id":                    "",
		"process_tag":                   "",
		"endpoint":                      "",
		"failure_stage":                 "",
		"failure_reason":                "",
		"error":                         "",
		"expires_at":                    expiresAt,
		"planning_at":                   nil,
		"creating_at":                   nil,
		"running_at":                    nil,
		"stopping_at":                   nil,
		"completed_at":                  nil,
		"failed_at":                     nil,
		"updated_at":                    now,
		"version":                       deployment.Version + 1,
	})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrConflict
	}
	var saved deploymentRecord
	if err := db.First(&saved, "id = ?", deployment.ID).Error; err != nil {
		return err
	}
	revision := revisionRecordFromDeployment(saved, now)
	revision.SandboxID, revision.Endpoint = "", ""
	return db.Create(&revision).Error
}

// PruneDeployments removes only terminal records whose Sandbox evidence has
// already been cleared and whose retention window has elapsed. Audit events
// are intentionally retained as the long-lived control-plane history.
func (s *DBStore) PruneDeployments(before time.Time, limit int) (int, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return 0, err
	}
	if before.IsZero() {
		return 0, fmt.Errorf("deployment retention cutoff is required")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 1000 {
		limit = 1000
	}
	var records []deploymentRecord
	if err := db.Where("status IN ? AND sandbox_id = '' AND updated_at < ?", []string{DeploymentStatusCompleted, DeploymentStatusExpired, DeploymentStatusFailed}, before.UTC()).Order("updated_at ASC, id ASC").Limit(limit).Find(&records).Error; err != nil {
		return 0, err
	}
	if len(records) == 0 {
		return 0, nil
	}
	pruned := 0
	err = db.Transaction(func(tx *gorm.DB) error {
		for _, record := range records {
			result := tx.Where("id = ? AND version = ? AND status IN ? AND sandbox_id = '' AND updated_at < ?", record.ID, record.Version, []string{DeploymentStatusCompleted, DeploymentStatusExpired, DeploymentStatusFailed}, before.UTC()).Delete(&deploymentRecord{})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				continue
			}
			if err := tx.Where("deployment_id = ?", record.ID).Delete(&deploymentEventRecord{}).Error; err != nil {
				return err
			}
			if err := tx.Where("deployment_id = ?", record.ID).Delete(&deploymentRevisionRecord{}).Error; err != nil {
				return err
			}
			pruned++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return pruned, nil
}

func (s *DBStore) AppendDeploymentEvent(event DeploymentEvent) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	event.DeploymentID = strings.TrimSpace(event.DeploymentID)
	event.EventType = strings.TrimSpace(event.EventType)
	event.Stage = strings.TrimSpace(event.Stage)
	event.Stream = strings.TrimSpace(event.Stream)
	if event.DeploymentID == "" || event.EventType == "" || len(event.DeploymentID) > 128 || len(event.EventType) > 64 || len(event.Stage) > 64 || len(event.Stream) > 32 {
		return fmt.Errorf("deployment event identity is invalid")
	}
	if strings.IndexFunc(event.DeploymentID, unicode.IsControl) >= 0 || strings.IndexFunc(event.EventType, unicode.IsControl) >= 0 || strings.IndexFunc(event.Stage, unicode.IsControl) >= 0 || strings.IndexFunc(event.Stream, unicode.IsControl) >= 0 {
		return fmt.Errorf("deployment event identity is invalid")
	}
	if len(event.Message) > maxDeploymentEventMessage {
		event.Message = event.Message[:maxDeploymentEventMessage]
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	record := deploymentEventRecord{
		DeploymentID: event.DeploymentID,
		Attempt:      event.Attempt,
		EventType:    event.EventType,
		Stage:        event.Stage,
		Stream:       event.Stream,
		Message:      event.Message,
		CreatedAt:    event.CreatedAt,
	}
	return db.Transaction(func(tx *gorm.DB) error {
		// Serialize retention for one deployment so concurrent output callbacks
		// cannot both observe the same budget and grow the timeline past its cap.
		var owner deploymentRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ?", event.DeploymentID).First(&owner).Error; err != nil {
			return err
		}
		if event.RevisionNumber <= 0 {
			event.RevisionNumber = owner.RevisionNumber
		}
		if event.RevisionNumber != owner.RevisionNumber {
			return ErrConflict
		}
		record.RevisionNumber = event.RevisionNumber
		for {
			var count int64
			if err := tx.Model(&deploymentEventRecord{}).Where("deployment_id = ? AND revision_number = ?", event.DeploymentID, event.RevisionNumber).Count(&count).Error; err != nil {
				return err
			}
			var bytes int64
			if err := tx.Model(&deploymentEventRecord{}).Where("deployment_id = ? AND revision_number = ?", event.DeploymentID, event.RevisionNumber).Select("COALESCE(SUM(LENGTH(message)), 0)").Scan(&bytes).Error; err != nil {
				return err
			}
			if count < maxDeploymentEvents && bytes+int64(len(event.Message)) <= maxDeploymentEventBytes {
				break
			}
			var oldest deploymentEventRecord
			if err := tx.Where("deployment_id = ? AND revision_number = ?", event.DeploymentID, event.RevisionNumber).Order("id ASC").First(&oldest).Error; err != nil {
				return err
			}
			if err := tx.Delete(&oldest).Error; err != nil {
				return err
			}
		}
		return tx.Create(&record).Error
	})
}

func (s *DBStore) ListDeploymentEvents(id string, beforeID int64, limit int) ([]DeploymentEvent, bool, error) {
	return s.listDeploymentEvents(id, 0, beforeID, 0, limit)
}

func (s *DBStore) ListDeploymentEventsAfter(id string, afterID int64, limit int) ([]DeploymentEvent, bool, error) {
	return s.listDeploymentEvents(id, 0, 0, afterID, limit)
}

func (s *DBStore) listDeploymentEvents(id string, revisionNumber, beforeID, afterID int64, limit int) ([]DeploymentEvent, bool, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, false, err
	}
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 128 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return nil, false, fmt.Errorf("deployment id is invalid")
	}
	limit = min(max(limit, 1), 500)
	query := db.Where("deployment_id = ?", id)
	if revisionNumber > 0 {
		query = query.Where("revision_number = ?", revisionNumber)
	}
	if beforeID > 0 {
		query = query.Where("id < ?", beforeID)
	}
	order := "id DESC"
	if afterID > 0 {
		query = query.Where("id > ?", afterID)
		order = "id ASC"
	}
	var records []deploymentEventRecord
	if err := query.Order(order).Limit(limit + 1).Find(&records).Error; err != nil {
		return nil, false, err
	}
	hasMore := len(records) > limit
	if hasMore {
		records = records[:limit]
	}
	events := make([]DeploymentEvent, len(records))
	for i := range records {
		index := i
		if afterID == 0 {
			index = len(records) - 1 - i
		}
		record := records[index]
		events[i] = DeploymentEvent(record)
	}
	return events, hasMore, nil
}

func normalizeDeployment(deployment Deployment) (Deployment, error) {
	deployment.ID = strings.TrimSpace(deployment.ID)
	if deployment.ID == "" || len(deployment.ID) > 128 || strings.IndexFunc(deployment.ID, unicode.IsControl) >= 0 {
		return Deployment{}, fmt.Errorf("deployment id is invalid")
	}
	if deployment.AccountID <= 0 || deployment.ScopeID <= 0 || deployment.GitHubInstallationID <= 0 || deployment.RepositoryID <= 0 {
		return Deployment{}, fmt.Errorf("deployment ownership is invalid")
	}
	deployment.ScopeType = strings.TrimSpace(deployment.ScopeType)
	if deployment.ScopeType != AccountScopeTypeAccount && deployment.ScopeType != AccountScopeTypeGitHubInstall {
		return Deployment{}, fmt.Errorf("deployment scope is invalid")
	}
	deployment.RepositoryFullName = strings.TrimSpace(deployment.RepositoryFullName)
	if !deploymentRepositoryPattern.MatchString(deployment.RepositoryFullName) || strings.IndexFunc(deployment.RepositoryFullName, unicode.IsControl) >= 0 {
		return Deployment{}, fmt.Errorf("deployment repository is invalid")
	}
	deployment.Ref = strings.TrimSpace(deployment.Ref)
	if deployment.Ref == "" || len(deployment.Ref) > 256 || strings.IndexFunc(deployment.Ref, unicode.IsControl) >= 0 {
		return Deployment{}, fmt.Errorf("deployment ref is invalid")
	}
	deployment.CommitSHA = strings.TrimSpace(deployment.CommitSHA)
	if !validDeploymentCommit(deployment.CommitSHA) {
		return Deployment{}, fmt.Errorf("deployment commit is invalid")
	}
	deployment.ConfigPath = path.Clean(strings.TrimSpace(deployment.ConfigPath))
	if deployment.ConfigPath == "." || len(deployment.ConfigPath) > 1024 || strings.IndexFunc(deployment.ConfigPath, unicode.IsControl) >= 0 || path.IsAbs(deployment.ConfigPath) || strings.HasPrefix(deployment.ConfigPath, "../") || deployment.ConfigPath == ".." || strings.Contains(deployment.ConfigPath, "\\") {
		return Deployment{}, fmt.Errorf("deployment config path is invalid")
	}
	if deployment.SnapshotVersion <= 0 || strings.TrimSpace(deployment.SpecHash) == "" || strings.TrimSpace(deployment.SpecJSONEncrypted) == "" {
		return Deployment{}, fmt.Errorf("deployment plan evidence is incomplete")
	}
	if !validDeploymentStatus(deployment.Status) && deployment.Status != "" {
		return Deployment{}, fmt.Errorf("deployment status is invalid")
	}
	deployment.DeploymentTemplateName = strings.TrimSpace(deployment.DeploymentTemplateName)
	if len(deployment.DeploymentTemplateName) > 256 || strings.IndexFunc(deployment.DeploymentTemplateName, unicode.IsControl) >= 0 {
		return Deployment{}, fmt.Errorf("deployment template name is invalid")
	}
	if deployment.ExpiresAt.IsZero() {
		return Deployment{}, fmt.Errorf("deployment expiry is required")
	}
	return deployment, nil
}

func normalizeDeploymentPagination(options DeploymentListOptions) (int, int, error) {
	limit := options.Limit
	if limit == 0 {
		limit = 100
	}
	if limit < 0 || limit > 500 || options.Offset < 0 {
		return 0, 0, fmt.Errorf("deployment pagination is invalid")
	}
	return limit, options.Offset, nil
}

func validDeploymentStatus(status string) bool {
	switch status {
	case DeploymentStatusQueued, DeploymentStatusPlanning, DeploymentStatusCreating, DeploymentStatusRunning, DeploymentStatusStopping, DeploymentStatusCompleted, DeploymentStatusExpired, DeploymentStatusFailed:
		return true
	default:
		return false
	}
}

func validDeploymentCommit(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) > 0 && value == strings.ToLower(value)
}

func applyDeploymentTimestamps(record *deploymentRecord, now time.Time) {
	set := func(value **time.Time) {
		if *value == nil {
			*value = &now
		}
	}
	switch record.Status {
	case DeploymentStatusPlanning:
		set(&record.PlanningAt)
	case DeploymentStatusCreating:
		set(&record.PlanningAt)
		set(&record.CreatingAt)
	case DeploymentStatusRunning:
		set(&record.PlanningAt)
		set(&record.CreatingAt)
		set(&record.RunningAt)
	case DeploymentStatusStopping:
		set(&record.StoppingAt)
	case DeploymentStatusCompleted, DeploymentStatusExpired:
		if record.CompletedAt == nil {
			record.CompletedAt = &now
		}
	case DeploymentStatusFailed:
		if record.FailedAt == nil {
			record.FailedAt = &now
		}
	}
}

func deploymentFromRecord(record deploymentRecord) Deployment {
	return Deployment{
		CreateRequestHash: record.CreateRequestHash, ExecutionAccountID: record.ExecutionAccountID,
		SourceRepositoryID: record.SourceRepositoryID, SourceRepositoryFullName: record.SourceRepositoryFullName, SourceRef: record.SourceRef, PullRequestTitle: record.PullRequestTitle, RevisionNumber: record.RevisionNumber,
		ID:                         record.ID,
		AccountID:                  record.AccountID,
		ScopeType:                  record.ScopeType,
		ScopeID:                    record.ScopeID,
		GitHubInstallationID:       record.GitHubInstallationID,
		RepositoryID:               record.RepositoryID,
		RepositoryFullName:         record.RepositoryFullName,
		Ref:                        record.Ref,
		CommitSHA:                  record.CommitSHA,
		ConfigPath:                 record.ConfigPath,
		SnapshotVersion:            record.SnapshotVersion,
		SpecHash:                   record.SpecHash,
		SpecJSONEncrypted:          record.SpecJSONEncrypted,
		Status:                     record.Status,
		SandboxID:                  record.SandboxID,
		Attempt:                    record.Attempt,
		RuntimeID:                  record.RuntimeID,
		ProcessID:                  record.ProcessID,
		ProcessTag:                 record.ProcessTag,
		SandboxAPIURL:              record.SandboxAPIURL,
		SandboxAPIKeyEncrypted:     record.SandboxAPIKeyEncrypted,
		SandboxConfigSource:        record.SandboxConfigSource,
		SandboxRegion:              record.SandboxRegion,
		DeploymentTemplateID:       record.DeploymentTemplateID,
		DeploymentTemplateName:     record.DeploymentTemplateName,
		DeploymentTemplateCPU:      record.DeploymentTemplateCPU,
		DeploymentTemplateMemoryMB: record.DeploymentTemplateMemoryMB,
		DeploymentTemplateDiskMB:   record.DeploymentTemplateDiskMB,
		Endpoint:                   record.Endpoint,
		FailureStage:               record.FailureStage,
		FailureReason:              record.FailureReason,
		Error:                      record.Error,
		ExpiresAt:                  record.ExpiresAt,
		CreatedAt:                  record.CreatedAt,
		PlanningAt:                 pointerToTime(record.PlanningAt),
		CreatingAt:                 pointerToTime(record.CreatingAt),
		RunningAt:                  pointerToTime(record.RunningAt),
		StoppingAt:                 pointerToTime(record.StoppingAt),
		CompletedAt:                pointerToTime(record.CompletedAt),
		FailedAt:                   pointerToTime(record.FailedAt),
		UpdatedAt:                  record.UpdatedAt,
		Version:                    record.Version,
	}
}
