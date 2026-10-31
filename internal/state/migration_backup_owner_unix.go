//go:build !windows

package state

import (
	"os"
	"syscall"
)

func migrationBackupDirectoryOwnedByCurrentUser(info os.FileInfo) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, nil
	}
	return uint32(stat.Uid) == uint32(os.Geteuid()), nil
}
