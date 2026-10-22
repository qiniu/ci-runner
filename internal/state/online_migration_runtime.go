package state

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// OnlineMigrationOptions is explicit opt-in. DSNs belong only in protected
// runnerd configuration; neither is included in authority or status output.
type OnlineMigrationOptions struct {
	Directory   string
	SQLiteDSN   string `json:"-"`
	PostgresDSN string `json:"-"`
	Initialize  bool
	PauseBudget time.Duration
}

func (OnlineMigrationOptions) String() string {
	return "online migration options [connections redacted]"
}
func (o OnlineMigrationOptions) GoString() string { return o.String() }

type OnlineMigrationRuntime struct {
	coordinator *onlineMigrationCoordinator
	stores      []*DBStore
	pauseBudget time.Duration
}

// openMigrationEndpoint opens connections with context-bound I/O, without
// schema changes, backfills, capture repair or implicit role selection.
func openMigrationEndpoint(ctx context.Context, store *DBStore) error {
	driver := "sqlite"
	if store.opts.Backend == BackendPostgres {
		driver = "pgx"
	} else if store.opts.Backend != BackendSQLite {
		return fmt.Errorf("%w: unsupported endpoint", errOnlineMigrationNotReady)
	}
	pool, err := sql.Open(driver, store.opts.DatabaseDSN)
	if err != nil {
		return migrationStorageError(ctx, "open endpoint", err)
	}
	pool.SetMaxOpenConns(8)
	pool.SetMaxIdleConns(2)
	if driver == "sqlite" {
		pool.SetMaxOpenConns(1)
		pool.SetMaxIdleConns(1)
	}
	dialector := gorm.Dialector(sqlite.Dialector{Conn: pool})
	if driver == "pgx" {
		dialector = postgres.New(postgres.Config{Conn: pool})
	}
	db, err := gorm.Open(dialector, &gorm.Config{
		DisableAutomaticPing:                     true,
		DisableForeignKeyConstraintWhenMigrating: true, Logger: logger.Default.LogMode(logger.Silent),
	})
	if err == nil {
		err = pool.PingContext(ctx)
	}
	if err == nil && driver == "sqlite" {
		for _, statement := range []string{"PRAGMA journal_mode = WAL", "PRAGMA synchronous = NORMAL", "PRAGMA busy_timeout = 1000", "PRAGMA foreign_keys = ON"} {
			if err = db.WithContext(ctx).Exec(statement).Error; err != nil {
				break
			}
		}
	}
	if err != nil {
		_ = pool.Close()
		return migrationStorageError(ctx, "initialize endpoint connection", err)
	}
	store.db = db
	return nil
}

func newMigrationIdentity() (string, error) {
	var value [16]byte
	_, err := rand.Read(value[:])
	return hex.EncodeToString(value[:]), err
}

