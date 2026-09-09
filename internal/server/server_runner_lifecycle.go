package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/metrics"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

const workflowRunCacheTTL = 30 * time.Second

func (s *Server) workflowRunForCache(ctx context.Context, repository string, runID int64) (github.WorkflowRun, error) {
	key := fmt.Sprintf("%s#%d", repository, runID)
	now := time.Now()
	s.workflowRunMu.Lock()
	if s.workflowRunCache == nil {
		s.workflowRunCache = make(map[string]cachedWorkflowRun)
	}
	if cached, ok := s.workflowRunCache[key]; ok && now.Before(cached.expiresAt) {
		s.workflowRunMu.Unlock()
		return cached.run, nil
	}
	s.workflowRunMu.Unlock()
	value, err, _ := s.workflowRunGroup.Do(key, func() (any, error) {
		run, err := s.gh.GetWorkflowRun(ctx, repository, runID)
		if err != nil {
			return github.WorkflowRun{}, err
		}
		s.workflowRunMu.Lock()
		now := time.Now()
		for cachedKey, cached := range s.workflowRunCache {
			if !now.Before(cached.expiresAt) {
				delete(s.workflowRunCache, cachedKey)
			}
		}
		if len(s.workflowRunCache) >= maxWorkflowRunCacheItems {
			var oldestKey string
			var oldestExpiry time.Time
			for cachedKey, cached := range s.workflowRunCache {
				if oldestKey == "" || cached.expiresAt.Before(oldestExpiry) {
					oldestKey, oldestExpiry = cachedKey, cached.expiresAt
				}
			}
			if oldestKey != "" {
				delete(s.workflowRunCache, oldestKey)
			}
		}
		s.workflowRunCache[key] = cachedWorkflowRun{run: run, expiresAt: now.Add(workflowRunCacheTTL)}
		s.workflowRunMu.Unlock()
		return run, nil
	})
	if err != nil {
		return github.WorkflowRun{}, err
	}
	return value.(github.WorkflowRun), nil
}

type cacheScopeDecision struct {
	WriteScope  string
	ReadScopes  []string
	PullRequest int64
	Decision    string
}

func (s *Server) cacheScopesForWorkflow(ctx context.Context, repository string, st state.RunnerState) cacheScopeDecision {
	readOnly := func(defaultScope, reason string) cacheScopeDecision {
		return cacheScopeDecision{ReadScopes: []string{defaultScope}, Decision: reason}
	}
	noCache := func(reason string) cacheScopeDecision {
		return cacheScopeDecision{Decision: reason}
	}
	resolveRepositoryDefaultScope := func(reason string) (string, cacheScopeDecision, bool) {
		if s.gh == nil {
			return "", noCache(reason), false
		}
		repositoryMetadata, err := s.gh.GetRepository(ctx, repository)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("failed to resolve repository default branch for cache scope", "workflow_run_id", st.WorkflowRunID, "repository", repository, "error", err)
			}
			return "", noCache(reason), false
		}
		if !strings.EqualFold(strings.TrimSpace(repositoryMetadata.FullName), repository) || strings.TrimSpace(repositoryMetadata.DefaultBranch) == "" {
			return "", noCache("no_cache_untrusted_repository_metadata"), false
		}
		return scopeForBranch(repositoryMetadata.DefaultBranch), cacheScopeDecision{}, true
	}
	if st.WorkflowRunID <= 0 {
		defaultScope, decision, ok := resolveRepositoryDefaultScope("no_cache_repository_lookup_failed")
		if !ok {
			return decision
		}
		return readOnly(defaultScope, "read_only_missing_workflow_run")
	}
	run, err := s.workflowRunForCache(ctx, repository, st.WorkflowRunID)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("failed to resolve workflow run for cache scope", "workflow_run_id", st.WorkflowRunID, "repository", repository, "error", err)
		}
		defaultScope, decision, ok := resolveRepositoryDefaultScope("no_cache_repository_lookup_failed")
		if !ok {
			return decision
		}
		return readOnly(defaultScope, "read_only_workflow_run_lookup_failed")
	}
	defaultScope := ""
	if branch := strings.TrimSpace(run.Repository.DefaultBranch); branch != "" {
		defaultScope = scopeForBranch(branch)
	} else {
		var decision cacheScopeDecision
		var ok bool
		defaultScope, decision, ok = resolveRepositoryDefaultScope("no_cache_repository_lookup_failed")
		if !ok {
			return decision
		}
	}
	resolvedReadOnly := func(reason string) cacheScopeDecision {
		return readOnly(defaultScope, reason)
	}
	event := strings.ToLower(strings.TrimSpace(run.Event))
	if event == "pull_request" {
		if len(run.PullRequests) == 0 || run.PullRequests[0].Number <= 0 {
			return resolvedReadOnly("read_only_missing_pull_request_metadata")
		}
		pull, pullErr := s.gh.GetPullRequest(ctx, repository, run.PullRequests[0].Number)
		if pullErr != nil {
			s.logger.Warn("failed to resolve pull request for cache scope", "workflow_run_id", st.WorkflowRunID, "repository", repository, "error", pullErr)
			return resolvedReadOnly("read_only_pull_request_lookup_failed")
		}
		baseRepo := strings.TrimSpace(pull.Base.Repository.FullName)
		baseBranch := strings.TrimSpace(pull.Base.Ref)
		headRepo := strings.TrimSpace(pull.Head.Repository.FullName)
		if pull.Number != run.PullRequests[0].Number || baseRepo == "" || baseBranch == "" || headRepo == "" || !strings.EqualFold(baseRepo, repository) {
			return resolvedReadOnly("read_only_untrusted_pull_request_metadata")
		}
		prScope := scopeForPR(pull.Number)
		readScopes := uniqueCacheScopes(prScope, scopeForBranch(baseBranch), defaultScope)
		decision := "internal_pull_request"
		if !strings.EqualFold(headRepo, baseRepo) {
			decision = "fork_pull_request"
		}
		return cacheScopeDecision{WriteScope: prScope, ReadScopes: readScopes, PullRequest: pull.Number, Decision: decision}
	}
	if event == "pull_request_target" || event == "workflow_run" || event == "issue_comment" {
		return resolvedReadOnly("read_only_" + event)
	}
	headRepo := strings.TrimSpace(run.HeadRepository.FullName)
	baseRepo := strings.TrimSpace(run.Repository.FullName)
	branch := strings.TrimSpace(run.HeadBranch)
	if headRepo == "" || baseRepo == "" || !strings.EqualFold(baseRepo, repository) || !strings.EqualFold(headRepo, baseRepo) {
		return resolvedReadOnly("read_only_untrusted_repository_metadata")
	}
	if branch == "" {
		return resolvedReadOnly("read_only_missing_branch")
	}
	if !cacheWriteTrustedEvent(event) {
		return resolvedReadOnly("read_only_untrusted_event")
	}
	ownScope := scopeForBranch(branch)
	return cacheScopeDecision{WriteScope: ownScope, ReadScopes: uniqueCacheScopes(ownScope, defaultScope), Decision: "branch"}
}

func cacheWriteTrustedEvent(event string) bool {
	switch event {
	case "push", "workflow_dispatch", "repository_dispatch", "delete", "registry_package", "page_build", "schedule":
		return true
	default:
		return false
	}
}

func uniqueCacheScopes(scopes ...string) []string {
	result := make([]string, 0, len(scopes))
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		scope = strings.Trim(strings.TrimSpace(scope), "/")
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	return result
}

func cacheScopePrefixesJSON(cacheBasePrefix string, scopes []string) (string, error) {
	prefixes := make([]string, 0, len(scopes))
	for _, scope := range uniqueCacheScopes(scopes...) {
		prefixes = append(prefixes, strings.TrimRight(cacheBasePrefix, "/")+"/"+scope)
	}
	value, err := json.Marshal(prefixes)
	if err != nil {
		return "", err
	}
	return string(value), nil
}

