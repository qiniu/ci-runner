package server

import (
	"fmt"
	"net/http"
	"strconv"
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
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(publicTemplatesCacheTTL.Seconds())))
	writeJSON(w, http.StatusOK, templates)
}

func (s *Server) publicRunnerTemplates() ([]runnercatalog.PublicTemplate, error) {
	for {
		s.publicTemplatesMu.RLock()
		cached, epoch := s.publicTemplatesCache, s.publicTemplatesEpoch
		s.publicTemplatesMu.RUnlock()
		if time.Now().Before(cached.expiresAt) {
			return cached.templates, nil
		}
		// Coalesce fills per epoch without holding a cache lock during database
		// I/O. An Admin save starts a new epoch, so new readers do not wait on
		// an old fill and that fill cannot republish an invalidated snapshot.
		value, err, _ := s.publicTemplatesGroup.Do(strconv.FormatUint(epoch, 10), func() (any, error) {
			s.publicTemplatesMu.RLock()
			cached, currentEpoch := s.publicTemplatesCache, s.publicTemplatesEpoch
			s.publicTemplatesMu.RUnlock()
			if currentEpoch != epoch {
				return nil, nil
			}
			if time.Now().Before(cached.expiresAt) {
				return cached.templates, nil
			}
			profiles, err := s.store.ListProfiles()
			if err != nil {
				return nil, err
			}
			templates := runnercatalog.PublicTemplates(profiles)
			s.publicTemplatesMu.Lock()
			defer s.publicTemplatesMu.Unlock()
			if s.publicTemplatesEpoch != epoch {
				return nil, nil
			}
			s.publicTemplatesCache = cachedPublicTemplates{
				templates: templates,
				expiresAt: time.Now().Add(publicTemplatesCacheTTL),
			}
			return templates, nil
		})
		if err != nil {
			return nil, err
		}
		// A discarded fill retries against the new epoch.
		if value == nil {
			continue
		}
		return value.([]runnercatalog.PublicTemplate), nil
	}
}

func (s *Server) invalidatePublicRunnerTemplates() {
	s.publicTemplatesMu.Lock()
	s.publicTemplatesCache = cachedPublicTemplates{}
	s.publicTemplatesEpoch++
	s.publicTemplatesMu.Unlock()
}
