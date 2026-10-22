package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

var errOnlineMigrationCapture = errors.New("online migration capture rejected")

type migrationControlRecord struct {
	ID                int64  `gorm:"primaryKey;autoIncrement:false"`
	MigrationID       string `gorm:"size:64;not null"`
	InstanceID        string `gorm:"size:64;not null"`
	ProtocolVersion   int    `gorm:"not null"`
	SchemaFingerprint string `gorm:"size:64;not null"`
	Role              string `gorm:"size:16;not null"`
	Epoch             int64  `gorm:"not null"`
	ActiveTransaction int64  `gorm:"not null"`
}

func (migrationControlRecord) TableName() string { return "runnerd_migration_control" }

type migrationTransactionRecord struct {
	Position         int64     `gorm:"primaryKey;autoIncrement"`
	PreviousPosition int64     `gorm:"not null"`
	Epoch            int64     `gorm:"not null"`
	ChangeCount      int64     `gorm:"not null"`
	CreatedAt        time.Time `gorm:"not null"`
}

func (migrationTransactionRecord) TableName() string { return "runnerd_migration_transactions" }

type migrationFrontierRecord struct {
	Epoch    int64 `gorm:"primaryKey;autoIncrement:false"`
	Position int64 `gorm:"not null"`
}

func (migrationFrontierRecord) TableName() string { return "runnerd_migration_frontiers" }

type migrationChangeRecord struct {
	ID                  int64  `gorm:"primaryKey;autoIncrement"`
	TransactionPosition int64  `gorm:"not null;index:idx_migration_changes_transaction"`
	Table               string `gorm:"column:table_name;size:128;not null"`
	Operation           string `gorm:"size:16;not null"`
	KeyJSON             string `gorm:"type:text;not null" json:"-"`
	RowJSON             string `gorm:"type:text" json:"-"`
}

func (migrationChangeRecord) TableName() string { return "runnerd_migration_changes" }

// Avoid exposing row payloads (including encrypted credentials) in diagnostics.
func (c migrationChangeRecord) String() string {
	return fmt.Sprintf("migration change %d in transaction %d (%s %s; payload redacted)", c.ID, c.TransactionPosition, c.Table, c.Operation)
}

func (c migrationChangeRecord) GoString() string { return c.String() }

// installOnlineMigrationCapture must run before serving operations or behind
// the operation barrier. It never changes existing business rows or schemas.
// Keeping capture at the SQL transaction boundary preserves existing explicit
// transactions and their savepoints without duplicating the Store interface.
func (s *DBStore) installOnlineMigrationCapture(ctx context.Context, migrationID, instanceID, role string, epoch int64) error {
	if !validMigrationIdentity(migrationID) || !validMigrationIdentity(instanceID) || epoch <= 0 || (role != "primary" && role != "replica") {
		return fmt.Errorf("%w: invalid capture identity", errOnlineMigrationCapture)
	}
	if s.opts.Backend != BackendSQLite && s.opts.Backend != BackendPostgres {
		return fmt.Errorf("%w: unsupported capture backend", errOnlineMigrationCapture)
	}
	db, err := s.dbOrEnsure()
	if err != nil {
		return err
	}
	inventory, err := newOnlineMigrationInventory()
	if err != nil {
		return err
	}
	fingerprint, err := inventory.fingerprint()
	if err != nil {
		return err
	}
	want := migrationControlRecord{
		ID: 1, MigrationID: migrationID, InstanceID: instanceID,
		ProtocolVersion: onlineMigrationProtocolVersion, SchemaFingerprint: fingerprint,
		Role: role, Epoch: epoch,
	}
	if _, alreadyWrapped := db.ConnPool.(*migrationCapturePool); alreadyWrapped {
		return fmt.Errorf("%w: capture is already attached", errOnlineMigrationCapture)
	}
	if err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Reject a foreign protocol/identity before AutoMigrate can touch even
		// the control schema. Attachment is not a metadata repair operation.
		if tx.Migrator().HasTable(&migrationControlRecord{}) {
			var existing migrationControlRecord
			if err := tx.First(&existing, 1).Error; err != nil {
				return fmt.Errorf("%w: existing control is unreadable", errOnlineMigrationCapture)
			}
			if existing != want {
				return fmt.Errorf("%w: capture identity or role differs", errOnlineMigrationCapture)
			}
		}
		if err := tx.AutoMigrate(&migrationControlRecord{}, &migrationTransactionRecord{}, &migrationChangeRecord{}, &migrationRetentionRecord{}, &migrationFrontierRecord{}); err != nil {
			return err
		}
		var existing migrationControlRecord
		err := tx.First(&existing, 1).Error
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if err := tx.Create(&want).Error; err != nil {
				return err
			}
		case err != nil:
			return err
		case existing != want:
			return fmt.Errorf("%w: capture identity or role differs", errOnlineMigrationCapture)
		}
		for _, table := range inventory.Tables {
			if !tx.Migrator().HasTable(table.Name) {
				return fmt.Errorf("%w: missing business table %s", errOnlineMigrationCapture, table.Name)
			}
			for _, column := range table.Columns {
				if !tx.Migrator().HasColumn(table.Name, column.Name) {
					return fmt.Errorf("%w: missing business column %s.%s", errOnlineMigrationCapture, table.Name, column.Name)
				}
			}
			statements := sqliteMigrationTriggers(table)
			if s.opts.Backend == BackendPostgres {
				statements = postgresMigrationTriggers(table)
			}
			for _, statement := range statements {
				if err := tx.Exec(statement).Error; err != nil {
					return fmt.Errorf("install migration capture for %s: %w", table.Name, err)
				}
			}
		}
		return nil
	}); err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	pool := &migrationCapturePool{DB: sqlDB, backend: s.opts.Backend, control: want}
	wrapped := db.Session(&gorm.Session{NewDB: true, Context: context.Background()})
	wrapped.ConnPool, wrapped.Statement.ConnPool = pool, pool
	s.mu.Lock()
	s.db = wrapped
	s.mu.Unlock()
	return nil
}

