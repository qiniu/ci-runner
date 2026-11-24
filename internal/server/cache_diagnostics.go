package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

// Only locally owned categories and numeric HTTP/duration evidence enter the
// ordinary-user timeline. In particular, never copy an upstream error body.
func cacheFailureReason(err error) string {
	var stsErr *cacheSTSError
	if errors.As(err, &stsErr) {
		return stsErr.Error()
	}
	if errors.Is(err, state.ErrCacheServiceNotConfigured) {
		return "cache configuration or regional S3 mapping is unavailable"
	}
	return "cache credential preparation failed"
}

func (s *Server) recordCacheFailure(ctx context.Context, id, attempt, phase string, err error) {
	// Timeouts are evidence too. Give persistence its own bounded context while
	// the store still fences the exact active attempt and any accepted stop.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	message := fmt.Sprintf(
		"Cache S3 unavailable: phase=%s; %s; Runner lifecycle is unchanged; cache operations may fail\n",
		phase, cacheFailureReason(err),
	)
	if eventErr := s.store.AppendRunnerCacheEvent(ctx, id, attempt, message); eventErr != nil && !errors.Is(eventErr, state.ErrConflict) {
		s.logger.Warn("failed to retain cache diagnostic", "id", id, "error_type", fmt.Sprintf("%T", eventErr))
	}
}
