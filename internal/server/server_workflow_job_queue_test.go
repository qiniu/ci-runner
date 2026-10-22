package server

import (
	"bytes"
	"context"
	"errors"
	"expvar"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func sendQueueTestWebhook(t *testing.T, srv *Server, event, action string) *httptest.ResponseRecorder {
	t.Helper()
	payload := []byte(fmt.Sprintf(`{"action":%q,"repository":{"full_name":"o/r"},"workflow_job":{"id":1001,"labels":["self-hosted","e2b"]},"workflow_run":{"id":10}}`, action))
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(payload))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", "test-delivery")
	req.Header.Set("X-Hub-Signature-256", sign("secret", payload))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("webhook status=%d body=%s", rec.Code, rec.Body.String())
	}
	return rec
}

func queueTestState(t *testing.T, store state.Store) state.RunnerState {
	t.Helper()
	st, err := store.ReadState("1001")
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// Check the incident's complete event sequence and delayed polling with the
// real background worker, admission, REST client and fake Sandbox boundary.
func TestWorkflowJobWaitingResumesExactlyOnce(t *testing.T) {
	for _, wake := range []bool{true, false} {
		t.Run(fmt.Sprintf("queued_webhook_%v", wake), func(t *testing.T) {
			var ready atomic.Bool
			var lookups atomic.Int32
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/repos/o/r/actions/jobs/1001":
					lookups.Add(1)
					status := "waiting"
					if ready.Load() {
						status = "queued"
					}
					fmt.Fprintf(w, `{"id":1001,"status":%q}`, status)
				case "/repos/o/r/actions/runs/10/jobs":
					fmt.Fprint(w, `{"total_count":1,"jobs":[{"id":1001,"status":"queued","labels":["self-hosted","e2b"]}]}`)
				case "/repos/o/r/actions/runners/downloads":
					writeRunnerApplicationsResponse(w)
				case "/repos/o/r/actions/runners/registration-token":
					fmt.Fprint(w, `{"token":"test-token"}`)
				default:
					http.NotFound(w, r)
				}
			}))
			defer gh.Close()
			store := state.New(t.TempDir())
			fake := &fakeSandbox{}
			srv := newTestServer(t, store, gh.URL, fake)
			sendQueueTestWebhook(t, srv, "workflow_job", "queued")
			deadline := time.Now().Add(3 * time.Second)
			for {
				st := queueTestState(t, store)
				if st.Status == state.StatusQueued && !st.NextRetryAt.IsZero() {
					if st.LeaseOwner != "" || !st.LeaseExpiresAt.IsZero() || !st.CompletedAt.IsZero() || fake.startedCount() != 0 {
						t.Fatalf("waiting must release lease without starting or completing: %#v", st)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("waiting not deferred: %#v", st)
				}
				time.Sleep(time.Millisecond)
			}
			sendQueueTestWebhook(t, srv, "workflow_job", "waiting")
			ready.Store(true)
			if wake {
				var wg sync.WaitGroup
				for i := 0; i < 12; i++ {
					wg.Add(1)
					go func(i int) {
						defer wg.Done()
						if i%2 == 0 {
							sendQueueTestWebhook(t, srv, "workflow_run", "in_progress")
						} else {
							sendQueueTestWebhook(t, srv, "workflow_job", "queued")
						}
					}(i)
				}
				wg.Wait()
			}
			waitForState(t, store, "1001", state.StatusRunning)
			sendQueueTestWebhook(t, srv, "workflow_job", "queued")
			if fake.startedCount() != 1 || lookups.Load() < 2 {
				t.Fatalf("starts=%d lookups=%d", fake.startedCount(), lookups.Load())
			}
		})
	}
}

