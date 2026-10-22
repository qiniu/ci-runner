package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// Independent read pools leave business connections available to writers.
// Two reader connections allow bounded replay alongside a PG consistent view;
// every read transaction still has an explicit operation deadline.
type onlineMigrationSource struct {
	db      *gorm.DB
	control migrationControlRecord
	owned   *sql.DB
}

func newOnlineMigrationSource(ctx context.Context, store *DBStore) (*onlineMigrationSource, error) {
	db, err := store.dbOrEnsure()
	if err != nil {
		return nil, migrationStorageError(ctx, "open replication source", err)
	}
	pool, ok := db.ConnPool.(*migrationCapturePool)
	if !ok || pool.control.Role != "primary" {
		return nil, fmt.Errorf("%w: source capture is not attached", errOnlineMigrationNotReady)
	}
	source := &onlineMigrationSource{db: migrationNativeDB(db, pool), control: pool.control}
	if store.opts.Backend == BackendSQLite || store.opts.Backend == BackendPostgres {
		driver := "sqlite"
		if store.opts.Backend == BackendPostgres {
			driver = "pgx"
		}
		source.owned, err = sql.Open(driver, store.opts.DatabaseDSN)
		if err != nil {
			return nil, migrationStorageError(ctx, "open independent source reader", err)
		}
		dialector := gorm.Dialector(sqlite.Dialector{Conn: source.owned})
		if store.opts.Backend == BackendPostgres {
			dialector = postgres.New(postgres.Config{Conn: source.owned})
		}
		source.db, err = gorm.Open(dialector, &gorm.Config{
			DisableAutomaticPing: true,
			Logger:               gormlogger.Default.LogMode(gormlogger.Silent), DisableForeignKeyConstraintWhenMigrating: true,
		})
		if err == nil {
			source.owned, err = source.db.DB()
		}
		if err == nil {
			source.owned.SetMaxOpenConns(2)
			source.owned.SetMaxIdleConns(2)
			if store.opts.Backend == BackendSQLite {
				err = source.db.WithContext(ctx).Exec("PRAGMA busy_timeout = 1000").Error
			}
		}
		if err != nil {
			_ = source.Close()
			return nil, migrationStorageError(ctx, "open independent source reader", err)
		}
	}
	return source, nil
}

func (s *onlineMigrationSource) Close() error {
	if s.owned != nil {
		return s.owned.Close()
	}
	return nil
}

func readMigrationCheckpoint(tx *gorm.DB, source migrationControlRecord) (migrationCheckpointRecord, error) {
	checkpoint := migrationCheckpointKey(source)
	err := tx.Where("migration_id = ? AND source_instance_id = ? AND source_epoch = ?",
		checkpoint.MigrationID, checkpoint.SourceInstanceID, checkpoint.SourceEpoch).First(&checkpoint).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return checkpoint, fmt.Errorf("%w: source has no completed baseline or initialized checkpoint", errOnlineMigrationNotReady)
	}
	if err == nil && checkpoint.Position < 0 {
		err = fmt.Errorf("%w: invalid replication checkpoint", errOnlineMigrationNotReady)
	}
	return checkpoint, err
}

// The committed frontier is independent of sequence allocation and retained
// headers. Comparing both catches a removed tail; predecessor links below
// distinguish removed intermediate groups from legitimate rollback gaps.
func migrationCommittedPosition(db *gorm.DB, epoch int64) (int64, error) {
	var positions struct{ Frontier, Latest int64 }
	err := db.Raw(`SELECT COALESCE((SELECT position FROM runnerd_migration_frontiers WHERE epoch = ?), 0) AS frontier,
COALESCE((SELECT MAX(position) FROM runnerd_migration_transactions WHERE epoch = ?), 0) AS latest`, epoch, epoch).Scan(&positions).Error
	if err != nil {
		return 0, err
	}
	if positions.Frontier < 0 || positions.Frontier != positions.Latest {
		return 0, fmt.Errorf("%w: committed journal frontier differs", errOnlineMigrationNotReady)
	}
	return positions.Frontier, nil
}

