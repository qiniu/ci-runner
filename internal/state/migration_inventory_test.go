package state

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestMigrationInventoryCoversFreshSchema(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestDB(t, db) })
	inventory, err := newMigrationInventory()
	if err != nil {
		t.Fatal(err)
	}
	actual, err := db.Migrator().GetTables()
	if err != nil {
		t.Fatal(err)
	}
	actual = slices.DeleteFunc(actual, func(name string) bool { return strings.HasPrefix(name, "sqlite_") })
	slices.Sort(actual)
	var names []string
	for _, table := range inventory.Tables {
		names = append(names, table.Name)
		columns, err := db.Migrator().ColumnTypes(table.Name)
		if err != nil {
			t.Fatal(err)
		}
		var gotColumns, wantColumns []string
		for _, column := range columns {
			gotColumns = append(gotColumns, column.Name())
		}
		for _, column := range table.Columns {
			wantColumns = append(wantColumns, column.Name)
		}
		slices.Sort(gotColumns)
		if !reflect.DeepEqual(gotColumns, wantColumns) {
			t.Errorf("%s columns = %v, inventory = %v", table.Name, gotColumns, wantColumns)
		}
	}
	if !reflect.DeepEqual(actual, names) {
		t.Fatalf("schema tables = %v, inventory = %v", actual, names)
	}
}
