package server

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

// The form resolves a name or physical ID without treating reference type as
// visibility. Mutations independently validate the resulting binding before save.
func (s *Server) handleProfileTemplateStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdminAuth(w, r) {
		return
	}
	reference := strings.TrimSpace(r.URL.Query().Get("template"))
	kind := r.URL.Query().Get("reference_type")
	if reference == "" || (kind != "" && kind != "id" && kind != "name") {
		writeError(w, http.StatusBadRequest, "a template name or ID is required")
		return
	}
	svc, ok := s.adminProfileTemplateService(w)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), profileTemplateValidationTimeout)
	defer cancel()
	type response struct {
		sandboxrunner.TemplateInspection
		TemplateSource      string `json:"template_source"`
		TemplateID          string `json:"template_id"`
		DefaultTemplateName string `json:"default_template_name"`
	}
	// Exact IDs take precedence; a failed detail lookup is not evidence of privacy.
	if kind != "name" {
		inspector, ok := svc.(sandboxrunner.TemplateInspector)
		if !ok {
			writeErrorCode(w, http.StatusServiceUnavailable, "template_state_unavailable", "The admin Sandbox service cannot verify template visibility")
			return
		}
		info, err := inspector.InspectTemplate(ctx, reference)
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			writeJSON(w, http.StatusOK, response{TemplateInspection: info, TemplateSource: state.TemplateSourcePrivate, TemplateID: reference})
			return
		}
		if kind == "id" || !errors.Is(err, sandboxrunner.ErrTemplateNotFound) {
			writeAdminTemplateError(w, err)
			return
		}
	}
	catalog, ok := svc.(sandboxrunner.DefaultTemplateCatalog)
	if !ok {
		writeErrorCode(w, http.StatusServiceUnavailable, "public_template_catalog_unavailable", "The admin Sandbox service cannot list public templates")
		return
	}
	templates, err := catalog.ListDefaultTemplates(ctx)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		writeAdminTemplateError(w, err)
		return
	}
	match, err := findDefaultTemplate(reference, templates)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "public_template_invalid", "Template name must resolve uniquely in the admin Sandbox catalog")
		return
	}
	writeJSON(w, http.StatusOK, response{TemplateInspection: sandboxrunner.TemplateInspection{Public: match.Public, Runnable: match.Runnable(), Template: match.Details()}, TemplateSource: state.TemplateSourcePublic, DefaultTemplateName: reference})
}
