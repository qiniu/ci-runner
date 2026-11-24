package state

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestExecutionUsage(t *testing.T) { verifyExecutionUsage(t, reportTestStore(t)) }

// Exercise the real admission and watch writes; the first running interval
// crosses midnight, and a second attempt on the same request remains active.
func verifyExecutionUsage(t *testing.T, s *DBStore) {
	t.Helper()
	db, err := s.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	day := reportTime("2026-03-08T05:00:00Z") // midnight in New York before the 23-hour day
	since := day.Add(-24 * time.Hour)
	must(db.Model(&executionCollection{}).Where("id = 1").UpdateColumn("started_at", since).Error)
	const repo = "execution/test"
	const id = "execution-usage-test"
	_, _, err = s.CreateRequest(RunnerRequest{ID: id, JobID: 820001, RepositoryFullName: repo, ProfileName: "linux", ProfileSource: "global"}, nil)
	must(err)
	read := func() RunnerState {
		st, err := s.ReadState(id)
		must(err)
		return st
	}
	// Other report fixtures may have queued records; use a dedicated native claim for this request.
	begin := func(attempt string, at time.Time) RunnerState {
		st := read()
		must(db.Model(&runnerRequestRecord{}).Where("id = ?", id).UpdateColumns(map[string]any{"lease_owner": "worker", "lease_expires_at": time.Now().Add(time.Hour)}).Error)
		st.LeaseOwner = "worker"
		st.ProvisionAttemptID = attempt
		st.WatchToken = attempt
		st.Status = StatusCreating
		st.CreatingAt = at.Add(-time.Minute)
		must(s.BeginRunnerCreation(st, time.Hour))
		st = read()
		st.Status = StatusRunning
		st.RunningAt = at
		st.SandboxConfigSource = "account"
		must(s.AdoptRunningWatch(st, attempt, time.Hour))
		return read()
	}
	first := begin("exec-test-first", day.Add(-time.Hour))
	stop := first
	stop.Status = StatusStopping
	stop.StoppingAt = day.Add(time.Hour)
	must(s.BeginRunnerCleanup(t.Context(), stop, first.WatchToken, "cleanup", time.Hour))
	next := read()
	next.Status = StatusQueued
	next.RunningAt = time.Time{}
	next.CreatingAt = time.Time{}
	next.StoppingAt = time.Time{}
	must(s.WriteCleanupState(next, "cleanup"))
	second := begin("exec-test-second", day.Add(time.Hour))
	if err := s.WriteWatchedState(first, first.WatchToken); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale watch: %v", err)
	}
	var attempts []executionAttempt
	must(db.Where("request_id = ?", id).Order("creating_at").Find(&attempts).Error)
	if len(attempts) != 2 || attempts[0].Status != "requeued" || attempts[0].EndedAt == nil || attempts[0].SandboxConfigSource != "account" || attempts[0].ProfileName != "linux" || attempts[1].AttemptID != second.ProvisionAttemptID {
		t.Fatalf("history: %+v", attempts)
	}

	end := day.Add(2 * time.Hour)
	obs := end.Add(time.Hour)
	capture := func(repo string, id int64, start, finish time.Time, source string) {
		must(s.RecordJobTimes(t.Context(), repo, id, JobTimeObservation{Start: start, End: finish, ObservedAt: obs, Source: source}))
	}
	capture("other/repo", 820001, day.Add(-5*time.Hour), end, "webhook")
	capture(repo, 820002, day.Add(-4*time.Hour), end, "webhook")
	capture(repo, 820001, end, day, "webhook") // inverted
	capture(repo, 820001, time.Time{}, end, "github_api")
	capture(repo, 820001, end.Add(time.Minute), time.Time{}, "webhook") // conflicts with stored end
	capture(repo, 820001, day.Add(-30*time.Minute), time.Time{}, "webhook")
	capture(repo, 820001, day, day.Add(time.Hour), "webhook") // replay with conflicting times
	var saved runnerRequestRecord
	must(db.First(&saved, "id = ?", id).Error)
	if saved.GitHubJobStartSource != "webhook" || saved.GitHubJobEndSource != "github_api" || saved.GitHubJobStartObservedAt == nil || !saved.GitHubJobStartObservedAt.Equal(obs) {
		t.Fatalf("Job time provenance lost")
	}
	q := RunnerReportQuery{IncludeExecution: true, From: "2026-03-07", To: "2026-03-10", Timezone: "America/New_York", Grain: "day", Repository: repo}
	now := day.Add(24 * time.Hour)
	report, err := s.ReadRunnerReport(t.Context(), q, now)
	must(err)
	total := report.Execution.Total
	if total.Attempts != 2 || total.Jobs != 1 || total.Peak != 1 || total.ProvisionalSeconds != 23*3600 || total.AttemptSeconds != 7200 || total.JobSeconds != 9000 {
		t.Fatalf("whole-period total: %+v", total)
	}
	b := report.Execution.Buckets
	if len(b) != 3 || b[0].AttemptSeconds != 3600 || b[0].JobSeconds != 1800 || b[1].AttemptSeconds != 3600 || b[1].ProvisionalSeconds != 22*3600 || b[1].JobSeconds != 7200 || b[1].Peak != 1 || b[1].Attempts != 2 || b[2].ProvisionalSeconds != 3600 {
		t.Fatalf("midnight/adjacent intervals: %+v", b)
	}
	// A concurrent recorded interval overlaps both attempts; an orphan open
	// interval from a retired writer must never extend to the observation time.
	for _, a := range []executionAttempt{
		{AttemptID: "exec-overlap", RequestID: id, RepositoryFullName: repo, RunningAt: reportPtr(day), StoppingAt: reportPtr(day.Add(2 * time.Hour)), CreatingAt: day, Status: StatusStopping},
		{AttemptID: "exec-ended", RequestID: id, RepositoryFullName: repo, RunningAt: reportPtr(day.Add(4 * time.Hour)), EndedAt: reportPtr(day.Add(4*time.Hour + 30*time.Minute)), CreatingAt: day, Status: StatusCompleted},
		{AttemptID: "exec-orphan", RequestID: id, RepositoryFullName: repo, RunningAt: reportPtr(day), CreatingAt: day, Status: StatusRunning},
	} {
		must(db.Create(&a).Error)
	}
	q.From = "2026-03-08"
	q.To = "2026-03-09"
	report, err = s.ReadRunnerReport(t.Context(), q, now)
	must(err)
	b = report.Execution.Buckets
	if len(b) != 1 || b[0].Peak != 2 || b[0].AttemptSeconds != 12600 || b[0].ActiveAttempts != 1 || b[0].JobSeconds != 7200 {
		t.Fatalf("overlap: %+v", b)
	}
	// A second pool initializes against the same database. Concurrent writers
	// fill opposite ends without replacing either source or advancing the request.
	peer := NewWithOptions(s.opts).(*DBStore)
	peerDB, err := peer.dbOrEnsure()
	must(err)
	defer closeTestDB(t, peerDB)
	_, created, err := s.CreateRequest(RunnerRequest{ID: "exec-job-race", JobID: 820002, RepositoryFullName: repo}, nil)
	must(err)
	starts := make(chan struct{})
	results := make(chan error, 2)
	for _, write := range []struct {
		store *DBStore
		obs   JobTimeObservation
	}{
		{s, JobTimeObservation{Start: day, Source: "webhook", ObservedAt: obs}},
		{peer, JobTimeObservation{End: day, Source: "github_api", ObservedAt: obs}},
	} {
		go func() { <-starts; results <- write.store.RecordJobTimes(t.Context(), repo, 820002, write.obs) }()
	}
	close(starts)
	for range 2 {
		must(<-results)
	}
	observed, err := peer.ReadState(created.ID)
	must(err)
	if observed.Version != created.Version || observed.GitHubJobTiming == nil || observed.GitHubJobTiming.StartSource != "webhook" || observed.GitHubJobTiming.EndSource != "github_api" {
		t.Fatal("concurrent evidence lost")
	}
	_, _, err = s.CreateRequest(RunnerRequest{ID: "exec-incomplete", JobID: 820003, RepositoryFullName: repo}, nil)
	must(err)
	capture(repo, 820003, day, time.Time{}, "webhook")
	report, err = peer.ReadRunnerReport(t.Context(), q, now)
	must(err)
	if !report.Execution.CollectionStartedAt.Equal(since) || report.Execution.Buckets[0].Jobs != 2 || report.Execution.Buckets[0].JobSeconds != 7200 || report.Execution.IncompleteJobs != 1 {
		t.Fatalf("restart, zero duration or coverage: %+v", report.Execution)
	}
	q.From = "2026-03-01"
	q.To = "2026-03-02"
	report, err = s.ReadRunnerReport(t.Context(), q, now)
	must(err)
	if len(report.Execution.Buckets) != 0 || !report.Execution.CollectionStartedAt.Equal(since) {
		t.Fatal("fabricated history before collection")
	}
}

