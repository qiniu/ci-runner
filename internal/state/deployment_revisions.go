package state

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

func revisionRecordFromDeployment(d deploymentRecord, created time.Time) deploymentRevisionRecord {
	if d.RevisionNumber < 1 {
		d.RevisionNumber = 1
	}
	return deploymentRevisionRecord{
		ExecutionAccountID: d.ExecutionAccountID,
		SourceRepositoryID: d.SourceRepositoryID, SourceRepositoryFullName: d.SourceRepositoryFullName, SourceRef: d.SourceRef, PullRequestTitle: d.PullRequestTitle,
		ID: fmt.Sprintf("%s-r%d", d.ID, d.RevisionNumber), DeploymentID: d.ID, Number: d.RevisionNumber,
		Ref: d.Ref, CommitSHA: d.CommitSHA, ConfigPath: d.ConfigPath, SnapshotVersion: d.SnapshotVersion,
		SpecHash: d.SpecHash, SpecJSONEncrypted: d.SpecJSONEncrypted, TemplateID: d.DeploymentTemplateID, TemplateName: d.DeploymentTemplateName,
		TemplateCPU: d.DeploymentTemplateCPU, TemplateMemoryMB: d.DeploymentTemplateMemoryMB, TemplateDiskMB: d.DeploymentTemplateDiskMB,
		Status: d.Status, SandboxID: d.SandboxID, SandboxRegion: d.SandboxRegion, Endpoint: d.Endpoint,
		FailureStage: d.FailureStage, FailureReason: d.FailureReason, ExpiresAt: d.ExpiresAt,
		RunningAt: d.RunningAt, CreatedAt: created, UpdatedAt: d.UpdatedAt,
	}
}

func updateCurrentDeploymentRevision(tx *gorm.DB, d deploymentRecord) error {
	if d.RevisionNumber < 1 {
		d.RevisionNumber = 1
	}
	updates := map[string]any{
		"status": d.Status, "sandbox_region": d.SandboxRegion,
		"template_id": d.DeploymentTemplateID, "template_name": d.DeploymentTemplateName, "template_cpu": d.DeploymentTemplateCPU, "template_memory_mb": d.DeploymentTemplateMemoryMB, "template_disk_mb": d.DeploymentTemplateDiskMB, "endpoint": d.Endpoint,
		"failure_stage": d.FailureStage, "failure_reason": d.FailureReason,
		"expires_at": d.ExpiresAt, "running_at": d.RunningAt, "updated_at": d.UpdatedAt,
	}
	if d.SandboxID != "" {
		updates["sandbox_id"] = d.SandboxID
	}
	result := tx.Model(&deploymentRevisionRecord{}).Where("deployment_id = ? AND number = ?", d.ID, d.RevisionNumber).Updates(updates)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrConflict
	}
	return nil
}

func backfillDeploymentRevisions(db *gorm.DB) error {
	if !db.Migrator().HasTable(&deploymentRecord{}) || !db.Migrator().HasTable(&deploymentRevisionRecord{}) {
		return nil
	}
	var rows []deploymentRecord
	if err := db.Where("NOT EXISTS (SELECT 1 FROM deployment_revisions WHERE deployment_revisions.deployment_id = deployments.id)").Find(&rows).Error; err != nil {
		return err
	}
	for _, row := range rows {
		if row.RevisionNumber < 1 {
			row.RevisionNumber = 1
			if err := db.Model(&deploymentRecord{}).Where("id = ?", row.ID).Update("revision_number", 1).Error; err != nil {
				return err
			}
		}
		var count int64
		if err := db.Model(&deploymentRevisionRecord{}).Where("deployment_id = ?", row.ID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			revision := revisionRecordFromDeployment(row, row.CreatedAt)
			if err := db.Create(&revision).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func revisionFromRecord(r deploymentRevisionRecord, d deploymentRecord) DeploymentRevision {
	return DeploymentRevision{
		Deployment: Deployment{
			ExecutionAccountID: r.ExecutionAccountID,
			SourceRepositoryID: r.SourceRepositoryID, SourceRepositoryFullName: r.SourceRepositoryFullName, SourceRef: r.SourceRef, PullRequestTitle: r.PullRequestTitle,
			ID: d.ID, AccountID: d.AccountID, ScopeType: d.ScopeType, ScopeID: d.ScopeID,
			GitHubInstallationID: d.GitHubInstallationID, RepositoryID: d.RepositoryID, RepositoryFullName: d.RepositoryFullName,
			Ref: r.Ref, CommitSHA: r.CommitSHA, ConfigPath: r.ConfigPath, SnapshotVersion: r.SnapshotVersion,
			SpecHash: r.SpecHash, SpecJSONEncrypted: r.SpecJSONEncrypted, Status: r.Status, RevisionNumber: r.Number,
			SandboxID: r.SandboxID, SandboxRegion: r.SandboxRegion, DeploymentTemplateID: r.TemplateID, DeploymentTemplateName: r.TemplateName, DeploymentTemplateCPU: r.TemplateCPU, DeploymentTemplateMemoryMB: r.TemplateMemoryMB, DeploymentTemplateDiskMB: r.TemplateDiskMB,
			Endpoint: r.Endpoint, FailureStage: r.FailureStage, FailureReason: r.FailureReason, ExpiresAt: r.ExpiresAt,
			RunningAt: pointerToTime(r.RunningAt), CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		},
		RevisionID: r.ID,
	}
}

func (s *DBStore) ListDeploymentRevisions(id string, beforeNumber int64, limit int) ([]DeploymentRevision, bool, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, false, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, false, errors.New("deployment id is required")
	}
	var identity deploymentRecord
	err = db.First(&identity, "id = ?", id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, ErrNotFound
		}
		return nil, false, err
	}
	limit = min(max(limit, 1), 100)
	q := db.Where("deployment_id = ?", id)
	if beforeNumber > 0 {
		q = q.Where("number < ?", beforeNumber)
	}
	var rows []deploymentRevisionRecord
	if err := q.Order("number DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, false, err
	}
	more := len(rows) > limit
	if more {
		rows = rows[:limit]
	}
	out := make([]DeploymentRevision, len(rows))
	for i, r := range rows {
		out[i] = revisionFromRecord(r, identity)
	}
	return out, more, nil
}

func (s *DBStore) GetDeploymentRevision(id string, number int64) (DeploymentRevision, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return DeploymentRevision{}, err
	}
	id = strings.TrimSpace(id)
	var identity deploymentRecord
	err = db.First(&identity, "id = ?", id).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return DeploymentRevision{}, ErrNotFound
		}
		return DeploymentRevision{}, err
	}
	var row deploymentRevisionRecord
	if err := db.Where("deployment_id = ? AND number = ?", id, number).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return DeploymentRevision{}, ErrNotFound
		}
		return DeploymentRevision{}, err
	}
	return revisionFromRecord(row, identity), nil
}

func (s *DBStore) ListDeploymentRevisionEvents(id string, number, beforeID, afterID int64, limit int) ([]DeploymentEvent, bool, error) {
	if _, err := s.GetDeploymentRevision(id, number); err != nil {
		return nil, false, err
	}
	return s.listDeploymentEvents(id, number, beforeID, afterID, limit)
}

func (s *DBStore) LastSuccessfulDeploymentRevisionNumber(id string) (int64, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return 0, err
	}
	var rows []deploymentRevisionRecord
	if err := db.Select("number").Where("deployment_id = ? AND running_at IS NOT NULL", id).Order("number DESC").Limit(1).Find(&rows).Error; err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return rows[0].Number, nil
}