func (s *Server) createAndStart(w http.ResponseWriter, r *http.Request, req state.RunnerRequest, payload []byte) {
	st, created, err := s.enqueueRunnerRequest(req, payload)
	if err != nil {
		s.logger.Error("enqueue runner request failed", "id", req.ID, "repository", req.RepositoryFullName, "profile", req.ProfileName, "error", err)
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if created {
		s.logger.Info("runner request accepted", "id", st.ID, "repository", st.RepositoryFullName, "profile", st.ProfileName, "status", st.Status)
		writeJSON(w, http.StatusAccepted, st)
		return
	}
	s.logger.Info("runner request reused", "id", st.ID, "repository", st.RepositoryFullName, "profile", st.ProfileName, "status", st.Status)
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) enqueueWorkflowJob(repositoryFullName string, githubInstallationID int64, workflowRunName string, job github.WorkflowJob, payload []byte) (state.RunnerState, bool, error) {
	id := strconv.FormatInt(job.ID, 10)
	req := state.RunnerRequest{
		ID:                   id,
		Source:               "github_webhook",
		JobID:                job.ID,
		PullRequestNumber:    workflowJobPullRequestNumber(job.PullRequests),
		GitHubInstallationID: githubInstallationID,
		RepositoryFullName:   repositoryFullName,
		RequestedLabels:      append([]string(nil), job.Labels...),
		Labels:               []string(job.Labels),
		RunnerName:           "e2b-" + id,
	}
	if !s.cfg.RepositoryAllowed(repositoryFullName) {
		s.logger.Info("runner repository rejected by allowlist", "id", req.ID, "repository", repositoryFullName, "labels", []string(job.Labels))
		st, err := s.rejectAdmission(req, payload, "repository_not_allowed")
		return st, false, err
	}
	// Keep profile matching and request creation in one critical section with
	// scoped spec updates/deletes. Moving the reads before this lock allows a
	// mutation to commit between the match and persisted request snapshot.
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	match, err := s.matchProfileForAdmission(repositoryFullName, githubInstallationID, job.Labels)
	if err != nil {
		return state.RunnerState{}, false, err
	}
	req.ProfileSource = match.Source
	req.ProfileScopeType = match.ScopeType
	req.ProfileScopeID = match.ScopeID
	if match.Profile == nil {
		s.logger.Info("runner admission rejected", "id", req.ID, "repository", repositoryFullName, "labels", []string(job.Labels), "reason", match.Reason)
		st, err := s.rejectAdmission(req, payload, match.Reason)
		return st, false, err
	}
	req.ProfileName = match.Profile.Name
	req.RunnerGroup = match.Profile.RunnerGroup
	req.Labels = append([]string(nil), match.Profile.Labels...)
	s.logger.Info("workflow run job matched profile", "job_id", job.ID, "repository", repositoryFullName, "profile", match.Profile.Name, "runner_group", match.Profile.RunnerGroup, "labels", req.Labels)
	metrics.RecordWorkflowQueued(repositoryFullName, workflowRunName, job.Name, match.Profile.Name)
	return s.enqueueRunnerRequestLocked(req, payload)
}

func (s *Server) matchProfileForAdmission(repository string, installationID int64, labels []string) (state.ProfileMatch, error) {
	scope, ok, err := s.runnerProfileScopeForInstallation(installationID)
	if err != nil {
		return state.ProfileMatch{}, err
	}
	if ok {
		return s.store.MatchProfileForScope(scope, repository, labels)
	}
	return s.store.MatchProfile(repository, labels)
}

func (s *Server) enqueueRunnerRequest(req state.RunnerRequest, payload []byte) (state.RunnerState, bool, error) {
	s.admissionMu.Lock()
	defer s.admissionMu.Unlock()
	return s.enqueueRunnerRequestLocked(req, payload)
}

func (s *Server) enqueueRunnerRequestLocked(req state.RunnerRequest, payload []byte) (state.RunnerState, bool, error) {
	if st, err := s.store.ReadState(req.ID); err == nil {
		s.logger.Info("runner request already exists", "id", req.ID, "status", st.Status)
		return st, false, nil
	}
	created, st, err := s.store.CreateRequest(req, payload)
	if err != nil {
		return state.RunnerState{}, false, err
	}
	if created {
		s.logger.Info("runner request created", "id", req.ID, "source", req.Source, "labels", req.Labels)
		metrics.RecordRunnerRequest(req.RepositoryFullName, req.ProfileName, req.Source, "created")
		s.store.AppendLog(req.ID, "control.log", []byte("runner request created\n"))
		s.refreshMetrics()
		s.signalQueue()
	}
	return st, created, nil
}

func (s *Server) rejectAdmission(req state.RunnerRequest, payload []byte, reason string) (state.RunnerState, error) {
	// CreateRejectedRequest relies on database uniqueness for duplicate deliveries
	// and inserts the terminal state without exposing a queued row.
	created, st, err := s.store.CreateRejectedRequest(req, payload, reason)
	if err != nil {
		return state.RunnerState{}, err
	}
	if !created {
		s.logger.Info("runner admission rejection already exists", "id", req.ID, "status", st.Status, "reason", reason)
		return st, nil
	}
	metrics.RecordRunnerRequest(req.RepositoryFullName, req.ProfileName, req.Source, "rejected")
	s.store.AppendLog(req.ID, "control.log", []byte("runner admission rejected: "+reason+"\n"))
	s.logger.Info("runner admission rejected and recorded", "id", req.ID, "repository", req.RepositoryFullName, "reason", reason)
	metrics.RecordWorkflowFailure(req.RepositoryFullName, "unknown", req.RunnerName, req.ProfileName, "admission", reason)
	s.refreshMetrics()
	return st, nil
}

func (s *Server) startRunner(ctx context.Context, id, workerID string) {
	unlock := s.lockRunner(id)
	req, err := s.store.ReadRequest(id)
	if err != nil {
		unlock()
		s.logger.Error("read runner request", "id", id, "error", err)
		return
	}
	st, err := s.store.ReadState(id)
	if err != nil {
		unlock()
		s.logger.Error("read runner state", "id", id, "error", err)
		return
	}
	if st.Status == state.StatusCompleted || st.Status == state.StatusStopping {
		unlock()
		_ = s.store.ReleaseLease(id, workerID)
		s.logger.Info("runner start skipped because request is stopped", "id", id, "status", st.Status)
		s.store.AppendLog(id, "control.log", []byte("runner start skipped because request is stopped\n"))
		return
	}
	s.admissionMu.Lock()
	profile, err := s.profileForRunnerRequest(req)
	if err != nil {
		s.admissionMu.Unlock()
		unlock()
		s.failStart(id, st, "profile_lookup", fmt.Errorf("load profile %q: %w", req.ProfileName, err))
		return
	}
	if err := validateRequestedProfile(profile, req.RequestedLabels); err != nil {
		s.admissionMu.Unlock()
		unlock()
		s.failStart(id, st, "profile_validation", err)
		return
	}
	inFlight, err := s.store.InFlightCount()
	if err != nil {
		s.admissionMu.Unlock()
		unlock()
		_ = s.store.ReleaseLease(id, workerID)
		s.logger.Error("check global concurrency", "id", id, "error", err)
		return
	}
	if inFlight >= s.cfg.MaxConcurrentRunners {
		s.admissionMu.Unlock()
		unlock()
		s.logger.Info("runner start deferred because global concurrency is at capacity", "id", id, "in_flight", inFlight, "limit", s.cfg.MaxConcurrentRunners)
		s.deferQueuedRequest(id, workerID, time.Now().UTC().Add(5*time.Second))
		s.signalQueue()
		return
	}
	if req.ProfileName != "" {
		rejected, err := s.profileAtCapacityFor(req, profile)
		if err != nil {
			s.admissionMu.Unlock()
			unlock()
			_ = s.store.ReleaseLease(id, workerID)
			s.logger.Error("check profile concurrency", "id", id, "profile", req.ProfileName, "error", err)
			return
		}
		if rejected {
			s.admissionMu.Unlock()
			unlock()
			s.logger.Info("runner start deferred because profile is at capacity", "id", id, "profile", req.ProfileName)
			s.deferQueuedRequest(id, workerID, time.Now().UTC().Add(5*time.Second))
			s.signalQueue()
			return
		}
	}
	st.Status = state.StatusCreating
	st.LeaseOwner = workerID
	st.LeaseExpiresAt = time.Now().UTC().Add(s.cfg.WorkerLeaseTTL)
	if err := s.store.WriteState(st); err != nil {
		s.admissionMu.Unlock()
		unlock()
		_ = s.store.ReleaseLease(id, workerID)
		s.logger.Error("write creating state", "id", id, "error", err)
		return
	}
	s.admissionMu.Unlock()
	unlock()

	shouldStart, err := s.ensureWorkflowJobStillQueued(ctx, req)
	if err != nil {
		s.failStart(id, st, "github_job_status", err)
		return
	}
	if !shouldStart {
		_ = s.store.ReleaseLease(id, workerID)
		return
	}

	sandboxService, sandboxConfig, err := s.sandboxServiceAndConfigForRunnerRequestContext(ctx, req)
	if err != nil {
		s.failStart(id, st, "sandbox_config", err)
		return
	}
	if sandboxConfig.APIURL != "" || sandboxConfig.EncryptedAPIKey != "" {
		current, err := s.store.ReadState(id)
		if err != nil {
			s.failStart(id, st, "sandbox_config", fmt.Errorf("read state for sandbox config snapshot: %w", err))
			return
		}
		current.SandboxAPIURL = sandboxConfig.APIURL
		current.SandboxAPIKeyEncrypted = sandboxConfig.EncryptedAPIKey
		current.SandboxConfigSource = sandboxConfig.Source
		if err := s.store.WriteState(current); err != nil {
			s.failStart(id, current, "sandbox_config", fmt.Errorf("write sandbox config snapshot: %w", err))
			return
		}
	}
	templateID := profile.TemplateID
	if strings.TrimSpace(profile.ManagedBy) != "" {
		catalog, ok := sandboxService.(sandboxrunner.DefaultTemplateCatalog)
		if !ok {
			s.failStart(id, st, "template_resolution", newDefaultTemplateResolutionError(
				strings.TrimSpace(profile.DefaultTemplateName),
				defaultTemplateResolutionReasonNoCatalog,
			))
			return
		}
		templates, err := catalog.ListDefaultTemplates(ctx)
		if err != nil {
			s.failStart(id, st, "template_resolution", fmt.Errorf(
				"list default templates for %q: %w",
				strings.TrimSpace(profile.DefaultTemplateName),
				err,
			))
			return
		}
		templateID, err = resolveDefaultTemplateID(profile.DefaultTemplateName, templates)
		if err != nil {
			s.failStart(id, st, "template_resolution", err)
			return
		}
	}

	s.logger.Info("creating github registration token", "id", id)
	s.store.AppendLog(id, "control.log", []byte("creating github registration token\n"))
	createStartedAt := time.Now()
	token, err := s.gh.CreateRegistrationToken(ctx, req.RepositoryFullName, req.RunnerGroup)
	if err != nil {
		s.failStart(id, st, "github_registration", err)
		return
	}
	s.logger.Info("starting sandbox runner", "id", id, "runner_name", req.RunnerName)
	s.store.AppendLog(id, "control.log", []byte("starting sandbox runner\n"))
	exitCh := make(chan struct{})
	startStage := "sandbox_start"
	result, err := func() (sandboxrunner.StartResult, error) {
		createCtx, cancel := context.WithTimeout(ctx, s.cfg.SandboxCreateTimeout)
		defer cancel()
		repositoryURL, err := s.gh.RunnerURL(req.RepositoryFullName, req.RunnerGroup)
		if err != nil {
			startStage = "github_runner_url"
			return sandboxrunner.StartResult{}, err
		}

		// Generate cache STS credentials for this repository if cache is configured.
		input := sandboxrunner.StartInput{
			RequestID:         req.ID,
			RunnerName:        req.RunnerName,
			RepositoryURL:     repositoryURL,
			RegistrationToken: token.Token,
			Labels:            req.Labels,
			RunnerGroup:       strings.TrimSpace(req.RunnerGroup),
			TemplateID:        templateID,
			RequireDocker:     strings.TrimSpace(profile.ManagedBy) != "",
			Timeout:           s.cfg.SandboxTimeout,
			CommandContext:    ctx,
			OnStdout:          func(data []byte) { s.appendRunnerStdout(id, data) },
			OnStderr:          func(data []byte) { s.store.AppendLog(id, "stderr.log", data) },
			OnExit: func(result sandboxrunner.ExitResult, err error) {
				defer close(exitCh)
				s.runnerExited(id, result, err)
			},
		}

		if req.GitHubInstallationID > 0 {
			storage, storageErr := s.cacheStorageForInstallation(req.GitHubInstallationID, sandboxConfig.APIURL)
			if storageErr != nil {
				if !errors.Is(storageErr, state.ErrCacheServiceNotConfigured) {
					s.logger.Warn("failed to resolve cache storage", "id", id, "error", storageErr)
				} else {
					s.logger.Debug("cache storage is not configured", "id", id)
				}
			} else {
				cacheBasePrefix := storage.prefix + "/" + req.RepositoryFullName
				scopeDecision := s.cacheScopesForWorkflow(createCtx, req.RepositoryFullName, st)
				s.logger.Debug("cache STS scope decision", "id", id, "decision", scopeDecision.Decision, "pull_request", scopeDecision.PullRequest, "write_scope", scopeDecision.WriteScope, "read_scopes", scopeDecision.ReadScopes)

				stsCreds, stsErr := generateCacheSTS(createCtx, cacheSTSCredentialConfig{
					Bucket: storage.bucket, AccessKeyID: storage.accessKeyID, SecretAccessKey: storage.secretKey,
				}, cacheBasePrefix, scopeDecision.WriteScope, scopeDecision.ReadScopes, s.cfg.CacheSTSEndpoint, cacheSTSDurationSeconds(s.cfg.SandboxTimeout), s.cacheSTS)
				if stsErr != nil {
					s.logger.Warn("failed to generate cache STS", "id", id, "error", stsErr)
				} else if readPrefixesJSON, marshalErr := cacheScopePrefixesJSON(cacheBasePrefix, scopeDecision.ReadScopes); marshalErr != nil {
					s.logger.Warn("failed to encode cache read prefixes", "id", id, "error", marshalErr)
				} else {
					input.CacheS3Region = storage.region
					input.CacheS3Bucket = storage.bucket
					input.CacheS3Endpoint = storage.endpoint
					input.CacheS3ReadPrefixes = readPrefixesJSON
					if scopeDecision.WriteScope != "" {
						input.CacheS3WritePrefix = cacheBasePrefix + "/" + scopeDecision.WriteScope
					}
					input.CacheS3AccessKeyID = stsCreds.AccessKeyID
					input.CacheS3SecretKey = stsCreds.SecretAccessKey
					input.CacheS3SessionToken = stsCreds.SessionToken
				}
			}
		}

		return sandboxService.StartRunner(createCtx, input)
	}()
	if err != nil {
		s.failStart(id, st, startStage, err)
		return
	}

	unlock = s.lockRunner(id)
	current, err := s.store.ReadState(id)
	if err != nil {
		unlock()
		s.logger.Error("read state before running update", "id", id, "error", err)
		s.cleanupStartedSandbox(id, result)
		return
	}
	if current.Status != state.StatusCreating {
		unlock()
		s.logger.Info("runner status changed before running update", "id", id, "status", current.Status)
		s.cleanupStartedSandbox(id, result)
		return
	}
	st = current
	st.Status = state.StatusRunning
	st.SandboxID = result.SandboxID
	st.ProcessPID = result.PID
	st.Error = ""
	st.LeaseOwner = ""
	st.LeaseExpiresAt = time.Time{}
	if err := s.store.WriteState(st); err != nil {
		unlock()
		s.logger.Error("write running state", "id", id, "error", err)
		s.store.AppendLog(id, "control.log", []byte("write running state failed: "+err.Error()+"\n"))
		s.cleanupStartedSandbox(id, result)
		return
	}
	unlock()
	s.logger.Info("sandbox runner started", "id", id, "sandbox_id", result.SandboxID, "pid", result.PID)
	s.store.AppendLog(id, "control.log", []byte(fmt.Sprintf("sandbox runner started sandbox_id=%s pid=%d\n", result.SandboxID, result.PID)))
	metrics.RecordCreate(req.ProfileName, time.Since(createStartedAt), "success")
	metrics.RecordRunnerRegistered(req.ProfileName, "success")
	s.refreshMetrics()
	<-exitCh
}

func (s *Server) ensureWorkflowJobStillQueued(ctx context.Context, req state.RunnerRequest) (bool, error) {
	if req.JobID == 0 {
		return true, nil
	}
	s.logger.Info("checking workflow job status before sandbox start", "id", req.ID, "job_id", req.JobID, "repository", req.RepositoryFullName)
	s.store.AppendLog(req.ID, "control.log", []byte("checking workflow job status before sandbox start\n"))
	job, err := s.gh.GetWorkflowJob(ctx, req.RepositoryFullName, req.JobID)
	if err != nil {
		return false, err
	}
	if job.Status == "queued" {
		s.logger.Info("workflow job is still queued", "id", req.ID, "job_id", req.JobID, "repository", req.RepositoryFullName)
		return true, nil
	}
	reason := strings.TrimSpace(job.Status)
	if job.Conclusion != "" {
		reason += "/" + job.Conclusion
	}
	if reason == "" {
		reason = "not_queued"
	}
	s.completeWithoutSandbox(req.ID, job, "workflow job is "+reason)
	return false, nil
}

func (s *Server) completeWithoutSandbox(id string, job github.WorkflowJob, reason string) {
	unlock := s.lockRunner(id)
	defer unlock()

	st, err := s.store.ReadState(id)
	if err != nil {
		s.logger.Error("read runner state for skip", "id", id, "error", err)
		return
	}
	if st.Status == state.StatusCompleted || st.Status == state.StatusStopping || st.Status == state.StatusRunning {
		s.logger.Info("runner skip ignored because state already advanced", "id", id, "status", st.Status, "reason", reason)
		return
	}
	if shouldRecordAssignedJob(st, job) {
		st.AssignedJobID = job.ID
		st.AssignedJobName = job.Name
	}
	st.Status = state.StatusCompleted
	st.CompletedAt = time.Now().UTC()
	st.Error = ""
	st.FailureStage = ""
	st.FailureReason = ""
	st.LeaseOwner = ""
	st.LeaseExpiresAt = time.Time{}
	if err := s.store.WriteState(st); err != nil {
		s.logger.Error("write skipped runner state", "id", id, "error", err)
		return
	}
	s.logger.Info("runner skipped before sandbox start", "id", id, "job_id", job.ID, "job_status", job.Status, "job_conclusion", job.Conclusion, "reason", reason)
	s.store.AppendLog(id, "control.log", []byte("runner skipped before sandbox start: "+reason+"\n"))
	conclusion := workflowConclusion(job)
	jobName := workflowJobName(st, job)
	metrics.RecordWorkflowCompleted(st.RepositoryFullName, workflowNameFor(job.WorkflowName), jobName, st.ProfileName, conclusion)
	if isFailureConclusion(conclusion) {
		metrics.RecordWorkflowFailure(st.RepositoryFullName, workflowNameFor(job.WorkflowName), jobName, st.ProfileName, "workflow_job", conclusion)
	}
	s.refreshMetrics()
}

func (s *Server) markWorkflowJobInProgress(id string, job github.WorkflowJob) (state.RunnerState, bool, error) {
	unlock := s.lockRunner(id)
	defer unlock()

	st, err := s.store.ReadState(id)
	if err != nil {
		return state.RunnerState{}, false, err
	}
	if st.Status == state.StatusCompleted || st.Status == state.StatusStopping {
		return st, false, nil
	}
	if shouldRecordAssignedJob(st, job) {
		mismatched := workflowJobMismatch(st, job)
		recordMetric := st.AssignedJobID == 0 && st.AssignedJobName != job.Name
		st.AssignedJobID = job.ID
		st.AssignedJobName = job.Name
		if err := s.store.WriteState(st); err != nil {
			return state.RunnerState{}, false, err
		}
		if mismatched {
			s.logger.Warn("runner picked up different workflow job", "id", id, "workflow_job_id", st.WorkflowJobID, "assigned_job_id", job.ID, "assigned_job_name", job.Name)
			s.store.AppendLog(id, "control.log", []byte(fmt.Sprintf("runner picked up different workflow job %d (%s); original workflow job is %d\n", job.ID, job.Name, st.WorkflowJobID)))
			return st, false, nil
		}
		return st, recordMetric, nil
	}
	return st, false, nil
}

func (s *Server) appendRunnerStdout(id string, data []byte) {
	s.store.AppendLog(id, "stdout.log", data)
	text := string(data)
	if strings.Contains(text, "Listening for Jobs") {
		s.logger.Info("runner is listening for jobs", "id", id)
	}
	if strings.Contains(text, "Running job:") || strings.Contains(text, "RUNNERD_JOB_STARTED") {
		s.markRunnerJobStarted(id)
	}
	if strings.Contains(text, "RUNNERD_JOB_COMPLETED") {
		s.store.AppendLog(id, "control.log", []byte("runner completed job hook received\n"))
	}
}

func (s *Server) markRunnerJobStarted(id string) {
	unlock := s.lockRunner(id)
	defer unlock()

	st, err := s.store.ReadState(id)
	if err != nil {
		s.logger.Error("read runner state for job started marker", "id", id, "error", err)
		return
	}
	if st.AssignedJobID != 0 || st.AssignedJobName == runnerJobStartedMarker || st.Status == state.StatusCompleted || st.Status == state.StatusStopping {
		return
	}
	st.AssignedJobName = runnerJobStartedMarker
	if err := s.store.WriteState(st); err != nil {
		s.logger.Error("write runner job started marker", "id", id, "error", err)
		return
	}
	s.logger.Info("runner accepted a job", "id", id)
	s.store.AppendLog(id, "control.log", []byte("runner accepted a job\n"))
}

func (s *Server) failStart(id string, st state.RunnerState, stage string, err error) {
	unlock := s.lockRunner(id)
	defer unlock()

	current, readErr := s.store.ReadState(id)
	if readErr == nil {
		if current.Status == state.StatusCompleted || current.Status == state.StatusStopping {
			_ = s.store.ReleaseLease(id, s.workerID)
			s.logger.Info("runner failure ignored because request is stopped", "id", id, "status", current.Status, "error", err)
			return
		}
		st = current
	}
	result := s.applyFailure(&st, stage, err, true)
	s.logger.Error("runner failed", "id", id, "error", err)
	s.store.AppendLog(id, "control.log", []byte(result.logLine+"\n"))
	if writeErr := s.store.WriteState(st); writeErr != nil {
		s.logger.Error("write failed state", "id", id, "error", writeErr)
	}
	metrics.RecordCreate(st.ProfileName, 0, result.metricResult)
	if result.metricResult == "failed" {
		metrics.RecordWorkflowFailure(st.RepositoryFullName, "unknown", workflowJobName(st, github.WorkflowJob{}), st.ProfileName, st.FailureStage, st.FailureReason)
	}
	if st.Status == state.StatusQueued {
		s.signalQueue()
	}
	s.refreshMetrics()
}

func (s *Server) runnerExited(id string, result sandboxrunner.ExitResult, err error) {
	unlock := s.lockRunner(id)

	st, readErr := s.store.ReadState(id)
	if readErr != nil {
		unlock()
		s.logger.Error("read state after runner exit", "id", id, "error", readErr)
		return
	}
	if st.Status != state.StatusCreating && st.Status != state.StatusRunning {
		unlock()
		return
	}
	if shouldCheckGitHubBusyAfterRunnerExit(result, err) {
		unlock()
		if s.keepRunnerRunningWhenGitHubBusy(id, st, result, err) {
			return
		}
		unlock = s.lockRunner(id)
		st, readErr = s.store.ReadState(id)
		if readErr != nil {
			unlock()
			s.logger.Error("read state after runner exit busy check", "id", id, "error", readErr)
			return
		}
		if st.Status != state.StatusCreating && st.Status != state.StatusRunning {
			unlock()
			return
		}
	}
	defer unlock()
	if err != nil {
		st.Status = state.StatusFailed
		st.Error = err.Error()
		st.FailureStage = "runner_exit"
		st.FailureReason = "process_error"
		s.logger.Error("runner process exited with error", "id", id, "error", err)
		s.store.AppendLog(id, "control.log", []byte("runner process exited with error: "+err.Error()+"\n"))
		if cleanupErr := s.cleanupSandboxAfterExit(id, st); cleanupErr != nil {
			st.Error = st.Error + "; cleanup sandbox: " + cleanupErr.Error()
		}
		if cleanupErr := s.cleanupGitHubRunner(context.Background(), st); cleanupErr != nil {
			if s.scheduleCleanupRetry(&st, cleanupErr) {
				s.writeStateOrLog(id, st, "schedule github runner cleanup retry")
				s.refreshMetrics()
				return
			}
			st.Error = appendError(st.Error, "github runner cleanup: "+cleanupErr.Error())
		}
		metrics.RecordWorkflowFailure(st.RepositoryFullName, "unknown", workflowJobName(st, github.WorkflowJob{}), st.ProfileName, st.FailureStage, st.FailureReason)
		s.recordWorkflowRunDuration(st, "", workflowJobName(st, github.WorkflowJob{}), "failure")
		s.writeStateOrLog(id, st, "write failed exit state")
		return
	}
	if result.ExitCode == 0 {
		s.logger.Info("runner process exited", "id", id, "exit_code", result.ExitCode)
		s.store.AppendLog(id, "control.log", []byte("runner process exited cleanly\n"))
	} else {
		st.Status = state.StatusFailed
		st.Error = runnerExitMessage(result)
		st.FailureStage = "runner_exit"
		st.FailureReason = strconv.Itoa(result.ExitCode)
		s.logger.Error("runner process exited non-zero", "id", id, "exit_code", result.ExitCode, "stderr", result.Stderr, "runner_error", result.Error)
		s.store.AppendLog(id, "control.log", []byte(st.Error+"\n"))
	}
	if cleanupErr := s.cleanupSandboxAfterExit(id, st); cleanupErr != nil {
		st.Status = state.StatusFailed
		st.Error = "cleanup sandbox after runner exit: " + cleanupErr.Error()
	} else if st.Status != state.StatusFailed {
		st.Status = state.StatusCompleted
		st.CompletedAt = time.Now().UTC()
	}
	if cleanupErr := s.cleanupGitHubRunner(context.Background(), st); cleanupErr != nil {
		if s.scheduleCleanupRetry(&st, cleanupErr) {
			s.writeStateOrLog(id, st, "schedule github runner cleanup retry")
			s.refreshMetrics()
			return
		}
		st.Status = state.StatusFailed
		st.FailureStage = "cleanup"
		st.FailureReason = "github_runner_cleanup_failed"
		st.Error = appendError(st.Error, "github runner cleanup: "+cleanupErr.Error())
	}
	if st.Status == state.StatusFailed {
		metrics.RecordWorkflowFailure(st.RepositoryFullName, "unknown", workflowJobName(st, github.WorkflowJob{}), st.ProfileName, st.FailureStage, st.FailureReason)
		s.recordWorkflowRunDuration(st, "", workflowJobName(st, github.WorkflowJob{}), "failure")
	}
	s.writeStateOrLog(id, st, "write exited state")
	s.refreshMetrics()
}

func shouldCheckGitHubBusyAfterRunnerExit(result sandboxrunner.ExitResult, err error) bool {
	if err == nil && result.ExitCode == 0 {
		return false
	}
	return true
}

func (s *Server) keepRunnerRunningWhenGitHubBusy(id string, st state.RunnerState, result sandboxrunner.ExitResult, err error) bool {
	busy, busyErr := s.githubRunnerBusy(context.Background(), st)
	if busyErr != nil {
		s.logger.Warn("could not verify github runner busy state after runner exit", "id", id, "runner_name", st.RunnerName, "error", busyErr)
		s.store.AppendLog(id, "control.log", []byte("could not verify github runner busy state after runner exit: "+busyErr.Error()+"\n"))
		return false
	}
	if !busy {
		return false
	}
	unlock := s.lockRunner(id)
	defer unlock()
	latest, readErr := s.store.ReadState(id)
	if readErr != nil {
		s.logger.Error("read state after busy runner exit check", "id", id, "error", readErr)
		return false
	}
	if latest.Status != state.StatusCreating && latest.Status != state.StatusRunning {
		return true
	}
	if latest.RunnerName != st.RunnerName || latest.SandboxID != st.SandboxID {
		s.logger.Info("busy runner exit ignored because runner identity changed", "id", id, "runner_name", st.RunnerName, "sandbox_id", st.SandboxID)
		return true
	}
	message := "runner process stream ended while GitHub still reports the runner is busy"
	if err != nil {
		message += ": " + err.Error()
	} else {
		message += ": " + runnerExitMessage(result)
	}
	latest.Status = state.StatusRunning
	latest.LastErrorCode = "github_runner_busy"
	latest.LastErrorMessage = message
	latest.LastErrorRetryable = true
	latest.Error = message
	if latest.AssignedJobName == "" {
		latest.AssignedJobName = runnerJobStartedMarker
	}
	if writeErr := s.store.WriteState(latest); writeErr != nil {
		s.logger.Error("write running state after busy runner exit", "id", id, "error", writeErr)
		s.store.AppendLog(id, "control.log", []byte("write running state after busy runner exit failed: "+writeErr.Error()+"\n"))
		return false
	}
	s.logger.Warn("runner process stream ended while github runner is busy; keeping sandbox running", "id", id, "runner_name", latest.RunnerName, "sandbox_id", latest.SandboxID, "error", latest.Error)
	s.store.AppendLog(id, "control.log", []byte(message+"; keeping sandbox running\n"))
	s.refreshMetrics()
	return true
}

func (s *Server) cleanupSandboxAfterExit(id string, st state.RunnerState) error {
	if st.SandboxID == "" {
		return nil
	}
	if err := s.stopSandboxWithTimeout(context.Background(), id, st.SandboxID, st.ProcessPID); err != nil {
		if isSandboxGone(err) {
			s.logger.Info("sandbox already gone after runner exit", "id", id, "sandbox_id", st.SandboxID, "error", err)
			s.store.AppendLog(id, "control.log", []byte("sandbox already gone after runner exit: "+err.Error()+"\n"))
			return nil
		}
		s.logger.Error("cleanup sandbox after runner exit", "id", id, "sandbox_id", st.SandboxID, "error", err)
		s.store.AppendLog(id, "control.log", []byte("cleanup sandbox after runner exit failed: "+err.Error()+"\n"))
		return err
	}
	s.logger.Info("sandbox cleaned after runner exit", "id", id, "sandbox_id", st.SandboxID)
	s.store.AppendLog(id, "control.log", []byte("sandbox cleaned after runner exit\n"))
	return nil
}

func (s *Server) recoverRunner(ctx context.Context, id string) error {
	unlock := s.lockRunner(id)
	st, err := s.store.ReadState(id)
	if err != nil {
		unlock()
		return err
	}
	if !isActiveStatus(st.Status) {
		unlock()
		return nil
	}
	s.logger.Info("recovering runner after restart", "id", id, "status", st.Status, "sandbox_id", st.SandboxID)
	s.store.AppendLog(id, "control.log", []byte("recovering runner after service restart\n"))
	switch st.Status {
	case state.StatusQueued:
		st.LeaseOwner = ""
		st.LeaseExpiresAt = time.Time{}
		if err := s.store.WriteState(st); err != nil {
			unlock()
			return fmt.Errorf("clear queued runner lease: %w", err)
		}
		unlock()
		s.store.AppendLog(id, "control.log", []byte("queued runner lease cleared after restart\n"))
		s.signalQueue()
		return nil
	case state.StatusStopping:
		unlock()
		_, _, err := s.stopRunner(ctx, id, github.WorkflowJob{})
		return err
	case state.StatusCreating, state.StatusRunning:
		stateVersion := st.Version
		unlock()
		return s.recoverActiveRunner(ctx, st, stateVersion)
	}
	unlock()
	return nil
}

func (s *Server) recoverActiveRunner(ctx context.Context, st state.RunnerState, stateVersion int64) error {
	job, hasJob, err := s.workflowJobForRecovery(ctx, st)
	if err != nil {
		s.store.AppendLog(st.ID, "control.log", []byte("workflow job status check during recovery failed; continuing sandbox reconnect\n"))
		s.logger.Warn("workflow job status check during recovery failed; continuing sandbox reconnect", "id", st.ID, "error", err)
		job = github.WorkflowJob{}
		hasJob = false
	}
	if hasJob && strings.EqualFold(strings.TrimSpace(job.Status), "completed") {
		latest, _, err := s.stopRunner(ctx, st.ID, job)
		if err != nil {
			return err
		}
		if latest.Status != state.StatusCreating && latest.Status != state.StatusRunning {
			return nil
		}
		st = latest
		stateVersion = latest.Version
	}

	timeout := s.remainingSandboxTimeout(st, time.Now().UTC())
	if timeout <= 0 {
		s.failAndStopRunnerVersion(ctx, st.ID, stateVersion, "sandbox_timeout", "timeout", "runner exceeded sandbox timeout during recovery")
		return nil
	}
	req, err := s.store.ReadRequest(st.ID)
	if err != nil {
		return fmt.Errorf("read runner request for recovery: %w", err)
	}
	sandboxService, err := s.sandboxServiceForRunnerRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("resolve sandbox service for recovery: %w", err)
	}
	// Keep this gate buffered and signal it exactly once after RecoverRunner
	// returns: false on error or a deferred version-guard decision on success.
	// That prevents an attached OnExit watcher from owning rejected state.
	exitWatchAccepted := make(chan bool, 1)
	result, err := sandboxService.RecoverRunner(ctx, sandboxrunner.RecoverInput{
		RequestID:      st.ID,
		SandboxID:      st.SandboxID,
		PID:            st.ProcessPID,
		Timeout:        timeout,
		CommandContext: s.loopCtx,
		OnExit: func(result sandboxrunner.ExitResult, err error) {
			if <-exitWatchAccepted {
				s.runnerExited(st.ID, result, err)
			}
		},
	})
	if err != nil {
		exitWatchAccepted <- false
		if st.Status == state.StatusRunning && errors.Is(err, sandboxrunner.ErrSandboxNotFound) {
			s.failAndStopRunnerVersion(ctx, st.ID, stateVersion, "recovery", "sandbox_not_found", "runner sandbox no longer exists after restart")
			return nil
		}
		if st.Status == state.StatusCreating {
			switch {
			case errors.Is(err, sandboxrunner.ErrSandboxNotFound) && (!hasJob || strings.EqualFold(strings.TrimSpace(job.Status), "queued")):
				return s.requeueInterruptedCreation(st.ID, stateVersion)
			case errors.Is(err, sandboxrunner.ErrSandboxNotFound) && strings.EqualFold(strings.TrimSpace(job.Status), "in_progress"):
				s.failAndStopRunnerVersion(ctx, st.ID, stateVersion, "recovery", "sandbox_not_found", "runner sandbox no longer exists after interrupted creation")
				return nil
			case errors.Is(err, sandboxrunner.ErrRunnerNotFound) && result.SandboxID != "":
				stopErr := s.stopSandboxWithTimeout(ctx, st.ID, result.SandboxID, 0)
				if stopErr != nil && !isSandboxGone(stopErr) {
					s.store.AppendLog(st.ID, "control.log", []byte("cleanup interrupted runner creation failed\n"))
					return fmt.Errorf("stop sandbox without runner before requeue: %w", stopErr)
				}
				s.store.AppendLog(st.ID, "control.log", []byte("stopped sandbox without runner process after restart\n"))
				return s.requeueInterruptedCreation(st.ID, stateVersion)
			}
		}
		// A running request must reconnect to its persisted PID. If that exact
		// process is missing, preserve the sandbox because reconnect/list results
		// can be transient and attaching to a replacement process is unsafe.
		s.store.AppendLog(st.ID, "control.log", []byte("runner reconnect after restart failed; preserving request state\n"))
		return fmt.Errorf("reconnect runner: %w", err)
	}

	acceptExitWatch := false
	defer func() {
		exitWatchAccepted <- acceptExitWatch
	}()
	unlock := s.lockRunner(st.ID)
	defer unlock()
	latest, err := s.store.ReadState(st.ID)
	if err != nil {
		return err
	}
	if latest.Version != stateVersion || (latest.Status != state.StatusCreating && latest.Status != state.StatusRunning) {
		s.logger.Warn(
			"runner reconnect discarded after concurrent state change",
			"id", st.ID,
			"expected_version", stateVersion,
			"current_version", latest.Version,
			"current_status", latest.Status,
		)
		s.store.AppendLog(st.ID, "control.log", []byte("runner reconnect result discarded after concurrent state change\n"))
		return nil
	}
	latest.Status = state.StatusRunning
	latest.SandboxID = result.SandboxID
	latest.ProcessPID = result.PID
	latest.LeaseOwner = ""
	latest.LeaseExpiresAt = time.Time{}
	latest.Error = ""
	latest.FailureStage = ""
	latest.FailureReason = ""
	latest.LastErrorCode = ""
	latest.LastErrorMessage = ""
	latest.LastErrorRetryable = false
	if err := s.store.WriteState(latest); err != nil {
		return fmt.Errorf("write recovered runner state: %w", err)
	}
	acceptExitWatch = true
	s.logger.Info("runner reconnected after restart", "id", st.ID, "sandbox_id", result.SandboxID, "pid", result.PID)
	s.store.AppendLog(st.ID, "control.log", []byte(fmt.Sprintf("runner reconnected after restart sandbox_id=%s pid=%d\n", result.SandboxID, result.PID)))
	s.recordAudit("recovery", "runner.reconnected", "runner_request", latest.ID, map[string]any{
		"sandbox_id": result.SandboxID,
		"pid":        result.PID,
	})
	return nil
}

