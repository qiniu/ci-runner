package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

type progressTestOutput struct {
	bytes.Buffer
	lines chan string
}

func (w *progressTestOutput) Write(data []byte) (int, error) {
	n, err := w.Buffer.Write(data)
	select {
	case w.lines <- string(data):
	default:
	}
	return n, err
}

func TestMigrationProgressReportsBlockedStageAndFinalRows(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	output := &progressTestOutput{lines: make(chan string, 32)}
	p := newMigrationProgressReporter(ctx, output, time.Millisecond)
	p.update(state.OfflineMigrationProgress{Stage: "copy table runner_events"})
	p.update(state.OfflineMigrationProgress{Stage: "copy table runner_events", Rows: 256})
	// No subsequent worker callbacks: the independent heartbeat must still run.
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
wait:
	for {
		select {
		case line := <-output.lines:
			if strings.Contains(line, "status=running") && strings.Contains(line, "rows=256") {
				break wait
			}
		case <-deadline.C:
			p.finish(false)
			t.Fatal("no heartbeat while the worker was blocked")
		}
	}
	p.update(state.OfflineMigrationProgress{Stage: "verify table runner_events"})
	p.update(state.OfflineMigrationProgress{Stage: "verify table runner_events", Rows: 128})
	cancel()
	// The reporter stays alive during cancellation/rollback until finish joins it.
	p.finish(false)
	text := output.String()
	for _, want := range []string{
		`status=finished stage="copy table runner_events" rows=256`,
		`status=started stage="verify table runner_events" rows=0`,
		`status=failed stage="verify table runner_events" rows=128`,
		"rows_per_sec=", "remaining=", "since_progress=", "total_elapsed=",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in progress output: %s", want, text)
		}
	}
	if strings.Contains(text, "status=succeeded") {
		t.Fatal("failure reported as success")
	}
}

func TestRunMigrationKeepsFailedStdoutEmptyAndRedacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var output, errorOutput bytes.Buffer
	code := runMigration(ctx, state.OfflineMigrationOptions{SQLitePath: "/missing/private-source", BackupPath: "/private-backup", PostgresDSN: "password=private-password"}, &output, &errorOutput)
	if code != 1 || output.Len() != 0 || !strings.Contains(errorOutput.String(), "status=failed") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, output.String(), errorOutput.String())
	}
	if strings.Contains(errorOutput.String(), "private-") {
		t.Fatal("configuration or paths leaked to stderr")
	}
}

func TestRunPhasedMigrationKeepsFailedStdoutEmptyAndRedacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var output, errorOutput bytes.Buffer
	code := runPhasedMigration(ctx, state.PhasedMigrationOptions{Mode: "status", OfflineMigrationOptions: state.OfflineMigrationOptions{SQLitePath: "/missing/private-source", PostgresDSN: "password=private-password"}}, &output, &errorOutput)
	if code != 1 || output.Len() != 0 || !strings.Contains(errorOutput.String(), "status=failed") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, output.String(), errorOutput.String())
	}
	if strings.Contains(errorOutput.String(), "private-") {
		t.Fatal("phased migration leaked private configuration")
	}
}
