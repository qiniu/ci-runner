package state

import (
	"database/sql"
	"fmt"
	"os"

	"gorm.io/gorm"
)

func validateMigrationBackupDirectory(path string) (os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		return nil, fmt.Errorf("%w: backup directory must be a 0700 directory owned by the current user", errMigrationPrecondition)
	}
	owned, err := migrationBackupDirectoryOwnedByCurrentUser(info)
	if err != nil || !owned {
		return nil, fmt.Errorf("%w: backup directory must be a 0700 directory owned by the current user", errMigrationPrecondition)
	}
	return info, nil
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

func migrationSequenceWatermark(db *gorm.DB, table migrationTable, column migrationColumn) (int64, error) {
	var maximum int64
	if err := db.Table(table.Name).Select("COALESCE(MAX(" + migrationQuoted(column.Name) + "), 0)").Scan(&maximum).Error; err != nil {
		return 0, err
	}
	var allocated sql.NullInt64
	if err := db.Raw("SELECT seq FROM sqlite_sequence WHERE name = ?", table.Name).Scan(&allocated).Error; err != nil {
		return 0, err
	}
	return max(int64(0), maximum, allocated.Int64), nil
}