func (s *Server) workflowJobForRecovery(ctx context.Context, st state.RunnerState) (github.WorkflowJob, bool, error) {
	if st.WorkflowJobID == 0 || strings.TrimSpace(st.RepositoryFullName) == "" {
		return github.WorkflowJob{}, false, nil
	}
	jobCtx, cancel := context.WithTimeout(ctx, workflowJobLookupTimeout)
	defer cancel()
	job, err := s.gh.GetWorkflowJob(jobCtx, st.RepositoryFullName, st.WorkflowJobID)
	if err != nil {
		return github.WorkflowJob{}, false, fmt.Errorf("get workflow job %d: %w", st.WorkflowJobID, err)
	}
	return job, true, nil
}

func (s *Server) remainingSandboxTimeout(st state.RunnerState, now time.Time) time.Duration {
	startedAt := st.RunningAt
	if startedAt.IsZero() {
		startedAt = st.CreatingAt
	}
	if startedAt.IsZero() {
		return s.cfg.SandboxTimeout
	}
	remaining := s.cfg.SandboxTimeout - now.Sub(startedAt)
	return min(remaining, s.cfg.SandboxTimeout)
}

func (s *Server) requeueInterruptedCreation(id string, stateVersion int64) error {
	unlock := s.lockRunner(id)
	defer unlock()
	st, err := s.store.ReadState(id)
	if err != nil {
		return err
	}
	if st.Version != stateVersion || st.Status != state.StatusCreating {
		return nil
	}
	st.Status = state.StatusQueued
	st.SandboxID = ""
	st.ProcessPID = 0
	st.Error = ""
	st.FailureStage = ""
	st.FailureReason = ""
	st.LastErrorCode = ""
	st.LastErrorMessage = ""
	st.LastErrorRetryable = false
	st.LeaseOwner = ""
	st.LeaseExpiresAt = time.Time{}
	st.NextRetryAt = time.Time{}
	st.CreatingAt = time.Time{}
	st.RunningAt = time.Time{}
	st.StoppingAt = time.Time{}
	if err := s.store.WriteState(st); err != nil {
		return fmt.Errorf("requeue interrupted runner creation: %w", err)
	}
	s.logger.Info("interrupted runner creation requeued after restart", "id", id)
	s.store.AppendLog(id, "control.log", []byte("runner creation had no sandbox after restart; request requeued\n"))
	s.signalQueue()
	return nil
}

