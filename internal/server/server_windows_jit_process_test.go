package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestWindowsJITRegistrationProcess(t *testing.T) {
	const id = "lost-jit"
	if dir := os.Getenv("RUNNERD_TEST_JIT_DIR"); dir != "" {
		store := state.New(dir)
		if dsn := os.Getenv("RUNNERD_TEST_JIT_DSN"); dsn != "" {
			store = state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: dsn})
		}
		srv := newRunnerLifecycleTestServer(t, store, os.Getenv("RUNNERD_TEST_JIT_GITHUB"), &fakeSandbox{})
		srv.cfg.WorkerLeaseTTL = time.Second
		req, err := store.ReadRequest(id)
		if err != nil {
			t.Fatal(err)
		}
		st, err := store.ReadState(id)
		if err != nil {
			t.Fatal(err)
		}
		_, err = srv.prepareWindowsJIT(t.Context(), req, st)
		if err != nil && os.Getenv("RUNNERD_TEST_JIT_MODE") != "reject" {
			t.Fatal(err)
		}
		return
	}
	for _, mode := range []string{"crash-stop", "crash-recover", "crash-completed", "late-id", "reject"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			store := state.New(dir)
			var dsn string
			if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") == "1" {
				dsn = catalogServerTestDSN(t)
				store = state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: dsn, MigrateOnStart: true})
			}
			_, st, err := store.CreateRequest(state.RunnerRequest{ID: id, RepositoryFullName: "o/r", RunnerName: "e2b-lost-jit", JobID: 123, Labels: []string{"self-hosted", "windows"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			st.Status, st.ProvisionAttemptID = state.StatusCreating, "lost-attempt"
			st.LastAttemptAt = time.Now().UTC()
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			st, err = store.ReadState(id)
			if err != nil {
				t.Fatal(err)
			}
			if ok, err := store.ClaimRunnerWatch(t.Context(), st, "original", time.Second); !ok || err != nil {
				t.Fatal("original failed to claim", err)
			}
			var registrationVisible, removed atomic.Bool
			var posts, deletes atomic.Int32
			accepted := make(chan struct{})
			release := make(chan struct{})
			finish := sync.OnceFunc(func() { close(release) })
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST" && r.URL.Path == "/repos/o/r/actions/runners/generate-jitconfig":
					if posts.Add(1) != 1 {
						t.Error("JIT POST repeated")
						w.WriteHeader(409)
						return
					}
					var params struct{ Labels []string }
					if err := json.NewDecoder(r.Body).Decode(&params); err != nil || !slices.Contains(params.Labels, "runnerd-jit-lost-attempt") {
						t.Error("missing attempt label", err)
					}
					close(accepted)
					select {
					case <-r.Context().Done():
						return
					case <-release:
					}
					if mode == "reject" {
						w.WriteHeader(429)
						return
					}
					w.WriteHeader(201)
					fmt.Fprintf(w, `{"runner":{"id":71,"name":"e2b-lost-jit"},"encoded_jit_config":"%s"}`, jitTestConfig(t, "e2b-lost-jit", "71"))
				case r.Method == "GET" && r.URL.Path == "/repos/o/r/actions/jobs/123":
					_, _ = w.Write([]byte(`{"id":123,"status":"queued"}`))
				case r.Method == "GET" && r.URL.Path == "/repos/o/r/actions/runners":
					if registrationVisible.Load() && !removed.Load() {
						w.Write([]byte(`{"runners":[{"id":71,"name":"e2b-lost-jit","labels":[{"name":"runnerd-jit-lost-attempt"}]}]}`))
					} else {
						w.Write([]byte(`{"runners":[{"id":99,"name":"e2b-lost-jit","labels":[{"name":"runnerd-jit-other-attempt"}]}]}`))
					}
				case r.Method == "DELETE" && r.URL.Path == "/repos/o/r/actions/runners/71":
					deletes.Add(1)
					removed.Store(true)
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected call %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer gh.Close()
			defer finish()
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWindowsJITRegistrationProcess$")
			cmd.Env = append(os.Environ(), "RUNNERD_TEST_JIT_DIR="+dir, "RUNNERD_TEST_JIT_GITHUB="+gh.URL, "RUNNERD_TEST_JIT_DSN="+dsn, "RUNNERD_TEST_JIT_MODE="+mode)
			var childLog bytes.Buffer
			cmd.Stdout, cmd.Stderr = &childLog, &childLog
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					cmd.Process.Kill()
					cmd.Wait()
				}
				if t.Failed() {
					t.Log(childLog.String())
				}
			}()
			select {
			case <-accepted:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if strings.HasPrefix(mode, "crash-") {
				if err := cmd.Process.Kill(); err != nil {
					t.Fatal(err)
				}
				if err := cmd.Wait(); err == nil {
					t.Fatal("expected killed child")
				}
			}
			b := newRunnerLifecycleTestServer(t, store, gh.URL, &fakeSandbox{})
			if mode == "crash-recover" || mode == "crash-completed" {
				for ctx.Err() == nil {
					if err := b.recoverRunner(ctx, id); err != nil {
						t.Fatal(err)
					}
					row, err := store.ReadState(id)
					if err != nil {
						t.Fatal(err)
					}
					if row.Status == state.StatusStopping {
						break
					}
					time.Sleep(40 * time.Millisecond)
				}
			} else {
				if _, _, err := b.stopRunner(ctx, id, github.WorkflowJob{}, state.TerminationSourceManualStop); err != nil {
					t.Fatal(err)
				}
			}
			pending := assertAdmissionState(t, store, id, state.StatusStopping)
			if pending.JITRegistrationState != state.JITDispatched || pending.CleanupGitHubRunnerID != 0 || pending.CleanupGitHubDone || posts.Load() != 1 || deletes.Load() != 0 {
				t.Fatal("unresolved registration lost its intent or deleted the wrong Runner")
			}
			if n, err := store.InFlightCount(); err != nil || n != 1 {
				t.Fatal("released pending capacity", n, err)
			}
			switch mode {
			case "late-id", "reject":
				finish()
				if err := cmd.Wait(); err != nil {
					t.Fatal(err)
				}
			case "crash-completed":
				job := github.WorkflowJob{ID: 123, Status: "completed", Conclusion: "cancelled"}
				if _, _, err := b.stopRunner(ctx, id, job, state.TerminationSourceWorkflowJobWebhook); err != nil {
					t.Fatal(err)
				}
				registrationVisible.Store(true)
			case "crash-stop", "crash-recover":
				registrationVisible.Store(true)
			}
			if err := b.recoverRunner(ctx, id); err != nil {
				t.Fatal(err)
			}
			current, err := store.ReadState(id)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "crash-recover" {
				if current.Status != state.StatusQueued || current.RetryCount != 1 || current.NextRetryAt.IsZero() || current.JITRegistrationState == state.JITDispatched || current.CleanupGitHubRunnerID != 0 {
					t.Fatalf("invalid recovery: status=%s retries=%d", current.Status, current.RetryCount)
				}
			} else if mode == "crash-completed" {
				if current.Status != state.StatusCompleted || current.GitHubJobConclusion != "cancelled" || current.RetryCount != 0 || current.TerminationSource != state.TerminationSourceFailureCleanup {
					t.Fatal("original Job completion lost during registration cleanup", current.Status, current.GitHubJobConclusion)
				}
			} else if current.Status != state.StatusCompleted || current.TerminationSource != state.TerminationSourceManualStop || !current.CleanupGitHubDone {
				t.Fatalf("manual stop lost: status=%s reason=%s", current.Status, current.FailureReason)
			}
			if mode == "reject" {
				if deletes.Load() != 0 || current.JITRegistrationState == state.JITDispatched {
					t.Fatal("definitive rejection caused speculative cleanup")
				}
			} else if !removed.Load() || deletes.Load() != 1 || posts.Load() != 1 {
				t.Fatal("original registration was not cleaned exactly once")
			}
		})
	}
}
