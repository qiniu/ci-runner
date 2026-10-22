package state

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"

	"gorm.io/gorm"
)

// Role changes are protocol operations on native pools, never business Store
// mutations. Call only under the operation barrier or during startup recovery.
func changeMigrationControl(ctx context.Context, db *gorm.DB, from, to migrationControlRecord) error {
	if from.ID != 1 || to.ID != 1 || from.MigrationID != to.MigrationID || from.InstanceID != to.InstanceID ||
		from.ProtocolVersion != to.ProtocolVersion || from.SchemaFingerprint != to.SchemaFingerprint ||
		from.ActiveTransaction != 0 || to.ActiveTransaction != 0 || to.Epoch <= 0 ||
		(to.Role != "primary" && to.Role != "replica" && to.Role != "frozen") {
		return fmt.Errorf("%w: invalid role transition", errOnlineMigrationNotReady)
	}
	err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockMigrationControl(tx, from); err != nil {
			return err
		}
		return tx.Model(&migrationControlRecord{}).Where("id = 1").Updates(map[string]any{"role": to.Role, "epoch": to.Epoch}).Error
	})
	return migrationStorageError(ctx, "change database fence", err)
}

// Replace the pool adapter instead of mutating it: any retained old GORM handle
// still carries its old expected role/epoch and cannot write in the new epoch.
func (s *DBStore) attachMigrationControl(control migrationControlRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return fmt.Errorf("%w: database is not open", errOnlineMigrationNotReady)
	}
	if control.ID != 1 || control.ActiveTransaction != 0 || control.Epoch <= 0 ||
		(control.Role != "primary" && control.Role != "replica" && control.Role != "frozen") {
		return fmt.Errorf("%w: incompatible capture reattachment", errOnlineMigrationNotReady)
	}
	if old, ok := s.db.ConnPool.(*migrationCapturePool); ok &&
		(old.control.MigrationID != control.MigrationID || old.control.InstanceID != control.InstanceID ||
			old.control.ProtocolVersion != control.ProtocolVersion || old.control.SchemaFingerprint != control.SchemaFingerprint) {
		return fmt.Errorf("%w: incompatible capture identity", errOnlineMigrationNotReady)
	}
	sqlDB, err := s.db.DB()
	if err != nil {
		return fmt.Errorf("%w: capture requires a database pool", errOnlineMigrationNotReady)
	}
	pool := &migrationCapturePool{DB: sqlDB, backend: s.opts.Backend, control: control}
	wrapped := s.db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	wrapped.ConnPool, wrapped.Statement.ConnPool = pool, pool
	s.db = wrapped
	return nil
}

func migrationTableByName(inventory onlineMigrationInventory, name string) (onlineMigrationTable, bool) {
	for _, table := range inventory.Tables {
		if table.Name == name {
			return table, true
		}
	}
	return onlineMigrationTable{}, false
}