func (s *Server) stopIfExists(ctx context.Context, id string, job github.WorkflowJob) {
	if _, err := s.store.ReadState(id); err != nil {
		s.logger.Info("stop skipped because runner state does not exist", "id", id)
		return
	}
	if _, _, err := s.stopRunner(ctx, id, job); err != nil {
		s.logger.Error("stop runner", "id", id, "error", err)
	}
}

func (s *Server) stopRunner(ctx context.Context, id string, job github.WorkflowJob) (state.RunnerState, bool, error) {
	unlock := s.lockRunner(id)
	defer unlock()

	st, err := s.store.ReadState(id)
	if err != nil {
		s.logger.Error("read runner state for stop", "id", id, "error", err)
		return state.RunnerState{}, false, err
	}
	s.logger.Info("runner stop requested", "id", id, "status", st.Status, "sandbox_id", st.SandboxID, "pid", st.ProcessPID, "job_id", job.ID)
	if workflowJobStopConflictsWithAssignment(st, job) {
		s.logger.Warn(
			"runner stop deferred because runner ownership is unresolved or different",
			"id", id,
			"workflow_job_id", st.WorkflowJobID,
			"assigned_job_id", st.AssignedJobID,
			"assigned_job_name", st.AssignedJobName,
			"completed_job_id", job.ID,
		)
		s.store.AppendLog(id, "control.log", []byte(fmt.Sprintf(
			"runner stop deferred: workflow job %d completed while runner assignment is id=%d name=%q\n",
			job.ID,
			st.AssignedJobID,
			st.AssignedJobName,
		)))
		return st, false, nil
	}
	if st.Status == state.StatusCompleted {
		recorded := false
		if shouldRecordAssignedJob(st, job) {
			st.AssignedJobID = job.ID
			st.AssignedJobName = job.Name
			if err := s.store.WriteState(st); err != nil {
				return state.RunnerState{}, false, fmt.Errorf("write completed job assignment: %w", err)
			}
			recorded = true
			s.logger.Info("recorded completed runner job assignment", "id", id, "job_id", job.ID, "job_name", job.Name)
		}
		s.logger.Info("runner stop skipped because already completed", "id", id)
		return st, recorded, nil
	}
	if shouldRecordAssignedJob(st, job) {
		st.AssignedJobID = job.ID
		st.AssignedJobName = job.Name
	}
	st.Status = state.StatusStopping
	stopStartedAt := time.Now()
	if err := s.store.WriteState(st); err != nil {
		return state.RunnerState{}, false, fmt.Errorf("write stopping state: %w", err)
	}
	s.logger.Info("runner marked stopping", "id", id, "sandbox_id", st.SandboxID, "pid", st.ProcessPID)
	st.Version++
	if st.SandboxID != "" {
		if err := s.stopSandboxWithTimeout(ctx, id, st.SandboxID, st.ProcessPID); err != nil {
			if isSandboxGone(err) {
				s.logger.Info("sandbox already gone", "id", id, "sandbox_id", st.SandboxID, "error", err)
				s.store.AppendLog(id, "control.log", []byte("sandbox already gone: "+err.Error()+"\n"))
			} else {
				if s.scheduleStopRetry(&st, err) {
					if writeErr := s.store.WriteState(st); writeErr != nil {
						return state.RunnerState{}, false, fmt.Errorf("schedule stop retry: %v; write stopping state: %w", err, writeErr)
					}
					st.Version++
					s.store.AppendLog(id, "control.log", []byte(fmt.Sprintf("stop retry scheduled for %s: %s\n", st.NextRetryAt.Format(time.RFC3339), err)))
					s.logger.Info("runner stop retry scheduled", "id", id, "sandbox_id", st.SandboxID, "next_retry_at", st.NextRetryAt, "error", err)
					s.refreshMetrics()
					return st, false, nil
				}
				st.Status = state.StatusFailed
				st.FailureStage = "stop"
				st.FailureReason = "stop_failed"
				st.Error = err.Error()
				if writeErr := s.store.WriteState(st); writeErr != nil {
					return state.RunnerState{}, false, fmt.Errorf("stop sandbox: %v; write failed state: %w", err, writeErr)
				}
				s.logger.Error("runner stop failed", "id", id, "sandbox_id", st.SandboxID, "error", err)
				metrics.RecordStop(st.ProfileName, time.Since(stopStartedAt), "failed")
				metrics.RecordRunnerCleanup(st.ProfileName, "failed")
				metrics.RecordWorkflowFailure(st.RepositoryFullName, "unknown", workflowJobName(st, job), st.ProfileName, st.FailureStage, st.FailureReason)
				s.refreshMetrics()
				return st, false, err
			}
		}
	}
	if cleanupErr := s.cleanupGitHubRunner(ctx, st); cleanupErr != nil {
		if s.scheduleCleanupRetry(&st, cleanupErr) {
			if writeErr := s.store.WriteState(st); writeErr != nil {
				return state.RunnerState{}, false, fmt.Errorf("schedule github runner cleanup retry: %v; write stopping state: %w", cleanupErr, writeErr)
			}
			st.Version++
			s.store.AppendLog(id, "control.log", []byte(fmt.Sprintf("github runner cleanup retry scheduled for %s: %s\n", st.NextRetryAt.Format(time.RFC3339), cleanupErr)))
			s.logger.Info("github runner cleanup retry scheduled", "id", id, "runner_name", st.RunnerName, "next_retry_at", st.NextRetryAt, "error", cleanupErr)
			s.refreshMetrics()
			return st, false, nil
		}
		st.Status = state.StatusFailed
		st.FailureStage = "cleanup"
		st.FailureReason = "github_runner_cleanup_failed"
		st.Error = cleanupErr.Error()
		if writeErr := s.store.WriteState(st); writeErr != nil {
			return state.RunnerState{}, false, fmt.Errorf("cleanup github runner: %v; write failed state: %w", cleanupErr, writeErr)
		}
		metrics.RecordStop(st.ProfileName, time.Since(stopStartedAt), "failed")
		s.refreshMetrics()
		return st, false, cleanupErr
	}
	if workflowJobMismatch(st, job) {
		queued, queueErr := s.originalWorkflowJobQueued(ctx, st)
		if queueErr != nil {
			s.logger.Warn("original workflow job status check failed; requeueing mismatched request for retry", "id", st.ID, "workflow_job_id", st.WorkflowJobID, "assigned_job_id", job.ID, "error", queueErr)
			queued = true
		}
		requeued, next, err := s.requeueMismatchedWorkflowJob(st, job, queued)
		if err != nil {
			return state.RunnerState{}, false, err
		}
		if requeued {
			s.logger.Warn("requeued original workflow job after runner picked up different job", "id", id, "workflow_job_id", st.WorkflowJobID, "assigned_job_id", job.ID, "repository", st.RepositoryFullName)
			s.refreshMetrics()
			return next, false, nil
		}
		st = next
		st.Status = state.StatusCompleted
		st.CompletedAt = time.Now().UTC()
		st.NextRetryAt = time.Time{}
		st.LastErrorCode = ""
		st.LastErrorMessage = ""
		st.LastErrorRetryable = false
		st.FailureStage = ""
		st.FailureReason = ""
		st.Error = ""
		if err := s.store.WriteState(st); err != nil {
			return state.RunnerState{}, false, err
		}
		s.logger.Info("mismatched workflow job not requeued because original job is no longer queued", "id", id, "workflow_job_id", st.WorkflowJobID, "assigned_job_id", job.ID)
		metrics.RecordStop(st.ProfileName, time.Since(stopStartedAt), "success")
		s.refreshMetrics()
		return st, false, nil
	}
	if st.FailureStage != "" && st.FailureStage != "cleanup" {
		st.Status = state.StatusFailed
		st.FailedAt = time.Now().UTC()
	} else {
		st.Status = state.StatusCompleted
		st.CompletedAt = time.Now().UTC()
		st.FailureStage = ""
		st.FailureReason = ""
		st.Error = ""
	}
	st.NextRetryAt = time.Time{}
	st.LastErrorCode = ""
	st.LastErrorMessage = ""
	st.LastErrorRetryable = false
	if err := s.store.WriteState(st); err != nil {
		return state.RunnerState{}, false, err
	}
	s.logger.Info("runner stopped", "id", id, "sandbox_id", st.SandboxID, "duration_ms", time.Since(stopStartedAt).Milliseconds())
	metrics.RecordStop(st.ProfileName, time.Since(stopStartedAt), "success")
	s.refreshMetrics()
	return st, true, nil
}

