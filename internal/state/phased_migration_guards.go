package state

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type phasedTrigger struct{ name, sql string }

// Capture request keys, not row payloads. This also sees UpdateColumns and
// terminal-state evidence writes that do not advance updated_at or version.
func (m *phasedMigration) sourceTriggers() []phasedTrigger {
	var triggers []phasedTrigger
	for _, table := range m.inventory.Tables {
		for _, operation := range []string{"INSERT", "UPDATE", "DELETE"} {
			name := "runnerd_transfer_" + table.Name + "_" + strings.ToLower(operation)
			body := "SELECT RAISE(ABORT, 'source sealed for PostgreSQL migration') WHERE COALESCE((SELECT phase FROM runnerd_transfer_source WHERE id = 1), '') <> 'copying'; "
			if table.Name == "runner_requests" {
				if operation != "INSERT" {
					body += "INSERT INTO runnerd_transfer_changes(request_id) VALUES (OLD.id); "
				}
				if operation != "DELETE" {
					body += "INSERT INTO runnerd_transfer_changes(request_id) VALUES (NEW.id); "
				}
			}
			if table.Name == "runner_events" && operation != "INSERT" {
				body += "SELECT RAISE(ABORT, 'event history must remain append-only during migration'); "
			}
			triggers = append(triggers, phasedTrigger{name, "CREATE TRIGGER " + migrationQuoted(name) + " BEFORE " + operation + " ON " + migrationQuoted(table.Name) + " BEGIN " + body + "END"})
		}
	}
	// SQLite assigns automatic IDs before AFTER triggers. Reject explicit inserts
	// behind the copied prefix; the normal AUTOINCREMENT writer remains unchanged.
	name := "runnerd_transfer_events_monotonic"
	triggers = append(triggers, phasedTrigger{name, `CREATE TRIGGER "runnerd_transfer_events_monotonic" AFTER INSERT ON "runner_events" BEGIN SELECT RAISE(ABORT, 'event ID must advance migration history') WHERE NEW.id <= (SELECT event_floor FROM runnerd_transfer_source WHERE id = 1) OR NEW.id <= COALESCE((SELECT MAX(id) FROM runner_events WHERE id <> NEW.id), 0); END`})
	return triggers
}

func (m *phasedMigration) validateSourceTriggers() error {
	var found []struct{ Name, SQL string }
	if err := m.source.Raw("SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND name LIKE 'runnerd!_transfer!_%' ESCAPE '!' ").Scan(&found).Error; err != nil {
		return err
	}
	expected := m.sourceTriggers()
	if len(expected) != len(found) {
		return fmt.Errorf("%w: source capture or fence triggers changed", errMigrationPrecondition)
	}
	actual := make(map[string]string, len(found))
	for _, trigger := range found {
		actual[trigger.Name] = strings.TrimSuffix(strings.TrimSpace(trigger.SQL), ";")
	}
	for _, trigger := range expected {
		if actual[trigger.name] != trigger.sql {
			return fmt.Errorf("%w: source capture or fence triggers changed", errMigrationPrecondition)
		}
	}
	return nil
}

func quotePhasedSchema(name string) string { return `"` + strings.ReplaceAll(name, `"`, `""`) + `"` }

func (m *phasedMigration) targetGuardBody() string {
	return `
DECLARE current_phase text; transfer_token text;
BEGIN
 SELECT phase, token INTO current_phase, transfer_token FROM ` + quotePhasedSchema(m.schema) + `.runnerd_transfer_target WHERE id = 1;
 IF current_phase IS NULL OR current_phase NOT IN ('copying', 'ready', 'complete') OR
    (current_phase = 'copying' AND current_setting('runnerd.transfer_token', true) IS DISTINCT FROM transfer_token) THEN
  RAISE EXCEPTION 'PostgreSQL target is not ready for runnerd';
 END IF;
 IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
 RETURN NEW;
END `
}

func (m *phasedMigration) installTargetGuards(tx *gorm.DB) error {
	function := quotePhasedSchema(m.schema) + `.runnerd_transfer_guard`
	if err := tx.Exec(`CREATE FUNCTION ` + function + `() RETURNS trigger LANGUAGE plpgsql AS $guard$` + m.targetGuardBody() + `$guard$`).Error; err != nil {
		return err
	}
	for _, table := range m.inventory.Tables {
		if err := tx.Exec("CREATE TRIGGER runnerd_transfer_guard BEFORE INSERT OR UPDATE OR DELETE ON " + migrationQuoted(table.Name) + " FOR EACH ROW EXECUTE FUNCTION " + function + "()").Error; err != nil {
			return err
		}
		if err := tx.Exec("CREATE TRIGGER runnerd_transfer_truncate_guard BEFORE TRUNCATE ON " + migrationQuoted(table.Name) + " FOR EACH STATEMENT EXECUTE FUNCTION " + function + "()").Error; err != nil {
			return err
		}
	}
	return nil
}

