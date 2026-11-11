package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

// A canceled SDK stream without an end event returns exit code -1 and a nil
// Go error. Only a real process result may race watch cancellation into cleanup.
func runnerWatchObservedExit(ctx context.Context, result sandboxrunner.ExitResult, err error) bool {
	return ctx.Err() == nil || (err == nil && result.ExitCode >= 0)
}

func (s *Server) runnerWatchTTL() time.Duration {
	if s.cfg.WorkerLeaseTTL > 0 {
		return s.cfg.WorkerLeaseTTL
	}
	return 5 * time.Minute
}

func (s *Server) requireRunnerWatch(ctx context.Context, id, token string) error {
	ok, err := s.store.RenewRunnerWatch(ctx, id, token, s.runnerWatchTTL())
	if err != nil {
		return err
	}
	if !ok {
		return state.ErrConflict
	}
	return nil
}

// Keep the creation operation or attached observer alive until it returns or
// this process loses the database lease.
func (s *Server) renewRunnerWatch(ctx context.Context, cancel context.CancelFunc, id, token string) {
	ttl := s.runnerWatchTTL()
	ticker := time.NewTicker(ttl / 3)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			renewCtx, done := context.WithTimeout(ctx, ttl/3)
			ok, err := s.store.RenewRunnerWatch(renewCtx, id, token, ttl)
			done()
			if !ok || err != nil {
				if err != nil && !errors.Is(err, context.Canceled) {
					s.logger.Warn("renew runner watch", "id", id, "error", err)
				}
				cancel()
				return
			}
		}
	}
}

func writeWatchObservation(store state.Store, st state.RunnerState, token string) error {
	if token == "" {
		return store.WriteState(st)
	}
	return store.WriteWatchedState(st, token)
}

// Filter by database time. ClaimRunnerWatch rechecks status and version after
// this scan, so simultaneous ticks and startup recovery can share the work.
func (s *Server) runnerWatchLoop(ctx context.Context) {
	defer s.loopWG.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.recoverExpiredRunnerWatches(ctx)
		}
	}
}

func (s *Server) recoverExpiredRunnerWatches(ctx context.Context) {
	rows, err := s.store.ListExpiredRunnerWatches(ctx, maxConcurrentRecoveries)
	if err != nil {
		s.logger.Error("list expired runner watches", "error", err)
		return
	}
	var recoveries sync.WaitGroup
	for _, row := range rows {
		recoveries.Go(func() {
			recoverCtx, cancel := context.WithTimeout(ctx, min(s.runnerWatchTTL(), 30*time.Second))
			defer cancel()
			if err := s.recoverRunnerInState(recoverCtx, row.ID, row.Status); err != nil && ctx.Err() == nil {
				s.logger.Warn("recover expired runner watch", "id", row.ID, "error", err)
			}
		})
	}
	recoveries.Wait()
}
