package server

import (
	"errors"
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
	stage          string
	armed, used    atomic.Bool
	entered        chan struct{}
	release        chan struct{}
	writeFailure   error
	writeConflict  bool
	progressMu     sync.Mutex
	progress       map[string][]func()
	ready, workers sync.WaitGroup
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
	profile, err := s.Store.GetProfile(name)
	s.block("profile_lookup")
	return profile, err
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
	gh := httptest.NewServer(githubRunnerAPI(t))
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
	if srv.runnerCapacity.pending != 0 || len(srv.runnerCapacity.profiles) != 0 {
		t.Fatal("capacity reservations or profile observations leaked")
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
