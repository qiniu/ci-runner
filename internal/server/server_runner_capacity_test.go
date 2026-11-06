package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type admissionTestStore struct {
	state.Store
	progressMu     sync.Mutex
	progress       map[string][]func()
	ready, workers sync.WaitGroup
}

func (s *admissionTestStore) AppendStagedLog(id, name, stage string, data []byte) {
	s.Store.AppendStagedLog(id, name, stage, data)
	// Duplicate starts flush only initial reads. They must not report the
	// original worker ready while it is still provisioning.
	if stage == runnerEventStageTiming && strings.Contains(string(data), "stage=profile_lookup ") {
		s.progressMu.Lock()
		for _, notify := range s.progress[id] {
			notify()
		}
		delete(s.progress, id)
		s.progressMu.Unlock()
	}
}

func admissionTestFixture(t *testing.T, globalLimit, profileLimit int, scopes ...int64) (*Server, *admissionTestStore, *lifecycleSandboxService) {
	t.Helper()
	base := state.New(t.TempDir())
	if len(scopes) == 0 {
		upsertLifecycleProfile(t, base, state.RunnerProfile{Name: "shared", Labels: []string{"self-hosted", "custom"}, TemplateID: "custom-id", Enabled: true, MaxConcurrency: profileLimit})
	}
	for i, id := range []string{"a", "b"} {
		req := state.RunnerRequest{ID: id, RepositoryFullName: "o/r", ProfileName: "shared", Labels: []string{"self-hosted", "custom"}}
		if i == 1 {
			// Legacy/global specs share capacity regardless of account context.
			req.ProfileSource, req.ProfileScopeType, req.ProfileScopeID = "global", state.RunnerProfileScopeAccount, 2
		}
		if len(scopes) > 0 {
			req.ProfileSource, req.ProfileScopeType, req.ProfileScopeID = "scoped_custom", state.RunnerProfileScopeAccount, scopes[i]
			_, err := base.GetScopedProfile(state.RunnerProfileScope{Type: req.ProfileScopeType, ID: req.ProfileScopeID}, req.ProfileName)
			if errors.Is(err, state.ErrNotFound) {
				_, err = base.UpsertScopedProfileIfUnchanged(state.ScopedRunnerProfile{ScopeType: req.ProfileScopeType, ScopeID: req.ProfileScopeID, Name: req.ProfileName, WorkflowLabels: req.Labels, TemplateID: "custom-id", Enabled: true, MaxConcurrency: profileLimit}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		_, st, err := base.CreateRequest(req, nil)
		if err != nil {
			t.Fatal(err)
		}
		st.LeaseOwner, st.LeaseExpiresAt = "worker", time.Now().Add(time.Minute)
		if err := base.WriteState(st); err != nil {
			t.Fatal(err)
		}
	}
	store := &admissionTestStore{Store: base}
	fake := &lifecycleSandboxService{events: &lifecycleEventRecorder{}}
	handler := githubRunnerAPI(t)
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Model the same GitHub API for all repositories in the admission fixture.
		parts := strings.SplitN(r.URL.Path, "/", 5)
		if len(parts) == 5 && parts[1] == "repos" {
			r.URL.Path = "/repos/o/r/" + parts[4]
		}
		handler(w, r)
	}))
	t.Cleanup(gh.Close)
	srv := newRunnerLifecycleTestServer(t, store, gh.URL, fake)
	srv.Close()
	srv.cfg.MaxConcurrentRunners = globalLimit
	t.Cleanup(func() {
		ready := make(chan struct{})
		go func() { store.ready.Wait(); close(ready) }()
		awaitAdmissionTest(t, ready, "startup did not reach timing flush during cleanup")
		for _, input := range fake.startInputs() {
			input.OnExit(sandboxrunner.ExitResult{}, nil)
		}
		done := make(chan struct{})
		go func() { store.workers.Wait(); close(done) }()
		awaitAdmissionTest(t, done, "startup worker did not exit during cleanup")
	})
	return srv, store, fake
}

func startAdmissionTestRunner(t *testing.T, srv *Server, id string) <-chan struct{} {
	t.Helper()
	ready := make(chan struct{})
	store := srv.store.(*admissionTestStore)
	store.ready.Add(1)
	store.workers.Add(1)
	var once sync.Once
	notify := func() { once.Do(func() { close(ready); store.ready.Done() }) }
	store.progressMu.Lock()
	if store.progress == nil {
		store.progress = make(map[string][]func())
	}
	store.progress[id] = append(store.progress[id], notify)
	store.progressMu.Unlock()
	go func() {
		defer store.workers.Done()
		defer notify()
		srv.startClaimedRunner(t.Context(), queuedRunnerForTest(t, srv.store, id, "worker"))
	}()
	// startRunner intentionally lives until process exit. Timings flush after
	// durable admission and lock release, which is the progress under test.
	return ready
}

