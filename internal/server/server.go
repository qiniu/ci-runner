package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/metrics"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
	"golang.org/x/sync/singleflight"
)

type Server struct {
	cfg         config.Config
	store       state.Store
	gh          *github.Client
	sandbox     sandboxrunner.Service
	sandboxHTTP *http.Client
	logger      *slog.Logger
	mux         *http.ServeMux
	slots       chan struct{}
	oauth       *http.Client
	diagnostics *http.Client
	cacheSTS    *http.Client
	terminals   *terminalHub
	startedAt   time.Time

	admissionMu sync.Mutex
	locks       [64]sync.Mutex
	queueNotify chan struct{}
	startOnce   sync.Once
	workerID    string
	loopCtx     context.Context
	loopCancel  context.CancelFunc
	loopWG      sync.WaitGroup

	recoveryMetricsDeferred atomic.Int32

	pullTitleMu    sync.Mutex
	pullTitleCache map[string]cachedPullTitle
	pullTitleGroup singleflight.Group

	workflowRunMu    sync.Mutex
	workflowRunCache map[string]cachedWorkflowRun
	workflowRunGroup singleflight.Group

	diagnosticJobMu    sync.Mutex
	diagnosticJobCache map[string]cachedDiagnosticJob
	diagnosticJobGroup singleflight.Group

	userRepositoryAccessMu    sync.Mutex
	userRepositoryAccessCache map[int64]cachedUserRepositoryAccess
	userRepositoryAccessEpoch map[int64]uint64
	userRepositoryAccessGroup singleflight.Group
}

type cachedPullTitle struct {
	title     string
	errorText string
	expiresAt time.Time
}

type cachedWorkflowRun struct {
	run       github.WorkflowRun
	expiresAt time.Time
}

type cachedDiagnosticJob struct {
	job       github.WorkflowJob
	expiresAt time.Time
}

type cachedUserRepositoryAccess struct {
	access       []state.GitHubInstallationRepositoryAccess
	refreshAfter time.Time
	expiresAt    time.Time
	refreshing   bool
}

type manualCreateRequest struct {
	ID                 string   `json:"id"`
	RepositoryFullName string   `json:"repository_full_name"`
	ProfileName        string   `json:"runner_spec_name"`
	Labels             []string `json:"labels"`
}

type createProfileRequest struct {
	Name           string   `json:"name"`
	Labels         []string `json:"labels"`
	RequiredLabels []string `json:"required_labels"`
	TemplateID     string   `json:"template_id"`
	RunnerGroup    string   `json:"runner_group"`
	MaxConcurrency int      `json:"max_concurrency"`
	MinIdle        *int     `json:"min_idle"`
	Priority       *int     `json:"priority"`
	Enabled        *bool    `json:"enabled"`
}

type patchProfileRequest struct {
	Labels         *[]string `json:"labels"`
	RequiredLabels *[]string `json:"required_labels"`
	TemplateID     *string   `json:"template_id"`
	RunnerGroup    *string   `json:"runner_group"`
	MaxConcurrency *int      `json:"max_concurrency"`
	MinIdle        *int      `json:"min_idle"`
	Priority       *int      `json:"priority"`
	Enabled        *bool     `json:"enabled"`
}

const managedRunnerSpecErrorCode = "managed_runner_spec"

type profileMatchRequest struct {
	RepositoryFullName string   `json:"repository_full_name"`
	Labels             []string `json:"labels"`
}