func (s *Server) failAndStopRunner(ctx context.Context, id, stage, reason, message string) {
	s.failAndStopRunnerVersion(ctx, id, 0, stage, reason, message)
}

func (s *Server) failAndStopRunnerVersion(ctx context.Context, id string, expectedVersion int64, stage, reason, message string) {
	unlock := s.lockRunner(id)
	defer unlock()

	st, err := s.store.ReadState(id)
	if err != nil {
		s.logger.Error("read runner state for forced stop", "id", id, "error", err)
		return
	}
	if expectedVersion != 0 && st.Version != expectedVersion {
		return
	}
	if st.Status == state.StatusCompleted || st.Status == state.StatusFailed {
		return
	}
	stopStartedAt := time.Now()
	st.Status = state.StatusStopping
	st.FailureStage = stage
	st.FailureReason = reason
	st.Error = message
	if err := s.store.WriteState(st); err != nil {
		s.logger.Error("write forced stopping state", "id", id, "error", err)
		return
	}
	st.Version++
	if st.SandboxID != "" {
		if err := s.stopSandboxWithTimeout(ctx, id, st.SandboxID, st.ProcessPID); err != nil && !isSandboxGone(err) {
			st.Status = state.StatusFailed
			st.FailureStage = "stop"
			st.FailureReason = "stop_failed"
			st.Error = "forced stop failed after " + stage + ": " + err.Error()
			s.writeStateOrLog(id, st, "write forced stop failed state")
			metrics.RecordStop(st.ProfileName, time.Since(stopStartedAt), "failed")
			metrics.RecordRunnerCleanup(st.ProfileName, "failed")
			metrics.RecordWorkflowFailure(st.RepositoryFullName, "unknown", workflowJobName(st, github.WorkflowJob{}), st.ProfileName, st.FailureStage, st.FailureReason)
			s.refreshMetrics()
			return
		}
	}
	if cleanupErr := s.cleanupGitHubRunner(ctx, st); cleanupErr != nil {
		if s.scheduleCleanupRetry(&st, cleanupErr) {
			s.writeStateOrLog(id, st, "schedule github runner cleanup retry")
			s.refreshMetrics()
			return
		}
		st.FailureStage = "cleanup"
		st.FailureReason = "github_runner_cleanup_failed"
		st.Error = appendError(message, "github runner cleanup: "+cleanupErr.Error())
	}
	st.Status = state.StatusFailed
	st.FailedAt = time.Now().UTC()
	if st.FailureStage == "" {
		st.FailureStage = stage
	}
	if st.FailureReason == "" {
		st.FailureReason = reason
	}
	if st.Error == "" {
		st.Error = message
	}
	st.LeaseOwner = ""
	st.LeaseExpiresAt = time.Time{}
	s.writeStateOrLog(id, st, "write forced failed state")
	metrics.RecordStop(st.ProfileName, time.Since(stopStartedAt), "success")
	metrics.RecordWorkflowFailure(st.RepositoryFullName, "unknown", workflowJobName(st, github.WorkflowJob{}), st.ProfileName, stage, reason)
	s.recordWorkflowRunDuration(st, "", workflowJobName(st, github.WorkflowJob{}), "failure")
	s.refreshMetrics()
}

