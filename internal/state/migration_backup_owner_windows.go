//go:build windows

package state

import (
	"errors"
	"os"
)

func migrationBackupDirectoryOwnedByCurrentUser(os.FileInfo) (bool, error) {
	return false, errors.New("backup directory ownership checks are unavailable on Windows")
}