type adminSession struct {
	Provider  string `json:"provider,omitempty"`
	Subject   string `json:"subject"`
	Login     string `json:"login"`
	Role      string `json:"role"`
	AvatarURL string `json:"avatar_url,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
}

const runnerJobStartedMarker = "__runner_job_started__"

const maxWorkflowRunCacheItems = 1024

const (
	defaultRunnerRequestListLimit = 100
	maxRunnerRequestListLimit     = 500
	maxUserRunnerHistoryWindow    = 500
	maxConcurrentRecoveries       = 4
	maxSingleRecoveryTimeout      = 30 * time.Second
	oauthStateCookieName          = "runnerd_oauth_state"
	oauthReturnToCookieName       = "runnerd_oauth_return_to"
	githubAppSetupStateCookieName = "runnerd_github_app_setup_state"
	adminSessionCookieName        = "runnerd_admin_session"
)

var (
	githubOAuthAuthorizeURL = "https://github.com/login/oauth/authorize"
	githubOAuthTokenURL     = "https://github.com/login/oauth/access_token"
)

func New(cfg config.Config, store state.Store, gh *github.Client, sandbox sandboxrunner.Service, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Server{
		cfg:                       cfg,
		store:                     store,
		gh:                        gh,
		sandbox:                   sandbox,
		sandboxHTTP:               newSandboxHTTPClient(),
		logger:                    logger,
		mux:                       http.NewServeMux(),
		slots:                     make(chan struct{}, cfg.MaxConcurrentRunners),
		queueNotify:               make(chan struct{}, 1),
		oauth:                     &http.Client{Timeout: 10 * time.Second},
		diagnostics:               &http.Client{Timeout: 5 * time.Second},
		cacheSTS:                  &http.Client{Timeout: 30 * time.Second},
		terminals:                 newTerminalHub(logger),
		startedAt:                 time.Now().UTC(),
		pullTitleCache:            map[string]cachedPullTitle{},
		workflowRunCache:          map[string]cachedWorkflowRun{},
		diagnosticJobCache:        map[string]cachedDiagnosticJob{},
		userRepositoryAccessCache: map[int64]cachedUserRepositoryAccess{},
		userRepositoryAccessEpoch: map[int64]uint64{},
	}
	hostname, _ := os.Hostname()
	s.workerID = fmt.Sprintf("%s-%d", hostname, os.Getpid())
	s.loopCtx, s.loopCancel = context.WithCancel(context.Background())
	s.routes()
	return s
}

func newSandboxHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = time.Minute
	return &http.Client{Transport: transport}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	startedAt := time.Now()
	lw := &loggingResponseWriter{ResponseWriter: w, status: http.StatusOK}
	if isRetiredCatalogAPIPath(r.URL.Path) {
		r.Pattern = "retired_catalog_api"
		http.NotFound(lw, r)
	} else {
		s.mux.ServeHTTP(lw, r)
	}
	duration := time.Since(startedAt)
	routePattern := r.Pattern
	if routePattern == "" {
		routePattern = "unmatched"
	}
	metrics.RecordHTTPRequest(r.Method, routePattern, lw.status, duration)
	s.logger.Info(
		"http request",
		"method", r.Method,
		"path", r.URL.Path,
		"status", lw.status,
		"bytes", lw.bytes,
		"duration_ms", duration.Milliseconds(),
		"remote_addr", r.RemoteAddr,
		"github_event", r.Header.Get("X-GitHub-Event"),
		"github_delivery", r.Header.Get("X-GitHub-Delivery"),
	)
}

func isRetiredCatalogAPIPath(path string) bool {
	return path == "/runner_groups" || strings.HasPrefix(path, "/runner_groups/") ||
		path == "/runner_policies" || strings.HasPrefix(path, "/runner_policies/") ||
		path == "/diagnostics/catalog-migration-readiness" || strings.HasPrefix(path, "/diagnostics/catalog-migration-readiness/")
}

type loggingResponseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *loggingResponseWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *loggingResponseWriter) Write(data []byte) (int, error) {
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, err
}

func (w *loggingResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (s *Server) Close() {
	if s.terminals != nil {
		s.terminals.Close()
	}
	if s.loopCancel != nil {
		s.logger.Info("stopping background loops")
		s.loopCancel()
	}
	s.loopWG.Wait()
	s.logger.Info("background loops stopped")
}

func (s *Server) Start() {
	s.startBackgroundLoops()
}

func (s *Server) Recover(ctx context.Context) error {
	s.recoveryMetricsDeferred.Add(1)
	defer func() {
		if s.recoveryMetricsDeferred.Add(-1) == 0 {
			s.refreshMetrics()
		}
	}()

	states, err := s.store.ListActiveStates()
	if err != nil {
		return err
	}
	s.logger.Info("recovering runner state", "count", len(states))
	active := make([]state.RunnerState, 0, len(states))
	for _, st := range states {
		if isActiveStatus(st.Status) {
			active = append(active, st)
		}
	}
	if len(active) == 0 {
		return nil
	}

	workerCount := min(maxConcurrentRecoveries, len(active))
	if _, ok := s.recoveryTimeoutPerRunner(ctx, len(active), workerCount); !ok {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("recovery budget exhausted before dispatch: %w", err)
		}
		return errors.New("recovery budget exhausted before dispatch")
	}
	type recoveryJob struct {
		state             state.RunnerState
		remainingRequests int
	}
	jobs := make(chan recoveryJob)
	recoveryErrors := make(chan error, len(active))
	var workers sync.WaitGroup
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				if err := ctx.Err(); err != nil {
					recoveryErrors <- fmt.Errorf("recover runner %s skipped: %w", job.state.ID, err)
					continue
				}
				perRunnerTimeout, ok := s.recoveryTimeoutPerRunner(ctx, job.remainingRequests, workerCount)
				if !ok {
					recoveryErrors <- fmt.Errorf("recover runner %s skipped: recovery budget exhausted", job.state.ID)
					continue
				}
				runnerCtx, cancel := context.WithTimeout(ctx, perRunnerTimeout)
				err := s.recoverRunner(runnerCtx, job.state.ID)
				cancel()
				if err != nil {
					recoveryErrors <- fmt.Errorf("recover runner %s: %w", job.state.ID, err)
				}
			}
		}()
	}
dispatch:
	for i, st := range active {
		select {
		case jobs <- recoveryJob{state: st, remainingRequests: len(active) - i}:
		case <-ctx.Done():
			for _, skipped := range active[i:] {
				recoveryErrors <- fmt.Errorf("recover runner %s skipped: %w", skipped.ID, ctx.Err())
			}
			break dispatch
		}
	}
	close(jobs)
	workers.Wait()
	close(recoveryErrors)

	var joined []error
	for err := range recoveryErrors {
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}

func (s *Server) recoveryTimeoutPerRunner(ctx context.Context, requestCount, workerCount int) (time.Duration, bool) {
	if requestCount <= 0 || workerCount <= 0 {
		return 0, false
	}
	budget := s.cfg.RecoveryTimeout
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline {
		remaining := time.Until(deadline)
		if budget <= 0 || remaining < budget {
			budget = remaining
		}
	}
	if budget <= 0 {
		if !hasDeadline {
			return maxSingleRecoveryTimeout, true
		}
		return 0, false
	}
	waves := (requestCount + workerCount - 1) / workerCount
	timeout := budget / time.Duration(waves)
	if timeout <= 0 {
		return 0, false
	}
	if timeout > maxSingleRecoveryTimeout {
		return maxSingleRecoveryTimeout, true
	}
	return timeout, true
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /", s.handleRoot)
	s.mux.HandleFunc("GET /api/public/runner-templates", s.handlePublicRunnerTemplates)
	s.mux.HandleFunc("GET /admin", s.handleAdminRedirect)
	s.mux.HandleFunc("GET /admin/api/accounts", s.handleAdminListAccounts)
	s.mux.HandleFunc("PATCH /admin/api/accounts/{id}/role", s.handleAdminUpdateAccountRole)
	s.mux.HandleFunc("GET /admin/api/sandbox-service-default", s.handleAdminGetSandboxServiceDefault)
	s.mux.HandleFunc("PUT /admin/api/sandbox-service-default", s.handleAdminSaveSandboxServiceDefault)
	s.mux.HandleFunc("DELETE /admin/api/sandbox-service-default/api-key", s.handleAdminDeleteSandboxServiceDefaultAPIKey)
	s.mux.HandleFunc("POST /admin/api/sandbox-service-default/audiences", s.handleAdminAddSandboxServiceDefaultAudience)
	s.mux.HandleFunc("DELETE /admin/api/sandbox-service-default/audiences/{id}", s.handleAdminDeleteSandboxServiceDefaultAudience)
	s.mux.HandleFunc("GET /admin/", s.handleAdmin)
	s.mux.HandleFunc("GET /user", s.handleUserRedirect)
	s.mux.HandleFunc("GET /user/", s.handleUserRedirect)
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /sandbox/regions", s.handleSandboxRegions)
	s.mux.HandleFunc("GET /auth/session", s.handleAuthSession)
	s.mux.HandleFunc("GET /auth/github/login", s.handleGitHubOAuthLogin)
	s.mux.HandleFunc("GET /auth/github/callback", s.handleGitHubOAuthCallback)
	s.mux.HandleFunc("POST /auth/logout", s.handleAuthLogout)
	s.mux.HandleFunc("GET /github-app/install", s.handleGitHubAppInstallRedirect)
	s.mux.HandleFunc("GET /github-app/setup", s.handleGitHubAppSetupRedirect)
	s.mux.HandleFunc("GET /user/github-app", s.handleUserGitHubApp)
	s.mux.HandleFunc("POST /user/github-app/installations", s.handleUserSaveGitHubInstallation)
	s.mux.HandleFunc("POST /user/github-app/installations/sync", s.handleUserSyncGitHubInstallations)
	s.mux.HandleFunc("GET /user/github-app/installations/{id}/repositories", s.handleUserListGitHubInstallationRepositories)
	s.mux.HandleFunc("DELETE /user/github-app/installations/{id}", s.handleUserDeleteGitHubInstallation)
	s.mux.HandleFunc("GET /user/onboarding/product-tour", s.handleUserProductTourOnboarding)
	s.mux.HandleFunc("PUT /user/onboarding/product-tour", s.handleUserSaveProductTourOnboarding)
	s.mux.HandleFunc("GET /user/runner_requests", s.handleUserListRunners)
	s.mux.HandleFunc("GET /user/runner_requests/{id}", s.handleUserGetRunner)
	s.mux.HandleFunc("GET /user/runner_requests/{id}/group", s.handleUserGetRunnerJobGroup)
	s.mux.HandleFunc("GET /user/github/pulls/{owner}/{repo}/{number}/jobs", s.handleUserGetGitHubPullJobGroup)
	s.mux.HandleFunc("GET /user/github/runs/{owner}/{repo}/{runID}/jobs", s.handleUserGetGitHubRunJobGroup)
	s.mux.HandleFunc("GET /user/github/branches/{owner}/{repo}/{sha}/jobs", s.handleUserGetGitHubBranchJobGroup)
	s.mux.HandleFunc("GET /user/github/branches/{owner}/{repo}/{branch}/{sha}/jobs", s.handleUserGetGitHubBranchJobGroup)
	s.mux.HandleFunc("GET /user/runner_requests/{id}/siblings", s.handleUserListRunnerSiblings)
	s.mux.HandleFunc("GET /user/runner_requests/{id}/logs/{name}", s.handleUserGetRunnerLog)
	s.mux.HandleFunc("GET /user/runner_requests/{id}/github-log", s.handleUserGetRunnerGitHubLog)
	s.mux.HandleFunc("POST /user/runner_requests/{id}/terminal", s.handleUserCreateRunnerTerminal)
	s.mux.HandleFunc("GET /user/runner_requests/{id}/terminal/{sessionID}/events", s.handleUserRunnerTerminalEvents)
	s.mux.HandleFunc("POST /user/runner_requests/{id}/terminal/{sessionID}/input", s.handleUserRunnerTerminalInput)
	s.mux.HandleFunc("POST /user/runner_requests/{id}/terminal/{sessionID}/resize", s.handleUserRunnerTerminalResize)
	s.mux.HandleFunc("DELETE /user/runner_requests/{id}/terminal/{sessionID}", s.handleUserCloseRunnerTerminal)
	s.mux.HandleFunc("GET /user/preferences", s.handleUserPreferences)
	s.mux.HandleFunc("PUT /user/preferences/sandbox", s.handleUserSaveSandboxConfig)
	s.mux.HandleFunc("DELETE /user/preferences/sandbox-api-key", s.handleUserDeleteSandboxAPIKey)
	s.mux.HandleFunc("PUT /user/preferences/cache", s.handleUserSaveCacheConfig)
	s.mux.HandleFunc("DELETE /user/preferences/cache", s.handleUserDeleteCacheConfig)
	s.mux.HandleFunc("GET /user/sandbox/templates", s.handleListSandboxTemplates)
	s.mux.HandleFunc("GET /user/sandbox/instances", s.handleListSandboxes)
	s.mux.HandleFunc("GET /user/runner-specs", s.handleUserListRunnerSpecs)
	s.mux.HandleFunc("POST /user/runner-specs", s.handleUserCreateRunnerSpec)
	s.mux.HandleFunc("PATCH /user/runner-specs/{name}", s.handleUserPatchRunnerSpec)
	s.mux.HandleFunc("DELETE /user/runner-specs/{name}", s.handleUserDeleteRunnerSpec)
	s.mux.HandleFunc("POST /webhooks/github", s.handleGitHubWebhook)
	s.mux.HandleFunc("POST /runner_requests", s.handleCreateRunner)
	s.mux.HandleFunc("GET /runner_requests", s.handleListRunners)
	s.mux.HandleFunc("GET /runner_requests_lookup/{identifier}", s.handleResolveRunnerRequest)
	s.mux.HandleFunc("GET /runner_requests/{id}", s.handleGetRunner)
	s.mux.HandleFunc("GET /runner_requests/{id}/diagnostics", s.handleDiagnosticsRunnerRequest)
	s.mux.HandleFunc("GET /runner_requests/{id}/events", s.handleRunnerRequestEvents)
	s.mux.HandleFunc("POST /runner_requests/{id}/retry", s.handleRetryRunner)
	s.mux.HandleFunc("GET /runner_requests/{id}/logs/{name}", s.handleGetRunnerLog)
	s.mux.HandleFunc("DELETE /runner_requests/{id}", s.handleDeleteRunner)
	s.mux.HandleFunc("GET /audit-events", s.handleListAuditEvents)
	s.mux.HandleFunc("GET /runner_specs", s.handleListProfiles)
	s.mux.HandleFunc("POST /runner_specs", s.handleCreateProfile)
	s.mux.HandleFunc("POST /runner_specs/match", s.handleMatchProfile)
	s.mux.HandleFunc("GET /runner_specs/{name}", s.handleGetProfile)
	s.mux.HandleFunc("PATCH /runner_specs/{name}", s.handlePatchProfile)
	s.mux.HandleFunc("DELETE /runner_specs/{name}", s.handleDeleteProfile)
	s.mux.HandleFunc("GET /diagnostics/pprof", s.handleDiagnosticsPprof)
	s.mux.HandleFunc("GET /diagnostics/vars", s.handleDiagnosticsVars)
	s.mux.HandleFunc("GET /diagnostics/runner-requests/{id}", s.handleLegacyDiagnosticsRunnerRequest)
}

func (s *Server) handleAdminRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
}

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/admin/")
	retiredName := strings.TrimSuffix(name, "/")
	if retiredName == "runner_groups" || retiredName == "runner_policies" {
		http.Redirect(w, r, "/admin/runner_specs", http.StatusTemporaryRedirect)
		return
	}
	s.handleUI(w, r, name)
}

func (s *Server) handleUserRedirect(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, "/", http.StatusMovedPermanently)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/")
	if isUserUIRoute(name) {
		name = "index.html"
	}
	s.handleUI(w, r, name)
}

func isUserUIRoute(name string) bool {
	if name == "accounts" || name == "repositories" || name == "settings" {
		return true
	}
	for _, prefix := range []string{
		"account/",
		"organizations/",
		"jobs/",
		"github/pulls/",
		"github/runs/",
		"github/branches/",
	} {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func (s *Server) handleUI(w http.ResponseWriter, r *http.Request, name string) {
	if name == "" {
		name = "index.html"
	}
	name = path.Clean("/" + name)
	if strings.HasPrefix(name, "/..") {
		http.NotFound(w, r)
		return
	}
	if !s.serveUIAsset(w, r, name) {
		http.NotFound(w, r)
	}
}

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
