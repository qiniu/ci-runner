package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

type metricsReadStore struct {
	state.Store
	read func(context.Context) (state.RunnerRequestMetrics, error)
}

func (s *metricsReadStore) GetRunnerRequestMetrics(ctx context.Context) (state.RunnerRequestMetrics, error) {
	return s.read(ctx)
}

func TestRunnerRequestMetricsCacheCoalescesAndExpires(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	store := &metricsReadStore{read: func(ctx context.Context) (state.RunnerRequestMetrics, error) {
		calls.Add(1)
		if _, ok := ctx.Deadline(); !ok {
			t.Error("shared statistics read has no deadline")
		}
		<-release
		return state.RunnerRequestMetrics{Completed: 42}, nil
	}}
	srv := &Server{store: store}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			value, err := srv.getRunnerRequestMetrics(context.Background())
			if err != nil || value.Completed != 42 {
				t.Errorf("metrics=%+v err=%v", value, err)
			}
		}()
	}
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("concurrent reads=%d, want 1", got)
	}
	srv.requestMetrics.mu.Lock()
	srv.requestMetrics.expiresAt = time.Now().Add(-time.Second)
	srv.requestMetrics.mu.Unlock()
	if _, err := srv.getRunnerRequestMetrics(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("reads after expiration=%d, want 2", got)
	}
}

func TestRunnerRequestMetricsCacheDoesNotRetainErrors(t *testing.T) {
	calls := 0
	srv := &Server{logger: slog.Default(), store: &metricsReadStore{read: func(context.Context) (state.RunnerRequestMetrics, error) {
		calls++
		if calls == 1 {
			return state.RunnerRequestMetrics{}, errors.New("database unavailable")
		}
		return state.RunnerRequestMetrics{Running: 3}, nil
	}}}
	if _, err := srv.getRunnerRequestMetrics(context.Background()); err == nil {
		t.Fatal("expected database error")
	}
	value, err := srv.getRunnerRequestMetrics(context.Background())
	if err != nil || value.Running != 3 || calls != 2 {
		t.Fatalf("retry value=%+v err=%v calls=%d", value, err, calls)
	}
}

func TestRunnerRequestMetricsCancelledWaiterDoesNotCancelSharedRead(t *testing.T) {
	started := make(chan context.Context, 1)
	release := make(chan struct{})
	srv := &Server{store: &metricsReadStore{read: func(ctx context.Context) (state.RunnerRequestMetrics, error) {
		started <- ctx
		<-release
		return state.RunnerRequestMetrics{Queued: 7}, nil
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := srv.getRunnerRequestMetrics(ctx); done <- err }()
	queryCtx := <-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter error=%v", err)
	}
	if err := queryCtx.Err(); err != nil {
		t.Fatalf("shared read was cancelled: %v", err)
	}
	close(release)
	value, err := srv.getRunnerRequestMetrics(context.Background())
	if err != nil || value.Queued != 7 {
		t.Fatalf("remaining waiter value=%+v err=%v", value, err)
	}
}

func TestRunnerRequestMetricsWarmCacheStillRequiresAdmin(t *testing.T) {
	srv := newTestServer(t, state.New(t.TempDir()), "http://example.test", &fakeSandbox{})
	admin := httptest.NewRecorder()
	srv.ServeHTTP(admin, adminRequest(http.MethodGet, "/runner_request_metrics", nil))
	if admin.Code != http.StatusOK {
		t.Fatalf("warm cache status=%d", admin.Code)
	}
	unauthenticated := httptest.NewRecorder()
	srv.ServeHTTP(unauthenticated, httptest.NewRequest(http.MethodGet, "/runner_request_metrics", nil))
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated cached response=%d", unauthenticated.Code)
	}
}
