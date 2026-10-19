package worker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	deploy "github.com/miclle/deployd"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestApplyDeadlineUsesSavedExpiryAndKeepsCleanupIndependent(t *testing.T) {
	// The SQL connection opener must outlive the virtual-time bubble.
	store := state.New(t.TempDir())
	if _, err := store.ListActiveDeployments(); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		d := deploymentFixture(t)
		d.ExpiresAt = time.Now().Add(time.Second)
		plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: github.RepositoryCloneURL(d.RepositoryFullName), CommitSHA: d.CommitSHA}, deploy.Spec{InstallCommand: "true", StartCommand: "true", Port: 8080})
		if err != nil {
			t.Fatal(err)
		}
		d.SpecHash = plan.Snapshot().SpecHash
		if _, err := store.CreateDeployment(d); err != nil {
			t.Fatal(err)
		}
		factory := &scheduledFactory{created: make(chan string, 1), closed: make(chan string, 2)}
		w := New(store, factory, func(context.Context, state.Deployment) (deploy.Source, error) {
			return blockedCheckout{release: make(chan struct{})}, nil
		}, func(state.Deployment) (deploy.Spec, error) { return plan.Spec(), nil }, t.TempDir())
		if err := w.Process(context.Background(), d.ID); err != nil {
			t.Fatal(err)
		}
		current, err := store.GetDeployment(d.ID)
		if err != nil || current.Status != state.DeploymentStatusExpired || current.SandboxID != "" || len(factory.closed) == 0 {
			t.Fatalf("expired apply did not release the Sandbox: %#v %v", current, err)
		}
	})
}

func TestExpiredApplyRetainsCauseUntilCleanupSucceeds(t *testing.T) {
	store := state.New(t.TempDir())
	if _, err := store.ListActiveDeployments(); err != nil {
		t.Fatal(err)
	}
	synctest.Test(t, func(t *testing.T) {
		d := deploymentFixture(t)
		d.ExpiresAt = time.Now().Add(time.Second)
		plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: github.RepositoryCloneURL(d.RepositoryFullName), CommitSHA: d.CommitSHA}, deploy.Spec{InstallCommand: "true", StartCommand: "true", Port: 8080})
		if err != nil {
			t.Fatal(err)
		}
		d.SpecHash = plan.Snapshot().SpecHash
		if _, err := store.CreateDeployment(d); err != nil {
			t.Fatal(err)
		}
		cleanupErr := errors.New("cleanup unavailable")
		factory := &fakeFactory{runtime: &fakeRuntime{id: "sandbox-1", closeErr: cleanupErr}}
		w := New(store, factory, func(context.Context, state.Deployment) (deploy.Source, error) {
			return blockedCheckout{release: make(chan struct{})}, nil
		}, func(state.Deployment) (deploy.Spec, error) { return plan.Spec(), nil }, t.TempDir())
		if err := w.Process(context.Background(), d.ID); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			current, err := store.GetDeployment(d.ID)
			if err != nil || current.Status != state.DeploymentStatusFailed || current.FailureStage != expiryCleanupStage || current.FailureReason != "deployment expired; cleanup is incomplete" || current.SandboxID == "" {
				t.Fatalf("expiry cause or cleanup evidence lost: %#v %v", current, err)
			}
			if err := w.RunOnce(context.Background()); !errors.Is(err, cleanupErr) {
				t.Fatalf("expected retryable cleanup failure: %v", err)
			}
		}
		factory.runtime.closeErr = nil
		if err := w.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		current, err := store.GetDeployment(d.ID)
		if err != nil || current.Status != state.DeploymentStatusExpired || current.SandboxID != "" || current.FailureReason != "" || current.Error != "" {
			t.Fatalf("cleaned expiry retained a false execution failure: %#v %v", current, err)
		}
	})
}

type scheduledRuntime struct {
	*fakeRuntime
	inspected chan<- string
	closed    chan<- string
}

func (r *scheduledRuntime) Inspect(context.Context, deploy.ProcessRef) (deploy.ProcessState, error) {
	r.inspected <- r.id
	return deploy.ProcessState{Running: true}, nil
}

func (r *scheduledRuntime) Close(context.Context) error {
	r.closed <- r.id
	return nil
}

