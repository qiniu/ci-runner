package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOnlineMigrationConfigOptInAndSecret(t *testing.T) {
	directory := t.TempDir()
	path := writeMinimalConfig(t, directory, "runnerd.yaml")
	base, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ObfuscateSecret([]byte("host=localhost password=migration-secret dbname=runnerd"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, extra string
		wantError   bool
	}{
		{"default-disabled", "", false},
		{"enabled", fmt.Sprintf("database:\n  online_migration:\n    directory: ./var/migration\n    postgres_dsn: %s\n    initialize: true\n    pause_budget_milliseconds: 250\n", secret), false},
		{"missing-target", "database:\n  online_migration:\n    directory: ./migration\n", true},
		{"missing-directory", "database:\n  online_migration:\n    postgres_dsn: postgres://localhost/runnerd\n", true},
		{"wrong-backend", "database:\n  backend: mysql\n  dsn: mysql-dsn\n  online_migration:\n    directory: ./migration\n    postgres_dsn: postgres://localhost/runnerd\n", true},
		{"unbounded-pause", "database:\n  online_migration:\n    directory: ./migration\n    postgres_dsn: postgres://localhost/runnerd\n    pause_budget_milliseconds: 60000\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(path, append(append([]byte(nil), base...), []byte(tc.extra)...), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadFile(path)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected configuration result: %v", err)
			}
			if tc.name == "enabled" {
				if cfg.OnlineMigration.Directory != filepath.Join(directory, "var", "migration") || !strings.Contains(cfg.OnlineMigration.PostgresDSN.Value(), "migration-secret") {
					t.Fatal("migration config did not resolve path or decode secret")
				}
				encoded, err := json.Marshal(cfg)
				if err != nil || strings.Contains(string(encoded), "migration-secret") || strings.Contains(fmt.Sprintf("%+v", cfg), "migration-secret") {
					t.Fatal("migration DSN leaked through config serialization")
				}
			}
		})
	}
}
