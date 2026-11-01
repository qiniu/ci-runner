// runnerd-migrate copies a stopped SQLite database into an empty PostgreSQL schema.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/state"
)

func main() {
	configPath := flag.String("config", "", "protected target config with database.backend=postgres and database.dsn")
	source := flag.String("sqlite", "", "absolute path to stopped SQLite source")
	backup := flag.String("backup", "", "new backup file path in an existing private directory")
	stopped := flag.Bool("source-stopped", false, "confirm all source writers and target services are stopped")
	timeout := flag.Duration("timeout", 30*time.Minute, "total migration deadline")
	flag.Parse()
	if !*stopped || *configPath == "" || *source == "" || *backup == "" || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "require --config, --sqlite, --backup, --source-stopped and a positive --timeout")
		os.Exit(1)
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if cfg.StateBackend != state.BackendPostgres {
		fmt.Fprintln(os.Stderr, "require a valid standalone PostgreSQL target config")
		os.Exit(1)
	}
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	report, err := state.MigrateSQLiteToPostgres(ctx, state.OfflineMigrationOptions{SQLitePath: *source, BackupPath: *backup, PostgresDSN: cfg.StateDatabaseDSN.Value()})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(report); err != nil {
		fmt.Fprintln(os.Stderr, "migration committed but report could not be written; inspect target before retrying")
		os.Exit(1)
	}
}
