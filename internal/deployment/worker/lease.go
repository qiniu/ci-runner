package worker

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	deploy "github.com/miclle/deployd"
	"github.com/qiniu/ci-runner/internal/state"
)

var ErrWorkClaimed = errors.New("deployment work is owned by another worker")

// Each operation gets its own worker copy and token. Release always matches
// that token, including when a paused process resumes after takeover.
func (w *Worker) withWork(ctx context.Context, d state.Deployment, run func(*Worker, context.Context, state.Deployment) error) (err error) {
	owned := *w
	if owned.LeaseTTL < time.Millisecond {
		owned.LeaseTTL = defaultLeaseTTL
	}
	owned.workID, owned.workToken = d.ID, rand.Text()
	ok, err := w.Store.ClaimDeploymentWork(ctx, d, owned.workToken, owned.LeaseTTL)
	if err != nil {
		return err
	}
	if !ok {
		return ErrWorkClaimed
	}

	workCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(owned.LeaseTTL / 3)
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				return
			case <-ticker.C:
				if err := owned.requireWork(workCtx); err != nil {
					cancel()
					return
				}
			}
		}
	}()
	defer func() {
		cancel()
		<-done
		releaseCtx, finish := context.WithTimeout(context.Background(), 5*time.Second)
		defer finish()
		err = errors.Join(err, w.Store.ReleaseDeploymentWork(releaseCtx, d.ID, owned.workToken))
	}()
	return run(&owned, workCtx, d)
}

func (w *Worker) requireWork(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, w.LeaseTTL/3)
	defer cancel()
	ok, err := w.Store.RenewDeploymentWork(ctx, w.workID, w.workToken, w.LeaseTTL)
	if err != nil {
		return err
	}
	if !ok {
		return state.ErrConflict
	}
	return nil
}

// deployd also uses the runtime to stop a process in a detached cleanup
// context after Apply returns an error. Fence that call and user commands.
type leasedRuntime struct {
	RuntimeHandle
	owner *Worker
}

func (r leasedRuntime) Run(ctx context.Context, command deploy.Command, output deploy.Output) (deploy.Exit, error) {
	if err := r.owner.requireWork(ctx); err != nil {
		return deploy.Exit{}, err
	}
	return r.RuntimeHandle.Run(ctx, command, output)
}

func (r leasedRuntime) Start(ctx context.Context, command deploy.Command, output deploy.Output) (deploy.ProcessRef, error) {
	if err := r.owner.requireWork(ctx); err != nil {
		return deploy.ProcessRef{}, err
	}
	return r.RuntimeHandle.Start(ctx, command, output)
}

func (r leasedRuntime) Stop(ctx context.Context, ref deploy.ProcessRef) error {
	if err := r.owner.requireWork(ctx); err != nil {
		return err
	}
	return r.RuntimeHandle.Stop(ctx, ref)
}
