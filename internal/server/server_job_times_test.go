package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestSignedJobTimesFollowOriginalJobAcrossRunnerAssignment(t *testing.T) {
	const repo = "o/r"
	store := state.New(t.TempDir())
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/jobs/1702" {
			t.Errorf("unexpected lookup %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":1702,"status":"completed","conclusion":"failure","started_at":"2025-01-01T00:01:00Z","completed_at":"2025-01-01T00:02:00Z"}`))
	}))
	defer api.Close()
	srv := newTestServer(t, store, api.URL, &fakeSandbox{})
	for i, id := range []string{"actual-runner", "original-request"} {
		_, _, err := store.CreateRequest(state.RunnerRequest{ID: id, JobID: int64(1701 + i), RepositoryFullName: repo}, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	// A valid source observation identifies the original Job even when the webhook
	// names the Runner provisioned for a different request.
	body := []byte(`{"action":"in_progress","repository":{"full_name":"o/r"},"workflow_job":{"id":1702,"runner_name":"e2b-actual-runner","started_at":"2025-01-01T00:00:00Z"}}`)
	deliver := func(signature string) int {
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "workflow_job")
		req.Header.Set("X-Hub-Signature-256", signature)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w.Code
	}
	if code := deliver("invalid"); code != 401 {
		t.Fatal(code)
	}
	before, err := store.ReadState("original-request")
	if err != nil || before.GitHubJobTiming != nil {
		t.Fatalf("unsigned timing: %v", err)
	}
	for range 2 {
		if code := deliver(sign("secret", body)); code != 202 {
			t.Fatal(code)
		}
	}
	first, err := store.ReadState("original-request")
	if err != nil || first.GitHubJobTiming == nil {
		t.Fatalf("missing signed start: %v", err)
	}
	if _, err := srv.lookupLifecycleJob(t.Context(), repo, 1702); err != nil {
		t.Fatal(err)
	}
	original, err := store.ReadState("original-request")
	if err != nil {
		t.Fatal(err)
	}
	timing := original.GitHubJobTiming
	if timing == nil || timing.StartSource != "webhook" || timing.EndSource != "github_api" || timing.CompletedAt == nil || timing.StartedAt == nil || timing.CompletedAt.Sub(*timing.StartedAt) != 2*time.Minute || timing.Conclusion != "failure" || original.Version != before.Version || !timing.StartObservedAt.Equal(*first.GitHubJobTiming.StartObservedAt) {
		t.Fatalf("timing or version changed unexpectedly: %+v", timing)
	}
	actual, err := store.ReadState("actual-runner")
	if err != nil || actual.GitHubJobTiming != nil {
		t.Fatalf("misattributed timing: %+v %v", actual.GitHubJobTiming, err)
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, adminRequest("GET", "/reports/execution-usage", nil))
	var report struct{ Execution *state.ExecutionUsage }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &report) != nil || report.Execution == nil {
		t.Fatalf("execution report: %d %s", w.Code, w.Body)
	}
}

type failingJobTimeStore struct {
	state.Store
	calls int
}

func (s *failingJobTimeStore) RecordJobTimes(context.Context, string, int64, state.JobTimeObservation) error {
	s.calls++
	return errors.New("timing storage unavailable")
}

func TestWebhookLifecycleSurvivesTimingFailure(t *testing.T) {
	store := &failingJobTimeStore{Store: state.New(t.TempDir())}
	api := httptest.NewServer(githubRunnerAPI(t))
	defer api.Close()
	fake := &fakeSandbox{}
	srv := newTestServerWithoutLoops(t, store, api.URL, fake, 10)
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "1001", JobID: 1001, RepositoryFullName: "o/r", RunnerName: "e2b-1001"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = state.StatusRunning
	st.SandboxID = "box"
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"in_progress", "completed"} {
		body := []byte(fmt.Sprintf(`{"action":%q,"repository":{"full_name":"o/r"},"workflow_job":{"id":1001,"status":%q,"runner_name":"e2b-1001","started_at":"2025-01-01T01:00:00Z"}}`, action, action))
		req := httptest.NewRequest(http.MethodPost, "/webhooks/github", bytes.NewReader(body))
		req.Header.Set("X-GitHub-Event", "workflow_job")
		req.Header.Set("X-Hub-Signature-256", sign("secret", body))
		resp := httptest.NewRecorder()
		srv.ServeHTTP(resp, req)
		saved, err := store.ReadState(st.ID)
		if resp.Code != http.StatusAccepted || err != nil || saved.AssignedJobID != 1001 {
			t.Fatalf("%s: status %d, assigned %d, error %v: %s", action, resp.Code, saved.AssignedJobID, err, resp.Body)
		}
		if action == "completed" && (saved.Status != state.StatusCompleted || fake.stoppedCount() != 1) {
			t.Fatalf("cleanup: status=%s stops=%d", saved.Status, fake.stoppedCount())
		}
	}
	if store.calls != 2 {
		t.Fatalf("timing writes = %d", store.calls)
	}
}
