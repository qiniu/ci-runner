package server

import (
	"errors"
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

type admissionBlockingStore struct {
	state.Store
	stage                   string
	armed, used             atomic.Bool
	entered                 chan struct{}
	release                 chan struct{}
	writeFailure            error
	writeConflict           bool
	caseInsensitiveProfiles bool
	progressMu              sync.Mutex
	progress                map[string][]func()
	ready, workers          sync.WaitGroup
}

func (s *admissionBlockingStore) AppendStagedLog(id, name, stage string, data []byte) {
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

func (s *admissionBlockingStore) block(stage string) bool {
	if s.armed.Load() && stage == s.stage && s.used.CompareAndSwap(false, true) {
		close(s.entered)
		<-s.release
		return true
	}
	return false
}

func (s *admissionBlockingStore) GetProfile(name string) (state.RunnerProfile, error) {
	profile, err := s.Store.GetProfile(s.lookupProfileName(name))
	s.block("profile_lookup")
	return profile, err
}

// Model a case-insensitive catalog lookup while keeping the fixture database
// portable. The regression pauses before any creating write, so both durable
// counts are zero under either SQLite or a case-insensitive MySQL collation.
func (s *admissionBlockingStore) lookupProfileName(name string) string {
	if s.caseInsensitiveProfiles && strings.EqualFold(name, "shared") {
		return "shared"
	}
	return name
}

func (s *admissionBlockingStore) GetScopedProfile(scope state.RunnerProfileScope, name string) (state.ScopedRunnerProfile, error) {
	return s.Store.GetScopedProfile(scope, s.lookupProfileName(name))
}

func (s *admissionBlockingStore) InFlightCount() (int, error) {
	count, err := s.Store.InFlightCount()
	// Block AFTER reading to exercise an arbitrarily stale successful result.
	s.block("global_capacity_check")
	return count, err
}

func (s *admissionBlockingStore) InFlightCountForProfile(name string) (int, error) {
	count, err := s.Store.InFlightCountForProfile(name)
	s.block("profile_capacity_check")
	return count, err
}

func (s *admissionBlockingStore) InFlightCountForProfileScope(source string, scope state.RunnerProfileScope, name string) (int, error) {
	count, err := s.Store.InFlightCountForProfileScope(source, scope, name)
	s.block("profile_capacity_check")
	return count, err
}

func (s *admissionBlockingStore) InFlightCountForRepositoryOwner(owner string) (int, error) {
	count, err := s.Store.InFlightCountForRepositoryOwner(owner)
	s.block("account_capacity_check")
	return count, err
}

func (s *admissionBlockingStore) InFlightCountForRepository(repository string) (int, error) {
	count, err := s.Store.InFlightCountForRepository(repository)
	s.block("repository_capacity_check")
	return count, err
}

func (s *admissionBlockingStore) WriteState(st state.RunnerState) error {
	if st.Status == state.StatusCreating && s.block("creating_state_persist") {
		if s.writeFailure != nil {
			return s.writeFailure
		}
		if s.writeConflict {
			latest, err := s.Store.ReadState(st.ID)
			if err != nil {
				return err
			}
			latest.Status = state.StatusCompleted
			if err := s.Store.WriteState(latest); err != nil {
				return err
			}
		}
	}
	return s.Store.WriteState(st)
}

func (s *admissionBlockingStore) CreateRequest(req state.RunnerRequest, payload []byte) (bool, state.RunnerState, error) {
	s.block("request_create")
	return s.Store.CreateRequest(req, payload)
}

func admissionTestFixture(t *testing.T, stage string, globalLimit, profileLimit int, scopes ...int64) (*Server, *admissionBlockingStore, *lifecycleSandboxService, func()) {
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
	store := &admissionBlockingStore{Store: base, stage: stage, entered: make(chan struct{}), release: make(chan struct{})}
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
	store.armed.Store(true)
	var once sync.Once
	release := func() { once.Do(func() { close(store.release) }) }
	t.Cleanup(func() {
		release()
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
	return srv, store, fake, release
}

func startAdmissionTestRunner(t *testing.T, srv *Server, id string) <-chan struct{} {
	t.Helper()
	ready := make(chan struct{})
	store := srv.store.(*admissionBlockingStore)
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
		srv.startRunner(t.Context(), id, "worker")
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

func assertCapacityReleased(t *testing.T, srv *Server) {
	t.Helper()
	srv.runnerCapacity.mu.Lock()
	defer srv.runnerCapacity.mu.Unlock()
	if srv.runnerCapacity.pending != 0 || len(srv.runnerCapacity.profiles) != 0 || len(srv.runnerCapacity.accounts) != 0 || len(srv.runnerCapacity.repositories) != 0 {
		t.Fatal("capacity reservations or observations leaked")
	}
}

func TestRunnerCapacityReservesAccountAndRepositoryAcrossProfiles(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		accountLimit, repositoryLimit int
		want                          string
	}{
		{"account", 1, 0, "account_capacity_check"},
		{"repository", 0, 1, "repository_capacity_check"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capacity := &runnerAdmissionCapacity{}
			first := capacity.observe(state.RunnerRequest{GitHubInstallationID: 123, RepositoryFullName: "Acme/Repo", ProfileName: "one"}, "one")
			second := capacity.observe(state.RunnerRequest{GitHubInstallationID: 123, RepositoryFullName: "acme/repo", ProfileName: "two"}, "two")
			if stage := first.reserve(0, 10, 0, 10, 0, tc.accountLimit, 0, tc.repositoryLimit); stage != "" {
				t.Fatalf("first reserve: %s", stage)
			}
			if stage := second.reserve(0, 10, 0, 10, 0, tc.accountLimit, 0, tc.repositoryLimit); stage != tc.want {
				t.Fatalf("second reserve: %s, want %s", stage, tc.want)
			}
			first.finish()
			second.finish()
			if len(capacity.accounts) != 0 || len(capacity.repositories) != 0 {
				t.Fatal("scope observations leaked")
			}
		})
	}
}

func TestRunnerAdmissionStoreWaitDoesNotBlockOtherStarts(t *testing.T) {
	for _, stage := range []string{"profile_lookup", "global_capacity_check", "profile_capacity_check", "creating_state_persist"} {
		t.Run(stage, func(t *testing.T) {
			srv, store, fake, release := admissionTestFixture(t, stage, 3, 3)
			a := startAdmissionTestRunner(t, srv, "a")
			awaitAdmissionTest(t, store.entered, "first request did not reach blocked Store operation")
			b := startAdmissionTestRunner(t, srv, "b")
			awaitAdmissionTest(t, b, "independent startup was serialized behind Store wait")
			assertAdmissionState(t, store.Store, "b", state.StatusRunning)
			release()
			awaitAdmissionTest(t, a, "blocked startup did not finish")
			assertAdmissionState(t, store.Store, "a", state.StatusRunning)
			if len(fake.startInputs()) != 2 {
				t.Fatal("each request must start exactly once")
			}
			assertCapacityReleased(t, srv)
		})
	}
}

func TestRunnerAdmissionCountsConcurrentReservations(t *testing.T) {
	for _, tc := range []struct {
		name, stage     string
		global, profile int
		scopes          []int64
	}{
		{"pending_global_write", "creating_state_persist", 1, 0, nil},
		{"pending_profile_write", "creating_state_persist", 10, 1, nil},
		{"pending_scoped_write", "creating_state_persist", 10, 1, []int64{1, 1}},
		{"stale_global_read", "global_capacity_check", 1, 0, nil},
		{"stale_profile_read", "profile_capacity_check", 10, 1, nil},
		{"stale_scoped_read", "profile_capacity_check", 10, 1, []int64{1, 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, fake, release := admissionTestFixture(t, tc.stage, tc.global, tc.profile, tc.scopes...)
			a := startAdmissionTestRunner(t, srv, "a")
			awaitAdmissionTest(t, store.entered, "Store block not reached")
			awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, "b"), "second request waited instead of deciding capacity")
			release()
			awaitAdmissionTest(t, a, "first request did not finish")
			winner, deferred := "a", "b"
			if tc.stage != "creating_state_persist" {
				winner, deferred = deferred, winner
			}
			assertAdmissionState(t, store.Store, winner, state.StatusRunning)
			st := assertAdmissionState(t, store.Store, deferred, state.StatusQueued)
			if st.LeaseOwner != "" || st.NextRetryAt.IsZero() {
				t.Fatal("capacity deferral must release lease and schedule retry")
			}
			if len(fake.startInputs()) != 1 {
				t.Fatal("concurrent starts exceeded available capacity")
			}
			assertCapacityReleased(t, srv)
		})
	}
}

func TestRunnerAdmissionReleasesFailedCreatingReservation(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "write_error", true: "version_conflict"}[conflict], func(t *testing.T) {
			srv, store, fake, release := admissionTestFixture(t, "creating_state_persist", 1, 1)
			store.writeConflict = conflict
			if !conflict {
				store.writeFailure = errors.New("injected write failure")
			}
			a := startAdmissionTestRunner(t, srv, "a")
			awaitAdmissionTest(t, store.entered, "creating write not reached")
			awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, "b"), "capacity deferral blocked")
			release()
			awaitAdmissionTest(t, a, "failed creating write did not return")
			assertCapacityReleased(t, srv)
			awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, "b"), "released slot was not reusable")
			assertAdmissionState(t, store.Store, "b", state.StatusRunning)
			if conflict {
				assertAdmissionState(t, store.Store, "a", state.StatusCompleted)
			}
			if len(fake.startInputs()) != 1 || fake.startInputs()[0].RequestID != "b" {
				t.Fatal("failed/CAS-conflicting admission launched a Sandbox")
			}
			assertCapacityReleased(t, srv)
		})
	}
}