type scheduledFactory struct{ created, inspected, closed chan string }

func (f *scheduledFactory) runtime(id string) RuntimeHandle {
	return &scheduledRuntime{fakeRuntime: &fakeRuntime{id: id}, inspected: f.inspected, closed: f.closed}
}

func (f *scheduledFactory) Create(_ context.Context, d state.Deployment, _ int64) (RuntimeHandle, string, error) {
	f.created <- d.ID
	return f.runtime(d.ID), d.ID, nil
}

func (f *scheduledFactory) Connect(_ context.Context, d state.Deployment) (RuntimeHandle, error) {
	return f.runtime(d.ID), nil
}

func (f *scheduledFactory) Reconcile(context.Context, state.Deployment) (RuntimeHandle, string, error) {
	return nil, "", nil
}

type blockedCheckout struct {
	fakeSource
	release <-chan struct{}
}

func (s blockedCheckout) Materialize(ctx context.Context, _ deploy.Runtime, _ deploy.Snapshot, _ string, _ deploy.Output) error {
	select {
	case <-s.release:
		return errors.New("fixture checkout ended")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestSchedulerSeparatesSlowBuildsMonitoringAndExpiry(t *testing.T) {
	for _, cancelBlocked := range []bool{false, true} {
		t.Run(fmt.Sprintf("cancel-blocked=%v", cancelBlocked), func(t *testing.T) {
			store := state.New(t.TempDir())
			plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: github.RepositoryCloneURL("owner/repo"), CommitSHA: strings.Repeat("a", 40)}, deploy.Spec{InstallCommand: "true", StartCommand: "true", Port: 8080})
			if err != nil {
				t.Fatal(err)
			}
			for i := range 20 {
				d := deploymentFixture(t)
				d.SpecHash = plan.Snapshot().SpecHash
				switch {
				case i < 7:
					d.ID = fmt.Sprintf("build-%02d", i)
				case i < 19:
					d.ID, d.Status = fmt.Sprintf("monitor-%02d", i), state.DeploymentStatusRunning
					d.SandboxID, d.RuntimeID, d.ProcessID, d.ProcessTag = d.ID, d.ID, "41", "attempt"
				default:
					d.ID, d.Status = "z-expired", state.DeploymentStatusRunning
					d.SandboxID, d.RuntimeID = d.ID, d.ID
					d.ExpiresAt = time.Now().Add(-time.Minute)
				}
				if _, err := store.CreateDeployment(d); err != nil {
					t.Fatal(err)
				}
			}
			factory := &scheduledFactory{created: make(chan string, 100), inspected: make(chan string, 100), closed: make(chan string, 100)}
			release := make(chan struct{})
			w := New(store, factory, func(context.Context, state.Deployment) (deploy.Source, error) {
				return blockedCheckout{release: release}, nil
			}, func(state.Deployment) (deploy.Spec, error) { return plan.Spec(), nil }, t.TempDir())
			notify := make(chan struct{}, 1)
			notify <- struct{}{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() { w.Run(ctx, notify, nil); close(done) }()
			receive := func(ch <-chan string) string {
				t.Helper()
				select {
				case id := <-ch:
					return id
				case <-time.After(10 * time.Second):
					t.Fatal("scheduler stalled behind a blocked deployment")
					return ""
				}
			}
			seen := make(map[string]bool)
			for range 4 {
				id := receive(factory.created)
				if seen[id] {
					t.Fatal("duplicate in-flight deployment")
				}
				seen[id] = true
			}
			if id := receive(factory.closed); id != "z-expired" {
				t.Fatalf("expiry was not independently cleaned: %s", id)
			}
			for range 12 {
				id := receive(factory.inspected)
				if seen[id] {
					t.Fatal("monitor repeated an identity within one cycle")
				}
				seen[id] = true
			}
			select {
			case id := <-factory.created:
				t.Fatalf("exceeded provisioning capacity: %s", id)
			default:
			}
			if !cancelBlocked {
				close(release)
				for range 3 {
					id := receive(factory.created)
					if seen[id] {
						t.Fatal("duplicate queued deployment")
					}
					seen[id] = true
				}
			}
			cancel()
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("scheduler did not cancel and join blocked workers")
			}
		})
	}
}
