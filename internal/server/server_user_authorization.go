package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
	"golang.org/x/sync/errgroup"
)

var errGitHubUserAccessTokenRequired = errors.New("github user access token is required")

const (
	maxUserRepositoryAccessCacheItems  = 512
	userRepositoryAccessRefreshAfter   = 20 * time.Second
	userRepositoryAccessCacheTTL       = 30 * time.Second
	userRepositoryAccessRefreshRetry   = 5 * time.Second
	userRepositoryAccessRefreshTimeout = 15 * time.Second
	userRepositoryAccessConcurrency    = 4
)

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
	if access, ok, refresh := s.cachedUserRepositoryAccessForRequest(accountID); ok {
		if refresh {
			s.refreshUserRepositoryAccessAsync(accountID)
		}
		return access, nil
	}
	return s.refreshUserRepositoryAccess(ctx, accountID)
}

func (s *Server) refreshUserRepositoryAccess(ctx context.Context, accountID int64) ([]state.GitHubInstallationRepositoryAccess, error) {
	for {
		epoch := s.userRepositoryAccessEpochForRefresh(accountID)
		result := s.userRepositoryAccessGroup.DoChan(fmt.Sprintf("account:%d:epoch:%d", accountID, epoch), func() (any, error) {
			refreshCtx, cancel := s.userRepositoryAccessRefreshContext()
			defer cancel()
			access, err := s.loadUserRepositoryAccess(refreshCtx, accountID)
			if err != nil {
				if isRejectedGitHubUserAccess(err) {
					s.invalidateUserRepositoryAccessIfCurrent(accountID, epoch)
				} else {
					s.finishUserRepositoryAccessRefresh(accountID, epoch)
				}
				return nil, err
			}
			s.cacheUserRepositoryAccessIfCurrent(accountID, epoch, access)
			return access, nil
		})
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case completed := <-result:
			if completed.Err != nil {
				return nil, completed.Err
			}
			if !s.userRepositoryAccessEpochMatches(accountID, epoch) {
				continue
			}
			access, _ := completed.Val.([]state.GitHubInstallationRepositoryAccess)
			return cloneUserRepositoryAccess(access), nil
		}
	}
}

