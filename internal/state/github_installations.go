package state

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *DBStore) ListGitHubInstallations(accountID int64) ([]GitHubInstallation, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	if accountID <= 0 {
		return nil, ErrNotFound
	}
	var records []githubInstallationRecord
	if err := db.
		Where("account_id = ?", accountID).
		Order("account_login ASC, installation_id ASC").
		Find(&records).Error; err != nil {
		return nil, err
	}
	repositories, err := repositoriesForGitHubInstallations(db, records)
	if err != nil {
		return nil, err
	}
	installations := make([]GitHubInstallation, 0, len(records))
	for _, record := range records {
		installations = append(installations, recordToGitHubInstallation(record, repositories[record.ID]))
	}
	return installations, nil
}

func (s *DBStore) AccountScopeForPersonalGitHubInstallation(installationID int64) (int64, bool, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return 0, false, err
	}
	if installationID <= 0 {
		return 0, false, ErrNotFound
	}
	var rows []struct {
		AccountID int64 `gorm:"column:account_id"`
	}
	if err := db.Table("github_installations AS gi").
		Select("gi.account_id").
		Joins("JOIN oauth_identities AS oi ON oi.account_id = gi.account_id AND oi.oauth_provider = ? AND LOWER(oi.oauth_login) = LOWER(gi.account_login)", "github").
		Where("gi.installation_id = ? AND gi.account_login != ''", installationID).
		Limit(1).
		Scan(&rows).Error; err != nil {
		return 0, false, err
	}
	if len(rows) == 0 || rows[0].AccountID <= 0 {
		return 0, false, nil
	}
	return rows[0].AccountID, true, nil
}

func (s *DBStore) ListGitHubInstallationAccounts() ([]GitHubInstallationAccount, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	var records []githubInstallationRecord
	if err := db.
		Where("github_account_id > 0 AND account_type != ''").
		Order("updated_at DESC, id DESC").
		Find(&records).Error; err != nil {
		return nil, err
	}
	var ownerRecords []githubInstallationOwnerRecord
	if err := db.Order("updated_at DESC, installation_id DESC").Find(&ownerRecords).Error; err != nil {
		return nil, err
	}
	accounts := make([]GitHubInstallationAccount, 0, len(records)+len(ownerRecords))
	seen := map[string]bool{}
	for _, record := range ownerRecords {
		account := ownerRecordToGitHubInstallationAccount(record)
		key := fmt.Sprintf("%s:%d", account.AccountType, account.GitHubAccountID)
		if seen[key] {
			continue
		}
		seen[key] = true
		accounts = append(accounts, account)
	}
	for _, record := range records {
		account := recordToGitHubInstallationAccount(record)
		key := fmt.Sprintf("%s:%d", account.AccountType, account.GitHubAccountID)
		if seen[key] {
			continue
		}
		seen[key] = true
		accounts = append(accounts, account)
	}
	slices.SortFunc(accounts, func(a, b GitHubInstallationAccount) int {
		aLogin := strings.ToLower(a.AccountLogin)
		bLogin := strings.ToLower(b.AccountLogin)
		if aLogin < bLogin {
			return -1
		}
		if aLogin > bLogin {
			return 1
		}
		return 0
	})
	return accounts, nil
}

func (s *DBStore) GetGitHubInstallationOwner(installationID int64) (GitHubInstallationAccount, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return GitHubInstallationAccount{}, err
	}
	if installationID <= 0 {
		return GitHubInstallationAccount{}, ErrNotFound
	}
	var record githubInstallationOwnerRecord
	if err := db.First(&record, "installation_id = ?", installationID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return GitHubInstallationAccount{}, ErrNotFound
		}
		return GitHubInstallationAccount{}, err
	}
	return ownerRecordToGitHubInstallationAccount(record), nil
}

func (s *DBStore) UpsertGitHubInstallationOwner(installationID int64, owner GitHubInstallationAccount) (GitHubInstallationAccount, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return GitHubInstallationAccount{}, err
	}
	record, err := upsertGitHubInstallationOwner(db, installationID, owner)
	if err != nil {
		return GitHubInstallationAccount{}, err
	}
	return ownerRecordToGitHubInstallationAccount(record), nil
}

