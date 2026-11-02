package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

// Keep reporting even while a single SQLite operation or network call blocks.
// The worker only publishes credential-free stage names and completed row counts.
type migrationProgressReporter struct {
	mu           sync.Mutex
	output       io.Writer
	started      time.Time
	stageStarted time.Time
	lastAdvance  time.Time
	deadline     time.Time
	current      state.OfflineMigrationProgress
	stop         chan struct{}
	done         chan struct{}
}

func newMigrationProgressReporter(ctx context.Context, output io.Writer, interval time.Duration) *migrationProgressReporter {
	now := time.Now()
	deadline, _ := ctx.Deadline()
	p := &migrationProgressReporter{output: output, started: now, deadline: deadline, stop: make(chan struct{}), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stop:
				return
			case <-ticker.C:
				p.mu.Lock()
				if p.current.Stage != "" {
					status := "running"
					if ctx.Err() != nil {
						status = "cancelling"
					}
					p.write(status, time.Now())
				}
				p.mu.Unlock()
			}
		}
	}()
	return p
}

func (p *migrationProgressReporter) update(next state.OfflineMigrationProgress) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	if next.Stage != p.current.Stage {
		if p.current.Stage != "" {
			p.write("finished", now)
		}
		p.current = next
		p.stageStarted, p.lastAdvance = now, now
		p.write("started", now)
		return
	}
	if next.Rows != p.current.Rows {
		p.lastAdvance = now
	}
	p.current = next
}

func (p *migrationProgressReporter) finish(success bool) {
	close(p.stop)
	<-p.done
	p.mu.Lock()
	defer p.mu.Unlock()
	status := "failed"
	if success {
		status = "succeeded"
	}
	p.write(status, time.Now())
}

func (p *migrationProgressReporter) write(status string, now time.Time) {
	remaining := max(time.Duration(0), p.deadline.Sub(now))
	stageElapsed := now.Sub(p.stageStarted)
	var rate float64
	if stageElapsed > 0 {
		rate = float64(p.current.Rows) / stageElapsed.Seconds()
	}
	fmt.Fprintf(p.output, "%s status=%s stage=%q rows=%d rows_per_sec=%.1f stage_elapsed=%s total_elapsed=%s remaining=%s since_progress=%s\n",
		now.Format(time.RFC3339), status, p.current.Stage, p.current.Rows, rate,
		stageElapsed.Round(time.Second), now.Sub(p.started).Round(time.Second), remaining.Round(time.Second), now.Sub(p.lastAdvance).Round(time.Second))
}