func readMigrationRowByKey(db *gorm.DB, table onlineMigrationTable, key migrationRow) (migrationRow, error) {
	query := db
	for _, column := range migrationKeyColumns(table) {
		query = query.Where(migrationQuoted(column.Name)+" = ?", key[column.Name])
	}
	rows, _, err := readMigrationRows(query, table, "", 1)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// verifyFinalChanges never scans unchanged business tables. A completed full
// check at V anchors the proof; frozen source changes (V,F] enumerate every
// dirty key, including old keys from primary-key updates. All consumers must be
// stopped at F. A bounded change budget cancels an overlarge final window.
func (r *onlineMigrationReplica) verifyFinalChanges(ctx context.Context, source *onlineMigrationSource, maxChanges int64) error {
	if !r.acceptsSource(source.control) || source.control.Role != "frozen" || maxChanges < 1 || maxChanges > 100000 {
		return fmt.Errorf("%w: final verification requires a frozen source and change budget", errOnlineMigrationNotReady)
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return fmt.Errorf("%w: final verification requires a deadline", errOnlineMigrationNotReady)
	}
	return r.transaction(ctx, func(target *gorm.DB) error {
		checkpoint, err := readMigrationCheckpoint(target, source.control)
		if err != nil {
			return err
		}
		if !checkpoint.Verified || checkpoint.VerifiedPosition < 0 || checkpoint.VerifiedPosition > checkpoint.Position {
			return fmt.Errorf("%w: no valid full verification anchor", errOnlineMigrationNotReady)
		}
		return source.db.WithContext(ctx).Transaction(func(reader *gorm.DB) error {
			var control migrationControlRecord
			if err := reader.First(&control, 1).Error; err != nil {
				return err
			}
			if control != source.control {
				return fmt.Errorf("%w: final source identity changed", errOnlineMigrationNotReady)
			}
			retained, err := migrationRetainedThrough(reader, control.Epoch)
			if err != nil {
				return err
			}
			if checkpoint.VerifiedPosition < retained {
				return fmt.Errorf("%w: verification anchor is behind retained journal", errOnlineMigrationNotReady)
			}
			position, err := migrationCommittedPosition(reader, control.Epoch)
			if err != nil {
				return err
			}
			if position != checkpoint.Position {
				return fmt.Errorf("%w: final replication is not caught up", errOnlineMigrationNotReady)
			}
			var lastID, count int64
			for {
				var changes []migrationChangeRecord
				if err := reader.Table("runnerd_migration_changes AS c").Select("c.*").
					Joins("JOIN runnerd_migration_transactions AS t ON t.position = c.transaction_position").
					Where("t.epoch = ? AND t.position > ? AND t.position <= ? AND c.id > ?", control.Epoch, checkpoint.VerifiedPosition, position, lastID).
					Order("c.id").Limit(256).Find(&changes).Error; err != nil {
					return err
				}
				if len(changes) == 0 {
					break
				}
				for _, change := range changes {
					count++
					if count > maxChanges {
						return fmt.Errorf("%w: final change budget exceeded", errOnlineMigrationNotReady)
					}
					table, found := migrationTableByName(r.inventory, change.Table)
					if !found {
						return fmt.Errorf("%w: unknown final verification table", errOnlineMigrationNotReady)
					}
					key, err := decodeMigrationRow(migrationKeyColumns(table), change.KeyJSON)
					if err != nil {
						return err
					}
					row, err := readMigrationRowByKey(reader, table, key)
					if err != nil {
						return err
					}
					other, err := readMigrationRowByKey(target, table, key)
					if err != nil {
						return err
					}
					if (row == nil) != (other == nil) {
						return fmt.Errorf("%w: final key presence differs in %s", errOnlineMigrationNotReady, table.Name)
					}
					if row != nil {
						encoded, err := canonicalMigrationRow(table, row)
						if err != nil {
							return err
						}
						encodedOther, err := canonicalMigrationRow(table, other)
						if err != nil {
							return err
						}
						if !bytes.Equal(encoded, encodedOther) {
							return fmt.Errorf("%w: final row differs in %s", errOnlineMigrationNotReady, table.Name)
						}
					}
					lastID = change.ID
				}
			}
			checkpoint.VerifiedPosition = position
			return target.Save(&checkpoint).Error
		}, migrationReadOptions())
	})
}

func migrationSequenceWatermark(db *gorm.DB, table onlineMigrationTable, column onlineMigrationColumn) (int64, error) {
	var maximum int64
	if err := db.Table(table.Name).Select("COALESCE(MAX(" + migrationQuoted(column.Name) + "), 0)").Scan(&maximum).Error; err != nil {
		return 0, err
	}
	var allocated sql.NullInt64
	switch db.Dialector.Name() {
	case BackendSQLite:
		if err := db.Raw("SELECT seq FROM sqlite_sequence WHERE name = ?", table.Name).Scan(&allocated).Error; err != nil {
			return 0, err
		}
	case BackendPostgres:
		var sequence sql.NullString
		if err := db.Raw("SELECT pg_get_serial_sequence(?, ?)", table.Name, column.Name).Scan(&sequence).Error; err != nil {
			return 0, err
		}
		if !sequence.Valid {
			return 0, fmt.Errorf("%w: missing sequence for %s", errOnlineMigrationNotReady, table.Name)
		}
		if err := db.Raw("SELECT pg_sequence_last_value(?::regclass)", sequence.String).Scan(&allocated).Error; err != nil {
			return 0, err
		}
	default:
		return 0, fmt.Errorf("%w: unsupported sequence dialect", errOnlineMigrationNotReady)
	}
	return max(int64(0), maximum, allocated.Int64), nil
}

// Corrections never lower either allocator. PostgreSQL setval is intentionally
// non-transactional; failure may burn IDs but cannot re-use an allocated ID.
func (r *onlineMigrationReplica) alignSequences(ctx context.Context, source *onlineMigrationSource) error {
	if !r.acceptsSource(source.control) || source.control.Role != "frozen" {
		return fmt.Errorf("%w: sequence alignment requires a frozen source", errOnlineMigrationNotReady)
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return fmt.Errorf("%w: sequence alignment requires a deadline", errOnlineMigrationNotReady)
	}
	return r.transaction(ctx, func(target *gorm.DB) error {
		return source.db.WithContext(ctx).Transaction(func(reader *gorm.DB) error {
			var control migrationControlRecord
			if err := reader.First(&control, 1).Error; err != nil {
				return err
			}
			if control != source.control {
				return fmt.Errorf("%w: sequence source identity changed", errOnlineMigrationNotReady)
			}
			for _, table := range r.inventory.Tables {
				for _, column := range table.Columns {
					if !column.AutoIncrement {
						continue
					}
					watermark, err := migrationSequenceWatermark(reader, table, column)
					if err != nil {
						return err
					}
					current, err := migrationSequenceWatermark(target, table, column)
					if err != nil {
						return err
					}
					watermark = max(watermark, current)
					if watermark == math.MaxInt64 {
						return fmt.Errorf("%w: sequence exhausted in %s", errOnlineMigrationNotReady, table.Name)
					}
					if target.Dialector.Name() == BackendPostgres {
						if watermark > 0 {
							if err := target.Exec("SELECT setval(pg_get_serial_sequence(?, ?)::regclass, ?, true)", table.Name, column.Name, watermark).Error; err != nil {
								return err
							}
						}
					} else {
						result := target.Exec("UPDATE sqlite_sequence SET seq = ? WHERE name = ?", watermark, table.Name)
						if result.Error != nil {
							return result.Error
						}
						if result.RowsAffected == 0 {
							if err := target.Exec("INSERT INTO sqlite_sequence (name, seq) VALUES (?, ?)", table.Name, watermark).Error; err != nil {
								return err
							}
						}
					}
				}
			}
			return nil
		}, migrationReadOptions())
	})
}

// The baseline is already proven identical at the handoff checkpoint. The next
// primary has no commits in its new epoch yet, so position zero is its reverse
// baseline. Never reset a checkpoint that has begun receiving that epoch.
func (r *onlineMigrationReplica) initializeHandoffCheckpoint(ctx context.Context, source migrationControlRecord) error {
	if !r.acceptsSource(source) || source.Epoch != r.control.Epoch {
		return fmt.Errorf("%w: incompatible reverse baseline", errOnlineMigrationNotReady)
	}
	return r.transaction(ctx, func(tx *gorm.DB) error {
		var checkpoint migrationCheckpointRecord
		key := migrationCheckpointKey(source)
		err := tx.Where("migration_id = ? AND source_instance_id = ? AND source_epoch = ?", key.MigrationID, key.SourceInstanceID, key.SourceEpoch).First(&checkpoint).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			key.Verified = true
			return tx.Create(&key).Error
		}
		if err == nil && (checkpoint.Position != 0 || !checkpoint.Verified || checkpoint.VerifiedPosition != 0) {
			return fmt.Errorf("%w: handoff epoch already has replication progress", errOnlineMigrationNotReady)
		}
		return err
	})
}
