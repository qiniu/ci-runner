package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

type adminReadErrorStore struct {
	state.Store
	err    error
	cancel context.CancelFunc
}

func (s *adminReadErrorStore) ListStatesCursor(context.Context, state.RunnerRequestListOptions, *state.RunnerRequestCursor) ([]state.RunnerState, bool, error) {
	if s.cancel != nil {
		s.cancel()
	}
	return nil, false, s.err
}

func (s *adminReadErrorStore) GetRunnerRequestMetrics(context.Context) (state.RunnerRequestMetrics, error) {
	return state.RunnerRequestMetrics{}, s.err
}

func captureAdminReadLogs(t *testing.T, srv *Server) *bytes.Buffer {
	t.Helper()
	// Stop background loops before replacing the logger or reading its buffer.
	srv.Close()
	var logs bytes.Buffer
	srv.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	return &logs
}

func assertAdminReadFailureLogs(t *testing.T, logs *bytes.Buffer, count int, resource, category string) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(logs.Bytes()))
	found := 0
	for {
		var entry map[string]any
		if err := decoder.Decode(&entry); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if entry["msg"] != "admin runner read failed" {
			continue
		}
		found++
		if entry["resource"] != resource || entry["category"] != category || entry["level"] != "ERROR" {
			t.Errorf("unexpected failure log: %v", entry)
		}
		for key := range entry {
			switch key {
			case "time", "level", "msg", "resource", "category":
			default:
				t.Errorf("unexpected field in bounded failure log: %s", key)
			}
		}
	}
	if found != count {
		t.Fatalf("failure logs=%d want=%d: %s", found, count, logs.String())
	}
	if strings.Contains(logs.String(), "database unavailable") {
		t.Fatal("raw database error leaked into logs")
	}
}

func TestAdminRunnerReadsClassifyCancelledHTTPRequests(t *testing.T) {
	for _, path := range []string{"/runner_requests?limit=100", "/runner_request_metrics"} {
		for _, tc := range []struct {
			name     string
			cancel   bool
			err      error
			status   int
			category string
		}{
			{"cancelled_http", true, context.Canceled, 499, ""},
			{"query_timeout", false, context.DeadlineExceeded, http.StatusInternalServerError, "query_timeout"},
			{"query_cancelled_with_live_http", false, context.Canceled, http.StatusInternalServerError, "query_cancelled"},
			{"database_error", false, errors.New("database unavailable"), http.StatusInternalServerError, "query_failed"},
			{"cancelled_http_with_database_error", true, errors.New("database unavailable"), http.StatusInternalServerError, "query_failed"},
			{"cancelled_http_with_query_timeout", true, context.DeadlineExceeded, http.StatusInternalServerError, "query_timeout"},
		} {
			// Metrics has an independent shared context. Its HTTP cancellation
			// scenarios are exercised with a blocked shared read below.
			if path == "/runner_request_metrics" && tc.cancel {
				continue
			}
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				store := &adminReadErrorStore{Store: state.New(t.TempDir()), err: tc.err}
				if tc.cancel {
					store.cancel = cancel
				}
				srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
				logs := captureAdminReadLogs(t, srv)
				response := httptest.NewRecorder()
				srv.ServeHTTP(response, adminRequest(http.MethodGet, path, nil).WithContext(ctx))
				if response.Code != tc.status {
					t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
				}
				count := 1
				if tc.status == 499 {
					count = 0
				}
				resource := "runner_requests"
				if path == "/runner_request_metrics" {
					resource = "runner_request_metrics"
				}
				assertAdminReadFailureLogs(t, logs, count, resource, tc.category)
			})
		}
	}
}

// Done is evaluated by getRunnerRequestMetrics after joining the singleflight
// read, giving the test a deterministic subscription barrier without sleeps.
type subscribedMetricsContext struct {
	context.Context
	subscribed chan struct{}
	once       sync.Once
}

func (c *subscribedMetricsContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.subscribed) })
	return c.Context.Done()
}

func TestRunnerRequestMetricsFailureLoggedOnceAfterWaiterCancellation(t *testing.T) {
	for _, tc := range []struct {
		name        string
		liveWaiters int
	}{
		{"all_http_waiters_left", 0},
		{"one_live_waiter", 1},
		{"multiple_live_waiters", 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan context.Context, 1)
			release := make(chan struct{})
			var releaseOnce sync.Once
			finishRead := func() { releaseOnce.Do(func() { close(release) }) }
			defer finishRead()
			var calls atomic.Int32
			store := &metricsReadStore{Store: state.New(t.TempDir()), read: func(ctx context.Context) (state.RunnerRequestMetrics, error) {
				if calls.Add(1) == 1 {
					started <- ctx
				}
				<-release
				return state.RunnerRequestMetrics{}, context.DeadlineExceeded
			}}
			srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
			logs := captureAdminReadLogs(t, srv)
			wait := func(ch <-chan struct{}) {
				t.Helper()
				select {
				case <-ch:
				case <-time.After(5 * time.Second):
					t.Fatal("metrics waiter did not reach expected state")
				}
			}
			request := func(ctx context.Context) (*httptest.ResponseRecorder, <-chan struct{}) {
				observed := &subscribedMetricsContext{Context: ctx, subscribed: make(chan struct{})}
				response, done := httptest.NewRecorder(), make(chan struct{})
				go func() {
					srv.ServeHTTP(response, adminRequest(http.MethodGet, "/runner_request_metrics", nil).WithContext(observed))
					close(done)
				}()
				wait(observed.subscribed)
				return response, done
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cancelled, cancelledDone := request(ctx)
			queryCtx := <-started
			responses := make([]*httptest.ResponseRecorder, tc.liveWaiters)
			finished := make([]<-chan struct{}, tc.liveWaiters)
			for i := range responses {
				responses[i], finished[i] = request(context.Background())
			}
			cancel()
			wait(cancelledDone)
			if cancelled.Code != 499 || queryCtx.Err() != nil {
				t.Fatalf("cancelled status=%d shared context error=%v", cancelled.Code, queryCtx.Err())
			}
			assertAdminReadFailureLogs(t, logs, 0, "", "")
			// Observe completion even when there are no HTTP waiters left.
			completed := srv.requestMetrics.group.DoChan("metrics", func() (any, error) {
				t.Error("shared query ended before its release")
				return nil, errors.New("unexpected second query")
			})
			finishRead()
			select {
			case result := <-completed:
				if !errors.Is(result.Err, context.DeadlineExceeded) {
					t.Fatalf("shared result error=%v", result.Err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("shared query did not complete")
			}
			for i, done := range finished {
				wait(done)
				if responses[i].Code != http.StatusInternalServerError {
					t.Errorf("live waiter %d status=%d", i, responses[i].Code)
				}
			}
			if calls.Load() != 1 {
				t.Fatalf("database reads=%d, want one shared read", calls.Load())
			}
			assertAdminReadFailureLogs(t, logs, 1, "runner_request_metrics", "query_timeout")
		})
	}
}
