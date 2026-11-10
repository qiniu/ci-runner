package state

import (
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func (s *DBStore) GetAccountSecret(scopeType string, scopeID int64, keyType string) (AccountSecret, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return AccountSecret{}, err
	}
	scopeType = normalizeAccountScopeType(scopeType)
	keyType = normalizeAccountSecretKeyType(keyType)
	if scopeType == "" || scopeID <= 0 || keyType == "" {
		return AccountSecret{}, ErrNotFound
	}
	var record accountSecretRecord
	if err := db.First(&record, "scope_type = ? AND scope_id = ? AND key_type = ?", scopeType, scopeID, keyType).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return AccountSecret{}, ErrNotFound
		}
		return AccountSecret{}, err
	}
	return recordToAccountSecret(record), nil
}

func (s *DBStore) UpsertAccountSecret(secret AccountSecret) (AccountSecret, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return AccountSecret{}, err
	}
	var saved AccountSecret
	err = db.Transaction(func(tx *gorm.DB) error {
		var writeErr error
		saved, writeErr = upsertAccountSecret(tx, secret)
		return writeErr
	})
	return saved, err
}

func upsertAccountSecret(db *gorm.DB, secret AccountSecret) (AccountSecret, error) {
	scopeType := normalizeAccountScopeType(secret.ScopeType)
	keyType := normalizeAccountSecretKeyType(secret.KeyType)
	if scopeType == "" {
		return AccountSecret{}, fmt.Errorf("scope_type is required")
	}
	if secret.ScopeID <= 0 {
		return AccountSecret{}, fmt.Errorf("scope_id is required")
	}
	if keyType == "" {
		return AccountSecret{}, fmt.Errorf("key_type is required")
	}
	encryptedValue := strings.TrimSpace(secret.EncryptedValue)
	if encryptedValue == "" {
		return AccountSecret{}, fmt.Errorf("encrypted_value is required")
	}
	now := time.Now().UTC()
	record := accountSecretRecord{
		ScopeType:      scopeType,
		ScopeID:        secret.ScopeID,
		KeyType:        keyType,
		EncryptedValue: encryptedValue,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if err := db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "scope_type"}, {Name: "scope_id"}, {Name: "key_type"}},
		DoUpdates: clause.AssignmentColumns([]string{"encrypted_value", "updated_at"}),
	}).Create(&record).Error; err != nil {
		return AccountSecret{}, err
	}
	if err := db.First(&record, "scope_type = ? AND scope_id = ? AND key_type = ?", scopeType, secret.ScopeID, keyType).Error; err != nil {
		return AccountSecret{}, err
	}
	if scopeType == AccountScopeTypeAccount && keyType == AccountSecretTypeGitHubOAuthToken {
		if err := invalidateRepositoryAccess(db, secret.ScopeID); err != nil {
			return AccountSecret{}, err
		}
	}
	return recordToAccountSecret(record), nil
}

func (s *DBStore) DeleteAccountSecret(scopeType string, scopeID int64, keyType string) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error { return deleteAccountSecret(tx, scopeType, scopeID, keyType) })
}

func deleteAccountSecret(db *gorm.DB, scopeType string, scopeID int64, keyType string) error {
	scopeType = normalizeAccountScopeType(scopeType)
	keyType = normalizeAccountSecretKeyType(keyType)
	if scopeType == "" || scopeID <= 0 || keyType == "" {
		return ErrNotFound
	}
	result := db.Where("scope_type = ? AND scope_id = ? AND key_type = ?", scopeType, scopeID, keyType).Delete(&accountSecretRecord{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrNotFound
	}
	if scopeType == AccountScopeTypeAccount && keyType == AccountSecretTypeGitHubOAuthToken {
		return invalidateRepositoryAccess(db, scopeID)
	}
	return nil
}

func recordToAccountSecret(record accountSecretRecord) AccountSecret {
	return AccountSecret(record)
}

func normalizeAccountSecretKeyType(keyType string) string {
	return strings.ToLower(strings.TrimSpace(keyType))
}

func normalizeAccountScopeType(scopeType string) string {
	return strings.ToLower(strings.TrimSpace(scopeType))
}
