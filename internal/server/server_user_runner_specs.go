package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

type userRunnerSpecResponse struct {
	Name                string   `json:"name"`
	Source              string   `json:"source"`
	WorkflowLabels      []string `json:"workflow_labels"`
	TemplateID          string   `json:"template_id,omitempty"`
	DefaultTemplateName string   `json:"default_template_name,omitempty"`
	RunnerGroup         string   `json:"runner_group,omitempty"`
	Enabled             bool     `json:"enabled"`
	MaxConcurrency      int      `json:"max_concurrency"`
	OverridesGlobal     bool     `json:"overrides_global"`
	UpdatedAt           string   `json:"updated_at"`
}

type userRunnerSpecListResponse struct {
	ScopeType     string                   `json:"scope_type"`
	ScopeID       int64                    `json:"scope_id"`
	SandboxSource string                   `json:"sandbox_source"`
	SandboxRegion string                   `json:"sandbox_region,omitempty"`
	Items         []userRunnerSpecResponse `json:"items"`
}

type userRunnerSpecMutationRequest struct {
	Name              string   `json:"name"`
	WorkflowLabels    []string `json:"workflow_labels"`
	TemplateID        string   `json:"template_id"`
	RunnerGroup       string   `json:"runner_group"`
	MaxConcurrency    int      `json:"max_concurrency"`
	Enabled           bool     `json:"enabled"`
	ExpectedUpdatedAt string   `json:"expected_updated_at"`
}

type userRunnerSpecPatchRequest struct {
	WorkflowLabels    *[]string `json:"workflow_labels"`
	TemplateID        *string   `json:"template_id"`
	RunnerGroup       *string   `json:"runner_group"`
	MaxConcurrency    *int      `json:"max_concurrency"`
	Enabled           *bool     `json:"enabled"`
	ExpectedUpdatedAt string    `json:"expected_updated_at"`
}

var errRunnerSpecInUse = errors.New("runner spec is in use")

func (s *Server) userRunnerScope(w http.ResponseWriter, r *http.Request, accountID int64) (state.RunnerProfileScope, accountPreferenceScope, bool) {
	prefScope, err := s.accountPreferenceScopeFromRequest(accountID, r)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "runner_spec_scope_invalid", err.Error())
		return state.RunnerProfileScope{}, accountPreferenceScope{}, false
	}
	manageable, err := s.accountPreferenceScopeManageable(r.Context(), accountID, prefScope)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return state.RunnerProfileScope{}, accountPreferenceScope{}, false
	}
	if !manageable {
		writeErrorCode(w, http.StatusForbidden, "runner_spec_scope_forbidden", "Runner specs for this scope are managed by its owner")
		return state.RunnerProfileScope{}, accountPreferenceScope{}, false
	}
	profileScope := state.RunnerProfileScope{Type: prefScope.Type, ID: prefScope.ID}
	if prefScope.Type == state.AccountScopeTypeGitHubInstall {
		canonical, ok, err := s.runnerProfileScopeForInstallation(prefScope.ID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return state.RunnerProfileScope{}, accountPreferenceScope{}, false
		}
		if ok {
			profileScope = canonical
			if canonical.Type == state.RunnerProfileScopeAccount {
				prefScope = accountPreferenceScope{Type: state.AccountScopeTypeAccount, ID: canonical.ID}
			}
		}
	}
	return profileScope, prefScope, true
}

func (s *Server) handleUserListRunnerSpecs(w http.ResponseWriter, r *http.Request) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	scope, prefScope, ok := s.userRunnerScope(w, r, account.ID)
	if !ok {
		return
	}
	s.writeUserRunnerSpecList(w, scope, prefScope)
}

