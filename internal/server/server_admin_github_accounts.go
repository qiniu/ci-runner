package server

import (
	"net/http"
	"strconv"

	"github.com/qiniu/ci-runner/internal/github"
)

type adminGitHubAppInstallationsResponse struct {
	Installations []github.Installation `json:"installations"`
}

type adminGitHubAppInstallationDetailResponse struct {
	Installation github.Installation `json:"installation"`
	Repositories []string            `json:"repositories"`
}

func (s *Server) handleAdminListGitHubAppInstallations(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminAccount(w, r); !ok {
		return
	}
	installations, err := s.gh.ListInstallations(r.Context())
	if err != nil {
		s.logger.Error("list github app installations", "error", err)
		writeError(w, http.StatusBadGateway, "failed to list GitHub App installations")
		return
	}
	if installations == nil {
		installations = []github.Installation{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, adminGitHubAppInstallationsResponse{Installations: installations})
}

func (s *Server) handleAdminGetGitHubAppInstallationRepositories(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireAdminAccount(w, r); !ok {
		return
	}
	installationID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || installationID <= 0 {
		writeError(w, http.StatusBadRequest, "installation id must be a positive integer")
		return
	}
	installation, err := s.gh.GetInstallation(r.Context(), installationID)
	if err != nil {
		s.logger.Error("get github app installation", "installation_id", installationID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to load GitHub App installation")
		return
	}
	repositories, err := s.gh.ListInstallationRepositories(r.Context(), installationID)
	if err != nil {
		s.logger.Error("list github app installation repositories", "installation_id", installationID, "error", err)
		writeError(w, http.StatusBadGateway, "failed to list GitHub App installation repositories")
		return
	}
	if repositories == nil {
		repositories = []string{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, adminGitHubAppInstallationDetailResponse{
		Installation: installation,
		Repositories: repositories,
	})
}
