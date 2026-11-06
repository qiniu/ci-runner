package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/qiniu/ci-runner/internal/runnercatalog"
)

// Only overlapping origin reads share a result. The next read after completion
// sees a fresh database view, including mutations committed by another server.
func (s *Server) publicTemplatesRead() <-chan singleflight.Result {
	return s.publicTemplatesGroup.DoChan("catalog", func() (value any, err error) {
		parent := s.loopCtx
		if parent == nil {
			parent = context.Background()
		}
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		// DoChan runs outside the HTTP recovery boundary. Keep diagnostics
		// independent of any surviving waiter and omit provider/SQL details.
		defer func() {
			category := readFailureCategory(err)
			if recover() != nil {
				err = errors.New("public catalog query panicked")
				category = "query_panicked"
			}
			if err != nil {
				s.logger.ErrorContext(ctx, "public runner catalog read failed", "category", category)
			}
		}()
		profiles, err := s.store.ListProfilesContext(ctx)
		if err != nil {
			return nil, err
		}
		return runnercatalog.PublicTemplates(profiles), nil
	})
}

func (s *Server) handlePublicRunnerTemplates(w http.ResponseWriter, r *http.Request) {
	if r.Context().Err() != nil {
		w.WriteHeader(499) // Client Closed Request, retained in logs and metrics.
		return
	}
	select {
	case <-r.Context().Done():
		w.WriteHeader(499)
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
