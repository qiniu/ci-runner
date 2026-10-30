package server

import (
	"context"
	"sync"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
	"golang.org/x/sync/singleflight"
)

const (
	runnerRequestMetricsTTL     = 30 * time.Second
	runnerRequestMetricsTimeout = 10 * time.Second
)

type runnerRequestMetricsCache struct {
	mu        sync.Mutex
	value     state.RunnerRequestMetrics
	expiresAt time.Time
	group     singleflight.Group
}

func (c *runnerRequestMetricsCache) read() (state.RunnerRequestMetrics, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.value, time.Now().Before(c.expiresAt)
}

func (s *Server) getRunnerRequestMetrics(ctx context.Context) (state.RunnerRequestMetrics, error) {
	if value, ok := s.requestMetrics.read(); ok {
		return value, nil
	}
	result := s.requestMetrics.group.DoChan("metrics", func() (any, error) {
		if value, ok := s.requestMetrics.read(); ok {
			return value, nil
		}
		// Waiters can leave independently; the shared read has its own deadline.
		queryCtx, cancel := context.WithTimeout(context.Background(), runnerRequestMetricsTimeout)
		defer cancel()
		value, err := s.store.GetRunnerRequestMetrics(queryCtx)
		if err != nil {
			// The shared read owns this failure even if every HTTP waiter left.
			// Log it once here, rather than once per remaining subscriber.
			s.logAdminReadFailure(queryCtx, "runner_request_metrics", err)
			return nil, err
		}
		s.requestMetrics.mu.Lock()
		s.requestMetrics.value = value
		s.requestMetrics.expiresAt = time.Now().Add(runnerRequestMetricsTTL)
		s.requestMetrics.mu.Unlock()
		return value, nil
	})
	select {
	case <-ctx.Done():
		return state.RunnerRequestMetrics{}, ctx.Err()
	case completed := <-result:
		if completed.Err != nil {
			return state.RunnerRequestMetrics{}, completed.Err
		}
		return completed.Val.(state.RunnerRequestMetrics), nil
	}
}
