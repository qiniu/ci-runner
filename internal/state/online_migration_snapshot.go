package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

type onlineMigrationSnapshot struct {
	Path       string
	Control    migrationControlRecord
	Checkpoint int64
	Digest     string
	db         *gorm.DB
}

// createOnlineMigrationSnapshot uses SQLite's consistent VACUUM INTO backup,
// not an in-place VACUUM. A separate pool leaves the one-connection business
// pool available to writers in WAL mode. The caller must bound snapshot time
// and WAL/disk growth; the completed artifact permits resumable baseline reads
// without holding a long-lived read transaction on the live database.
func (s *DBStore) createOnlineMigrationSnapshot(ctx context.Context, directory string) (_ *onlineMigrationSnapshot, resultErr error) {
	if s.opts.Backend != BackendSQLite {
		return nil, fmt.Errorf("%w: baseline source must be SQLite", errOnlineMigrationNotReady)
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return nil, fmt.Errorf("%w: snapshot requires a deadline", errOnlineMigrationNotReady)
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return nil, err
	}
	pool, ok := db.ConnPool.(*migrationCapturePool)
	if !ok || pool.control.Role != "primary" {
		return nil, fmt.Errorf("%w: baseline source capture is not attached", errOnlineMigrationNotReady)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(directory)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: snapshot directory must be private", errOnlineMigrationNotReady)
	}
	// VACUUM INTO may need every live WAL page as well as the main file.
	// Reserve headroom before starting; ongoing disk/WAL growth still requires
	// external monitoring and the operation deadline, not an availability claim.
	var required uint64 = 256 << 20
	for _, candidate := range []string{s.opts.DatabaseDSN, s.opts.DatabaseDSN + "-wal"} {
		if info, err := os.Stat(candidate); err == nil {
			required += uint64(info.Size())
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: inspect snapshot capacity", errOnlineMigrationNotReady)
		}
	}
	var capacity syscall.Statfs_t
	if err := syscall.Statfs(directory, &capacity); err != nil || capacity.Bavail*uint64(capacity.Bsize) < required {
		return nil, fmt.Errorf("%w: insufficient snapshot disk headroom", errOnlineMigrationNotReady)
	}
	file, err := os.CreateTemp(directory, "snapshot-*.db")
	if err != nil {
		return nil, err
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = os.Remove(path) // Only the unique artifact created by this call.
		}
	}()
	reader, err := sql.Open("sqlite", s.opts.DatabaseDSN)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	reader.SetMaxOpenConns(1)
	reader.SetMaxIdleConns(1)
	// Opening a reader must not change journal_mode or run startup migration.
	if _, err := reader.ExecContext(ctx, "PRAGMA busy_timeout = 1000"); err != nil {
		return nil, err
	}
	if _, err := reader.ExecContext(ctx, "VACUUM INTO ?", path); err != nil {
		return nil, fmt.Errorf("create consistent migration snapshot: %w", err)
	}
	// VACUUM INTO's output durability is not assumed. Sync the file and its
	// directory before returning a path that progress records may reference.
	if err := syncMigrationSnapshot(path, directory); err != nil {
		return nil, err
	}
	return openOnlineMigrationSnapshot(ctx, path, pool.control)
}

func syncMigrationSnapshot(path, directory string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	err = file.Sync()
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	dir, err := os.Open(directory)
	if err != nil {
		return err
	}
	err = dir.Sync()
	closeErr = dir.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// openOnlineMigrationSnapshot is also the resume path. Immutable read-only mode
// prevents its copied capture triggers, startup migrations or any caller from
// changing the baseline and silently invalidating its checkpoint.
func openOnlineMigrationSnapshot(ctx context.Context, path string, expected migrationControlRecord) (*onlineMigrationSnapshot, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: snapshot must be a private regular file", errOnlineMigrationNotReady)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := (&url.URL{Scheme: "file", Path: absolute, RawQuery: "mode=ro&immutable=1"}).String()
	db, err := gorm.Open(sqlite.Open(uri), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent), DisableForeignKeyConstraintWhenMigrating: true,
	})
	if err != nil {
		return nil, err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	var control migrationControlRecord
	err = db.WithContext(ctx).First(&control, 1).Error
	if err == nil && (control != expected || control.Role != "primary" || control.ActiveTransaction != 0) {
		err = fmt.Errorf("%w: snapshot identity or checkpoint epoch differs", errOnlineMigrationNotReady)
	}
	if err == nil {
		var integrity string
		err = db.WithContext(ctx).Raw("PRAGMA quick_check").Scan(&integrity).Error
		if err == nil && integrity != "ok" {
			err = fmt.Errorf("%w: snapshot integrity check failed", errOnlineMigrationNotReady)
		}
	}
	var checkpoint int64
	if err == nil {
		checkpoint, err = migrationCommittedPosition(db.WithContext(ctx), control.Epoch)
	}
	if err != nil {
		return nil, errors.Join(err, sqlDB.Close())
	}
	file, err := os.Open(absolute)
	if err != nil {
		return nil, errors.Join(err, sqlDB.Close())
	}
	digest := sha256.New()
	var buffer [64 * 1024]byte
	var copyErr error
	for {
		if copyErr = ctx.Err(); copyErr != nil {
			break
		}
		n, readErr := file.Read(buffer[:])
		_, _ = digest.Write(buffer[:n])
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				copyErr = readErr
			}
			break
		}
	}
	if err := errors.Join(copyErr, file.Close()); err != nil {
		return nil, errors.Join(err, sqlDB.Close())
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, sqlDB.Close())
	}
	return &onlineMigrationSnapshot{Path: absolute, Control: control, Checkpoint: checkpoint, Digest: hex.EncodeToString(digest.Sum(nil)), db: db}, nil
}

func (s *onlineMigrationSnapshot) Close() error {
	db, err := s.db.DB()
	if err != nil {
		return err
	}
	return db.Close()
}