func (s *Server) cleanupGitHubRunner(ctx context.Context, st state.RunnerState) error {
	if strings.TrimSpace(st.RunnerName) == "" {
		return nil
	}
	if strings.TrimSpace(st.RepositoryFullName) == "" {
		s.logger.Info("github runner cleanup skipped because repository is empty", "id", st.ID, "runner_name", st.RunnerName)
		return nil
	}
	cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	removed, err := s.gh.RemoveRunnerByName(cleanupCtx, st.RepositoryFullName, st.RunnerGroup, st.RunnerName)
	if err != nil {
		s.logger.Warn("github runner cleanup failed", "id", st.ID, "runner_name", st.RunnerName, "repository", st.RepositoryFullName, "error", err)
		s.store.AppendLog(st.ID, "control.log", []byte("github runner cleanup failed: "+err.Error()+"\n"))
		metrics.RecordRunnerCleanup(st.ProfileName, "error")
		return err
	}
	if removed {
		s.logger.Info("github runner registration removed", "id", st.ID, "runner_name", st.RunnerName, "repository", st.RepositoryFullName)
		s.store.AppendLog(st.ID, "control.log", []byte("github runner registration removed\n"))
		metrics.RecordRunnerCleanup(st.ProfileName, "removed")
		return nil
	}
	s.logger.Info("github runner registration already absent", "id", st.ID, "runner_name", st.RunnerName, "repository", st.RepositoryFullName)
	metrics.RecordRunnerCleanup(st.ProfileName, "absent")
	return nil
}