func TestWorkflowJobWaitingBackoffAndTerminalTransitions(t *testing.T) {
	for _, next := range []string{"queued", "completed", "in_progress", "pending", "requested", "unknown", "lookup_error"} {
		t.Run(next, func(t *testing.T) {
			var observed atomic.Value
			observed.Store("waiting")
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				status := observed.Load().(string)
				if status == "lookup_error" {
					http.Error(w, "private upstream body", http.StatusServiceUnavailable)
					return
				}
				conclusion := ""
				if status == "completed" {
					conclusion = "cancelled"
				}
				fmt.Fprintf(w, `{"id":1001,"status":%q,"conclusion":%q,"runner_name":"other-runner"}`, status, conclusion)
			}))
			defer gh.Close()
			store := state.New(t.TempDir())
			srv := newTestServer(t, store, gh.URL, &fakeSandbox{})
			srv.Close()
			profileName := "waiting-metrics-" + next
			profile, err := store.GetProfile("default")
			if err != nil {
				t.Fatal(err)
			}
			profile.Name = profileName
			if _, err := store.UpsertProfile(profile); err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.CreateRequest(state.RunnerRequest{
				ID: "1001", Source: "github_webhook", JobID: 1001, RepositoryFullName: "o/r",
				RequestedLabels: []string{"self-hosted", "e2b"}, Labels: []string{"self-hosted", "e2b"},
				ProfileName: profileName, RunnerName: "e2b-1001", CreatedAt: time.Now().Add(-time.Hour),
			}, nil); err != nil {
				t.Fatal(err)
			}
			sendQueueTestWebhook(t, srv, "workflow_job", "queued")
			req, _ := store.ReadRequest("1001")
			metricCount := func(name, key string) int64 {
				t.Helper()
				metric := expvar.Get(name).(*expvar.Map).Get(key)
				if metric == nil {
					return 0
				}
				return metric.(*expvar.Int).Value()
			}
			failureReason := "github_job_status_unknown"
			if next == "lookup_error" {
				failureReason = "github_job_status_unavailable"
			}
			createKey := profileName + "|create|failed"
			workflowKey := "o/r|unknown|e2b-1001|" + profileName + "|github_job_status|" + failureReason
			createsBefore := metricCount("e2b_runner_operations_total", createKey)
			failuresBefore := metricCount("github_workflow_job_failures_total", workflowKey)
			// An earlier provisioning failure must keep its retry budget through
			// waiting and the later successful queued observation.
			initial := queueTestState(t, store)
			initial.RetryCount = 1
			if err := store.WriteState(initial); err != nil {
				t.Fatal(err)
			}
			check := func() bool {
				st := queueTestState(t, store)
				st.Status, st.LeaseOwner = state.StatusCreating, "test-worker"
				st.LeaseExpiresAt = time.Now().Add(time.Minute)
				if err := store.WriteState(st); err != nil {
					t.Fatal(err)
				}
				start, err := srv.ensureWorkflowJobStillQueued(context.Background(), req, "test-worker")
				if err != nil {
					t.Fatal(err)
				}
				return start
			}
			for i := 0; i < 10; i++ {
				before := time.Now()
				if check() {
					t.Fatal("waiting must not start")
				}
				st := queueTestState(t, store)
				if st.RetryCount != 1 || st.Status != state.StatusQueued || st.LeaseOwner != "" || !st.LeaseExpiresAt.IsZero() || st.NextRetryAt.Before(before) || st.NextRetryAt.After(time.Now().Add(srv.cfg.RetryMaxDelay)) || !st.CompletedAt.IsZero() {
					t.Fatalf("invalid wait: %#v", st)
				}
				if n, _ := store.InFlightCount(); n != 0 {
					t.Fatalf("waiting consumes capacity: %d", n)
				}
				if _, _, claimed, err := store.ClaimNextRunnable("other", before, time.Second); err != nil || claimed {
					t.Fatalf("claimed before delay: %v %v", claimed, err)
				}
			}
			capStart := time.Now()
			check()
			if st := queueTestState(t, store); st.NextRetryAt.Before(capStart.Add(srv.cfg.RetryMaxDelay)) {
				t.Fatalf("long waits must reach the capped delay: %#v", st)
			}
			// Restart retains the delay and wait classification.
			before := queueTestState(t, store)
			if err := srv.recoverRunner(context.Background(), "1001"); err != nil {
				t.Fatal(err)
			}
			if st := queueTestState(t, store); st.NextRetryAt != before.NextRetryAt || st.LastErrorCode != workflowJobWaitCode {
				t.Fatalf("restart lost scheduled wait: %#v", st)
			}
			observed.Store(next)
			start := check()
			st := queueTestState(t, store)
			switch next {
			case "queued":
				if !start || st.RetryCount != 1 || st.LastErrorCode != "" || !st.NextRetryAt.IsZero() {
					t.Fatalf("queued did not resume: %#v", st)
				}
			case "completed":
				if start || st.Status != state.StatusCompleted || st.GitHubJobConclusion != "cancelled" || st.GitHubJobObservedAt.IsZero() || !st.StoppingAt.IsZero() {
					t.Fatalf("lost terminal evidence: %#v", st)
				}
			default:
				if start || st.Status != state.StatusQueued || !st.CompletedAt.IsZero() {
					t.Fatalf("nonterminal falsely completed: %#v", st)
				}
			}
			if next == "unknown" || next == "lookup_error" {
				for i := 0; i < srv.cfg.RetryMaxAttempts && queueTestState(t, store).Status != state.StatusFailed; i++ {
					check()
				}
				if st = queueTestState(t, store); st.Status != state.StatusFailed || st.FailureStage != "github_job_status" || !st.NextRetryAt.IsZero() || strings.Contains(st.Error, "private upstream") {
					t.Fatalf("unbounded or leaking lookup failure: %#v", st)
				}
				if start, err := srv.ensureWorkflowJobStillQueued(context.Background(), req, "test-worker"); err != nil || start {
					t.Fatalf("terminal request must ignore a repeated lookup: start=%v err=%v", start, err)
				}
			}
			wantFailures := int64(0)
			if next == "unknown" || next == "lookup_error" {
				wantFailures = 1
			}
			if got := metricCount("e2b_runner_operations_total", createKey) - createsBefore; got != wantFailures {
				t.Errorf("failed create count=%d want=%d", got, wantFailures)
			}
			if got := metricCount("github_workflow_job_failures_total", workflowKey) - failuresBefore; got != wantFailures {
				t.Errorf("workflow failure count=%d want=%d", got, wantFailures)
			}
		})
	}
}

