package state

import (
	"context"
	"database/sql"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	runnerCapacityGuard = 1
	runnerCatalogGuard  = 2
)

func initializeRunnerAdmissionGuards(db *gorm.DB) error {
	guards := []runnerAdmissionGuardRecord{{ID: runnerCapacityGuard}, {ID: runnerCatalogGuard}}
	return db.Clauses(clause.OnConflict{DoNothing: true}).Create(&guards).Error
}

// Capacity claims and sponsor allocations serialize without blocking new queue
// entries. Catalog/policy edits exclude both groups through their commit.
func (s *DBStore) WithRunnerAdmission(ctx context.Context, mutation func(Store) error) error {
	// Queue local contenders before starting a transaction. The database row
	// remains the authority across independent stores and processes.
	if s.capacityGate != nil {
		select {
		case s.capacityGate <- struct{}{}:
			defer func() { <-s.capacityGate }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.withRunnerGuards(ctx, []int{runnerCapacityGuard}, "UPDATE", mutation)
}

func (s *DBStore) WithRunnerEnqueue(ctx context.Context, mutation func(Store) error) error {
	return s.withRunnerGuards(ctx, []int{runnerCatalogGuard}, "SHARE", mutation)
}

func (s *DBStore) WithRunnerCatalogMutation(ctx context.Context, mutation func(Store) error) error {
	return s.withRunnerGuards(ctx, []int{runnerCapacityGuard, runnerCatalogGuard}, "UPDATE", mutation)
}

func (s *DBStore) withRunnerGuards(ctx context.Context, ids []int, strength string, mutation func(Store) error) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var opts *sql.TxOptions
	if db.Dialector.Name() != BackendSQLite {
		// The guarded reads must see the previous lock holder's commit,
		// including when the deployment defaults to repeatable read.
		opts = &sql.TxOptions{Isolation: sql.LevelReadCommitted}
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == BackendPostgres {
			// A stopped client cannot cancel an idle transaction itself.
			if err := tx.Exec(`SELECT set_config(name, LEAST(NULLIF(setting::bigint, 0), 5000)::text, true)
				FROM pg_settings WHERE name IN ('statement_timeout', 'idle_in_transaction_session_timeout')`).Error; err != nil {
				return err
			}
		}
		if tx.Dialector.Name() == BackendSQLite {
			// SQLite has no shared row lock. Reserve its writer before reading.
			if err := tx.Model(&runnerAdmissionGuardRecord{}).Where("id = ?", runnerCapacityGuard).UpdateColumn("id", runnerCapacityGuard).Error; err != nil {
				return err
			}
		} else {
			for _, id := range ids {
				var row runnerAdmissionGuardRecord
				if err := tx.Clauses(clause.Locking{Strength: strength}).First(&row, id).Error; err != nil {
					return err
				}
			}
		}
		return mutation(&DBStore{opts: s.opts, db: tx, migrated: true})
	}, opts)
}
