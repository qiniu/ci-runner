package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
	"golang.org/x/sync/errgroup"
)

var errGitHubUserAccessTokenRequired = errors.New("github user access token is required")

const userRepositoryAccessConcurrency = 4

func (s *Server) githubUserAccessToken(accountID int64) (string, error) {
	secret, err := s.store.GetAccountSecret(state.AccountScopeTypeAccount, accountID, state.AccountSecretTypeGitHubOAuthToken)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return "", errGitHubUserAccessTokenRequired
		}
		return "", err
	}
	token, err := decryptSecret(secret.EncryptedValue, s.cfg.AuthEncryptionKey.Value())
	if err != nil {
		return "", errGitHubUserAccessTokenRequired
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", errGitHubUserAccessTokenRequired
	}
	return token, nil
}

func (s *Server) userAuthorizedRepositoryAccess(ctx context.Context, accountID int64) ([]state.GitHubInstallationRepositoryAccess, error) {
	return s.repositoryAccess(ctx, accountID, false)
}

func (s *Server) refreshUserRepositoryAccess(ctx context.Context, accountID int64) ([]state.GitHubInstallationRepositoryAccess, error) {
	if err := s.store.RequestRepositoryAccessRefresh(ctx, accountID); err != nil {
		return nil, err
	}
	return s.repositoryAccess(ctx, accountID, true)
}

func (s *Server) repositoryAccess(ctx context.Context, accountID int64, requireFresh bool) ([]state.GitHubInstallationRepositoryAccess, error) {
	// Waiters on any node observe the same claim and result; a disconnected
	// request does not cancel a refresh needed by other callers.
	ctx, cancel := context.WithTimeout(ctx, 2*state.RepositoryAccessRefreshTimeout)
	defer cancel()
	for {
		snapshot, err := s.store.ReadRepositoryAccessSnapshot(ctx, accountID)
		if err != nil {
			return nil, err
		}
		if snapshot.RefreshDue {
			claim, err := s.store.ClaimRepositoryAccessRefresh(ctx, accountID)
			if err != nil {
				return nil, err
			}
			if claim == "" {
				continue
			}
			done := make(chan error, 1)
			go func() { done <- s.refreshRepositoryAccessClaim(accountID, claim) }()
			if !snapshot.Valid || requireFresh {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case err := <-done:
					if err != nil && !errors.Is(err, state.ErrConflict) {
						return nil, err
					}
				}
			}
			// A credential change or hard expiry may have occurred while claiming.
			// Only the new read may authorize this request.
			continue
		}
		if snapshot.Valid && (!requireFresh || (!snapshot.Refreshing && snapshot.Failure == "")) {
			return snapshot.Access, nil
		}
		if snapshot.Failure != "" {
			return nil, &repositoryAccessFailure{snapshot.Failure, snapshot.RetryAfter}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type repositoryAccessFailure struct{ code, retryAfter string }

func (e *repositoryAccessFailure) Error() string { return e.code }

func (s *Server) refreshRepositoryAccessClaim(accountID int64, claim string) error {
	parent := s.loopCtx
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, state.RepositoryAccessRefreshTimeout)
	defer cancel()
	access, loadErr := s.loadUserRepositoryAccess(ctx, accountID)
	result := state.RepositoryAccessResult{Access: access}
	if loadErr != nil {
		switch {
		case isRejectedGitHubUserAccess(loadErr):
			result.Failure = state.AccessRefreshRejected
		case github.IsRateLimitError(loadErr):
			result.Failure = state.AccessRefreshRateLimited
			if value, ok := github.RateLimitRetryAfter(loadErr); ok {
				// Persist only a valid duration or HTTP date.
				if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
					result.RetryAfter = strconv.FormatUint(seconds, 10)
				} else if when, err := http.ParseTime(value); err == nil {
					result.RetryAfter = when.UTC().Format(http.TimeFormat)
				}
			}
		default:
			result.Failure = state.AccessRefreshUnavailable
		}
	}
	err := s.store.FinishRepositoryAccessRefresh(ctx, accountID, claim, result)
	if loadErr != nil {
		return loadErr
	}
	return err
}

