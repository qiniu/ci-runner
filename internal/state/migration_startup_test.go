package state

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestMigrationStartupFenceSQLite(t *testing.T) {
	testMigrationStartupFence(t, BackendSQLite)
}

func TestMigrationStartupFenceSQLBackends(t *testing.T) {
	testMigrationStartupFence(t, BackendPostgres)
}

func testMigrationStartupFence(t *testing.T, backend string) {
	t.Helper()
	for _, tc := range []struct {
		name, phase              string
		protocol                 int
		absent, empty, malformed bool
		sqliteOK, postgresOK     bool
	}{
		{name: "ordinary database", absent: true, sqliteOK: true, postgresOK: true},
		{name: "copying source or target", phase: "copying", protocol: 1, sqliteOK: true},
		{name: "sealed source", phase: "sealed", protocol: 1},
		{name: "released target", phase: "ready", protocol: 1, postgresOK: true},
		{name: "completed target", phase: "complete", protocol: 1, postgresOK: true},
		{name: "unknown phase", phase: "unknown", protocol: 1},
		{name: "unknown protocol", phase: "complete", protocol: 2},
		{name: "missing marker", empty: true},
		{name: "malformed marker", malformed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dsn := filepath.Join(t.TempDir(), "state.db")
			table := "runnerd_transfer_source"
			if backend == BackendPostgres {
				dsn = isolatedPostgresDSN(t)
				table = "runnerd_transfer_target"
			}
			opts := Options{Backend: backend, DatabaseDSN: dsn, MigrateOnStart: true}
			fixture := NewWithOptions(opts).(*DBStore)
			db, err := fixture.open()
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestDB(t, db)
			if !tc.absent {
				columns := "id INTEGER PRIMARY KEY, protocol INTEGER, phase TEXT"
				if tc.malformed {
					columns = "id INTEGER PRIMARY KEY"
				}
				if err := db.Exec("CREATE TABLE " + table + " (" + columns + ")").Error; err != nil {
					t.Fatal(err)
				}
				if !tc.empty && !tc.malformed {
					if err := db.Exec("INSERT INTO "+table+" (id, protocol, phase) VALUES (1, ?, ?)", tc.protocol, tc.phase).Error; err != nil {
						t.Fatal(err)
					}
				}
			}
			reopened := NewWithOptions(opts).(*DBStore)
			err = reopened.Ensure()
			accepted := tc.sqliteOK
			if backend == BackendPostgres {
				accepted = tc.postgresOK
			}
			if accepted {
				if err != nil {
					t.Fatal(err)
				}
				defer closeTestDB(t, reopened.db)
				if !db.Migrator().HasTable(&runnerEventRecord{}) {
					t.Fatal("ordinary schema upgrade did not run")
				}
			} else {
				if err == nil {
					defer closeTestDB(t, reopened.db)
					t.Fatal("fenced database accepted")
				}
				if !tc.empty && !tc.malformed && !errors.Is(err, errMigrationPrecondition) {
					t.Fatalf("unexpected fence error: %v", err)
				}
				if db.Migrator().HasTable(&runnerEventRecord{}) {
					t.Fatal("startup changed schema before checking fence")
				}
			}
			if !tc.absent && !tc.empty && !tc.malformed {
				var marker struct {
					Protocol int
					Phase    string
				}
				if err := db.Table(table).Where("id = 1").Take(&marker).Error; err != nil {
					t.Fatal(err)
				}
				if marker.Protocol != tc.protocol || marker.Phase != tc.phase {
					t.Fatalf("startup changed migration marker: %+v", marker)
				}
			}
		})
	}
}
