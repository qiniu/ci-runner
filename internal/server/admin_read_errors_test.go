package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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
	if s.cancel != nil {
		s.cancel()
	}
	return state.RunnerRequestMetrics{}, s.err
}

func TestAdminReadErrorsDistinguishCancelledHTTPRequests(t *testing.T) {
	for _, path := range []string{"/runner_requests?limit=100", "/runner_request_metrics"} {
		for _, tc := range []struct {
			name   string
			cancel bool
			err    error
			status int
		}{
			{"cancelled_http", true, context.Canceled, 499},
			{"shared_query_timeout", false, context.DeadlineExceeded, http.StatusInternalServerError},
			{"query_cancelled_with_live_http", false, context.Canceled, http.StatusInternalServerError},
			{"database_error", false, errors.New("database unavailable"), http.StatusInternalServerError},
		} {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				store := &adminReadErrorStore{Store: state.New(t.TempDir()), err: tc.err}
				if tc.cancel {
					store.cancel = cancel
				}
				srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
				request := adminRequest(http.MethodGet, path, nil).WithContext(ctx)
				response := httptest.NewRecorder()
				srv.ServeHTTP(response, request)
				if response.Code != tc.status {
					t.Fatalf("status=%d want=%d body=%s", response.Code, tc.status, response.Body.String())
				}
			})
		}
	}
}
