package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
	qnsandbox "github.com/qiniu/go-sdk/v7/sandbox"
)

const profileTemplateValidationTimeout = 5 * time.Second

// validateAdminProfileTemplateBinding runs before the audited transaction.
// Admin credentials validate bindings independently of runtime fallback controls.
// Callers validate local fields first; provider responses never reach clients.
func (s *Server) validateAdminProfileTemplateBinding(w http.ResponseWriter, r *http.Request, profile state.RunnerProfile) bool {
	defaultConfig, err := s.store.GetSandboxServiceDefault()
	if err != nil && !errors.Is(err, state.ErrNotFound) {
		writeErrorCode(w, http.StatusInternalServerError, "sandbox_service_config_error", "Cannot load the admin Sandbox service configuration")
		return false
	}
	if strings.TrimSpace(defaultConfig.APIURL) == "" || strings.TrimSpace(defaultConfig.APIKeyEncrypted) == "" {
		writeErrorCode(w, http.StatusConflict, "sandbox_service_not_configured", "Configure the admin Sandbox service before creating a Runner Spec, publishing it, or changing its template")
		return false
	}
	svc, err := s.sandboxServiceForConfig(sandboxServiceConfigSnapshot{
		APIURL: defaultConfig.APIURL, EncryptedAPIKey: defaultConfig.APIKeyEncrypted,
		Source: sandboxConfigSourceAdminDefault,
	})
	if err != nil {
		writeErrorCode(w, http.StatusInternalServerError, "sandbox_service_config_error", "Cannot initialize the admin Sandbox service; check its endpoint and credentials")
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), profileTemplateValidationTimeout)
	defer cancel()
	if profile.TemplateSource == state.TemplateSourcePublic {
		catalog, ok := svc.(sandboxrunner.DefaultTemplateCatalog)
		if !ok {
			writeErrorCode(w, http.StatusServiceUnavailable, "public_template_catalog_unavailable", "The admin Sandbox service cannot list public templates")
			return false
		}
		var templates []sandboxrunner.CatalogTemplate
		templates, err = catalog.ListDefaultTemplates(ctx)
		if err == nil {
			_, err = resolveDefaultTemplateID(profile.DefaultTemplateName, templates)
			if err != nil {
				writeErrorCode(w, http.StatusBadRequest, "public_template_invalid", "Public template name must resolve uniquely to a public, runnable template")
				return false
			}
		}
	} else {
		err = svc.ValidateTemplate(ctx, profile.TemplateID)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		return true
	}
	// Never forward provider bodies or transport errors: they may contain
	// credentials or other information outside the admin template contract.
	var apiErr *qnsandbox.APIError
	status, code, message := http.StatusBadGateway, "template_validation_unavailable", "Cannot validate the template with the admin Sandbox service; try again"
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		status, code, message = http.StatusGatewayTimeout, "template_validation_timeout", "Template validation timed out or was canceled; try again"
	case errors.Is(err, sandboxrunner.ErrTemplateNotFound):
		status, code, message = http.StatusBadRequest, "template_not_found", "Template was not found in the admin Sandbox service"
	case errors.Is(err, sandboxrunner.ErrTemplateNotReady):
		status, code, message = http.StatusBadRequest, "template_not_ready", "Template has no usable default build in the admin Sandbox service"
	case errors.Is(err, sandboxrunner.ErrTemplateStateUnavailable):
		code, message = "template_state_unavailable", "Template exists, but its usable default build cannot be confirmed in the owned or public default catalog"
	case errors.As(err, &apiErr) && (apiErr.StatusCode == http.StatusUnauthorized || apiErr.StatusCode == http.StatusForbidden):
		code, message = "sandbox_template_access_denied", "The admin Sandbox credentials cannot access this template; check the API key and template permissions"
	}
	writeErrorCode(w, status, code, message)
	return false
}

func writeProfileConflict(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, state.ErrConflict) {
		return false
	}
	writeErrorCode(w, http.StatusConflict, "runner_spec_conflict", "Runner Spec changed while saving; refresh and try again")
	return true
}

func profileExecutionChanged(a, b state.RunnerProfile) bool {
	return !sameStringSlice(a.Labels, b.Labels) || !sameStringSlice(a.RequiredLabels, b.RequiredLabels) || a.TemplateSource != b.TemplateSource || a.TemplateID != b.TemplateID || a.DefaultTemplateName != b.DefaultTemplateName || a.RunnerGroup != b.RunnerGroup || a.RunnerUpdatePolicy != b.RunnerUpdatePolicy || a.RequireDocker != b.RequireDocker || a.ForkSponsorship != b.ForkSponsorship
}

func writeProfileInUse(w http.ResponseWriter, err error) bool {
	if !errors.Is(err, errRunnerSpecInUse) {
		return false
	}
	writeErrorCode(w, http.StatusConflict, "runner_spec_in_use", "Runner Spec cannot change its execution settings or be deleted while active requests use it")
	return true
}
