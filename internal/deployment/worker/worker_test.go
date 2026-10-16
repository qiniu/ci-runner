package worker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	deploy "github.com/miclle/deployd"
	deployruntime "github.com/qiniu/ci-runner/internal/deployment/runtime"
	"github.com/qiniu/ci-runner/internal/state"
)

type fakeRuntime struct {
	id           string
	closed       bool
	url          string
	startErr     error
	closeErr     error
	inspectErr   error
	startCommand deploy.Command
	runCommands  []deploy.Command
}

func (r *fakeRuntime) ID() string { return r.id }
func (r *fakeRuntime) Run(_ context.Context, command deploy.Command, _ deploy.Output) (deploy.Exit, error) {
	r.runCommands = append(r.runCommands, command)
	return deploy.Exit{}, nil
}

func (r *fakeRuntime) Start(_ context.Context, command deploy.Command, _ deploy.Output) (deploy.ProcessRef, error) {
	r.startCommand = command
	if r.startErr != nil {
		return deploy.ProcessRef{}, r.startErr
	}
	return deploy.ProcessRef{RuntimeID: r.id, ID: "41", Tag: command.Tag}, nil
}

func (r *fakeRuntime) Inspect(context.Context, deploy.ProcessRef) (deploy.ProcessState, error) {
	return deploy.ProcessState{Running: true}, r.inspectErr
}
func (r *fakeRuntime) Stop(context.Context, deploy.ProcessRef) error { return nil }
func (r *fakeRuntime) Endpoint(context.Context, int) (string, error) {
	return r.url, nil
}
func (r *fakeRuntime) Close(context.Context) error { r.closed = true; return r.closeErr }

type fakeFactory struct {
	runtime    *fakeRuntime
	createErr  error
	connectErr error
	creates    int
	reconciles int
}

func (f *fakeFactory) Create(context.Context, state.Deployment, int64) (RuntimeHandle, string, error) {
	f.creates++
	return f.runtime, "sandbox-1", f.createErr
}

func (f *fakeFactory) Connect(context.Context, state.Deployment) (RuntimeHandle, error) {
	if f.runtime == nil {
		return nil, f.connectErr
	}
	return f.runtime, f.connectErr
}

func (f *fakeFactory) Reconcile(context.Context, state.Deployment) (RuntimeHandle, string, error) {
	f.reconciles++
	if f.runtime == nil {
		return nil, "", f.createErr
	}
	return f.runtime, "sandbox-1", f.createErr
}

type fakeSource struct{}

func (fakeSource) Resolve(context.Context) (deploy.ResolvedSource, error) {
	return deploy.ResolvedSource{SourceID: githubSourceURL("owner/repo"), CommitSHA: strings.Repeat("a", 40)}, nil
}

func (fakeSource) Materialize(context.Context, deploy.Runtime, deploy.Snapshot, string, deploy.Output) error {
	return nil
}

func deploymentFixture(t *testing.T) state.Deployment {
	t.Helper()
	return state.Deployment{
		ID: "deployment-1", AccountID: 1, ScopeType: state.AccountScopeTypeAccount, ScopeID: 1,
		GitHubInstallationID: 2, RepositoryID: 3, RepositoryFullName: "owner/repo", Ref: "main",
		CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1,
		SpecHash: "sha256:placeholder", SpecJSONEncrypted: "unused", Status: state.DeploymentStatusQueued,
		DeploymentTemplateID: "template-1",
		ExpiresAt:            time.Now().UTC().Add(time.Hour), CreatedAt: time.Now().UTC(),
	}
}

