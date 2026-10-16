package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	deploy "github.com/miclle/deployd"
	deployconfig "github.com/qiniu/ci-runner/internal/deployment/config"
	deployruntime "github.com/qiniu/ci-runner/internal/deployment/runtime"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

const (
	defaultDeploymentTTL = time.Hour
	minDeploymentTTL     = 5 * time.Minute
	maxDeploymentTTL     = 24 * time.Hour
	maxDeploymentBody    = 1 << 20
)

type createDeploymentRequest struct {
	CommitSHA          string `json:"commit_sha"`
	InstallationID     int64  `json:"installation_id"`
	RepositoryFullName string `json:"repository_full_name"`
	Ref                string `json:"ref"`
	Source             string `json:"source"`
	ConfigPath         string `json:"config_path"`
	TTLSeconds         int64  `json:"ttl_seconds"`
	TemplateID         string `json:"template_id"`
	TemplateName       string `json:"template_name"`
	TemplateCPU        int32  `json:"template_cpu"`
	TemplateMemoryMB   int32  `json:"template_memory_mb"`
	TemplateDiskMB     int32  `json:"template_disk_mb"`
}

type retryDeploymentRequest struct {
	Mode           string `json:"mode"`
	RevisionNumber int64  `json:"revision_number,omitempty"`
}

type deploymentResponse struct {
	LastSuccessfulRevisionNumber int64                        `json:"last_successful_revision_number,omitempty"`
	ID                           string                       `json:"id"`
	ScopeType                    string                       `json:"scope_type"`
	ScopeID                      int64                        `json:"scope_id"`
	GitHubInstallationID         int64                        `json:"github_installation_id"`
	RepositoryID                 int64                        `json:"repository_id"`
	RepositoryFullName           string                       `json:"repository_full_name"`
	Ref                          string                       `json:"ref,omitempty"`
	Source                       string                       `json:"source,omitempty"`
	CommitSHA                    string                       `json:"commit_sha"`
	ConfigPath                   string                       `json:"config_path"`
	SnapshotVersion              int                          `json:"snapshot_version"`
	SpecHash                     string                       `json:"spec_hash"`
	Status                       string                       `json:"status"`
	TemplateID                   string                       `json:"template_id,omitempty"`
	TemplateName                 string                       `json:"template_name,omitempty"`
	TemplateCPU                  int32                        `json:"template_cpu,omitempty"`
	TemplateMemoryMB             int32                        `json:"template_memory_mb,omitempty"`
	TemplateDiskMB               int32                        `json:"template_disk_mb,omitempty"`
	SandboxID                    string                       `json:"sandbox_id,omitempty"`
	SandboxConfigSource          string                       `json:"sandbox_config_source,omitempty"`
	SandboxRegion                string                       `json:"sandbox_region,omitempty"`
	Endpoint                     string                       `json:"endpoint,omitempty"`
	EndpointAccess               string                       `json:"endpoint_access,omitempty"`
	FailureStage                 string                       `json:"failure_stage,omitempty"`
	FailureReason                string                       `json:"failure_reason,omitempty"`
	ExpiresAt                    time.Time                    `json:"expires_at"`
	CreatedAt                    time.Time                    `json:"created_at"`
	PlanningAt                   time.Time                    `json:"planning_at,omitempty"`
	CreatingAt                   time.Time                    `json:"creating_at,omitempty"`
	RunningAt                    time.Time                    `json:"running_at,omitempty"`
	StoppingAt                   time.Time                    `json:"stopping_at,omitempty"`
	CompletedAt                  time.Time                    `json:"completed_at,omitempty"`
	FailedAt                     time.Time                    `json:"failed_at,omitempty"`
	UpdatedAt                    time.Time                    `json:"updated_at"`
	Version                      int64                        `json:"version"`
	RevisionNumber               int64                        `json:"revision_number,omitempty"`
	Revisions                    []deploymentRevisionResponse `json:"revisions,omitempty"`
	RevisionsHasMore             bool                         `json:"revisions_has_more"`
	SelectedRevision             *deploymentRevisionResponse  `json:"selected_revision,omitempty"`
	PullRequestNumber            int64                        `json:"pull_request_number,omitempty"`
	SourceRepositoryID           int64                        `json:"source_repository_id,omitempty"`
	SourceRepositoryFullName     string                       `json:"source_repository_full_name,omitempty"`
	SourceRef                    string                       `json:"source_ref,omitempty"`
	PullRequestTitle             string                       `json:"pull_request_title,omitempty"`
}

