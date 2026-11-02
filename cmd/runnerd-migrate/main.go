// runnerd-migrate transfers SQLite state to PostgreSQL with explicit cutover gates.
package main

import (
	"context"
	"encoding/json"
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

func main() {
	mode := flag.String("mode", "offline", "offline, prepare, sync, status, cutover, resume-source, or backfill")
	skipEvents := flag.Bool("skip-events", false, "sync requests and other tables without copying historical runner events")
	maxFinalChanges := flag.Int64("max-final-changes", 10000, "maximum pending request journal entries allowed at cutover")
	maxFinalRows := flag.Int64("max-final-rows", 100000, "maximum combined rows in the other tables at cutover")
	batchPause := flag.Duration("batch-pause", 10*time.Millisecond, "pause between committed copy batches; not used during cutover")
	configPath := flag.String("config", "", "protected target config with database.backend=postgres and database.dsn")
	source := flag.String("sqlite", "", "absolute path to SQLite source (retained and sealed after phased cutover)")
	backup := flag.String("backup", "", "new backup file path in an existing private directory")
	stopped := flag.Bool("source-stopped", false, "confirm all source writers and target services are stopped")
	timeout := flag.Duration("timeout", 30*time.Minute, "total migration deadline")
	flag.Parse()
	if *configPath == "" || *source == "" || *timeout <= 0 || *maxFinalChanges <= 0 || *maxFinalRows <= 0 || *batchPause < 0 || (*mode == "offline" && (!*stopped || *backup == "")) {
		fmt.Fprintln(os.Stderr, "require --config, --sqlite and a positive --timeout/--max-final-changes; offline also requires --backup and --source-stopped")
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
	options := state.OfflineMigrationOptions{SQLitePath: *source, BackupPath: *backup, PostgresDSN: cfg.StateDatabaseDSN.Value()}
	if *mode == "offline" {
		os.Exit(runMigration(ctx, options, os.Stdout, os.Stderr))
	}
	os.Exit(runPhasedMigration(ctx, state.PhasedMigrationOptions{OfflineMigrationOptions: options, Mode: *mode, SourceStopped: *stopped, SkipEvents: *skipEvents, MaxFinalChanges: *maxFinalChanges, MaxFinalRows: *maxFinalRows, BatchPause: *batchPause}, os.Stdout, os.Stderr))
}

func runPhasedMigration(ctx context.Context, options state.PhasedMigrationOptions, output, errorOutput io.Writer) int {
	progress := newMigrationProgressReporter(ctx, errorOutput, 10*time.Second)
	options.Progress = progress.update
	report, err := state.MigrateSQLiteToPostgresPhased(ctx, options)
	progress.finish(err == nil)
	if err != nil {
		fmt.Fprintln(errorOutput, err)
		return 1
	}
	if err := json.NewEncoder(output).Encode(report); err != nil {
		fmt.Fprintln(errorOutput, "phase may have committed but report could not be written; run --mode status before retrying or switching services")
		return 1
	}
	return 0
}

func runMigration(ctx context.Context, options state.OfflineMigrationOptions, output, errorOutput io.Writer) int {
	progress := newMigrationProgressReporter(ctx, errorOutput, 10*time.Second)
	options.Progress = progress.update
	report, err := state.MigrateSQLiteToPostgres(ctx, options)
	progress.finish(err == nil)
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
