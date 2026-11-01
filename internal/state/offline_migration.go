package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// OfflineMigrationOptions requires a stopped source and an unused target schema.
// The writer lock protects the copy, not the cutover after this command exits.
type OfflineMigrationOptions struct {
	SQLitePath  string
	BackupPath  string
	PostgresDSN string `json:"-"`
}

func (OfflineMigrationOptions) String() string     { return "offline migration options [redacted]" }
func (o OfflineMigrationOptions) GoString() string { return o.String() }

type OfflineMigrationTable struct {
	Table  string `json:"table"`
	Rows   int64  `json:"rows"`
	SHA256 string `json:"sha256"`
}
type OfflineMigrationReport struct {
	BackupPath   string                  `json:"backup_path"`
	ExtraTables  int                     `json:"retained_source_extra_tables"`
	ExtraColumns int                     `json:"retained_source_extra_columns"`
	Tables       []OfflineMigrationTable `json:"tables"`
	Verified     bool                    `json:"verified"`
}

// MigrateSQLiteToPostgres never runs source startup migrations or business
// callbacks. Only model-owned columns transfer; legacy extras stay in backup.
// All target DDL, inserts and verification commit together. A connection loss
// during commit is reported as failure: inspect the target, never auto-retry.
func MigrateSQLiteToPostgres(ctx context.Context, options OfflineMigrationOptions) (report OfflineMigrationReport, resultErr error) {
	stage := "validate options"
	defer func() {
		// Driver messages can contain credentials or row values. Report only stage.
		if resultErr != nil {
			switch {
			case errors.Is(resultErr, context.Canceled):
				resultErr = fmt.Errorf("offline migration failed at %s: %w", stage, context.Canceled)
			case errors.Is(resultErr, context.DeadlineExceeded):
				resultErr = fmt.Errorf("offline migration failed at %s: %w", stage, context.DeadlineExceeded)
			case errors.Is(resultErr, errMigrationInvalid), errors.Is(resultErr, errMigrationPrecondition), errors.Is(resultErr, errLegacyMigration):
				resultErr = fmt.Errorf("offline migration failed at %s: %w", stage, resultErr)
			default:
				resultErr = fmt.Errorf("offline migration failed at %s", stage)
			}
			if stage == "commit verified PostgreSQL data" {
				resultErr = fmt.Errorf("offline migration commit unconfirmed; inspect target before retrying")
			}
		}
	}()
	if _, ok := ctx.Deadline(); !ok || options.PostgresDSN == "" || options.BackupPath == "" {
		return report, fmt.Errorf("%w: deadline and explicit paths required", errMigrationPrecondition)
	}
	sourcePath, err := filepath.Abs(options.SQLitePath)
	if err != nil {
		return report, err
	}
	info, err := os.Stat(sourcePath)
	if err != nil || !info.Mode().IsRegular() {
		return report, fmt.Errorf("%w: existing SQLite file required", errMigrationPrecondition)
	}
	stage = "open and lock SQLite source (stop all writers first)"
	uri := (&url.URL{Scheme: "file", Path: sourcePath, RawQuery: "mode=rw"}).String()
	source, err := sql.Open("sqlite", uri)
	if err != nil {
		return report, err
	}
	defer source.Close()
	conn, err := source.Conn(ctx)
	if err != nil {
		return report, err
	}
	defer conn.Close()
	if _, err = conn.ExecContext(ctx, "PRAGMA busy_timeout = 1000"); err != nil {
		return report, err
	}
	if _, err = conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return report, err
	}
	defer conn.ExecContext(context.Background(), "ROLLBACK")
	cfg := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableForeignKeyConstraintWhenMigrating: true, DisableAutomaticPing: true}
	locked, err := gorm.Open(sqlite.Dialector{Conn: conn}, cfg)
	if err != nil {
		return report, err
	}
	stage = "validate source schema and migration fence"
	if locked.Migrator().HasTable(&migrationControlRecord{}) {
		if err = validateStandaloneMigrationDB(locked.WithContext(ctx)); err != nil {
			return report, err
		}
	}
	inventory, err := newMigrationInventory()
	if err != nil {
		return report, err
	}
	schemaReport, err := inspectMigrationSchema(ctx, locked, inventory)
	if err != nil {
		return report, err
	}
	report.ExtraTables, report.ExtraColumns = schemaReport.ExtraTables, schemaReport.ExtraColumns
	stage = "create exclusive private SQLite backup"
	backupPath, err := filepath.Abs(options.BackupPath)
	if err != nil {
		return report, err
	}
	_, err = validateMigrationBackupDirectory(filepath.Dir(backupPath))
	if err != nil {
		return report, err
	}
	// O_EXCL forbids accidentally overwriting any existing backup or source.
	backup, err := os.OpenFile(backupPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return report, err
	}
	report.BackupPath = backupPath
	defer backup.Close()
	backupInfo, err := backup.Stat()
	if err != nil {
		return report, err
	}
	if current, statErr := os.Stat(backupPath); statErr != nil || !os.SameFile(backupInfo, current) {
		return report, fmt.Errorf("%w: backup file was replaced before snapshot", errMigrationPrecondition)
	}
	// A second connection can snapshot while BEGIN IMMEDIATE blocks writers.
	// Retain even a failed artifact for inspection; a retry needs a fresh path.
	if _, err = source.ExecContext(ctx, "VACUUM INTO ?", backupPath); err != nil {
		return report, err
	}
	if err = syncMigrationSnapshot(backupPath, filepath.Dir(backupPath)); err != nil {
		return report, err
	}
	if current, statErr := os.Stat(backupPath); statErr != nil || !os.SameFile(backupInfo, current) {
		return report, fmt.Errorf("%w: backup file was replaced during snapshot", errMigrationPrecondition)
	}
	stage = "check SQLite backup integrity"
	backupURI := (&url.URL{Scheme: "file", Path: backupPath, RawQuery: "mode=ro&immutable=1"}).String()
	reader, err := gorm.Open(sqlite.Open(backupURI), cfg)
	if err != nil {
		return report, err
	}
	readerPool, err := reader.DB()
	if err != nil {
		return report, err
	}
	defer readerPool.Close()
	if current, statErr := os.Stat(backupPath); statErr != nil || !os.SameFile(backupInfo, current) {
		return report, fmt.Errorf("%w: backup file was replaced before verification", errMigrationPrecondition)
	}
	reader = reader.WithContext(ctx)
	var integrity string
	if err = reader.Raw("PRAGMA integrity_check").Scan(&integrity).Error; err != nil || integrity != "ok" {
		return report, fmt.Errorf("%w: backup integrity failed", errMigrationPrecondition)
	}
	stage = "connect PostgreSQL target"
	targetPool, err := sql.Open("pgx", options.PostgresDSN)
	if err != nil {
		return report, err
	}
	defer targetPool.Close()
	target, err := gorm.Open(postgres.New(postgres.Config{Conn: targetPool}), cfg)
	if err != nil {
		return report, err
	}
	err = target.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		stage = "check empty PostgreSQL schema (do not start target service)"
		// Serialize cooperating importers. Non-cooperating writers must be stopped.
		if err := tx.Exec("SELECT pg_advisory_xact_lock(724901832)").Error; err != nil {
			return err
		}
		// GORM resolves unqualified model tables through search_path. Pin the
		// selected schema so fallback schemas cannot supply existing tables.
		var targetSchema sql.NullString
		if err := tx.Raw("SELECT current_schema()").Scan(&targetSchema).Error; err != nil {
			return err
		}
		if !targetSchema.Valid || targetSchema.String == "" {
			return fmt.Errorf("%w: target schema unavailable", errMigrationPrecondition)
		}
		if err := tx.Exec("SELECT set_config('search_path', quote_ident(?), true)", targetSchema.String).Error; err != nil {
			return err
		}
		var count int64
		if err := tx.Raw(`SELECT count(*) FROM pg_class WHERE relnamespace = (
SELECT oid FROM pg_namespace WHERE nspname = current_schema())`).Scan(&count).Error; err != nil {
			return err
		}
		if count != 0 {
			return fmt.Errorf("%w: target schema not empty", errMigrationPrecondition)
		}
		stage = "create target schema"
		if err := tx.AutoMigrate(migrationModels()...); err != nil {
			return err
		}
		for _, table := range inventory.Tables {
			stage = "copy table " + table.Name
			after := ""
			for {
				rows, more, err := readMigrationRows(reader, table, after, 256)
				if err != nil {
					return err
				}
				if len(rows) > 0 {
					page := make([]map[string]any, len(rows))
					for i, row := range rows {
						page[i] = map[string]any(row)
					}
					if err := tx.Table(table.Name).Create(page).Error; err != nil {
						return err
					}
				}
				if !more {
					break
				}
				after, err = encodeMigrationKey(table, rows[len(rows)-1])
				if err != nil {
					return err
				}
			}
			stage = "verify table " + table.Name
			result, err := verifyOfflineTable(reader, tx, table)
			if err != nil {
				return err
			}
			report.Tables = append(report.Tables, result)
			stage = "align sequences for " + table.Name
			for _, column := range table.Columns {
				if !column.AutoIncrement {
					continue
				}
				watermark, err := migrationSequenceWatermark(reader, table, column)
				if err != nil {
					return err
				}
				if watermark == math.MaxInt64 {
					return fmt.Errorf("%w: sequence exhausted", errMigrationPrecondition)
				}
				if watermark > 0 {
					var sequence sql.NullString
					if err := tx.Raw("SELECT pg_get_serial_sequence(?, ?)", table.Name, column.Name).Scan(&sequence).Error; err != nil {
						return err
					}
					if !sequence.Valid || sequence.String == "" {
						return fmt.Errorf("%w: PostgreSQL sequence missing for %s.%s", errMigrationPrecondition, table.Name, column.Name)
					}
					if err := tx.Exec("SELECT setval(?::regclass, ?, true)", sequence.String, watermark).Error; err != nil {
						return err
					}
				}
			}
		}
		stage = "commit verified PostgreSQL data"
		return nil
	})
	if err != nil {
		return report, err
	}
	report.Verified = true
	return report, nil
}