func (s *Server) writeUserRunnerSpecList(w http.ResponseWriter, scope state.RunnerProfileScope, prefScope accountPreferenceScope) {
	items, err := s.store.ListEffectiveProfiles(scope)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list runner specs")
		return
	}
	sandboxSource := "none"
	sandboxRegion := ""
	if _, snapshot, err := s.sandboxServiceForScope(prefScope); err == nil {
		sandboxSource = snapshot.Source
		sandboxRegion = s.sandboxRegionForAPIURL(snapshot.APIURL)
	}
	response := userRunnerSpecListResponse{ScopeType: scope.Type, ScopeID: scope.ID, SandboxSource: sandboxSource, SandboxRegion: sandboxRegion, Items: make([]userRunnerSpecResponse, 0, len(items))}
	for _, item := range items {
		responseItem := userRunnerSpecResponse{Name: item.Profile.Name, Source: item.Source, WorkflowLabels: append([]string(nil), item.WorkflowLabels...), DefaultTemplateName: item.Profile.DefaultTemplateName, Enabled: item.Profile.Enabled, MaxConcurrency: item.Profile.MaxConcurrency, OverridesGlobal: item.OverridesGlobal, UpdatedAt: item.Profile.UpdatedAt.UTC().Format(time.RFC3339Nano)}
		if item.Source == "scoped_custom" {
			responseItem.TemplateID = item.Profile.TemplateID
			responseItem.RunnerGroup = item.Profile.RunnerGroup
		}
		response.Items = append(response.Items, responseItem)
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleUserCreateRunnerSpec(w http.ResponseWriter, r *http.Request) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	scope, prefScope, ok := s.userRunnerScope(w, r, account.ID)
	if !ok {
		return
	}
	var input userRunnerSpecMutationRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_runner_spec", "invalid runner spec payload")
		return
	}
	if scope.Type == state.AccountScopeTypeAccount && strings.TrimSpace(input.RunnerGroup) != "" {
		writeErrorCode(w, http.StatusBadRequest, "runner_group_not_supported", "runner group is only supported for organization scopes")
		return
	}
	labels, _, err := state.NormalizeWorkflowLabels(input.WorkflowLabels)
	name := strings.TrimSpace(input.Name)
	if err != nil || !validUserRunnerSpecName(name) || strings.TrimSpace(input.TemplateID) == "" || input.MaxConcurrency < 0 {
		writeErrorCode(w, http.StatusBadRequest, "invalid_runner_spec", "invalid runner spec payload")
		return
	}
	if err := s.validateScopedProfileTemplate(r.Context(), prefScope, input.TemplateID); err != nil {
		s.writeScopedTemplateValidationError(w, err)
		return
	}
	profile := state.ScopedRunnerProfile{ScopeType: scope.Type, ScopeID: scope.ID, Name: name, WorkflowLabels: labels, TemplateID: strings.TrimSpace(input.TemplateID), RunnerGroup: strings.TrimSpace(input.RunnerGroup), MaxConcurrency: input.MaxConcurrency, Enabled: input.Enabled}
	// The insert is the publication point for a new spec. An admission racing
	// creation may be ordered before or after it, and cannot reference stale
	// state for a spec that did not previously exist.
	s.admissionMu.Lock()
	err = s.applyMutationWithAudit("github:"+session.Subject, "user_runner_spec.create", "scoped_runner_profile", fmt.Sprintf("%s:%d:%s", scope.Type, scope.ID, profile.Name), map[string]any{"template_id": profile.TemplateID, "workflow_labels": labels}, func(tx state.Store) error {
		_, err := tx.UpsertScopedProfileIfUnchanged(profile, nil)
		return err
	})
	s.admissionMu.Unlock()
	if err != nil {
		if errors.Is(err, state.ErrRunnerProfileNameConflict) {
			writeErrorCode(w, http.StatusConflict, "runner_spec_name_conflict", "runner spec name conflicts with an enabled platform spec")
			return
		}
		if errors.Is(err, state.ErrRunnerProfileLabelsConflict) {
			writeErrorCode(w, http.StatusConflict, "runner_spec_labels_conflict", "a runner spec with these labels already exists")
			return
		}
		if errors.Is(err, state.ErrConflict) {
			writeErrorCode(w, http.StatusConflict, "runner_spec_name_conflict", "a runner spec with this name already exists")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeUserRunnerSpecList(w, scope, prefScope)
}

func (s *Server) handleUserPatchRunnerSpec(w http.ResponseWriter, r *http.Request) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	scope, prefScope, ok := s.userRunnerScope(w, r, account.ID)
	if !ok {
		return
	}
	var input userRunnerSpecPatchRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_runner_spec", "invalid runner spec payload")
		return
	}
	expected, ok := parseRunnerSpecRevision(w, input.ExpectedUpdatedAt, true)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	current, err := s.store.GetScopedProfile(scope, name)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeErrorCode(w, http.StatusNotFound, "runner_spec_not_found", "runner spec not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	templateChanged := input.TemplateID != nil && strings.TrimSpace(*input.TemplateID) != current.TemplateID
	labelsChanged := false
	if input.WorkflowLabels != nil {
		labels, _, labelErr := state.NormalizeWorkflowLabels(*input.WorkflowLabels)
		if labelErr != nil {
			writeErrorCode(w, http.StatusBadRequest, "invalid_runner_spec", labelErr.Error())
			return
		}
		labelsChanged = !sameStringSlice(labels, current.WorkflowLabels)
		current.WorkflowLabels = labels
	}
	runnerGroupChanged := false
	if input.RunnerGroup != nil {
		if scope.Type == state.AccountScopeTypeAccount && strings.TrimSpace(*input.RunnerGroup) != "" {
			writeErrorCode(w, http.StatusBadRequest, "runner_group_not_supported", "runner group is only supported for organization scopes")
			return
		}
		nextRunnerGroup := strings.TrimSpace(*input.RunnerGroup)
		runnerGroupChanged = nextRunnerGroup != current.RunnerGroup
		current.RunnerGroup = nextRunnerGroup
	}
	if labelsChanged || templateChanged || runnerGroupChanged {
		count, countErr := s.store.ActiveCountForProfileScope("scoped_custom", scope, name)
		if countErr != nil {
			writeError(w, http.StatusInternalServerError, countErr.Error())
			return
		}
		if count > 0 {
			writeErrorCode(w, http.StatusConflict, "runner_spec_in_use", "runner spec cannot change while active requests use it")
			return
		}
	}
	if templateChanged {
		if err := s.validateScopedProfileTemplate(r.Context(), prefScope, *input.TemplateID); err != nil {
			s.writeScopedTemplateValidationError(w, err)
			return
		}
		current.TemplateID = strings.TrimSpace(*input.TemplateID)
	}
	if input.MaxConcurrency != nil && *input.MaxConcurrency < 0 {
		writeErrorCode(w, http.StatusBadRequest, "invalid_runner_spec", "max concurrency must not be negative")
		return
	}
	if input.MaxConcurrency != nil {
		current.MaxConcurrency = *input.MaxConcurrency
	}
	if input.Enabled != nil {
		current.Enabled = *input.Enabled
	}
	s.admissionMu.Lock()
	err = s.applyMutationWithAudit("github:"+session.Subject, "user_runner_spec.update", "scoped_runner_profile", fmt.Sprintf("%s:%d:%s", scope.Type, scope.ID, name), map[string]any{"template_id_changed": templateChanged, "workflow_labels_changed": labelsChanged, "runner_group_changed": runnerGroupChanged}, func(tx state.Store) error {
		if labelsChanged || templateChanged || runnerGroupChanged {
			count, countErr := tx.ActiveCountForProfileScope("scoped_custom", scope, name)
			if countErr != nil {
				return countErr
			}
			if count > 0 {
				return errRunnerSpecInUse
			}
		}
		_, err := tx.UpsertScopedProfileIfUnchanged(current, &expected)
		return err
	})
	s.admissionMu.Unlock()
	if err != nil {
		if errors.Is(err, errRunnerSpecInUse) {
			writeErrorCode(w, http.StatusConflict, "runner_spec_in_use", "runner spec cannot change while active requests use it")
			return
		}
		if errors.Is(err, state.ErrRunnerProfileLabelsConflict) {
			writeErrorCode(w, http.StatusConflict, "runner_spec_labels_conflict", "a runner spec with these labels already exists")
			return
		}
		if errors.Is(err, state.ErrConflict) {
			writeErrorCode(w, http.StatusConflict, "runner_spec_conflict", "Runner spec changed while saving; refresh and try again")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeUserRunnerSpecList(w, scope, prefScope)
}

func validUserRunnerSpecName(name string) bool {
	return name != "" && !strings.Contains(name, "/") && name != "." && name != ".."
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Server) handleUserDeleteRunnerSpec(w http.ResponseWriter, r *http.Request) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	scope, prefScope, ok := s.userRunnerScope(w, r, account.ID)
	if !ok {
		return
	}
	expected, ok := parseRunnerSpecRevision(w, r.URL.Query().Get("expected_updated_at"), true)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if _, err := s.store.GetScopedProfile(scope, name); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeErrorCode(w, http.StatusNotFound, "runner_spec_not_found", "runner spec not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if count, err := s.store.ActiveCountForProfileScope("scoped_custom", scope, name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	} else if count > 0 {
		writeErrorCode(w, http.StatusConflict, "runner_spec_in_use", "runner spec is in use by active requests")
		return
	}
	s.admissionMu.Lock()
	err := s.applyMutationWithAudit("github:"+session.Subject, "user_runner_spec.delete", "scoped_runner_profile", fmt.Sprintf("%s:%d:%s", scope.Type, scope.ID, name), nil, func(tx state.Store) error {
		count, countErr := tx.ActiveCountForProfileScope("scoped_custom", scope, name)
		if countErr != nil {
			return countErr
		}
		if count > 0 {
			return errRunnerSpecInUse
		}
		return tx.DeleteScopedProfileIfUnchanged(scope, name, &expected)
	})
	s.admissionMu.Unlock()
	if err != nil {
		if errors.Is(err, errRunnerSpecInUse) {
			writeErrorCode(w, http.StatusConflict, "runner_spec_in_use", "runner spec is in use by active requests")
			return
		}
		if errors.Is(err, state.ErrConflict) {
			writeErrorCode(w, http.StatusConflict, "runner_spec_conflict", "Runner spec changed while deleting; refresh and try again")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeUserRunnerSpecList(w, scope, prefScope)
}

func parseRunnerSpecRevision(w http.ResponseWriter, value string, required bool) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		if required {
			writeErrorCode(w, http.StatusBadRequest, "invalid_runner_spec_revision", "expected_updated_at is required")
			return time.Time{}, false
		}
		return time.Time{}, true
	}
	revision, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_runner_spec_revision", "expected_updated_at must be RFC3339")
		return time.Time{}, false
	}
	return revision, true
}

