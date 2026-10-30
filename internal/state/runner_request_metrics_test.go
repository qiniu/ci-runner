package state

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func assertRunnerRequestMetricCounts(t *testing.T, store *DBStore, specs int64) {
	t.Helper()
	empty, err := store.GetRunnerRequestMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if empty != (RunnerRequestMetrics{RunnerSpecs: specs}) {
		t.Fatalf("empty request metrics = %#v", empty)
	}
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		status        string
		stage, reason any
	}{
		{StatusQueued, nil, nil},
		{StatusCreating, nil, nil},
		{StatusRunning, nil, nil},
		{StatusStopping, nil, nil},
		{StatusCompleted, "admission", "profile_labels_not_matched"},
		{StatusFailed, "admission", "profile_labels_not_matched"},
		{StatusFailed, nil, nil},
		{StatusFailed, "admission", nil},
		{StatusFailed, nil, "profile_labels_not_matched"},
		{StatusFailed, "runner", "profile_labels_not_matched"},
		{StatusFailed, "admission", "other"},
		{StatusFailed, "", ""},
		{"historical_status", nil, nil},
		{StatusFailed, "runner", strings.Repeat("historical-", 100)},
	}
	now := time.Now().UTC()
	for i, c := range cases {
		err := db.Model(&runnerRequestRecord{}).Create(map[string]any{
			"id": fmt.Sprintf("metrics-fixture-%d", i), "source": "test", "labels_json": "[]",
			"runner_name": fmt.Sprintf("metrics-fixture-%d", i), "status": c.status,
			"failure_stage": c.stage, "failure_reason": c.reason,
			"queued_at": now, "updated_at": now, "github_context_backfilled": true,
		}).Error
		if err != nil {
			t.Fatal(err)
		}
	}
	got, err := store.GetRunnerRequestMetrics(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := RunnerRequestMetrics{Queued: 1, Creating: 1, Running: 1, Stopping: 1, Completed: 1, Failed: 7, Unmatched: 1, RunnerSpecs: specs}
	if got != want {
		t.Fatalf("metrics including historical NULLs = %#v, want %#v", got, want)
	}
	// Counters must reflect later state changes, not the fixture's initial snapshot.
	if err := db.Model(&runnerRequestRecord{}).Where("status = ?", StatusQueued).Update("status", StatusCompleted).Error; err != nil {
		t.Fatal(err)
	}
	want.Queued = 0
	want.Completed = 2
	got, err = store.GetRunnerRequestMetrics(context.Background())
	if err != nil || got != want {
		t.Fatalf("metrics after state change = %#v, err=%v, want %#v", got, err, want)
	}
}

type metricsSQLCapture struct {
	logger.Interface
	statements []string
}

func (l *metricsSQLCapture) Trace(_ context.Context, _ time.Time, fc func() (string, int64), _ error) {
	sql, _ := fc()
	l.statements = append(l.statements, sql)
}

func TestRunnerRequestMetricsSQLiteCoveringIndex(t *testing.T) {
	store := New(t.TempDir()).(*DBStore)
	db, err := store.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	capture := &metricsSQLCapture{Interface: logger.Default.LogMode(logger.Silent)}
	store.db = db.Session(&gorm.Session{Logger: capture})
	assertRunnerRequestMetricCounts(t, store, 0)
	var query string
	for _, sql := range capture.statements {
		if strings.Contains(sql, "runner_requests") && strings.HasPrefix(sql, "SELECT") {
			query = sql
		}
	}
	if query == "" {
		t.Fatal("did not capture the metrics request aggregation")
	}
	var plan []struct{ Detail string }
	if err := db.Raw("EXPLAIN QUERY PLAN " + query).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	for _, entry := range plan {
		if strings.Contains(entry.Detail, "USING COVERING INDEX idx_runner_requests_metrics") {
			return
		}
	}
	t.Fatalf("metrics must avoid reading wide request rows; plan=%#v", plan)
}