func TestRunnerAdmissionSeparatesScopedCapacity(t *testing.T) {
	srv, store, fake, release := admissionTestFixture(t, "creating_state_persist", 3, 1, 1, 2)
	a := startAdmissionTestRunner(t, srv, "a")
	awaitAdmissionTest(t, store.entered, "creating write not reached")
	awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, "b"), "different scope was blocked")
	assertAdmissionState(t, store.Store, "b", state.StatusRunning)
	release()
	awaitAdmissionTest(t, a, "first request did not finish")
	if len(fake.startInputs()) != 2 {
		t.Fatal("same spec name in different scopes must have independent capacity")
	}
	assertCapacityReleased(t, srv)
}

func TestRunnerAdmissionDuplicateStartKeepsOneReservation(t *testing.T) {
	srv, store, fake, release := admissionTestFixture(t, "creating_state_persist", 2, 2)
	a := startAdmissionTestRunner(t, srv, "a")
	awaitAdmissionTest(t, store.entered, "creating write not reached")
	// Unlike the original long-lived worker, the duplicate must return.
	duplicate := make(chan struct{})
	store.workers.Add(1)
	go func() {
		defer store.workers.Done()
		defer close(duplicate)
		srv.startRunner(t.Context(), "a", "worker")
	}()
	release()
	awaitAdmissionTest(t, a, "first start did not finish")
	awaitAdmissionTest(t, duplicate, "duplicate start did not finish")
	if len(fake.startInputs()) != 1 {
		t.Fatalf("duplicate startup created %d Sandboxes, want 1", len(fake.startInputs()))
	}
	assertCapacityReleased(t, srv)
}