// applyNext commits exactly one complete source transaction and its checkpoint.
// A retry after an unknown commit result re-reads the target checkpoint under
// the replica lock, so an already committed group cannot be applied twice.
func (r *onlineMigrationReplica) applyNext(ctx context.Context, source *onlineMigrationSource) (bool, error) {
	return r.applyNextUntil(ctx, source, nil)
}

// An inclusive bound prevents replay from overshooting a consistent verification view.
func (r *onlineMigrationReplica) applyNextUntil(ctx context.Context, source *onlineMigrationSource, through *int64) (bool, error) {
	if !r.acceptsSource(source.control) {
		return false, fmt.Errorf("%w: incompatible replication source", errOnlineMigrationNotReady)
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return false, fmt.Errorf("%w: replication requires a deadline", errOnlineMigrationNotReady)
	}
	applied := false
	err := r.transaction(ctx, func(target *gorm.DB) error {
		checkpoint, err := readMigrationCheckpoint(target, source.control)
		if err != nil {
			return err
		}
		if through != nil {
			if checkpoint.Position > *through {
				return fmt.Errorf("%w: replica is ahead of verification view", errOnlineMigrationNotReady)
			}
			if checkpoint.Position == *through {
				return nil
			}
		}
		return source.db.WithContext(ctx).Transaction(func(reader *gorm.DB) error {
			var control migrationControlRecord
			if err := reader.First(&control, 1).Error; err != nil {
				return err
			}
			if control != source.control {
				return fmt.Errorf("%w: source identity, role or epoch changed", errOnlineMigrationNotReady)
			}
			retained, err := migrationRetainedThrough(reader, control.Epoch)
			if err != nil {
				return err
			}
			if checkpoint.Position < retained {
				return fmt.Errorf("%w: replica is behind retained journal; restore a matching baseline", errOnlineMigrationNotReady)
			}
			latest, err := migrationCommittedPosition(reader, control.Epoch)
			if err != nil {
				return err
			}
			if latest < checkpoint.Position {
				return fmt.Errorf("%w: source checkpoint moved backwards", errOnlineMigrationNotReady)
			}
			var group migrationTransactionRecord
			query := reader.Where("epoch = ? AND position > ?", control.Epoch, checkpoint.Position)
			if through != nil {
				query = query.Where("position <= ?", *through)
			}
			err = query.Order("position").First(&group).Error
			if errors.Is(err, gorm.ErrRecordNotFound) {
				if checkpoint.Position < latest && (through == nil || checkpoint.Position < *through) {
					return fmt.Errorf("%w: committed journal group missing", errOnlineMigrationNotReady)
				}
				return nil
			}
			if err != nil {
				return err
			}
			if group.ChangeCount < 0 || group.Position > latest || group.PreviousPosition != checkpoint.Position {
				return fmt.Errorf("%w: invalid source transaction header", errOnlineMigrationNotReady)
			}
			var lastID, count int64
			for {
				var changes []migrationChangeRecord
				if err := reader.Where("transaction_position = ? AND id > ?", group.Position, lastID).Order("id").Limit(256).Find(&changes).Error; err != nil {
					return err
				}
				if len(changes) == 0 {
					break
				}
				for _, change := range changes {
					count++
					if count > group.ChangeCount {
						return fmt.Errorf("%w: source transaction change count differs", errOnlineMigrationNotReady)
					}
					if err := migrationApplyChange(target, r.inventory, change); err != nil {
						return err
					}
					lastID = change.ID
				}
			}
			if count != group.ChangeCount {
				return fmt.Errorf("%w: incomplete source transaction", errOnlineMigrationNotReady)
			}
			checkpoint.Position = group.Position
			if err := target.Save(&checkpoint).Error; err != nil {
				return err
			}
			applied = true
			return nil
		}, migrationReadOptions())
	})
	return applied && err == nil, err
}

type onlineMigrationTableVerification struct {
	Table  string
	Rows   int64
	Digest string
}

type onlineMigrationVerification struct {
	SourceInstanceID string
	SourceEpoch      int64
	Checkpoint       int64
	Tables           []onlineMigrationTableVerification
}

