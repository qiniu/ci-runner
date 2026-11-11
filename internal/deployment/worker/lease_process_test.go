//go:build unix

package worker

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	deploy "github.com/miclle/deployd"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
	"github.com/qiniu/ci-runner/internal/testpg"
)

type leaseTestSource struct {
	fakeSource
	dir string
}

func (s leaseTestSource) Materialize(ctx context.Context, _ deploy.Runtime, _ deploy.Snapshot, _ string, _ deploy.Output) error {
	if err := os.WriteFile(filepath.Join(s.dir, "checkout"), nil, 0o600); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

type leaseTestRuntime struct {
	*fakeRuntime
	dir string
}

func (r leaseTestRuntime) Close(context.Context) error {
	file, err := os.OpenFile(filepath.Join(r.dir, "close-calls"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = file.WriteString(r.ID() + "\n")
	return err
}

type leaseTestFactory struct{ rt leaseTestRuntime }

func (f leaseTestFactory) Create(context.Context, state.Deployment, int64) (RuntimeHandle, string, error) {
	return f.rt, f.rt.ID(), nil
}

func (f leaseTestFactory) Connect(context.Context, state.Deployment) (RuntimeHandle, error) {
	return f.rt, nil
}

func (f leaseTestFactory) Reconcile(context.Context, state.Deployment) (RuntimeHandle, string, error) {
	return f.rt, f.rt.ID(), nil
}

// Both workers use normal Process/RunOnce paths and independent SQL pools.
// Keep the creator inside checkout while the peer scans, then kill or pause
// it. On resumption the old deferred Sandbox close must be fenced too.
func TestDeploymentWorkProcess(t *testing.T) {
	if dir := os.Getenv("RUNNERD_TEST_DEPLOY_CHILD"); dir != "" {
		deploymentWorkProcess(t, dir, os.Getenv("RUNNERD_TEST_DEPLOY_DSN"), "child")
		return
	}
	for _, mode := range []string{"kill", "pause"} {
		t.Run(mode, func(t *testing.T) {
			var dsn string
			if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") == "1" {
				dsn = testpg.SchemaDSN(t)
			}
			deploymentWorkProcess(t, t.TempDir(), dsn, mode)
		})
	}
}

func deploymentWorkProcess(t *testing.T, dir, dsn, mode string) {
	t.Helper()
	store := state.New(dir)
	if dsn != "" {
		store = state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: dsn, MigrateOnStart: mode != "child"})
	}
	d := deploymentFixture(t)
	spec := deploy.Spec{InstallCommand: "true", StartCommand: "true", Port: 8080}
	plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: github.RepositoryCloneURL(d.RepositoryFullName), CommitSHA: d.CommitSHA}, spec)
	if err != nil {
		t.Fatal(err)
	}
	d.SpecHash = plan.Snapshot().SpecHash
	factory := leaseTestFactory{leaseTestRuntime{&fakeRuntime{id: "original-sandbox"}, dir}}
	w := New(store, factory, func(context.Context, state.Deployment) (deploy.Source, error) {
		return leaseTestSource{fakeSource{}, dir}, nil
	}, func(state.Deployment) (deploy.Spec, error) { return plan.Spec(), nil }, dir)
	w.LeaseTTL = time.Second
	if mode == "child" {
		if err := w.Process(t.Context(), d.ID); !errors.Is(err, state.ErrConflict) {
			t.Fatalf("expected superseded work: %v", err)
		}
		return
	}
	if _, err := store.CreateDeployment(d); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestDeploymentWorkProcess$")
	cmd.Env = append(os.Environ(), "RUNNERD_TEST_DEPLOY_CHILD="+dir, "RUNNERD_TEST_DEPLOY_DSN="+dsn)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
		if t.Failed() {
			t.Log(output.String())
		}
	}()
	for {
		if _, err := os.Stat(filepath.Join(dir, "checkout")); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	before, err := store.GetDeployment(d.ID)
	if err != nil || before.Status != state.DeploymentStatusCreating || before.SandboxID != "original-sandbox" {
		t.Fatal("creator did not save Sandbox", before.Status, err)
	}
	// Observe across a full TTL. Check both candidate enumeration and a
	// candidate a peer could have loaded before the owner acquired its lease.
	time.Sleep(w.LeaseTTL + w.LeaseTTL/3)
	other := deploymentFixture(t)
	other.ID = "second-deployment"
	other.ExpiresAt = time.Now().UTC().Add(-time.Minute)
	if _, err := store.CreateDeployment(other); err != nil {
		t.Fatal(err)
	}
	if err := w.advance(ctx, before); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	finished, err := store.GetDeployment(other.ID)
	if err != nil || finished.Status != state.DeploymentStatusExpired {
		t.Fatal("peer did not advance independent deployment", finished.Status, err)
	}
	live, err := store.GetDeployment(d.ID)
	if err != nil || before.Version != live.Version || live.SandboxID != before.SandboxID {
		t.Fatal("live creator was changed", live.Status, err)
	}
	if mode == "kill" {
		if err := cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err == nil {
			t.Fatal("expected killed creator")
		}
	} else if err := cmd.Process.Signal(syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	var recovered state.Deployment
	for ctx.Err() == nil {
		if err := w.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		recovered, err = store.GetDeployment(d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if recovered.Status == state.DeploymentStatusFailed && recovered.SandboxID == "" {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if ctx.Err() != nil || recovered.FailureStage != "recovery" || recovered.Attempt != before.Attempt || recovered.RevisionNumber != before.RevisionNumber {
		t.Fatalf("failed to reconcile original attempt: %s / %s", recovered.Status, recovered.FailureStage)
	}
	t.Logf("%s to cleanup: %s", mode, time.Since(started).Round(time.Millisecond))
	if mode == "pause" {
		if err := cmd.Process.Signal(syscall.SIGCONT); err != nil {
			t.Fatal(err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	final, err := store.GetDeployment(d.ID)
	if err != nil || final.Version != recovered.Version {
		t.Fatal("stale owner changed state", err)
	}
	stops, err := os.ReadFile(filepath.Join(dir, "close-calls"))
	if err != nil || strings.TrimSpace(string(stops)) != "original-sandbox" {
		t.Fatalf("wrong original Sandbox close calls: %q %v", stops, err)
	}
}
