package state

import (
	"testing"
	"time"
)

func TestRunnerReportEfficiency(t *testing.T) { verifyEfficiency(t, reportTestStore(t)) }

func verifyEfficiency(t *testing.T, s *DBStore) {
	t.Helper()
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	now := reportTime("2026-03-03T12:00:00Z")
	queue := now.Add(-2 * time.Hour)
	create := queue.Add(time.Minute)
	run := create.Add(time.Minute)
	stop := now.Add(-time.Minute)
	missingStart := reportPtr(time.Time{})
	if db.Dialector.Name() == BackendMySQL {
		// Strict MySQL DATETIME cannot store Go year one. NULL represents missing evidence there.
		missingStart = nil
	}
	for _, item := range []struct {
		id, status, stage, reason string
		running, stopping, ended  *time.Time
	}{
		{"e1", StatusFailed, "admission", "profile_labels_not_matched", nil, nil, &now},
		{"e2", StatusFailed, "runner", "exit", &run, &stop, &now},
		{"e3", StatusFailed, "", "", nil, nil, &now},
		{"e4", StatusCompleted, "", "", &create, &now, &now},
		{"e5", StatusCompleted, "", "", &queue, &now, &stop},
		{"e6", StatusCompleted, "", "", missingStart, nil, nil},
	} {
		r := runnerRequestRecord{ID: item.id, RunnerName: item.id, Source: "fixture", RepositoryFullName: "fixture/efficiency", LabelsJSON: "[]", Status: item.status, FailureStage: item.stage, FailureReason: item.reason, QueuedAt: queue, CreatingAt: &create, RunningAt: item.running, StoppingAt: item.stopping, UpdatedAt: now}
		if item.status == StatusFailed {
			r.FailedAt = item.ended
		} else {
			r.CompletedAt = item.ended
		}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	q := RunnerReportQuery{IncludeEfficiency: true, From: "2026-03-03", To: "2026-03-04", Timezone: "UTC", Grain: "day", Repository: "fixture/efficiency"}
	r, err := s.ReadRunnerReport(t.Context(), q, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Totals.Startup.Samples != 3 || r.Totals.Creation.Samples != 2 || r.Totals.Cleanup.Samples != 2 || r.Totals.Cleanup.Eligible != 3 || *r.Efficiency.StartupP95 != 120 || *r.Efficiency.CreationP95 != 60 || *r.Efficiency.CleanupP95 != 60 {
		t.Fatalf("invalid durations %+v %+v", r.Totals, r.Efficiency)
	}
	for _, g := range r.Efficiency.Groups {
		rows, err := r.Requests(g.Dimension, g.Key)
		if err != nil || int64(len(rows)) != g.Count {
			t.Fatalf("group %+v: %d %v", g, len(rows), err)
		}
	}
	if r.Efficiency.Rejections["profile_labels_not_matched"]["2026-03-03"] != 1 {
		t.Fatal("missing rejection trend")
	}
	for dim, key := range map[string]string{"admission_bucket_reason": "2026-03-03:profile_labels_not_matched", "admission_bucket": "2026-03-03", "admission_reason": "profile_labels_not_matched", "before_start_stage": "", "started_reason": "exit"} {
		rows, err := r.Requests(dim, key)
		if err != nil || len(rows) != 1 {
			t.Fatalf("%s %q: %d %v", dim, key, len(rows), err)
		}
	}
	samples, err := r.Requests("creation", "")
	if err != nil || len(samples) != 2 {
		t.Fatalf("creation drill: %v %v", samples, err)
	}
	old := queue.AddDate(0, -1, 0)
	recent := now.Add(-time.Minute)
	for _, item := range []struct {
		id    string
		since *time.Time
	}{{"old", &old}, {"missing", nil}, {"recent", &recent}} {
		r := runnerRequestRecord{ID: "backlog-" + item.id, RunnerName: "backlog-" + item.id, Source: "fixture", LabelsJSON: "[]", RepositoryFullName: q.Repository, Status: StatusStopping, QueuedAt: old, StoppingAt: item.since, UpdatedAt: recent}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows, more, err := s.ReadReportBacklog(t.Context(), q, StatusStopping, 30, now)
	if err != nil || more || len(rows) != 2 {
		t.Fatalf("backlog: %+v %t %v", rows, more, err)
	}
	for _, row := range rows {
		if row.ID == "backlog-old" && (row.Seconds == nil || *row.Seconds != now.Sub(old).Seconds()) || row.ID == "backlog-missing" && row.Seconds != nil || row.ID == "backlog-recent" {
			t.Fatalf("unexpected state age: %+v", row)
		}
	}
}

func TestRunnerReportQueueAgeAfterRetry(t *testing.T) {
	verifyQueueAgeAfterRetry(t, reportTestStore(t))
}

func verifyQueueAgeAfterRetry(t *testing.T, s *DBStore) {
	t.Helper()
	const repo = "fixture/queue-age"
	const id = "queue-age"
	first := time.Now().UTC().Truncate(time.Second).Add(-24 * time.Hour)
	if _, _, err := s.CreateRequest(RunnerRequest{ID: id, Source: "fixture", RepositoryFullName: repo, CreatedAt: first}, nil); err != nil {
		t.Fatal(err)
	}
	check := func(at time.Time, wantSince *time.Time) {
		t.Helper()
		rows, more, err := s.ReadReportBacklog(t.Context(), RunnerReportQuery{Repository: repo}, StatusQueued, 30, at)
		if err != nil || more {
			t.Fatalf("backlog: %v, more=%v", err, more)
		}
		if wantSince == nil {
			if len(rows) != 0 {
				t.Fatalf("recently requeued request counted at %v: %+v", at, rows)
			}
			return
		}
		if len(rows) != 1 || rows[0].ID != id || rows[0].Since == nil || !rows[0].Since.Equal(*wantSince) || rows[0].Seconds == nil || *rows[0].Seconds != at.Sub(*wantSince).Seconds() {
			t.Fatalf("backlog at %v: got %+v, want queue entry at %v", at, rows, wantSince)
		}
	}
	check(first.Add(30*time.Minute), &first)
	writeStatus := func(status string) {
		t.Helper()
		st, err := s.ReadState(id)
		if err != nil {
			t.Fatal(err)
		}
		st.Status = status
		if err := s.WriteState(st); err != nil {
			t.Fatal(err)
		}
	}
	writeStatus(StatusFailed)
	retry := first.Add(23 * time.Hour)
	if _, err := s.RetryRequest(id, retry); err != nil {
		t.Fatal(err)
	}
	// Manual acceleration of an already-queued retry removes the backoff
	// without starting a new wait.
	queued, err := s.ReadState(id)
	if err != nil {
		t.Fatal(err)
	}
	queued.LastErrorRetryable = true
	queued.ScheduleQueuedRetry(time.Hour)
	if err := s.WriteState(queued); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryRequest(id, retry.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	check(retry.Add(30*time.Minute-time.Second), nil)
	check(retry.Add(30*time.Minute), &retry)
	writeStatus(StatusCreating)
	writeStatus(StatusQueued) // Automatic requeue through the shared state writer.
	st, err := s.ReadState(id)
	if err != nil {
		t.Fatal(err)
	}
	check(st.UpdatedAt.Add(time.Minute), nil)
	check(st.UpdatedAt.Add(30*time.Minute), &st.UpdatedAt)
	if !st.CreatedAt.Equal(first) {
		t.Fatalf("historical queued_at changed: %v", st.CreatedAt)
	}
}
