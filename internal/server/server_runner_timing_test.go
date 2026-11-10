package server

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type startupTimingStore struct {
	state.Store
	before           func(string) error
	sandbox          *lifecycleSandboxService
	initialRead      atomic.Bool
	afterInitialRead func()
	capacityStage    string
}

func (s *startupTimingStore) WithRunnerAdmission(ctx context.Context, mutation func(state.Store) error) error {
	if err := s.before("admission_lock_wait"); err != nil {
		return err
	}
	return s.Store.WithRunnerAdmission(ctx, func(tx state.Store) error {
		return mutation(&startupTimingStore{Store: tx, before: s.before, capacityStage: s.capacityStage})
	})
}

func (s *startupTimingStore) ReadRequest(id string) (state.RunnerRequest, error) {
	if err := s.before("request_read"); err != nil {
		return state.RunnerRequest{}, err
	}
	return s.Store.ReadRequest(id)
}

func (s *startupTimingStore) ReadState(id string) (state.RunnerState, error) {
	initial := !s.initialRead.Swap(true)
	stage := "other_state_read"
	if initial {
		stage = "initial_state_read"
	} else if len(s.sandbox.startInputs()) > 0 {
		stage = "running_state_read"
	}
	if err := s.before(stage); err != nil {
		return state.RunnerState{}, err
	}
	st, err := s.Store.ReadState(id)
	if initial && s.afterInitialRead != nil {
		s.afterInitialRead()
	}
	return st, err
}

func (s *startupTimingStore) GetProfile(name string) (state.RunnerProfile, error) {
	if err := s.before("profile_lookup"); err != nil {
		return state.RunnerProfile{}, err
	}
	return s.Store.GetProfile(name)
}

func (s *startupTimingStore) RunnerCapacityCounts(req state.RunnerRequest) (state.RunnerCapacityCounts, error) {
	if err := s.before("capacity_count"); err != nil {
		return state.RunnerCapacityCounts{}, err
	}
	counts, err := s.Store.RunnerCapacityCounts(req)
	if s.capacityStage == "global_capacity_check" {
		counts.Global = 10
	}
	if s.capacityStage == "profile_capacity_check" {
		counts.Profile = 10
	}
	return counts, err
}

func newStartupTimingFixture(t *testing.T) (*Server, *startupTimingStore, chan string) {
	t.Helper()
	base := state.New(t.TempDir())
	upsertLifecycleProfile(t, base, state.RunnerProfile{
		Name: "custom", Labels: []string{"self-hosted", "custom"},
		TemplateID: "custom-id", Enabled: true, MaxConcurrency: 10,
	})
	if _, _, err := base.CreateRequest(state.RunnerRequest{
		ID: "timing-start", RepositoryFullName: "o/r", ProfileName: "custom",
		Labels: []string{"self-hosted", "custom"},
	}, nil); err != nil {
		t.Fatal(err)
	}
	sandbox := &lifecycleSandboxService{events: &lifecycleEventRecorder{}}
	store := &startupTimingStore{Store: base, sandbox: sandbox, before: func(string) error { return nil }}
	batches := make(chan string, 10)
	observer := &timingObservingStore{
		Store: store, onReadRequest: func() {}, onTiming: func(batch string) { batches <- batch },
	}
	gh := httptest.NewServer(githubRunnerAPI(t))
	t.Cleanup(gh.Close)
	srv := newRunnerLifecycleTestServer(t, observer, gh.URL, sandbox)
	// The constructor may inspect the Store. Only the explicit start is under test.
	srv.Close()
	store.initialRead.Store(false)
	return srv, store, batches
}

type startupTimingSample struct {
	duration time.Duration
	observed time.Time
}

func parseStartupTimings(t *testing.T, batch string) map[string]startupTimingSample {
	t.Helper()
	samples := make(map[string]startupTimingSample)
	for _, line := range strings.Split(strings.TrimSpace(batch), "\n") {
		var stage, clock, observed string
		var ms int64
		if n, err := fmt.Sscanf(line, "stage=%s duration_ms=%d clock=%s observed_at=%s", &stage, &ms, &clock, &observed); err != nil || n != 4 {
			t.Fatalf("invalid timing line: %q", line)
		}
		end, err := time.Parse(time.RFC3339Nano, observed)
		if err != nil {
			t.Fatal(err)
		}
		if clock != "monotonic" && stage != "queue_wait" {
			t.Errorf("stage %s clock = %s", stage, clock)
		}
		if _, exists := samples[stage]; exists {
			t.Errorf("duplicate stage %s", stage)
		}
		samples[stage] = startupTimingSample{duration: time.Duration(ms) * time.Millisecond, observed: end}
	}
	return samples
}