func upsertGitHubInstallationOwner(db *gorm.DB, installationID int64, owner GitHubInstallationAccount) (githubInstallationOwnerRecord, error) {
	accountType := normalizeGitHubAccountType(owner.AccountType)
	accountLogin := strings.TrimSpace(owner.AccountLogin)
	if installationID <= 0 {
		return githubInstallationOwnerRecord{}, fmt.Errorf("installation_id is required")
	}
	if owner.GitHubAccountID <= 0 || accountType == "" || accountLogin == "" {
		return githubInstallationOwnerRecord{}, fmt.Errorf("stable github installation owner identity is required")
	}
	now := time.Now().UTC()
	record := githubInstallationOwnerRecord{
		InstallationID:  installationID,
		GitHubAccountID: owner.GitHubAccountID,
		AccountType:     accountType,
		AccountLogin:    accountLogin,
		AccountName:     strings.TrimSpace(owner.AccountName),
		AccountAvatar:   strings.TrimSpace(owner.AccountAvatar),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "installation_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"github_account_id", "account_type", "account_login", "account_name", "account_avatar", "updated_at"}),
	}).Create(&record).Error; err != nil {
		return githubInstallationOwnerRecord{}, err
	}
	if err := db.First(&record, "installation_id = ?", installationID).Error; err != nil {
		return githubInstallationOwnerRecord{}, err
	}
	return record, nil
}

func (s *DBStore) GitHubInstallationAccountForInstallation(installationID int64) (GitHubInstallationAccount, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return GitHubInstallationAccount{}, err
	}
	var record githubInstallationRecord
	if err := db.
		Where("installation_id = ? AND github_account_id > 0 AND account_type != ''", installationID).
		Order("updated_at DESC, id DESC").
		First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return GitHubInstallationAccount{}, ErrNotFound
		}
		return GitHubInstallationAccount{}, err
	}
	return recordToGitHubInstallationAccount(record), nil
}

func (s *DBStore) GitHubInstallationAccountForLogin(accountLogin string) (GitHubInstallationAccount, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return GitHubInstallationAccount{}, err
	}
	accountLogin = strings.TrimSpace(accountLogin)
	if accountLogin == "" {
		return GitHubInstallationAccount{}, ErrNotFound
	}
	var record githubInstallationRecord
	if err := db.
		Where("LOWER(account_login) = LOWER(?) AND github_account_id > 0 AND account_type != ''", accountLogin).
		Order("updated_at DESC, id DESC").
		First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return GitHubInstallationAccount{}, ErrNotFound
		}
		return GitHubInstallationAccount{}, err
	}
	return recordToGitHubInstallationAccount(record), nil
}

func (s *DBStore) GitHubInstallationScopeForAccountLogin(accountLogin string) (int64, bool, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return 0, false, err
	}
	accountLogin = strings.TrimSpace(accountLogin)
	if accountLogin == "" {
		return 0, false, ErrNotFound
	}
	var record githubInstallationRecord
	if err := db.
		Where("LOWER(account_login) = LOWER(?)", accountLogin).
		Order("updated_at DESC, installation_id ASC").
		First(&record).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, false, nil
		}
		return 0, false, err
	}
	return record.InstallationID, true, nil
}

func (s *DBStore) UpsertGitHubInstallation(installation GitHubInstallation) (GitHubInstallation, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return GitHubInstallation{}, err
	}
	if installation.AccountID <= 0 {
		return GitHubInstallation{}, fmt.Errorf("account_id is required")
	}
	if installation.InstallationID <= 0 {
		return GitHubInstallation{}, fmt.Errorf("installation_id is required")
	}
	now := time.Now().UTC()
	record := githubInstallationRecord{
		AccountID:       installation.AccountID,
		InstallationID:  installation.InstallationID,
		GitHubAccountID: installation.GitHubAccountID,
		AccountType:     normalizeGitHubAccountType(installation.AccountType),
		AccountLogin:    strings.TrimSpace(installation.AccountLogin),
		AccountName:     strings.TrimSpace(installation.AccountName),
		AccountAvatar:   strings.TrimSpace(installation.AccountAvatar),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "account_id"}, {Name: "installation_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"github_account_id", "account_type", "account_login", "account_name", "account_avatar", "updated_at"}),
		}).Create(&record).Error; err != nil {
			return err
		}
		if record.GitHubAccountID > 0 && record.AccountType != "" && record.AccountLogin != "" {
			if _, err := upsertGitHubInstallationOwner(tx, installation.InstallationID, recordToGitHubInstallationAccount(record)); err != nil {
				return err
			}
		}
		return invalidateRepositoryAccess(tx, installation.AccountID)
	}); err != nil {
		return GitHubInstallation{}, err
	}
	if err := db.First(&record, "account_id = ? AND installation_id = ?", installation.AccountID, installation.InstallationID).Error; err != nil {
		return GitHubInstallation{}, err
	}
	return recordToGitHubInstallation(record, nil), nil
}

