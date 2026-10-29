package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type windowsJITSandbox struct {
	*lifecycleSandboxService
	fail bool
}

func (s *windowsJITSandbox) StartRunner(ctx context.Context, input sandboxrunner.StartInput) (sandboxrunner.StartResult, error) {
	if s.fail {
		return sandboxrunner.StartResult{}, errors.New("sandbox unavailable")
	}
	return s.lifecycleSandboxService.StartRunner(ctx, input)
}

func TestWindowsLifecycleUsesJITAndCleansUnadoptedRegistration(t *testing.T) {
	for _, scenario := range []string{"success", "sandbox failure", "cleanup failure", "lost response", "rate limited", "secondary rate limited"} {
		t.Run(scenario, func(t *testing.T) {
			fail := scenario != "success"
			var cleanupUnavailable atomic.Bool
			cleanupUnavailable.Store(scenario == "cleanup failure")
			var removed atomic.Bool
			var registered atomic.Bool
			var posts atomic.Int64
			ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/runners":
					if posts.Load() > 0 && (scenario == "rate limited" || scenario == "secondary rate limited") {
						t.Error("definitively rejected JIT must not reconcile")
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					if registered.Load() {
						_, _ = w.Write([]byte(`{"runners":[{"id":71,"name":"e2b-win-jit"}]}`))
					} else {
						_, _ = w.Write([]byte(`{"runners":[]}`))
					}
				case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/runners/downloads":
					_, _ = w.Write([]byte(`[{"os":"win","architecture":"x64","filename":"actions-runner-win-x64-2.337.0.zip","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-win-x64-2.337.0.zip","sha256_checksum":"1150692afa94e71f872017e254ea55b6eece1eece3fe7e3a6d4c93d0a1b85cfc"}]`))
				case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/actions/runners/generate-jitconfig":
					posts.Add(1)
					if scenario == "rate limited" {
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					if scenario == "secondary rate limited" {
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte("secondary rate limit"))
						return
					}
					registered.Store(true)
					if scenario == "lost response" {
						w.WriteHeader(http.StatusGatewayTimeout)
						return
					}
					w.WriteHeader(201)
					_, _ = w.Write([]byte(`{"runner":{"id":71,"name":"e2b-win-jit"},"encoded_jit_config":"` + jitTestConfig(t, "e2b-win-jit", "71") + `"}`))
				case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/actions/runners/71":
					if cleanupUnavailable.Load() {
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					registered.Store(false)
					removed.Store(true)
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected GitHub request (must not request registration token): %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer ghServer.Close()
			store := &windowsTimestampPrecisionStore{Store: state.New(t.TempDir())}
			upsertLifecycleProfile(t, store, state.RunnerProfile{Name: "windows", Labels: []string{"self-hosted", "windows"}, TemplateID: "windows-template", MaxConcurrency: 1, Enabled: true})
			service := &windowsJITSandbox{lifecycleSandboxService: &lifecycleSandboxService{}, fail: fail}
			srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, service)
			createLifecycleRequest(t, store, "win-jit", "windows", 0)
			if scenario == "rate limited" || scenario == "secondary rate limited" {
				st, _ := store.ReadState("win-jit")
				st.RetryCount = srv.cfg.RetryMaxAttempts
				if err := store.WriteState(st); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			// Let the successful fake command exit after the state reaches running.
			if fail {
				srv.startRunner(ctx, "win-jit", "test-worker")
				if scenario == "rate limited" || scenario == "secondary rate limited" {
					pending, err := store.ReadState("win-jit")
					if err != nil || pending.Status != state.StatusQueued || pending.NextRetryAt.IsZero() || removed.Load() {
						t.Fatalf("lost rate-limit deferral: status=%s reason=%s err=%v", pending.Status, pending.FailureReason, err)
					}
					return
				}
				if scenario == "cleanup failure" {
					pending, err := store.ReadState("win-jit")
					if err != nil || pending.Status != state.StatusStopping || pending.FailureReason != "jit_cleanup_pending" {
						t.Fatalf("cleanup failure lost: status=%s reason=%s err=%v", pending.Status, pending.FailureReason, err)
					}
					cleanupUnavailable.Store(false)
					if err := srv.Recover(ctx); err != nil {
						t.Fatal(err)
					}
					final, err := store.ReadState("win-jit")
					if err != nil || final.Status != state.StatusFailed {
						t.Fatalf("cleanup recovery status=%s err=%v", final.Status, err)
					}
				}
				if !removed.Load() {
					t.Fatal("pre-created Runner leaked after Sandbox failure")
				}
			} else {
				go srv.startRunner(ctx, "win-jit", "test-worker")
				waitForState(t, store, "win-jit", state.StatusRunning)
				in := service.startInputs()
				if len(in) != 1 || in[0].RegistrationToken != "" || in[0].JITConfig == "" || in[0].RequireDocker {
					t.Fatal("Windows received unsafe registration or Docker policy")
				}
				if removed.Load() {
					t.Fatal("active adopted JIT registration removed")
				}
				in[0].OnExit(sandboxrunner.ExitResult{}, nil)
			}
		})
	}
}

func jitTestConfig(t *testing.T, name, id string) string {
	t.Helper()
	settings, err := json.Marshal(map[string]string{"AgentId": id, "AgentName": name, "Ephemeral": "True", "DisableUpdate": "True", "WorkFolder": `..\work`})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{".runner": base64.StdEncoding.EncodeToString(settings), ".credentials": "e30=", ".credentials_rsaparams": "e30="})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

// The names are reused across retries: stale work must never delete or fail
// the replacement, including after GitHub returned no identity.
func TestWindowsJITStaleAttemptCannotMutateReplacement(t *testing.T) {
	var calls atomic.Int64
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ghServer.Close()
	store := state.New(t.TempDir())
	upsertLifecycleProfile(t, store, state.RunnerProfile{Name: "windows", Labels: []string{"self-hosted", "windows"}, Enabled: true, TemplateID: "win", MaxConcurrency: 1})
	createLifecycleRequest(t, store, "stale-jit", "windows", 0)
	srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, &lifecycleSandboxService{})
	old, _ := store.ReadState("stale-jit")
	old.Status = state.StatusCreating
	old.LastAttemptAt = time.Now().UTC()
	if err := store.WriteState(old); err != nil {
		t.Fatal(err)
	}
	old, _ = store.ReadState(old.ID)
	next := old
	next.LastAttemptAt = old.LastAttemptAt.Add(time.Second)
	if err := store.WriteState(next); err != nil {
		t.Fatal(err)
	}
	req, _ := store.ReadRequest(old.ID)
	if _, err := srv.prepareWindowsJIT(t.Context(), req, old.LastAttemptAt); !errors.Is(err, errRunnerCreationSuperseded) {
		t.Fatalf("stale registration: %v", err)
	}
	if err := srv.cleanupWindowsJIT(req, old.LastAttemptAt, 0); err != nil {
		t.Fatal(err)
	}
	srv.retainWindowsJITCleanup(old.ID, old.LastAttemptAt)
	srv.failStartForAttempt(old.ID, old, "sandbox_start", errors.New("temporary failure"), &old.LastAttemptAt)
	final, _ := store.ReadState(old.ID)
	if calls.Load() != 0 || !final.LastAttemptAt.Equal(next.LastAttemptAt) || final.Status != state.StatusCreating || final.Error != "" {
		t.Fatalf("stale attempt changed replacement: calls=%d status=%s", calls.Load(), final.Status)
	}
}

func TestWindowsJITRegistrationSerializesCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"runners":[]}`))
			return
		}
		close(entered)
		<-release
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"runner":{"id":71,"name":"e2b-race-jit"},"encoded_jit_config":"` + jitTestConfig(t, "e2b-race-jit", "71") + `"}`))
	}))
	defer ghServer.Close()
	store := state.New(t.TempDir())
	upsertLifecycleProfile(t, store, state.RunnerProfile{Name: "windows", Labels: []string{"self-hosted", "windows"}, Enabled: true, TemplateID: "win", MaxConcurrency: 1})
	createLifecycleRequest(t, store, "race-jit", "windows", 0)
	srv := newRunnerLifecycleTestServer(t, store, ghServer.URL, &lifecycleSandboxService{})
	st, _ := store.ReadState("race-jit")
	st.Status = state.StatusCreating
	st.LastAttemptAt = time.Now().UTC()
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	req, _ := store.ReadRequest(st.ID)
	done := make(chan error, 1)
	go func() { _, err := srv.prepareWindowsJIT(t.Context(), req, st.LastAttemptAt); done <- err }()
	<-entered
	// A competing lifecycle mutation must use this same lock. Release POST,
	// then confirm it can finish without leaving a later registration in flight.
	cancelled := make(chan struct{})
	go func() {
		unlock := srv.lockRunner(st.ID)
		defer unlock()
		current, _ := store.ReadState(st.ID)
		current.Status = state.StatusCompleted
		_ = store.WriteState(current)
		close(cancelled)
	}()
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-cancelled
	final, _ := store.ReadState(st.ID)
	if final.Status != state.StatusCompleted {
		t.Fatal("cancellation lost")
	}
}

// Model MySQL datetime(3), the lowest precision of the supported fresh schemas.
// A nanosecond in-memory attempt token must not reject its own persisted row.
type windowsTimestampPrecisionStore struct{ state.Store }

func (s *windowsTimestampPrecisionStore) WriteState(st state.RunnerState) error {
	st.LastAttemptAt = st.LastAttemptAt.Truncate(time.Millisecond)
	return s.Store.WriteState(st)
}
