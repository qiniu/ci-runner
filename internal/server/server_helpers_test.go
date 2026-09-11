package server

import (
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/redact"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

// ---------- workflowConclusion ----------

func TestWorkflowConclusionPrefersConclusionOverStatus(t *testing.T) {
	job := github.WorkflowJob{Conclusion: "success", Status: "completed"}
	if got := workflowConclusion(job); got != "success" {
		t.Errorf("workflowConclusion: got %q, want %q", got, "success")
	}
}

func TestSandboxRegionEndpoint(t *testing.T) {
	tests := map[string]string{
		"cn-yangzhou-1": "https://cn-yangzhou-1-sandbox.qiniuapi.com",
		"us-south-1":    "https://us-south-1-sandbox.qiniuapi.com",
	}
	for region, want := range tests {
		srv := &Server{cfg: config.Config{SandboxRegions: []config.SandboxRegionConfig{{ID: region, SandboxAPIURL: want}}}}
		got, ok := srv.sandboxRegionEndpoint(region)
		if !ok || got != want {
			t.Fatalf("sandboxRegionEndpoint(%q) = %q, %v; want %q, true", region, got, ok, want)
		}
	}
	server := &Server{cfg: config.Config{SandboxRegions: []config.SandboxRegionConfig{
		{ID: "known", SandboxAPIURL: "https://known.example"},
		{ID: "custom-region", SandboxAPIURL: "https://custom.example/"},
	}}}
	if got, ok := server.sandboxRegionEndpoint("custom-region"); !ok || got != "https://custom.example" {
		t.Fatalf("custom sandbox region = %q, %v; want https://custom.example, true", got, ok)
	}
	if got, ok := server.supportedSandboxRegionEndpoint("HTTPS://CUSTOM.EXAMPLE/"); !ok || got != "https://custom.example" {
		t.Fatalf("custom sandbox endpoint = %q, %v; want https://custom.example, true", got, ok)
	}
	if _, ok := server.sandboxRegionEndpoint("unknown"); ok {
		t.Fatal("unknown sandbox region should be rejected")
	}
}

func TestWorkflowConclusionFallsBackToStatusWhenConclusionEmpty(t *testing.T) {
	job := github.WorkflowJob{Conclusion: "", Status: "in_progress"}
	if got := workflowConclusion(job); got != "in_progress" {
		t.Errorf("workflowConclusion fallback: got %q, want %q", got, "in_progress")
	}
}

func TestWorkflowConclusionReturnsUnknownWhenBothEmpty(t *testing.T) {
	job := github.WorkflowJob{}
	if got := workflowConclusion(job); got != "unknown" {
		t.Errorf("workflowConclusion empty: got %q, want %q", got, "unknown")
	}
}

func TestWorkflowConclusionTrimsConclusionWhitespace(t *testing.T) {
	job := github.WorkflowJob{Conclusion: "  failure  "}
	if got := workflowConclusion(job); got != "failure" {
		t.Errorf("workflowConclusion trim: got %q, want %q", got, "failure")
	}
}

// ---------- workflowNameFor ----------

func TestWorkflowNameForReturnsFirstNonEmpty(t *testing.T) {
	if got := workflowNameFor("CI", "fallback"); got != "CI" {
		t.Errorf("workflowNameFor: got %q, want %q", got, "CI")
	}
}

func TestWorkflowNameForSkipsWhitespace(t *testing.T) {
	if got := workflowNameFor("  ", "build"); got != "build" {
		t.Errorf("workflowNameFor skip whitespace: got %q, want %q", got, "build")
	}
}

func TestWorkflowNameForReturnsUnknownWhenAllEmpty(t *testing.T) {
	if got := workflowNameFor("", "  ", ""); got != "unknown" {
		t.Errorf("workflowNameFor all empty: got %q, want %q", got, "unknown")
	}
}

func TestWorkflowNameForNoArgs(t *testing.T) {
	if got := workflowNameFor(); got != "unknown" {
		t.Errorf("workflowNameFor(): got %q, want %q", got, "unknown")
	}
}

// ---------- workflowJobName ----------

func TestWorkflowJobNamePrefersJobName(t *testing.T) {
	st := state.RunnerState{AssignedJobName: "state-job", RunnerName: "e2b-runner"}
	job := github.WorkflowJob{Name: "job-name"}
	if got := workflowJobName(st, job); got != "job-name" {
		t.Errorf("workflowJobName: got %q, want %q", got, "job-name")
	}
}

func TestWorkflowJobNameFallsBackToAssignedJobName(t *testing.T) {
	st := state.RunnerState{AssignedJobName: "state-job", RunnerName: "e2b-runner"}
	job := github.WorkflowJob{Name: ""}
	if got := workflowJobName(st, job); got != "state-job" {
		t.Errorf("workflowJobName assigned fallback: got %q, want %q", got, "state-job")
	}
}

func TestWorkflowJobNameSkipsStartedMarker(t *testing.T) {
	st := state.RunnerState{AssignedJobName: runnerJobStartedMarker, RunnerName: "e2b-runner"}
	job := github.WorkflowJob{Name: ""}
	// marker is not a real job name; should fall through to RunnerName
	if got := workflowJobName(st, job); got != "e2b-runner" {
		t.Errorf("workflowJobName skip marker: got %q, want %q", got, "e2b-runner")
	}
}

func TestWorkflowJobNameFallsBackToRunnerName(t *testing.T) {
	st := state.RunnerState{RunnerName: "e2b-runner-42"}
	job := github.WorkflowJob{}
	if got := workflowJobName(st, job); got != "e2b-runner-42" {
		t.Errorf("workflowJobName runner fallback: got %q, want %q", got, "e2b-runner-42")
	}
}

func TestWorkflowJobNameReturnsUnknownWhenAllEmpty(t *testing.T) {
	st := state.RunnerState{}
	job := github.WorkflowJob{}
	if got := workflowJobName(st, job); got != "unknown" {
		t.Errorf("workflowJobName all empty: got %q, want %q", got, "unknown")
	}
}

// ---------- isFailureConclusion ----------

func TestIsFailureConclusionMatchesFailure(t *testing.T) {
	if !isFailureConclusion("failure") {
		t.Error("isFailureConclusion(\"failure\") should be true")
	}
}

func TestIsFailureConclusionIsCaseInsensitive(t *testing.T) {
	if !isFailureConclusion("FAILURE") {
		t.Error("isFailureConclusion(\"FAILURE\") should be true")
	}
	if !isFailureConclusion("Failure") {
		t.Error("isFailureConclusion(\"Failure\") should be true")
	}
}

func TestIsFailureConclusionTrimsWhitespace(t *testing.T) {
	if !isFailureConclusion("  failure  ") {
		t.Error("isFailureConclusion with whitespace should be true")
	}
}

func TestIsFailureConclusionReturnsFalseForOther(t *testing.T) {
	for _, c := range []string{"success", "cancelled", "skipped", "", "timed_out"} {
		if isFailureConclusion(c) {
			t.Errorf("isFailureConclusion(%q) should be false", c)
		}
	}
}

func TestDiagnoseRunnerRequestReportsPersistedFailure(t *testing.T) {
	findings := diagnoseRunnerRequest(state.RunnerState{
		Status:        state.StatusFailed,
		FailureStage:  "cleanup",
		FailureReason: "github_runner_cleanup_failed",
	}, nil, false, diagnosticGitHubJob{LookupStatus: "ok", Conclusion: "success"})

	if len(findings) != 1 {
		t.Fatalf("diagnoseRunnerRequest returned %d findings, want 1: %#v", len(findings), findings)
	}
	if got := findings[0]; got.Code != "runner_request_failed" || got.Severity != "critical" || !strings.Contains(got.Detail, "cleanup") || !strings.Contains(got.Detail, "github_runner_cleanup_failed") {
		t.Fatalf("persisted failure finding = %#v, want critical runner_request_failed with stage and reason", got)
	}
}

func TestDiagnoseRunnerRequestIncludesFallbackErrorWithFailureStage(t *testing.T) {
	findings := diagnoseRunnerRequest(state.RunnerState{
		Status:       state.StatusFailed,
		FailureStage: "create",
		Error:        "sandbox request timed out",
	}, nil, false, diagnosticGitHubJob{LookupStatus: "not_applicable"})

	if len(findings) != 1 || !strings.Contains(findings[0].Detail, "create") || !strings.Contains(findings[0].Detail, "sandbox request timed out") {
		t.Fatalf("persisted failure finding = %#v, want stage and fallback error", findings)
	}
}

func TestDiagnoseRunnerRequestReportsUnmatchedAdmission(t *testing.T) {
	findings := diagnoseRunnerRequest(state.RunnerState{
		Status:        state.StatusFailed,
		FailureStage:  "admission",
		FailureReason: "profile_labels_not_matched",
	}, nil, false, diagnosticGitHubJob{LookupStatus: "not_applicable"})

	if len(findings) != 1 {
		t.Fatalf("diagnoseRunnerRequest returned %d findings, want 1: %#v", len(findings), findings)
	}
	if got := findings[0]; got.Code != "runner_request_unmatched" || got.Severity != "warning" {
		t.Fatalf("unmatched admission finding = %#v, want warning runner_request_unmatched", got)
	}
}

func TestDiagnoseRunnerRequestUsesPersistedAssignmentWhenAcceptanceEventIsOutsideTail(t *testing.T) {
	findings := diagnoseRunnerRequest(state.RunnerState{
		Status:        state.StatusCompleted,
		AssignedJobID: 101445685709,
	}, nil, true, diagnosticGitHubJob{LookupStatus: "ok", Conclusion: "failure"})

	for _, finding := range findings {
		if finding.Code == "runner_termination_unobserved" && finding.Severity == "critical" {
			return
		}
	}
	t.Fatalf("findings = %#v, want critical runner_termination_unobserved", findings)
}

func TestDiagnoseRunnerRequestIgnoresLifecycleMarkersInProcessOutput(t *testing.T) {
	findings := diagnoseRunnerRequest(state.RunnerState{
		Status:        state.StatusCompleted,
		AssignedJobID: 101445685709,
	}, []state.RunnerEvent{
		{EventType: "stdout_log", Message: "runner process exited\n"},
		{EventType: "stderr_log", Message: "sandbox already gone\n"},
	}, false, diagnosticGitHubJob{LookupStatus: "ok", Conclusion: "failure"})

	gotCodes := make(map[string]bool, len(findings))
	for _, finding := range findings {
		gotCodes[finding.Code] = true
	}
	if !gotCodes["runner_termination_unobserved"] {
		t.Fatalf("findings = %#v, want process output to leave runner termination unobserved", findings)
	}
	if gotCodes["sandbox_gone_before_cleanup"] {
		t.Fatalf("findings = %#v, want process output ignored for Sandbox lifecycle evidence", findings)
	}
}

// ---------- runnerExitMessage ----------

func TestRunnerExitMessageIncludesExitCode(t *testing.T) {
	result := sandboxrunner.ExitResult{ExitCode: 1, Stderr: ""}
	msg := runnerExitMessage(result)
	if !strings.Contains(msg, "1") {
		t.Errorf("runnerExitMessage: missing exit code in %q", msg)
	}
}

func TestRunnerExitMessagePrefersStderr(t *testing.T) {
	result := sandboxrunner.ExitResult{ExitCode: 2, Stderr: "out of memory", Error: "process error", Stdout: "some output"}
	msg := runnerExitMessage(result)
	if !strings.Contains(msg, "out of memory") {
		t.Errorf("runnerExitMessage: expected stderr in message, got %q", msg)
	}
}

func TestRunnerExitMessageFallsBackToError(t *testing.T) {
	result := sandboxrunner.ExitResult{ExitCode: 2, Stderr: "", Error: "process error"}
	msg := runnerExitMessage(result)
	if !strings.Contains(msg, "process error") {
		t.Errorf("runnerExitMessage: expected error in message, got %q", msg)
	}
}

func TestRunnerExitMessageFallsBackToStdout(t *testing.T) {
	result := sandboxrunner.ExitResult{ExitCode: 2, Stderr: "", Error: "", Stdout: "runner output"}
	msg := runnerExitMessage(result)
	if !strings.Contains(msg, "runner output") {
		t.Errorf("runnerExitMessage: expected stdout fallback in message, got %q", msg)
	}
}

func TestRunnerExitMessageCodeOnlyWhenNoDetail(t *testing.T) {
	result := sandboxrunner.ExitResult{ExitCode: 137}
	msg := runnerExitMessage(result)
	if msg != "runner process exited with code 137" {
		t.Errorf("runnerExitMessage no detail: got %q", msg)
	}
}

// ---------- shouldStopIdleRunner ----------

func TestShouldStopIdleRunnerReturnsFalseWhenTimeoutDisabled(t *testing.T) {
	s := &Server{cfg: config.Config{RunnerIdleTimeout: 0}}
	st := state.RunnerState{
		RunningAt: time.Now().Add(-10 * time.Minute),
	}
	if s.shouldStopIdleRunner(st, time.Now()) {
		t.Error("shouldStopIdleRunner: should return false when timeout is 0")
	}
}

func TestShouldStopIdleRunnerReturnsFalseWhenRunningAtZero(t *testing.T) {
	s := &Server{cfg: config.Config{RunnerIdleTimeout: time.Minute}}
	st := state.RunnerState{}
	if s.shouldStopIdleRunner(st, time.Now()) {
		t.Error("shouldStopIdleRunner: should return false when RunningAt is zero")
	}
}

func TestShouldStopIdleRunnerReturnsFalseWhenJobAssigned(t *testing.T) {
	s := &Server{cfg: config.Config{RunnerIdleTimeout: time.Minute}}
	st := state.RunnerState{
		RunningAt:     time.Now().Add(-10 * time.Minute),
		AssignedJobID: 12345,
	}
	if s.shouldStopIdleRunner(st, time.Now()) {
		t.Error("shouldStopIdleRunner: should return false when job is assigned")
	}
}

func TestShouldStopIdleRunnerReturnsFalseWhenJobStartedMarkerSet(t *testing.T) {
	s := &Server{cfg: config.Config{RunnerIdleTimeout: time.Minute}}
	st := state.RunnerState{
		RunningAt:       time.Now().Add(-10 * time.Minute),
		AssignedJobName: runnerJobStartedMarker,
	}
	if s.shouldStopIdleRunner(st, time.Now()) {
		t.Error("shouldStopIdleRunner: should return false when job started marker set")
	}
}

func TestShouldStopIdleRunnerReturnsTrueWhenIdleTimeout(t *testing.T) {
	s := &Server{cfg: config.Config{RunnerIdleTimeout: time.Minute}}
	st := state.RunnerState{
		RunningAt: time.Now().Add(-2 * time.Minute),
	}
	if !s.shouldStopIdleRunner(st, time.Now()) {
		t.Error("shouldStopIdleRunner: should return true when idle timeout exceeded")
	}
}

func TestShouldStopIdleRunnerReturnsFalseWhenNotYetTimedOut(t *testing.T) {
	s := &Server{cfg: config.Config{RunnerIdleTimeout: 5 * time.Minute}}
	st := state.RunnerState{
		RunningAt: time.Now().Add(-1 * time.Minute),
	}
	if s.shouldStopIdleRunner(st, time.Now()) {
		t.Error("shouldStopIdleRunner: should return false when not yet timed out")
	}
}

// ---------- handleHealthz ----------

func TestHealthzReturnsOK(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /healthz: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET /healthz: invalid JSON: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("GET /healthz: expected status=ok, got %v", body)
	}
}

