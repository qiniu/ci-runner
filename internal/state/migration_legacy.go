package state

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

var errLegacyMigration = errors.New("unfinished legacy online migration")

// Legacy online migration metadata is read only. Never clear a replica fence.
type migrationControlRecord struct {
	ID                int64  `gorm:"primaryKey;autoIncrement:false"`
	MigrationID       string `gorm:"size:64;not null"`
	InstanceID        string `gorm:"size:64;not null"`
	ProtocolVersion   int    `gorm:"not null"`
	SchemaFingerprint string `gorm:"size:64;not null"`
	Role              string `gorm:"size:16;not null"`
	Epoch             int64  `gorm:"not null"`
	ActiveTransaction int64  `gorm:"not null"`
}

func (migrationControlRecord) TableName() string { return "runnerd_migration_control" }

func validMigrationIdentity(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// The terminal marker allows a later ordinary startup on this database only.
// Old replicas retain their replica fence forever; no historical business rows,
// payloads, migration journal or database files are deleted here.
func validateStandaloneMigrationDB(db *gorm.DB) error {
	var control migrationControlRecord
	if err := db.First(&control, 1).Error; err != nil {
		return err
	}
	if control.Role != "standalone" {
		if control.Role == "primary" || control.Role == "frozen" {
			return fmt.Errorf("%w: finalize using the online-migration binary from commit be3b7911650599e5257febcf94976caf57f9fc32 before upgrading; for pre-merge PR #151 databases, fetch it with git fetch origin pull/151/head", errLegacyMigration)
		}
		return fmt.Errorf("%w: permanently fenced legacy replica is not a standalone source, do not start or import", errMigrationPrecondition)
	}
	if control.ActiveTransaction != 0 || control.ProtocolVersion != 1 || !validMigrationIdentity(control.MigrationID) || !validMigrationIdentity(control.InstanceID) {
		return fmt.Errorf("%w: finalize using the online-migration binary from commit be3b7911650599e5257febcf94976caf57f9fc32 before upgrading; for pre-merge PR #151 databases, fetch it with git fetch origin pull/151/head", errLegacyMigration)
	}
	var count int64
	var err error
	if db.Dialector.Name() == BackendSQLite {
		err = db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'runnerd!_mig!_%' ESCAPE '!' ").Scan(&count).Error
	} else if db.Dialector.Name() == BackendPostgres {
		err = db.Raw(`SELECT COUNT(*) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND t.tgname LIKE 'runnerd!_mig!_%' ESCAPE '!'`).Scan(&count).Error
	} else {
		return fmt.Errorf("unsupported legacy migration marker")
	}
	if err != nil {
		return err
	}
	if count != 0 {
		return fmt.Errorf("%w: legacy migration capture triggers remain", errLegacyMigration)
	}
	return nil
}
