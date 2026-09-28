package state

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func validateForkSponsorshipPolicy(policy ForkSponsorshipPolicy) error {
	if policy.SponsorInstallationID <= 0 {
		return fmt.Errorf("sponsor installation id must be positive")
	}
	if policy.SourceRepositoryID <= 0 {
		return fmt.Errorf("source repository id must be positive")
	}
	if strings.TrimSpace(policy.SourceRepositoryFullName) == "" {
		return fmt.Errorf("source repository full name is required")
	}
	switch policy.Mode {
	case ForkSponsorshipModeApprovalRequired, ForkSponsorshipModeWritePermission, ForkSponsorshipModeOrganizationMember:
	default:
		return fmt.Errorf("unsupported fork sponsorship mode %q", policy.Mode)
	}
	if policy.MaxConcurrency <= 0 {
		return fmt.Errorf("fork sponsorship max concurrency must be positive")
	}
	return nil
}

func (s *DBStore) ListForkSponsorshipPolicies(sponsorInstallationID int64) ([]ForkSponsorshipPolicy, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var records []forkSponsorshipPolicyRecord
	if err := db.Where("sponsor_installation_id = ?", sponsorInstallationID).
		Order("source_repository_full_name ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	policies := make([]ForkSponsorshipPolicy, 0, len(records))
	for _, record := range records {
		policies = append(policies, forkSponsorshipPolicyFromRecord(record))
	}
	return policies, nil
}

func (s *DBStore) GetForkSponsorshipPolicy(sponsorInstallationID, sourceRepositoryID int64) (ForkSponsorshipPolicy, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return ForkSponsorshipPolicy{}, err
	}
	var record forkSponsorshipPolicyRecord
	if err := db.First(&record, "sponsor_installation_id = ? AND source_repository_id = ?", sponsorInstallationID, sourceRepositoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ForkSponsorshipPolicy{}, ErrNotFound
		}
		return ForkSponsorshipPolicy{}, err
	}
	return forkSponsorshipPolicyFromRecord(record), nil
}

func (s *DBStore) GetForkSponsorshipPolicyBySourceRepositoryID(sourceRepositoryID int64) (ForkSponsorshipPolicy, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return ForkSponsorshipPolicy{}, err
	}
	var record forkSponsorshipPolicyRecord
	if err := db.First(&record, "source_repository_id = ?", sourceRepositoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ForkSponsorshipPolicy{}, ErrNotFound
		}
		return ForkSponsorshipPolicy{}, err
	}
	return forkSponsorshipPolicyFromRecord(record), nil
}

func (s *DBStore) UpsertForkSponsorshipPolicy(policy ForkSponsorshipPolicy) (ForkSponsorshipPolicy, error) {
	policy.SourceRepositoryFullName = strings.TrimSpace(policy.SourceRepositoryFullName)
	policy.Mode = strings.TrimSpace(policy.Mode)
	if err := validateForkSponsorshipPolicy(policy); err != nil {
		return ForkSponsorshipPolicy{}, err
	}
	existing, err := s.GetForkSponsorshipPolicyBySourceRepositoryID(policy.SourceRepositoryID)
	if err == nil {
		if existing.SponsorInstallationID != policy.SponsorInstallationID {
			return ForkSponsorshipPolicy{}, ErrConflict
		}
		return s.UpdateForkSponsorshipPolicy(policy)
	}
	if !errors.Is(err, ErrNotFound) {
		return ForkSponsorshipPolicy{}, err
	}
	return s.CreateForkSponsorshipPolicy(policy)
}

func (s *DBStore) CreateForkSponsorshipPolicy(policy ForkSponsorshipPolicy) (ForkSponsorshipPolicy, error) {
	policy.SourceRepositoryFullName = strings.TrimSpace(policy.SourceRepositoryFullName)
	policy.Mode = strings.TrimSpace(policy.Mode)
	if err := validateForkSponsorshipPolicy(policy); err != nil {
		return ForkSponsorshipPolicy{}, err
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return ForkSponsorshipPolicy{}, err
	}
	now := time.Now().UTC()
	record := forkSponsorshipPolicyRecord{
		SponsorInstallationID:    policy.SponsorInstallationID,
		SourceRepositoryID:       policy.SourceRepositoryID,
		SourceRepositoryFullName: policy.SourceRepositoryFullName,
		Mode:                     policy.Mode,
		Enabled:                  policy.Enabled,
		MaxConcurrency:           policy.MaxConcurrency,
		CreatedAt:                now,
		UpdatedAt:                now,
	}
	result := db.Create(&record)
	if translator, ok := db.Dialector.(gorm.ErrorTranslator); ok && result.Error != nil && errors.Is(translator.Translate(result.Error), gorm.ErrDuplicatedKey) {
		return ForkSponsorshipPolicy{}, ErrConflict
	}
	if result.Error != nil {
		return ForkSponsorshipPolicy{}, result.Error
	}
	if result.RowsAffected == 0 {
		return ForkSponsorshipPolicy{}, ErrConflict
	}
	return s.GetForkSponsorshipPolicy(policy.SponsorInstallationID, policy.SourceRepositoryID)
}