func TestWorkerProcessesQueuedDeploymentAndFencesAttempt(t *testing.T) {
	store := state.New(t.TempDir())
	fixture := deploymentFixture(t)
	fixture.ID = strings.Repeat("d", 26) // Same length as HTTP deployment IDs.
	spec := deploy.Spec{InstallCommand: "true", StartCommand: "true", Port: 8080}
	plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: githubSourceURL(fixture.RepositoryFullName), CommitSHA: fixture.CommitSHA}, spec)
	if err != nil {
		t.Fatal(err)
	}
	fixture.SpecHash = plan.Snapshot().SpecHash
	if _, err := store.CreateDeployment(fixture); err != nil {
		t.Fatal(err)
	}
	factory := &fakeFactory{runtime: &fakeRuntime{id: "sandbox-1"}}
	probe := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer probe.Close()
	factory.runtime.url = probe.URL
	normalized := plan.Spec()
	w := New(store, factory, func(context.Context, state.Deployment) (deploy.Source, error) { return fakeSource{}, nil }, func(state.Deployment) (deploy.Spec, error) { return normalized, nil }, t.TempDir())
	if err := w.Process(context.Background(), fixture.ID); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetDeployment(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.DeploymentStatusRunning || got.SandboxID != "sandbox-1" || got.Attempt != 1 || got.ProcessID != "41" || got.ProcessTag == "" {
		t.Fatalf("deployment = %#v", got)
	}
	if got.Version == fixture.Version {
		t.Fatal("worker did not persist lifecycle version")
	}
	if host := factory.runtime.startCommand.Env["__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS"]; host != "127.0.0.1" {
		t.Fatalf("application Host environment = %q", host)
	}
	for _, command := range factory.runtime.runCommands {
		if _, ok := command.Env["__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS"]; ok {
			t.Fatal("application Host policy was supplied to a setup command")
		}
	}
}

