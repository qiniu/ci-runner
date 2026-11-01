package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise main in a separate process so accidental service startup, os.Exit,
// and the migration subcommand's independent flag parsing are all covered.
func TestMigrationCommandProcess(t *testing.T) {
	if os.Getenv("RUNNERD_MIGRATION_COMMAND_HELPER") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append([]string{os.Args[0]}, os.Args[i+1:]...)
				main()
				os.Exit(0)
			}
		}
		os.Exit(99)
	}
	dir := t.TempDir()
	sqliteConfig := filepath.Join(dir, "sqlite.yaml")
	postgresConfig := filepath.Join(dir, "postgres.yaml")
	for path, data := range map[string]string{
		sqliteConfig:   "database:\n  backend: sqlite\n  dsn: must-not-create.db\n",
		postgresConfig: "database:\n  backend: postgres\n  dsn: postgres://test:secret@127.0.0.1:1/unused\n",
	} {
		data += `github:
  webhook_secret: test-webhook
  token: test-token
  oauth:
    client_id: test-client
    client_secret: test-secret
auth:
  encryption_key: test-encryption-key
  session_secret: test-session
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test
`
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{"--sqlite", filepath.Join(dir, "missing.db"), "--backup", filepath.Join(dir, "backup.db")}
	for _, tc := range []struct {
		name string
		args []string
		code int
		want string
	}{
		{name: "help", args: []string{"migrate", "--help"}, want: "Usage of runnerd migrate"},
		{name: "missing confirmation", args: append([]string{"migrate", "--config", sqliteConfig}, paths...), code: 1, want: "require --config"},
		{name: "wrong backend", args: append([]string{"migrate", "--config", sqliteConfig, "--source-stopped"}, paths...), code: 1, want: "PostgreSQL target config"},
		{name: "missing source", args: append([]string{"migrate", "--config", postgresConfig, "--source-stopped"}, paths...), code: 1, want: "existing SQLite file required"},
		{name: "service flag rejected", args: []string{"migrate", "--bootstrap-admin", "github:123"}, code: 2, want: "flag provided but not defined"},
		{name: "trailing argument", args: []string{"migrate", "unexpected"}, code: 2, want: "accepts flags only"},
		{name: "misplaced subcommand", args: []string{"--config", sqliteConfig, "migrate"}, code: 2, want: "use runnerd migrate"},
		{name: "invalid timeout", args: append([]string{"migrate", "--config", postgresConfig, "--source-stopped", "--timeout", "0s"}, paths...), code: 1, want: "positive --timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			args := append([]string{"-test.run=^TestMigrationCommandProcess$", "--"}, tc.args...)
			cmd := exec.CommandContext(ctx, os.Args[0], args...)
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "RUNNERD_MIGRATION_COMMAND_HELPER=1", "PPROF_PORT=0")
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			code := 0
			if err != nil {
				var exitErr *exec.ExitError
				if !errors.As(err, &exitErr) {
					t.Fatal(err)
				}
				code = exitErr.ExitCode()
			}
			if ctx.Err() != nil || code != tc.code || !strings.Contains(stderr.String(), tc.want) {
				t.Fatalf("exit = %d, context = %v, stderr = %q; want exit %d and %q", code, ctx.Err(), stderr.String(), tc.code, tc.want)
			}
			if stdout.Len() != 0 {
				t.Fatalf("unexpected report or service logs on stdout: %q", stdout.String())
			}
			for _, name := range []string{"must-not-create.db", "missing.db", "backup.db"} {
				if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
					t.Fatalf("unexpected database/backup %s: %v", name, err)
				}
			}
		})
	}
}
