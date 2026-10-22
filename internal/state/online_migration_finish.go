package state

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

// The terminal marker allows a later ordinary startup on this database only.
// Old replicas retain their replica fence forever; no historical business rows,
// payloads, migration journal or database files are deleted here.
func validateStandaloneMigrationDB(db *gorm.DB) error {
	var control migrationControlRecord
	if err := db.First(&control, 1).Error; err != nil || control.Role != "standalone" || control.ActiveTransaction != 0 ||
		control.ProtocolVersion != onlineMigrationProtocolVersion || !validMigrationIdentity(control.MigrationID) || !validMigrationIdentity(control.InstanceID) {
		return fmt.Errorf("%w: use configured online migration recovery", errOnlineMigrationNotReady)
	}
	var count int64
	var err error
	if db.Dialector.Name() == BackendSQLite {
		err = db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'runnerd!_mig!_%' ESCAPE '!' ").Scan(&count).Error
	} else if db.Dialector.Name() == BackendPostgres {
		err = db.Raw(`SELECT COUNT(*) FROM pg_trigger t JOIN pg_class c ON c.oid = t.tgrelid
JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = current_schema() AND t.tgname LIKE 'runnerd!_mig!_%' ESCAPE '!'`).Scan(&count).Error
	} else {
		return fmt.Errorf("%w: unsupported standalone marker", errOnlineMigrationNotReady)
	}
	if err != nil || count != 0 {
		return fmt.Errorf("%w: standalone database still has capture triggers", errOnlineMigrationNotReady)
	}
	return nil
}

// Called only after durable terminal authority or while recovering that exact
// terminal manifest. Trigger removal and the standalone marker commit together.
func finalizeMigrationEndpoint(ctx context.Context, endpoint *migrationEndpoint, inventory onlineMigrationInventory) error {
	standalone := endpoint.control
	standalone.Role = "standalone"
	err := endpoint.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var actual migrationControlRecord
		if err := tx.First(&actual, 1).Error; err != nil {
			return err
		}
		if actual == standalone {
			return validateStandaloneMigrationDB(tx)
		}
		if err := lockMigrationControl(tx, endpoint.control); err != nil {
			return err
		}
		for _, table := range inventory.Tables {
			name := "runnerd_mig_" + table.Name
			if endpoint.store.opts.Backend == BackendSQLite {
				for _, operation := range []string{"insert", "update", "delete"} {
					if err := tx.Exec("DROP TRIGGER " + migrationQuoted(name+"_"+operation)).Error; err != nil {
						return err
					}
				}
			} else {
				if err := tx.Exec("DROP TRIGGER " + migrationQuoted(name) + " ON " + migrationQuoted(table.Name)).Error; err != nil {
					return err
				}
				if err := tx.Exec("DROP FUNCTION " + migrationQuoted(name) + "()").Error; err != nil {
					return err
				}
			}
		}
		return tx.Model(&migrationControlRecord{}).Where("id = 1").Update("role", "standalone").Error
	})
	if err != nil {
		return migrationStorageError(ctx, "finalize capture", err)
	}
	standaloneDB, err := migrationStandaloneTimeDB(endpoint.db.Session(&gorm.Session{NewDB: true, Context: context.Background()}))
	if err != nil {
		return migrationStorageError(ctx, "initialize standalone pool", err)
	}
	endpoint.store.mu.Lock()
	endpoint.store.db = standaloneDB
	endpoint.store.mu.Unlock()
	return nil
}

func (c *onlineMigrationCoordinator) finish(ctx context.Context, abort bool) (resultErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.operational(ctx); err != nil {
		return err
	}
	manifest, _, err := c.authority.read()
	if err != nil {
		return err
	}
	phase := onlineMigrationCompleted
	if abort {
		phase = onlineMigrationAborted
		if manifest.Primary != BackendSQLite {
			return fmt.Errorf("%w: abort requires original SQLite authority", errOnlineMigrationNotReady)
		}
	}
	if !manifest.Phase.permits(phase) {
		return fmt.Errorf("%w: cannot finish or abort this phase", errOnlineMigrationNotReady)
	}
	barrier, err := c.store.drain(ctx, 100)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if resultErr != nil && committed {
			c.broken = true
			barrier.fail()
		} else {
			barrier.abort()
		}
	}()
	manifest.Revision++
	manifest.Phase = phase
	published, err := c.authority.commit(ctx, manifest)
	committed = published
	if err != nil {
		return migrationStorageError(ctx, "commit terminal authority", err)
	}
	if err := c.step("terminal_authority_committed"); err != nil {
		return migrationStorageError(ctx, "finish stage", err)
	}
	if err := finalizeMigrationEndpoint(ctx, c.endpoints[manifest.Primary], c.inventory); err != nil {
		return err
	}
	_ = c.source.Close()
	c.source = nil
	return nil
}