func (s *DBStore) DeleteGitHubInstallation(accountID, id int64) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	if accountID <= 0 || id <= 0 {
		return ErrNotFound
	}
	return db.Transaction(func(tx *gorm.DB) error {
		result := tx.Where("account_id = ? AND id = ?", accountID, id).Delete(&githubInstallationRecord{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrNotFound
		}
		return invalidateRepositoryAccess(tx, accountID)
	})
}

func repositoriesForGitHubInstallations(db *gorm.DB, records []githubInstallationRecord) (map[int64][]string, error) {
	out := make(map[int64][]string, len(records))
	if len(records) == 0 {
		return out, nil
	}
	installationIDs := make([]int64, 0, len(records))
	localIDsByInstallationID := map[int64][]int64{}
	for _, record := range records {
		installationIDs = append(installationIDs, record.InstallationID)
		localIDsByInstallationID[record.InstallationID] = append(localIDsByInstallationID[record.InstallationID], record.ID)
	}
	var rows []struct {
		GitHubInstallationID int64  `gorm:"column:github_installation_id"`
		RepositoryFullName   string `gorm:"column:repository_full_name"`
	}
	if err := db.Model(&runnerRequestRecord{}).
		Select("github_installation_id, repository_full_name").
		Where("github_installation_id IN ?", uniquePositiveInt64s(installationIDs)).
		Where("repository_full_name != ''").
		Group("github_installation_id, repository_full_name").
		Order("repository_full_name ASC").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, row := range rows {
		for _, localID := range localIDsByInstallationID[row.GitHubInstallationID] {
			out[localID] = append(out[localID], row.RepositoryFullName)
		}
	}
	return out, nil
}

func recordToGitHubInstallation(record githubInstallationRecord, repositories []string) GitHubInstallation {
	return GitHubInstallation{
		ID:              record.ID,
		AccountID:       record.AccountID,
		InstallationID:  record.InstallationID,
		GitHubAccountID: record.GitHubAccountID,
		AccountType:     normalizeGitHubAccountType(record.AccountType),
		AccountLogin:    record.AccountLogin,
		AccountName:     record.AccountName,
		AccountAvatar:   record.AccountAvatar,
		Repositories:    normalizeRepositories(repositories),
		CreatedAt:       record.CreatedAt,
		UpdatedAt:       record.UpdatedAt,
	}
}

func recordToGitHubInstallationAccount(record githubInstallationRecord) GitHubInstallationAccount {
	return GitHubInstallationAccount{
		GitHubAccountID: record.GitHubAccountID,
		AccountType:     normalizeGitHubAccountType(record.AccountType),
		AccountLogin:    record.AccountLogin,
		AccountName:     record.AccountName,
		AccountAvatar:   record.AccountAvatar,
	}
}

func ownerRecordToGitHubInstallationAccount(record githubInstallationOwnerRecord) GitHubInstallationAccount {
	return GitHubInstallationAccount{
		GitHubAccountID: record.GitHubAccountID,
		AccountType:     normalizeGitHubAccountType(record.AccountType),
		AccountLogin:    record.AccountLogin,
		AccountName:     record.AccountName,
		AccountAvatar:   record.AccountAvatar,
	}
}

func normalizeGitHubAccountType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "user":
		return "user"
	case "organization", "org":
		return "organization"
	default:
		return ""
	}
}

func normalizeRepositories(repositories []string) []string {
	out := make([]string, 0, len(repositories))
	seen := map[string]bool{}
	for _, repository := range repositories {
		repository = strings.TrimSpace(repository)
		if repository == "" || strings.Contains(repository, "*") || !strings.Contains(repository, "/") || seen[repository] {
			continue
		}
		seen[repository] = true
		out = append(out, repository)
	}
	return out
}
