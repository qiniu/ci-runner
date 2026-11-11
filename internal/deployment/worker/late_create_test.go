package worker

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	deploy "github.com/miclle/deployd"
	deployruntime "github.com/qiniu/ci-runner/internal/deployment/runtime"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
	"github.com/qiniu/ci-runner/internal/testpg"
)

type lateCreateFixture struct {
	entered, reply chan struct{}
	runtime        *fakeRuntime
}

func (f *lateCreateFixture) Create(context.Context, state.Deployment, int64) (RuntimeHandle, string, error) {
	close(f.entered)
	<-f.reply
	return f.runtime, f.runtime.ID(), nil
}

func (f *lateCreateFixture) Connect(_ context.Context, d state.Deployment) (RuntimeHandle, error) {
	if d.SandboxID != f.runtime.ID() {
		return nil, errors.New("unexpected Sandbox ID")
	}
	return f.runtime, nil
}

func (f *lateCreateFixture) Reconcile(context.Context, state.Deployment) (RuntimeHandle, string, error) {
	return nil, "", nil
}

func TestLateDeploymentCreateReceiptSurvivesStop(t *testing.T) {
	dir := t.TempDir()
	opts := state.Options{Backend: state.BackendSQLite, DatabaseDSN: filepath.Join(dir, "runnerd.db"), MigrateOnStart: true}
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") == "1" {
		opts.Backend, opts.DatabaseDSN = state.BackendPostgres, testpg.SchemaDSN(t)
	}
	store := state.NewWithOptions(opts)
	d := deploymentFixture(t)
	spec := deploy.Spec{InstallCommand: "true", StartCommand: "true", Port: 8080}
	plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: github.RepositoryCloneURL(d.RepositoryFullName), CommitSHA: d.CommitSHA}, spec)
	if err != nil {
		t.Fatal(err)
	}
	d.SpecHash = plan.Snapshot().SpecHash
	if _, err := store.CreateDeployment(d); err != nil {
		t.Fatal(err)
	}
	factory := &lateCreateFixture{make(chan struct{}), make(chan struct{}), &fakeRuntime{id: "created-before-stop"}}
	newWorker := func(st state.Store) *Worker {
		return New(st, factory, func(context.Context, state.Deployment) (deploy.Source, error) { return fakeSource{}, nil }, func(state.Deployment) (deploy.Spec, error) { return plan.Spec(), nil }, dir)
	}
	opts.MigrateOnStart = false
	a, b := newWorker(store), newWorker(state.NewWithOptions(opts))
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	reply := sync.OnceFunc(func() { close(factory.reply) })
	go func() { done <- a.Process(ctx, d.ID) }()
	defer reply()
	select {
	case <-factory.entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	current, err := store.GetDeployment(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	current.Status = state.DeploymentStatusStopping
	if err := store.UpdateDeployment(current); err != nil {
		t.Fatal(err)
	}
	current.Version++
	if err := b.StopObserved(ctx, current, false); !errors.Is(err, deployruntime.ErrCreatePending) {
		t.Fatalf("expected pending: %v", err)
	}
	reply()
	select {
	case err := <-done:
		if !errors.Is(err, state.ErrConflict) {
			t.Fatalf("old creator resumed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	err = b.RunOnce(ctx)
	final, readErr := store.GetDeployment(d.ID)
	if err != nil || readErr != nil || final.Status != state.DeploymentStatusCompleted || !factory.runtime.closed {
		t.Fatalf("positive Create receipt did not complete pending stop: status=%s sandbox=%q closed=%t err=%v", final.Status, final.SandboxID, factory.runtime.closed, err)
	}
}