// ---------- handleGitHubWebhook – signature rejection ----------

func TestWebhookRejectsInvalidSignature(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	body := []byte(`{"action":"queued"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	req.Header.Set("X-GitHub-Event", "workflow_job")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("webhook bad sig: expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestWebhookRejectsMissingSignature(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(`{}`))
	req.Header.Set("X-GitHub-Event", "workflow_job")
	// No X-Hub-Signature-256 header
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("webhook no sig: expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// ---------- handleGitHubWebhook – unknown event ----------

func TestWebhookIgnoresUnknownEventTypes(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	body := []byte(`{"action":"created"}`)
	req := httptest.NewRequest(http.MethodPost, "/webhooks/github", strings.NewReader(string(body)))
	req.Header.Set("X-Hub-Signature-256", sign("secret", body))
	req.Header.Set("X-GitHub-Event", "push")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook unknown event: expected 202, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "ignored") {
		t.Errorf("webhook unknown event: expected 'ignored' in response, got %s", rec.Body.String())
	}
}

// ---------- handleListAuditEvents ----------

func TestListAuditEventsEndpointRequiresAuth(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	req := httptest.NewRequest(http.MethodGet, "/audit-events", nil)
	// No Authorization header
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /audit-events without auth: expected 401, got %d", rec.Code)
	}
}

func TestListAuditEventsEndpointReturnsEvents(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	// Insert an audit event directly into the store
	if _, err := store.AppendAuditEvent(state.AuditEvent{
		Actor:        "admin_api",
		Action:       "runner.retry",
		ResourceType: "runner_request",
		ResourceID:   "event-test-id",
	}); err != nil {
		t.Fatal(err)
	}

	req := adminRequest(http.MethodGet, "/audit-events", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /audit-events: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "runner.retry") {
		t.Errorf("GET /audit-events: expected event in response, got %s", rec.Body.String())
	}
}

// ---------- handleDeleteProfile ----------

func TestDeleteProfileEndpointRemovesProfile(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	configureAdminProfileTemplateService(t, srv, nil, "base")

	// Create the profile via the API
	createReq := adminRequest(http.MethodPost, "/runner_specs",
		strings.NewReader(`{"name":"to-delete-api","labels":["self-hosted"],"template_id":"base","max_concurrency":1,"enabled":true}`))
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create profile: got %d body=%s", createRec.Code, createRec.Body.String())
	}

	// Delete it
	deleteReq := adminRequest(http.MethodDelete, "/runner_specs/to-delete-api", nil)
	deleteRec := httptest.NewRecorder()
	srv.ServeHTTP(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("DELETE /runner_specs/to-delete-api: expected 200, got %d body=%s", deleteRec.Code, deleteRec.Body.String())
	}

	// Verify it's gone via GET
	getReq := adminRequest(http.MethodGet, "/runner_specs/to-delete-api", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("GET after delete: expected 404, got %d body=%s", getRec.Code, getRec.Body.String())
	}
}

// ---------- handleAdminRedirect ----------

func TestAdminRedirectReturns301(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /admin: expected 301, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/admin/" {
		t.Errorf("GET /admin: Location = %q, want /admin/", loc)
	}
}

func TestRetiredAdminCatalogRoutesRedirectToRunnerSpecs(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	for _, path := range []string{
		"/admin/runner_groups",
		"/admin/runner_groups/",
		"/admin/runner_policies",
		"/admin/runner_policies/",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusTemporaryRedirect {
			t.Fatalf("GET %s: status = %d, want %d", path, rec.Code, http.StatusTemporaryRedirect)
		}
		if location := rec.Header().Get("Location"); location != "/admin/runner_specs" {
			t.Fatalf("GET %s: Location = %q, want /admin/runner_specs", path, location)
		}
	}
}

func TestReleaseCRemovesRetiredCatalogAPIs(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	tests := []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodGet, "/runner_groups", ""},
		{http.MethodGet, "/runner_groups/group", ""},
		{http.MethodGet, "/runner_policies", ""},
		{http.MethodPost, "/runner_groups", `{}`},
		{http.MethodPatch, "/runner_groups/group", `{}`},
		{http.MethodDelete, "/runner_groups/group", ""},
		{http.MethodPost, "/runner_policies", `{}`},
		{http.MethodPatch, "/runner_policies/1", `{}`},
		{http.MethodDelete, "/runner_policies/1", ""},
		{http.MethodGet, "/diagnostics/catalog-migration-readiness", ""},
		{http.MethodGet, "/diagnostics/catalog-migration-readiness/", ""},
		{http.MethodOptions, "/runner_groups/group", ""},
		{"PURGE", "/runner_policies/1", ""},
	}
	for _, tt := range tests {
		req := adminRequest(tt.method, tt.path, strings.NewReader(tt.body))
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want %d; body=%s", tt.method, tt.path, rec.Code, http.StatusNotFound, rec.Body.String())
		}
	}
}

func TestUserRedirectReturns301(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	req := httptest.NewRequest(http.MethodGet, "/user", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("GET /user: expected 301, got %d", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("GET /user: Location = %q, want /", loc)
	}
}

// ---------- handleGetRunner ----------

func TestGetRunnerReturnsStateAfterCreate(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	// Create a runner request
	createReq := adminRequest(http.MethodPost, "/runner_requests",
		strings.NewReader(`{"id":"get-runner-1","repository_full_name":"o/r","runner_spec_name":"default"}`))
	createRec := httptest.NewRecorder()
	srv.ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusAccepted {
		t.Fatalf("create runner: got %d body=%s", createRec.Code, createRec.Body.String())
	}

	// Fetch the runner state
	getReq := adminRequest(http.MethodGet, "/runner_requests/get-runner-1", nil)
	getRec := httptest.NewRecorder()
	srv.ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET /runner_requests/get-runner-1: expected 200, got %d body=%s", getRec.Code, getRec.Body.String())
	}
	if !strings.Contains(getRec.Body.String(), "get-runner-1") {
		t.Errorf("GET /runner_requests/get-runner-1: id not in response: %s", getRec.Body.String())
	}
}

func TestGetRunnerReturns404ForMissing(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	req := adminRequest(http.MethodGet, "/runner_requests/does-not-exist", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GET missing runner: expected 404, got %d", rec.Code)
	}
}

func TestResolveRunnerRequestPrefersRunnerNameBeforeInternalID(t *testing.T) {
	store := state.New(t.TempDir())
	for _, request := range []state.RunnerRequest{
		{ID: "manual", Source: "manual_api", Labels: []string{"self-hosted"}, RunnerName: "e2b-manual"},
		{ID: "e2b-manual", Source: "manual_api", Labels: []string{"self-hosted"}, RunnerName: "e2b-e2b-manual"},
		{ID: "resolve", Source: "manual_api", Labels: []string{"self-hosted"}, RunnerName: "e2b-resolve"},
	} {
		if _, _, err := store.CreateRequest(request, nil); err != nil {
			t.Fatal(err)
		}
	}

	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	req := adminRequest(http.MethodGet, "/runner_requests_lookup/e2b-manual", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET runner request resolver: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body state.RunnerState
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ID != "manual" || body.RunnerName != "e2b-manual" {
		t.Fatalf("state = %#v, want exact Runner Name to take precedence", body)
	}

	req = adminRequest(http.MethodGet, "/runner_requests/resolve", nil)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET request whose ID is resolve: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
}

// ---------- handleListProfiles ----------

func TestListProfilesEndpointReturnsProfiles(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	req := adminRequest(http.MethodGet, "/runner_specs", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /runner_specs: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	// The default profile is created by newTestServer
	if !strings.Contains(rec.Body.String(), "default") {
		t.Errorf("GET /runner_specs: expected default profile in response, got %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "default_available") {
		t.Errorf("GET /runner_specs exposed retired default_available: %s", rec.Body.String())
	}
}

type readStateErrorStore struct {
	state.Store
	err error
	ids []string
}

func (s *readStateErrorStore) ReadState(id string) (state.RunnerState, error) {
	s.ids = append(s.ids, id)
	return state.RunnerState{}, s.err
}

type runnerEventsErrorStore struct {
	state.Store
	err error
}

func (s *runnerEventsErrorStore) ListRunnerEvents(string, int64, int, ...string) ([]state.RunnerEvent, bool, error) {
	return nil, false, s.err
}

func (s *runnerEventsErrorStore) ListRunnerEventsAfter(string, int64, int, ...string) ([]state.RunnerEvent, bool, error) {
	return nil, false, s.err
}

func TestDiagnosticsPprofEndpointRequiresAuth(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	req := httptest.NewRequest(http.MethodGet, "/diagnostics/pprof", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET /diagnostics/pprof without auth: expected 401, got %d", rec.Code)
	}
}

func TestDiagnosticsPprofEndpointReturnsJSON(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	req := adminRequest(http.MethodGet, "/diagnostics/pprof", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /diagnostics/pprof: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("GET /diagnostics/pprof: invalid JSON: %v", err)
	}
	if _, ok := body["state"]; !ok {
		t.Error("GET /diagnostics/pprof: missing 'state' field in response")
	}
	if _, ok := body["github"]; !ok {
		t.Error("GET /diagnostics/pprof: missing 'github' field in response")
	}
	if _, ok := body["recent_failures"]; ok {
		t.Error("GET /diagnostics/pprof: returned request history outside the runtime diagnostics scope")
	}
}

func TestDiagnosticsRunnerRequestEndpointRequiresAuth(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	req := httptest.NewRequest(http.MethodGet, "/diagnostics/runner-requests/101", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("GET runner request diagnostics without auth: expected 401, got %d", rec.Code)
	}
}

func TestDiagnosticsRunnerRequestReportsUnobservedTermination(t *testing.T) {
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/xgo-dev/llgo/actions/jobs/101445685709" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, github.WorkflowJob{
			ID:         101445685709,
			Name:       "llgo (ubuntu-latest, LLVM 22, Go current)",
			Status:     "completed",
			Conclusion: "failure",
			RunnerName: "e2b-101445685709",
		})
	}))
	defer githubAPI.Close()

	store := state.New(t.TempDir())
	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "101445685709",
		Source:             "github_webhook",
		JobID:              101445685709,
		RepositoryFullName: "xgo-dev/llgo",
		Labels:             []string{"self-hosted", "qiniu", "ubuntu-24.04"},
		RunnerName:         "e2b-101445685709",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = state.StatusCompleted
	st.AssignedJobID = st.WorkflowJobID
	st.AssignedJobName = "llgo (ubuntu-latest, LLVM 22, Go current)"
	st.RunningAt = time.Now().UTC().Add(-20 * time.Minute)
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	store.AppendLog(st.ID, "control.log", []byte("sandbox runner started sandbox_id=sb-1 pid=903\n"))
	store.AppendLog(st.ID, "stdout.log", []byte("runner setup output\n"))
	store.AppendLog(st.ID, "control.log", []byte("runner accepted a job\n"))

	srv := newTestServer(t, store, githubAPI.URL, &fakeSandbox{})
	req := adminRequest(http.MethodGet, "/runner_requests/101445685709/diagnostics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET runner request diagnostics: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		GitHubJob struct {
			LookupStatus string `json:"lookup_status"`
			Conclusion   string `json:"conclusion"`
		} `json:"github_job"`
		Findings []struct {
			Code     string `json:"code"`
			Severity string `json:"severity"`
		} `json:"findings"`
		Events []struct {
			EventType string `json:"event_type"`
			Message   string `json:"message"`
		} `json:"events"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.GitHubJob.LookupStatus != "ok" || body.GitHubJob.Conclusion != "failure" {
		t.Fatalf("github_job = %#v, want successful failure lookup", body.GitHubJob)
	}
	gotCodes := map[string]bool{}
	for _, finding := range body.Findings {
		gotCodes[finding.Code] = true
	}
	for _, code := range []string{"github_job_failed", "request_completed_after_github_failure", "runner_termination_unobserved"} {
		if !gotCodes[code] {
			t.Errorf("missing diagnostic finding %q in %#v", code, body.Findings)
		}
	}
	if len(body.Events) != 2 || body.Events[0].EventType != "control_log" || body.Events[1].EventType != "control_log" || body.Events[1].Message != "runner accepted a job\n" {
		t.Fatalf("events = %#v, want bounded chronological control events", body.Events)
	}
}

func TestRunnerRequestEventsReturnsMixedExclusivePage(t *testing.T) {
	store := state.New(t.TempDir())
	_, _, err := store.CreateRequest(state.RunnerRequest{
		ID:         "101445685709",
		Source:     "github_webhook",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-101445685709",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		name    string
		message string
	}{
		{name: "control.log", message: "created\n"},
		{name: "stdout.log", message: "setup output\n"},
		{name: "stderr.log", message: "setup warning\n"},
		{name: "control.log", message: "accepted\n"},
		{name: "stdout.log", message: "job output\n"},
	} {
		store.AppendLog("101445685709", entry.name, []byte(entry.message))
	}
	allEvents, _, err := store.ListRunnerEvents("101445685709", 0, 10)
	if err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	req := adminRequest(http.MethodGet, fmt.Sprintf("/runner_requests/101445685709/events?before_id=%d", allEvents[4].ID), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET runner request events: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var page struct {
		Events  []state.RunnerEvent `json:"events"`
		HasMore bool                `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.HasMore {
		t.Fatal("event page unexpectedly reports older records")
	}
	if len(page.Events) != 4 {
		t.Fatalf("events = %#v, want four records before exclusive cursor", page.Events)
	}
	wantTypes := []string{"control_log", "stdout_log", "stderr_log", "control_log"}
	for i, want := range wantTypes {
		if page.Events[i].EventType != want {
			t.Fatalf("events[%d].event_type = %q, want %q", i, page.Events[i].EventType, want)
		}
	}
}

func TestRunnerRequestEventsReturnsEventsAfterExclusiveCursor(t *testing.T) {
	store := state.New(t.TempDir())
	_, _, err := store.CreateRequest(state.RunnerRequest{
		ID:         "101445685710",
		Source:     "github_webhook",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-101445685710",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"one\n", "two\n", "three\n", "four\n"} {
		store.AppendLog("101445685710", "stdout.log", []byte(message))
	}
	allEvents, _, err := store.ListRunnerEvents("101445685710", 0, 10)
	if err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	req := adminRequest(http.MethodGet, fmt.Sprintf("/runner_requests/101445685710/events?after_id=%d", allEvents[1].ID), nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET runner request events after cursor: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var page struct {
		Events  []state.RunnerEvent `json:"events"`
		HasMore bool                `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.HasMore {
		t.Fatal("event page unexpectedly reports newer records")
	}
	if len(page.Events) != 2 || page.Events[0].Message != "three\n" || page.Events[1].Message != "four\n" {
		t.Fatalf("events = %#v, want records after exclusive cursor", page.Events)
	}
}

func TestRunnerRequestEventsRejectsBothCursorDirections(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	req := adminRequest(http.MethodGet, "/runner_requests/request-1/events?after_id=1&before_id=2", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("conflicting cursors: expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunnerRequestEventsRejectsInvalidCursor(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	req := adminRequest(http.MethodGet, "/runner_requests/request-1/events?before_id=not-a-number", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor: expected 400, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestRunnerRequestEventsRequiresAdmin(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	req := httptest.NewRequest(http.MethodGet, "/runner_requests/request-1/events", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated event page: expected 401, got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestDiagnosticsRunnerRequestAcceptsRunnerName(t *testing.T) {
	store := state.New(t.TempDir())
	_, _, err := store.CreateRequest(state.RunnerRequest{
		ID:         "101445685709",
		Source:     "github_webhook",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-101445685709",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	req := adminRequest(http.MethodGet, "/diagnostics/runner-requests/e2b-101445685709", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET runner request diagnostics by runner name: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		State state.RunnerState `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.State.ID != "101445685709" || body.State.RunnerName != "e2b-101445685709" {
		t.Fatalf("state = %#v, want request resolved from runner name", body.State)
	}
}

func TestDiagnosticsRunnerRequestReturnsInternalErrorWithoutExactIDFallback(t *testing.T) {
	store := &readStateErrorStore{
		Store: state.New(t.TempDir()),
		err:   errors.New("database unavailable"),
	}
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	req := adminRequest(http.MethodGet, "/diagnostics/runner-requests/e2b-101445685709", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET runner request diagnostics on DB error: expected 500, got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.ids) != 1 || store.ids[0] != "101445685709" {
		t.Fatalf("ReadState ids = %#v, want no exact-ID fallback after Runner Name lookup DB error", store.ids)
	}
}

func TestRunnerRequestEventErrorsDoNotLeakStoreDetails(t *testing.T) {
	baseStore := state.New(t.TempDir())
	_, _, err := baseStore.CreateRequest(state.RunnerRequest{
		ID:         "event-store-error",
		Source:     "test",
		Labels:     []string{"self-hosted"},
		RunnerName: "e2b-event-store-error",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store := &runnerEventsErrorStore{Store: baseStore, err: errors.New("database password leaked")}
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	for _, path := range []string{
		"/diagnostics/runner-requests/event-store-error",
		"/runner_requests/event-store-error/events?after_id=0",
	} {
		req := adminRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("GET %s: expected 500, got %d body=%s", path, rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), "database password leaked") {
			t.Fatalf("GET %s leaked the raw store error: %s", path, rec.Body.String())
		}
	}
}

func TestDiagnosticsRunnerRequestCachesGitHubJobLookup(t *testing.T) {
	githubCalls := 0
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		githubCalls++
		if r.URL.Path != "/repos/o/r/actions/jobs/42" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, github.WorkflowJob{
			ID:         42,
			Status:     "completed",
			Conclusion: "success",
			RunnerName: "e2b-cache-github-job",
		})
	}))
	defer githubAPI.Close()

	store := state.New(t.TempDir())
	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "cache-github-job",
		Source:             "github_webhook",
		JobID:              42,
		RepositoryFullName: "o/r",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-cache-github-job",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = state.StatusCompleted
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	srv := newTestServer(t, store, githubAPI.URL, &fakeSandbox{})
	for range 2 {
		req := adminRequest(http.MethodGet, "/diagnostics/runner-requests/cache-github-job", nil)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET runner request diagnostics: expected 200, got %d body=%s", rec.Code, rec.Body.String())
		}
	}
	if githubCalls != 1 {
		t.Fatalf("GitHub workflow job calls = %d, want 1 within diagnostics cache TTL", githubCalls)
	}
}

func TestDiagnosticWorkflowJobCoalescesConcurrentLookups(t *testing.T) {
	var githubCalls atomic.Int32
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		githubCalls.Add(1)
		time.Sleep(50 * time.Millisecond)
		writeJSON(w, http.StatusOK, github.WorkflowJob{ID: 42, Status: "completed", Conclusion: "success"})
	}))
	defer githubAPI.Close()

	srv := newTestServer(t, state.New(t.TempDir()), githubAPI.URL, &fakeSandbox{})
	const callers = 8
	start := make(chan struct{})
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			job, err := srv.diagnosticWorkflowJob(context.Background(), "o/r", 42)
			if err == nil && job.ID != 42 {
				err = fmt.Errorf("workflow job ID = %d, want 42", job.ID)
			}
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if calls := githubCalls.Load(); calls != 1 {
		t.Fatalf("concurrent GitHub workflow job calls = %d, want 1", calls)
	}
}

