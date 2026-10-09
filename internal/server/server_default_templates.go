package server

import (
	"fmt"
	"strings"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
)

const (
	defaultTemplateResolutionReasonMissing     = "missing"
	defaultTemplateResolutionReasonDuplicate   = "duplicate"
	defaultTemplateResolutionReasonPrivate     = "private"
	defaultTemplateResolutionReasonNonRunnable = "non_runnable"
	defaultTemplateResolutionReasonEmptyID     = "empty_template_id"
	defaultTemplateResolutionReasonNoCatalog   = "catalog_unavailable"
)

type defaultTemplateResolutionError struct {
	RequestedName string
	Reason        string
}

func (e *defaultTemplateResolutionError) Error() string {
	return fmt.Sprintf("default template %q cannot be resolved: %s", e.RequestedName, e.Reason)
}

func findDefaultTemplate(requestedName string, templates []sandboxrunner.CatalogTemplate) (sandboxrunner.CatalogTemplate, error) {
	requestedName = strings.TrimSpace(requestedName)
	if requestedName == "" {
		return sandboxrunner.CatalogTemplate{}, newDefaultTemplateResolutionError(requestedName, defaultTemplateResolutionReasonMissing)
	}
	matches := make([]sandboxrunner.CatalogTemplate, 0, 1)
	for _, template := range templates {
		for _, name := range template.Names {
			name = strings.TrimSpace(name)
			if name == requestedName || strings.HasSuffix(name, "/"+requestedName) {
				matches = append(matches, template)
				break
			}
		}
	}

	if len(matches) == 0 {
		return sandboxrunner.CatalogTemplate{}, newDefaultTemplateResolutionError(requestedName, defaultTemplateResolutionReasonMissing)
	}
	if len(matches) > 1 {
		return sandboxrunner.CatalogTemplate{}, newDefaultTemplateResolutionError(requestedName, defaultTemplateResolutionReasonDuplicate)
	}

	return matches[0], nil
}

func resolveDefaultTemplateID(requestedName string, templates []sandboxrunner.CatalogTemplate) (string, error) {
	requestedName = strings.TrimSpace(requestedName)
	match, err := findDefaultTemplate(requestedName, templates)
	if err != nil {
		return "", err
	}
	if !match.Public {
		return "", newDefaultTemplateResolutionError(requestedName, defaultTemplateResolutionReasonPrivate)
	}
	templateID := strings.TrimSpace(match.TemplateID)
	if templateID == "" {
		return "", newDefaultTemplateResolutionError(requestedName, defaultTemplateResolutionReasonEmptyID)
	}
	if !match.Runnable() {
		return "", newDefaultTemplateResolutionError(requestedName, defaultTemplateResolutionReasonNonRunnable)
	}
	return templateID, nil
}

func newDefaultTemplateResolutionError(requestedName, reason string) error {
	return &defaultTemplateResolutionError{
		RequestedName: requestedName,
		Reason:        reason,
	}
}
