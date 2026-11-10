package state

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	repositoryAccessRefreshAfter   = 20 * time.Second
	repositoryAccessTTL            = 30 * time.Second
	repositoryAccessRetry          = 5 * time.Second
	RepositoryAccessRefreshTimeout = 15 * time.Second

	AccessRefreshRejected    = "reauth_required"
	AccessRefreshRateLimited = "rate_limited"
	AccessRefreshUnavailable = "unavailable"
)

// Only classified failures and Retry-After are shared; never store the provider's response body.
type RepositoryAccessResult struct {
	Access     []GitHubInstallationRepositoryAccess
	Failure    string
	RetryAfter string
}

type RepositoryAccessSnapshot struct {
	RepositoryAccessResult
	Valid      bool
	RefreshDue bool
	Refreshing bool
}

func accessClockMillis(db *gorm.DB) clause.Expr {
	switch db.Dialector.Name() {
	case BackendPostgres:
		return gorm.Expr("CAST(FLOOR(EXTRACT(EPOCH FROM clock_timestamp()) * 1000) AS BIGINT)")
	case BackendMySQL:
		return gorm.Expr("FLOOR(UNIX_TIMESTAMP(CURRENT_TIMESTAMP(3)) * 1000)")
	default:
		return gorm.Expr("CAST(strftime('%s','now') AS INTEGER)*1000 + CAST(substr(strftime('%f','now'),4) AS INTEGER)")
	}
}

func (s *DBStore) ReadRepositoryAccessSnapshot(ctx context.Context, accountID int64) (RepositoryAccessSnapshot, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return RepositoryAccessSnapshot{}, err
	}
	var row struct {
		AccessJSON                    []byte
		Failure, RetryAfter           string
		Valid, RefreshDue, Refreshing bool
	}
	now := accessClockMillis(db)
	err = db.WithContext(ctx).Model(&repositoryAccessSnapshotRecord{}).
		Select("access_json, failure, retry_after, expires_at > (?) AS valid, refresh_after <= (?) AND lease_until <= (?) AS refresh_due, lease_until > (?) AS refreshing", now, now, now, now).
		Where("account_id = ?", accountID).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return RepositoryAccessSnapshot{RefreshDue: true}, nil
	}
	if err != nil {
		return RepositoryAccessSnapshot{}, err
	}
	snapshot := RepositoryAccessSnapshot{Valid: row.Valid, RefreshDue: row.RefreshDue, Refreshing: row.Refreshing}
	snapshot.Failure, snapshot.RetryAfter = row.Failure, row.RetryAfter
	if row.Valid {
		if err := json.Unmarshal(row.AccessJSON, &snapshot.Access); err != nil {
			return RepositoryAccessSnapshot{}, err
		}
	}
	return snapshot, nil
}

// Deploy checkout requires a live check, even if ordinary reads can still
// use the snapshot. An already running refresh can serve both callers.
func (s *DBStore) RequestRepositoryAccessRefresh(ctx context.Context, accountID int64) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Model(&repositoryAccessSnapshotRecord{}).Where("account_id = ?", accountID).UpdateColumn("refresh_after", 0).Error
}

// Upsert the invalidation even on a cold account. A concurrent first refresh
// must serialize on the same primary key as the credential/installation write.
func invalidateRepositoryAccess(tx *gorm.DB, accountID int64) error {
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "account_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"access_json", "refresh_after", "expires_at", "refresh_token", "lease_until", "failure", "retry_after"}),
	}).Create(&repositoryAccessSnapshotRecord{AccountID: accountID}).Error
}

func (s *DBStore) ClaimRepositoryAccessRefresh(ctx context.Context, accountID int64) (string, error) {
	db, err := s.dbOrEnsure()
	if err != nil {
		return "", err
	}
	token := rand.Text()
	now := accessClockMillis(db)
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&repositoryAccessSnapshotRecord{AccountID: accountID}).Error; err != nil {
			return err
		}
		update := tx.Model(&repositoryAccessSnapshotRecord{}).
			Where("account_id = ? AND refresh_after <= (?) AND lease_until <= (?)", accountID, now, now).
			Updates(map[string]any{"refresh_token": token, "lease_until": gorm.Expr("(?) + ?", now, RepositoryAccessRefreshTimeout.Milliseconds()), "failure": "", "retry_after": ""})
		if update.RowsAffected == 0 {
			token = ""
		}
		return update.Error
	})
	return token, err
}

func (s *DBStore) FinishRepositoryAccessRefresh(ctx context.Context, accountID int64, claim string, result RepositoryAccessResult) error {
	if claim == "" {
		return ErrConflict
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	now := accessClockMillis(db)
	updates := map[string]any{"refresh_token": "", "lease_until": 0, "failure": result.Failure, "retry_after": result.RetryAfter}
	switch result.Failure {
	case "":
		data, err := json.Marshal(result.Access)
		if err != nil {
			return err
		}
		updates["access_json"] = data
		updates["refresh_after"] = gorm.Expr("(?) + ?", now, repositoryAccessRefreshAfter.Milliseconds())
		updates["expires_at"] = gorm.Expr("(?) + ?", now, repositoryAccessTTL.Milliseconds())
	case AccessRefreshRejected, AccessRefreshRateLimited, AccessRefreshUnavailable:
		updates["refresh_after"] = gorm.Expr("(?) + ?", now, repositoryAccessRetry.Milliseconds())
		if result.Failure == AccessRefreshRejected {
			updates["access_json"], updates["expires_at"] = nil, 0
		}
	default:
		return fmt.Errorf("unknown repository access refresh failure")
	}
	saved := db.WithContext(ctx).Model(&repositoryAccessSnapshotRecord{}).
		Where("account_id = ? AND refresh_token = ? AND lease_until > (?)", accountID, claim, now).Updates(updates)
	if saved.Error != nil {
		return saved.Error
	}
	if saved.RowsAffected != 1 {
		return ErrConflict
	}
	return nil
}
