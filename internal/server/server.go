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
	deploymentworker "github.com/qiniu/ci-runner/internal/deployment/worker"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/metrics"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
	"golang.org/x/sync/singleflight"
)

type Server struct {
	requestMetrics   runnerRequestMetricsCache
	cfg              config.Config
	store            state.Store
	gh               *github.Client
	sandbox          sandboxrunner.Service
	sandboxHTTP      *http.Client
	logger           *slog.Logger
	mux              *http.ServeMux
	slots            chan struct{}
	oauth            *http.Client
	diagnostics      *http.Client
	cacheSTS         *http.Client
	terminals        *terminalHub
	deploymentWorker *deploymentworker.Worker
	startedAt        time.Time

	// Admissions share the guard; catalog mutations exclude them through persistence.
	admissionMu          sync.RWMutex
	runnerCapacity       runnerAdmissionCapacity
	forkSponsorshipLocks [64]sync.Mutex
	runnerLocksMu        sync.Mutex
	runnerLocks          map[string]*runnerRequestLock
	windowsCreationLocks map[string]*runnerRequestLock
	queueNotify          chan struct{}
	deploymentNotify     chan struct{}
	startOnce            sync.Once
	workerID             string
	loopCtx              context.Context
	loopCancel           context.CancelFunc
	loopWG               sync.WaitGroup

	recoveryMetricsDeferred atomic.Int32
	waitingMetricsDirty     atomic.Bool

	publicTemplatesMu    sync.RWMutex
	publicTemplatesCache cachedPublicTemplates
	publicTemplatesEpoch uint64
	publicTemplatesGroup singleflight.Group

	pullTitleMu    sync.Mutex
	pullTitleCache map[string]cachedPullTitle
	pullTitleGroup singleflight.Group

	workflowRunMu    sync.Mutex
	workflowRunCache map[string]cachedWorkflowRun
	workflowRunGroup singleflight.Group

	diagnosticJobMu    sync.Mutex
	diagnosticJobCache map[string]cachedDiagnosticJob
	diagnosticJobGroup singleflight.Group

	networkDiagnosticMu       sync.Mutex
	networkDiagnosticAttempts map[string]networkDiagnosticAttempt

	userRepositoryAccessMu    sync.Mutex
	userRepositoryAccessCache map[int64]cachedUserRepositoryAccess
	userRepositoryAccessEpoch map[int64]uint64
	userRepositoryAccessGroup singleflight.Group
}

// runnerRequestLock serializes lifecycle mutations for one request. refs keeps
// idle lock entries bounded while allowing waiters to retain the same entry
// until they acquire and release its mutex.
type runnerRequestLock struct {
	mu   sync.Mutex
	refs int
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
	job        github.WorkflowJob
	observedAt time.Time
	expiresAt  time.Time
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
	TemplateSource      string   `json:"template_source"`
	DefaultTemplateName string   `json:"default_template_name"`
	Published           bool     `json:"published"`
	Name                string   `json:"name"`
	Labels              []string `json:"labels"`
	RequiredLabels      []string `json:"required_labels"`
	TemplateID          string   `json:"template_id"`
	RunnerGroup         string   `json:"runner_group"`
	MaxConcurrency      int      `json:"max_concurrency"`
	MinIdle             *int     `json:"min_idle"`
	Priority            *int     `json:"priority"`
	Enabled             *bool    `json:"enabled"`
}

