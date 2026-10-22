package state

import (
	"path/filepath"
	"testing"
)

func TestOnlineMigrationStatusDoesNotUseBrokenEndpoints(t *testing.T) {
	authority, err := openOnlineMigrationAuthority(filepath.Join(t.TempDir(), "authority"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	manifest := testMigrationManifest(t)
	commitTestManifest(t, authority, manifest)
	// No usable source/replica is intentionally attached. An incomplete handoff
	// must still report its durable authority and explicit recovery requirement.
	runtime := &OnlineMigrationRuntime{coordinator: &onlineMigrationCoordinator{authority: authority, broken: true}}
	status, err := runtime.Status(migrationTestContext(t))
	if err != nil || !status.RecoveryRequired || status.Primary != manifest.Primary || status.Epoch != manifest.Epoch || status.SourcePosition != 0 {
		t.Fatalf("broken migration reported stale endpoint evidence: %+v %v", status, err)
	}
}