func (s *Server) writeStateOrLog(id string, st state.RunnerState, msg string) {
	if err := s.store.WriteState(st); err != nil {
		s.logger.Error(msg, "id", id, "error", err)
		s.store.AppendLog(id, "control.log", []byte(msg+": "+err.Error()+"\n"))
	}
}

func runnerExitMessage(result sandboxrunner.ExitResult) string {
	detail := strings.TrimSpace(result.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(result.Error)
	}
	if detail == "" {
		detail = strings.TrimSpace(result.Stdout)
	}
	if detail == "" {
		return fmt.Sprintf("runner process exited with code %d", result.ExitCode)
	}
	return fmt.Sprintf("runner process exited with code %d: %s", result.ExitCode, detail)
}

type failureResult struct {
	metricResult string
	logLine      string
}

func (s *Server) applyFailure(st *state.RunnerState, stage string, err error, allowRetry bool) failureResult {
	now := time.Now().UTC()
	code, retryable := classifyRetryableError(stage, err)
	st.Error = err.Error()
	st.FailureStage = stage
	st.FailureReason = code
	st.LastErrorCode = code
	st.LastErrorMessage = err.Error()
	st.LastErrorRetryable = retryable
	st.LeaseOwner = ""
	st.LeaseExpiresAt = time.Time{}
	st.SandboxID = ""
	st.ProcessPID = 0
	st.CompletedAt = time.Time{}
	if allowRetry && retryable && isQueueDeferFailure(code) {
		st.Status = state.StatusQueued
		if st.RetryCount < s.cfg.RetryMaxAttempts {
			st.RetryCount++
		}
		st.NextRetryAt = s.nextRetryAt(max(st.RetryCount, 1), now)
		st.CreatingAt = time.Time{}
		st.RunningAt = time.Time{}
		st.StoppingAt = time.Time{}
		st.FailedAt = time.Time{}
		return failureResult{
			metricResult: "retry",
			logLine:      fmt.Sprintf("runner deferred at %s due to %s, next retry scheduled for %s: %s", stage, code, st.NextRetryAt.Format(time.RFC3339), err),
		}
	}
	if allowRetry && retryable && st.RetryCount < s.cfg.RetryMaxAttempts {
		st.Status = state.StatusQueued
		st.RetryCount++
		st.NextRetryAt = s.nextRetryAt(st.RetryCount, now)
		st.CreatingAt = time.Time{}
		st.RunningAt = time.Time{}
		st.StoppingAt = time.Time{}
		st.FailedAt = time.Time{}
		return failureResult{
			metricResult: "retry",
			logLine:      fmt.Sprintf("runner failed at %s, retry %d/%d scheduled for %s: %s", stage, st.RetryCount, s.cfg.RetryMaxAttempts, st.NextRetryAt.Format(time.RFC3339), err),
		}
	}
	st.Status = state.StatusFailed
	st.NextRetryAt = time.Time{}
	return failureResult{
		metricResult: "failed",
		logLine:      fmt.Sprintf("runner failed at %s: %s", stage, err),
	}
}

func (s *Server) nextRetryAt(retryCount int, now time.Time) time.Time {
	delay := s.cfg.RetryBaseDelay
	for i := 1; i < retryCount; i++ {
		delay *= 2
		if delay >= s.cfg.RetryMaxDelay {
			delay = s.cfg.RetryMaxDelay
			break
		}
	}
	if delay > s.cfg.RetryMaxDelay {
		delay = s.cfg.RetryMaxDelay
	}
	return now.Add(delay)
}

func isQueueDeferFailure(code string) bool {
	switch code {
	case "sandbox_capacity", "http_rate_limited", "github_secondary_rate_limit":
		return true
	default:
		return false
	}
}

func classifyRetryableError(stage string, err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var resolutionErr *defaultTemplateResolutionError
	if errors.As(err, &resolutionErr) {
		return resolutionErr.Reason, false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", true
	}
	msg := strings.ToLower(err.Error())
	switch {
	case isGitHubRunnerBusyCleanup(err):
		return "github_runner_busy", true
	case strings.Contains(msg, "failed to place sandbox"):
		return "sandbox_capacity", true
	case strings.Contains(msg, "secondary rate limit"):
		return "github_secondary_rate_limit", true
	case strings.Contains(msg, "status 429"):
		return "http_rate_limited", true
	case strings.Contains(msg, "status 408"), strings.Contains(msg, "status 409"), strings.Contains(msg, "status 425"):
		return "http_retryable_status", true
	case strings.Contains(msg, "status 5"):
		if strings.Contains(stage, "github") {
			return "github_server_error", true
		}
		return "backend_server_error", true
	case strings.Contains(msg, "timeout"), strings.Contains(msg, "timed out"), strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "connection reset"), strings.Contains(msg, "temporary"), strings.Contains(msg, "eof"):
		return "temporary_network_error", true
	case strings.Contains(msg, "status 401"), strings.Contains(msg, "status 403"):
		return "auth_error", false
	default:
		return stage, false
	}
}

func shouldRecordAssignedJob(st state.RunnerState, job github.WorkflowJob) bool {
	if job.ID == 0 {
		return false
	}
	return st.AssignedJobID != job.ID || st.AssignedJobName != job.Name
}

// workflowJobStopConflictsWithAssignment reports whether a completed workflow
// job must not stop or reassign this runner because its actual job ownership is
// unresolved or already points at a different job. A matching non-empty
// runner_name is authoritative and makes the completion safe to apply.
func workflowJobStopConflictsWithAssignment(st state.RunnerState, job github.WorkflowJob) bool {
	if job.ID == 0 {
		return false
	}
	if runnerName := strings.TrimSpace(job.RunnerName); runnerName != "" && runnerName == strings.TrimSpace(st.RunnerName) {
		return false
	}
	if st.AssignedJobID != 0 {
		return st.AssignedJobID != job.ID
	}
	return st.AssignedJobName == runnerJobStartedMarker && st.WorkflowJobID == job.ID
}

func workflowJobMismatch(st state.RunnerState, job github.WorkflowJob) bool {
	return st.WorkflowJobID != 0 && job.ID != 0 && st.WorkflowJobID != job.ID
}

func (s *Server) signalQueue() {
	select {
	case s.queueNotify <- struct{}{}:
	default:
	}
}

func (s *Server) retryOrFail(st state.RunnerState, stage string, err error) {
	unlock := s.lockRunner(st.ID)
	defer unlock()
	current, readErr := s.store.ReadState(st.ID)
	if readErr != nil {
		s.logger.Error("read state for retry/fail", "id", st.ID, "error", readErr)
		return
	}
	if current.Status == state.StatusCompleted || current.Status == state.StatusFailed {
		return
	}
	result := s.applyFailure(&current, stage, err, true)
	if writeErr := s.store.WriteState(current); writeErr != nil {
		s.logger.Error("write retry/fail state", "id", st.ID, "error", writeErr)
		return
	}
	s.store.AppendLog(st.ID, "control.log", []byte(result.logLine+"\n"))
	if current.Status == state.StatusQueued {
		s.signalQueue()
	}
	s.refreshMetrics()
}

func (s *Server) deferQueuedRequest(id, workerID string, nextAttempt time.Time) {
	unlock := s.lockRunner(id)
	defer unlock()
	st, err := s.store.ReadState(id)
	if err != nil {
		s.logger.Error("read state for queue defer", "id", id, "error", err)
		_ = s.store.ReleaseLease(id, workerID)
		return
	}
	if st.Status != state.StatusQueued || st.LeaseOwner != workerID {
		_ = s.store.ReleaseLease(id, workerID)
		return
	}
	st.LeaseOwner = ""
	st.LeaseExpiresAt = time.Time{}
	st.NextRetryAt = nextAttempt
	if err := s.store.WriteState(st); err != nil {
		s.logger.Error("defer queued request", "id", id, "error", err)
		_ = s.store.ReleaseLease(id, workerID)
	}
}

