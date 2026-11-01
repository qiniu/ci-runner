package state

import (
	"errors"
	"fmt"
	"sort"
	"sync"

	"gorm.io/gorm/schema"
)

var (
	errMigrationInvalid      = errors.New("migration data is invalid")
	errMigrationPrecondition = errors.New("migration precondition failed")
)

// The inventory is deliberately explicit: retired tables and arbitrary tables
// found in an operator's database must not become active migration inputs.
func migrationModels() []any {
	return []any{
		&runnerRequestRecord{}, &runnerEventRecord{}, &runnerProfileRecord{},
		&scopedRunnerProfileRecord{}, &deploymentRecord{}, &deploymentRevisionRecord{},
		&deploymentEventRecord{}, &auditEventRecord{}, &accountRecord{}, &oauthIdentityRecord{},
		&githubInstallationRecord{}, &githubInstallationOwnerRecord{},
		&forkSponsorshipPolicyRecord{}, &forkSponsorshipApprovalRecord{},
		&accountSecretRecord{}, &accountPreferenceRecord{},
		&sandboxServiceDefaultRecord{}, &sandboxServiceDefaultAudienceRecord{},
	}
}

type migrationColumn struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Size          int    `json:"size"`
	Precision     int    `json:"precision"`
	Scale         int    `json:"scale"`
	PrimaryKey    bool   `json:"primary_key"`
	AutoIncrement bool   `json:"auto_increment"`
	NotNull       bool   `json:"not_null"`
	Declaration   string `json:"declaration"`
}

type migrationTable struct {
	Name    string            `json:"name"`
	Columns []migrationColumn `json:"columns"`
}

type migrationInventory struct {
	Tables []migrationTable `json:"tables"`
}

func newMigrationInventory() (migrationInventory, error) {
	return describeMigrationModels(migrationModels())
}

func describeMigrationModels(models []any) (migrationInventory, error) {
	inventory := migrationInventory{}
	seen := make(map[string]bool, len(models))
	var cache sync.Map
	for _, model := range models {
		s, err := schema.Parse(model, &cache, schema.NamingStrategy{})
		if err != nil {
			return migrationInventory{}, fmt.Errorf("parse migration model: %w", err)
		}
		if !validMigrationIdentifier(s.Table) || seen[s.Table] {
			return migrationInventory{}, errors.New("invalid or duplicate migration table")
		}
		seen[s.Table] = true
		table := migrationTable{Name: s.Table}
		for _, field := range s.Fields {
			// Associations have no physical DB column. Explicitly ignored model
			// fields must not leak into the transfer schema either.
			if field.DBName == "" || field.IgnoreMigration {
				continue
			}
			if !validMigrationIdentifier(field.DBName) {
				return migrationInventory{}, errors.New("invalid migration column")
			}
			switch field.GORMDataType {
			case schema.Bool, schema.Int, schema.Uint, schema.String, schema.Time, schema.Bytes, schema.Float:
			default:
				return migrationInventory{}, fmt.Errorf("unsupported migration type for %s.%s", s.Table, field.DBName)
			}
			table.Columns = append(table.Columns, migrationColumn{
				Name: field.DBName, Kind: string(field.GORMDataType),
				Size: field.Size, Precision: field.Precision, Scale: field.Scale,
				PrimaryKey: field.PrimaryKey, AutoIncrement: field.AutoIncrement,
				NotNull: field.NotNull, Declaration: field.Tag.Get("gorm"),
			})
		}
		if len(s.PrimaryFields) == 0 || len(table.Columns) == 0 {
			return migrationInventory{}, errors.New("migration table requires columns and a primary key")
		}
		sort.Slice(table.Columns, func(i, j int) bool { return table.Columns[i].Name < table.Columns[j].Name })
		inventory.Tables = append(inventory.Tables, table)
	}
	if len(inventory.Tables) == 0 {
		return migrationInventory{}, errors.New("migration inventory is empty")
	}
	sort.Slice(inventory.Tables, func(i, j int) bool { return inventory.Tables[i].Name < inventory.Tables[j].Name })
	return inventory, nil
}

func validMigrationIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if (r < 'a' || r > 'z') && r != '_' && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

func migrationQuoted(name string) string { return `"` + name + `"` }

func migrationKeyColumns(table migrationTable) []migrationColumn {
	var keys []migrationColumn
	for _, column := range table.Columns {
		if column.PrimaryKey {
			keys = append(keys, column)
		}
	}
	return keys
}

func migrationTableByName(inventory migrationInventory, name string) (migrationTable, bool) {
	for _, table := range inventory.Tables {
		if table.Name == name {
			return table, true
		}
	}
	return migrationTable{}, false
}