type patchProfileRequest struct {
	TemplateSource      *string   `json:"template_source"`
	DefaultTemplateName *string   `json:"default_template_name"`
	Published           *bool     `json:"published"`
	ExpectedUpdatedAt   string    `json:"expected_updated_at"`
	Labels              *[]string `json:"labels"`
	RequiredLabels      *[]string `json:"required_labels"`
	TemplateID          *string   `json:"template_id"`
	RunnerGroup         *string   `json:"runner_group"`
	MaxConcurrency      *int      `json:"max_concurrency"`
	MinIdle             *int      `json:"min_idle"`
	Priority            *int      `json:"priority"`
	Enabled             *bool     `json:"enabled"`
}

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
		deploymentNotify:          make(chan struct{}, 1),
		oauth:                     &http.Client{Timeout: 10 * time.Second},
		diagnostics:               &http.Client{Timeout: 5 * time.Second},
		cacheSTS:                  &http.Client{Timeout: 30 * time.Second},
		terminals:                 newTerminalHub(logger),
		startedAt:                 time.Now().UTC(),
		pullTitleCache:            map[string]cachedPullTitle{},
		workflowRunCache:          map[string]cachedWorkflowRun{},
		diagnosticJobCache:        map[string]cachedDiagnosticJob{},
		networkDiagnosticAttempts: map[string]networkDiagnosticAttempt{},
		userRepositoryAccessCache: map[int64]cachedUserRepositoryAccess{},
		userRepositoryAccessEpoch: map[int64]uint64{},
	}
	hostname, _ := os.Hostname()
	s.workerID = fmt.Sprintf("%s-%d", hostname, os.Getpid())
	s.loopCtx, s.loopCancel = context.WithCancel(context.Background())
	s.deploymentWorker = s.newDeploymentWorker()
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
	var runnerRecoveryErr error
	if len(active) == 0 {
		if s.deploymentWorker != nil {
			// Deployment recovery and provisioning run after Start on their own
			// context; the startup recovery deadline must not execute queued plans.
			s.signalDeployments()
			return nil
		}
		if s.cfg.DeploymentRetention > 0 {
			_, err := s.store.PruneDeployments(time.Now().UTC().Add(-s.cfg.DeploymentRetention), 100)
			return err
		}
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
	runnerRecoveryErr = errors.Join(joined...)
	if s.deploymentWorker != nil {
		s.signalDeployments()
		return runnerRecoveryErr
	}
	if s.cfg.DeploymentRetention > 0 {
		pruneErr := func() error {
			_, err := s.store.PruneDeployments(time.Now().UTC().Add(-s.cfg.DeploymentRetention), 100)
			return err
		}()
		return errors.Join(runnerRecoveryErr, pruneErr)
	}
	return runnerRecoveryErr
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
	s.mux.HandleFunc("GET /admin/api/github-app/installations", s.handleAdminListGitHubAppInstallations)
	s.mux.HandleFunc("GET /admin/api/github-app/installations/{id}/repositories", s.handleAdminGetGitHubAppInstallationRepositories)
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
	s.mux.HandleFunc("POST /user/deployments", s.handleUserCreateDeployment)
	s.mux.HandleFunc("GET /user/deployment-templates", s.handleUserDeploymentTemplates)
	s.mux.HandleFunc("GET /user/deployment-refs", s.handleUserDeploymentRefs)
	s.mux.HandleFunc("GET /user/deployment-repositories", s.handleUserDeploymentRepositories)
	s.mux.HandleFunc("GET /user/deployments", s.handleUserListDeployments)
	s.mux.HandleFunc("GET /user/deployments/{id}", s.handleUserGetDeployment)
	s.mux.HandleFunc("GET /user/deployments/{id}/pull-request", s.handleUserGetDeploymentPullRequest)
	s.mux.HandleFunc("GET /user/deployments/{id}/resources", s.handleUserGetDeploymentResources)
	s.mux.HandleFunc("GET /user/deployments/{id}/events", s.handleUserListDeploymentEvents)
	s.mux.HandleFunc("GET /user/deployments/{id}/revisions", s.handleUserListDeploymentRevisions)
	s.mux.HandleFunc("GET /user/deployments/{id}/revisions/{number}/events", s.handleUserListDeploymentEvents)
	s.mux.HandleFunc("POST /user/deployments/{id}/stop", s.handleUserStopDeployment)
	// DELETE is a stop alias: Sandbox resources are removed, retained revision
	// and event history is pruned only by the configured retention policy.
	s.mux.HandleFunc("DELETE /user/deployments/{id}", s.handleUserStopDeployment)
	s.mux.HandleFunc("POST /user/deployments/{id}/retry", s.handleUserRetryDeployment)
	s.mux.HandleFunc("POST /user/deployments/{id}/revisions/{number}/terminal", s.handleUserCreateDeploymentTerminal)
	s.mux.HandleFunc("GET /user/deployments/{id}/revisions/{number}/terminal/{sessionID}/events", s.handleUserDeploymentTerminalEvents)
	s.mux.HandleFunc("POST /user/deployments/{id}/revisions/{number}/terminal/{sessionID}/input", s.handleUserDeploymentTerminalInput)
	s.mux.HandleFunc("POST /user/deployments/{id}/revisions/{number}/terminal/{sessionID}/resize", s.handleUserDeploymentTerminalResize)
	s.mux.HandleFunc("DELETE /user/deployments/{id}/revisions/{number}/terminal/{sessionID}", s.handleUserCloseDeploymentTerminal)
	s.mux.HandleFunc("GET /user/runner_requests", s.handleUserListRunners)
	s.mux.HandleFunc("GET /user/runner_requests/{id}", s.handleUserGetRunner)
	s.mux.HandleFunc("GET /user/runner_requests/{id}/group", s.handleUserGetRunnerJobGroup)
	s.mux.HandleFunc("GET /user/github/pulls/{owner}/{repo}/{number}/jobs", s.handleUserGetGitHubPullJobGroup)
	s.mux.HandleFunc("GET /user/github/runs/{owner}/{repo}/{runID}/jobs", s.handleUserGetGitHubRunJobGroup)
	s.mux.HandleFunc("GET /user/github/branches/{owner}/{repo}/{sha}/jobs", s.handleUserGetGitHubBranchJobGroup)
	s.mux.HandleFunc("GET /user/github/branches/{owner}/{repo}/{branch}/{sha}/jobs", s.handleUserGetGitHubBranchJobGroup)
	s.mux.HandleFunc("GET /user/runner_requests/{id}/siblings", s.handleUserListRunnerSiblings)
	s.mux.HandleFunc("GET /user/runner_requests/{id}/logs/{name}", s.handleUserGetRunnerLog)
	s.mux.HandleFunc("GET /user/runner_requests/{id}/events", s.handleUserRunnerRequestEvents)
	// Keep the retired log endpoint out of the SPA fallback.
	s.mux.HandleFunc("GET /user/runner_requests/{id}/github-log", http.NotFound)
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
	s.mux.HandleFunc("GET /user/github-runner-groups", s.handleUserListGitHubRunnerGroups)
	s.mux.HandleFunc("GET /user/runner-specs", s.handleUserListRunnerSpecs)
	s.mux.HandleFunc("POST /user/runner-specs", s.handleUserCreateRunnerSpec)
	s.mux.HandleFunc("PATCH /user/runner-specs/{name}", s.handleUserPatchRunnerSpec)
	s.mux.HandleFunc("DELETE /user/runner-specs/{name}", s.handleUserDeleteRunnerSpec)
	s.mux.HandleFunc("GET /user/fork-sponsorship-policies", s.handleUserListForkSponsorshipPolicies)
	s.mux.HandleFunc("GET /user/fork-sponsorship-repositories", s.handleUserListForkSponsorshipRepositories)
	s.mux.HandleFunc("POST /user/fork-sponsorship-policies", s.handleUserCreateForkSponsorshipPolicy)
	s.mux.HandleFunc("PUT /user/fork-sponsorship-policies/{sourceRepositoryID}", s.handleUserPutForkSponsorshipPolicy)
	s.mux.HandleFunc("DELETE /user/fork-sponsorship-policies/{sourceRepositoryID}", s.handleUserDeleteForkSponsorshipPolicy)
	s.mux.HandleFunc("POST /user/fork-sponsorship-policies/{sourceRepositoryID}/approvals", s.handleUserAddForkSponsorshipApproval)
	s.mux.HandleFunc("DELETE /user/fork-sponsorship-policies/{sourceRepositoryID}/approvals/{forkRepositoryID}", s.handleUserDeleteForkSponsorshipApproval)
	s.mux.HandleFunc("POST /webhooks/github", s.handleGitHubWebhook)
	s.mux.HandleFunc("POST /runner_requests", s.handleCreateRunner)
	s.mux.HandleFunc("GET /runner_request_metrics", s.handleRunnerRequestMetrics)
	s.mux.HandleFunc("GET /runner_requests", s.handleListRunners)
	s.mux.HandleFunc("GET /runner_request_repositories", s.handleListRunnerRequestRepositories)
	s.mux.HandleFunc("GET /runner_requests_lookup/{identifier}", s.handleResolveRunnerRequest)
	s.mux.HandleFunc("GET /runner_requests/{id}", s.handleGetRunner)
	s.mux.HandleFunc("GET /runner_requests/{id}/diagnostics", s.handleDiagnosticsRunnerRequest)
	s.mux.HandleFunc("POST /runner_requests/{id}/network-diagnostics", s.handleRunnerNetworkDiagnostic)
	s.mux.HandleFunc("GET /runner_requests/{id}/events", s.handleRunnerRequestEvents)
	s.mux.HandleFunc("POST /runner_requests/{id}/retry", s.handleRetryRunner)
	s.mux.HandleFunc("GET /runner_requests/{id}/logs/{name}", s.handleGetRunnerLog)
	s.mux.HandleFunc("DELETE /runner_requests/{id}", s.handleDeleteRunner)
	s.mux.HandleFunc("GET /audit-events", s.handleListAuditEvents)
	s.mux.HandleFunc("GET /runner_specs", s.handleListProfiles)
	s.mux.HandleFunc("POST /runner_specs", s.handleCreateProfile)
	s.mux.HandleFunc("POST /runner_specs/match", s.handleMatchProfile)
	s.mux.HandleFunc("GET /runner_specs/templates/status", s.handleProfileTemplateStatus)
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