// OpenOnlineMigration holds the authority lock for the server's entire life.
// The configured SQLite file remains the identity anchor after PG takes over;
// operators must not change database.backend to select the active primary.
func OpenOnlineMigration(ctx context.Context, options OnlineMigrationOptions) (_ *OnlineMigrationRuntime, resultErr error) {
	if _, bounded := ctx.Deadline(); !bounded || options.Directory == "" || options.SQLiteDSN == "" || options.PostgresDSN == "" {
		return nil, fmt.Errorf("%w: explicit endpoint configuration and startup deadline required", errOnlineMigrationNotReady)
	}
	// A migration never implicitly creates/replaces a missing source database.
	info, err := os.Stat(options.SQLiteDSN)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: existing SQLite file required", errOnlineMigrationNotReady)
	}
	authority, err := openOnlineMigrationAuthority(options.Directory)
	if err != nil {
		return nil, migrationStorageError(ctx, "lock authority directory", err)
	}
	if options.PauseBudget == 0 {
		options.PauseBudget = 500 * time.Millisecond
	}
	if options.PauseBudget < 50*time.Millisecond || options.PauseBudget > 5*time.Second {
		_ = authority.Close()
		return nil, fmt.Errorf("%w: pause budget must be 50..5000 milliseconds", errOnlineMigrationNotReady)
	}
	runtime := &OnlineMigrationRuntime{pauseBudget: options.PauseBudget}
	defer func() {
		if resultErr != nil {
			for _, store := range runtime.stores {
				if store.db != nil {
					if pool, err := store.db.DB(); err == nil {
						_ = pool.Close()
					}
				}
			}
			_ = authority.Close()
		}
	}()
	source := NewWithOptions(Options{Backend: BackendSQLite, DatabaseDSN: options.SQLiteDSN}).(*DBStore)
	target := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: options.PostgresDSN}).(*DBStore)
	runtime.stores = []*DBStore{source, target}
	manifest, exists, err := authority.read()
	if err != nil {
		return nil, err
	}
	if !exists && !options.Initialize {
		return nil, fmt.Errorf("%w: missing authority; initialization must be explicit", errOnlineMigrationNotReady)
	}
	if exists && (manifest.Phase == onlineMigrationCompleted || manifest.Phase == onlineMigrationAborted) {
		primary, identity := source, manifest.SQLiteInstanceID
		if manifest.Primary == BackendPostgres {
			primary, identity = target, manifest.PostgresInstanceID
		}
		if err := openMigrationEndpoint(ctx, primary); err != nil {
			return nil, err
		}
		inventory, err := newOnlineMigrationInventory()
		if err != nil {
			return nil, err
		}
		fingerprint, err := inventory.fingerprint()
		if err != nil || fingerprint != manifest.SchemaFingerprint {
			return nil, fmt.Errorf("%w: terminal recovery schema differs", errOnlineMigrationNotReady)
		}
		endpoint := &migrationEndpoint{store: primary, db: primary.db, control: migrationControlRecord{
			ID:          1,
			MigrationID: manifest.MigrationID, InstanceID: identity, ProtocolVersion: manifest.ProtocolVersion,
			SchemaFingerprint: manifest.SchemaFingerprint, Role: "primary", Epoch: manifest.Epoch,
		}}
		if err := finalizeMigrationEndpoint(ctx, endpoint, inventory); err != nil {
			return nil, err
		}
		if err := initializeStateBackfills(primary.db.WithContext(ctx)); err != nil {
			return nil, migrationStorageError(ctx, "initialize terminal authority", err)
		}
		facade, err := newOnlineMigrationStore(primary, manifest.Epoch)
		if err != nil {
			return nil, err
		}
		runtime.coordinator = &onlineMigrationCoordinator{
			authority: authority, store: facade, inventory: inventory,
			endpoints: map[string]*migrationEndpoint{manifest.Primary: endpoint},
		}
		return runtime, nil
	}
	if err := openMigrationEndpoint(ctx, source); err != nil {
		return nil, err
	}
	if !exists {
		if source.db.Migrator().HasTable(&migrationControlRecord{}) {
			return nil, fmt.Errorf("%w: captured SQLite has no authority; recover the original directory", errOnlineMigrationNotReady)
		}
		inventory, err := newOnlineMigrationInventory()
		if err != nil {
			return nil, err
		}
		fingerprint, err := inventory.fingerprint()
		if err != nil {
			return nil, err
		}
		migrationID, err := newMigrationIdentity()
		if err != nil {
			return nil, err
		}
		sqliteID, err := newMigrationIdentity()
		if err != nil {
			return nil, err
		}
		postgresID, err := newMigrationIdentity()
		if err != nil {
			return nil, err
		}
		manifest = onlineMigrationManifest{
			ProtocolVersion: onlineMigrationProtocolVersion, Revision: 1,
			MigrationID: migrationID, SchemaFingerprint: fingerprint, SQLiteInstanceID: sqliteID, PostgresInstanceID: postgresID,
			Primary: BackendSQLite, Epoch: 1, Phase: onlineMigrationPlanned,
		}
		if _, err := authority.commit(ctx, manifest); err != nil {
			return nil, migrationStorageError(ctx, "initialize authority", err)
		}
	}
	if err := openMigrationEndpoint(ctx, target); err != nil {
		return nil, err
	}
	if manifest.Phase == onlineMigrationPlanned {
		if err := initializeMigrationEndpoints(ctx, manifest, source, target); err != nil {
			return nil, err
		}
		manifest.Revision++
		manifest.Phase = onlineMigrationCapturing
		if _, err := authority.commit(ctx, manifest); err != nil {
			return nil, migrationStorageError(ctx, "confirm capture initialization", err)
		}
	} else {
		if err := recoverOnlineMigrationEndpoints(ctx, authority, source, target); err != nil {
			return nil, err
		}
		primary := source
		if manifest.Primary == BackendPostgres {
			primary = target
		}
		if err := initializeStateBackfills(primary.db.WithContext(ctx)); err != nil {
			return nil, migrationStorageError(ctx, "initialize authoritative state", err)
		}
	}
	coordinator, err := newOnlineMigrationCoordinator(ctx, authority, source, target)
	if err != nil {
		return nil, err
	}
	runtime.coordinator = coordinator
	return runtime, nil
}