func TestWorkflowJobWaitingRateLimitDeferral(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusForbidden} {
		for _, exhausted := range []bool{false, true} {
			t.Run(fmt.Sprintf("status_%d/exhausted_%v", status, exhausted), func(t *testing.T) {
				var ready atomic.Bool
				gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if ready.Load() {
						fmt.Fprint(w, `{"id":1001,"status":"queued"}`)
						return
					}
					message := "private upstream body"
					if status == http.StatusForbidden {
						message += ": You have exceeded a secondary rate limit"
					}
					http.Error(w, message, status)
				}))
				defer gh.Close()
				store := state.New(t.TempDir())
				fake := &fakeSandbox{}
				srv := newTestServer(t, store, gh.URL, fake)
				srv.Close()
				sendQueueTestWebhook(t, srv, "workflow_job", "queued")
				req, err := store.ReadRequest("1001")
				if err != nil {
					t.Fatal(err)
				}
				if exhausted {
					st := queueTestState(t, store)
					st.RetryCount = srv.cfg.RetryMaxAttempts
					if err := store.WriteState(st); err != nil {
						t.Fatal(err)
					}
				}
				check := func() bool {
					st := queueTestState(t, store)
					st.Status, st.LeaseOwner = state.StatusCreating, "test-worker"
					st.LeaseExpiresAt = time.Now().Add(time.Minute)
					if err := store.WriteState(st); err != nil {
						t.Fatal(err)
					}
					start, err := srv.ensureWorkflowJobStillQueued(context.Background(), req, "test-worker")
					if err != nil {
						t.Fatal(err)
					}
					return start
				}
				for i := 0; i < srv.cfg.RetryMaxAttempts+2; i++ {
					before := time.Now()
					if check() {
						t.Fatal("rate-limited lookup must not start a Runner")
					}
					st := queueTestState(t, store)
					if st.Status != state.StatusQueued || st.LastErrorCode != "github_job_status_rate_limited" || !st.LastErrorRetryable || st.RetryCount > srv.cfg.RetryMaxAttempts || st.NextRetryAt.Before(before) || st.NextRetryAt.After(time.Now().Add(srv.cfg.RetryMaxDelay)) {
						t.Fatalf("rate limit must remain deferred: %#v", st)
					}
					if st.LeaseOwner != "" || !st.LeaseExpiresAt.IsZero() || !st.CreatingAt.IsZero() || !st.CompletedAt.IsZero() || !st.FailedAt.IsZero() {
						t.Fatalf("rate limit retained creation or terminal state: %#v", st)
					}
					if n, err := store.InFlightCount(); err != nil || n != 0 || fake.startedCount() != 0 {
						t.Fatalf("rate limit consumed Runner capacity: count=%d err=%v starts=%d", n, err, fake.startedCount())
					}
				}
				before := queueTestState(t, store)
				if before.RetryCount != srv.cfg.RetryMaxAttempts {
					t.Fatalf("retry count must saturate: %#v", before)
				}
				if err := srv.recoverRunner(context.Background(), "1001"); err != nil {
					t.Fatal(err)
				}
				if st := queueTestState(t, store); st.NextRetryAt != before.NextRetryAt || st.LastErrorCode != before.LastErrorCode {
					t.Fatalf("restart lost rate-limit deferral: %#v", st)
				}
				ready.Store(true)
				if !check() {
					t.Fatal("queued Job must resume after rate limit clears without a webhook")
				}
				if st := queueTestState(t, store); st.LastErrorCode != "" || st.LastErrorMessage != "" || st.LastErrorRetryable || !st.NextRetryAt.IsZero() || st.RetryCount != before.RetryCount {
					t.Fatalf("queued observation did not clear rate-limit metadata: %#v", st)
				}
				control, err := store.ReadLog("1001", "control.log", 0)
				if err != nil || strings.Contains(string(control), "private upstream body") {
					t.Fatalf("lookup response leaked into control evidence: err=%v log=%s", err, control)
				}
			})
		}
	}
}