func TestWorkerMissingTemplateFailsWithoutProvisioning(t *testing.T) {
	store := state.New(t.TempDir())
	d := deploymentFixture(t)
	d.DeploymentTemplateID = ""
	plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: githubSourceURL(d.RepositoryFullName), CommitSHA: d.CommitSHA}, deploy.Spec{InstallCommand: "true", StartCommand: "true", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	d.SpecHash = plan.Snapshot().SpecHash
	if _, err := store.CreateDeployment(d); err != nil {
		t.Fatal(err)
	}
	w := New(store, &fakeFactory{}, func(context.Context, state.Deployment) (deploy.Source, error) { return fakeSource{}, nil }, func(state.Deployment) (deploy.Spec, error) { return plan.Spec(), nil }, t.TempDir())
	w.Event = func(_ state.Deployment, event state.DeploymentEvent) {
		if err := store.AppendDeploymentEvent(event); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetDeployment(d.ID)
	if err != nil || got.Status != state.DeploymentStatusFailed || got.FailureStage != "template" || got.SandboxID != "" {
		t.Fatalf("missing-template deployment=%#v err=%v", got, err)
	}
	events, _, err := store.ListDeploymentEvents(d.ID, 0, 10)
	if err != nil || len(events) != 2 || events[1].EventType != "failure" {
		t.Fatalf("failure log=%#v err=%v", events, err)
	}
}

func TestWorkerExpiryCleansSandboxAndDoesNotHideConnectFailure(t *testing.T) {
	store := state.New(t.TempDir())
	fixture := deploymentFixture(t)
	fixture.Status = state.DeploymentStatusRunning
	fixture.SandboxID = "sandbox-1"
	fixture.RuntimeID = "sandbox-1"
	fixture.ProcessID = "41"
	fixture.ProcessTag = "deployd-deployment-1-1"
	fixture.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if _, err := store.CreateDeployment(fixture); err != nil {
		t.Fatal(err)
	}
	factory := &fakeFactory{runtime: &fakeRuntime{id: "sandbox-1"}}
	w := New(store, factory, nil, nil, t.TempDir())
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetDeployment(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.DeploymentStatusExpired || got.SandboxID != "" || !factory.runtime.closed {
		t.Fatalf("expired deployment = %#v closed=%v", got, factory.runtime.closed)
	}
}

func TestWorkerExecutionFailureCleansSandboxAndRetainsFailure(t *testing.T) {
	store := state.New(t.TempDir())
	fixture := deploymentFixture(t)
	spec := deploy.Spec{InstallCommand: "true", StartCommand: "true", Port: 8080}
	plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: githubSourceURL(fixture.RepositoryFullName), CommitSHA: fixture.CommitSHA}, spec)
	if err != nil {
		t.Fatal(err)
	}
	fixture.SpecHash = plan.Snapshot().SpecHash
	if _, err := store.CreateDeployment(fixture); err != nil {
		t.Fatal(err)
	}
	factory := &fakeFactory{runtime: &fakeRuntime{id: "sandbox-1", startErr: errors.New("start failed")}}
	w := New(store, factory, func(context.Context, state.Deployment) (deploy.Source, error) { return fakeSource{}, nil }, func(state.Deployment) (deploy.Spec, error) { return plan.Spec(), nil }, t.TempDir())
	if err := w.Process(context.Background(), fixture.ID); err != nil {
		t.Fatalf("Process returned persistence error: %v", err)
	}
	got, err := store.GetDeployment(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.DeploymentStatusFailed || got.SandboxID != "" || !factory.runtime.closed {
		t.Fatalf("failed deployment = %#v closed=%v", got, factory.runtime.closed)
	}
}

func TestWorkerExecutionFailureKeepsSandboxWhenCleanupFails(t *testing.T) {
	store := state.New(t.TempDir())
	fixture := deploymentFixture(t)
	spec := deploy.Spec{InstallCommand: "true", StartCommand: "true", Port: 8080}
	plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: githubSourceURL(fixture.RepositoryFullName), CommitSHA: fixture.CommitSHA}, spec)
	if err != nil {
		t.Fatal(err)
	}
	fixture.SpecHash = plan.Snapshot().SpecHash
	if _, err := store.CreateDeployment(fixture); err != nil {
		t.Fatal(err)
	}
	factory := &fakeFactory{runtime: &fakeRuntime{id: "sandbox-1", startErr: errors.New("start failed"), closeErr: errors.New("delete failed")}}
	w := New(store, factory, func(context.Context, state.Deployment) (deploy.Source, error) { return fakeSource{}, nil }, func(state.Deployment) (deploy.Spec, error) { return plan.Spec(), nil }, t.TempDir())
	if err := w.Process(context.Background(), fixture.ID); err != nil {
		t.Fatalf("Process returned persistence error: %v", err)
	}
	got, err := store.GetDeployment(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.DeploymentStatusFailed || got.SandboxID == "" {
		t.Fatalf("failed cleanup lost remote evidence = %#v", got)
	}
	if err := w.RunOnce(context.Background()); err == nil {
		t.Fatal("RunOnce hid cleanup failure")
	}
	got, err = store.GetDeployment(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.DeploymentStatusFailed || got.SandboxID == "" {
		t.Fatalf("cleanup failure state = %#v", got)
	}
}

func TestWorkerPersistsAndCleansReconciledCreateEvidence(t *testing.T) {
	store := state.New(t.TempDir())
	fixture := deploymentFixture(t)
	plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: githubSourceURL(fixture.RepositoryFullName), CommitSHA: fixture.CommitSHA}, deploy.Spec{InstallCommand: "true", StartCommand: "true", Port: 8080})
	if err != nil {
		t.Fatal(err)
	}
	fixture.SpecHash = plan.Snapshot().SpecHash
	if _, err := store.CreateDeployment(fixture); err != nil {
		t.Fatal(err)
	}
	runtime := &fakeRuntime{id: "sandbox-1"}
	factory := &fakeFactory{runtime: runtime, createErr: errors.New("creation still pending")}
	w := New(store, factory, func(context.Context, state.Deployment) (deploy.Source, error) { return fakeSource{}, nil }, func(state.Deployment) (deploy.Spec, error) {
		return plan.Spec(), nil
	}, t.TempDir())
	if err := w.Process(context.Background(), fixture.ID); err != nil {
		t.Fatalf("Process returned persistence error: %v", err)
	}
	got, err := store.GetDeployment(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.DeploymentStatusFailed || got.SandboxID != "" || !runtime.closed {
		t.Fatalf("reconciled create state = %#v closed=%v", got, runtime.closed)
	}
}

func TestWorkerRecoversInterruptedCreationWithoutNewSandbox(t *testing.T) {
	for _, found := range []bool{false, true} {
		t.Run(fmt.Sprint(found), func(t *testing.T) {
			store := state.New(t.TempDir())
			fixture := deploymentFixture(t)
			fixture.Status = state.DeploymentStatusCreating
			fixture.Attempt = 2
			if _, err := store.CreateDeployment(fixture); err != nil {
				t.Fatal(err)
			}
			factory := &fakeFactory{}
			if found {
				factory.runtime = &fakeRuntime{id: "sandbox-1"}
			}
			w := New(store, factory, nil, nil, t.TempDir())
			err := w.RunOnce(context.Background())
			if !found && !errors.Is(err, deployruntime.ErrCreatePending) {
				t.Fatalf("unresolved create error = %v", err)
			}
			if found && err != nil {
				t.Fatal(err)
			}
			got, err := store.GetDeployment(fixture.ID)
			if err != nil {
				t.Fatal(err)
			}
			if factory.creates != 0 || factory.reconciles != 1 || got.Attempt != 2 {
				t.Fatalf("recovery replayed create: %#v %#v", factory, got)
			}
			if found {
				if got.Status != state.DeploymentStatusFailed || got.SandboxID != "" || !factory.runtime.closed {
					t.Fatalf("interrupted create cleanup = %#v", got)
				}
				if got.FailureStage != "recovery" || got.FailureReason != "deployment execution was interrupted" {
					t.Fatalf("successful cleanup erased the original failure: stage=%q reason=%q", got.FailureStage, got.FailureReason)
				}
			} else if got.Status != state.DeploymentStatusCreating {
				t.Fatalf("unknown outcome was declared terminal: %#v", got)
			}
		})
	}
}

