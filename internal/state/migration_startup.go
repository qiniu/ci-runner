package state

import (
	"fmt"

	"gorm.io/gorm"
)

// validatePhasedMigrationStartup runs before GORM startup migrations/backfills.
// Database triggers additionally protect an older runnerd binary from writes.
func validatePhasedMigrationStartup(db *gorm.DB) error {
	var table string
	switch db.Dialector.Name() {
	case BackendSQLite:
		table = "runnerd_transfer_source"
	case BackendPostgres:
		table = "runnerd_transfer_target"
	default:
		return nil
	}
	var exists bool
	if db.Dialector.Name() == BackendSQLite {
		if err := db.Raw("SELECT COUNT(*) > 0 FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&exists).Error; err != nil {
			return err
		}
	} else if err := db.Raw("SELECT to_regclass(?) IS NOT NULL", table).Scan(&exists).Error; err != nil {
		return err
	}
	if !exists {
		return nil
	}
	var control struct {
		Protocol int
		Phase    string
	}
	if err := db.Table(table).Where("id = 1").Take(&control).Error; err != nil {
		return err
	}
	allowed := control.Protocol == 1 && ((db.Dialector.Name() == BackendSQLite && control.Phase == "copying") || (db.Dialector.Name() == BackendPostgres && (control.Phase == "ready" || control.Phase == "complete")))
	if !allowed {
		return fmt.Errorf("%w: database is fenced by phased migration; use the matching archived migration release to inspect status; see docs/database-transfer-retirement.md", errMigrationPrecondition)
	}
	return nil
}
