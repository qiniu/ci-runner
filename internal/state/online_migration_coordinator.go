package state

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

type onlineMigrationSwitchOptions struct {
	ExpectedPrimary string
	QueueBudget     int
	MaxFinalChanges int64
	RecoveryTimeout time.Duration
}

type migrationEndpoint struct {
	store   *DBStore
	db      *gorm.DB
	control migrationControlRecord
}

type onlineMigrationCoordinator struct {
	mu              sync.Mutex // Serializes baseline/replay/verification and handoffs.
	authority       *onlineMigrationAuthority
	store           *onlineMigrationStore
	endpoints       map[string]*migrationEndpoint
	source          *onlineMigrationSource
	replica         *onlineMigrationReplica
	inventory       onlineMigrationInventory
	broken          bool
	backgroundError string
	schemaReport    migrationSchemaReport

	// Private fault injection. Runtime configuration never selects test hooks.
	after func(string) error
}

func newOnlineMigrationCoordinator(ctx context.Context, authority *onlineMigrationAuthority, sqliteStore, postgresStore *DBStore) (*onlineMigrationCoordinator, error) {
	manifest, exists, err := authority.read()
	if err != nil || !exists || manifest.Prepared != nil {
		return nil, fmt.Errorf("%w: coordinator requires recovered authority", errOnlineMigrationNotReady)
	}
	inventory, err := newOnlineMigrationInventory()
	if err != nil {
		return nil, err
	}
	fingerprint, err := inventory.fingerprint()
	if err != nil || fingerprint != manifest.SchemaFingerprint {
		return nil, fmt.Errorf("%w: coordinator schema differs", errOnlineMigrationNotReady)
	}
	coordinator := &onlineMigrationCoordinator{authority: authority, inventory: inventory, endpoints: make(map[string]*migrationEndpoint)}
	for backend, store := range map[string]*DBStore{BackendSQLite: sqliteStore, BackendPostgres: postgresStore} {
		if store == nil || store.opts.Backend != backend {
			return nil, fmt.Errorf("%w: incompatible migration endpoint", errOnlineMigrationNotReady)
		}
		db, err := store.dbOrEnsure()
		if err != nil {
			return nil, migrationStorageError(ctx, "open coordinator endpoint", err)
		}
		pool, attached := db.ConnPool.(*migrationCapturePool)
		instance := manifest.SQLiteInstanceID
		if backend == BackendPostgres {
			instance = manifest.PostgresInstanceID
		}
		role := "replica"
		if backend == manifest.Primary {
			role = "primary"
		}
		if !attached || pool.control.MigrationID != manifest.MigrationID || pool.control.InstanceID != instance ||
			pool.control.SchemaFingerprint != manifest.SchemaFingerprint || pool.control.ProtocolVersion != manifest.ProtocolVersion ||
			pool.control.Role != role || pool.control.Epoch != manifest.Epoch {
			return nil, fmt.Errorf("%w: endpoint is not bound to current authority", errOnlineMigrationNotReady)
		}
		endpoint := &migrationEndpoint{store: store, db: migrationNativeDB(db, pool), control: pool.control}
		report, err := inspectMigrationSchema(ctx, endpoint.db, inventory)
		if err != nil {
			return nil, err
		}
		if backend == BackendSQLite {
			coordinator.schemaReport = report
		}
		// Prepare both directions' metadata before any cutover queues callers.
		if err := endpoint.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := lockMigrationControl(tx, endpoint.control); err != nil {
				return err
			}
			return tx.AutoMigrate(&migrationBaselineRecord{}, &migrationCheckpointRecord{}, &migrationRetentionRecord{})
		}); err != nil {
			return nil, migrationStorageError(ctx, "prepare handoff metadata", err)
		}
		coordinator.endpoints[backend] = endpoint
	}
	coordinator.store, err = newOnlineMigrationStore(coordinator.endpoints[manifest.Primary].store, manifest.Epoch)
	if err != nil {
		return nil, err
	}
	coordinator.source, err = newOnlineMigrationSource(ctx, coordinator.endpoints[manifest.Primary].store)
	if err != nil {
		return nil, err
	}
	other := BackendPostgres
	if manifest.Primary == BackendPostgres {
		other = BackendSQLite
	}
	target := coordinator.endpoints[other]
	coordinator.replica = &onlineMigrationReplica{db: target.db, control: target.control, inventory: inventory}
	return coordinator, nil
}

