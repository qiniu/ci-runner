package state

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// MigrationStatus contains no row values, DSNs, credentials or filesystem paths.
type MigrationStatus struct {
	Phase                string `json:"phase"`
	Primary              string `json:"primary"`
	Epoch                int64  `json:"epoch"`
	RecoveryRequired     bool   `json:"recovery_required"`
	SourcePosition       int64  `json:"source_position"`
	AppliedPosition      int64  `json:"applied_position"`
	VerifiedPosition     int64  `json:"verified_position"`
	Verified             bool   `json:"verified"`
	PendingGroups        int64  `json:"pending_groups"`
	OutboxBytes          int64  `json:"outbox_bytes"`
	FreeDiskBytes        uint64 `json:"free_disk_bytes"`
	PrunedThrough        int64  `json:"pruned_through"`
	CapacityWarning      bool   `json:"capacity_warning"`
	BackgroundError      string `json:"background_error,omitempty"`
	RetainedExtraTables  int    `json:"retained_source_extra_tables"`
	RetainedExtraColumns int    `json:"retained_source_extra_columns"`
}

func (r *OnlineMigrationRuntime) Status(ctx context.Context) (MigrationStatus, error) {
	c := r.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	var status MigrationStatus
	manifest, exists, err := c.authority.read()
	if err != nil || !exists {
		return status, fmt.Errorf("%w: authority unavailable", errOnlineMigrationNotReady)
	}
	status.Phase, status.Primary, status.Epoch, status.RecoveryRequired = string(manifest.Phase), manifest.Primary, manifest.Epoch, c.broken
	status.BackgroundError = c.backgroundError
	status.RetainedExtraTables, status.RetainedExtraColumns = c.schemaReport.ExtraTables, c.schemaReport.ExtraColumns
	if c.broken {
		// Endpoints may still carry the previous epoch after a committed fault.
		// Report durable authority only, never mixed-epoch replication numbers.
		return status, nil
	}
	if manifest.Phase == onlineMigrationCompleted || manifest.Phase == onlineMigrationAborted {
		return status, nil
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return status, fmt.Errorf("%w: status requires a deadline", errOnlineMigrationNotReady)
	}
	status.SourcePosition, err = migrationCommittedPosition(c.source.db.WithContext(ctx), c.source.control.Epoch)
	if err != nil {
		return status, migrationStorageError(ctx, "read source status", err)
	}
	if manifest.Phase != onlineMigrationCapturing && manifest.Phase != onlineMigrationBaseline {
		checkpoint, err := readMigrationCheckpoint(c.replica.db.WithContext(ctx), c.source.control)
		if err != nil {
			return status, err
		}
		status.AppliedPosition, status.VerifiedPosition, status.Verified = checkpoint.Position, checkpoint.VerifiedPosition, checkpoint.Verified
	}
	if err := c.source.db.WithContext(ctx).Model(&migrationTransactionRecord{}).
		Where("epoch = ? AND position > ?", manifest.Epoch, status.AppliedPosition).Count(&status.PendingGroups).Error; err != nil {
		return status, migrationStorageError(ctx, "read replication lag", err)
	}
	// Count stored payload bytes independently of sequence gaps. Physical index
	// and WAL size still require the database/disk monitoring in the runbook.
	length := "length(CAST(key_json AS BLOB)) + COALESCE(length(CAST(row_json AS BLOB)), 0)"
	if manifest.Primary == BackendPostgres {
		length = "octet_length(key_json) + COALESCE(octet_length(row_json), 0)"
	}
	if err := c.source.db.WithContext(ctx).Model(&migrationChangeRecord{}).Select("COALESCE(SUM(" + length + "), 0)").Scan(&status.OutboxBytes).Error; err != nil {
		return status, migrationStorageError(ctx, "read outbox capacity", err)
	}
	var disk syscall.Statfs_t
	if err := syscall.Statfs(c.authority.directory, &disk); err != nil {
		return status, fmt.Errorf("%w: inspect migration disk capacity", errOnlineMigrationNotReady)
	}
	status.FreeDiskBytes = disk.Bavail * uint64(disk.Bsize)
	if err := syscall.Statfs(filepath.Dir(c.endpoints[BackendSQLite].store.opts.DatabaseDSN), &disk); err != nil {
		return status, fmt.Errorf("%w: inspect SQLite disk capacity", errOnlineMigrationNotReady)
	}
	status.FreeDiskBytes = min(status.FreeDiskBytes, disk.Bavail*uint64(disk.Bsize))
	status.PrunedThrough, err = migrationRetainedThrough(c.source.db.WithContext(ctx), manifest.Epoch)
	if err != nil {
		return status, migrationStorageError(ctx, "read journal retention", err)
	}
	status.CapacityWarning = status.OutboxBytes > 1<<30 || status.FreeDiskBytes < 256<<20
	return status, nil
}

// Run performs resumable work only; changing authority always requires an
// explicit command. Errors pause this iteration while the primary keeps serving.
func (r *OnlineMigrationRuntime) Run(ctx context.Context, logger *slog.Logger) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	nextProof := time.Now().Add(time.Minute)
	nextCapacity := time.Now().Add(time.Minute)
	lastError := ""
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		workCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		c := r.coordinator
		manifest, _, err := c.authority.read()
		if err == nil {
			switch manifest.Phase {
			case onlineMigrationBaseline:
				_, err = c.baseline(workCtx, 256, 1)
			case onlineMigrationCatchingUp, onlineMigrationReady, onlineMigrationPostgresPrimary, onlineMigrationSQLiteRestored:
				_, err = c.replay(workCtx, 128)
			case onlineMigrationCompleted, onlineMigrationAborted:
				cancel()
				return
			}
		}
		cancel()
		if err == nil && time.Now().After(nextProof) && (manifest.Phase == onlineMigrationCatchingUp || manifest.Phase == onlineMigrationReady || manifest.Phase == onlineMigrationPostgresPrimary || manifest.Phase == onlineMigrationSQLiteRestored) {
			proofCtx, cancelProof := context.WithTimeout(ctx, 30*time.Second)
			_, err = c.refreshVerification(proofCtx, 256)
			cancelProof()
			nextProof = time.Now().Add(time.Minute)
		}
		if err != nil && ctx.Err() == nil {
			// Log only fixed categories. SQL/provider errors never become status.
			category := "replication_paused"
			if os.IsTimeout(err) {
				category = "replication_timeout"
			}
			if lastError != category {
				logger.Warn("online migration background work paused", "category", category)
			}
			lastError = category
			c.mu.Lock()
			c.backgroundError = category
			c.mu.Unlock()
		} else {
			lastError = ""
			c.mu.Lock()
			c.backgroundError = ""
			c.mu.Unlock()
		}
		if time.Now().After(nextCapacity) {
			capacityCtx, cancelCapacity := context.WithTimeout(ctx, 5*time.Second)
			status, statusErr := r.Status(capacityCtx)
			cancelCapacity()
			if statusErr == nil && status.CapacityWarning {
				logger.Warn("online migration capacity requires operator attention", "outbox_bytes", status.OutboxBytes, "free_disk_bytes", status.FreeDiskBytes)
			}
			nextCapacity = time.Now().Add(time.Minute)
		}
	}
}
