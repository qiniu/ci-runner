package state

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"gorm.io/gorm"
)

func (c *onlineMigrationCoordinator) operational(ctx context.Context) error {
	if c.broken || c.source == nil {
		return fmt.Errorf("%w: coordinator requires recovery", errOnlineMigrationNotReady)
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return fmt.Errorf("%w: operation requires a deadline", errOnlineMigrationNotReady)
	}
	return ctx.Err()
}

func (c *onlineMigrationCoordinator) phase(ctx context.Context, phase onlineMigrationPhase) error {
	manifest, exists, err := c.authority.read()
	if err != nil || !exists {
		return fmt.Errorf("%w: authority unavailable", errOnlineMigrationNotReady)
	}
	if manifest.Phase == phase {
		return nil
	}
	manifest.Revision++
	manifest.Phase = phase
	_, err = c.authority.commit(ctx, manifest)
	return migrationStorageError(ctx, "publish migration phase", err)
}

// replay serializes consumers with verification and authority changes. Each
// group remains its own transaction; a timed-out call retains committed progress.
func (c *onlineMigrationCoordinator) replay(ctx context.Context, maxGroups int) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.operational(ctx); err != nil {
		return 0, err
	}
	if maxGroups < 1 || maxGroups > 10000 {
		return 0, fmt.Errorf("%w: invalid replay budget", errOnlineMigrationNotReady)
	}
	for i := 0; i < maxGroups; i++ {
		applied, err := c.replica.applyNext(ctx, c.source)
		if err != nil || !applied {
			return i, err
		}
	}
	return maxGroups, nil
}

// refreshVerification establishes a new full proof outside the Store barrier.
// SQLite uses an immutable artifact; PostgreSQL uses a separate repeatable-read
// transaction. Replay stops at exactly that view's position, even while the
// primary continues committing newer transactions.
func (c *onlineMigrationCoordinator) refreshVerification(ctx context.Context, pageSize int) (onlineMigrationVerification, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var report onlineMigrationVerification
	if err := c.operational(ctx); err != nil {
		return report, err
	}
	if pageSize < 1 || pageSize > 1000 {
		return report, fmt.Errorf("%w: invalid verification page size", errOnlineMigrationNotReady)
	}
	manifest, _, err := c.authority.read()
	if err != nil {
		return report, err
	}
	if manifest.Phase != onlineMigrationCatchingUp && manifest.Phase != onlineMigrationReady &&
		manifest.Phase != onlineMigrationPostgresPrimary && manifest.Phase != onlineMigrationSQLiteRestored {
		return report, fmt.Errorf("%w: verification requires a completed baseline", errOnlineMigrationNotReady)
	}
	if manifest.Phase == onlineMigrationReady {
		if err := c.phase(ctx, onlineMigrationCatchingUp); err != nil {
			return report, err
		}
	}
	verify := func(view *gorm.DB, control migrationControlRecord, position int64) error {
		if err := c.step("verification_view_ready"); err != nil {
			return migrationStorageError(ctx, "verification view", err)
		}
		for {
			applied, err := c.replica.applyNextUntil(ctx, c.source, &position)
			if err != nil {
				return err
			}
			if !applied {
				break
			}
		}
		var err error
		report, err = c.replica.verifyView(ctx, view, control, position, pageSize)
		return err
	}
	if manifest.Primary == BackendSQLite {
		snapshot, snapshotErr := c.endpoints[BackendSQLite].store.createOnlineMigrationSnapshot(ctx, c.authority.directory)
		if snapshotErr != nil {
			return report, migrationStorageError(ctx, "create verification snapshot", snapshotErr)
		}
		defer func() {
			_ = snapshot.Close()
			_ = os.Remove(snapshot.Path) // This proof artifact is not baseline recovery state.
		}()
		err = verify(snapshot.db, snapshot.Control, snapshot.Checkpoint)
	} else {
		err = c.source.db.WithContext(ctx).Transaction(func(view *gorm.DB) error {
			var control migrationControlRecord
			if err := view.First(&control, 1).Error; err != nil {
				return err
			}
			if control != c.source.control {
				return fmt.Errorf("%w: verification source changed", errOnlineMigrationNotReady)
			}
			position, err := migrationCommittedPosition(view, control.Epoch)
			if err != nil {
				return err
			}
			return verify(view, control, position)
		}, migrationReadOptions())
	}
	if err == nil && manifest.Primary == BackendSQLite && manifest.Phase != onlineMigrationSQLiteRestored {
		err = c.phase(ctx, onlineMigrationReady)
	}
	return report, migrationStorageError(ctx, "refresh verification", err)
}

// baseline resumes only the artifact durably bound in the authority manifest.
// Lost or changed snapshots require operator recovery, never an implicit merge
// of unrelated snapshots into a partially copied target.
func (c *onlineMigrationCoordinator) baseline(ctx context.Context, pageSize, maxBatches int) (bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.operational(ctx); err != nil {
		return false, err
	}
	manifest, _, err := c.authority.read()
	if err != nil {
		return false, err
	}
	if manifest.Primary != BackendSQLite || manifest.Phase != onlineMigrationBaseline || manifest.SnapshotFile == "" ||
		pageSize < 1 || pageSize > 1000 || maxBatches < 1 || maxBatches > 10000 {
		return false, fmt.Errorf("%w: invalid baseline operation", errOnlineMigrationNotReady)
	}
	snapshot, err := openOnlineMigrationSnapshot(ctx, filepath.Join(c.authority.directory, manifest.SnapshotFile), c.source.control)
	if err != nil {
		return false, migrationStorageError(ctx, "reopen baseline snapshot", err)
	}
	defer snapshot.Close()
	if snapshot.Digest != manifest.SnapshotDigest {
		return false, fmt.Errorf("%w: baseline artifact changed", errOnlineMigrationNotReady)
	}
	for i := 0; i < maxBatches; i++ {
		complete, err := c.replica.copySnapshotBatch(ctx, snapshot, pageSize)
		if err != nil {
			return false, err
		}
		if complete {
			return true, c.phase(ctx, onlineMigrationCatchingUp)
		}
	}
	return false, nil
}
