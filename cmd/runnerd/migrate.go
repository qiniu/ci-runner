package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/state"
)

// runMigrate never initializes the ordinary Store or starts business services.
func runMigrate(args []string, output, errorOutput io.Writer) int {
	flags := flag.NewFlagSet("runnerd migrate", flag.ContinueOnError)
	flags.SetOutput(errorOutput)
	configPath := flags.String("config", "", "protected target config with database.backend=postgres and database.dsn")
	source := flags.String("sqlite", "", "absolute path to stopped SQLite source")
	backup := flags.String("backup", "", "new backup file path in an existing private directory")
	stopped := flags.Bool("source-stopped", false, "confirm all source writers and target services are stopped")
	timeout := flags.Duration("timeout", 30*time.Minute, "total migration deadline")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(errorOutput, "runnerd migrate accepts flags only; see runnerd migrate --help")
		return 2
	}
	if !*stopped || *configPath == "" || *source == "" || *backup == "" || *timeout <= 0 {
		fmt.Fprintln(errorOutput, "require --config, --sqlite, --backup, --source-stopped and a positive --timeout")
		return 1
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(errorOutput, err)
		return 1
	}
	if cfg.StateBackend != state.BackendPostgres {
		fmt.Fprintln(errorOutput, "require a valid standalone PostgreSQL target config")
		return 1
	}
	parent, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(parent, *timeout)
	defer cancel()
	report, err := state.MigrateSQLiteToPostgres(ctx, state.OfflineMigrationOptions{SQLitePath: *source, BackupPath: *backup, PostgresDSN: cfg.StateDatabaseDSN.Value()})
	if err != nil {
		fmt.Fprintln(errorOutput, err)
		return 1
	}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		fmt.Fprintln(errorOutput, "migration committed but report could not be written; inspect target before retrying")
		return 1
	}
	return 0
}
