package server

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

// One lease covers both external cleanup steps. Persist each confirmed step so
// recovery after a crash during the next step can resume at its boundary.
type runnerCleanup struct {
	server    *Server
	ctx       context.Context
	cancel    context.CancelFunc
	id, token string
}

func (s *Server) beginRunnerCleanup(ctx context.Context, st *state.RunnerState, started time.Time, observedWatch string) (*runnerCleanup, error) {
	markRunnerCleanupStarted(st, started)
	token := rand.Text()
	if err := s.store.BeginRunnerCleanup(ctx, *st, observedWatch, token, s.runnerWatchTTL()); err != nil {
		return nil, err
	}
	st.Version++
	st.WatchToken = token
	// An accepted stop outlives its HTTP request or the caller's former
	// creating/running watch. Shutdown and lease loss cancel provider calls.
	ctx, cancel := context.WithCancel(s.loopCtx)
	go s.renewRunnerWatch(ctx, cancel, st.ID, token)
	return &runnerCleanup{server: s, ctx: ctx, cancel: cancel, id: st.ID, token: token}, nil
}

func (c *runnerCleanup) close() {
	c.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.server.store.ReleaseRunnerCleanup(ctx, c.id, c.token); err != nil {
		c.server.logger.Warn("release runner cleanup", "id", c.id, "error", err)
	}
}

// Losing the owner or failing to record a remote result leaves the intent
// pending. Treating either as a provider failure would release capacity early.
var errCleanupUnconfirmed = errors.New("runner cleanup unconfirmed")

func (c *runnerCleanup) requireLease() error {
	if err := c.server.requireRunnerWatch(c.ctx, c.id, c.token); err != nil {
		return fmt.Errorf("%w: %w", errCleanupUnconfirmed, err)
	}
	return nil
}

func (c *runnerCleanup) confirm(step state.RunnerCleanupStep) error {
	if err := c.server.store.RecordRunnerCleanup(c.ctx, c.id, c.token, step); err != nil {
		return fmt.Errorf("%w: %w", errCleanupUnconfirmed, err)
	}
	return nil
}

func (c *runnerCleanup) stopSandbox(st state.RunnerState) error {
	if st.CleanupSandboxDone || st.SandboxID == "" {
		return nil
	}
	if err := c.requireLease(); err != nil {
		return err
	}
	err := c.server.stopSandboxWithTimeout(c.ctx, c.id, st.SandboxID, st.ProcessPID)
	if c.ctx.Err() != nil {
		return fmt.Errorf("%w: %w", errCleanupUnconfirmed, c.ctx.Err())
	}
	if err != nil && !isSandboxGone(err) {
		return err
	}
	if receiptErr := c.confirm(state.CleanupSandbox); receiptErr != nil {
		return receiptErr
	}
	return err // Callers record "already gone" separately.
}

func (c *runnerCleanup) removeGitHub(st state.RunnerState) error {
	if st.CleanupGitHubDone {
		return nil
	}
	if err := c.requireLease(); err != nil {
		return err
	}
	if st.RunnerName != "" && st.RepositoryFullName != "" {
		ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
		defer cancel()
		runnerID := st.CleanupGitHubRunnerID
		var err error
		if runnerID == 0 {
			runnerID, err = c.server.gh.FindRunnerIDByName(ctx, st.RepositoryFullName, st.RunnerGroup, st.RunnerName)
			if err == nil && runnerID != 0 {
				if err := c.server.store.BindCleanupGitHubRunner(ctx, c.id, c.token, runnerID); err != nil {
					return fmt.Errorf("%w: %w", errCleanupUnconfirmed, err)
				}
			}
		}
		if err == nil && runnerID != 0 {
			if err := c.requireLease(); err != nil {
				return err
			}
			err = c.server.gh.RemoveRunner(ctx, st.RepositoryFullName, st.RunnerGroup, runnerID)
		}
		if c.ctx.Err() != nil {
			return fmt.Errorf("%w: %w", errCleanupUnconfirmed, c.ctx.Err())
		}
		if err != nil {
			if leaseErr := c.requireLease(); leaseErr != nil {
				return leaseErr
			}
			return c.server.recordGitHubCleanup(st, runnerID != 0, err)
		}
		if err := c.confirm(state.CleanupGitHub); err != nil {
			return err
		}
		return c.server.recordGitHubCleanup(st, runnerID != 0, nil)
	}
	return c.confirm(state.CleanupGitHub)
}

func (c *runnerCleanup) write(st state.RunnerState) error {
	return c.server.store.WriteCleanupState(st, c.token)
}

func (c *runnerCleanup) writeOrLog(st state.RunnerState, msg string) {
	if err := c.write(st); err != nil {
		c.server.logger.Error(msg, "id", c.id, "error", err)
		c.server.store.AppendLog(c.id, "control.log", []byte(msg+": "+err.Error()+"\n"))
	}
}

// A recovered Sandbox without a listener must finish cleanup before a new
// attempt. A competing explicit stop can cancel this requeue by advancing the
// business version while the external call is in progress.
func (c *runnerCleanup) requeueInterrupted(st state.RunnerState) (state.RunnerState, bool, error) {
	err := c.stopSandbox(st)
	if err == nil || isSandboxGone(err) {
		req, readErr := c.server.store.ReadRequest(st.ID)
		if readErr != nil {
			return st, false, readErr
		}
		err = nil
		if sandboxrunner.PlatformForLabels(req.Labels) == sandboxrunner.PlatformWindows {
			err = c.removeGitHub(st)
		}
	}
	if err != nil {
		if errors.Is(err, errCleanupUnconfirmed) {
			return st, false, err
		}
		if st.FailureStage != pendingBootstrapFailure {
			st.LastErrorCode = interruptedCreateCleanup
			st.LastErrorMessage = err.Error()
			st.LastErrorRetryable = true
		}
		st.NextRetryAt = time.Now().UTC().Add(c.server.retryDelay(1))
		if writeErr := c.write(st); writeErr != nil {
			return st, false, writeErr
		}
		return st, false, fmt.Errorf("cleanup interrupted creation: %w", err)
	}
	if st.SandboxCreateDispatched && st.SandboxID != "" {
		result := c.server.applyReconciledBootstrapFailure(&st)
		if err := c.write(st); err != nil {
			return st, false, err
		}
		c.server.recordStartFailure(st, st.FailureStage, errors.New(st.LastErrorMessage), result)
		st.Version++
		return st, false, nil
	}
	resetInterruptedCreation(&st)
	if err := c.write(st); err != nil {
		return st, false, err
	}
	st.Version++
	c.server.store.AppendStagedLog(st.ID, "control.log", runnerEventStageRunnerRequeue, []byte("requeued interrupted creation after Sandbox cleanup\n"))
	c.server.signalQueue()
	return st, false, nil
}