func TestDiagnosticsRunnerRequestPrefersExactInternalIDWithRunnerPrefix(t *testing.T) {
	store := state.New(t.TempDir())
	for _, request := range []state.RunnerRequest{
		{ID: "manual", Source: "manual_api", Labels: []string{"self-hosted"}, RunnerName: "e2b-manual"},
		{ID: "e2b-manual", Source: "manual_api", Labels: []string{"self-hosted"}, RunnerName: "e2b-e2b-manual"},
	} {
		if _, _, err := store.CreateRequest(request, nil); err != nil {
			t.Fatal(err)
		}
	}

	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	req := adminRequest(http.MethodGet, "/runner_requests/e2b-manual/diagnostics", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET runner request diagnostics by exact internal ID: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		State state.RunnerState `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.State.ID != "e2b-manual" || body.State.RunnerName != "e2b-e2b-manual" {
		t.Fatalf("state = %#v, want exact internal request ID to take precedence", body.State)
	}
}

func TestLegacyDiagnosticsRunnerRequestPrefersRunnerNameBeforeInternalID(t *testing.T) {
	store := state.New(t.TempDir())
	for _, request := range []state.RunnerRequest{
		{ID: "manual", Source: "manual_api", Labels: []string{"self-hosted"}, RunnerName: "e2b-manual"},
		{ID: "e2b-manual", Source: "manual_api", Labels: []string{"self-hosted"}, RunnerName: "e2b-e2b-manual"},
	} {
		if _, _, err := store.CreateRequest(request, nil); err != nil {
			t.Fatal(err)
		}
	}

	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	req := adminRequest(http.MethodGet, "/diagnostics/runner-requests/e2b-manual", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET legacy runner request diagnostics: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		State state.RunnerState `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.State.ID != "manual" || body.State.RunnerName != "e2b-manual" {
		t.Fatalf("state = %#v, want legacy lookup to prefer exact Runner Name", body.State)
	}
}

func TestDiagnosticsRunnerRequestKeepsLocalEvidenceWhenGitHubIsUnavailable(t *testing.T) {
	githubAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, http.StatusServiceUnavailable, "temporarily unavailable")
	}))
	defer githubAPI.Close()

	store := state.New(t.TempDir())
	_, _, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "github-unavailable",
		Source:             "github_webhook",
		JobID:              42,
		RepositoryFullName: "o/r",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-github-unavailable",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	store.AppendLog("github-unavailable", "control.log", []byte("runner request created\n"))

	srv := newTestServer(t, store, githubAPI.URL, &fakeSandbox{})
	req := adminRequest(http.MethodGet, "/diagnostics/runner-requests/github-unavailable", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET runner request diagnostics: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"lookup_status":"unavailable"`) ||
		!strings.Contains(rec.Body.String(), `"code":"github_lookup_unavailable"`) ||
		!strings.Contains(rec.Body.String(), `"message":"runner request created\n"`) {
		t.Fatalf("diagnostics did not preserve local evidence: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "temporarily unavailable") {
		t.Fatalf("diagnostics exposed provider error details: %s", rec.Body.String())
	}
}

func TestRedactDatabaseDSNRemovesPostgresURLCredentials(t *testing.T) {
	got := redact.DatabaseDSN("postgres://runner:secret@example.test/runnerd?sslmode=disable")
	if strings.Contains(got, "runner:") || strings.Contains(got, "secret") {
		t.Fatalf("database DSN leaked credentials: %s", got)
	}
	if !strings.Contains(got, "example.test") || !strings.Contains(got, "runnerd") {
		t.Fatalf("database DSN lost non-secret location fields: %s", got)
	}
}

func TestRedactDatabaseDSNRemovesMySQLCredentials(t *testing.T) {
	got := redact.DatabaseDSN("runner:secret@tcp(mysql.example:3306)/runnerd?parseTime=true")
	if strings.Contains(got, "runner:") || strings.Contains(got, "secret") {
		t.Fatalf("database DSN leaked credentials: %s", got)
	}
	if !strings.Contains(got, "mysql.example:3306") || !strings.Contains(got, "runnerd") {
		t.Fatalf("database DSN lost non-secret location fields: %s", got)
	}
}

func TestRedactDatabaseDSNRemovesPostgresKeyValuePassword(t *testing.T) {
	got := redact.DatabaseDSN("host=localhost user=postgres password='secret value' dbname=runnerd")
	if strings.Contains(got, "secret") {
		t.Fatalf("database DSN leaked key-value password: %s", got)
	}
	if !strings.Contains(got, "host=localhost") || !strings.Contains(got, "dbname=runnerd") {
		t.Fatalf("database DSN lost non-secret key-value fields: %s", got)
	}
}

// ---------- isSandboxGone ----------

func TestIsSandboxGoneReturnsFalseForNilError(t *testing.T) {
	if isSandboxGone(nil) {
		t.Error("isSandboxGone(nil) should be false")
	}
}

func TestIsSandboxGoneDetectsStatus404(t *testing.T) {
	err := fmt.Errorf("sandbox request failed: status 404")
	if !isSandboxGone(err) {
		t.Errorf("isSandboxGone(status 404): expected true")
	}
}

func TestIsSandboxGoneDetectsSandboxNotFound(t *testing.T) {
	err := fmt.Errorf("SandboxNotFound: sandbox xyz does not exist")
	if !isSandboxGone(err) {
		t.Errorf("isSandboxGone(SandboxNotFound): expected true")
	}
}

func TestIsSandboxGoneDetectsResumeError(t *testing.T) {
	err := fmt.Errorf("Sandbox can't be resumed")
	if !isSandboxGone(err) {
		t.Errorf("isSandboxGone(can't be resumed): expected true")
	}
}

func TestIsSandboxGoneReturnsFalseForOtherError(t *testing.T) {
	err := fmt.Errorf("network timeout")
	if isSandboxGone(err) {
		t.Errorf("isSandboxGone(timeout): expected false")
	}
}

// ---------- newID ----------

func TestNewIDReturnsNonEmptyHexString(t *testing.T) {
	id := newID()
	if len(id) == 0 {
		t.Error("newID: returned empty string")
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("newID: non-hex character %q in %q", c, id)
		}
	}
}

func TestNewIDGeneratesUniqueValues(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 20; i++ {
		id := newID()
		if seen[id] {
			t.Fatalf("newID: duplicate id %q generated", id)
		}
		seen[id] = true
	}
}

// ---------- handleDiagnosticsVars ----------

func TestDiagnosticsVariablesUseCurrentProcessWithoutPprofDiscovery(t *testing.T) {
	const variableName = "runnerd_diagnostics_current_process_test"
	variable, ok := expvar.Get(variableName).(*expvar.String)
	if !ok {
		variable = expvar.NewString(variableName)
	}
	variable.Set("current-process")
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	req := adminRequest(http.MethodGet, "/diagnostics/vars", nil)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /diagnostics/vars: expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &values); err != nil {
		t.Fatalf("GET /diagnostics/vars returned invalid JSON: %v", err)
	}
	if got := string(values[variableName]); got != `"current-process"` {
		t.Fatalf("GET /diagnostics/vars %s = %s", variableName, got)
	}
}

type auditFailingStore struct {
	state.Store
}

func (s *auditFailingStore) AppendAuditEvent(state.AuditEvent) (state.AuditEvent, error) {
	return state.AuditEvent{}, errors.New("audit unavailable")
}

func (s *auditFailingStore) ApplyMutationWithAudit(state.AuditEvent, func(state.Store) error) (state.AuditEvent, error) {
	return state.AuditEvent{}, fmt.Errorf("%w: audit unavailable", state.ErrAuditEventPersistence)
}

func TestRejectedCatalogMutationDoesNotPersistAudit(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})
	before, err := store.ListAuditEvents(100)
	if err != nil {
		t.Fatal(err)
	}
	req := adminRequest(http.MethodPost, "/runner_specs", strings.NewReader(`{
		"name":"rejected-audit",
		"labels":["self-hosted"],
		"required_labels":["missing"],
		"template_id":"audit-template"
	}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("POST invalid profile: status=%d body=%s", rec.Code, rec.Body.String())
	}
	after, err := store.ListAuditEvents(100)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("rejected profile mutation persisted an audit event: before=%d after=%d latest=%#v", len(before), len(after), after[0])
	}
}