type waitingMetricsCountingStore struct {
	state.Store
	profileScans atomic.Int32
	activeScans  atomic.Int32
	failProfiles atomic.Bool
}

func (s *waitingMetricsCountingStore) ListProfiles() ([]state.RunnerProfile, error) {
	s.profileScans.Add(1)
	if s.failProfiles.Load() {
		return nil, errors.New("profile metrics lookup failed")
	}
	return s.Store.ListProfiles()
}

func (s *waitingMetricsCountingStore) ListActiveStates() ([]state.RunnerState, error) {
	s.activeScans.Add(1)
	return s.Store.ListActiveStates()
}

func TestWorkflowJobWaitingMetricsCoalesced(t *testing.T) {
	var terminal atomic.Bool
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if terminal.Load() {
			fmt.Fprint(w, `{"id":1001,"status":"unknown"}`)
			return
		}
		fmt.Fprint(w, `{"id":1001,"status":"waiting"}`)
	}))
	defer gh.Close()
	base := state.New(t.TempDir())
	srv := newTestServer(t, base, gh.URL, &fakeSandbox{})
	srv.Close()
	sendQueueTestWebhook(t, srv, "workflow_job", "queued")
	req, err := base.ReadRequest("1001")
	if err != nil {
		t.Fatal(err)
	}
	store := &waitingMetricsCountingStore{Store: base}
	srv.store = store
	srv.cfg.RetryBaseDelay, srv.cfg.RetryMaxDelay = time.Hour, time.Hour
	check := func() {
		t.Helper()
		st := queueTestState(t, base)
		st.Status, st.LeaseOwner = state.StatusCreating, "test-worker"
		st.LeaseExpiresAt = time.Now().Add(time.Minute)
		if err := base.WriteState(st); err != nil {
			t.Fatal(err)
		}
		if start, err := srv.ensureWorkflowJobStillQueued(context.Background(), req, "test-worker"); start || err != nil {
			t.Fatalf("unexpected start=%v err=%v", start, err)
		}
	}
	for i := 0; i < 10; i++ {
		check()
	}
	if store.profileScans.Load() != 0 || store.activeScans.Load() != 0 || !srv.waitingMetricsDirty.Load() {
		t.Fatalf("waiting polls scanned aggregates: profiles=%d active=%d dirty=%v", store.profileScans.Load(), store.activeScans.Load(), srv.waitingMetricsDirty.Load())
	}
	srv.reconcileOnce(context.Background())
	if store.profileScans.Load() != 1 || srv.waitingMetricsDirty.Load() {
		t.Fatalf("waiting metric updates were not coalesced: scans=%d dirty=%v", store.profileScans.Load(), srv.waitingMetricsDirty.Load())
	}
	retries := expvar.Get("e2b_runner_retry_state").(*expvar.Map).Get("default|queued")
	if retries == nil || retries.(*expvar.Int).Value() != 1 {
		t.Fatalf("coalesced refresh lost retry gauge: %v", retries)
	}
	srv.reconcileOnce(context.Background())
	if store.profileScans.Load() != 1 {
		t.Fatal("reconciliation without new waiting changes refreshed aggregates")
	}
	check()
	store.failProfiles.Store(true)
	srv.reconcileOnce(context.Background())
	if !srv.waitingMetricsDirty.Load() {
		t.Fatal("failed aggregate lookup discarded the pending refresh")
	}
	store.failProfiles.Store(false)
	srv.reconcileOnce(context.Background())
	if srv.waitingMetricsDirty.Load() || store.profileScans.Load() != 3 {
		t.Fatal("pending metrics did not recover after aggregate lookup failure")
	}
	// Terminal failures must still refresh gauges synchronously.
	st := queueTestState(t, base)
	st.RetryCount = srv.cfg.RetryMaxAttempts
	if err := base.WriteState(st); err != nil {
		t.Fatal(err)
	}
	terminal.Store(true)
	check()
	if queueTestState(t, base).Status != state.StatusFailed || store.profileScans.Load() != 4 {
		t.Fatal("terminal failure did not refresh gauges immediately")
	}
}