func TestRunnerAdmissionSlowEnqueueDoesNotBlockStart(t *testing.T) {
	srv, store, _, release := admissionTestFixture(t, "request_create", 3, 3)
	enqueued := make(chan struct{})
	go func() {
		defer close(enqueued)
		_, _, err := srv.enqueueRunnerRequest(state.RunnerRequest{ID: "c", RepositoryFullName: "o/r", ProfileName: "shared"}, nil)
		if err != nil {
			t.Error(err)
		}
	}()
	awaitAdmissionTest(t, store.entered, "enqueue did not enter Store")
	awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, "b"), "startup blocked behind enqueue Store operation")
	assertAdmissionState(t, store.Store, "b", state.StatusRunning)
	release()
	awaitAdmissionTest(t, enqueued, "enqueue did not finish")
}

func TestRunnerAdmissionConcurrentEnqueueIsIdempotent(t *testing.T) {
	srv, store, _, _ := admissionTestFixture(t, "", 3, 3)
	var created atomic.Int32
	var workers sync.WaitGroup
	for range 12 {
		workers.Go(func() {
			_, inserted, err := srv.enqueueRunnerRequest(state.RunnerRequest{ID: "duplicate", RepositoryFullName: "o/r", ProfileName: "shared", JobID: 123}, nil)
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
	assertAdmissionState(t, store.Store, "duplicate", state.StatusQueued)
}

func TestRunnerAdmissionUsesReloadedProfileName(t *testing.T) {
	for _, tc := range []struct {
		name        string
		insensitive bool
		scopes      []int64
	}{
		{"global_case_insensitive", true, nil},
		{"scoped_case_insensitive", true, []int64{1, 1}},
		{"distinct_case_sensitive_specs", false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv, store, fake, release := admissionTestFixture(t, "creating_state_persist", 10, 1, tc.scopes...)
			store.caseInsensitiveProfiles = tc.insensitive
			if !tc.insensitive {
				upsertLifecycleProfile(t, store.Store, state.RunnerProfile{Name: "SHARED", Labels: []string{"self-hosted", "custom"}, TemplateID: "custom-id", Enabled: true, MaxConcurrency: 1})
			}
			st := assertAdmissionState(t, store.Store, "b", state.StatusQueued)
			st.ProfileName = "SHARED"
			if err := store.Store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			a := startAdmissionTestRunner(t, srv, "a")
			awaitAdmissionTest(t, store.entered, "creating write not reached")
			awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, "b"), "second admission did not decide capacity")
			want, starts := state.StatusRunning, 2
			if tc.insensitive {
				want, starts = state.StatusQueued, 1
			}
			assertAdmissionState(t, store.Store, "b", want)
			release()
			awaitAdmissionTest(t, a, "first startup did not finish")
			if got := len(fake.startInputs()); got != starts {
				t.Fatalf("started %d Sandboxes, want %d", got, starts)
			}
			assertCapacityReleased(t, srv)
		})
	}
}

