package state

import (
	"context"
	"fmt"

	"gorm.io/gorm"
)

type migrationSchemaReport struct {
	ExtraTables  int `json:"extra_tables"`
	ExtraColumns int `json:"extra_columns"`
}

// Inspect the SQLite source. Legacy additions remain in the retained source
// file. Only the explicit inventory is transferred; missing model fields or
// differing primary keys reject import before writing target data.
func inspectMigrationSchema(ctx context.Context, db *gorm.DB, inventory migrationInventory) (migrationSchemaReport, error) {
	var report migrationSchemaReport
	tables, err := db.WithContext(ctx).Migrator().GetTables()
	if err != nil {
		return report, fmt.Errorf("inspect migration schema failed")
	}
	for _, table := range inventory.Tables {
		foundTable := false
		for _, name := range tables {
			if name == table.Name {
				foundTable = true
				break
			}
		}
		if !foundTable {
			return report, fmt.Errorf("%w: required schema table missing in %s", errMigrationInvalid, table.Name)
		}
		// Read SQLite's metadata rather than reparsing CREATE TABLE SQL through
		// GORM. Valid legacy DDL (for example named or multiline primary-key
		// constraints) can otherwise appear to have a missing or unknown key.
		// table_xinfo also includes generated columns retained only in the backup.
		var columns []struct {
			Name string
			PK   int
		}
		if err := db.WithContext(ctx).Raw("SELECT name, pk FROM pragma_table_xinfo(?, 'main')", table.Name).Scan(&columns).Error; err != nil {
			return report, fmt.Errorf("%w: inspect migration columns failed for %s", errMigrationInvalid, table.Name)
		}
		actual := make(map[string]bool, len(columns))
		for _, column := range columns {
			actual[column.Name] = column.PK > 0
		}
		for _, column := range table.Columns {
			found, exists := actual[column.Name]
			if !exists {
				return report, fmt.Errorf("%w: required schema column missing in %s.%s", errMigrationInvalid, table.Name, column.Name)
			}
			if found != column.PrimaryKey {
				return report, fmt.Errorf("%w: schema primary key differs in %s.%s (expected %t, actual %t)", errMigrationInvalid, table.Name, column.Name, column.PrimaryKey, found)
			}
			delete(actual, column.Name)
		}
		for name, primary := range actual {
			if primary {
				return report, fmt.Errorf("%w: unexpected schema primary key in %s.%s", errMigrationInvalid, table.Name, name)
			}
			report.ExtraColumns++
		}
	}
	metadata := map[string]bool{
		"runnerd_migration_control": true, "runnerd_migration_transactions": true,
		"runnerd_migration_changes": true, "runnerd_migration_checkpoints": true, "runnerd_migration_baseline": true,
		"runnerd_migration_retention": true, "runnerd_migration_frontiers": true, "sqlite_sequence": true,
	}
	for _, name := range tables {
		if _, known := migrationTableByName(inventory, name); !known && !metadata[name] {
			report.ExtraTables++
		}
	}
	return report, nil
}
