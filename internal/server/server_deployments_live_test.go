package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	deploy "github.com/miclle/deployd"
	deploygit "github.com/miclle/deployd/source/git"
	"github.com/qiniu/ci-runner/internal/config"
	deployconfig "github.com/qiniu/ci-runner/internal/deployment/config"
	deployruntime "github.com/qiniu/ci-runner/internal/deployment/runtime"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestDeploymentLivePublicWorker(t *testing.T) {
	if os.Getenv("RUNNERD_DEPLOY_LIVE") != "1" {
		t.Skip("manual real Sandbox acceptance")
	}
	configPath := os.Getenv("RUNNERD_DEPLOY_LIVE_CONFIG")
	if !filepath.IsAbs(configPath) {
		t.Fatal("RUNNERD_DEPLOY_LIVE_CONFIG must be an absolute local config path")
	}
	accountID, err := strconv.ParseInt(os.Getenv("RUNNERD_DEPLOY_LIVE_ACCOUNT_ID"), 10, 64)
	if err != nil || accountID <= 0 {
		t.Fatal("RUNNERD_DEPLOY_LIVE_ACCOUNT_ID must identify a configured account")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal("local config load failed")
	}

	cfg.DeploymentWorkRoot = "/tmp/runnerd-deploy125-acceptance"
	original := state.NewWithOptions(state.Options{Backend: cfg.StateBackend, DatabaseDSN: cfg.StateDatabaseDSN.Value()})
	srv := New(cfg, original, nil, nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	service, snapshot, err := srv.sandboxServiceForDeployment(ctx, accountPreferenceScope{Type: state.AccountScopeTypeAccount, ID: accountID})
	if err != nil {
		t.Fatal("account Sandbox config unavailable")
	}
	template := os.Getenv("RUNNERD_DEPLOY_LIVE_TEMPLATE")
	if template == "" {
		template = "base"
	}
	cfg.DeploymentTemplateID = template
	if catalog, ok := service.(sandboxrunner.DefaultTemplateCatalog); ok {
		entries, err := catalog.ListDefaultTemplates(ctx)
		if err != nil {
			t.Fatal("public template catalog unavailable")
		}
		for _, entry := range entries {
			for _, name := range entry.Names {
				if name == template && entry.Runnable() {
					cfg.DeploymentTemplateID = entry.TemplateID
				}
			}
		}
	}
	srv.cfg.DeploymentTemplateID = cfg.DeploymentTemplateID
	// The isolated worker DB tests execution only. Repository admission through
	// GitHub OAuth/App authorization is a separate API acceptance gate.
	const commit = "fe64a78977093d7c7fd7fb9a159975c5f73f8ff6"
	response, err := (&http.Client{Timeout: 15 * time.Second}).Get("https://raw.githubusercontent.com/qiniu/deployment/" + commit + "/.qiniu/deploy.yaml")
	if err != nil {
		t.Fatal("public source config unavailable")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	response.Body.Close()
	if err != nil || response.StatusCode != http.StatusOK {
		t.Fatal("public source config read failed")
	}
	spec, err := deployconfig.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: githubDeploymentSourceURL("qiniu/deployment"), CommitSHA: commit}, spec)
	if err != nil {
		t.Fatal(err)
	}
	specJSON, _ := json.Marshal(plan.Spec())
	encrypted, err := encryptSecret(string(specJSON), cfg.AuthEncryptionKey.Value())
	if err != nil {
		t.Fatal("spec encrypt failed")
	}
	evidence := []map[string]any{}
	for _, mode := range []string{"stop", "expiry", "create-timeout", "start-failure"} {
		t.Run(mode, func(t *testing.T) {
			originalHTTP := srv.sandboxHTTP
			fault := &deploymentLostCreateResponse{base: originalHTTP.Transport, enabled: mode == "create-timeout"}
			if mode == "create-timeout" || mode == "start-failure" {
				client := *originalHTTP
				client.Transport = fault
				srv.sandboxHTTP = &client
				defer func() { srv.sandboxHTTP = originalHTTP }()
			}
			store := state.New(t.TempDir())
			worker := srv.newDeploymentWorker()
			worker.Store = store
			worker.Source = func(context.Context, state.Deployment) (deploy.Source, error) {
				return deploygit.New(githubDeploymentSourceURL("qiniu/deployment"), deploygit.Options{Ref: commit})
			}
			worker.Event = func(_ state.Deployment, event state.DeploymentEvent) {
				if err := store.AppendDeploymentEvent(event); err != nil {
					t.Error("event persistence failed")
				}
			}
			lifetime := 10 * time.Minute
			if mode == "expiry" {
				lifetime = 3 * time.Minute
			}
			modePlan, modeEncrypted := plan, encrypted
			if mode == "start-failure" {
				failedSpec := plan.Spec()
				failedSpec.InstallCommand = "true"
				failedSpec.StartCommand = "exit 23"
				modePlan, err = deploy.NewPlan(deploy.ResolvedSource{SourceID: githubDeploymentSourceURL("qiniu/deployment"), CommitSHA: commit}, failedSpec)
				if err != nil {
					t.Fatal(err)
				}
				data, err := json.Marshal(modePlan.Spec())
				if err != nil {
					t.Fatal("failure fixture encoding failed")
				}
				modeEncrypted, err = encryptSecret(string(data), cfg.AuthEncryptionKey.Value())
				if err != nil {
					t.Fatal("failure fixture encryption failed")
				}
			}
			record := state.Deployment{ID: "live125-" + mode + "-" + newID(), AccountID: accountID, ScopeType: state.AccountScopeTypeAccount, ScopeID: accountID, GitHubInstallationID: 1, RepositoryID: 1, RepositoryFullName: "qiniu/deployment", Ref: "example/vite", CommitSHA: commit, ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: modePlan.Snapshot().Version, SpecHash: modePlan.Snapshot().SpecHash, SpecJSONEncrypted: modeEncrypted, Status: state.DeploymentStatusQueued, DeploymentTemplateID: cfg.DeploymentTemplateID, SandboxAPIURL: snapshot.APIURL, SandboxAPIKeyEncrypted: snapshot.EncryptedAPIKey, SandboxConfigSource: snapshot.Source, SandboxRegion: srv.sandboxRegionForAPIURL(snapshot.APIURL), ExpiresAt: time.Now().UTC().Add(lifetime)}
			if _, err := store.CreateDeployment(record); err != nil {
				t.Fatal(err)
			}
			defer func() {
				fault.disable()
				cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
				defer cancel()
				if err := worker.Stop(cleanup, record.ID, false); err != nil {
					t.Error("deferred Sandbox cleanup failed")
				}
				if t.Failed() && (mode == "create-timeout" || mode == "start-failure") {
					// A regression may lose the local checkpoint. Retain an independent
					// cleanup path for the real resource observed by this test.
					if sandboxID, _, _ := fault.evidence(); sandboxID != "" {
						probe := record
						probe.SandboxID = sandboxID
						factory := &serverDeploymentRuntimeFactory{server: srv, templateID: cfg.DeploymentTemplateID}
						handle, err := factory.Connect(cleanup, probe)
						if err != nil || (handle != nil && handle.Close(cleanup) != nil) {
							t.Errorf("independent cleanup failed for sandbox=%s", sandboxID)
						}
					}
				}
			}()
			started := time.Now().UTC()
			if mode == "create-timeout" {
				if err := worker.Process(ctx, record.ID); !errors.Is(err, deployruntime.ErrCreatePending) {
					t.Fatalf("expected unresolved create, error_category=%T", err)
				}
				pending, err := store.GetDeployment(record.ID)
				if err != nil || pending.Status != state.DeploymentStatusCreating || pending.Attempt != 1 || pending.SandboxID != "" {
					t.Fatal("lost create response did not preserve the unresolved attempt")
				}
				fault.disable()
				sandboxID, creates, lookups := fault.evidence()
				if sandboxID == "" || creates != 1 || lookups == 0 {
					t.Fatalf("fault injection incomplete: accepted=%v creates=%d blocked_lookups=%d", sandboxID != "", creates, lookups)
				}
				resumed := srv.newDeploymentWorker()
				resumed.Store, resumed.Event = store, worker.Event
				if err := resumed.RunOnce(ctx); err != nil {
					t.Fatalf("real provider reconciliation failed, error_category=%T", err)
				}
				terminal, err := store.GetDeployment(record.ID)
				if err != nil || terminal.Status != state.DeploymentStatusFailed || terminal.FailureStage != "recovery" || terminal.Attempt != 1 || terminal.SandboxID != "" {
					t.Fatal("interrupted creation was not compensated on the original attempt")
				}
				_, creates, _ = fault.evidence()
				if creates != 1 {
					t.Fatal("reconciliation attempted a replacement create")
				}
				factory := &serverDeploymentRuntimeFactory{server: srv, templateID: cfg.DeploymentTemplateID}
				probe := pending
				probe.SandboxID = sandboxID
				if handle, err := factory.Connect(ctx, probe); err != nil || handle != nil {
					t.Fatalf("provider resource release not confirmed: exists=%v error_category=%T", handle != nil, err)
				}
				evidence = append(evidence, map[string]any{"mode": mode, "started_at": started, "finished_at": time.Now().UTC(), "deployment_id": record.ID, "sandbox_id": sandboxID, "terminal_status": terminal.Status, "failure_stage": terminal.FailureStage, "attempt": terminal.Attempt, "create_requests": creates, "blocked_initial_lookups": lookups, "provider_missing_after_cleanup": true})
				t.Logf("lost create response recovered: deployment=%s sandbox=%s attempt=%d creates=%d provider_missing=true", record.ID, sandboxID, terminal.Attempt, creates)
				return
			}
			if err := worker.Process(ctx, record.ID); err != nil {
				t.Fatal(err)
			}
			if mode == "start-failure" {
				failed, err := store.GetDeployment(record.ID)
				if err != nil || failed.Status != state.DeploymentStatusFailed || (failed.FailureStage != string(deploy.Starting) && failed.FailureStage != string(deploy.Probing)) || failed.FailureReason == "" || failed.SandboxID != "" {
					t.Fatal("failed application did not retain failure and release its Sandbox")
				}
				revision, err := store.GetDeploymentRevision(record.ID, 1)
				if err != nil || revision.Status != state.DeploymentStatusFailed || revision.FailureStage != failed.FailureStage || revision.FailureReason != failed.FailureReason {
					t.Fatal("revision lost application failure evidence")
				}
				sandboxID, creates, _ := fault.evidence()
				if sandboxID == "" || creates != 1 {
					t.Fatal("startup failure never exercised a real Sandbox")
				}
				factory := &serverDeploymentRuntimeFactory{server: srv}
				probe := record
				probe.SandboxID = sandboxID
				if handle, err := factory.Connect(ctx, probe); err != nil || handle != nil {
					t.Fatalf("failed Sandbox release unconfirmed: exists=%v error_category=%T", handle != nil, err)
				}
				evidence = append(evidence, map[string]any{"mode": mode, "started_at": started, "finished_at": time.Now().UTC(), "deployment_id": record.ID, "sandbox_id": sandboxID, "terminal_status": failed.Status, "failure_stage": failed.FailureStage, "attempt": failed.Attempt, "create_requests": creates, "provider_missing_after_cleanup": true, "revision_failure_preserved": true})
				t.Logf("application startup failure cleaned: deployment=%s sandbox=%s stage=%s provider_missing=true", record.ID, sandboxID, failed.FailureStage)
				return
			}
			running, err := store.GetDeployment(record.ID)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("deployment=%s status=%s sandbox=%s endpoint=%s expires_at=%s", running.ID, running.Status, running.SandboxID, running.Endpoint, running.ExpiresAt.Format(time.RFC3339))
			if running.Status != state.DeploymentStatusRunning {
				events, _, _ := store.ListDeploymentEvents(record.ID, 0, 20)
				for _, event := range events {
					if event.EventType != "output" {
						t.Logf("failure evidence stage=%s message=%s", event.Stage, event.Message)
					}
				}
				t.Fatal("real service did not become ready")
			}
			probeClient := &http.Client{Timeout: 15 * time.Second}
			public, err := probeClient.Get(running.Endpoint)
			if err != nil {
				t.Fatal("public endpoint probe failed")
			}
			html, _ := io.ReadAll(io.LimitReader(public.Body, 65536))
			public.Body.Close()
			if public.StatusCode != http.StatusOK || !bytes.Contains(bytes.ToLower(html), []byte("<!doctype html>")) {
				t.Fatalf("public HTTP evidence status=%d bytes=%d", public.StatusCode, len(html))
			}
			// New controller instance reuses only durable state and the exact attempt.
			resumed := srv.newDeploymentWorker()
			resumed.Store = store
			resumed.Event = worker.Event
			if err := resumed.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
			afterRestart, _ := store.GetDeployment(record.ID)
			if afterRestart.Status != state.DeploymentStatusRunning || afterRestart.SandboxID != running.SandboxID || afterRestart.Attempt != running.Attempt {
				t.Fatal("restart replaced or stopped healthy service")
			}
			if mode == "stop" {
				if err := resumed.Stop(ctx, record.ID, false); err != nil {
					t.Fatal(err)
				}
			} else {
				for time.Now().Before(running.ExpiresAt.Add(20 * time.Second)) {
					if err := resumed.RunOnce(ctx); err != nil {
						t.Logf("expiry reconciliation error category=%T", err)
					}
					latest, _ := store.GetDeployment(record.ID)
					if latest.Status == state.DeploymentStatusFailed {
						t.Fatalf("unexpected failure stage=%s reason=%s failed_at=%s expires_at=%s provider_released=%v", latest.FailureStage, latest.FailureReason, latest.FailedAt.Format(time.RFC3339Nano), latest.ExpiresAt.Format(time.RFC3339Nano), latest.SandboxID == "")
					}
					if latest.Status == state.DeploymentStatusExpired && latest.SandboxID == "" {
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("acceptance deadline exceeded")
					case <-time.After(10 * time.Second):
					}
				}
			}
			terminal, err := store.GetDeployment(record.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := state.DeploymentStatusCompleted
			if mode == "expiry" {
				want = state.DeploymentStatusExpired
			}
			if terminal.Status != want || terminal.SandboxID != "" {
				t.Fatalf("cleanup not confirmed status=%s sandbox=%s", terminal.Status, terminal.SandboxID)
			}
			factory := &serverDeploymentRuntimeFactory{server: srv, templateID: cfg.DeploymentTemplateID}
			if handle, err := factory.Connect(ctx, running); err != nil || handle != nil {
				t.Fatalf("provider resource release not confirmed: exists=%v error_category=%T", handle != nil, err)
			}
			evidence = append(evidence, map[string]any{"mode": mode, "started_at": started, "finished_at": time.Now().UTC(), "repository": "qiniu/deployment", "commit": commit, "deployment_id": record.ID, "sandbox_id": running.SandboxID, "endpoint": running.Endpoint, "expires_at": running.ExpiresAt, "terminal_status": terminal.Status, "public_http_status": public.StatusCode, "controller_reconnect_preserved_attempt": true, "provider_missing_after_cleanup": true})
		})
	}
	if path := os.Getenv("RUNNERD_DEPLOY_LIVE_EVIDENCE"); path != "" {
		if !filepath.IsAbs(path) {
			t.Fatal("RUNNERD_DEPLOY_LIVE_EVIDENCE must be absolute")
		}
		payload, _ := json.MarshalIndent(evidence, "", "  ")
		if err := os.WriteFile(path, payload, 0o600); err != nil {
			t.Fatal("evidence write failed")
		}
	}
}

// Forward one real create, discard its successful response, and temporarily
// fail metadata lookup. Recovery must discover and clean up the real resource.
// All other HTTP exchanges use the ordinary provider transport unchanged.
type deploymentLostCreateResponse struct {
	mu             sync.Mutex
	base           http.RoundTripper
	enabled        bool
	creates        int
	blockedLookups int
	sandboxID      string
}

func (f *deploymentLostCreateResponse) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	create := r.Method == http.MethodPost && r.URL.Path == "/sandboxes"
	if create {
		f.creates++
		// Fail closed even after restoring lookup, so a regression cannot
		// allocate a replacement that escapes this test's cleanup evidence.
		if f.creates > 1 {
			return nil, context.DeadlineExceeded
		}
	}
	if f.enabled && r.Method == http.MethodGet && r.URL.Path == "/v2/sandboxes" {
		f.blockedLookups++
		return nil, context.DeadlineExceeded
	}
	response, err := f.base.RoundTrip(r)
	if err != nil || !create || response.StatusCode < 200 || response.StatusCode >= 300 {
		return response, err
	}
	var accepted struct {
		SandboxID string `json:"sandboxID"`
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, 65537))
	response.Body.Close()
	if readErr != nil || len(data) > 65536 {
		return nil, errors.New("unexpected provider create response")
	}
	_ = json.Unmarshal(data, &accepted)
	f.sandboxID = accepted.SandboxID
	if !f.enabled {
		response.Body = io.NopCloser(bytes.NewReader(data))
		return response, nil
	}
	return nil, context.DeadlineExceeded
}

func (f *deploymentLostCreateResponse) disable() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enabled = false
}

func (f *deploymentLostCreateResponse) evidence() (string, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sandboxID, f.creates, f.blockedLookups
}