func TestRunnerScopeCapacityConcurrentAdmission(t *testing.T) {
	for _, dimension := range []string{"account", "repository"} {
		for _, stage := range []string{"creating_state_persist", dimension + "_capacity_check"} {
			t.Run(dimension+"/"+stage, func(t *testing.T) {
				srv, store, fake, release := admissionTestFixture(t, stage, 10, 0)
				if dimension == "account" {
					srv.cfg.RunnerAccountConcurrency = map[string]int{"o": 1}
				} else {
					srv.cfg.RunnerRepositoryConcurrency = map[string]int{"o/r": 1}
				}
				// Separate Specs and, for account limits, separate repositories must still share a quota.
				upsertLifecycleProfile(t, store.Store, state.RunnerProfile{Name: "second", Labels: []string{"self-hosted", "custom"}, TemplateID: "custom-id", Enabled: true})
				b := assertAdmissionState(t, store.Store, "b", state.StatusQueued)
				b.ProfileName = "second"
				if dimension == "account" {
					b.RepositoryFullName = "O/another"
				} else {
					b.RepositoryFullName = "O/R"
				}
				if err := store.Store.WriteState(b); err != nil {
					t.Fatal(err)
				}
				aDone := startAdmissionTestRunner(t, srv, "a")
				awaitAdmissionTest(t, store.entered, "first admission did not block")
				awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, "b"), "second admission blocked")
				release()
				awaitAdmissionTest(t, aDone, "first admission did not finish")
				winner, deferred := "a", "b"
				if stage != "creating_state_persist" {
					winner, deferred = deferred, winner
				}
				assertAdmissionState(t, store.Store, winner, state.StatusRunning)
				queued := assertAdmissionState(t, store.Store, deferred, state.StatusQueued)
				if queued.LastErrorCode != "runner_capacity_"+dimension || queued.RetryCount != 0 || queued.LeaseOwner != "" || queued.NextRetryAt.IsZero() {
					t.Fatalf("invalid capacity wait: %#v", queued)
				}
				if len(fake.startInputs()) != 1 {
					t.Fatal("scope quota oversubscribed")
				}
				assertCapacityReleased(t, srv)
				// A new controller reconstructs usage from persisted states, not reservations.
				srv.runnerCapacity = runnerAdmissionCapacity{}
				awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, deferred), "restart capacity check stalled")
				assertAdmissionState(t, store.Store, deferred, state.StatusQueued)
				finished := assertAdmissionState(t, store.Store, winner, state.StatusRunning)
				finished.Status = state.StatusCompleted
				if err := store.Store.WriteState(finished); err != nil {
					t.Fatal(err)
				}
				awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, deferred), "released capacity was not reusable")
				resumed := assertAdmissionState(t, store.Store, deferred, state.StatusRunning)
				if strings.HasPrefix(resumed.LastErrorCode, "runner_capacity_") {
					t.Fatal("stale capacity reason survived admission")
				}
				assertCapacityReleased(t, srv)
			})
		}
	}
}