func (s *Server) userRepositoryAccessRefreshContext() (context.Context, context.CancelFunc) {
	parent := context.Background()
	if s.loopCtx != nil {
		parent = s.loopCtx
	}
	return context.WithTimeout(parent, userRepositoryAccessRefreshTimeout)
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
			items, err := s.gh.ListUserInstallationRepositoriesForActiveInstallation(groupCtx, token, installation.InstallationID)
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

func (s *Server) refreshUserRepositoryAccessAsync(accountID int64) {
	go func() {
		parent := context.Background()
		if s.loopCtx != nil {
			parent = s.loopCtx
		}
		ctx, cancel := context.WithTimeout(parent, userRepositoryAccessRefreshTimeout)
		defer cancel()
		if _, err := s.refreshUserRepositoryAccess(ctx, accountID); err != nil && !errors.Is(err, context.Canceled) {
			s.logger.Warn("refresh github user repository access in background", "account_id", accountID, "error", err)
		}
	}()
}

func isRejectedGitHubUserAccess(err error) bool {
	if errors.Is(err, errGitHubUserAccessTokenRequired) {
		return true
	}
	status, ok := github.ErrorStatus(err)
	return ok && status == http.StatusUnauthorized
}

func (s *Server) cachedUserRepositoryAccessForRequest(accountID int64) ([]state.GitHubInstallationRepositoryAccess, bool, bool) {
	now := time.Now()
	s.userRepositoryAccessMu.Lock()
	defer s.userRepositoryAccessMu.Unlock()
	cached, ok := s.userRepositoryAccessCache[accountID]
	if !ok {
		return nil, false, false
	}
	if !now.Before(cached.expiresAt) {
		delete(s.userRepositoryAccessCache, accountID)
		return nil, false, false
	}
	refresh := !now.Before(cached.refreshAfter) && !cached.refreshing
	if refresh {
		cached.refreshing = true
		s.userRepositoryAccessCache[accountID] = cached
	}
	return cloneUserRepositoryAccess(cached.access), true, refresh
}

func (s *Server) cachedUserRepositoryAccess(accountID int64) ([]state.GitHubInstallationRepositoryAccess, bool) {
	now := time.Now()
	s.userRepositoryAccessMu.Lock()
	cached, ok := s.userRepositoryAccessCache[accountID]
	if ok && !now.Before(cached.expiresAt) {
		delete(s.userRepositoryAccessCache, accountID)
		ok = false
	}
	s.userRepositoryAccessMu.Unlock()
	if !ok {
		return nil, false
	}
	return cloneUserRepositoryAccess(cached.access), true
}

func (s *Server) cacheUserRepositoryAccess(accountID int64, access []state.GitHubInstallationRepositoryAccess) {
	epoch := s.userRepositoryAccessEpochForRefresh(accountID)
	s.cacheUserRepositoryAccessIfCurrent(accountID, epoch, access)
}

func (s *Server) cacheUserRepositoryAccessIfCurrent(accountID int64, epoch uint64, access []state.GitHubInstallationRepositoryAccess) bool {
	s.userRepositoryAccessMu.Lock()
	defer s.userRepositoryAccessMu.Unlock()
	if s.userRepositoryAccessEpoch[accountID] != epoch {
		return false
	}
	if s.userRepositoryAccessCache == nil {
		s.userRepositoryAccessCache = map[int64]cachedUserRepositoryAccess{}
	}
	now := time.Now()
	if _, exists := s.userRepositoryAccessCache[accountID]; !exists &&
		len(s.userRepositoryAccessCache) >= maxUserRepositoryAccessCacheItems {
		evictOneUserRepositoryAccess(s.userRepositoryAccessCache)
	}
	s.userRepositoryAccessCache[accountID] = cachedUserRepositoryAccess{
		access:       cloneUserRepositoryAccess(access),
		refreshAfter: now.Add(userRepositoryAccessRefreshAfter),
		expiresAt:    now.Add(userRepositoryAccessCacheTTL),
	}
	return true
}

func (s *Server) finishUserRepositoryAccessRefresh(accountID int64, epoch uint64) {
	s.userRepositoryAccessMu.Lock()
	defer s.userRepositoryAccessMu.Unlock()
	if s.userRepositoryAccessEpoch[accountID] != epoch {
		return
	}
	cached, ok := s.userRepositoryAccessCache[accountID]
	if !ok {
		return
	}
	cached.refreshing = false
	nextRefresh := time.Now().Add(userRepositoryAccessRefreshRetry)
	if nextRefresh.After(cached.expiresAt) {
		nextRefresh = cached.expiresAt
	}
	cached.refreshAfter = nextRefresh
	s.userRepositoryAccessCache[accountID] = cached
}

func evictOneUserRepositoryAccess(cache map[int64]cachedUserRepositoryAccess) {
	for accountID := range cache {
		delete(cache, accountID)
		return
	}
}

func (s *Server) invalidateUserRepositoryAccess(accountID int64) {
	s.userRepositoryAccessMu.Lock()
	defer s.userRepositoryAccessMu.Unlock()
	if s.userRepositoryAccessEpoch == nil {
		s.userRepositoryAccessEpoch = map[int64]uint64{}
	}
	s.userRepositoryAccessEpoch[accountID]++
	delete(s.userRepositoryAccessCache, accountID)
}

func (s *Server) invalidateUserRepositoryAccessIfCurrent(accountID int64, epoch uint64) {
	s.userRepositoryAccessMu.Lock()
	defer s.userRepositoryAccessMu.Unlock()
	if s.userRepositoryAccessEpoch[accountID] != epoch {
		return
	}
	if s.userRepositoryAccessEpoch == nil {
		s.userRepositoryAccessEpoch = map[int64]uint64{}
	}
	s.userRepositoryAccessEpoch[accountID]++
	delete(s.userRepositoryAccessCache, accountID)
}

func (s *Server) userRepositoryAccessEpochForRefresh(accountID int64) uint64 {
	s.userRepositoryAccessMu.Lock()
	epoch := s.userRepositoryAccessEpoch[accountID]
	s.userRepositoryAccessMu.Unlock()
	return epoch
}

func (s *Server) userRepositoryAccessEpochMatches(accountID int64, epoch uint64) bool {
	s.userRepositoryAccessMu.Lock()
	matches := s.userRepositoryAccessEpoch[accountID] == epoch
	s.userRepositoryAccessMu.Unlock()
	return matches
}

func cloneUserRepositoryAccess(access []state.GitHubInstallationRepositoryAccess) []state.GitHubInstallationRepositoryAccess {
	cloned := make([]state.GitHubInstallationRepositoryAccess, 0, len(access))
	for _, item := range access {
		cloned = append(cloned, state.GitHubInstallationRepositoryAccess{
			InstallationID: item.InstallationID,
			Repositories:   append([]string(nil), item.Repositories...),
		})
	}
	return cloned
}

func (s *Server) writeUserRepositoryAuthorizationError(w http.ResponseWriter, err error) {
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
