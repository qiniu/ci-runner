package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
)

func (s *Server) handleUserDeploymentRefs(w http.ResponseWriter, r *http.Request) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	input := createDeploymentRequest{RepositoryFullName: strings.TrimSpace(r.URL.Query().Get("repository_full_name")), Ref: "discovery", Source: r.URL.Query().Get("source")}
	if input.Source == "pull_request" {
		input.Ref = "1"
	}
	input.InstallationID, _ = strconv.ParseInt(r.URL.Query().Get("installation_id"), 10, 64)
	page := 1
	if value := r.URL.Query().Get("page"); value != "" {
		page, _ = strconv.Atoi(value)
	}
	if err := validateCreateDeploymentRequest(input); err != nil || input.Source == "" || page <= 0 || page > 1000 {
		writeError(w, http.StatusBadRequest, "invalid deployment ref request")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	installation, found, err := s.githubInstallationForAccount(account.ID, input.InstallationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load GitHub installation")
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "GitHub installation is not linked to this account")
		return
	}
	if s.gh == nil {
		writeError(w, http.StatusBadGateway, "GitHub client is not configured")
		return
	}
	token, err := s.githubUserAccessToken(account.ID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	repositories, err := s.gh.ListUserInstallationRepositoryDetails(ctx, token, installation.InstallationID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	repository, ok := deploymentRepositoryAccess(repositories, input.RepositoryFullName, installation)
	if !ok {
		writeErrorCode(w, http.StatusForbidden, "repository_access_forbidden", "repository is not accessible through this GitHub installation and user session")
		return
	}
	var refs []string
	var hasMore bool
	if input.Source == "pull_request" {
		refs, hasMore, err = s.gh.ListDeploymentPullRequests(ctx, repository.FullName, page)
	} else {
		refs, hasMore, err = s.gh.ListRepositoryRefs(ctx, repository.FullName, input.Source, page)
	}
	if err != nil {
		if status, known := github.ErrorStatus(err); known && status == http.StatusForbidden && !github.IsRateLimitError(err) {
			writeErrorCode(w, http.StatusForbidden, "deployment_repository_contents_required", "The GitHub App cannot read this repository's versions. Check that Contents: Read-only permission has been approved for this installation.")
			return
		}
		writeErrorCode(w, http.StatusBadGateway, "deployment_refs_unavailable", "Unable to load repository versions")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"refs": refs, "has_more": hasMore, "page": page})
}