type deploymentRevisionResponse struct {
	ID                       string    `json:"id"`
	Number                   int64     `json:"number"`
	Status                   string    `json:"status"`
	Ref                      string    `json:"ref,omitempty"`
	CommitSHA                string    `json:"commit_sha"`
	ConfigPath               string    `json:"config_path"`
	TemplateID               string    `json:"template_id,omitempty"`
	TemplateName             string    `json:"template_name,omitempty"`
	TemplateCPU              int32     `json:"template_cpu,omitempty"`
	TemplateMemoryMB         int32     `json:"template_memory_mb,omitempty"`
	TemplateDiskMB           int32     `json:"template_disk_mb,omitempty"`
	SandboxID                string    `json:"sandbox_id,omitempty"`
	SandboxRegion            string    `json:"sandbox_region,omitempty"`
	Endpoint                 string    `json:"endpoint,omitempty"`
	FailureStage             string    `json:"failure_stage,omitempty"`
	FailureReason            string    `json:"failure_reason,omitempty"`
	ExpiresAt                time.Time `json:"expires_at"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
	PullRequestNumber        int64     `json:"pull_request_number,omitempty"`
	SourceRepositoryID       int64     `json:"source_repository_id,omitempty"`
	SourceRepositoryFullName string    `json:"source_repository_full_name,omitempty"`
	SourceRef                string    `json:"source_ref,omitempty"`
	PullRequestTitle         string    `json:"pull_request_title,omitempty"`
}

// deploymentResourcesResponse is intentionally separate from the persisted
// deployment response. Details and logs are local state; this small endpoint
// is the only path that may contact Sandbox to refresh provider-owned values.
type deploymentResourcesResponse struct {
	TemplateID       string    `json:"template_id,omitempty"`
	TemplateName     string    `json:"template_name,omitempty"`
	TemplateCPU      int32     `json:"template_cpu,omitempty"`
	TemplateMemoryMB int32     `json:"template_memory_mb,omitempty"`
	TemplateDiskMB   int32     `json:"template_disk_mb,omitempty"`
	SandboxID        string    `json:"sandbox_id,omitempty"`
	SandboxRegion    string    `json:"sandbox_region,omitempty"`
	ExpiresAt        time.Time `json:"expires_at,omitempty"`
}

func (s *Server) handleUserCreateDeployment(w http.ResponseWriter, r *http.Request) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	var input createDeploymentRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDeploymentBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_deployment", "invalid deployment payload")
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeErrorCode(w, http.StatusBadRequest, "invalid_deployment", "invalid deployment payload")
		return
	}
	if err := validateCreateDeploymentRequest(input); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_deployment", err.Error())
		return
	}
	input.Ref = normalizeDeploymentRef(input.Ref, input.Source)
	input.TemplateID = strings.TrimSpace(input.TemplateID)
	input.TemplateName = strings.TrimSpace(input.TemplateName)
	input.RepositoryFullName = strings.TrimSpace(input.RepositoryFullName)
	input.ConfigPath = normalizeDeploymentConfigPath(input.ConfigPath)
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > 128 || !utf8.ValidString(key) || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		writeErrorCode(w, http.StatusBadRequest, "invalid_deployment", "Idempotency-Key is invalid")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	r = r.WithContext(ctx)

	installation, found, err := s.githubInstallationForAccount(account.ID, input.InstallationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load GitHub installation")
		return
	}
	if !found {
		writeErrorCode(w, http.StatusNotFound, "github_installation_not_found", "GitHub installation is not linked to this account")
		return
	}
	if s.gh == nil {
		writeError(w, http.StatusBadGateway, "GitHub client is not configured")
		return
	}

	// Repository access authorizes deployment execution independently of Settings ownership.
	scope, ok := s.deploymentScope(w, account.ID, installation)
	if !ok {
		return
	}

	token, err := s.githubUserAccessToken(account.ID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	repositories, err := s.gh.ListUserInstallationRepositoryDetails(r.Context(), token, installation.InstallationID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	repository, ok := deploymentRepositoryAccess(repositories, input.RepositoryFullName, installation)
	if !ok {
		writeErrorCode(w, http.StatusForbidden, "repository_access_forbidden", "repository is not accessible through this GitHub installation and user session")
		return
	}
	// Return the existing resource only after current authorization. Compare the
	// original request, even after provider expiry or the current revision changes.
	deploymentID := newDeploymentID(account.ID, key)
	if key != "" {
		if s.writeDeploymentReplay(w, deploymentID, account.ID, installation.InstallationID, repository.ID, input) {
			return
		}
	}

	templateID := input.TemplateID
	if templateID == "" {
		templateID = strings.TrimSpace(s.cfg.DeploymentTemplateID)
	}
	if templateID == "" {
		writeErrorCode(w, http.StatusBadRequest, "deployment_template_required", "Select a Sandbox template before deploying")
		return
	}
	service, sandboxSnapshot, snapshotErr := s.sandboxServiceForDeployment(r.Context(), scope)
	if snapshotErr != nil {
		writeErrorCode(w, http.StatusConflict, "deployment_sandbox_not_configured", "Configure Sandbox credentials in this account or organization's Preferences before deploying")
		return
	}
	if !s.authorizeDeploymentTemplate(w, r, account.ID, scope, service, templateID) {
		return
	}
	if err := service.ValidateTemplate(r.Context(), templateID); err != nil {
		writeErrorCode(w, http.StatusConflict, "deployment_template_unavailable", "The selected Sandbox template is unavailable or has no runnable default build")
		return
	}

	sourceSnapshot, err := s.resolveDeploymentSource(r.Context(), repository, input.Ref, input.CommitSHA)
	if err != nil {
		if errors.Is(err, errDeploymentSourceChanged) {
			writeErrorCode(w, http.StatusConflict, "deployment_source_changed", "The selected commit is no longer available in this source. Refresh the source and choose a commit again.")
			return
		}
		writeErrorCode(w, http.StatusBadGateway, "commit_resolution_failed", "failed to resolve repository ref")
		return
	}
	commitSHA := sourceSnapshot.CommitSHA
	configData, err := s.gh.ReadRepositoryFile(r.Context(), repository.FullName, input.ConfigPath, commitSHA, deployconfig.MaxBytes)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "deployment_config_invalid", "failed to read deployment configuration")
		return
	}
	spec, err := deployconfig.Parse(configData)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "deployment_config_invalid", "invalid deployment configuration")
		return
	}
	plan, err := deploy.NewPlan(deploy.ResolvedSource{
		SourceID:  githubDeploymentSourceURL(repository.FullName),
		CommitSHA: commitSHA,
	}, spec)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "deployment_config_invalid", "invalid deployment execution plan")
		return
	}
	specJSON, err := json.Marshal(plan.Spec())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "marshal deployment plan")
		return
	}
	encryptedSpec, err := encryptSecret(string(specJSON), s.cfg.AuthEncryptionKey.Value())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encrypt deployment plan")
		return
	}

	now := time.Now().UTC()
	ttl := defaultDeploymentTTL
	if input.TTLSeconds > 0 {
		ttl = time.Duration(input.TTLSeconds) * time.Second
	}
	deployment := state.Deployment{
		CreateRequestHash:  deploymentCreateRequestHash(input, installation.InstallationID, repository.ID),
		SourceRepositoryID: sourceSnapshot.RepositoryID, SourceRepositoryFullName: sourceSnapshot.RepositoryFullName, SourceRef: sourceSnapshot.Ref, PullRequestTitle: sourceSnapshot.PullRequestTitle,
		ID:                         deploymentID,
		AccountID:                  account.ID,
		ExecutionAccountID:         account.ID,
		ScopeType:                  scope.Type,
		ScopeID:                    scope.ID,
		GitHubInstallationID:       installation.InstallationID,
		RepositoryID:               repository.ID,
		RepositoryFullName:         repository.FullName,
		Ref:                        input.Ref,
		CommitSHA:                  commitSHA,
		ConfigPath:                 input.ConfigPath,
		SnapshotVersion:            plan.Snapshot().Version,
		SpecHash:                   plan.Snapshot().SpecHash,
		SpecJSONEncrypted:          encryptedSpec,
		Status:                     state.DeploymentStatusQueued,
		SandboxAPIURL:              sandboxSnapshot.APIURL,
		SandboxAPIKeyEncrypted:     sandboxSnapshot.EncryptedAPIKey,
		SandboxConfigSource:        sandboxSnapshot.Source,
		SandboxRegion:              s.sandboxRegionForAPIURL(sandboxSnapshot.APIURL),
		DeploymentTemplateID:       templateID,
		DeploymentTemplateName:     input.TemplateName,
		DeploymentTemplateCPU:      input.TemplateCPU,
		DeploymentTemplateMemoryMB: input.TemplateMemoryMB,
		DeploymentTemplateDiskMB:   input.TemplateDiskMB,
		ExpiresAt:                  now.Add(ttl),
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}
	auditPayload := map[string]any{
		"installation_id":      installation.InstallationID,
		"repository_id":        repository.ID,
		"repository_full_name": repository.FullName,
		"ref":                  input.Ref,
		"commit_sha":           commitSHA,
		"config_path":          input.ConfigPath,
		"snapshot_version":     plan.Snapshot().Version,
		"spec_hash":            plan.Snapshot().SpecHash,
		"expires_at":           deployment.ExpiresAt,
		"template_id":          templateID,
		"template_name":        input.TemplateName,
		"template_cpu":         input.TemplateCPU,
		"template_memory_mb":   input.TemplateMemoryMB,
		"template_disk_mb":     input.TemplateDiskMB,
	}
	err = s.applyMutationWithAudit("github:"+session.Subject, "deployment.create", "deployment", deployment.ID, auditPayload, func(tx state.Store) error {
		if _, lookupErr := tx.GetDeployment(deployment.ID); lookupErr == nil {
			return state.ErrConflict
		} else if !errors.Is(lookupErr, state.ErrNotFound) {
			return lookupErr
		}
		if _, createErr := tx.CreateDeploymentWithScopeLimit(deployment, s.cfg.DeploymentMaxConcurrent); createErr != nil {
			return createErr
		}
		return tx.AppendDeploymentEvent(state.DeploymentEvent{DeploymentID: deployment.ID, EventType: "lifecycle", Stage: "queued", Message: "Deployment queued for execution."})
	})
	if err != nil {
		if errors.Is(err, state.ErrDeploymentQuotaExceeded) {
			w.Header().Set("Retry-After", "60")
			writeErrorCode(w, http.StatusTooManyRequests, "deployment_quota_exceeded", "deployment scope concurrency limit reached")
			return
		}
		if key != "" && !errors.Is(err, state.ErrAuditEventPersistence) && s.writeDeploymentReplay(w, deploymentID, account.ID, installation.InstallationID, repository.ID, input) {
			return
		}
		writeError(w, http.StatusInternalServerError, "persist deployment")
		return
	}
	s.signalDeployments()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, deploymentResponseFromState(deployment))
}

func githubDeploymentSourceURL(repository string) string {
	return "https://github.com/" + strings.TrimSuffix(strings.TrimSpace(repository), ".git") + ".git"
}

func (s *Server) handleUserDeploymentRepositories(w http.ResponseWriter, r *http.Request) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePagination(r, 100, 500)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	access, err := s.userAuthorizedRepositoryAccess(r.Context(), account.ID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	items, more, err := s.store.ListDeploymentRepositories(access, r.URL.Query().Get("repository_full_name"), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list deployment repositories")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "has_more": more, "limit": limit, "offset": offset})
}

func (s *Server) handleUserListDeployments(w http.ResponseWriter, r *http.Request) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePagination(r, 100, 500)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	options := state.DeploymentListOptions{Limit: limit, Offset: offset, RepositoryFullName: r.URL.Query().Get("repository_full_name"), Ref: r.URL.Query().Get("ref"), CommitSHA: r.URL.Query().Get("commit_sha"), Status: r.URL.Query().Get("status")}
	if _, err := state.DeploymentListStatusValues(options.Status); err != nil || len(r.URL.Query()["status"]) > 1 {
		writeError(w, http.StatusBadRequest, "invalid deployment status filter")
		return
	}
	if value := r.URL.Query().Get("pull_request_number"); value != "" {
		number, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || number <= 0 || options.Ref != "" || options.CommitSHA != "" {
			writeError(w, http.StatusBadRequest, "invalid pull request context")
			return
		}
		options.Ref = fmt.Sprintf("refs/pull/%d/head", number)
	}
	access, err := s.userAuthorizedRepositoryAccess(r.Context(), account.ID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	items, total, err := s.store.ListAuthorizedDeployments(access, options)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list deployments")
		return
	}
	counts, err := s.store.CountAuthorizedDeploymentsByStatus(access, options)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "count deployments")
		return
	}
	responses := make([]deploymentResponse, 0, len(items))
	for _, item := range items {
		response := deploymentResponseFromState(item)
		if item.ContextRevision != nil {
			revision := deploymentRevisionResponseFromState(*item.ContextRevision)
			response.SelectedRevision = &revision
		}
		responses = append(responses, response)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"items": responses, "total": total, "status_counts": counts, "limit": limit, "offset": offset})
}

func (s *Server) handleUserGetDeployment(w http.ResponseWriter, r *http.Request) {
	_, _, deployment, ok := s.loadAuthorizedDeployment(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	response := deploymentResponseFromState(deployment)
	revisions, more, err := s.store.ListDeploymentRevisions(deployment.ID, 0, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load deployment revisions")
		return
	}
	lastSuccessful, err := s.store.LastSuccessfulDeploymentRevisionNumber(deployment.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load successful deployment revision")
		return
	}
	response.LastSuccessfulRevisionNumber = lastSuccessful
	response.RevisionsHasMore = more
	response.Revisions = make([]deploymentRevisionResponse, 0, len(revisions))
	for _, revision := range revisions {
		response.Revisions = append(response.Revisions, deploymentRevisionResponseFromState(revision))
	}
	if value := strings.TrimSpace(r.URL.Query().Get("revision")); value != "" {
		number, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil || number <= 0 {
			writeError(w, http.StatusBadRequest, "invalid revision number")
			return
		}
		revision, lookupErr := s.store.GetDeploymentRevision(deployment.ID, number)
		if lookupErr != nil {
			if errors.Is(lookupErr, state.ErrNotFound) {
				writeError(w, http.StatusNotFound, "deployment revision not found")
			} else {
				writeError(w, http.StatusInternalServerError, "load deployment revision")
			}
			return
		}
		selected := deploymentRevisionResponseFromState(revision)
		response.SelectedRevision = &selected
	}
	writeJSON(w, http.StatusOK, response)
}

// Legacy title enrichment is separate so detail and log reads stay local.
func (s *Server) handleUserGetDeploymentPullRequest(w http.ResponseWriter, r *http.Request) {
	_, _, deployment, ok := s.loadAuthorizedDeployment(w, r)
	if !ok {
		return
	}
	number := deploymentPullRequestNumber(deployment.Ref)
	if number == 0 {
		writeError(w, http.StatusNotFound, "deployment is not a pull request")
		return
	}
	title := deployment.PullRequestTitle
	if strings.TrimSpace(title) == "" {
		group := userRunnerJobGroupResponse{Group: "pull_request", Repository: deployment.RepositoryFullName, PullRequestNumber: number}
		s.enrichUserRunnerJobGroup(r.Context(), &group)
		title = strings.TrimSpace(group.PullRequestTitle)
		if title != "" {
			if err := s.store.FillDeploymentPullRequestTitle(deployment.ID, deployment.Ref, title); err != nil {
				writeError(w, http.StatusInternalServerError, "save deployment pull request title")
				return
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"pull_request_number": number, "pull_request_title": title})
}

func (s *Server) handleUserGetDeploymentResources(w http.ResponseWriter, r *http.Request) {
	_, _, deployment, ok := s.loadAuthorizedDeployment(w, r)
	if !ok {
		return
	}
	selected := state.Deployment(deployment)
	if value := strings.TrimSpace(r.URL.Query().Get("revision")); value != "" {
		number, err := strconv.ParseInt(value, 10, 64)
		if err != nil || number <= 0 {
			writeError(w, http.StatusBadRequest, "invalid revision number")
			return
		}
		revision, err := s.store.GetDeploymentRevision(deployment.ID, number)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				writeError(w, http.StatusNotFound, "deployment revision not found")
			} else {
				writeError(w, http.StatusInternalServerError, "load deployment revision")
			}
			return
		}
		selected = revision.Deployment
	}
	response := deploymentResourcesResponse{
		TemplateID:       selected.DeploymentTemplateID,
		TemplateName:     selected.DeploymentTemplateName,
		TemplateCPU:      selected.DeploymentTemplateCPU,
		TemplateMemoryMB: selected.DeploymentTemplateMemoryMB,
		TemplateDiskMB:   selected.DeploymentTemplateDiskMB,
		SandboxID:        selected.SandboxID,
		SandboxRegion:    selected.SandboxRegion,
		ExpiresAt:        selected.ExpiresAt,
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	// Provider reads are best effort. The detail view remains useful from the
	// local snapshot when Sandbox is temporarily unavailable.
	_ = s.refreshDeploymentResources(ctx, &deployment, &selected, &response)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) refreshDeploymentResources(ctx context.Context, deployment, selected *state.Deployment, response *deploymentResourcesResponse) error {
	if selected == nil || response == nil {
		return nil
	}
	var service sandboxrunner.Service
	if strings.TrimSpace(deployment.SandboxAPIURL) != "" && strings.TrimSpace(deployment.SandboxAPIKeyEncrypted) != "" {
		resolved, err := s.sandboxServiceForConfig(sandboxServiceConfigSnapshot{APIURL: deployment.SandboxAPIURL, EncryptedAPIKey: deployment.SandboxAPIKeyEncrypted, Source: deployment.SandboxConfigSource})
		if err != nil {
			return err
		}
		service = resolved
	} else if s.sandbox != nil {
		service = s.sandbox
	} else {
		resolved, _, err := s.sandboxServiceForDeployment(ctx, accountPreferenceScope{Type: deployment.ScopeType, ID: deployment.ScopeID})
		if err != nil {
			return err
		}
		service = resolved
	}

	if inspector, ok := service.(sandboxrunner.DeploymentSandboxInspector); ok && strings.TrimSpace(selected.SandboxID) != "" {
		box, err := inspector.InspectDeploymentSandbox(ctx, selected.SandboxID)
		if err != nil {
			if !errors.Is(err, sandboxrunner.ErrSandboxNotFound) {
				return err
			}
		} else {
			response.SandboxID = box.SandboxID
			response.TemplateID = box.TemplateID
			response.TemplateCPU = box.CPUCount
			response.TemplateMemoryMB = box.MemoryMB
			response.TemplateDiskMB = box.DiskSizeMB
			if !box.ExpiresAt.IsZero() {
				response.ExpiresAt = box.ExpiresAt
			}
		}
	}
	if inspector, ok := service.(sandboxrunner.TemplateInspector); ok && strings.TrimSpace(response.TemplateID) != "" {
		info, err := inspector.InspectTemplate(ctx, response.TemplateID)
		if err != nil {
			return err
		}
		if info.Template != nil {
			if name := sandboxTemplateDisplayName(info.Template.Names); name != "" {
				response.TemplateName = name
			}
			if response.TemplateCPU == 0 {
				response.TemplateCPU = info.Template.CPUCount
			}
			if response.TemplateMemoryMB == 0 {
				response.TemplateMemoryMB = info.Template.MemoryMB
			}
			if response.TemplateDiskMB == 0 {
				response.TemplateDiskMB = info.Template.DiskSizeMB
			}
		}
	}
	return nil
}

func sandboxTemplateDisplayName(names []string) string {
	for _, value := range names {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func (s *Server) handleUserListDeploymentRevisions(w http.ResponseWriter, r *http.Request) {
	_, _, deployment, ok := s.loadAuthorizedDeployment(w, r)
	if !ok {
		return
	}
	before := int64(0)
	if value := strings.TrimSpace(r.URL.Query().Get("before_number")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid before_number")
			return
		}
		before = parsed
	}
	items, more, err := s.store.ListDeploymentRevisions(deployment.ID, before, 100)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list deployment revisions")
		return
	}
	result := make([]deploymentRevisionResponse, 0, len(items))
	for _, item := range items {
		result = append(result, deploymentRevisionResponseFromState(item))
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"revisions": result, "has_more": more})
}

// Deployment content stays local; all reads and actions share Jobs' cached
// exact installation/repository authorization, regardless of the creator.
func (s *Server) loadAuthorizedDeployment(w http.ResponseWriter, r *http.Request) (adminSession, state.Account, state.Deployment, bool) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return adminSession{}, state.Account{}, state.Deployment{}, false
	}
	deployment, err := s.store.GetDeployment(strings.TrimSpace(r.PathValue("id")))
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusNotFound, "deployment not found")
		return adminSession{}, state.Account{}, state.Deployment{}, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load deployment")
		return adminSession{}, state.Account{}, state.Deployment{}, false
	}
	access, err := s.userAuthorizedRepositoryAccess(r.Context(), account.ID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return adminSession{}, state.Account{}, state.Deployment{}, false
	}
	if !hasRepositoryAccess(access, deployment.GitHubInstallationID, deployment.RepositoryFullName) {
		writeError(w, http.StatusNotFound, "deployment not found")
		return adminSession{}, state.Account{}, state.Deployment{}, false
	}
	return session, account, deployment, true
}

func (s *Server) handleUserListDeploymentEvents(w http.ResponseWriter, r *http.Request) {
	_, _, deployment, ok := s.loadAuthorizedDeployment(w, r)
	if !ok {
		return
	}
	number := deployment.RevisionNumber
	if value := r.PathValue("number"); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid revision number")
			return
		}
		number = parsed
	}
	limit := 200
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, "invalid limit")
			return
		}
		limit = min(parsed, 500)
	}
	cursors := [2]int64{}
	for i, key := range []string{"before_id", "after_id"} {
		if value := strings.TrimSpace(r.URL.Query().Get(key)); value != "" {
			parsed, err := strconv.ParseInt(value, 10, 64)
			if err != nil || parsed <= 0 {
				writeError(w, http.StatusBadRequest, "invalid "+key)
				return
			}
			cursors[i] = parsed
		}
	}
	if cursors[0] > 0 && cursors[1] > 0 {
		writeError(w, http.StatusBadRequest, "before_id and after_id are mutually exclusive")
		return
	}
	var events []state.DeploymentEvent
	var hasMore bool
	var err error
	if cursors[1] > 0 {
		events, hasMore, err = s.store.ListDeploymentRevisionEvents(deployment.ID, number, 0, cursors[1], limit)
	} else {
		events, hasMore, err = s.store.ListDeploymentRevisionEvents(deployment.ID, number, cursors[0], 0, limit)
	}
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeError(w, http.StatusNotFound, "deployment revision not found")
		} else {
			writeError(w, http.StatusInternalServerError, "list deployment events")
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "has_more": hasMore})
}

func (s *Server) handleUserStopDeployment(w http.ResponseWriter, r *http.Request) {
	session, _, deployment, ok := s.loadAuthorizedDeployment(w, r)
	if !ok {
		return
	}
	if s.deploymentWorker == nil && deployment.SandboxID != "" {
		writeError(w, http.StatusServiceUnavailable, "deployment worker is not configured")
		return
	}
	terminal := deployment.Status == state.DeploymentStatusCompleted || deployment.Status == state.DeploymentStatusExpired || deployment.Status == state.DeploymentStatusFailed
	if !(terminal && deployment.SandboxID == "") {
		next := deployment
		next.Status = state.DeploymentStatusStopping
		if deployment.Status == state.DeploymentStatusFailed {
			next.Status = state.DeploymentStatusFailed
		}
		if deployment.Status == state.DeploymentStatusQueued || (s.deploymentWorker == nil && deployment.SandboxID == "") {
			next.Status = state.DeploymentStatusCompleted
		}
		err := s.applyMutationWithAudit("github:"+session.Subject, "deployment.stop", "deployment", deployment.ID, map[string]any{"status": next.Status}, func(tx state.Store) error { return tx.UpdateDeployment(next) })
		if err != nil {
			writeError(w, http.StatusConflict, "deployment changed while stopping")
			return
		}
	}
	if s.deploymentWorker != nil {
		if err := s.deploymentWorker.Stop(r.Context(), deployment.ID, false); err != nil {
			if errors.Is(err, deployruntime.ErrCreatePending) {
				updated, _ := s.store.GetDeployment(deployment.ID)
				w.Header().Set("Cache-Control", "no-store")
				writeJSON(w, http.StatusAccepted, deploymentResponseFromState(updated))
				return
			}
			writeError(w, http.StatusBadGateway, "deployment cleanup is incomplete")
			return
		}
	}
	updated, err := s.store.GetDeployment(deployment.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load deployment")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, deploymentResponseFromState(updated))
}

func (s *Server) handleUserRetryDeployment(w http.ResponseWriter, r *http.Request) {
	session, account, deployment, ok := s.loadAuthorizedDeployment(w, r)
	if !ok {
		return
	}
	input := retryDeploymentRequest{Mode: "revision"}
	if r.Body != nil {
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&input); err != nil && !errors.Is(err, io.EOF) {
			writeErrorCode(w, http.StatusBadRequest, "invalid_deployment_retry", "invalid redeploy selection")
			return
		}
		if err := decoder.Decode(new(any)); err != io.EOF {
			writeErrorCode(w, http.StatusBadRequest, "invalid_deployment_retry", "invalid redeploy selection")
			return
		}
	}
	input.Mode = strings.TrimSpace(strings.ToLower(input.Mode))
	if input.Mode == "" {
		input.Mode = "revision"
	}
	if input.Mode != "latest" && input.Mode != "revision" {
		writeErrorCode(w, http.StatusBadRequest, "invalid_deployment_retry", "redeploy mode must be latest or revision")
		return
	}
	if deployment.Status != state.DeploymentStatusFailed && deployment.Status != state.DeploymentStatusCompleted && deployment.Status != state.DeploymentStatusExpired {
		writeError(w, http.StatusConflict, "deployment retry is not allowed in the current state")
		return
	}
	if deployment.SandboxID != "" {
		if s.deploymentWorker == nil {
			writeError(w, http.StatusServiceUnavailable, "deployment worker is not configured")
			return
		}
		if err := s.deploymentWorker.Stop(r.Context(), deployment.ID, false); err != nil {
			writeError(w, http.StatusBadGateway, "deployment cleanup is incomplete")
			return
		}
		deployment, _ = s.store.GetDeployment(deployment.ID)
	}
	if input.Mode == "revision" && input.RevisionNumber > 0 {
		revision, err := s.store.GetDeploymentRevision(deployment.ID, input.RevisionNumber)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				writeError(w, http.StatusNotFound, "deployment revision not found")
			} else {
				writeError(w, http.StatusInternalServerError, "load deployment revision")
			}
			return
		}
		deployment.SourceRepositoryID, deployment.SourceRepositoryFullName, deployment.SourceRef = revision.SourceRepositoryID, revision.SourceRepositoryFullName, revision.SourceRef
		deployment.PullRequestTitle = revision.PullRequestTitle
		deployment.Ref = revision.Ref
		deployment.CommitSHA = revision.CommitSHA
		deployment.ConfigPath = revision.ConfigPath
		deployment.SnapshotVersion = revision.SnapshotVersion
		deployment.SpecHash = revision.SpecHash
		deployment.SpecJSONEncrypted = revision.SpecJSONEncrypted
		deployment.DeploymentTemplateID = revision.DeploymentTemplateID
		deployment.DeploymentTemplateName = revision.DeploymentTemplateName
		deployment.DeploymentTemplateCPU = revision.DeploymentTemplateCPU
		deployment.DeploymentTemplateMemoryMB = revision.DeploymentTemplateMemoryMB
		deployment.DeploymentTemplateDiskMB = revision.DeploymentTemplateDiskMB
		deployment.SandboxID = ""
		deployment.RuntimeID = ""
		deployment.ProcessID = ""
		deployment.ProcessTag = ""
	} else if input.Mode == "latest" {
		if !strings.HasPrefix(deployment.Ref, "refs/heads/") && deploymentPullRequestNumber(deployment.Ref) == 0 {
			writeErrorCode(w, http.StatusBadRequest, "invalid_deployment_retry", "latest redeploy is available only for branch or pull request deployments")
			return
		}
		if err := s.refreshDeploymentPlanFromGitHub(r.Context(), account.ID, &deployment); err != nil {
			writeErrorCode(w, http.StatusBadGateway, "deployment_latest_source_failed", "failed to resolve the latest repository source")
			return
		}
	}
	if strings.TrimSpace(deployment.DeploymentTemplateID) == "" {
		writeErrorCode(w, http.StatusConflict, "deployment_template_required", "Create a new deployment with an available Sandbox template")
		return
	}
	service, snapshot, snapshotErr := s.sandboxServiceForDeployment(r.Context(), accountPreferenceScope{Type: deployment.ScopeType, ID: deployment.ScopeID})
	if snapshotErr != nil {
		writeErrorCode(w, http.StatusConflict, "deployment_sandbox_not_configured", "Deploy requires configured scoped Sandbox credentials")
		return
	}
	if err := service.ValidateTemplate(r.Context(), deployment.DeploymentTemplateID); err != nil {
		writeErrorCode(w, http.StatusConflict, "deployment_template_unavailable", "The selected Sandbox template is unavailable or has no runnable default build")
		return
	}
	deployment.ExecutionAccountID = account.ID
	deployment.SandboxAPIURL, deployment.SandboxAPIKeyEncrypted, deployment.SandboxConfigSource = snapshot.APIURL, snapshot.EncryptedAPIKey, snapshot.Source
	deployment.SandboxRegion = s.sandboxRegionForAPIURL(snapshot.APIURL)
	expiresAt := time.Now().UTC().Add(defaultDeploymentTTL)
	err := s.applyMutationWithAudit("github:"+session.Subject, "deployment.retry", "deployment", deployment.ID, map[string]any{"expires_at": expiresAt}, func(tx state.Store) error {
		if err := tx.RetryDeploymentWithScopeLimit(deployment, expiresAt, s.cfg.DeploymentMaxConcurrent); err != nil {
			return err
		}
		return tx.AppendDeploymentEvent(state.DeploymentEvent{DeploymentID: deployment.ID, Attempt: deployment.Attempt, EventType: "lifecycle", Stage: "queued", Message: "Deployment queued for retry."})
	})
	if errors.Is(err, state.ErrDeploymentQuotaExceeded) {
		w.Header().Set("Retry-After", "60")
		writeErrorCode(w, http.StatusTooManyRequests, "deployment_quota_exceeded", "deployment scope concurrency limit reached")
		return
	}
	if errors.Is(err, state.ErrRetryNotAllowed) {
		writeError(w, http.StatusConflict, "deployment retry is not allowed in the current state")
		return
	}
	if errors.Is(err, state.ErrConflict) {
		writeError(w, http.StatusConflict, "deployment changed while retrying")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "retry deployment")
		return
	}
	updated, err := s.store.GetDeployment(deployment.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load deployment")
		return
	}
	s.signalDeployments()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusAccepted, deploymentResponseFromState(updated))
}

func (s *Server) refreshDeploymentPlanFromGitHub(ctx context.Context, accountID int64, deployment *state.Deployment) error {
	if deployment == nil || s.gh == nil {
		return errors.New("GitHub client is not configured")
	}
	if _, err := s.githubUserAccessToken(accountID); err != nil {
		return err
	}
	sourceSnapshot, err := s.resolveDeploymentSource(ctx, github.Repository{ID: deployment.RepositoryID, FullName: deployment.RepositoryFullName}, deployment.Ref, "")
	if err != nil {
		return err
	}
	commitSHA := sourceSnapshot.CommitSHA
	configData, err := s.gh.ReadRepositoryFile(ctx, deployment.RepositoryFullName, deployment.ConfigPath, commitSHA, deployconfig.MaxBytes)
	if err != nil {
		return err
	}
	spec, err := deployconfig.Parse(configData)
	if err != nil {
		return err
	}
	plan, err := deploy.NewPlan(deploy.ResolvedSource{SourceID: githubDeploymentSourceURL(deployment.RepositoryFullName), CommitSHA: commitSHA}, spec)
	if err != nil {
		return err
	}
	specJSON, err := json.Marshal(plan.Spec())
	if err != nil {
		return err
	}
	encryptedSpec, err := encryptSecret(string(specJSON), s.cfg.AuthEncryptionKey.Value())
	if err != nil {
		return err
	}
	deployment.SourceRepositoryID, deployment.SourceRepositoryFullName, deployment.SourceRef = sourceSnapshot.RepositoryID, sourceSnapshot.RepositoryFullName, sourceSnapshot.Ref
	deployment.PullRequestTitle = sourceSnapshot.PullRequestTitle
	deployment.CommitSHA = commitSHA
	deployment.SnapshotVersion = plan.Snapshot().Version
	deployment.SpecHash = plan.Snapshot().SpecHash
	deployment.SpecJSONEncrypted = encryptedSpec
	deployment.SandboxID = ""
	deployment.RuntimeID = ""
	deployment.ProcessID = ""
	deployment.ProcessTag = ""
	return nil
}

// Use 128 bits in a single-case, unpadded alphabet for compact URLs and
// consistent identity even with case-insensitive database collations.
func newDeploymentID(accountID int64, key string) string {
	var identity [16]byte
	if key == "" {
		if _, err := rand.Read(identity[:]); err != nil {
			panic(fmt.Sprintf("crypto/rand failed: %v", err))
		}
	} else {
		digest := sha256.Sum256(fmt.Appendf(nil, "deployment-request/v1:%d:%s", accountID, key))
		copy(identity[:], digest[:16])
	}
	return strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(identity[:]))
}

func (s *Server) writeDeploymentReplay(w http.ResponseWriter, id string, accountID, installationID, repositoryID int64, input createDeploymentRequest) bool {
	saved, err := s.store.GetDeployment(id)
	if errors.Is(err, state.ErrNotFound) {
		return false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load deployment")
		return true
	}
	if saved.AccountID != accountID || saved.GitHubInstallationID != installationID || saved.RepositoryID != repositoryID || saved.CreateRequestHash != deploymentCreateRequestHash(input, installationID, repositoryID) {
		writeErrorCode(w, http.StatusConflict, "deployment_request_conflict", "Idempotency-Key was already used for a different deployment request")
		return true
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, deploymentResponseFromState(saved))
	return true
}

// Hash only execution-affecting admission fields. Display snapshots do not
// change a request; refs are qualified by admission, and zero TTL/omitted
// config paths use their effective values.
// Historical rows have no original identity and must not infer one from mutable
// expiry or revision fields.
func deploymentCreateRequestHash(input createDeploymentRequest, installationID, repositoryID int64) string {
	ttl := input.TTLSeconds
	if ttl == 0 {
		ttl = int64(defaultDeploymentTTL / time.Second)
	}
	identity := struct {
		InstallationID int64
		RepositoryID   int64
		Ref            string
		CommitSHA      string
		ConfigPath     string
		TemplateID     string
		TTLSeconds     int64
	}{installationID, repositoryID, strings.TrimSpace(input.Ref), input.CommitSHA, normalizeDeploymentConfigPath(input.ConfigPath), strings.TrimSpace(input.TemplateID), ttl}
	encoded, _ := json.Marshal(identity)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func validateCreateDeploymentRequest(input createDeploymentRequest) error {
	if input.Source != "" && input.Source != "branch" && input.Source != "tag" && input.Source != "pull_request" {
		return errors.New("source must be branch, tag or pull_request")
	}
	if input.CommitSHA != "" && !validDeploymentSHA(input.CommitSHA) {
		return errors.New("commit_sha must be a full lowercase SHA")
	}
	if input.Source == "pull_request" {
		number, err := strconv.ParseInt(input.Ref, 10, 64)
		if err != nil || number <= 0 || strconv.FormatInt(number, 10) != input.Ref {
			return errors.New("pull request ref must be a positive number")
		}
	} else if input.Source == "" && strings.HasPrefix(input.Ref, "refs/pull/") {
		return errors.New("pull requests require source pull_request")
	}
	if len(input.TemplateID) > 256 || strings.IndexFunc(input.TemplateID, unicode.IsControl) >= 0 {
		return errors.New("template_id is invalid")
	}
	if len(input.TemplateName) > 256 || strings.IndexFunc(input.TemplateName, unicode.IsControl) >= 0 {
		return errors.New("template_name is invalid")
	}
	if input.TemplateCPU < 0 || input.TemplateMemoryMB < 0 || input.TemplateDiskMB < 0 {
		return errors.New("template resources are invalid")
	}
	if input.InstallationID <= 0 {
		return errors.New("installation_id is required")
	}
	parts := strings.Split(strings.TrimSpace(input.RepositoryFullName), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || strings.ContainsAny(input.RepositoryFullName, "\\?#%") || strings.IndexFunc(input.RepositoryFullName, unicode.IsSpace) >= 0 || strings.IndexFunc(input.RepositoryFullName, unicode.IsControl) >= 0 {
		return errors.New("repository_full_name must be owner/repository")
	}
	if strings.TrimSpace(input.Ref) == "" || len(normalizeDeploymentRef(input.Ref, input.Source)) > 256 || strings.IndexFunc(input.Ref, unicode.IsControl) >= 0 {
		return errors.New("ref must be non-empty and at most 256 bytes including its source prefix")
	}
	configPath := normalizeDeploymentConfigPath(input.ConfigPath)
	if configPath == "" {
		return errors.New("config_path is invalid")
	}
	if input.TTLSeconds < 0 || (input.TTLSeconds > 0 && (input.TTLSeconds < int64(minDeploymentTTL/time.Second) || input.TTLSeconds > int64(maxDeploymentTTL/time.Second))) {
		return fmt.Errorf("ttl_seconds must be between %d and %d", int(minDeploymentTTL/time.Second), int(maxDeploymentTTL/time.Second))
	}
	return nil
}

func normalizeDeploymentRef(ref, source string) string {
	ref = strings.TrimSpace(ref)
	switch source {
	case "branch":
		return "refs/heads/" + ref
	case "tag":
		return "refs/tags/" + ref
	case "pull_request":
		return "refs/pull/" + ref + "/head"
	default:
		return ref
	}
}

func normalizeDeploymentConfigPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return deployconfig.DefaultPath
	}
	if path.IsAbs(value) || len(value) > 1024 || strings.IndexFunc(value, unicode.IsControl) >= 0 || strings.Contains(value, "\\") {
		return ""
	}
	value = path.Clean(value)
	if value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return ""
	}
	return value
}

func deploymentRepositoryAccess(repositories []github.Repository, requested string, installation state.GitHubInstallation) (github.Repository, bool) {
	requested = strings.TrimSpace(requested)
	for _, repository := range repositories {
		if !strings.EqualFold(strings.TrimSpace(repository.FullName), requested) || repository.ID <= 0 {
			continue
		}
		if owner := strings.TrimSpace(installation.AccountLogin); owner != "" && !strings.EqualFold(owner, strings.TrimSpace(repository.Owner.Login)) {
			continue
		}
		if installation.GitHubAccountID > 0 && repository.Owner.ID > 0 && installation.GitHubAccountID != repository.Owner.ID {
			continue
		}
		return repository, true
	}
	return github.Repository{}, false
}

func deploymentResponseFromState(item state.Deployment) deploymentResponse {
	source, ref := "", item.Ref
	if strings.HasPrefix(ref, "refs/heads/") {
		source, ref = "branch", strings.TrimPrefix(ref, "refs/heads/")
	} else if strings.HasPrefix(ref, "refs/tags/") {
		source, ref = "tag", strings.TrimPrefix(ref, "refs/tags/")
	}
	if number := deploymentPullRequestNumber(ref); number > 0 {
		source, ref = "pull_request", strconv.FormatInt(number, 10)
	}
	return deploymentResponse{
		PullRequestNumber: deploymentPullRequestNumber(item.Ref), SourceRepositoryID: item.SourceRepositoryID, SourceRepositoryFullName: item.SourceRepositoryFullName, SourceRef: item.SourceRef, PullRequestTitle: item.PullRequestTitle,
		ID: item.ID, ScopeType: item.ScopeType, ScopeID: item.ScopeID,
		GitHubInstallationID: item.GitHubInstallationID, RepositoryID: item.RepositoryID,
		RepositoryFullName: item.RepositoryFullName, Ref: ref, Source: source, CommitSHA: item.CommitSHA,
		ConfigPath: item.ConfigPath, SnapshotVersion: item.SnapshotVersion, SpecHash: item.SpecHash,
		Status: item.Status, TemplateID: item.DeploymentTemplateID, TemplateName: item.DeploymentTemplateName, TemplateCPU: item.DeploymentTemplateCPU, TemplateMemoryMB: item.DeploymentTemplateMemoryMB, TemplateDiskMB: item.DeploymentTemplateDiskMB, SandboxID: item.SandboxID, SandboxConfigSource: item.SandboxConfigSource,
		SandboxRegion: item.SandboxRegion,
		Endpoint:      item.Endpoint, EndpointAccess: deploymentEndpointAccess(item.Endpoint), FailureStage: item.FailureStage, FailureReason: item.FailureReason,
		ExpiresAt: item.ExpiresAt, CreatedAt: item.CreatedAt, PlanningAt: item.PlanningAt,
		CreatingAt: item.CreatingAt, RunningAt: item.RunningAt, StoppingAt: item.StoppingAt,
		CompletedAt: item.CompletedAt, FailedAt: item.FailedAt, UpdatedAt: item.UpdatedAt,
		Version:        item.Version,
		RevisionNumber: max(item.RevisionNumber, 1),
	}
}

func deploymentRevisionResponseFromState(item state.DeploymentRevision) deploymentRevisionResponse {
	return deploymentRevisionResponse{PullRequestNumber: deploymentPullRequestNumber(item.Ref), SourceRepositoryID: item.SourceRepositoryID, SourceRepositoryFullName: item.SourceRepositoryFullName, SourceRef: item.SourceRef, PullRequestTitle: item.PullRequestTitle, ID: item.RevisionID, Number: item.RevisionNumber, Status: item.Status, Ref: item.Ref, CommitSHA: item.CommitSHA, ConfigPath: item.ConfigPath, TemplateID: item.DeploymentTemplateID, TemplateName: item.DeploymentTemplateName, TemplateCPU: item.DeploymentTemplateCPU, TemplateMemoryMB: item.DeploymentTemplateMemoryMB, TemplateDiskMB: item.DeploymentTemplateDiskMB, SandboxID: item.SandboxID, SandboxRegion: item.SandboxRegion, Endpoint: item.Endpoint, FailureStage: item.FailureStage, FailureReason: item.FailureReason, ExpiresAt: item.ExpiresAt, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt}
}

func deploymentEndpointAccess(endpoint string) string {
	if strings.TrimSpace(endpoint) == "" {
		return ""
	}
	return "public"
}
