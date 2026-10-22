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

// Legacy additions remain in the retained source file. Only the explicit
// inventory is transferred; missing model fields or differing primary keys
// reject startup instead of relying on trigger creation to detect a mismatch.
func inspectMigrationSchema(ctx context.Context, db *gorm.DB, inventory onlineMigrationInventory) (migrationSchemaReport, error) {
	var report migrationSchemaReport
	tables, err := db.WithContext(ctx).Migrator().GetTables()
	if err != nil {
		return report, migrationStorageError(ctx, "inspect migration schema", err)
	}
	for _, table := range inventory.Tables {
		columns, err := db.WithContext(ctx).Migrator().ColumnTypes(table.Name)
		if err != nil {
			return report, migrationStorageError(ctx, "inspect migration columns", err)
		}
		actual := make(map[string]gorm.ColumnType, len(columns))
		for _, column := range columns {
			actual[column.Name()] = column
		}
		for _, column := range table.Columns {
			found, exists := actual[column.Name]
			if !exists {
				return report, fmt.Errorf("%w: required schema column missing in %s", errOnlineMigrationNotReady, table.Name)
			}
			primary, known := found.PrimaryKey()
			if !known || primary != column.PrimaryKey {
				return report, fmt.Errorf("%w: schema primary key differs in %s", errOnlineMigrationNotReady, table.Name)
			}
			delete(actual, column.Name)
		}
		for _, column := range actual {
			if primary, known := column.PrimaryKey(); !known || primary {
				return report, fmt.Errorf("%w: unexpected schema primary key in %s", errOnlineMigrationNotReady, table.Name)
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