func initializeMigrationEndpoints(ctx context.Context, manifest onlineMigrationManifest, source, target *DBStore) error {
	inventory, err := newOnlineMigrationInventory()
	if err != nil {
		return err
	}
	fingerprint, err := inventory.fingerprint()
	if err != nil || fingerprint != manifest.SchemaFingerprint {
		return fmt.Errorf("%w: initialization schema changed", errOnlineMigrationNotReady)
	}
	// Validate and fence the fresh target before enabling source capture.
	for _, store := range []*DBStore{target, source} {
		role, identity := "primary", manifest.SQLiteInstanceID
		if store == target {
			role, identity = "replica", manifest.PostgresInstanceID
		}
		control := migrationControlRecord{
			ID: 1, MigrationID: manifest.MigrationID, InstanceID: identity,
			ProtocolVersion: manifest.ProtocolVersion, SchemaFingerprint: fingerprint, Role: role, Epoch: manifest.Epoch,
		}
		if store.db.Migrator().HasTable(&migrationControlRecord{}) {
			var actual migrationControlRecord
			if err := store.db.WithContext(ctx).First(&actual, 1).Error; err != nil || actual != control {
				return fmt.Errorf("%w: initialization endpoint identity differs", errOnlineMigrationNotReady)
			}
			if err := verifyMigrationCapture(ctx, &migrationEndpoint{store: store, db: store.db, control: control}, inventory); err != nil {
				return err
			}
			if err := store.attachMigrationControl(control); err != nil {
				return err
			}
			continue
		}
		if store == target {
			// A new target must be empty. Unknown tables also reject initialization
			// so a typo cannot migrate into somebody else's database/schema.
			tables, err := store.db.WithContext(ctx).Migrator().GetTables()
			if err != nil {
				return migrationStorageError(ctx, "inspect initial target", err)
			}
			for _, name := range tables {
				if _, known := migrationTableByName(inventory, name); !known {
					return fmt.Errorf("%w: initial target contains unknown tables", errOnlineMigrationNotReady)
				}
				var count int64
				if err := store.db.WithContext(ctx).Table(name).Count(&count).Error; err != nil || count != 0 {
					return fmt.Errorf("%w: initial target is not empty", errOnlineMigrationNotReady)
				}
			}
		}
		if err := store.migrate(store.db.WithContext(ctx)); err != nil {
			return migrationStorageError(ctx, "prepare endpoint schema", err)
		}
		if err := store.installOnlineMigrationCapture(ctx, manifest.MigrationID, identity, role, manifest.Epoch); err != nil {
			return migrationStorageError(ctx, "initialize endpoint capture", err)
		}
	}
	return nil
}

func (r *OnlineMigrationRuntime) Store() Store { return r.coordinator.store }

// Close requires the HTTP server and workers to have stopped using Store.
func (r *OnlineMigrationRuntime) Close() error {
	c := r.coordinator
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.source != nil {
		_ = c.source.Close()
	}
	for _, store := range r.stores {
		if store.db == nil {
			continue
		}
		if pool, err := store.db.DB(); err == nil {
			_ = pool.Close()
		}
	}
	return c.authority.Close()
}

func (c *onlineMigrationCoordinator) prepareSnapshot(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.operational(ctx); err != nil {
		return err
	}
	manifest, _, err := c.authority.read()
	if err != nil {
		return err
	}
	if manifest.Phase != onlineMigrationCapturing || manifest.SnapshotFile != "" {
		return fmt.Errorf("%w: baseline snapshot already prepared or unavailable", errOnlineMigrationNotReady)
	}
	snapshot, err := c.endpoints[BackendSQLite].store.createOnlineMigrationSnapshot(ctx, c.authority.directory)
	if err != nil {
		return migrationStorageError(ctx, "prepare baseline snapshot", err)
	}
	defer snapshot.Close()
	manifest.Revision++
	manifest.Phase = onlineMigrationBaseline
	manifest.SnapshotFile, manifest.SnapshotDigest = filepath.Base(snapshot.Path), snapshot.Digest
	_, err = c.authority.commit(ctx, manifest)
	// Even on an uncertain commit, retain the artifact for startup recovery.
	return migrationStorageError(ctx, "bind baseline snapshot", err)
}

// Execute runs only in the process that owns the Store facade. The CLI sends
// these fixed commands over a private local socket; it never opens databases.
func (r *OnlineMigrationRuntime) Execute(ctx context.Context, command string) error {
	c := r.coordinator
	switch command {
	case "prepare":
		return c.prepareSnapshot(ctx)
	case "baseline":
		_, err := c.baseline(ctx, 256, 16)
		return err
	case "replay":
		_, err := c.replay(ctx, 256)
		return err
	case "verify":
		_, err := c.refreshVerification(ctx, 256)
		return err
	case "prune":
		pruneCtx, cancel := context.WithTimeout(ctx, r.pauseBudget)
		defer cancel()
		return c.prune(pruneCtx)
	case "cutover", "rollback":
		// The command must agree with authority. Never let a retried forward
		// command accidentally initiate rollback after a successful cutover.
		expected := BackendSQLite
		if command == "rollback" {
			expected = BackendPostgres
		}
		status, err := r.Status(ctx)
		if err != nil {
			return err
		}
		if status.CapacityWarning {
			return fmt.Errorf("%w: migration capacity limits require operator attention", errOnlineMigrationNotReady)
		}
		pauseCtx, cancel := context.WithTimeout(ctx, r.pauseBudget)
		defer cancel()
		return c.switchPrimary(pauseCtx, onlineMigrationSwitchOptions{
			ExpectedPrimary: expected,
			QueueBudget:     100, MaxFinalChanges: 10000, RecoveryTimeout: 5 * time.Second,
		})
	case "finish", "abort":
		pauseCtx, cancel := context.WithTimeout(ctx, r.pauseBudget)
		defer cancel()
		return c.finish(pauseCtx, command == "abort")
	default:
		return fmt.Errorf("%w: unknown migration command", errOnlineMigrationNotReady)
	}
}