func TestWorkerResumesFailedCleanupWithoutErasingCause(t *testing.T) {
	store := state.New(t.TempDir())
	fixture := deploymentFixture(t)
	fixture.Status = state.DeploymentStatusStopping
	fixture.SandboxID, fixture.RuntimeID = "sandbox-1", "sandbox-1"
	fixture.Attempt = 1
	fixture.FailureStage, fixture.FailureReason = "recovery", "deployment execution was interrupted"
	if _, err := store.CreateDeployment(fixture); err != nil {
		t.Fatal(err)
	}
	factory := &fakeFactory{runtime: &fakeRuntime{id: "sandbox-1"}}
	w := New(store, factory, nil, nil, t.TempDir())
	if err := w.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetDeployment(fixture.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.DeploymentStatusFailed || got.FailureStage != fixture.FailureStage || got.FailureReason != fixture.FailureReason || got.SandboxID != "" || !factory.runtime.closed {
		t.Fatalf("resumed cleanup lost failure evidence: %#v", got)
	}
	revision, err := store.GetDeploymentRevision(fixture.ID, 1)
	if err != nil || revision.Status != state.DeploymentStatusFailed || revision.FailureStage != fixture.FailureStage || revision.FailureReason != fixture.FailureReason {
		t.Fatalf("revision lost failure evidence: %#v err=%v", revision, err)
	}
}

func TestWorkerRechecksRunningAttemptAndPreservesHealthyService(t *testing.T) {
	for _, test := range []struct {
		name                   string
		inspectErr, connectErr error
		missing, cleaned       bool
	}{
		{name: "healthy"},
		{name: "temporary provider failure", connectErr: errors.New("temporary")},
		{name: "identity mismatch", inspectErr: deploy.ErrRuntimeMismatch},
		{name: "process exited", inspectErr: deploy.ErrProcessUnknown, cleaned: true},
		{name: "Sandbox already missing", missing: true, cleaned: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := state.New(t.TempDir())
			fixture := deploymentFixture(t)
			fixture.Status = state.DeploymentStatusRunning
			fixture.SandboxID, fixture.RuntimeID, fixture.ProcessID, fixture.ProcessTag = "sandbox-1", "sandbox-1", "41", "attempt-1"
			if _, err := store.CreateDeployment(fixture); err != nil {
				t.Fatal(err)
			}
			factory := &fakeFactory{connectErr: test.connectErr}
			if !test.missing {
				factory.runtime = &fakeRuntime{id: "sandbox-1", inspectErr: test.inspectErr}
			}
			w := New(store, factory, nil, nil, t.TempDir())
			err := w.RunOnce(context.Background())
			if (test.connectErr != nil || test.inspectErr == deploy.ErrRuntimeMismatch) && err == nil {
				t.Fatal("transient/mismatched evidence was hidden")
			}
			got, err := store.GetDeployment(fixture.ID)
			if err != nil {
				t.Fatal(err)
			}
			if test.cleaned {
				if got.Status != state.DeploymentStatusFailed || got.SandboxID != "" {
					t.Fatalf("lost process cleanup = %#v", got)
				}
			} else if got.Status != state.DeploymentStatusRunning || (factory.runtime != nil && factory.runtime.closed) {
				t.Fatalf("healthy/unknown service was stopped: %#v", got)
			}
			if factory.creates != 0 {
				t.Fatal("running attempt created a replacement")
			}
		})
	}
}

type deadlineCrossingFactory struct {
	*fakeFactory
	deadline time.Time
	onReturn func() error
}

