package server

import (
	"context"
	"net/http"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/qiniu/ci-runner/internal/runnercatalog"
)

// Only overlapping origin reads share a result. The next read after completion
// sees a fresh database view, including mutations committed by another server.
func (s *Server) publicTemplatesRead() <-chan singleflight.Result {
	return s.publicTemplatesGroup.DoChan("catalog", func() (any, error) {
		parent := s.loopCtx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		profiles, err := s.store.ListProfilesContext(ctx)
		if err != nil {
			return nil, err
		}
		return runnercatalog.PublicTemplates(profiles), nil
	})
}

func (s *Server) handlePublicRunnerTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() != nil {
		return
	}
	select {
	case <-r.Context().Done():
		return
	case result := <-s.publicTemplatesRead():
		if result.Err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list public runner templates")
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=60")
		writeJSON(w, http.StatusOK, result.Val)
	}
}