type completionFailureStore struct {
	state.Store
	readCount int
	failRead  bool
	failWrite bool
}

func (s *completionFailureStore) ReadState(id string) (state.RunnerState, error) {
	s.readCount++
	// startRunner reads once, then the startup check reads before and after
	// GitHub. Fail the final read inside completeWithoutSandbox only once.
	if s.failRead && s.readCount == 4 {
		return state.RunnerState{}, errors.New("temporary completion state read failure")
	}
	return s.Store.ReadState(id)
}

func (s *completionFailureStore) WriteState(st state.RunnerState) error {
	if s.failWrite && st.Status == state.StatusCompleted {
		s.failWrite = false
		return errors.New("temporary completion state write failure")
	}
	return s.Store.WriteState(st)
}

func TestWorkflowJobStartupCompletionPersistenceFailure(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, `{"id":1001,"status":"completed","conclusion":"cancelled"}`)
			}))
			defer gh.Close()
			base := state.New(t.TempDir())
			fake := &fakeSandbox{}
			srv := newTestServer(t, base, gh.URL, fake)
			srv.Close()
			sendQueueTestWebhook(t, srv, "workflow_job", "queued")
			st := queueTestState(t, base)
			st.LeaseOwner = "test-worker"
			st.LeaseExpiresAt = time.Now().Add(time.Minute)
			if err := base.WriteState(st); err != nil {
				t.Fatal(err)
			}
			srv.store = &completionFailureStore{Store: base, failRead: operation == "read", failWrite: operation == "write"}
			srv.startRunner(context.Background(), "1001", "test-worker")
			st = queueTestState(t, base)
			if st.Status != state.StatusQueued || st.RetryCount != 1 || st.NextRetryAt.IsZero() || st.LeaseOwner != "" || !st.LeaseExpiresAt.IsZero() || !st.CreatingAt.IsZero() {
				t.Fatalf("completion persistence failure must release creation capacity and schedule retry: %#v", st)
			}
			if !st.CompletedAt.IsZero() || st.GitHubJobStatus != "" || fake.startedCount() != 0 {
				t.Fatalf("failed completion must not claim success or provision a Runner: %#v", st)
			}
			if n, err := base.InFlightCount(); err != nil || n != 0 {
				t.Fatalf("completion failure retained capacity: n=%d err=%v", n, err)
			}
			control, err := base.ReadLog("1001", "control.log", 0)
			if err != nil || strings.Contains(string(control), "runner skipped before sandbox start") {
				t.Fatalf("completion failure logged success: err=%v log=%s", err, control)
			}
			// A subsequent normal attempt can retain GitHub's terminal result.
			srv.startRunner(context.Background(), "1001", "test-worker")
			st = queueTestState(t, base)
			if st.Status != state.StatusCompleted || st.GitHubJobConclusion != "cancelled" || st.GitHubJobObservedAt.IsZero() || st.LeaseOwner != "" || !st.NextRetryAt.IsZero() || fake.startedCount() != 0 {
				t.Fatalf("completion did not recover after the store failure: %#v", st)
			}
		})
	}
}

