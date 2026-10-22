package state

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestOnlineMigrationInventoryCoversFreshSchema(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { closeTestDB(t, db) })
	inventory, err := newOnlineMigrationInventory()
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

func TestOnlineMigrationInventoryKeysAndFingerprint(t *testing.T) {
	inventory, err := newOnlineMigrationInventory()
	if err != nil {
		t.Fatal(err)
	}
	keys := make(map[string][]string)
	for _, table := range inventory.Tables {
		for _, column := range table.Columns {
			if column.PrimaryKey {
				keys[table.Name] = append(keys[table.Name], column.Name)
			}
			if table.Name == "github_installation_owners" && column.Name == "installation_id" && column.AutoIncrement {
				t.Fatal("external installation ID must not become an allocated sequence")
			}
		}
	}
	if !reflect.DeepEqual(keys["scoped_runner_profiles"], []string{"name", "scope_id", "scope_type"}) {
		t.Fatalf("scoped composite key = %v", keys["scoped_runner_profiles"])
	}
	if !reflect.DeepEqual(keys["fork_sponsorship_approvals"], []string{"fork_repository_id", "source_repository_id"}) {
		t.Fatalf("sponsorship composite key = %v", keys["fork_sponsorship_approvals"])
	}
	first, err := inventory.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	models := onlineMigrationModels()
	slices.Reverse(models)
	reordered, err := describeOnlineMigrationModels(models)
	if err != nil {
		t.Fatal(err)
	}
	second, err := reordered.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("model ordering changed the schema fingerprint")
	}
	reordered.Tables[0].Columns[0].NotNull = !reordered.Tables[0].Columns[0].NotNull
	changed, err := reordered.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("column compatibility change did not change the fingerprint")
	}
	for _, retired := range []string{"runner_groups", "repository_policies", "runner_group_specs", "runner_profile_scope_controls"} {
		if _, exists := keys[retired]; exists {
			t.Fatalf("retired table %s is active in migration", retired)
		}
	}
}

func TestOnlineMigrationInventoryRejectsIncompleteModels(t *testing.T) {
	type noKey struct{ Value string }
	for name, models := range map[string][]any{
		"empty":          nil,
		"no primary key": {&noKey{}},
		"duplicate":      {&accountRecord{}, &accountRecord{}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := describeOnlineMigrationModels(models); err == nil {
				t.Fatal("accepted an incomplete or ambiguous inventory")
			}
		})
	}
}

func TestOnlineMigrationProtocolCannotSkipCaptureOrReturnToStaleSQLite(t *testing.T) {
	for _, edge := range [][2]onlineMigrationPhase{
		{onlineMigrationPlanned, onlineMigrationPostgresPrimary},
		{onlineMigrationBaseline, onlineMigrationReady},
		{onlineMigrationReady, onlineMigrationPostgresPrimary},
		{onlineMigrationPostgresPrimary, onlineMigrationAborted},
		{onlineMigrationPostgresPrimary, onlineMigrationSQLiteRestored},
		{onlineMigrationCutoverPrepared, onlineMigrationAborted},
		{onlineMigrationCompleted, onlineMigrationCapturing},
		{onlineMigrationAborted, onlineMigrationCapturing},
		{"unknown", onlineMigrationCapturing},
		{onlineMigrationReady, "unknown"},
	} {
		if edge[0].permits(edge[1]) {
			t.Errorf("accepted unsafe transition %s -> %s", edge[0], edge[1])
		}
	}
	for _, edge := range [][2]onlineMigrationPhase{
		{onlineMigrationReady, onlineMigrationCatchingUp},
		{onlineMigrationCutoverPrepared, onlineMigrationCatchingUp},
		{onlineMigrationRollbackPrepared, onlineMigrationPostgresPrimary},
		{onlineMigrationCapturing, onlineMigrationAborted},
	} {
		if !edge[0].permits(edge[1]) {
			t.Errorf("cannot cancel preparation safely: %s -> %s", edge[0], edge[1])
		}
	}
}

func TestOnlineMigrationCutoverRequiresFreshCompleteEvidence(t *testing.T) {
	fingerprint := strings.Repeat("a", 64)
	ready := onlineMigrationCutoverEvidence{
		ProtocolVersion: onlineMigrationProtocolVersion, Epoch: 2, SchemaFingerprint: fingerprint,
		SourceCheckpoint: 10, TargetCheckpoint: 10, VerifiedCheckpoint: 10,
		BaselineComplete: true, CaptureComplete: true, SourceDrained: true,
		TargetHealthy: true, ReverseCaptureReady: true, SequencesReady: true,
	}
	if err := ready.validate(2, fingerprint); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*onlineMigrationCutoverEvidence){
		"protocol":            func(e *onlineMigrationCutoverEvidence) { e.ProtocolVersion++ },
		"old epoch":           func(e *onlineMigrationCutoverEvidence) { e.Epoch-- },
		"schema":              func(e *onlineMigrationCutoverEvidence) { e.SchemaFingerprint = strings.Repeat("b", 64) },
		"baseline":            func(e *onlineMigrationCutoverEvidence) { e.BaselineComplete = false },
		"capture gap":         func(e *onlineMigrationCutoverEvidence) { e.CaptureComplete = false },
		"active transaction":  func(e *onlineMigrationCutoverEvidence) { e.SourceDrained = false },
		"target failure":      func(e *onlineMigrationCutoverEvidence) { e.TargetHealthy = false },
		"reverse capture":     func(e *onlineMigrationCutoverEvidence) { e.ReverseCaptureReady = false },
		"sequence":            func(e *onlineMigrationCutoverEvidence) { e.SequencesReady = false },
		"replication behind":  func(e *onlineMigrationCutoverEvidence) { e.TargetCheckpoint-- },
		"replication ahead":   func(e *onlineMigrationCutoverEvidence) { e.TargetCheckpoint++ },
		"verification behind": func(e *onlineMigrationCutoverEvidence) { e.VerifiedCheckpoint-- },
		"verification ahead":  func(e *onlineMigrationCutoverEvidence) { e.VerifiedCheckpoint++ },
	} {
		t.Run(name, func(t *testing.T) {
			evidence := ready
			change(&evidence)
			if err := evidence.validate(2, fingerprint); !errors.Is(err, errOnlineMigrationNotReady) {
				t.Fatalf("unsafe evidence error = %v", err)
			}
		})
	}
	if err := (onlineMigrationCutoverEvidence{}).validate(0, ""); !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("empty evidence accepted: %v", err)
	}
	ready.SourceCheckpoint, ready.TargetCheckpoint, ready.VerifiedCheckpoint = 0, 0, 0
	if err := ready.validate(2, fingerprint); err != nil {
		t.Fatalf("verified empty source rejected: %v", err)
	}
}
