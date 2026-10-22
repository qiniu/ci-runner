package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"
)

type migrationBaselineRecord struct {
	ID                int64  `gorm:"primaryKey;autoIncrement:false"`
	MigrationID       string `gorm:"size:64;not null"`
	SourceInstanceID  string `gorm:"size:64;not null"`
	SourceEpoch       int64  `gorm:"not null"`
	SchemaFingerprint string `gorm:"size:64;not null"`
	SnapshotDigest    string `gorm:"size:64;not null"`
	Checkpoint        int64  `gorm:"not null"`
	TableIndex        int    `gorm:"not null"`
	LastKey           string `gorm:"type:text;not null" json:"-"`
	RowsCopied        int64  `gorm:"not null"`
	Complete          bool   `gorm:"not null"`
}

func (migrationBaselineRecord) TableName() string  { return "runnerd_migration_baseline" }
func (b migrationBaselineRecord) String() string   { return "migration baseline [keys redacted]" }
func (b migrationBaselineRecord) GoString() string { return b.String() }

type migrationCheckpointRecord struct {
	MigrationID      string `gorm:"size:64;primaryKey"`
	SourceInstanceID string `gorm:"size:64;primaryKey"`
	SourceEpoch      int64  `gorm:"primaryKey;autoIncrement:false"`
	Position         int64  `gorm:"not null"`
	VerifiedPosition int64  `gorm:"not null"`
	Verified         bool   `gorm:"not null"`
}

func (migrationCheckpointRecord) TableName() string { return "runnerd_migration_checkpoints" }

// This handle is private to the migration implementation. Business methods must
// never receive its native pool or the trigger-suppression transaction context.
type onlineMigrationReplica struct {
	db        *gorm.DB
	control   migrationControlRecord
	inventory onlineMigrationInventory
}

func migrationNativeDB(db *gorm.DB, pool *migrationCapturePool) *gorm.DB {
	native := db.Session(&gorm.Session{NewDB: true, Context: context.Background(), Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	native.ConnPool, native.Statement.ConnPool = pool.DB, pool.DB
	return native
}

func newOnlineMigrationReplica(ctx context.Context, store *DBStore) (*onlineMigrationReplica, error) {
	db, err := store.dbOrEnsure()
	if err != nil {
		return nil, migrationStorageError(ctx, "open replica", err)
	}
	pool, ok := db.ConnPool.(*migrationCapturePool)
	if !ok || pool.control.Role != "replica" {
		return nil, fmt.Errorf("%w: replica capture is not attached", errOnlineMigrationNotReady)
	}
	inventory, err := newOnlineMigrationInventory()
	if err != nil {
		return nil, err
	}
	fingerprint, err := inventory.fingerprint()
	if err != nil || fingerprint != pool.control.SchemaFingerprint {
		return nil, fmt.Errorf("%w: incompatible replica schema", errOnlineMigrationNotReady)
	}
	replica := &onlineMigrationReplica{db: migrationNativeDB(db, pool), control: pool.control, inventory: inventory}
	if err := replica.transaction(ctx, func(tx *gorm.DB) error {
		return tx.AutoMigrate(&migrationBaselineRecord{}, &migrationCheckpointRecord{}, &migrationRetentionRecord{})
	}); err != nil {
		return nil, err
	}
	return replica, nil
}

// Driver errors may contain the rejected row or bind parameters, including
// credentials. Keep them out of operator-facing errors and the GORM logger.
func migrationStorageError(ctx context.Context, stage string, err error) error {
	if err == nil {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, errOnlineMigrationNotReady) {
		return err
	}
	return fmt.Errorf("%w: %s failed", errOnlineMigrationNotReady, stage)
}

func lockMigrationControl(tx *gorm.DB, expected migrationControlRecord) error {
	if err := tx.Exec("UPDATE runnerd_migration_control SET active_transaction = active_transaction WHERE id = 1").Error; err != nil {
		return err
	}
	var actual migrationControlRecord
	if err := tx.First(&actual, 1).Error; err != nil {
		return err
	}
	if actual != expected || actual.ActiveTransaction != 0 {
		return fmt.Errorf("%w: database identity, role or epoch changed", errOnlineMigrationNotReady)
	}
	return nil
}

func (r *onlineMigrationReplica) transaction(ctx context.Context, work func(*gorm.DB) error) error {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockMigrationControl(tx, r.control); err != nil {
			return err
		}
		if err := tx.Model(&migrationControlRecord{}).Where("id = 1").Update("active_transaction", -1).Error; err != nil {
			return err
		}
		if err := work(tx); err != nil {
			return err
		}
		return tx.Model(&migrationControlRecord{}).Where("id = 1").Update("active_transaction", 0).Error
	})
	return migrationStorageError(ctx, "replica transaction", err)
}

func (r *onlineMigrationReplica) acceptsSource(source migrationControlRecord) bool {
	return source.ID == 1 && (source.Role == "primary" || source.Role == "frozen") && source.ActiveTransaction == 0 &&
		source.MigrationID == r.control.MigrationID && source.InstanceID != r.control.InstanceID && source.Epoch > 0 &&
		source.ProtocolVersion == r.control.ProtocolVersion && source.SchemaFingerprint == r.control.SchemaFingerprint
}

func migrationCheckpointKey(source migrationControlRecord) migrationCheckpointRecord {
	return migrationCheckpointRecord{MigrationID: source.MigrationID, SourceInstanceID: source.InstanceID, SourceEpoch: source.Epoch}
}