func TestRunnerScopeCapacityDoesNotBlockIndependentRepository(t *testing.T) {
	for _, dimension := range []string{"account", "repository"} {
		t.Run(dimension, func(t *testing.T) {
			srv, store, fake, release := admissionTestFixture(t, "creating_state_persist", 10, 0)
			if dimension == "account" {
				srv.cfg.RunnerAccountConcurrency = map[string]int{"o": 1, "other": 1}
			} else {
				srv.cfg.RunnerRepositoryConcurrency = map[string]int{"o/r": 1, "o/other": 1}
			}
			b := assertAdmissionState(t, store.Store, "b", state.StatusQueued)
			if dimension == "account" {
				b.RepositoryFullName = "other/r"
			} else {
				b.RepositoryFullName = "o/other"
			}
			if err := store.Store.WriteState(b); err != nil {
				t.Fatal(err)
			}
			a := startAdmissionTestRunner(t, srv, "a")
			awaitAdmissionTest(t, store.entered, "first admission did not reserve")
			awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, "b"), "independent repository blocked")
			assertAdmissionState(t, store.Store, "b", state.StatusRunning)
			release()
			awaitAdmissionTest(t, a, "first admission did not finish")
			if len(fake.startInputs()) != 2 {
				t.Fatal("independent capacity was unavailable")
			}
			assertCapacityReleased(t, srv)
		})
	}
}

func TestRunnerScopeCapacityReleasesFailedWrite(t *testing.T) {
	for _, dimension := range []string{"account", "repository"} {
		t.Run(dimension, func(t *testing.T) {
			srv, store, _, release := admissionTestFixture(t, "creating_state_persist", 10, 0)
			if dimension == "account" {
				srv.cfg.RunnerAccountConcurrency = map[string]int{"o": 1}
			} else {
				srv.cfg.RunnerRepositoryConcurrency = map[string]int{"o/r": 1}
			}
			store.writeFailure = errors.New("creating failed")
			a := startAdmissionTestRunner(t, srv, "a")
			awaitAdmissionTest(t, store.entered, "creating did not block")
			awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, "b"), "capacity check stalled")
			assertAdmissionState(t, store.Store, "b", state.StatusQueued)
			release()
			awaitAdmissionTest(t, a, "failed write stalled")
			assertCapacityReleased(t, srv)
			awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, "b"), "released slot was not reusable")
			assertAdmissionState(t, store.Store, "b", state.StatusRunning)
			assertCapacityReleased(t, srv)
		})
	}
}

func TestRunnerScopeCapacityQueuePassesBusyRepository(t *testing.T) {
	for _, dimension := range []string{"account", "repository"} {
		t.Run(dimension, func(t *testing.T) {
			srv, store, _, _ := admissionTestFixture(t, "", 10, 0)
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
			if err := store.Store.ReleaseLease("b", "worker"); err != nil {
				t.Fatal(err)
			}
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
				req, _, claimed, err := store.Store.ClaimNextRunnable("worker", time.Now(), time.Minute)
				if err != nil || !claimed {
					t.Fatalf("queue exhausted before independent request: %v", err)
				}
				awaitAdmissionTest(t, startAdmissionTestRunner(t, srv, req.ID), "claimed request stalled")
				if req.ID == "independent" {
					if i != 21 {
						t.Fatalf("fixture did not exercise backlog: skipped %d requests", i)
					}
					assertAdmissionState(t, store.Store, req.ID, state.StatusRunning)
					assertCapacityReleased(t, srv)
					return
				}
				queued := assertAdmissionState(t, store.Store, req.ID, state.StatusQueued)
				if queued.LeaseOwner != "" || queued.NextRetryAt.IsZero() {
					t.Fatal("blocked request retained queue lease")
				}
			}
			t.Fatal("busy scope blocked independent request")
		})
	}
}