func TestRunnerStartupAttributesStoreDelays(t *testing.T) {
	for _, stage := range []string{"request_read", "initial_state_read", "profile_lookup", "capacity_count", "running_state_read"} {
		for _, fail := range []bool{false, true} {
			// An unreadable request deliberately retains the existing no-event behavior.
			if stage == "request_read" && fail {
				continue
			}
			t.Run(fmt.Sprintf("%s/error_%t", stage, fail), func(t *testing.T) {
				srv, store, batches := newStartupTimingFixture(t)
				claim := queuedRunnerForTest(t, store.Store, "timing-start", "test-worker")
				var injected atomic.Bool
				var entered, released time.Time
				store.before = func(operation string) error {
					if operation != stage || injected.Swap(true) {
						return nil
					}
					entered = time.Now()
					time.Sleep(40 * time.Millisecond)
					released = time.Now()
					if fail {
						return errors.New("fixture-private-database-error")
					}
					return nil
				}
				done := make(chan struct{})
				go func() { srv.startClaimedRunner(t.Context(), claim); close(done) }()
				var batch string
				select {
				case batch = <-batches:
				case <-time.After(5 * time.Second):
					t.Fatal("startup timing was not flushed")
				}
				samples := parseStartupTimings(t, batch)
				got, ok := samples[stage]
				if !ok {
					t.Fatalf("missing %s: %s", stage, batch)
				}
				if got.duration < 40*time.Millisecond || got.observed.Before(released) || got.observed.Add(-got.duration-time.Millisecond).After(entered) {
					t.Errorf("%s does not cover injected wait [%v, %v]: %#v", stage, entered, released, got)
				}
				lockStage := "admission_lock_wait"
				if stage == "request_read" || stage == "initial_state_read" {
					lockStage = "initial_lock_wait"
				}
				if stage == "running_state_read" {
					lockStage = "running_lock_wait"
				}
				if samples[lockStage].observed.After(entered) {
					t.Errorf("%s includes Store wait for %s", lockStage, stage)
				}
				if strings.Contains(batch, "fixture-private") {
					t.Error("timing leaked Store error text")
				}
				if fail {
					later := "creating_state_persist"
					if stage == "running_state_read" {
						later = "running_state_persist"
					}
					if _, ok := samples[later]; ok {
						t.Errorf("failed %s fabricated later %s", stage, later)
					}
				} else {
					st, err := store.Store.ReadState("timing-start")
					if err != nil || st.Status != state.StatusRunning {
						t.Fatalf("successful start = %#v, %v", st, err)
					}
					store.sandbox.startInputs()[0].OnExit(sandboxrunner.ExitResult{}, nil)
				}
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("startup did not finish")
				}
			})
		}
	}
}

func TestRunnerStartupRecordsAdmissionWaitBeforeProfileLookup(t *testing.T) {
	srv, store, batches := newStartupTimingFixture(t)
	claim := queuedRunnerForTest(t, store.Store, "timing-start", "test-worker")
	entered, release := make(chan struct{}), make(chan struct{})
	store.before = func(stage string) error {
		if stage == "admission_lock_wait" {
			close(entered)
			<-release
		}
		return nil
	}
	done := make(chan struct{})
	go func() { srv.startClaimedRunner(t.Context(), claim); close(done) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("admission did not begin")
	}
	time.Sleep(50 * time.Millisecond)
	released := time.Now()
	close(release)
	var batch string
	select {
	case batch = <-batches:
	case <-time.After(5 * time.Second):
		t.Fatal("startup timing was not flushed")
	}
	samples := parseStartupTimings(t, batch)
	wait := samples["admission_lock_wait"]
	if wait.duration < 40*time.Millisecond || wait.observed.Before(released) {
		t.Errorf("admission wait does not cover contention: %#v", wait)
	}
	lookup := samples["profile_lookup"]
	if lookup.observed.Add(-lookup.duration).Before(released) {
		t.Error("profile lookup includes admission wait")
	}
	store.sandbox.startInputs()[0].OnExit(sandboxrunner.ExitResult{}, nil)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not finish")
	}
}

func TestRunnerStartupSkipsTimingsWhenCapacityDefers(t *testing.T) {
	for _, stage := range []string{"global_capacity_check", "profile_capacity_check"} {
		t.Run(stage, func(t *testing.T) {
			srv, store, batches := newStartupTimingFixture(t)
			claim := queuedRunnerForTest(t, store.Store, "timing-start", "test-worker")
			store.capacityStage = stage
			srv.startClaimedRunner(t.Context(), claim)
			select {
			case batch := <-batches:
				t.Fatalf("capacity-only deferral appended timing events: %s", batch)
			default:
			}
			st, err := store.Store.ReadState("timing-start")
			if err != nil || st.Status != state.StatusQueued || len(store.sandbox.startInputs()) != 0 {
				t.Fatalf("capacity deferral changed admission behavior: %#v, %v", st, err)
			}
		})
	}
}
