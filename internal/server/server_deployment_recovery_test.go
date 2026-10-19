package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	deploy "github.com/miclle/deployd"
	"github.com/qiniu/ci-runner/internal/config"
	deploymentworker "github.com/qiniu/ci-runner/internal/deployment/worker"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
)

func TestStartupRecoveryDefersQueuedDeploymentToIndependentWorker(t *testing.T) {
	store := state.New(t.TempDir())
	api := httptest.NewServer(http.NotFoundHandler())
	defer api.Close()
	srv := New(config.Config{DeploymentWorkRoot: t.TempDir()}, store, github.NewClient(api.URL, api.Client()), &fakeSandbox{}, nil)
	t.Cleanup(srv.Close)
	d, err := store.CreateDeployment(state.Deployment{
		ID: "startup-queued", AccountID: 1, ScopeType: state.AccountScopeTypeAccount, ScopeID: 1,
		GitHubInstallationID: 101, RepositoryID: 1, RepositoryFullName: "owner/repo", Ref: "main",
		CommitSHA: strings.Repeat("a", 40), ConfigPath: ".qiniu/deploy.yaml", SnapshotVersion: 1,
		SpecHash: "sha256:fixture", SpecJSONEncrypted: "fixture", Status: state.DeploymentStatusQueued,
		ExpiresAt: time.Now().UTC().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan context.Context, 1)
	srv.deploymentWorker = deploymentworker.New(store, pendingCleanupFactory{}, func(ctx context.Context, _ state.Deployment) (deploy.Source, error) {
		started <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}, func(state.Deployment) (deploy.Spec, error) {
		return deploy.Spec{InstallCommand: "true", StartCommand: "true", Port: 8080}, nil
	}, t.TempDir())
	recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancelRecovery()
	if err := srv.Recover(recoveryCtx); err != nil {
		t.Fatal(err)
	}
	current, err := store.GetDeployment(d.ID)
	if err != nil || current.Status != state.DeploymentStatusQueued || current.Version != d.Version || current.Attempt != 0 {
		t.Fatalf("startup recovery executed or changed the queued plan: %#v %v", current, err)
	}
	select {
	case <-started:
		t.Fatal("deployment execution started before background loops")
	default:
	}
	// Main cancels the recovery context before opening the HTTP gate. The queued
	// wakeup must survive and use the independent worker context after Start.
	cancelRecovery()
	srv.Start()
	select {
	case ctx := <-started:
		deadline, ok := ctx.Deadline()
		if errors.Is(ctx.Err(), context.Canceled) || !ok || time.Until(deadline) < 50*time.Minute {
			t.Fatalf("worker inherited startup recovery deadline: %v %v", deadline, ctx.Err())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup did not wake the independent deployment worker")
	}
}
