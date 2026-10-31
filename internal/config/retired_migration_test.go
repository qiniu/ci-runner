package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetiredOnlineMigrationConfigRejected(t *testing.T) {
	for _, value := range []string{"{}", "null", "{directory: ./var/migration, postgres_dsn: private-secret}"} {
		path := filepath.Join(t.TempDir(), "runnerd.yaml")
		if err := os.WriteFile(path, []byte("database:\n  online_migration: "+value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadFile(path)
		if err == nil || !strings.Contains(err.Error(), "database.online_migration") || strings.Contains(err.Error(), "private-secret") {
			t.Fatalf("legacy config was ignored or leaked: %v", err)
		}
	}
}
