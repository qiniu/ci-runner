package server

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func (s *Server) runnerWatchTTL() time.Duration {
	if s.cfg.WorkerLeaseTTL > 0 {
		return s.cfg.WorkerLeaseTTL
	}
	return 5 * time.Minute
}

// Renew only while this process has an attached observer. Cancellation stops
// local observation; another node can attach after the database lease expires.
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
			ok, err := s.store.RenewRunningWatch(renewCtx, id, token, ttl)
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

func (s *Server) writeWatchObservation(st state.RunnerState, token string) error {
	if token == "" {
		return s.store.WriteState(st)
	}
	return s.store.WriteWatchedState(st, token)
}

// Filter by database time. ClaimRunningWatch rechecks status and version after
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
	ids, err := s.store.ListExpiredRunningWatches(ctx, maxConcurrentRecoveries)
	if err != nil {
		s.logger.Error("list expired runner watches", "error", err)
		return
	}
	var recoveries sync.WaitGroup
	for _, id := range ids {
		recoveries.Go(func() {
			recoverCtx, cancel := context.WithTimeout(ctx, min(s.runnerWatchTTL(), 30*time.Second))
			defer cancel()
			if err := s.recoverRunnerInState(recoverCtx, id, state.StatusRunning); err != nil && ctx.Err() == nil {
				s.logger.Warn("recover expired runner watch", "id", id, "error", err)
			}
		})
	}
	recoveries.Wait()
}