func (f *deadlineCrossingFactory) Connect(ctx context.Context, d state.Deployment) (RuntimeHandle, error) {
	timer := time.NewTimer(max(time.Until(f.deadline), 0))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-timer.C:
		if f.onReturn != nil {
			callback := f.onReturn
			f.onReturn = nil
			if err := callback(); err != nil {
				return nil, err
			}
		}
		return f.fakeFactory.Connect(ctx, d)
	}
}

func TestWorkerExpiredObservationCannotStopReplacementAttempt(t *testing.T) {
	// Open the SQL connection outside the bubble; its background opener outlives
	// the check. Virtual time keeps database speed from consuming the short TTL.
	store := state.New(t.TempDir())
	if _, err := store.ListActiveDeployments(); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		fixture := deploymentFixture(t)
		fixture.Status = state.DeploymentStatusRunning
		fixture.SandboxID, fixture.RuntimeID, fixture.ProcessID, fixture.ProcessTag = "sandbox-1", "sandbox-1", "41", "attempt-1"
		fixture.ExpiresAt = time.Now().UTC().Add(200 * time.Millisecond)
		if _, err := store.CreateDeployment(fixture); err != nil {
			t.Fatal(err)
		}
		factory := &deadlineCrossingFactory{fakeFactory: &fakeFactory{}, deadline: fixture.ExpiresAt.Add(20 * time.Millisecond)}
		factory.onReturn = func() error {
			replacement, err := store.GetDeployment(fixture.ID)
			if err != nil {
				return err
			}
			replacement.Attempt++
			replacement.SandboxID, replacement.RuntimeID, replacement.ProcessID, replacement.ProcessTag = "sandbox-2", "sandbox-2", "42", "attempt-2"
			replacement.ExpiresAt = time.Now().UTC().Add(time.Hour)
			return store.UpdateDeployment(replacement)
		}
		w := New(store, factory, nil, nil, t.TempDir())
		if err := w.RunOnce(context.Background()); !errors.Is(err, state.ErrConflict) {
			t.Fatalf("stale expiry observation was accepted: %v", err)
		}
		got, err := store.GetDeployment(fixture.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != state.DeploymentStatusRunning || got.SandboxID != "sandbox-2" || got.ProcessTag != "attempt-2" {
			t.Fatalf("stale observation stopped replacement: %#v", got)
		}
	})
}

func TestWorkerExpiresWhenProviderLookupCrossesDeadline(t *testing.T) {
	// Open the SQL connection outside the bubble; its background opener outlives
	// the check. Virtual time keeps database speed from consuming the short TTL.
	store := state.New(t.TempDir())
	if _, err := store.ListActiveDeployments(); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		fixture := deploymentFixture(t)
		fixture.Status = state.DeploymentStatusRunning
		fixture.SandboxID, fixture.RuntimeID, fixture.ProcessID, fixture.ProcessTag = "sandbox-1", "sandbox-1", "41", "attempt-1"
		fixture.ExpiresAt = time.Now().UTC().Add(200 * time.Millisecond)
		if _, err := store.CreateDeployment(fixture); err != nil {
			t.Fatal(err)
		}
		factory := &deadlineCrossingFactory{fakeFactory: &fakeFactory{}, deadline: fixture.ExpiresAt.Add(20 * time.Millisecond)}
		w := New(store, factory, nil, nil, t.TempDir())
		if err := w.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		got, err := store.GetDeployment(fixture.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != state.DeploymentStatusExpired || got.SandboxID != "" || got.FailureReason != "" {
			t.Fatalf("expiry was misclassified after provider lookup: status=%s sandbox=%s reason=%s", got.Status, got.SandboxID, got.FailureReason)
		}
	})
}

func TestGitSourceUsesRevisionActor(t *testing.T) {
	for _, tc := range []struct{ actor, want int64 }{{0, 1}, {2, 2}} {
		var got int64
		factory := GitSourceFactory(func(_ context.Context, id int64) (string, error) { got = id; return "actor-token", nil })
		_, err := factory(context.Background(), state.Deployment{AccountID: 1, ExecutionAccountID: tc.actor, RepositoryFullName: "owner/repo", CommitSHA: strings.Repeat("a", 40)})
		if err != nil || got != tc.want {
			t.Fatalf("actor=%d want=%d err=%v", got, tc.want, err)
		}
	}
}
