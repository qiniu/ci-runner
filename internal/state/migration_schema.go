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
// reject import before writing target data.
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
		columns, err := db.WithContext(ctx).Migrator().ColumnTypes(table.Name)
		if err != nil {
			return report, fmt.Errorf("%w: inspect migration columns failed for %s", errMigrationInvalid, table.Name)
		}
		actual := make(map[string]gorm.ColumnType, len(columns))
		for _, column := range columns {
			actual[column.Name()] = column
		}
		for _, column := range table.Columns {
			found, exists := actual[column.Name]
			if !exists {
				return report, fmt.Errorf("%w: required schema column missing in %s", errMigrationInvalid, table.Name)
			}
			primary, known := found.PrimaryKey()
			if !known || primary != column.PrimaryKey {
				return report, fmt.Errorf("%w: schema primary key differs in %s", errMigrationInvalid, table.Name)
			}
			delete(actual, column.Name)
		}
		for _, column := range actual {
			if primary, known := column.PrimaryKey(); !known || primary {
				return report, fmt.Errorf("%w: unexpected schema primary key in %s", errMigrationInvalid, table.Name)
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
