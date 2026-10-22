package state

import (
	"database/sql"
	"errors"
	"time"

	"gorm.io/gorm"
)

// A restored SQLite database keeps this precision marker after capture ends.
// Validations begun on PG may still complete after observation is finalized.
// This pool delegates ordinary transactions unchanged and creates no outbox.
type migrationTimePool struct{ *sql.DB }

func (p *migrationTimePool) GetDBConn() (*sql.DB, error) { return p.DB, nil }

func migrationStandaloneTimeDB(db *gorm.DB) (*gorm.DB, error) {
	if db.Dialector.Name() != BackendSQLite {
		return db, nil
	}
	pool, err := db.DB()
	if err != nil {
		return nil, err
	}
	wrapped := db.Session(&gorm.Session{NewDB: true})
	marker := &migrationTimePool{DB: pool}
	wrapped.ConnPool, wrapped.Statement.ConnPool = marker, marker
	return wrapped, nil
}

// A profile read from PostgreSQL carries microseconds. An unchanged SQLite
// replica row may still retain its original nanosecond tail after rollback.
// Resolve that equivalent revision to the actual SQLite value, then keep the
// normal exact SQL compare-and-swap so a concurrent newer revision still fails.
// Ordinary stores retain their existing timestamp equality behavior.
func migrationExpectedUpdatedAt(db *gorm.DB, table string, key map[string]any, expected time.Time) (time.Time, error) {
	if db.Dialector.Name() != BackendSQLite {
		return expected, nil
	}
	_, poolAttached := db.ConnPool.(*migrationCapturePool)
	_, transactionAttached := db.Statement.ConnPool.(*migrationCaptureTx)
	_, standalone := db.ConnPool.(*migrationTimePool)
	if !poolAttached && !transactionAttached && !standalone {
		return expected, nil
	}
	var current struct{ UpdatedAt time.Time }
	err := db.Session(&gorm.Session{NewDB: true}).Table(table).Select("updated_at").Where(key).Take(&current).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return expected, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	if current.UpdatedAt.UTC().Truncate(time.Microsecond).Equal(expected.UTC().Truncate(time.Microsecond)) {
		return current.UpdatedAt, nil
	}
	return expected, nil
}