func ensureEndpointControl(ctx context.Context, endpoint *migrationEndpoint, desired migrationControlRecord, allowed ...migrationControlRecord) error {
	var actual migrationControlRecord
	if err := endpoint.db.WithContext(ctx).First(&actual, 1).Error; err != nil {
		return migrationStorageError(ctx, "read endpoint fence", err)
	}
	if actual == desired {
		return nil
	}
	accepted := false
	for _, candidate := range allowed {
		accepted = accepted || actual == candidate
	}
	if !accepted {
		return fmt.Errorf("%w: unexpected endpoint fence", errOnlineMigrationNotReady)
	}
	err := changeMigrationControl(ctx, endpoint.db, actual, desired)
	if err != nil {
		// Resolve a lost commit result without issuing business writes. If the
		// database cannot answer, keep the operation barrier fail-closed.
		var observed migrationControlRecord
		if readErr := endpoint.db.WithContext(ctx).First(&observed, 1).Error; readErr == nil && observed == desired {
			return nil
		}
	}
	return err
}

func verifyMigrationCapture(ctx context.Context, endpoint *migrationEndpoint, inventory onlineMigrationInventory) error {
	for _, table := range inventory.Tables {
		if endpoint.store.opts.Backend == BackendSQLite {
			for _, statement := range sqliteMigrationTriggers(table) {
				// SQLite omits IF NOT EXISTS in sqlite_master's retained DDL.
				expected := strings.Replace(statement, "IF NOT EXISTS ", "", 1)
				name := strings.Split(statement, `"`)[1]
				var actual string
				if err := endpoint.db.WithContext(ctx).Raw("SELECT sql FROM sqlite_master WHERE type = 'trigger' AND name = ? AND tbl_name = ?", name, table.Name).Scan(&actual).Error; err != nil {
					return migrationStorageError(ctx, "inspect capture trigger", err)
				}
				if strings.TrimSpace(actual) != strings.TrimSpace(expected) {
					return fmt.Errorf("%w: capture trigger differs in %s", errOnlineMigrationNotReady, table.Name)
				}
			}
		} else {
			name := "runnerd_mig_" + table.Name
			statement := postgresMigrationTriggers(table)[0]
			expected := strings.Split(strings.Split(statement, "$runnerd$ ")[1], " $runnerd$")[0]
			var body string
			if err := endpoint.db.WithContext(ctx).Raw(`SELECT p.prosrc FROM pg_trigger t
JOIN pg_class c ON c.oid = t.tgrelid JOIN pg_namespace n ON n.oid = c.relnamespace
JOIN pg_proc p ON p.oid = t.tgfoid
WHERE n.nspname = current_schema() AND c.relname = ? AND t.tgname = ? AND p.proname = ?
AND t.tgenabled = 'O' AND t.tgtype = 29 AND NOT t.tgisinternal`, table.Name, name, name).Scan(&body).Error; err != nil {
				return migrationStorageError(ctx, "inspect capture function", err)
			}
			if strings.TrimSpace(body) != strings.TrimSpace(expected) {
				return fmt.Errorf("%w: capture function differs in %s", errOnlineMigrationNotReady, table.Name)
			}
		}
	}
	return nil
}

func (c *onlineMigrationCoordinator) step(stage string) error {
	if c.after != nil {
		return c.after(stage)
	}
	return nil
}