// copySnapshotBatch commits at most one bounded table page and its resume key
// together. Replay cannot start until the final page and checkpoint H commit.
func (r *onlineMigrationReplica) copySnapshotBatch(ctx context.Context, snapshot *onlineMigrationSnapshot, limit int) (bool, error) {
	if _, bounded := ctx.Deadline(); !bounded {
		return false, fmt.Errorf("%w: baseline copy requires a deadline", errOnlineMigrationNotReady)
	}
	if !r.acceptsSource(snapshot.Control) || snapshot.Checkpoint < 0 || len(snapshot.Digest) != 64 || limit < 1 || limit > 1000 {
		return false, fmt.Errorf("%w: incompatible baseline", errOnlineMigrationNotReady)
	}
	complete := false
	err := r.transaction(ctx, func(tx *gorm.DB) error {
		var progress migrationBaselineRecord
		err := tx.First(&progress, 1).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// A fresh baseline never merges with pre-existing business rows.
			for _, table := range r.inventory.Tables {
				var count int64
				if err := tx.Table(table.Name).Count(&count).Error; err != nil {
					return err
				}
				if count != 0 {
					return fmt.Errorf("%w: baseline target is not empty", errOnlineMigrationNotReady)
				}
			}
			var checkpoints int64
			if err := tx.Model(&migrationCheckpointRecord{}).Count(&checkpoints).Error; err != nil {
				return err
			}
			if checkpoints != 0 {
				return fmt.Errorf("%w: baseline target already has replication progress", errOnlineMigrationNotReady)
			}
			progress = migrationBaselineRecord{
				ID: 1, MigrationID: snapshot.Control.MigrationID, SourceInstanceID: snapshot.Control.InstanceID,
				SourceEpoch: snapshot.Control.Epoch, SchemaFingerprint: snapshot.Control.SchemaFingerprint,
				SnapshotDigest: snapshot.Digest, Checkpoint: snapshot.Checkpoint,
			}
		} else if err != nil {
			return err
		}
		if progress.MigrationID != snapshot.Control.MigrationID || progress.SourceInstanceID != snapshot.Control.InstanceID ||
			progress.SourceEpoch != snapshot.Control.Epoch || progress.SchemaFingerprint != snapshot.Control.SchemaFingerprint ||
			progress.SnapshotDigest != snapshot.Digest || progress.Checkpoint != snapshot.Checkpoint ||
			progress.TableIndex < 0 || progress.TableIndex > len(r.inventory.Tables) || progress.RowsCopied < 0 ||
			progress.Complete != (progress.TableIndex == len(r.inventory.Tables)) {
			return fmt.Errorf("%w: baseline resume identity or progress differs", errOnlineMigrationNotReady)
		}
		if progress.Complete {
			checkpoint, err := readMigrationCheckpoint(tx, snapshot.Control)
			if err != nil {
				return err
			}
			if checkpoint.Position < progress.Checkpoint || progress.LastKey != "" {
				return fmt.Errorf("%w: completed baseline checkpoint differs", errOnlineMigrationNotReady)
			}
			complete = true
			return nil
		}
		table := r.inventory.Tables[progress.TableIndex]
		rows, more, err := readMigrationRows(snapshot.db.WithContext(ctx), table, progress.LastKey, limit)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err := tx.Table(table.Name).Create(map[string]any(row)).Error; err != nil {
				return err
			}
		}
		progress.RowsCopied += int64(len(rows))
		if more {
			progress.LastKey, err = encodeMigrationKey(table, rows[len(rows)-1])
			if err != nil {
				return err
			}
		} else {
			progress.TableIndex++
			progress.LastKey = ""
		}
		progress.Complete = progress.TableIndex == len(r.inventory.Tables)
		if err := tx.Save(&progress).Error; err != nil {
			return err
		}
		if progress.Complete {
			checkpoint := migrationCheckpointKey(snapshot.Control)
			checkpoint.Position = snapshot.Checkpoint
			if err := tx.Create(&checkpoint).Error; err != nil {
				return err
			}
		}
		complete = progress.Complete
		return nil
	})
	return complete && err == nil, err
}

func migrationApplyChange(tx *gorm.DB, inventory onlineMigrationInventory, change migrationChangeRecord) error {
	var table *onlineMigrationTable
	for i := range inventory.Tables {
		if inventory.Tables[i].Name == change.Table {
			table = &inventory.Tables[i]
			break
		}
	}
	if table == nil {
		return fmt.Errorf("%w: change references an unknown table", errOnlineMigrationNotReady)
	}
	keyColumns := migrationKeyColumns(*table)
	key, err := decodeMigrationRow(keyColumns, change.KeyJSON)
	if err != nil {
		return err
	}
	switch change.Operation {
	case "delete":
		if change.RowJSON != "" {
			return fmt.Errorf("%w: delete contains unexpected row data", errOnlineMigrationNotReady)
		}
		query := tx.Table(table.Name)
		for _, column := range keyColumns {
			query = query.Where(migrationQuoted(column.Name)+" = ?", key[column.Name])
		}
		return query.Delete(map[string]any{}).Error
	case "upsert":
		row, err := decodeMigrationRow(table.Columns, change.RowJSON)
		if err != nil {
			return err
		}
		if !equalMigrationKeys(*table, key, row) {
			return fmt.Errorf("%w: row and key differ", errOnlineMigrationNotReady)
		}
		var keys []clause.Column
		var updates []string
		for _, column := range table.Columns {
			if column.PrimaryKey {
				keys = append(keys, clause.Column{Name: column.Name})
			} else {
				updates = append(updates, column.Name)
			}
		}
		return tx.Table(table.Name).Clauses(clause.OnConflict{Columns: keys, DoUpdates: clause.AssignmentColumns(updates)}).Create(map[string]any(row)).Error
	default:
		return fmt.Errorf("%w: unknown change operation", errOnlineMigrationNotReady)
	}
}

func migrationReadOptions() *sql.TxOptions {
	return &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
}
