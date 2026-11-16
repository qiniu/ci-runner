package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestResolveUnknownCreationEndpoint(t *testing.T) {
	for _, scenario := range []string{"success", "not admin", "not authenticated", "no attestation", "stale version", "stale attempt", "recent", "sandbox exists", "lookup error", "job running", "github runner exists", "receipt during verification", "audit failure", "lease lost"} {
		t.Run(scenario, func(t *testing.T) {
			store := state.New(t.TempDir())
			_, _, err := store.EnsureAccountForOAuthIdentity(state.OAuthIdentity{OAuthProvider: "github", OAuthSubject: "12345", OAuthLogin: "octocat"}, "admin")
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = store.EnsureAccountForOAuthIdentity(state.OAuthIdentity{OAuthProvider: "github", OAuthSubject: "678", OAuthLogin: "user"}, "user")
			if err != nil {
				t.Fatal(err)
			}
			_, st, err := store.CreateRequest(state.RunnerRequest{ID: "unknown", JobID: 123, RunnerName: "e2b-123", RepositoryFullName: "o/r", Labels: []string{"linux"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			st.Status = state.StatusCreating
			st.ProvisionAttemptID = "attempt"
			st.CreatingAt = time.Now().Add(-time.Hour)
			if err := store.WriteState(st); err != nil {
				t.Fatal(err)
			}
			st, _ = store.ReadState(st.ID)
			if ok, err := store.ClaimRunnerWatch(t.Context(), st, "creator", time.Minute); !ok || err != nil {
				t.Fatal(err)
			}
			st, _ = store.ReadState(st.ID)
			if err := store.DispatchSandboxCreate(t.Context(), st.ID, "creator", st.ProvisionAttemptID); err != nil {
				t.Fatal(err)
			}
			st, _ = store.ReadState(st.ID)
			st.Status = state.StatusStopping
			st.StoppingAt = time.Now().Add(-time.Hour)
			st.LastErrorCode = "sandbox_create_outcome_unknown"
			st.LastErrorMessage = "deadline_exceeded"
			if scenario == "recent" {
				st.StoppingAt = time.Now()
			}
			if err := store.BeginRunnerCleanup(t.Context(), st, "creator", "initial-cleanup", time.Minute); err != nil {
				t.Fatal(err)
			}
			if err := store.ReleaseRunnerCleanup(t.Context(), st.ID, "initial-cleanup"); err != nil {
				t.Fatal(err)
			}
			st, _ = store.ReadState(st.ID)
			original := st
			fake := &resolutionSandbox{fakeSandbox: &fakeSandbox{}, lookupErr: sandboxrunner.ErrSandboxNotFound}
			if scenario == "sandbox exists" {
				fake.lookupErr = nil
				fake.recoverResult.SandboxID = "present"
			}
			if scenario == "lookup error" {
				fake.lookupErr = errors.New("credential-canary")
			}
			gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/o/r/actions/jobs/123":
					if scenario == "receipt during verification" {
						if err := store.RecordSandboxCreateResult(t.Context(), original, "late-box"); err != nil {
							t.Error(err)
						}
					}
					if scenario == "lease lost" {
						current, _ := store.ReadState(st.ID)
						if err := store.ReleaseRunnerCleanup(t.Context(), st.ID, current.WatchToken); err != nil {
							t.Error(err)
						}
					}
					status := "completed"
					if scenario == "job running" {
						status = "in_progress"
					}
					writeJSON(w, 200, map[string]any{"id": 123, "status": status, "conclusion": "success"})
				case "/repos/o/r/actions/runners":
					runners := []map[string]any{}
					if scenario == "github runner exists" {
						runners = append(runners, map[string]any{"id": 77, "name": "e2b-123"})
					}
					writeJSON(w, 200, map[string]any{"runners": runners})
				default:
					t.Errorf("unexpected remote call %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer gh.Close()
			actualStore := store
			if scenario == "audit failure" {
				actualStore = &resolutionFailingStore{Store: store}
			}
			srv := New(config.Config{AuthSessionSecret: "session-secret", AuthEncryptionKey: "encryption-key", WorkerLeaseTTL: time.Minute, SandboxStopTimeout: time.Second}, actualStore, github.NewClient(gh.URL, http.DefaultClient), fake, nil)
			defer srv.Close()
			input := unknownCreationResolutionRequest{Version: st.Version, AttemptID: st.ProvisionAttemptID, Evidence: "Operator verified original dispatch was canceled and no resource remains", NoSandboxOrPendingCreation: true}
			if scenario == "no attestation" {
				input.NoSandboxOrPendingCreation = false
			}
			if scenario == "stale version" {
				input.Version--
			}
			if scenario == "stale attempt" {
				input.AttemptID = "old"
			}
			body, _ := json.Marshal(input)
			req := adminRequest("POST", "/runner_requests/unknown/resolve-unknown-creation", bytes.NewReader(body))
			if scenario == "not authenticated" {
				req.Header.Del("Cookie")
			}
			if scenario == "not admin" {
				req.Header.Del("Cookie")
				req.AddCookie(testSessionCookie("678", "user", "user"))
			}
			rec := httptest.NewRecorder()
			srv.ServeHTTP(rec, req)
			got, _ := store.ReadState(st.ID)
			audits, _ := store.ListAuditEvents(10)
			if scenario == "success" {
				if rec.Code != 200 || got.Status != state.StatusCompleted || got.CleanupSandboxDone || len(audits) != 1 || audits[0].Actor != "github:12345" {
					t.Fatalf("resolution failed: status=%d body=%s state=%+v audit=%+v", rec.Code, rec.Body.String(), got, audits)
				}
				// Duplicate submission cannot create another audit or replay provider mutations.
				rec2 := httptest.NewRecorder()
				srv.ServeHTTP(rec2, adminRequest("POST", "/runner_requests/unknown/resolve-unknown-creation", bytes.NewReader(body)))
				if rec2.Code != 409 {
					t.Fatal(rec2.Code)
				}
			} else {
				if rec.Code < 400 || got.Status != state.StatusStopping || len(audits) != 0 {
					t.Fatalf("unsafe resolution %s: status=%d body=%s state=%s audits=%d", scenario, rec.Code, rec.Body.String(), got.Status, len(audits))
				}
				if strings.Contains(rec.Body.String(), "credential-canary") {
					t.Fatal("provider error leaked")
				}
			}
			if fake.stoppedCount() != 0 {
				t.Fatal("resolution must not fabricate a remote stop")
			}
		})
	}
}

type resolutionFailingStore struct{ state.Store }

func (s *resolutionFailingStore) ResolveUnknownSandboxCreation(_ context.Context, _ state.RunnerState, _ string, _ state.AuditEvent) error {
	return state.ErrAuditEventPersistence
}

type resolutionSandbox struct {
	*fakeSandbox
	lookupErr error
}

func (s *resolutionSandbox) FindRunnerSandbox(ctx context.Context, id, attempt string) (string, error) {
	if s.lookupErr != nil {
		return "", s.lookupErr
	}
	return s.fakeSandbox.FindRunnerSandbox(ctx, id, attempt)
}