func (s *DBStore) UpdateForkSponsorshipPolicy(policy ForkSponsorshipPolicy) (ForkSponsorshipPolicy, error) {
	policy.SourceRepositoryFullName = strings.TrimSpace(policy.SourceRepositoryFullName)
	policy.Mode = strings.TrimSpace(policy.Mode)
	if err := validateForkSponsorshipPolicy(policy); err != nil {
		return ForkSponsorshipPolicy{}, err
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return ForkSponsorshipPolicy{}, err
	}
	result := db.Model(&forkSponsorshipPolicyRecord{}).
		Where("sponsor_installation_id = ? AND source_repository_id = ?", policy.SponsorInstallationID, policy.SourceRepositoryID).
		Updates(map[string]any{
			"source_repository_full_name": policy.SourceRepositoryFullName,
			"mode":                        policy.Mode,
			"enabled":                     policy.Enabled,
			"max_concurrency":             policy.MaxConcurrency,
			"updated_at":                  time.Now().UTC(),
		})
	if result.Error != nil {
		return ForkSponsorshipPolicy{}, result.Error
	}
	if result.RowsAffected == 0 {
		existing, lookupErr := s.GetForkSponsorshipPolicyBySourceRepositoryID(policy.SourceRepositoryID)
		if lookupErr == nil {
			if existing.SponsorInstallationID != policy.SponsorInstallationID {
				return ForkSponsorshipPolicy{}, ErrConflict
			}
			return existing, nil
		}
		if lookupErr != nil && !errors.Is(lookupErr, ErrNotFound) {
			return ForkSponsorshipPolicy{}, lookupErr
		}
		return ForkSponsorshipPolicy{}, ErrNotFound
	}
	return s.GetForkSponsorshipPolicy(policy.SponsorInstallationID, policy.SourceRepositoryID)
}

func lockForkSponsorshipPolicy(tx *gorm.DB, sponsorInstallationID, sourceRepositoryID int64) error {
	var record forkSponsorshipPolicyRecord
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Select("sponsor_installation_id", "source_repository_id").
		First(&record, "sponsor_installation_id = ? AND source_repository_id = ?", sponsorInstallationID, sourceRepositoryID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrNotFound
	}
	return err
}

func upsertForkSponsorshipApprovalRecord(tx *gorm.DB, record forkSponsorshipApprovalRecord) error {
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "source_repository_id"}, {Name: "fork_repository_id"}},
		DoUpdates: clause.Assignments(map[string]any{
			"sponsor_installation_id":   record.SponsorInstallationID,
			"fork_repository_full_name": record.ForkRepositoryFullName,
			"fork_owner_id":             record.ForkOwnerID,
			"fork_owner_login":          record.ForkOwnerLogin,
			"updated_at":                record.UpdatedAt,
		}),
	}).Create(&record).Error
}

func (s *DBStore) DeleteForkSponsorshipPolicy(sponsorInstallationID, sourceRepositoryID int64) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := lockForkSponsorshipPolicy(tx, sponsorInstallationID, sourceRepositoryID); err != nil {
			return err
		}
		if err := tx.Delete(&forkSponsorshipApprovalRecord{}, "sponsor_installation_id = ? AND source_repository_id = ?", sponsorInstallationID, sourceRepositoryID).Error; err != nil {
			return err
		}
		return tx.Delete(&forkSponsorshipPolicyRecord{}, "sponsor_installation_id = ? AND source_repository_id = ?", sponsorInstallationID, sourceRepositoryID).Error
	})
}