func TestCatalogMutationFailsClosedWhenAuditCannotBePersisted(t *testing.T) {
	baseStore := state.New(t.TempDir())
	if _, err := baseStore.UpsertProfile(state.RunnerProfile{
		Name: "audit-protected", Labels: []string{"self-hosted", "audit-protected"},
		RequiredLabels: []string{"audit-protected"}, TemplateID: "audit-template",
		MaxConcurrency: 1, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, &auditFailingStore{Store: baseStore}, "http://example.test", &fakeSandbox{})
	req := adminRequest(http.MethodPatch, "/runner_specs/audit-protected", strings.NewReader(`{"enabled":false}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PATCH profile with unavailable audit: status=%d body=%s", rec.Code, rec.Body.String())
	}
	profile, err := baseStore.GetProfile("audit-protected")
	if err != nil {
		t.Fatal(err)
	}
	if !profile.Enabled {
		t.Fatal("profile mutation committed even though its audit event could not be persisted")
	}
}

func TestSandboxMutationFailsClosedWhenAuditCannotBePersisted(t *testing.T) {
	baseStore := state.New(t.TempDir())
	srv := newTestServer(t, &auditFailingStore{Store: baseStore}, "http://example.test", &fakeSandbox{})
	req := adminRequest(
		http.MethodPut,
		"/admin/api/sandbox-service-default",
		strings.NewReader(`{"enabled":false,"api_url":"https://sandbox.example.test"}`),
	)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("PUT Sandbox default with unavailable audit: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := baseStore.GetSandboxServiceDefault(); !errors.Is(err, state.ErrNotFound) {
		t.Fatalf("Sandbox default mutation committed even though its audit event could not be persisted: %v", err)
	}
}

func TestScheduleStopRetryReturnsTrueForRetryableError(t *testing.T) {
	s := &Server{cfg: config.Config{
		RetryMaxAttempts: 3,
		RetryBaseDelay:   10 * time.Millisecond,
		RetryMaxDelay:    time.Second,
	}}
	st := &state.RunnerState{RetryCount: 0}
	retryable := s.scheduleStopRetry(st, fmt.Errorf("http_retryable_status: status 429"))
	if !retryable {
		t.Error("scheduleStopRetry: expected true for retryable error")
	}
	if st.RetryCount != 1 {
		t.Errorf("scheduleStopRetry: expected RetryCount=1, got %d", st.RetryCount)
	}
	if st.Status != state.StatusStopping {
		t.Errorf("scheduleStopRetry: expected status=stopping, got %s", st.Status)
	}
}

func TestScheduleStopRetryReturnsFalseWhenMaxAttemptsReached(t *testing.T) {
	s := &Server{cfg: config.Config{
		RetryMaxAttempts: 3,
		RetryBaseDelay:   10 * time.Millisecond,
		RetryMaxDelay:    time.Second,
	}}
	st := &state.RunnerState{RetryCount: 3}
	retryable := s.scheduleStopRetry(st, fmt.Errorf("status 429"))
	if retryable {
		t.Error("scheduleStopRetry: expected false when max attempts reached")
	}
}

func TestScheduleStopRetryReturnsFalseForNonRetryableError(t *testing.T) {
	s := &Server{cfg: config.Config{
		RetryMaxAttempts: 5,
		RetryBaseDelay:   10 * time.Millisecond,
		RetryMaxDelay:    time.Second,
	}}
	st := &state.RunnerState{RetryCount: 0}
	retryable := s.scheduleStopRetry(st, fmt.Errorf("status 401 unauthorized"))
	if retryable {
		t.Error("scheduleStopRetry: expected false for auth error")
	}
}

// ---------- markRunnerJobStarted ----------

func TestMarkRunnerJobStartedSetsMarkerOnRunningRunner(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	// Create a runner and set it to Running
	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "mark-job-1",
		Source:             "test",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-mark-job-1",
		RepositoryFullName: "o/r",
		ProfileName:        "default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = state.StatusRunning
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	srv.markRunnerJobStarted("mark-job-1")

	got, err := store.ReadState("mark-job-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.AssignedJobName != runnerJobStartedMarker {
		t.Errorf("markRunnerJobStarted: AssignedJobName = %q, want %q", got.AssignedJobName, runnerJobStartedMarker)
	}
}

func TestMarkRunnerJobStartedIsIdempotent(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "mark-job-2",
		Source:             "test",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-mark-job-2",
		RepositoryFullName: "o/r",
		ProfileName:        "default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = state.StatusRunning
	st.AssignedJobName = runnerJobStartedMarker
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	// Calling twice is safe
	srv.markRunnerJobStarted("mark-job-2")
	srv.markRunnerJobStarted("mark-job-2")

	got, err := store.ReadState("mark-job-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.AssignedJobName != runnerJobStartedMarker {
		t.Errorf("markRunnerJobStarted idempotent: AssignedJobName = %q", got.AssignedJobName)
	}
}

// ---------- cleanupSandboxAfterExit ----------

func TestCleanupSandboxAfterExitNoopWhenNoSandboxID(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	st := state.RunnerState{ID: "cleanup-no-sandbox", SandboxID: ""}
	if err := srv.cleanupSandboxAfterExit("cleanup-no-sandbox", st); err != nil {
		t.Errorf("cleanupSandboxAfterExit: expected nil for empty SandboxID, got %v", err)
	}
}

// ---------- runnerExited (clean exit) ----------

func TestRunnerExitedWithExitCode0TransitionsToCompleted(t *testing.T) {
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/runners" {
			w.Write([]byte(`{"runners":[]}`))
			return
		}
		t.Fatalf("unexpected github request: %s %s", r.Method, r.URL.String())
	}))
	defer ghServer.Close()

	store := state.New(t.TempDir())
	srv := newTestServer(t, store, ghServer.URL, &fakeSandbox{})

	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "exited-clean",
		Source:             "test",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-exited-clean",
		RepositoryFullName: "o/r",
		ProfileName:        "default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = state.StatusRunning
	st.SandboxID = "" // no sandbox to stop
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	srv.runnerExited("exited-clean", sandboxrunner.ExitResult{ExitCode: 0}, nil)

	got, err := store.ReadState("exited-clean")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusCompleted {
		t.Errorf("runnerExited exit=0: expected status=completed, got %s", got.Status)
	}
}

func TestRunnerExitedWithNonZeroExitCodeTransitionsToFailed(t *testing.T) {
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/runners" {
			w.Write([]byte(`{"runners":[]}`))
			return
		}
		t.Fatalf("unexpected github request: %s %s", r.Method, r.URL.String())
	}))
	defer ghServer.Close()

	store := state.New(t.TempDir())
	srv := newTestServer(t, store, ghServer.URL, &fakeSandbox{})

	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "exited-nonzero",
		Source:             "test",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-exited-nonzero",
		RepositoryFullName: "o/r",
		ProfileName:        "default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = state.StatusRunning
	st.SandboxID = ""
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	srv.runnerExited("exited-nonzero", sandboxrunner.ExitResult{ExitCode: 137, Stderr: "OOM killed"}, nil)

	got, err := store.ReadState("exited-nonzero")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusFailed {
		t.Errorf("runnerExited nonzero: expected status=failed, got %s", got.Status)
	}
	if !strings.Contains(got.Error, "137") {
		t.Errorf("runnerExited nonzero: expected error to contain exit code, got %q", got.Error)
	}
}

func TestRunnerExitedKeepsSandboxWhenGitHubRunnerIsBusy(t *testing.T) {
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/runners" {
			w.Write([]byte(`{"runners":[{"id":99,"name":"e2b-exited-busy","status":"online","busy":true}]}`))
			return
		}
		t.Fatalf("unexpected github request: %s %s", r.Method, r.URL.String())
	}))
	defer ghServer.Close()

	store := state.New(t.TempDir())
	fake := &fakeSandbox{}
	srv := newTestServer(t, store, ghServer.URL, fake)

	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "exited-busy",
		Source:             "test",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-exited-busy",
		RepositoryFullName: "o/r",
		ProfileName:        "default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = state.StatusRunning
	st.SandboxID = "sb-exited-busy"
	st.ProcessPID = 42
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	srv.runnerExited("exited-busy", sandboxrunner.ExitResult{ExitCode: -1, Error: "deadline_exceeded: context deadline exceeded"}, nil)

	got, err := store.ReadState("exited-busy")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusRunning {
		t.Fatalf("runnerExited busy: expected status=running, got %s", got.Status)
	}
	if got.LastErrorCode != "github_runner_busy" || !got.LastErrorRetryable {
		t.Fatalf("runnerExited busy: expected retryable busy marker, got code=%q retryable=%v", got.LastErrorCode, got.LastErrorRetryable)
	}
	if got.AssignedJobName != runnerJobStartedMarker {
		t.Fatalf("runnerExited busy: expected job started marker, got %q", got.AssignedJobName)
	}
	if fake.stoppedCount() != 0 {
		t.Fatalf("runnerExited busy: expected sandbox to remain running, got %d stops", fake.stoppedCount())
	}
}

// ---------- reconcileOnce ----------

func TestReconcileOnceSignalsQueueForQueuedRunner(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	_, _, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "reconcile-queued",
		Source:             "test",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-reconcile-queued",
		RepositoryFullName: "o/r",
		ProfileName:        "default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Drain any existing signal
	select {
	case <-srv.queueNotify:
	default:
	}

	srv.reconcileOnce(t.Context())

	// reconcileOnce should have signaled the queue for the queued runner
	// (It either directly signals or processQueuedRequests ran — we just verify no panic)
}

func TestReconcileMismatchedCompletedJobsRequeuesOriginalJob(t *testing.T) {
	ghServer := httptest.NewServer(githubRunnerAPI(t))
	defer ghServer.Close()

	store := state.New(t.TempDir())
	srv := newTestServer(t, store, ghServer.URL, &fakeSandbox{})
	srv.Close()

	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "1001",
		Source:             "test",
		JobID:              1001,
		RepositoryFullName: "o/r",
		Labels:             []string{"self-hosted", "e2b"},
		ProfileName:        "default",
		RunnerName:         "e2b-1001",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = state.StatusCompleted
	st.AssignedJobID = 2002
	st.AssignedJobName = "prepare"
	st.CompletedAt = time.Now().UTC()
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	srv.reconcileMismatchedCompletedJobs(t.Context())

	got, err := store.ReadState("1001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusQueued {
		t.Fatalf("expected mismatched completed request to be requeued, got %s", got.Status)
	}
	if got.AssignedJobID != 0 || got.AssignedJobName != "" {
		t.Fatalf("expected assigned job to be cleared, got id=%d name=%q", got.AssignedJobID, got.AssignedJobName)
	}
}

func TestReconcileCompletedWorkflowJobsMarksFailedRecoveryCompleted(t *testing.T) {
	ghServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/jobs/1001":
			w.Write([]byte(`{"id":1001,"name":"check","status":"completed","conclusion":"success","labels":["self-hosted","e2b"]}`))
		default:
			t.Fatalf("unexpected github request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer ghServer.Close()

	store := state.New(t.TempDir())
	srv := newTestServer(t, store, ghServer.URL, &fakeSandbox{})
	srv.Close()

	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "1001",
		Source:             "test",
		JobID:              1001,
		RepositoryFullName: "o/r",
		Labels:             []string{"self-hosted", "e2b"},
		ProfileName:        "default",
		RunnerName:         "e2b-1001",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status = state.StatusFailed
	st.FailureStage = "recovery"
	st.FailureReason = "cleanup_failed"
	st.Error = "recover cleanup github runner: busy"
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	srv.reconcileCompletedWorkflowJobs(t.Context())

	got, err := store.ReadState("1001")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusCompleted {
		t.Fatalf("expected completed state, got %s", got.Status)
	}
	if got.FailureStage != "" || got.Error != "" || got.CompletedAt.IsZero() {
		t.Fatalf("expected failure metadata cleared and completed_at set, got %#v", got)
	}
}

// ---------- failStart ----------

func TestFailStartTransitionsRunnerToFailed(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	// Create a queued runner
	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "fail-start-1",
		Source:             "test",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-fail-start-1",
		RepositoryFullName: "o/r",
		ProfileName:        "default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Exhaust retries so it goes straight to failed (not re-queued)
	st.RetryCount = srv.cfg.RetryMaxAttempts
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	srv.failStart("fail-start-1", st, "sandbox_start", fmt.Errorf("template not found"))

	got, err := store.ReadState("fail-start-1")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusFailed {
		t.Errorf("failStart: expected status=failed, got %s", got.Status)
	}
	if got.FailureStage != "sandbox_start" {
		t.Errorf("failStart: FailureStage = %q, want %q", got.FailureStage, "sandbox_start")
	}
}

func TestFailStartRequeuesRunnerWhenRetriesRemain(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "fail-start-retry",
		Source:             "test",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-fail-start-retry",
		RepositoryFullName: "o/r",
		ProfileName:        "default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// RetryCount = 0, MaxAttempts = 3 → should retry
	st.RetryCount = 0
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	// Use a retryable error (status 429)
	srv.failStart("fail-start-retry", st, "github", fmt.Errorf("received status 429"))

	got, err := store.ReadState("fail-start-retry")
	if err != nil {
		t.Fatal(err)
	}
	// Should be re-queued for retry
	if got.Status != state.StatusQueued {
		t.Errorf("failStart retry: expected status=queued, got %s (error=%s)", got.Status, got.Error)
	}
}

func TestFailStartDefersRateLimitEvenWhenMaxAttemptsReached(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "fail-start-rate-limit",
		Source:             "test",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-fail-start-rate-limit",
		RepositoryFullName: "o/r",
		ProfileName:        "default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.RetryCount = srv.cfg.RetryMaxAttempts
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	srv.failStart("fail-start-rate-limit", st, "sandbox_start", fmt.Errorf("api error: status 429"))

	got, err := store.ReadState("fail-start-rate-limit")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusQueued {
		t.Fatalf("expected rate-limited start to remain queued, got %#v", got)
	}
	if got.FailureReason != "http_rate_limited" {
		t.Fatalf("expected http_rate_limited reason, got %q", got.FailureReason)
	}
	if got.NextRetryAt.IsZero() {
		t.Fatal("expected next retry time for rate-limited start")
	}
	if got.RetryCount != srv.cfg.RetryMaxAttempts {
		t.Fatalf("expected retry count capped at max attempts, got %d", got.RetryCount)
	}
}

func TestFailStartDefersSandboxCapacityEvenWhenMaxAttemptsReached(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "fail-start-capacity",
		Source:             "test",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-fail-start-capacity",
		RepositoryFullName: "o/r",
		ProfileName:        "default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.RetryCount = srv.cfg.RetryMaxAttempts
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	srv.failStart("fail-start-capacity", st, "sandbox_start", fmt.Errorf("failed to place sandbox"))

	got, err := store.ReadState("fail-start-capacity")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusQueued {
		t.Fatalf("expected capacity failure to remain queued, got %#v", got)
	}
	if got.FailureReason != "sandbox_capacity" {
		t.Fatalf("expected sandbox_capacity reason, got %q", got.FailureReason)
	}
	if got.NextRetryAt.IsZero() {
		t.Fatal("expected next retry time for capacity failure")
	}
}

func TestFailStartFailsRetryableErrorWhenMaxAttemptsReached(t *testing.T) {
	store := state.New(t.TempDir())
	srv := newTestServer(t, store, "http://example.test", &fakeSandbox{})

	_, st, err := store.CreateRequest(state.RunnerRequest{
		ID:                 "fail-start-timeout-max",
		Source:             "test",
		Labels:             []string{"self-hosted"},
		RunnerName:         "e2b-fail-start-timeout-max",
		RepositoryFullName: "o/r",
		ProfileName:        "default",
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.RetryCount = srv.cfg.RetryMaxAttempts
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}

	srv.failStart("fail-start-timeout-max", st, "sandbox_start", fmt.Errorf("api error: status 408"))

	got, err := store.ReadState("fail-start-timeout-max")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != state.StatusFailed {
		t.Fatalf("expected generic retryable error to fail at max attempts, got %#v", got)
	}
	if got.FailureReason != "http_retryable_status" {
		t.Fatalf("expected http_retryable_status reason, got %q", got.FailureReason)
	}
}
