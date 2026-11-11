package state

import (
	"context"
	"strings"
	"time"

	"gorm.io/gorm"
)

// The token identifies one operation on a revision. Heartbeats do not change
// the deployment version or timestamps used by API callers and history.
func (s *DBStore) ClaimDeploymentWork(ctx context.Context, d Deployment, token string, ttl time.Duration) (bool, error) {
	if token == "" || ttl <= 0 {
		return false, ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return false, err
	}
	result := db.WithContext(ctx).Model(&deploymentRecord{}).
		Where("id = ? AND revision_number = ? AND attempt = ? AND version = ? AND status = ?", d.ID, d.RevisionNumber, d.Attempt, d.Version, d.Status).
		Where("work_expires_at IS NULL OR work_expires_at <= ?", queueTime(db, 0)).
		UpdateColumns(map[string]any{"work_token": token, "work_expires_at": queueTime(db, ttl)})
	return result.RowsAffected == 1, result.Error
}

func liveDeploymentWork(db *gorm.DB, token string) *gorm.DB {
	return db.Where("work_token = ? AND work_expires_at > ?", token, queueTime(db, 0))
}

func (s *DBStore) RenewDeploymentWork(ctx context.Context, id, token string, ttl time.Duration) (bool, error) {
	if token == "" || ttl <= 0 {
		return false, ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return false, err
	}
	live := liveDeploymentWork(db.WithContext(ctx).Model(&deploymentRecord{}).Where("id = ?", id), token)
	res := live.Session(&gorm.Session{}).UpdateColumn("work_expires_at", queueTime(db, ttl))
	if res.Error != nil || res.RowsAffected != 0 || db.Dialector.Name() != BackendMySQL {
		return res.RowsAffected == 1, res.Error
	}
	// MySQL may report an unchanged millisecond as zero changed rows.
	var count int64
	err = live.Count(&count).Error
	return count == 1, err
}

func (s *DBStore) ReleaseDeploymentWork(ctx context.Context, id, token string) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Model(&deploymentRecord{}).Where("id = ? AND work_token = ?", id, token).
		UpdateColumns(map[string]any{"work_token": nil, "work_expires_at": nil}).Error
}

// RecordDeploymentSandbox accepts positive provider evidence after a stop or
// lease handoff. The old worker cannot change status, cleanup intent or a
// replacement's binding. Keep the current revision's history in the same commit.
func (s *DBStore) RecordDeploymentSandbox(ctx context.Context, d Deployment) error {
	id := strings.TrimSpace(d.SandboxID)
	if id == "" || d.Attempt <= 0 || d.RevisionNumber <= 0 {
		return ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&deploymentRecord{}).
			Where("id = ? AND revision_number = ? AND attempt = ?", d.ID, d.RevisionNumber, d.Attempt).
			Where("status IN ? AND (sandbox_id IS NULL OR sandbox_id = '')", []string{DeploymentStatusCreating, DeploymentStatusStopping}).
			UpdateColumns(map[string]any{"sandbox_id": id, "runtime_id": strings.TrimSpace(d.RuntimeID), "version": gorm.Expr("version + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		var saved deploymentRecord
		if err := tx.First(&saved, "id = ?", d.ID).Error; err != nil {
			return err
		}
		return updateCurrentDeploymentRevision(tx, saved)
	})
}