func awaitAdmissionTest(t *testing.T, done <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal(message)
	}
}

func assertAdmissionState(t *testing.T, store state.Store, id, status string) state.RunnerState {
	t.Helper()
	st, err := store.ReadState(id)
	if err != nil || st.Status != status {
		t.Fatalf("request %s status = %s, error = %v; want %s", id, st.Status, err, status)
	}
	return st
}

// The admission checks and creating write now share a database transaction.
type unavailableEnqueueStore struct{ state.Store }

func (s unavailableEnqueueStore) WithRunnerEnqueue(context.Context, func(state.Store) error) error {
	return errors.New("catalog guard unavailable")
}

func TestDuplicateWorkflowJobDoesNotAcquireCatalogGuard(t *testing.T) {
	srv, store, _ := admissionTestFixture(t, 1, 1)
	job := github.WorkflowJob{ID: 123, Labels: []string{"self-hosted", "custom"}}
	want, created, err := srv.enqueueWorkflowJob(t.Context(), "o/r", 0, "test", job, nil)
	if err != nil || !created {
		t.Fatalf("first delivery: created=%v err=%v", created, err)
	}
	srv.store = unavailableEnqueueStore{store}
	got, created, err := srv.enqueueWorkflowJob(t.Context(), "o/r", 0, "test", job, nil)
	if err != nil || created || got.ID != want.ID {
		t.Fatalf("duplicate: id=%q created=%v err=%v", got.ID, created, err)
	}
	got.LastErrorCode = workflowJobWaitCode
	if err := store.WriteState(got); err != nil {
		t.Fatal(err)
	}
	if _, _, err := srv.enqueueWorkflowJob(t.Context(), "o/r", 0, "test", job, nil); err == nil {
		t.Fatal("waiting job skipped current admission checks")
	}
}

type failedRunnerAdmissionStore struct {
	state.Store
	stage string
}

func (s *failedRunnerAdmissionStore) WithRunnerAdmission(ctx context.Context, fn func(state.Store) error) error {
	if s.stage == "admission_lock_wait" {
		return context.DeadlineExceeded
	}
	return s.Store.WithRunnerAdmission(ctx, func(tx state.Store) error {
		return fn(&failedRunnerAdmissionStore{Store: tx, stage: s.stage})
	})
}

func (s *failedRunnerAdmissionStore) GetProfile(name string) (state.RunnerProfile, error) {
	if s.stage == "profile_lookup" {
		return state.RunnerProfile{}, state.ErrNotFound
	}
	return state.RunnerProfile{}, context.Canceled
}