func (s *DBStore) ListForkSponsorshipApprovals(sponsorInstallationID, sourceRepositoryID int64) ([]ForkSponsorshipApproval, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var records []forkSponsorshipApprovalRecord
	if err := db.Where("sponsor_installation_id = ? AND source_repository_id = ?", sponsorInstallationID, sourceRepositoryID).
		Order("fork_repository_full_name ASC").Find(&records).Error; err != nil {
		return nil, err
	}
	approvals := make([]ForkSponsorshipApproval, 0, len(records))
	for _, record := range records {
		approvals = append(approvals, forkSponsorshipApprovalFromRecord(record))
	}
	return approvals, nil
}

func (s *DBStore) GetForkSponsorshipApproval(sourceRepositoryID, forkRepositoryID int64) (ForkSponsorshipApproval, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return ForkSponsorshipApproval{}, err
	}
	var record forkSponsorshipApprovalRecord
	if err := db.First(&record, "source_repository_id = ? AND fork_repository_id = ?", sourceRepositoryID, forkRepositoryID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ForkSponsorshipApproval{}, ErrNotFound
		}
		return ForkSponsorshipApproval{}, err
	}
	return forkSponsorshipApprovalFromRecord(record), nil
}

func (s *DBStore) UpsertForkSponsorshipApproval(approval ForkSponsorshipApproval) (ForkSponsorshipApproval, error) {
	approval.ForkRepositoryFullName = strings.TrimSpace(approval.ForkRepositoryFullName)
	approval.ForkOwnerLogin = strings.TrimSpace(approval.ForkOwnerLogin)
	if approval.SponsorInstallationID <= 0 || approval.SourceRepositoryID <= 0 || approval.ForkRepositoryID <= 0 || approval.ForkOwnerID <= 0 || approval.ForkRepositoryFullName == "" || approval.ForkOwnerLogin == "" {
		return ForkSponsorshipApproval{}, fmt.Errorf("complete positive fork sponsorship approval identity is required")
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return ForkSponsorshipApproval{}, err
	}
	now := time.Now().UTC()
	record := forkSponsorshipApprovalRecord{
		SponsorInstallationID:  approval.SponsorInstallationID,
		SourceRepositoryID:     approval.SourceRepositoryID,
		ForkRepositoryID:       approval.ForkRepositoryID,
		ForkRepositoryFullName: approval.ForkRepositoryFullName,
		ForkOwnerID:            approval.ForkOwnerID,
		ForkOwnerLogin:         approval.ForkOwnerLogin,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := lockForkSponsorshipPolicy(tx, approval.SponsorInstallationID, approval.SourceRepositoryID); err != nil {
			return err
		}
		return upsertForkSponsorshipApprovalRecord(tx, record)
	}); err != nil {
		return ForkSponsorshipApproval{}, err
	}
	return s.GetForkSponsorshipApproval(approval.SourceRepositoryID, approval.ForkRepositoryID)
}

func (s *DBStore) DeleteForkSponsorshipApproval(sponsorInstallationID, sourceRepositoryID, forkRepositoryID int64) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	result := db.Delete(&forkSponsorshipApprovalRecord{}, "sponsor_installation_id = ? AND source_repository_id = ? AND fork_repository_id = ?", sponsorInstallationID, sourceRepositoryID, forkRepositoryID)
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *DBStore) InFlightCountForForkSponsorship(sponsorInstallationID, sourceRepositoryID int64) (int, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return 0, err
	}
	var count int64
	err = db.Model(&runnerRequestRecord{}).
		Where("sponsor_installation_id = ? AND sponsor_source_repository_id = ? AND status IN ?", sponsorInstallationID, sourceRepositoryID, []string{StatusCreating, StatusRunning, StatusStopping}).
		Count(&count).Error
	return int(count), err
}

func forkSponsorshipPolicyFromRecord(record forkSponsorshipPolicyRecord) ForkSponsorshipPolicy {
	return ForkSponsorshipPolicy(record)
}

func forkSponsorshipApprovalFromRecord(record forkSponsorshipApprovalRecord) ForkSponsorshipApproval {
	return ForkSponsorshipApproval{
		SponsorInstallationID:  record.SponsorInstallationID,
		SourceRepositoryID:     record.SourceRepositoryID,
		ForkRepositoryID:       record.ForkRepositoryID,
		ForkRepositoryFullName: record.ForkRepositoryFullName,
		ForkOwnerID:            record.ForkOwnerID,
		ForkOwnerLogin:         record.ForkOwnerLogin,
		CreatedAt:              record.CreatedAt,
		UpdatedAt:              record.UpdatedAt,
	}
}