func TestWorkflowJobStartupDiscardsStaleObservation(t *testing.T) {
	for _, status := range []string{"waiting", "queued", "completed", "lookup_error", "assignment_changed"} {
		t.Run(status, func(t *testing.T) {
			lookup, resume := make(chan struct{}), make(chan struct{})
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				close(lookup)
				<-resume
				if status == "lookup_error" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				observed := status
				if status == "assignment_changed" {
					observed = "queued"
				}
				fmt.Fprintf(w, `{"id":1001,"status":%q}`, observed)
			}))
			defer gh.Close()
			store := state.New(t.TempDir())
			srv := newTestServer(t, store, gh.URL, &fakeSandbox{})
			srv.Close()
			sendQueueTestWebhook(t, srv, "workflow_job", "queued")
			st := queueTestState(t, store)
			st.Status, st.LeaseOwner = state.StatusCreating, "worker"
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			req, _ := store.ReadRequest("1001")
			done := make(chan bool, 1)
			go func() {
				start, err := srv.ensureWorkflowJobStillQueued(context.Background(), req, "worker")
				done <- start || err != nil
			}()
			<-lookup
			st = queueTestState(t, store)
			st.Status = state.StatusCompleted
			if status == "assignment_changed" {
				st.Status = state.StatusCreating
				st.AssignedJobID, st.AssignedJobName = 1001, "build"
			}
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			close(resume)
			started := <-done
			after := queueTestState(t, store)
			if status == "assignment_changed" {
				if started || after.Status != state.StatusQueued || after.LeaseOwner != "" || after.NextRetryAt.IsZero() || after.AssignedJobID != 1001 {
					t.Fatalf("stale assignment observation not deferred: %#v", after)
				}
			} else if started || after.Status != state.StatusCompleted {
				t.Fatal("stale lookup advanced a completed request")
			}
		})
	}
}

func TestWorkflowJobWaitingRecoveryFromInterruptedCheck(t *testing.T) {
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":1001,"status":"waiting"}`)
	}))
	defer gh.Close()
	store := state.New(t.TempDir())
	fake := &fakeSandbox{recoverErr: sandboxrunner.ErrSandboxNotFound}
	srv := newTestServer(t, store, gh.URL, fake)
	srv.Close()
	sendQueueTestWebhook(t, srv, "workflow_job", "queued")
	st := queueTestState(t, store)
	st.Status, st.LeaseOwner = state.StatusCreating, "old-process"
	st.LeaseExpiresAt = time.Now().Add(time.Hour)
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	if err := srv.recoverRunner(context.Background(), "1001"); err != nil {
		t.Fatal(err)
	}
	st = queueTestState(t, store)
	if st.Status != state.StatusQueued || st.LeaseOwner != "" || !st.CompletedAt.IsZero() || fake.startedCount() != 0 {
		t.Fatalf("interrupted waiting lookup stranded at restart: %#v", st)
	}
}

func TestWorkflowJobWaitingWakeOnlyAppliesToUnleasedMatchingWait(t *testing.T) {
	for _, scenario := range []string{"wake", "leased", "other_job", "other_repository", "other_installation", "other_backoff"} {
		t.Run(scenario, func(t *testing.T) {
			store := state.New(t.TempDir())
			srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
			srv.Close()
			sendQueueTestWebhook(t, srv, "workflow_job", "queued")
			st := queueTestState(t, store)
			st.LastErrorCode = workflowJobWaitCode
			st.NextRetryAt = time.Now().Add(time.Hour)
			if scenario == "leased" {
				st.LeaseOwner, st.LeaseExpiresAt = "worker", time.Now().Add(time.Minute)
			}
			if scenario == "other_backoff" {
				st.LastErrorCode = "sandbox_capacity"
			}
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			req, _ := store.ReadRequest("1001")
			switch scenario {
			case "other_job":
				req.JobID++
			case "other_repository":
				req.RepositoryFullName = "another/repo"
			case "other_installation":
				req.GitHubInstallationID++
			}
			after, err := srv.wakeWaitingWorkflowJob(req, st)
			if err != nil {
				t.Fatal(err)
			}
			if (scenario == "wake") != after.NextRetryAt.IsZero() {
				t.Fatalf("unexpected wake for %s: %#v", scenario, after)
			}
		})
	}
}

