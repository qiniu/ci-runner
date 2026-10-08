package server

import (
	"net/http"
	"time"

	"github.com/qiniu/ci-runner/internal/runnercatalog"
)

const publicTemplatesCacheTTL = time.Minute

type cachedPublicTemplates struct {
	templates []runnercatalog.PublicTemplate
	expiresAt time.Time
}

func (s *Server) handlePublicRunnerTemplates(w http.ResponseWriter, _ *http.Request) {
	templates, err := s.publicRunnerTemplates()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list public runner templates")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, templates)
}

func (s *Server) publicRunnerTemplates() ([]runnercatalog.PublicTemplate, error) {
	// Serialize cache fills with invalidation so a read started before an Admin
	// save cannot restore an old snapshot after that save invalidates it.
	s.publicTemplatesMu.Lock()
	defer s.publicTemplatesMu.Unlock()
	if time.Now().Before(s.publicTemplatesCache.expiresAt) {
		return s.publicTemplatesCache.templates, nil
	}
	profiles, err := s.store.ListProfiles()
	if err != nil {
		return nil, err
	}
	templates := runnercatalog.PublicTemplates(profiles)
	s.publicTemplatesCache = cachedPublicTemplates{
		templates: templates,
		expiresAt: time.Now().Add(publicTemplatesCacheTTL),
	}
	return templates, nil
}

func (s *Server) invalidatePublicRunnerTemplates() {
	s.publicTemplatesMu.Lock()
	s.publicTemplatesCache = cachedPublicTemplates{}
	s.publicTemplatesMu.Unlock()
}
