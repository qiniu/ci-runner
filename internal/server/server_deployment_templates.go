package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

// Owners may browse their scoped catalog. Repository members receive only
// runnable public templates for deployment, never the private provider catalog.
func (s *Server) handleUserDeploymentTemplates(w http.ResponseWriter, r *http.Request) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	installationID, err := strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64)
	if err != nil || installationID <= 0 {
		writeError(w, http.StatusBadRequest, "invalid installation_id")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	r = r.WithContext(ctx)
	installation, found, err := s.githubInstallationForAccount(account.ID, installationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load GitHub installation")
		return
	}
	if !found {
		writeErrorCode(w, http.StatusNotFound, "github_installation_not_found", "GitHub installation is not linked to this account")
		return
	}
	scope, ok := s.deploymentScope(w, account.ID, installation)
	if !ok {
		return
	}
	manageable, err := s.accountPreferenceScopeManageable(ctx, account.ID, scope)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	if !manageable {
		access, err := s.userAuthorizedRepositoryAccess(ctx, account.ID)
		if err != nil {
			s.writeUserRepositoryAuthorizationError(w, err)
			return
		}
		if !hasRepositoryAccess(access, installation.InstallationID, r.URL.Query().Get("repository_full_name")) {
			writeErrorCode(w, http.StatusForbidden, "repository_access_forbidden", "repository access is required for deployment templates")
			return
		}
	}
	service, snapshot, err := s.sandboxServiceForDeployment(ctx, scope)
	if err != nil {
		writeErrorCode(w, http.StatusConflict, "deployment_sandbox_not_configured", "Configure Sandbox credentials in this account or organization's Preferences before deploying")
		return
	}
	var items []sandboxrunner.CatalogTemplate
	if manageable {
		items, err = deploymentTemplates(ctx, service)
	} else {
		items, err = publicDeploymentTemplates(ctx, service)
	}
	if err != nil {
		writeErrorCode(w, http.StatusBadGateway, "deployment_templates_unavailable", "Unable to load available deployment templates")
		return
	}
	if !manageable {
		w.Header().Set("Cache-Control", "no-store")
		// Only public template choices; exclude private catalogs and scope metadata.
		writeJSON(w, http.StatusOK, map[string]any{"templates": publicDeploymentTemplateOptions(items)})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"templates": items, "default_template_id": s.cfg.DeploymentTemplateID,
		"region": s.sandboxRegionForAPIURL(snapshot.APIURL),
	})
}

func (s *Server) deploymentScope(w http.ResponseWriter, accountID int64, installation state.GitHubInstallation) (accountPreferenceScope, bool) {
	scope := accountPreferenceScope{Type: state.AccountScopeTypeAccount, ID: accountID}
	switch strings.ToLower(strings.TrimSpace(installation.AccountType)) {
	case "organization":
		scope = accountPreferenceScope{Type: state.AccountScopeTypeGitHubInstall, ID: installation.InstallationID}
	case "user":
	default:
		writeErrorCode(w, http.StatusForbidden, "deployment_scope_forbidden", "GitHub installation ownership must be synchronized before deploying")
		return scope, false
	}
	return scope, true
}

func deploymentTemplates(ctx context.Context, service sandboxrunner.Service) ([]sandboxrunner.CatalogTemplate, error) {
	owned, ok := service.(sandboxrunner.Catalog)
	if !ok {
		return nil, errors.New("sandbox catalog unavailable")
	}
	public, ok := service.(sandboxrunner.DefaultTemplateCatalog)
	if !ok {
		return nil, errors.New("public Sandbox catalog unavailable")
	}
	items, err := owned.ListTemplates(ctx)
	if err != nil {
		return nil, err
	}
	defaults, err := public.ListDefaultTemplates(ctx)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]sandboxrunner.CatalogTemplate)
	for _, item := range append(items, defaults...) {
		if !item.Runnable() {
			continue
		}
		if previous, exists := byID[item.TemplateID]; exists {
			item.Aliases = append(item.Aliases, previous.Aliases...)
		}
		byID[item.TemplateID] = item
	}
	result := make([]sandboxrunner.CatalogTemplate, 0, len(byID))
	for _, item := range byID {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TemplateID < result[j].TemplateID })
	return result, nil
}

// Only the provider's public directory can establish a non-owner template choice.
func publicDeploymentTemplates(ctx context.Context, service sandboxrunner.Service) ([]sandboxrunner.CatalogTemplate, error) {
	catalog, ok := service.(sandboxrunner.DefaultTemplateCatalog)
	if !ok {
		return nil, errors.New("public Sandbox catalog unavailable")
	}
	return catalog.ListDefaultTemplates(ctx)
}

type publicDeploymentTemplateOption struct {
	TemplateID string   `json:"template_id"`
	Names      []string `json:"names"`
}

func publicDeploymentTemplateOptions(items []sandboxrunner.CatalogTemplate) []publicDeploymentTemplateOption {
	result := []publicDeploymentTemplateOption{}
	for _, item := range items {
		if item.Public && item.Runnable() {
			result = append(result, publicDeploymentTemplateOption{TemplateID: item.TemplateID, Names: item.Names})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].TemplateID < result[j].TemplateID })
	return result
}

func (s *Server) authorizeDeploymentTemplate(w http.ResponseWriter, r *http.Request, accountID int64, scope accountPreferenceScope, service sandboxrunner.Service, template string) bool {
	manageable, err := s.accountPreferenceScopeManageable(r.Context(), accountID, scope)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return false
	}
	if manageable {
		return true
	}
	items, err := publicDeploymentTemplates(r.Context(), service)
	if err != nil {
		writeErrorCode(w, http.StatusBadGateway, "deployment_templates_unavailable", "Unable to load available deployment templates")
		return false
	}
	for _, item := range publicDeploymentTemplateOptions(items) {
		if item.TemplateID == template {
			return true
		}
	}
	writeErrorCode(w, http.StatusForbidden, "deployment_template_forbidden", "Select a public template for this deployment")
	return false
}