func (m *phasedMigration) validateTargetGuards() error {
	var guards []struct {
		TableName, Name, Enabled, Body, FunctionSchema string
		Kind                                           int
	}
	err := m.target.Raw(`SELECT c.relname AS table_name, t.tgname AS name, t.tgenabled AS enabled,
 t.tgtype AS kind, p.prosrc AS body, pn.nspname AS function_schema
 FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace
 JOIN pg_proc p ON p.oid=t.tgfoid JOIN pg_namespace pn ON pn.oid=p.pronamespace
 WHERE n.nspname=current_schema() AND t.tgname IN ('runnerd_transfer_guard','runnerd_transfer_truncate_guard')`).Scan(&guards).Error
	if err != nil {
		return err
	}
	if len(guards) != 2*len(m.inventory.Tables) {
		return fmt.Errorf("%w: target write guards changed", errMigrationPrecondition)
	}
	for _, guard := range guards {
		_, known := migrationTableByName(m.inventory, guard.TableName)
		kind := 31 // ROW | BEFORE | INSERT | DELETE | UPDATE
		if guard.Name == "runnerd_transfer_truncate_guard" {
			kind = 34
		} // BEFORE | TRUNCATE
		if !known || guard.Enabled != "O" || guard.Kind != kind || guard.FunctionSchema != m.schema || guard.Body != m.targetGuardBody() {
			return fmt.Errorf("%w: target write guards changed", errMigrationPrecondition)
		}
	}
	return nil
}

func (m *phasedMigration) sourceImmediate(fn func(*gorm.DB) error) error {
	if err := m.source.Exec("BEGIN IMMEDIATE").Error; err != nil {
		return err
	}
	defer m.source.WithContext(context.Background()).Exec("ROLLBACK")
	if err := fn(m.source.Session(&gorm.Session{SkipDefaultTransaction: true})); err != nil {
		return err
	}
	return m.source.Exec("COMMIT").Error
}

func (m *phasedMigration) copyTransaction(fn func(*gorm.DB) error) error {
	return m.target.Transaction(func(tx *gorm.DB) error {
		var state phasedTargetRecord
		if err := tx.First(&state, 1).Error; err != nil {
			return err
		}
		if state.Phase != "copying" || state.Token != m.sourceState.Token {
			return fmt.Errorf("%w: target no longer accepts source updates", errMigrationPrecondition)
		}
		if err := tx.Exec("SELECT set_config('runnerd.transfer_token', ?, true)", state.Token).Error; err != nil {
			return err
		}
		return fn(tx)
	})
}

func (m *phasedMigration) onlineBackup() error {
	m.progress("create private online SQLite backup", 0)
	if m.options.BackupPath == "" {
		return fmt.Errorf("%w: first prepare requires --backup", errMigrationPrecondition)
	}
	path, err := filepath.Abs(m.options.BackupPath)
	if err != nil {
		return err
	}
	if _, err := validateMigrationBackupDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	original, err := file.Stat()
	if err != nil {
		return err
	}
	same := func() bool { current, err := os.Stat(path); return err == nil && os.SameFile(original, current) }
	if !same() {
		return fmt.Errorf("%w: backup was replaced", errMigrationPrecondition)
	}
	// In WAL mode VACUUM INTO takes a read snapshot, not a source-wide write lock.
	if err := m.source.Exec("VACUUM INTO ?", path).Error; err != nil {
		return err
	}
	if err := syncMigrationSnapshot(path, filepath.Dir(path)); err != nil {
		return err
	}
	if !same() {
		return fmt.Errorf("%w: backup was replaced", errMigrationPrecondition)
	}
	m.progress("verify online backup integrity", 0)
	backup, err := gorm.Open(sqlite.Open((&url.URL{Scheme: "file", Path: path, RawQuery: "mode=ro&immutable=1"}).String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		return err
	}
	pool, err := backup.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	var integrity string
	if err := backup.WithContext(m.source.Statement.Context).Raw("PRAGMA integrity_check").Scan(&integrity).Error; err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("%w: backup integrity failed", errMigrationPrecondition)
	}
	return nil
}

// validatePhasedMigrationStartup runs before GORM startup migrations/backfills.
// Database triggers additionally protect an older runnerd binary from writes.
func validatePhasedMigrationStartup(db *gorm.DB) error {
	var table string
	switch db.Dialector.Name() {
	case BackendSQLite:
		table = (phasedSourceRecord{}).TableName()
	case BackendPostgres:
		table = (phasedTargetRecord{}).TableName()
	default:
		return nil
	}
	var exists bool
	if db.Dialector.Name() == BackendSQLite {
		if err := db.Raw("SELECT COUNT(*) > 0 FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&exists).Error; err != nil {
			return err
		}
	} else if err := db.Raw("SELECT to_regclass(?) IS NOT NULL", table).Scan(&exists).Error; err != nil {
		return err
	}
	if !exists {
		return nil
	}
	var control struct {
		Protocol int
		Phase    string
	}
	if err := db.Table(table).Where("id = 1").Take(&control).Error; err != nil {
		return err
	}
	allowed := control.Protocol == phasedProtocol && ((db.Dialector.Name() == BackendSQLite && control.Phase == "copying") || (db.Dialector.Name() == BackendPostgres && (control.Phase == "ready" || control.Phase == "complete")))
	if !allowed {
		return fmt.Errorf("%w: database is fenced by phased migration; inspect runnerd-migrate --mode status", errMigrationPrecondition)
	}
	return nil
}
