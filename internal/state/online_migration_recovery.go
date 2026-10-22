package state

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// Recovery receives already-opened stores that have NOT run normal startup
// migration/backfills. Never call dbOrEnsure here: the manifest must be resolved
// before any initialization can write to a DSN that might be the old replica.
func recoverOnlineMigrationEndpoints(ctx context.Context, authority *onlineMigrationAuthority, sqliteStore, postgresStore *DBStore) error {
	manifest, exists, err := authority.read()
	if err != nil || !exists {
		return fmt.Errorf("%w: recovery requires durable authority", errOnlineMigrationNotReady)
	}
	inventory, err := newOnlineMigrationInventory()
	if err != nil {
		return err
	}
	fingerprint, err := inventory.fingerprint()
	if err != nil || fingerprint != manifest.SchemaFingerprint {
		return fmt.Errorf("%w: recovery schema differs", errOnlineMigrationNotReady)
	}
	endpoints := make(map[string]*migrationEndpoint)
	for backend, store := range map[string]*DBStore{BackendSQLite: sqliteStore, BackendPostgres: postgresStore} {
		if store == nil || store.opts.Backend != backend {
			return fmt.Errorf("%w: incompatible recovery endpoint", errOnlineMigrationNotReady)
		}
		store.mu.Lock()
		db := store.db
		store.mu.Unlock()
		if db == nil {
			return fmt.Errorf("%w: recovery endpoint must already be open", errOnlineMigrationNotReady)
		}
		if pool, ok := db.ConnPool.(*migrationCapturePool); ok {
			db = migrationNativeDB(db, pool)
		} else {
			db = db.Session(&gorm.Session{NewDB: true})
		}
		instance := manifest.SQLiteInstanceID
		if backend == BackendPostgres {
			instance = manifest.PostgresInstanceID
		}
		role := "replica"
		if backend == manifest.Primary {
			role = "primary"
		}
		desired := migrationControlRecord{
			ID: 1, MigrationID: manifest.MigrationID, InstanceID: instance,
			ProtocolVersion: manifest.ProtocolVersion, SchemaFingerprint: manifest.SchemaFingerprint, Role: role, Epoch: manifest.Epoch,
		}
		endpoint := &migrationEndpoint{store: store, db: db, control: desired}
		if _, err := inspectMigrationSchema(ctx, db, inventory); err != nil {
			return err
		}
		if err := verifyMigrationCapture(ctx, endpoint, inventory); err != nil {
			return err
		}
		endpoints[backend] = endpoint
	}
	other := BackendPostgres
	if manifest.Primary == BackendPostgres {
		other = BackendSQLite
	}
	primary, replica := endpoints[manifest.Primary], endpoints[other]
	allowedReplica := []migrationControlRecord{replica.control}
	allowedPrimary := []migrationControlRecord{primary.control}
	frozen := primary.control
	frozen.Role = "frozen"
	allowedPrimary = append(allowedPrimary, frozen)
	if manifest.Phase == onlineMigrationPostgresPrimary || manifest.Phase == onlineMigrationSQLiteRestored {
		// Authority can commit immediately before opening the new primary.
		newPrimaryStillFenced := primary.control
		newPrimaryStillFenced.Role = "replica"
		allowedPrimary = append(allowedPrimary, newPrimaryStillFenced)
	}
	// The only permitted future-epoch fences belong to a durable prepared
	// intent. A primary in that future epoch before authority commit is invalid.
	if manifest.Prepared != nil {
		nextSource, nextTarget := primary.control, replica.control
		nextSource.Role, nextSource.Epoch = "replica", manifest.Prepared.NextEpoch
		nextTarget.Epoch = manifest.Prepared.NextEpoch
		allowedPrimary = append(allowedPrimary, nextSource)
		allowedReplica = append(allowedReplica, nextTarget)
	}
	// Restore/confirm the replica first. Never open a primary while the other
	// endpoint could still be a business writer.
	if err := ensureEndpointControl(ctx, replica, replica.control, allowedReplica...); err != nil {
		return err
	}
	if manifest.Phase == onlineMigrationPostgresPrimary || manifest.Phase == onlineMigrationSQLiteRestored || manifest.Phase == onlineMigrationRollbackPrepared {
		if _, err := readMigrationCheckpoint(replica.db.WithContext(ctx), primary.control); err != nil {
			return migrationStorageError(ctx, "read reverse handoff checkpoint", err)
		}
	}
	if err := ensureEndpointControl(ctx, primary, primary.control, allowedPrimary...); err != nil {
		return err
	}
	if err := replica.store.attachMigrationControl(replica.control); err != nil {
		return err
	}
	if err := primary.store.attachMigrationControl(primary.control); err != nil {
		return err
	}
	if manifest.Prepared != nil {
		manifest.Revision++
		manifest.Prepared = nil
		manifest.Phase = onlineMigrationCatchingUp
		if manifest.Primary == BackendPostgres {
			manifest.Phase = onlineMigrationPostgresPrimary
		}
		if _, err := authority.commit(ctx, manifest); err != nil {
			return migrationStorageError(ctx, "cancel prepared authority at recovery", err)
		}
	}
	return nil
}
