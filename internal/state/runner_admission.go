package state

import (
	"context"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WithRunnerAdmission serializes the database checks and writes that change
// runner admission. Callers must use the transaction Store and keep remote
// provider calls outside the callback. A connection loss rolls back the write
// and releases the guard with the rest of the transaction.
func (s *DBStore) WithRunnerAdmission(ctx context.Context, mutation func(Store) error) error {
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if tx.Dialector.Name() == BackendPostgres {
			// A stopped client can keep a transaction and its locks alive.
			// Bound both a pending statement and an idle session on the server;
			// retain any shorter timeout the operator configured.
			if err := tx.Exec(`SELECT set_config(name, LEAST(NULLIF(setting::bigint, 0), 5000)::text, true)
				FROM pg_settings WHERE name IN ('statement_timeout', 'idle_in_transaction_session_timeout')`).Error; err != nil {
				return err
			}
		}
		// A conflicting UPDATE takes a write lock even when the ID already
		// exists. InnoDB must acquire it before the first consistent count read.
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "id"}},
			DoUpdates: clause.Assignments(map[string]any{"id": 1}),
		}).Create(&runnerAdmissionGuardRecord{ID: 1}).Error; err != nil {
			return err
		}
		return mutation(&DBStore{opts: s.opts, db: tx, migrated: true})
	})
}