func validMigrationIdentity(value string) bool {
	if len(value) == 0 || len(value) > 64 {
		return false
	}
	for _, c := range value {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// The control row is locked before allocating a position. On PostgreSQL this
// serializes migration-era write transactions, so position order is commit
// order. An ordinary sequence alone cannot provide that guarantee.
type migrationCapturePool struct {
	*sql.DB
	backend string
	control migrationControlRecord
}

var (
	_ gorm.ConnPoolBeginner = (*migrationCapturePool)(nil)
	_ gorm.GetDBConnector   = (*migrationCapturePool)(nil)
	_ gorm.Tx               = (*migrationCaptureTx)(nil)
)

func (p *migrationCapturePool) GetDBConn() (*sql.DB, error) { return p.DB, nil }

func (p *migrationCapturePool) BeginTx(ctx context.Context, opts *sql.TxOptions) (gorm.ConnPool, error) {
	tx, err := p.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	if opts != nil && opts.ReadOnly {
		return tx, nil
	}
	managed := &migrationCaptureTx{Tx: tx, ctx: ctx, backend: p.backend}
	if err := managed.begin(p.control); err != nil {
		_ = tx.Rollback() // Preserve the capture error even if cancellation already rolled back.
		return nil, err
	}
	return managed, nil
}

type migrationCaptureTx struct {
	*sql.Tx
	ctx      context.Context
	backend  string
	position int64
	epoch    int64
	previous int64
}

func (t *migrationCaptureTx) begin(want migrationControlRecord) error {
	// A no-op update obtains the SQLite write lock before any reads; PostgreSQL
	// takes the same singleton row lock before business SQL can lock other rows.
	if _, err := t.ExecContext(t.ctx, "UPDATE runnerd_migration_control SET active_transaction = active_transaction WHERE id = 1"); err != nil {
		return err
	}
	var got migrationControlRecord
	if err := t.QueryRowContext(t.ctx, "SELECT migration_id, instance_id, protocol_version, schema_fingerprint, role, epoch, active_transaction FROM runnerd_migration_control WHERE id = 1").Scan(
		&got.MigrationID, &got.InstanceID, &got.ProtocolVersion, &got.SchemaFingerprint, &got.Role, &got.Epoch, &got.ActiveTransaction,
	); err != nil {
		return err
	}
	got.ID = 1
	if got != want || got.Role != "primary" {
		return fmt.Errorf("%w: primary identity changed or replica is fenced", errOnlineMigrationCapture)
	}
	frontierQuery := "SELECT COALESCE((SELECT position FROM runnerd_migration_frontiers WHERE epoch = " + t.bind(1) + "), 0), COALESCE((SELECT MAX(position) FROM runnerd_migration_transactions WHERE epoch = " + t.bind(2) + "), 0)"
	var latest int64
	if err := t.QueryRowContext(t.ctx, frontierQuery, want.Epoch, want.Epoch).Scan(&t.previous, &latest); err != nil {
		return err
	}
	if t.previous < 0 || t.previous != latest {
		return fmt.Errorf("%w: committed journal frontier differs", errOnlineMigrationCapture)
	}
	t.epoch = want.Epoch
	query := "INSERT INTO runnerd_migration_transactions (epoch, previous_position, change_count, created_at) VALUES (?, ?, 0, ?) RETURNING position"
	if t.backend == BackendPostgres {
		query = "INSERT INTO runnerd_migration_transactions (epoch, previous_position, change_count, created_at) VALUES ($1, $2, 0, $3) RETURNING position"
	}
	if err := t.QueryRowContext(t.ctx, query, want.Epoch, t.previous, time.Now().UTC()).Scan(&t.position); err != nil {
		return err
	}
	if t.position <= t.previous {
		return fmt.Errorf("%w: journal allocation moved backwards", errOnlineMigrationCapture)
	}
	_, err := t.ExecContext(t.ctx, "UPDATE runnerd_migration_control SET active_transaction = "+t.bind(1)+" WHERE id = 1", t.position)
	return err
}

func (t *migrationCaptureTx) bind(position int) string {
	if t.backend == BackendPostgres {
		return fmt.Sprintf("$%d", position)
	}
	return "?"
}

func (t *migrationCaptureTx) Commit() error {
	query := "UPDATE runnerd_migration_transactions SET change_count = (SELECT COUNT(*) FROM runnerd_migration_changes WHERE transaction_position = " + t.bind(1) + ") WHERE position = " + t.bind(2)
	if _, err := t.ExecContext(t.ctx, query, t.position, t.position); err != nil {
		_ = t.Tx.Rollback()
		return err
	}
	query = "INSERT INTO runnerd_migration_frontiers (epoch, position) VALUES (" + t.bind(1) + ", " + t.bind(2) + ") ON CONFLICT (epoch) DO UPDATE SET position = excluded.position WHERE runnerd_migration_frontiers.position = " + t.bind(3)
	result, err := t.ExecContext(t.ctx, query, t.epoch, t.position, t.previous)
	if err != nil {
		_ = t.Tx.Rollback()
		return err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		_ = t.Tx.Rollback()
		return fmt.Errorf("%w: committed journal frontier changed", errOnlineMigrationCapture)
	}
	if _, err := t.ExecContext(t.ctx, "UPDATE runnerd_migration_control SET active_transaction = 0 WHERE id = 1"); err != nil {
		_ = t.Tx.Rollback()
		return err
	}
	return t.Tx.Commit()
}

func migrationQuoted(name string) string { return `"` + name + `"` }

func migrationKeyColumns(table onlineMigrationTable) []onlineMigrationColumn {
	var keys []onlineMigrationColumn
	for _, column := range table.Columns {
		if column.PrimaryKey {
			keys = append(keys, column)
		}
	}
	return keys
}

// SQLite's JSON function argument ceiling is lower than the Runner row width.
// Concatenating object bodies preserves explicit NULLs, unlike json_patch.
func sqliteMigrationObject(columns []onlineMigrationColumn, prefix string) string {
	var objects []string
	for start := 0; start < len(columns); start += 30 {
		var pairs []string
		for _, column := range columns[start:min(start+30, len(columns))] {
			pairs = append(pairs, "'"+column.Name+"'", prefix+"."+migrationQuoted(column.Name))
		}
		objects = append(objects, "json_object("+strings.Join(pairs, ",")+")")
	}
	if len(objects) == 1 {
		return objects[0]
	}
	var bodies []string
	for _, object := range objects {
		bodies = append(bodies, "substr("+object+", 2, length("+object+") - 2)")
	}
	return "('{' || " + strings.Join(bodies, " || ',' || ") + " || '}')"
}

func sqliteMigrationTriggers(table onlineMigrationTable) []string {
	const control = "runnerd_migration_control"
	valid := "EXISTS (SELECT 1 FROM " + control + " c JOIN runnerd_migration_transactions t ON t.position = c.active_transaction AND t.epoch = c.epoch WHERE c.id = 1 AND c.role = 'primary' AND c.active_transaction > 0)"
	replication := "EXISTS (SELECT 1 FROM " + control + " WHERE id = 1 AND role = 'replica' AND active_transaction = -1)"
	guard := "SELECT CASE WHEN (" + valid + " OR " + replication + ") THEN 1 ELSE RAISE(ABORT, 'runnerd migration requires managed transaction') END; "
	oldKey := sqliteMigrationObject(migrationKeyColumns(table), "OLD")
	newKey := sqliteMigrationObject(migrationKeyColumns(table), "NEW")
	row := sqliteMigrationObject(table.Columns, "NEW")
	insert := func(operation, key, data, condition string) string {
		return "INSERT INTO runnerd_migration_changes (transaction_position, table_name, operation, key_json, row_json) SELECT active_transaction, '" + table.Name + "', '" + operation + "', " + key + ", " + data + " FROM " + control + " WHERE id = 1 AND role = 'primary'" + condition + "; "
	}
	var statements []string
	for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
		body := guard
		if operation == "UPDATE" {
			body += insert("delete", oldKey, "NULL", " AND "+oldKey+" IS NOT "+newKey)
		}
		if operation == "DELETE" {
			body += insert("delete", oldKey, "NULL", "")
		} else {
			body += insert("upsert", newKey, row, "")
		}
		name := "runnerd_mig_" + table.Name + "_" + strings.ToLower(operation)
		statements = append(statements, "CREATE TRIGGER IF NOT EXISTS "+migrationQuoted(name)+" AFTER "+operation+" ON "+migrationQuoted(table.Name)+" BEGIN "+body+" END")
	}
	return statements
}

func postgresMigrationObject(columns []onlineMigrationColumn, prefix string) string {
	var objects []string
	for start := 0; start < len(columns); start += 30 {
		var pairs []string
		for _, column := range columns[start:min(start+30, len(columns))] {
			pairs = append(pairs, "'"+column.Name+"'", prefix+"."+migrationQuoted(column.Name))
		}
		objects = append(objects, "jsonb_build_object("+strings.Join(pairs, ",")+")")
	}
	return "(" + strings.Join(objects, " || ") + ")"
}

func postgresMigrationTriggers(table onlineMigrationTable) []string {
	name := "runnerd_mig_" + table.Name
	oldKey := postgresMigrationObject(migrationKeyColumns(table), "OLD")
	newKey := postgresMigrationObject(migrationKeyColumns(table), "NEW")
	row := postgresMigrationObject(table.Columns, "NEW")
	insert := func(operation, key, data string) string {
		return "INSERT INTO runnerd_migration_changes (transaction_position, table_name, operation, key_json, row_json) VALUES (c.active_transaction, '" + table.Name + "', '" + operation + "', (" + key + ")::text, " + data + "); "
	}
	body := `DECLARE c runnerd_migration_control%ROWTYPE;
BEGIN
SELECT * INTO c FROM runnerd_migration_control WHERE id = 1;
IF c.role = 'replica' AND c.active_transaction = -1 THEN RETURN NULL; END IF;
IF c.role IS DISTINCT FROM 'primary' OR c.active_transaction IS NULL OR c.active_transaction <= 0 OR NOT EXISTS (SELECT 1 FROM runnerd_migration_transactions WHERE position = c.active_transaction AND epoch = c.epoch) THEN
RAISE EXCEPTION 'runnerd migration requires managed transaction';
END IF;
IF TG_OP = 'DELETE' THEN ` + insert("delete", oldKey, "NULL") + `
ELSE
IF TG_OP = 'UPDATE' THEN
IF ` + oldKey + ` IS DISTINCT FROM ` + newKey + ` THEN ` + insert("delete", oldKey, "NULL") + ` END IF;
END IF;
` + insert("upsert", newKey, row+"::text") + `
END IF;
RETURN NULL;
END;`
	return []string{
		"CREATE OR REPLACE FUNCTION " + migrationQuoted(name) + "() RETURNS trigger LANGUAGE plpgsql AS $runnerd$ " + body + " $runnerd$",
		"DROP TRIGGER IF EXISTS " + migrationQuoted(name) + " ON " + migrationQuoted(table.Name),
		"CREATE TRIGGER " + migrationQuoted(name) + " AFTER INSERT OR UPDATE OR DELETE ON " + migrationQuoted(table.Name) + " FOR EACH ROW EXECUTE FUNCTION " + migrationQuoted(name) + "()",
	}
}