func TestSQLiteExecutionOverlapPlan(t *testing.T) {
	db, err := reportTestStore(t).dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i<100000)
		INSERT INTO runner_execution_attempts (attempt_id,request_id,status,running_at,stopping_at,ended_at)
		SELECT CAST(i AS TEXT),CAST(i AS TEXT),'completed','2025-01-01','2025-01-02','2025-01-03' FROM n`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ANALYZE runner_execution_attempts").Error; err != nil {
		t.Fatal(err)
	}
	from := reportTime("2026-01-01T00:00:00Z")
	query, err := executionIntervals(db, RunnerReportQuery{}, from, from.Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var plan []struct{ Detail string }
	if err := db.Raw("EXPLAIN QUERY PLAN ?", query).Scan(&plan).Error; err != nil {
		t.Fatal(err)
	}
	var seeks int
	for _, step := range plan {
		if strings.Contains(step.Detail, "SEARCH r USING INDEX idx_execution_interval_end") {
			seeks++
		}
	}
	if seeks != 3 {
		t.Fatalf("expected three indexed end-time seeks: %+v", plan)
	}
	if err := scanExecutionIntervals(db, query, func(time.Time, *time.Time, *time.Time) { t.Fatal("historical interval entered empty window") }); err != nil {
		t.Fatal(err)
	}
}