func (s *Server) loadUserRepositoryAccess(ctx context.Context, accountID int64) ([]state.GitHubInstallationRepositoryAccess, error) {
	installations, err := s.store.ListGitHubInstallations(accountID)
	if err != nil {
		return nil, err
	}
	if len(installations) == 0 {
		access := []state.GitHubInstallationRepositoryAccess{}
		return access, nil
	}
	if s.gh == nil {
		return nil, errors.New("github client is not configured")
	}
	token, err := s.githubUserAccessToken(accountID)
	if err != nil {
		return nil, err
	}
	// Validate the OAuth token and all stored installations once. The repository
	// endpoint is independent per installation, but its metadata endpoint is
	// account-wide and must not be repeated by every concurrent worker.
	currentInstallations, err := s.gh.ListUserInstallations(ctx, token)
	if err != nil {
		if status, ok := github.ErrorStatus(err); ok &&
			(status == http.StatusNotFound || (status == http.StatusForbidden && !github.IsRateLimitError(err))) {
			return []state.GitHubInstallationRepositoryAccess{}, nil
		}
		return nil, err
	}
	activeInstallations := make(map[int64]struct{}, len(currentInstallations))
	for _, installation := range currentInstallations {
		if installation.ID > 0 && installation.SuspendedAt == nil {
			activeInstallations[installation.ID] = struct{}{}
		}
	}
	access := make([]state.GitHubInstallationRepositoryAccess, 0, len(installations))
	results := make([]state.GitHubInstallationRepositoryAccess, len(installations))
	resultOK := make([]bool, len(installations))
	group, groupCtx := errgroup.WithContext(ctx)
	// Installation repository listings are independent remote calls. Keep a
	// small bound so a cold Jobs request does not serialize every installation
	// or burst unboundedly against GitHub.
	group.SetLimit(userRepositoryAccessConcurrency)
	for index, installation := range installations {
		index, installation := index, installation
		if _, ok := activeInstallations[installation.InstallationID]; !ok {
			continue
		}
		group.Go(func() error {
			items, err := s.gh.ListUserInstallationRepositoriesForInstallations(groupCtx, token, installation.InstallationID, currentInstallations)
			if err != nil {
				if status, ok := github.ErrorStatus(err); ok &&
					(status == http.StatusNotFound || (status == http.StatusForbidden && !github.IsRateLimitError(err))) {
					return nil
				}
				return err
			}
			seen := map[string]bool{}
			repositories := make([]string, 0, len(items))
			for _, repository := range items {
				repository = strings.TrimSpace(repository)
				key := strings.ToLower(repository)
				if repository == "" || seen[key] {
					continue
				}
				seen[key] = true
				repositories = append(repositories, repository)
			}
			if len(repositories) > 0 {
				results[index] = state.GitHubInstallationRepositoryAccess{
					InstallationID: installation.InstallationID,
					Repositories:   repositories,
				}
				resultOK[index] = true
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	for index := range results {
		if resultOK[index] {
			access = append(access, results[index])
		}
	}
	return access, nil
}

func isRejectedGitHubUserAccess(err error) bool {
	if errors.Is(err, errGitHubUserAccessTokenRequired) {
		return true
	}
	status, ok := github.ErrorStatus(err)
	return ok && status == http.StatusUnauthorized
}

func (s *Server) writeUserRepositoryAuthorizationError(w http.ResponseWriter, err error) {
	var shared *repositoryAccessFailure
	if errors.As(err, &shared) {
		switch shared.code {
		case state.AccessRefreshRejected:
			err = errGitHubUserAccessTokenRequired
		case state.AccessRefreshRateLimited:
			if shared.retryAfter != "" {
				w.Header().Set("Retry-After", shared.retryAfter)
			}
			writeErrorCode(w, http.StatusServiceUnavailable, "GITHUB_RATE_LIMITED", "GitHub repository access is temporarily rate limited")
			return
		}
	}
	if errors.Is(err, errGitHubUserAccessTokenRequired) {
		writeErrorCode(w, http.StatusForbidden, "REAUTH_REQUIRED", "sign in with GitHub again to load repository access")
		return
	}
	if github.IsRateLimitError(err) {
		if retryAfter, ok := github.RateLimitRetryAfter(err); ok {
			w.Header().Set("Retry-After", retryAfter)
		}
		s.logger.Warn("github user repository access rate limited", "error", err)
		writeErrorCode(w, http.StatusServiceUnavailable, "GITHUB_RATE_LIMITED", "GitHub repository access is temporarily rate limited")
		return
	}
	if status, ok := github.ErrorStatus(err); ok {
		switch status {
		case http.StatusUnauthorized:
			writeErrorCode(w, http.StatusForbidden, "REAUTH_REQUIRED", "sign in with GitHub again to refresh repository access")
			return
		case http.StatusForbidden:
			writeErrorCode(w, http.StatusForbidden, "GITHUB_REPOSITORY_ACCESS_FORBIDDEN", "GitHub denied repository access")
			return
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, "github installation is not accessible")
			return
		}
	}
	s.logger.Warn("load github user repository access failed", "error", err)
	writeError(w, http.StatusBadGateway, "load github repository access")
}

func hasRepositoryAccess(access []state.GitHubInstallationRepositoryAccess, installationID int64, repository string) bool {
	repository = strings.TrimSpace(repository)
	for _, item := range access {
		if item.InstallationID != installationID {
			continue
		}
		for _, candidate := range item.Repositories {
			if strings.EqualFold(strings.TrimSpace(candidate), repository) {
				return true
			}
		}
	}
	return false
}