func releaseAdmissionTestClaim(t *testing.T, store state.Store, claim state.RunnerState) {
	t.Helper()
	owner := claim.LeaseOwner
	claim.LeaseOwner, claim.LeaseExpiresAt = "", time.Time{}
	if err := store.WriteQueuedClaim(claim, owner); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerAdmissionFailureDoesNotImmediatelyReclaim(t *testing.T) {
	for _, stage := range []string{"admission_lock_wait", "profile_lookup", "canceled_lookup"} {
		t.Run(stage, func(t *testing.T) {
			srv, base, _ := admissionTestFixture(t, 1, 1)
			claim := queuedRunnerForTest(t, base, "a", "worker")
			releaseAdmissionTestClaim(t, base, queuedRunnerForTest(t, base, "b", "worker"))
			srv.store = &failedRunnerAdmissionStore{Store: base, stage: stage}
			srv.startClaimedRunner(t.Context(), claim)
			st, err := base.ReadState("a")
			if err != nil {
				t.Fatal(err)
			}
			if stage != "profile_lookup" {
				if stage == "admission_lock_wait" {
					log, err := base.ReadLog("a", "control.log", 65536)
					if err != nil || strings.Count(string(log), "stage=admission_lock_wait ") != 1 {
						t.Fatalf("missing or repeated guard failure timing: %s, %v", log, err)
					}
				}
				if st.Status != state.StatusQueued || st.RetryCount != 0 || st.LeaseOwner != "" || st.NextRetryAt.IsZero() || st.LastErrorCode != "runner_admission_unavailable" {
					t.Fatalf("guard timeout did not defer the claim: %+v", st)
				}
			} else if st.Status != state.StatusFailed || st.FailureStage != stage {
				t.Fatalf("invalid profile did not fail visibly: %+v", st)
			}
			req, _, ok, err := base.ClaimNextRunnable("next", time.Minute)
			if err != nil || !ok || req.ID != "b" {
				t.Fatalf("next worker did not reach the other job: id=%q ok=%v err=%v", req.ID, ok, err)
			}
		})
	}
}

func TestRunnerAdmissionSharedLimits(t *testing.T) {
	for _, tc := range []struct {
		dimension       string
		global, profile int
		scopes          []int64
		secondRepo      string
		independent     bool
	}{
		{"global", 1, 0, nil, "other/r", false},
		{"profile", 10, 1, nil, "other/r", false},
		{"profile", 10, 1, []int64{1, 1}, "other/r", false},
		{"profile", 10, 1, []int64{1, 2}, "other/r", true},
		{"account", 10, 0, nil, "O/another", false},
		{"repository", 10, 0, nil, "O/R", false},
		{"account", 10, 0, nil, "other/r", true},
		{"repository", 10, 0, nil, "o/other", true},
	} {
		t.Run(tc.dimension, func(t *testing.T) {
			srv, store, fake := admissionTestFixture(t, tc.global, tc.profile, tc.scopes...)
			second := assertAdmissionState(t, store.Store, "b", state.StatusQueued)
			second.RepositoryFullName = tc.secondRepo
			if tc.dimension == "account" || tc.dimension == "repository" {
				upsertLifecycleProfile(t, store.Store, state.RunnerProfile{Name: "second", Labels: []string{"self-hosted", "custom"}, TemplateID: "custom-id", Enabled: true})
				second.ProfileName = "second"
			}
			if tc.dimension == "account" {
				srv.cfg.RunnerAccountConcurrency = map[string]int{"o": 1, "other": 1}
			}
			if tc.dimension == "repository" {
				srv.cfg.RunnerRepositoryConcurrency = map[string]int{"o/r": 1, "o/other": 1}
			}
			if err := store.Store.WriteState(second); err != nil {
				t.Fatal(err)
			}
			a := startAdmissionTestRunner(t, srv, "a")
			b := startAdmissionTestRunner(t, srv, "b")
			awaitAdmissionTest(t, a, "A stalled")
			awaitAdmissionTest(t, b, "B stalled")
			started := fake.startInputs()
			if tc.independent {
				if len(started) != 2 {
					t.Fatalf("independent scopes started %d jobs", len(started))
				}
				return
			}
			if len(started) != 1 {
				t.Fatalf("shared limit admitted %d jobs", len(started))
			}
			winner, waiting := started[0].RequestID, "a"
			if winner == "a" {
				waiting = "b"
			}
			st := assertAdmissionState(t, store.Store, waiting, state.StatusQueued)
			if st.LastErrorCode != "runner_capacity_"+tc.dimension || st.RetryCount != 0 || st.LeaseOwner != "" || st.NextRetryAt.IsZero() {
				t.Fatalf("unexpected capacity wait %+v", st)
			}
			awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, waiting), "capacity recheck stalled")
			events, _, err := store.Store.ListRunnerEvents(waiting, 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, event := range events {
				if event.Stage == runnerEventStageTiming {
					t.Fatal("capacity polling appended startup timings")
				}
				if event.Stage == "runner_capacity" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("capacity polling logged %d events", count)
			}
			st = assertAdmissionState(t, store.Store, winner, state.StatusRunning)
			st.Status = state.StatusCompleted
			if err := store.Store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, waiting), "released capacity unavailable")
			resumed := assertAdmissionState(t, store.Store, waiting, state.StatusRunning)
			if strings.HasPrefix(resumed.LastErrorCode, "runner_capacity_") {
				t.Fatal("stale deferral after admission")
			}
		})
	}
}