func (s *Server) scheduleStopRetry(st *state.RunnerState, err error) bool {
	code, retryable := classifyRetryableError("stop", err)
	if !retryable || st.RetryCount >= s.cfg.RetryMaxAttempts {
		return false
	}
	st.RetryCount++
	st.Status = state.StatusStopping
	st.FailureStage = "stop"
	st.FailureReason = code
	st.LastErrorCode = code
	st.LastErrorMessage = err.Error()
	st.LastErrorRetryable = true
	st.Error = err.Error()
	st.NextRetryAt = s.nextRetryAt(st.RetryCount, time.Now().UTC())
	return true
}

func (s *Server) scheduleCleanupRetry(st *state.RunnerState, err error) bool {
	code, retryable := classifyRetryableError("github_cleanup", err)
	if !retryable {
		return false
	}
	if isGitHubRunnerBusyCleanup(err) {
		if st.RetryCount < s.cfg.RetryMaxAttempts {
			st.RetryCount++
		}
	} else {
		if st.RetryCount >= s.cfg.RetryMaxAttempts {
			return false
		}
		st.RetryCount++
	}
	st.Status = state.StatusStopping
	if st.FailureStage == "" {
		st.FailureStage = "cleanup"
		st.FailureReason = code
		st.Error = err.Error()
	}
	st.LastErrorCode = code
	st.LastErrorMessage = err.Error()
	st.LastErrorRetryable = true
	st.NextRetryAt = s.nextRetryAt(st.RetryCount, time.Now().UTC())
	return true
}

func isGitHubRunnerBusyCleanup(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "github remove runner: status 422") &&
		strings.Contains(msg, "currently running a job") &&
		strings.Contains(msg, "cannot be deleted")
}

func appendError(current, extra string) string {
	current = strings.TrimSpace(current)
	extra = strings.TrimSpace(extra)
	if current == "" {
		return extra
	}
	if extra == "" {
		return current
	}
	return current + "; " + extra
}

func (s *Server) profileForRunnerRequest(req state.RunnerRequest) (state.RunnerProfile, error) {
	source := req.ProfileSource
	if source == "" || source == "global" {
		return s.store.GetProfile(req.ProfileName)
	}
	if source == "scoped_custom" && req.ProfileScopeType != "" && req.ProfileScopeID > 0 {
		profile, err := s.store.GetScopedProfile(state.RunnerProfileScope{Type: req.ProfileScopeType, ID: req.ProfileScopeID}, req.ProfileName)
		if err != nil {
			return state.RunnerProfile{}, err
		}
		return state.RunnerProfile{
			Name:           profile.Name,
			Labels:         append([]string(nil), profile.WorkflowLabels...),
			RequiredLabels: append([]string(nil), profile.WorkflowLabels...),
			TemplateID:     profile.TemplateID,
			RunnerGroup:    profile.RunnerGroup,
			MaxConcurrency: profile.MaxConcurrency,
			Enabled:        profile.Enabled,
			CreatedAt:      profile.CreatedAt,
			UpdatedAt:      profile.UpdatedAt,
		}, nil
	}
	return state.RunnerProfile{}, state.ErrNotFound
}

func (s *Server) profileAtCapacityFor(req state.RunnerRequest, profile state.RunnerProfile) (bool, error) {
	if profile.MaxConcurrency <= 0 {
		return false, nil
	}
	source := req.ProfileSource
	if source == "" {
		source = "global"
	}
	if source == "global" {
		inFlight, err := s.store.InFlightCountForProfile(req.ProfileName)
		if err != nil {
			return false, err
		}
		if profile.MaxConcurrency > 0 && inFlight >= profile.MaxConcurrency {
			return true, nil
		}
		return false, nil
	}
	if req.ProfileScopeType != "" && req.ProfileScopeID > 0 {
		inFlight, err := s.store.InFlightCountForProfileScope(source, state.RunnerProfileScope{Type: req.ProfileScopeType, ID: req.ProfileScopeID}, req.ProfileName)
		if err != nil {
			return false, err
		}
		if profile.MaxConcurrency > 0 && inFlight >= profile.MaxConcurrency {
			return true, nil
		}
	} else if profile.MaxConcurrency > 0 {
		inFlight, err := s.store.InFlightCountForProfile(req.ProfileName)
		if err != nil {
			return false, err
		}
		if inFlight >= profile.MaxConcurrency {
			return true, nil
		}
	}
	return false, nil
}

func validateRequestedProfile(profile state.RunnerProfile, requestedLabels []string) error {
	if !profile.Enabled {
		return fmt.Errorf("profile %q is disabled", profile.Name)
	}
	if len(requestedLabels) > 0 {
		if !github.LabelsMatch(profile.RequiredLabels, requestedLabels) || !github.LabelsMatch(requestedLabels, profile.Labels) {
			return fmt.Errorf("requested labels do not satisfy profile %q", profile.Name)
		}
	}
	return nil
}

func (s *Server) recordAudit(actor, action, resourceType, resourceID string, payload any) {
	event, err := auditEventFor(actor, action, resourceType, resourceID, payload)
	if err == nil {
		event, err = s.store.AppendAuditEvent(event)
	}
	if err != nil {
		s.logger.Error("append audit event", "action", action, "resource_type", resourceType, "resource_id", resourceID, "error", err)
		return
	}
	s.logger.Info("audit event recorded", "id", event.ID, "actor", actor, "action", action, "resource_type", resourceType, "resource_id", resourceID)
}

func (s *Server) applyMutationWithAudit(actor, action, resourceType, resourceID string, payload any, mutation func(state.Store) error) error {
	event, err := auditEventFor(actor, action, resourceType, resourceID, payload)
	if err != nil {
		return fmt.Errorf("%w: %w", state.ErrAuditEventPersistence, err)
	}
	event, err = s.store.ApplyMutationWithAudit(event, mutation)
	if err != nil {
		return err
	}
	s.logger.Info("required mutation audit event recorded", "id", event.ID, "actor", actor, "action", action, "resource_type", resourceType, "resource_id", resourceID)
	return nil
}

func auditEventFor(actor, action, resourceType, resourceID string, payload any) (state.AuditEvent, error) {
	var payloadJSON string
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return state.AuditEvent{}, fmt.Errorf("marshal audit payload: %w", err)
		}
		payloadJSON = string(data)
	}
	return state.AuditEvent{
		Actor:        actor,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		PayloadJSON:  payloadJSON,
		CreatedAt:    time.Now().UTC(),
	}, nil
}

func writeMutationAuditError(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, state.ErrAuditEventPersistence) {
		return false
	}
	writeError(w, http.StatusInternalServerError, "persist mutation audit: "+err.Error())
	return true
}

func isActiveStatus(status string) bool {
	switch status {
	case state.StatusQueued, state.StatusCreating, state.StatusRunning, state.StatusStopping:
		return true
	default:
		return false
	}
}

type pprofAddress struct {
	Path    string
	Address string
}

func discoverPprofArtifacts() ([]pprofAddress, []string) {
	executable, err := os.Executable()
	if err != nil {
		return nil, nil
	}
	dir := filepath.Dir(executable)
	name := filepath.Base(executable)
	pprofFiles, _ := filepath.Glob(filepath.Join(dir, name+"_*_*.pprof"))
	scriptFiles, _ := filepath.Glob(filepath.Join(dir, name+"_*_profile_dump.sh"))
	addresses := make([]pprofAddress, 0, len(pprofFiles))
	for _, file := range pprofFiles {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		addresses = append(addresses, pprofAddress{
			Path:    file,
			Address: strings.TrimSpace(string(data)),
		})
	}
	return addresses, scriptFiles
}

func (s *Server) lockRunner(id string) func() {
	h := fnv.New32a()
	_, _ = h.Write([]byte(id))
	mu := &s.locks[int(h.Sum32()%uint32(len(s.locks)))]
	mu.Lock()
	return func() {
		mu.Unlock()
	}
}

func (s *Server) cleanupStartedSandbox(id string, result sandboxrunner.StartResult) {
	if result.SandboxID == "" {
		s.logger.Info("cleanup started sandbox skipped because sandbox id is empty", "id", id)
		return
	}
	s.logger.Info("cleanup started sandbox", "id", id, "sandbox_id", result.SandboxID, "pid", result.PID)
	if err := s.stopSandboxWithTimeout(context.Background(), id, result.SandboxID, result.PID); err != nil && !isSandboxGone(err) {
		s.logger.Error("cleanup started sandbox", "id", id, "sandbox_id", result.SandboxID, "error", err)
		s.store.AppendLog(id, "control.log", []byte("cleanup started sandbox failed: "+err.Error()+"\n"))
		return
	}
	s.logger.Info("cleaned started sandbox", "id", id, "sandbox_id", result.SandboxID)
	s.store.AppendLog(id, "control.log", []byte("cleaned started sandbox\n"))
}

func (s *Server) stopSandboxWithTimeout(ctx context.Context, id, sandboxID string, pid uint32) error {
	if ctx == nil {
		ctx = context.Background()
	} else {
		ctx = context.WithoutCancel(ctx)
	}
	req, err := s.store.ReadRequest(id)
	if err != nil {
		return fmt.Errorf("read runner request for sandbox service: %w", err)
	}
	sandboxService, err := s.sandboxServiceForRunnerRequest(ctx, req)
	if err != nil {
		return err
	}
	s.logger.Info("stopping sandbox", "sandbox_id", sandboxID, "pid", pid, "timeout", s.cfg.SandboxStopTimeout.String())
	stopCtx, cancel := context.WithTimeout(ctx, s.cfg.SandboxStopTimeout)
	defer cancel()
	if err := sandboxService.StopRunner(stopCtx, sandboxID, pid); err != nil {
		s.logger.Error("stop sandbox failed", "sandbox_id", sandboxID, "pid", pid, "error", err)
		return err
	}
	s.logger.Info("sandbox stopped", "sandbox_id", sandboxID, "pid", pid)
	return nil
}