func verifyOfflineTable(source, target *gorm.DB, table migrationTable) (OfflineMigrationTable, error) {
	result := OfflineMigrationTable{Table: table.Name}
	digest := sha256.New()
	after := ""
	for {
		rows, more, err := readMigrationRows(source, table, after, 256)
		if err != nil {
			return result, err
		}
		others, err := readMigrationRowsByKeys(target, table, rows)
		if err != nil {
			return result, err
		}
		if len(rows) != len(others) {
			return result, fmt.Errorf("%w: row count differs", errMigrationPrecondition)
		}
		for _, row := range rows {
			encoded, err := canonicalMigrationRow(table, row)
			if err != nil {
				return result, err
			}
			key, keyErr := migrationKeyString(table, row)
			if keyErr != nil {
				return result, keyErr
			}
			other, ok := others[key]
			if !ok {
				return result, fmt.Errorf("%w: row key differs", errMigrationPrecondition)
			}
			otherEncoded, err := canonicalMigrationRow(table, other)
			if err != nil {
				return result, err
			}
			if !bytes.Equal(encoded, otherEncoded) {
				return result, fmt.Errorf("%w: row content differs", errMigrationPrecondition)
			}
			digest.Write(encoded)
			digest.Write([]byte{'\n'})
			result.Rows++
		}
		if !more {
			break
		}
		after, err = encodeMigrationKey(table, rows[len(rows)-1])
		if err != nil {
			return result, err
		}
	}
	var sourceCount, targetCount int64
	if err := source.Table(table.Name).Count(&sourceCount).Error; err != nil {
		return result, err
	}
	if err := target.Table(table.Name).Count(&targetCount).Error; err != nil {
		return result, err
	}
	if sourceCount != targetCount || sourceCount != result.Rows {
		return result, fmt.Errorf("%w: row count differs", errMigrationPrecondition)
	}
	result.SHA256 = hex.EncodeToString(digest.Sum(nil))
	return result, nil
}
