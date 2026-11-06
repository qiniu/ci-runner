package server

import (
	"net/http"

	"github.com/qiniu/ci-runner/internal/runnercatalog"
)

func (s *Server) handlePublicRunnerTemplates(w http.ResponseWriter, _ *http.Request) {
	profiles, err := s.store.ListProfiles()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list public runner templates")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60")
	writeJSON(w, http.StatusOK, runnercatalog.PublicTemplates(profiles))
}