func (s *Server) validateScopedProfileTemplate(ctx context.Context, scope accountPreferenceScope, templateID string) error {
	_, snapshot, err := s.sandboxServiceForScope(scope)
	if err != nil {
		return errSandboxServiceNotConfigured
	}
	svc, err := s.sandboxServiceForConfig(snapshot)
	if err != nil {
		return err
	}
	validateCtx, cancel := context.WithTimeout(ctx, profileTemplateValidationTimeout)
	defer cancel()
	err = svc.ValidateTemplate(validateCtx, strings.TrimSpace(templateID))
	if err == nil {
		return validateCtx.Err()
	}
	return err
}

func (s *Server) writeScopedTemplateValidationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errSandboxServiceNotConfigured):
		writeErrorCode(w, http.StatusConflict, "sandbox_service_not_configured", "configure Sandbox credentials for this scope before creating a custom runner spec")
	case errors.Is(err, sandboxrunner.ErrTemplateNotFound):
		writeErrorCode(w, http.StatusBadRequest, "template_not_found", "template was not found in the scope Sandbox service")
	case errors.Is(err, sandboxrunner.ErrTemplateNotReady):
		writeErrorCode(w, http.StatusBadRequest, "template_not_ready", "template is not ready")
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		writeErrorCode(w, http.StatusGatewayTimeout, "template_validation_timeout", "template validation timed out")
	default:
		writeErrorCode(w, http.StatusBadGateway, "template_validation_unavailable", "could not validate template with the scope Sandbox service")
	}
}
