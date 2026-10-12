package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
)

func (s *Server) organizationRunnerGroups(ctx context.Context, scope state.RunnerProfileScope) (string, []github.RunnerGroup, error) {
	if scope.Type != state.AccountScopeTypeGitHubInstall {
		return "", nil, github.ErrRunnerGroupNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	owner, err := s.githubInstallationOwner(ctx, scope.ID)
	if err != nil {
		return "", nil, err
	}
	if !strings.EqualFold(owner.AccountType, "organization") {
		return "", nil, github.ErrRunnerGroupNotFound
	}
	groups, err := s.gh.ListOrganizationRunnerGroups(ctx, scope.ID, owner.AccountLogin, "")
	if err != nil {
		return "", nil, err
	}
	registrable := make([]github.RunnerGroup, 0, len(groups))
	for _, group := range groups {
		if !group.Inherited {
			registrable = append(registrable, group)
		}
	}
	return owner.AccountLogin, registrable, nil
}

func (s *Server) handleUserListGitHubRunnerGroups(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	scope, _, ok := s.userRunnerScope(w, r, account.ID)
	if !ok {
		return
	}
	if scope.Type != state.AccountScopeTypeGitHubInstall {
		writeErrorCode(w, http.StatusBadRequest, "runner_group_not_supported", "Runner groups are available only for organization scopes")
		return
	}
	organization, groups, err := s.organizationRunnerGroups(r.Context(), scope)
	if err != nil {
		writeRunnerGroupError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Organization string               `json:"organization"`
		Groups       []github.RunnerGroup `json:"groups"`
	}{organization, groups})
}

func (s *Server) validateScopedRunnerGroup(ctx context.Context, scope state.RunnerProfileScope, name string) error {
	if strings.TrimSpace(name) == "" {
		return nil
	}
	_, groups, err := s.organizationRunnerGroups(ctx, scope)
	if err != nil {
		return err
	}
	for _, group := range groups {
		if strings.EqualFold(group.Name, strings.TrimSpace(name)) {
			return nil
		}
	}
	return github.ErrRunnerGroupNotFound
}

// Keep upstream response bodies and transport details out of API and lifecycle errors.
func runnerGroupError(err error) (int, string, string) {
	status, hasStatus := github.ErrorStatus(err)
	switch {
	case errors.Is(err, github.ErrRunnerGroupNotFound), errors.Is(err, github.ErrRunnerGroupInherited):
		return http.StatusBadRequest, "runner_group_not_found", "Select an existing organization Runner group that accepts organization runners"
	case errors.Is(err, github.ErrRunnerGroupRepositoryDenied):
		return http.StatusBadRequest, "runner_group_repository_denied", "The GitHub Runner group does not allow the target repository; check repository access in GitHub"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return http.StatusGatewayTimeout, "runner_group_timeout", "GitHub Runner group lookup timed out; try again"
	case github.IsRateLimitError(err), hasStatus && status == http.StatusTooManyRequests:
		return http.StatusServiceUnavailable, "runner_group_rate_limited", "GitHub rate limit reached; try again later"
	}
	if hasStatus && (status == http.StatusForbidden || status == http.StatusUnauthorized || status == http.StatusNotFound) {
		return http.StatusBadGateway, "runner_group_permission_denied", "Cannot access GitHub Runner groups; check the App installation and organization Self-hosted runners permission"
	}
	return http.StatusBadGateway, "runner_group_unavailable", "Could not query GitHub Runner groups; try again"
}

func writeRunnerGroupError(w http.ResponseWriter, err error) {
	status, code, message := runnerGroupError(err)
	writeErrorCode(w, status, code, message)
}

type runnerGroupValidationFailure struct {
	code      string
	message   string
	retryable bool
}

func (e *runnerGroupValidationFailure) Error() string { return e.code + ": " + e.message }

func newRunnerGroupValidationFailure(err error) error {
	_, code, message := runnerGroupError(err)
	return &runnerGroupValidationFailure{
		code: code, message: message,
		retryable: code == "runner_group_timeout" || code == "runner_group_rate_limited" || code == "runner_group_unavailable",
	}
}