func TestReadmittedRunnerGetsNewProvisionAttempt(t *testing.T) {
	srv, store, _ := admissionTestFixture(t, 1, 1)
	req, err := store.ReadRequest("a")
	if err != nil {
		t.Fatal(err)
	}
	admit := func(claim state.RunnerState) state.RunnerState {
		t.Helper()
		_, stage, err := srv.admitQueuedRunner(t.Context(), req, &claim, &runnerTimings{}, time.Now())
		if err != nil || stage != "" {
			t.Fatalf("admit: stage=%q err=%v", stage, err)
		}
		saved, err := store.ReadState(req.ID)
		if err != nil || saved.ProvisionAttemptID == "" || saved.ProvisionAttemptID != claim.ProvisionAttemptID || saved.Version != claim.Version {
			t.Fatalf("admission state does not match committed attempt: claim=%d saved=%d err=%v", claim.Version, saved.Version, err)
		}
		return saved
	}
	first := admit(queuedRunnerForTest(t, store, req.ID, "worker"))
	failed := first
	failed.Status = state.StatusFailed
	if err := store.WriteState(failed); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetryRequest(req.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	_, claim, ok, err := store.ClaimNextRunnable("other-worker", time.Minute)
	if err != nil || !ok || claim.ID != req.ID {
		t.Fatalf("reclaim: id=%q ok=%v err=%v", claim.ID, ok, err)
	}
	second := admit(claim)
	if first.ProvisionAttemptID == second.ProvisionAttemptID {
		t.Fatal("fresh retry reused the previous provision attempt")
	}
}

func TestRunnerAdmissionConcurrentEnqueueIsIdempotent(t *testing.T) {
	srv, store, _ := admissionTestFixture(t, 3, 3)
	var created atomic.Int32
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			_, inserted, err := srv.enqueueWorkflowJob(t.Context(), "o/r", 0, "test", github.WorkflowJob{ID: 123, Labels: []string{"self-hosted", "custom"}}, nil)
			if err != nil {
				t.Error(err)
			}
			if inserted {
				created.Add(1)
			}
		})
	}
	workers.Wait()
	if created.Load() != 1 {
		t.Fatalf("created %d requests, want 1", created.Load())
	}
	assertAdmissionState(t, store.Store, "123", state.StatusQueued)
}

func TestRunnerScopeCapacityQueuePassesBusyRepository(t *testing.T) {
	for _, dimension := range []string{"account", "repository"} {
		t.Run(dimension, func(t *testing.T) {
			srv, store, _ := admissionTestFixture(t, 10, 0)
			if dimension == "account" {
				srv.cfg.RunnerAccountConcurrency = map[string]int{"o": 1}
			} else {
				srv.cfg.RunnerRepositoryConcurrency = map[string]int{"o/r": 1}
			}
			active := assertAdmissionState(t, store.Store, "a", state.StatusQueued)
			active.Status = state.StatusRunning
			if err := store.Store.WriteState(active); err != nil {
				t.Fatal(err)
			}
			releaseAdmissionTestClaim(t, store.Store, assertAdmissionState(t, store.Store, "b", state.StatusQueued))
			for i := range 20 {
				if _, _, err := store.Store.CreateRequest(state.RunnerRequest{ID: fmt.Sprintf("busy-%02d", i), RepositoryFullName: "o/r", ProfileName: "shared"}, nil); err != nil {
					t.Fatal(err)
				}
			}
			repository := "o/independent"
			if dimension == "account" {
				repository = "other/independent"
			}
			if _, _, err := store.Store.CreateRequest(state.RunnerRequest{ID: "independent", RepositoryFullName: repository, ProfileName: "shared"}, nil); err != nil {
				t.Fatal(err)
			}
			for i := range 22 {
				// Previously deferred rows remain eligible during this backlog.
				req, _, claimed, err := store.Store.ClaimNextRunnable("worker", time.Minute)
				if err != nil || !claimed {
					t.Fatalf("queue exhausted before independent request: %v", err)
				}
				awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, req.ID), "claimed request stalled")
				if req.ID == "independent" {
					if i != 21 {
						t.Fatalf("fixture did not exercise backlog: skipped %d requests", i)
					}
					assertAdmissionState(t, store.Store, req.ID, state.StatusRunning)

					return
				}
				queued := assertAdmissionState(t, store.Store, req.ID, state.StatusQueued)
				if queued.LeaseOwner != "" || queued.NextRetryAt.IsZero() {
					t.Fatal("blocked request retained queue lease")
				}
				// Simulate the deferral expiring before the next poll. Its new
				// eligibility time must still place it behind the waiting request.
				queued.NextRetryAt = time.Now().UTC()
				if err := store.Store.WriteState(queued); err != nil {
					t.Fatal(err)
				}
			}
			t.Fatal("busy scope blocked independent request")
		})
	}
}