// verifySnapshot compares immutable source rows with a repeatable-read target
// at precisely the same position. Persisting evidence rechecks that position
// under the replica lock; a concurrently advanced target invalidates this run.
// This full check runs before the cutover barrier, never while requests queue.
func (r *onlineMigrationReplica) verifySnapshot(ctx context.Context, snapshot *onlineMigrationSnapshot, limit int) (onlineMigrationVerification, error) {
	return r.verifyView(ctx, snapshot.db, snapshot.Control, snapshot.Checkpoint, limit)
}

// verifyView also accepts a live PostgreSQL repeatable-read transaction. It never
// assumes that an arbitrary database handle is an immutable SQLite artifact.
func (r *onlineMigrationReplica) verifyView(ctx context.Context, view *gorm.DB, control migrationControlRecord, position int64, limit int) (onlineMigrationVerification, error) {
	report := onlineMigrationVerification{SourceInstanceID: control.InstanceID, SourceEpoch: control.Epoch, Checkpoint: position}
	if !r.acceptsSource(control) || limit < 1 || limit > 1000 {
		return report, fmt.Errorf("%w: incompatible verification snapshot", errOnlineMigrationNotReady)
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return report, fmt.Errorf("%w: verification requires a deadline", errOnlineMigrationNotReady)
	}
	// A failed recheck must not leave an older success at the same position
	// looking current after storage damage has been detected.
	err := r.transaction(ctx, func(tx *gorm.DB) error {
		checkpoint, err := readMigrationCheckpoint(tx, control)
		if err != nil {
			return err
		}
		if checkpoint.Position != position {
			return fmt.Errorf("%w: verification checkpoints differ", errOnlineMigrationNotReady)
		}
		checkpoint.Verified = false
		return tx.Save(&checkpoint).Error
	})
	if err != nil {
		return report, err
	}
	err = r.db.WithContext(ctx).Transaction(func(target *gorm.DB) error {
		var targetControl migrationControlRecord
		if err := target.First(&targetControl, 1).Error; err != nil {
			return err
		}
		if targetControl != r.control {
			return fmt.Errorf("%w: verification target identity changed", errOnlineMigrationNotReady)
		}
		checkpoint, err := readMigrationCheckpoint(target, control)
		if err != nil {
			return err
		}
		if checkpoint.Position != position {
			return fmt.Errorf("%w: verification checkpoints differ", errOnlineMigrationNotReady)
		}
		for _, table := range r.inventory.Tables {
			result := onlineMigrationTableVerification{Table: table.Name}
			digest := sha256.New()
			after := ""
			for {
				sourceRows, more, err := readMigrationRows(view.WithContext(ctx), table, after, limit)
				if err != nil {
					return err
				}
				targetRows, targetMore, err := readMigrationRows(target, table, after, limit)
				if err != nil {
					return err
				}
				if more != targetMore || len(sourceRows) != len(targetRows) {
					return fmt.Errorf("%w: row counts differ in %s", errOnlineMigrationNotReady, table.Name)
				}
				for i, row := range sourceRows {
					encoded, err := canonicalMigrationRow(table, row)
					if err != nil {
						return err
					}
					other, err := canonicalMigrationRow(table, targetRows[i])
					if err != nil {
						return err
					}
					if !bytes.Equal(encoded, other) {
						return fmt.Errorf("%w: row values differ in %s", errOnlineMigrationNotReady, table.Name)
					}
					_, _ = digest.Write(encoded)
					_, _ = digest.Write([]byte{'\n'})
					result.Rows++
				}
				if !more {
					break
				}
				after, err = encodeMigrationKey(table, sourceRows[len(sourceRows)-1])
				if err != nil {
					return err
				}
			}
			result.Digest = hex.EncodeToString(digest.Sum(nil))
			report.Tables = append(report.Tables, result)
		}
		return nil
	}, migrationReadOptions())
	if err == nil {
		err = r.transaction(ctx, func(tx *gorm.DB) error {
			checkpoint, err := readMigrationCheckpoint(tx, control)
			if err != nil {
				return err
			}
			if checkpoint.Position != position {
				return fmt.Errorf("%w: target advanced during verification", errOnlineMigrationNotReady)
			}
			checkpoint.Verified, checkpoint.VerifiedPosition = true, position
			return tx.Save(&checkpoint).Error
		})
	}
	return report, migrationStorageError(ctx, "snapshot verification", err)
}