// switchPrimary uses exactly the same protocol for forward cutover and rollback.
// No provider calls or full-table scans occur while Store admissions are held.
func (c *onlineMigrationCoordinator) switchPrimary(ctx context.Context, options onlineMigrationSwitchOptions) (resultErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.broken || options.RecoveryTimeout <= 0 || options.MaxFinalChanges < 1 || options.MaxFinalChanges > 100000 {
		return fmt.Errorf("%w: invalid handoff state or budgets", errOnlineMigrationNotReady)
	}
	manifest, exists, err := c.authority.read()
	if err != nil || !exists || (manifest.Phase != onlineMigrationReady && manifest.Phase != onlineMigrationPostgresPrimary) {
		return fmt.Errorf("%w: migration is not ready for handoff", errOnlineMigrationNotReady)
	}
	if options.ExpectedPrimary != "" && options.ExpectedPrimary != manifest.Primary {
		return fmt.Errorf("%w: command does not match current authority", errOnlineMigrationNotReady)
	}
	from := c.endpoints[manifest.Primary]
	nextBackend := BackendPostgres
	preparedPhase, committedPhase := onlineMigrationCutoverPrepared, onlineMigrationPostgresPrimary
	if manifest.Primary == BackendPostgres {
		nextBackend, preparedPhase, committedPhase = BackendSQLite, onlineMigrationRollbackPrepared, onlineMigrationSQLiteRestored
	}
	to := c.endpoints[nextBackend]
	if err := verifyMigrationCapture(ctx, from, c.inventory); err != nil {
		return err
	}
	if err := verifyMigrationCapture(ctx, to, c.inventory); err != nil {
		return err
	}
	barrier, err := c.store.drain(ctx, options.QueueBudget)
	if err != nil {
		return err
	}
	oldSource, oldTarget := from.control, to.control
	frozen := oldSource
	frozen.Role = "frozen"
	nextReplicaSource, nextReplicaTarget := oldSource, oldTarget
	nextReplicaSource.Role, nextReplicaSource.Epoch = "replica", manifest.Epoch+1
	nextReplicaTarget.Role, nextReplicaTarget.Epoch = "replica", manifest.Epoch+1
	nextPrimary := nextReplicaTarget
	nextPrimary.Role = "primary"
	preparedWritten, committed, uncertain := false, false, false
	defer func() {
		if resultErr == nil {
			return
		}
		if committed || uncertain {
			c.broken = true
			barrier.fail()
			return
		}
		recoveryCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), options.RecoveryTimeout)
		defer cancel()
		// Fence the would-be target before restoring the original primary.
		recoveryErr := ensureEndpointControl(recoveryCtx, to, oldTarget, oldTarget, nextReplicaTarget)
		if recoveryErr == nil {
			recoveryErr = ensureEndpointControl(recoveryCtx, from, oldSource, oldSource, frozen, nextReplicaSource)
		}
		if recoveryErr == nil {
			recoveryErr = from.store.attachMigrationControl(oldSource)
		}
		if recoveryErr == nil {
			recoveryErr = to.store.attachMigrationControl(oldTarget)
		}
		if recoveryErr == nil && preparedWritten {
			previous, _, readErr := c.authority.read()
			recoveryErr = readErr
			if recoveryErr == nil {
				previous.Revision++
				previous.Prepared = nil
				previous.Phase = onlineMigrationCatchingUp
				if manifest.Primary == BackendPostgres {
					previous.Phase = onlineMigrationPostgresPrimary
				}
				_, recoveryErr = c.authority.commit(recoveryCtx, previous)
			}
		}
		if recoveryErr != nil {
			c.broken = true
			barrier.fail()
			resultErr = fmt.Errorf("%w: handoff recovery requires restart", errOnlineMigrationNotReady)
			return
		}
		c.source.control, c.replica.control = oldSource, oldTarget
		barrier.abort()
	}()
	if barrier.source != from.store || barrier.epoch != manifest.Epoch {
		return fmt.Errorf("%w: Store authority changed", errOnlineMigrationNotReady)
	}
	if err := changeMigrationControl(barrier.ctx, from.db, oldSource, frozen); err != nil {
		return err
	}
	c.source.control = frozen
	if err := c.step("source_frozen"); err != nil {
		return migrationStorageError(ctx, "handoff stage", err)
	}
	for {
		applied, err := c.replica.applyNext(barrier.ctx, c.source)
		if err != nil {
			return err
		}
		if !applied {
			break
		}
	}
	if err := c.replica.verifyFinalChanges(barrier.ctx, c.source, options.MaxFinalChanges); err != nil {
		return err
	}
	if err := c.replica.alignSequences(barrier.ctx, c.source); err != nil {
		return err
	}
	checkpoint, err := readMigrationCheckpoint(to.db.WithContext(barrier.ctx), frozen)
	if err != nil || !checkpoint.Verified || checkpoint.Position != checkpoint.VerifiedPosition {
		return fmt.Errorf("%w: final verification evidence differs", errOnlineMigrationNotReady)
	}
	if err := c.step("final_verified"); err != nil {
		return migrationStorageError(ctx, "handoff stage", err)
	}
	prepared := manifest
	prepared.Revision++
	prepared.Phase = preparedPhase
	prepared.Prepared = &migrationPreparedAuthority{
		From: manifest.Primary, To: nextBackend, NextEpoch: manifest.Epoch + 1,
		SourcePosition: checkpoint.Position, VerifiedPosition: checkpoint.VerifiedPosition,
	}
	if published, err := c.authority.commit(barrier.ctx, prepared); err != nil {
		uncertain = published
		return migrationStorageError(ctx, "prepare authority", err)
	}
	preparedWritten = true
	if err := c.step("authority_prepared"); err != nil {
		return migrationStorageError(ctx, "handoff stage", err)
	}
	if err := changeMigrationControl(barrier.ctx, to.db, oldTarget, nextReplicaTarget); err != nil {
		return err
	}
	if err := c.step("target_epoch_prepared"); err != nil {
		return migrationStorageError(ctx, "handoff stage", err)
	}
	if err := changeMigrationControl(barrier.ctx, from.db, frozen, nextReplicaSource); err != nil {
		return err
	}
	if err := c.step("source_epoch_prepared"); err != nil {
		return migrationStorageError(ctx, "handoff stage", err)
	}
	if err := from.store.attachMigrationControl(nextReplicaSource); err != nil {
		return err
	}
	if err := to.store.attachMigrationControl(nextReplicaTarget); err != nil {
		return err
	}
	reverseReplica := &onlineMigrationReplica{db: from.db, control: nextReplicaSource, inventory: c.inventory}
	var futureGroups int64
	if err := to.db.WithContext(barrier.ctx).Model(&migrationTransactionRecord{}).Where("epoch = ?", nextPrimary.Epoch).Count(&futureGroups).Error; err != nil {
		return migrationStorageError(ctx, "check handoff epoch", err)
	}
	if futureGroups != 0 {
		return fmt.Errorf("%w: next primary epoch already has commits", errOnlineMigrationNotReady)
	}
	if frontier, err := migrationCommittedPosition(to.db.WithContext(barrier.ctx), nextPrimary.Epoch); err != nil || frontier != 0 {
		return fmt.Errorf("%w: next primary epoch has an invalid frontier", errOnlineMigrationNotReady)
	}
	if err := reverseReplica.initializeHandoffCheckpoint(barrier.ctx, nextPrimary); err != nil {
		return err
	}
	if err := c.step("reverse_baseline_ready"); err != nil {
		return migrationStorageError(ctx, "handoff stage", err)
	}
	if err := barrier.ctx.Err(); err != nil {
		return err
	}
	final := prepared
	final.Revision++
	final.Primary, final.Epoch, final.Phase, final.Prepared = nextBackend, nextPrimary.Epoch, committedPhase, nil
	if published, err := c.authority.commit(barrier.ctx, final); err != nil {
		uncertain = published
		return migrationStorageError(ctx, "commit authority", err)
	}
	committed = true
	if err := c.step("authority_committed"); err != nil {
		return migrationStorageError(ctx, "handoff stage", err)
	}
	// After durable commitment, finish the new authority even if the original
	// request deadline expires. Returning to the source is no longer valid.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), options.RecoveryTimeout)
	defer cancel()
	if err := ensureEndpointControl(finishCtx, to, nextPrimary, nextReplicaTarget, nextPrimary); err != nil {
		return err
	}
	if err := to.store.attachMigrationControl(nextPrimary); err != nil {
		return err
	}
	if err := c.step("target_primary_enabled"); err != nil {
		return migrationStorageError(ctx, "handoff stage", err)
	}
	nextReader, err := newOnlineMigrationSource(finishCtx, to.store)
	if err != nil {
		return err
	}
	if err := barrier.publish(to.store, final.Epoch); err != nil {
		_ = nextReader.Close()
		return err
	}
	_ = c.source.Close()
	c.source, c.replica = nextReader, reverseReplica
	from.control, to.control = nextReplicaSource, nextPrimary
	return nil
}