func TestQueuedDeliveryDoesNotReopenAdvancedRequests(t *testing.T) {
	for _, status := range []string{state.StatusCreating, state.StatusRunning, state.StatusStopping, state.StatusCompleted, state.StatusFailed} {
		t.Run(status, func(t *testing.T) {
			store := state.New(t.TempDir())
			srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
			srv.Close()
			sendQueueTestWebhook(t, srv, "workflow_job", "queued")
			st := queueTestState(t, store)
			st.Status, st.LastErrorCode = status, workflowJobWaitCode
			st.NextRetryAt = time.Now().Add(time.Hour)
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			before := queueTestState(t, store)
			sendQueueTestWebhook(t, srv, "workflow_job", "queued")
			if after := queueTestState(t, store); after.Version != before.Version || after.Status != status {
				t.Fatalf("queued delivery reopened %s: %#v", status, after)
			}
		})
	}
}

func TestHistoricalWaitingJobRequiresTargetedValidatedRetry(t *testing.T) {
	for _, scenario := range []string{"recover", "no_control_evidence", "stdout_only", "real_result", "running", "stopping", "sandbox", "disabled_spec", "not_queued", "assigned", "lookup_error", "concurrent_change"} {
		t.Run(scenario, func(t *testing.T) {
			store := state.New(t.TempDir())
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario == "lookup_error" {
					http.Error(w, "unavailable", http.StatusServiceUnavailable)
					return
				}
				if scenario == "concurrent_change" {
					st := queueTestState(t, store)
					st.Status = state.StatusStopping
					if err := store.WriteState(st); err != nil {
						t.Error(err)
					}
				}
				status, runner := "queued", ""
				if scenario == "not_queued" {
					status = "waiting"
				}
				if scenario == "assigned" {
					runner = "other"
				}
				fmt.Fprintf(w, `{"id":1001,"status":%q,"runner_name":%q}`, status, runner)
			}))
			defer gh.Close()
			srv := newTestServer(t, store, gh.URL, &fakeSandbox{})
			srv.Close()
			sendQueueTestWebhook(t, srv, "workflow_job", "queued")
			st := queueTestState(t, store)
			st.Status = state.StatusCompleted
			switch scenario {
			case "real_result":
				st.GitHubJobStatus, st.GitHubJobConclusion = "completed", "success"
			case "running":
				st.RunningAt = time.Now()
			case "stopping":
				st.StoppingAt = time.Now()
			case "sandbox":
				st.SandboxID = "existing-sandbox"
			case "disabled_spec":
				profile, _ := store.GetProfile("default")
				profile.Enabled = false
				if _, err := store.UpsertProfile(profile); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			if scenario != "no_control_evidence" {
				stream := "control.log"
				if scenario == "stdout_only" {
					stream = "stdout.log"
				}
				store.AppendLog("1001", stream, []byte("runner skipped before sandbox start: workflow job is waiting\n"))
			}
			// A queued replay alone must never reopen the historical terminal row.
			sendQueueTestWebhook(t, srv, "workflow_job", "queued")
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, adminRequest(http.MethodPost, "/runner_requests/1001/retry", nil))
			want := http.StatusConflict
			if scenario == "recover" {
				want = http.StatusAccepted
			}
			if rec.Code != want {
				t.Fatalf("retry status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
			}
			if scenario == "recover" {
				st = queueTestState(t, store)
				if st.Status != state.StatusQueued || !st.CompletedAt.IsZero() || st.SandboxID != "" {
					t.Fatalf("invalid historical recovery: %#v", st)
				}
			}
		})
	}
}
