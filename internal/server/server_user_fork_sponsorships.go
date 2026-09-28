package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/state"
)

type userForkSponsorshipPolicyResponse struct {
	state.ForkSponsorshipPolicy
	Approvals []state.ForkSponsorshipApproval `json:"approvals"`
}

type userForkSponsorshipRepositoryResponse struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	FullName string `json:"full_name"`
}

type userForkSponsorshipPolicyInput struct {
	SourceRepositoryFullName string `json:"source_repository_full_name"`
	Mode                     string `json:"mode"`
	Enabled                  bool   `json:"enabled"`
	MaxConcurrency           int    `json:"max_concurrency"`
}

type userForkSponsorshipApprovalInput struct {
	ForkRepositoryFullName string `json:"fork_repository_full_name"`
}

func (s *Server) userForkSponsorshipScope(w http.ResponseWriter, r *http.Request) (adminSession, state.Account, int64, state.GitHubInstallationAccount, bool) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return adminSession{}, state.Account{}, 0, state.GitHubInstallationAccount{}, false
	}
	scope, err := s.accountPreferenceScopeFromRequest(account.ID, r)
	if err != nil || scope.Type != state.AccountScopeTypeGitHubInstall {
		writeErrorCode(w, http.StatusBadRequest, "fork_sponsorship_scope_invalid", "an organization installation_id is required")
		return adminSession{}, state.Account{}, 0, state.GitHubInstallationAccount{}, false
	}
	manageable, err := s.accountPreferenceScopeManageable(r.Context(), account.ID, scope)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return adminSession{}, state.Account{}, 0, state.GitHubInstallationAccount{}, false
	}
	if !manageable {
		writeErrorCode(w, http.StatusForbidden, "fork_sponsorship_scope_forbidden", "fork sponsorship for this organization is managed by its owners")
		return adminSession{}, state.Account{}, 0, state.GitHubInstallationAccount{}, false
	}
	owner, err := s.githubInstallationOwner(r.Context(), scope.ID)
	if err != nil {
		writeError(w, http.StatusBadGateway, "failed to resolve GitHub installation owner")
		return adminSession{}, state.Account{}, 0, state.GitHubInstallationAccount{}, false
	}
	if !strings.EqualFold(strings.TrimSpace(owner.AccountType), "organization") {
		writeErrorCode(w, http.StatusBadRequest, "fork_sponsorship_scope_invalid", "fork sponsorship is available only for organization installations")
		return adminSession{}, state.Account{}, 0, state.GitHubInstallationAccount{}, false
	}
	return session, account, scope.ID, owner, true
}

func (s *Server) handleUserListForkSponsorshipPolicies(w http.ResponseWriter, r *http.Request) {
	_, _, installationID, _, ok := s.userForkSponsorshipScope(w, r)
	if !ok {
		return
	}
	policies, err := s.store.ListForkSponsorshipPolicies(installationID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list fork sponsorship policies")
		return
	}
	items := make([]userForkSponsorshipPolicyResponse, 0, len(policies))
	for _, policy := range policies {
		approvals, err := s.store.ListForkSponsorshipApprovals(installationID, policy.SourceRepositoryID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to list fork sponsorship approvals")
			return
		}
		items = append(items, userForkSponsorshipPolicyResponse{ForkSponsorshipPolicy: policy, Approvals: approvals})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleUserListForkSponsorshipRepositories(w http.ResponseWriter, r *http.Request) {
	_, account, installationID, owner, ok := s.userForkSponsorshipScope(w, r)
	if !ok {
		return
	}
	if s.gh == nil {
		writeError(w, http.StatusInternalServerError, "github client is not configured")
		return
	}
	token, err := s.githubUserAccessToken(account.ID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	repositories, err := s.gh.ListUserInstallationRepositoryDetails(r.Context(), token, installationID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	items := make([]userForkSponsorshipRepositoryResponse, 0, len(repositories))
	for _, repository := range repositories {
		if repository.ID <= 0 || repository.Fork || repository.Owner.ID != owner.GitHubAccountID || !strings.EqualFold(strings.TrimSpace(repository.Owner.Login), strings.TrimSpace(owner.AccountLogin)) {
			continue
		}
		fullName := strings.TrimSpace(repository.FullName)
		name := strings.TrimSpace(repository.Name)
		if fullName == "" || name == "" {
			continue
		}
		items = append(items, userForkSponsorshipRepositoryResponse{ID: repository.ID, Name: name, FullName: fullName})
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].FullName) < strings.ToLower(items[j].FullName)
	})
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleUserPutForkSponsorshipPolicy(w http.ResponseWriter, r *http.Request) {
	session, _, installationID, owner, ok := s.userForkSponsorshipScope(w, r)
	if !ok {
		return
	}
	sourceRepositoryID, ok := positivePathInt64(w, r.PathValue("sourceRepositoryID"), "invalid source repository id")
	if !ok {
		return
	}
	var input userForkSponsorshipPolicyInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_fork_sponsorship_policy", "invalid fork sponsorship policy payload")
		return
	}
	repository, err := s.validateForkSponsorshipSource(r, owner, sourceRepositoryID, input.SourceRepositoryFullName)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_fork_sponsorship_source", err.Error())
		return
	}
	policy := state.ForkSponsorshipPolicy{SponsorInstallationID: installationID, SourceRepositoryID: repository.ID, SourceRepositoryFullName: repository.FullName, Mode: strings.TrimSpace(input.Mode), Enabled: input.Enabled, MaxConcurrency: input.MaxConcurrency}
	if existing, err := s.store.GetForkSponsorshipPolicyBySourceRepositoryID(repository.ID); err == nil && existing.SponsorInstallationID != installationID {
		writeErrorCode(w, http.StatusConflict, "fork_sponsorship_source_conflict", "this fork network already has a sponsor")
		return
	} else if err != nil && !errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "failed to check fork sponsorship policy")
		return
	}
	var saved state.ForkSponsorshipPolicy
	unlock := s.lockForkSponsorship(installationID, repository.ID)
	err = func() error {
		defer unlock()
		mutationErr := s.applyMutationWithAudit("github:"+session.Subject, "fork_sponsorship_policy.update", "fork_sponsorship_policy", fmt.Sprintf("%d:%d", installationID, repository.ID), map[string]any{"source_repository_full_name": repository.FullName, "mode": policy.Mode, "enabled": policy.Enabled, "max_concurrency": policy.MaxConcurrency}, func(tx state.Store) error {
			var saveErr error
			saved, saveErr = tx.UpdateForkSponsorshipPolicy(policy)
			return saveErr
		})
		return mutationErr
	}()
	if errors.Is(err, state.ErrConflict) {
		writeErrorCode(w, http.StatusConflict, "fork_sponsorship_source_conflict", "this fork network already has a sponsor")
		return
	}
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusNotFound, "fork sponsorship policy not found")
		return
	}
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_fork_sponsorship_policy", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleUserCreateForkSponsorshipPolicy(w http.ResponseWriter, r *http.Request) {
	session, _, installationID, owner, ok := s.userForkSponsorshipScope(w, r)
	if !ok {
		return
	}
	var input userForkSponsorshipPolicyInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_fork_sponsorship_policy", "invalid fork sponsorship policy payload")
		return
	}
	if s.gh == nil {
		writeError(w, http.StatusInternalServerError, "github client is not configured")
		return
	}
	repository, err := s.gh.GetRepository(r.Context(), strings.TrimSpace(input.SourceRepositoryFullName))
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_fork_sponsorship_source", "failed to resolve source repository")
		return
	}
	repository, err = s.validateForkSponsorshipSource(r, owner, repository.ID, repository.FullName)
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_fork_sponsorship_source", err.Error())
		return
	}
	policy := state.ForkSponsorshipPolicy{SponsorInstallationID: installationID, SourceRepositoryID: repository.ID, SourceRepositoryFullName: repository.FullName, Mode: strings.TrimSpace(input.Mode), Enabled: input.Enabled, MaxConcurrency: input.MaxConcurrency}
	if existing, err := s.store.GetForkSponsorshipPolicyBySourceRepositoryID(repository.ID); err == nil && existing.SponsorInstallationID != installationID {
		writeErrorCode(w, http.StatusConflict, "fork_sponsorship_source_conflict", "this fork network already has a sponsor")
		return
	} else if err != nil && !errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusInternalServerError, "failed to check fork sponsorship policy")
		return
	}
	var saved state.ForkSponsorshipPolicy
	unlock := s.lockForkSponsorship(installationID, repository.ID)
	err = func() error {
		defer unlock()
		mutationErr := s.applyMutationWithAudit("github:"+session.Subject, "fork_sponsorship_policy.create", "fork_sponsorship_policy", fmt.Sprintf("%d:%d", installationID, repository.ID), map[string]any{"source_repository_full_name": repository.FullName, "mode": policy.Mode, "enabled": policy.Enabled, "max_concurrency": policy.MaxConcurrency}, func(tx state.Store) error {
			var saveErr error
			saved, saveErr = tx.CreateForkSponsorshipPolicy(policy)
			return saveErr
		})
		return mutationErr
	}()
	if errors.Is(err, state.ErrConflict) {
		writeErrorCode(w, http.StatusConflict, "fork_sponsorship_source_conflict", "this fork network already has a sponsor")
		return
	}
	if err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_fork_sponsorship_policy", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, saved)
}

func (s *Server) handleUserDeleteForkSponsorshipPolicy(w http.ResponseWriter, r *http.Request) {
	session, _, installationID, _, ok := s.userForkSponsorshipScope(w, r)
	if !ok {
		return
	}
	sourceRepositoryID, ok := positivePathInt64(w, r.PathValue("sourceRepositoryID"), "invalid source repository id")
	if !ok {
		return
	}
	unlock := s.lockForkSponsorship(installationID, sourceRepositoryID)
	err := func() error {
		defer unlock()
		mutationErr := s.applyMutationWithAudit("github:"+session.Subject, "fork_sponsorship_policy.delete", "fork_sponsorship_policy", fmt.Sprintf("%d:%d", installationID, sourceRepositoryID), nil, func(tx state.Store) error {
			return tx.DeleteForkSponsorshipPolicy(installationID, sourceRepositoryID)
		})
		return mutationErr
	}()
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusNotFound, "fork sponsorship policy not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete fork sponsorship policy")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleUserAddForkSponsorshipApproval(w http.ResponseWriter, r *http.Request) {
	session, _, installationID, _, ok := s.userForkSponsorshipScope(w, r)
	if !ok {
		return
	}
	sourceRepositoryID, ok := positivePathInt64(w, r.PathValue("sourceRepositoryID"), "invalid source repository id")
	if !ok {
		return
	}
	policy, err := s.store.GetForkSponsorshipPolicy(installationID, sourceRepositoryID)
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusNotFound, "fork sponsorship policy not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to load fork sponsorship policy")
		return
	}
	var input userForkSponsorshipApprovalInput
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeErrorCode(w, http.StatusBadRequest, "invalid_fork_sponsorship_approval", "invalid fork sponsorship approval payload")
		return
	}
	fork, err := s.gh.GetRepository(r.Context(), strings.TrimSpace(input.ForkRepositoryFullName))
	if err != nil || !fork.Fork || fork.ID <= 0 || fork.Owner.ID <= 0 || strings.TrimSpace(fork.Owner.Login) == "" || fork.Source == nil || fork.Source.ID != sourceRepositoryID || !strings.EqualFold(strings.TrimSpace(fork.Source.FullName), policy.SourceRepositoryFullName) {
		writeErrorCode(w, http.StatusBadRequest, "invalid_fork_sponsorship_approval", "repository is not a fork of the sponsored source repository")
		return
	}
	approval := state.ForkSponsorshipApproval{SponsorInstallationID: installationID, SourceRepositoryID: sourceRepositoryID, ForkRepositoryID: fork.ID, ForkRepositoryFullName: fork.FullName, ForkOwnerID: fork.Owner.ID, ForkOwnerLogin: fork.Owner.Login}
	var saved state.ForkSponsorshipApproval
	unlock := s.lockForkSponsorship(installationID, sourceRepositoryID)
	err = func() error {
		defer unlock()
		mutationErr := s.applyMutationWithAudit("github:"+session.Subject, "fork_sponsorship_approval.upsert", "fork_sponsorship_approval", fmt.Sprintf("%d:%d", sourceRepositoryID, fork.ID), map[string]any{"fork_repository_full_name": fork.FullName, "fork_owner_login": fork.Owner.Login}, func(tx state.Store) error {
			var saveErr error
			saved, saveErr = tx.UpsertForkSponsorshipApproval(approval)
			return saveErr
		})
		return mutationErr
	}()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to save fork sponsorship approval")
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleUserDeleteForkSponsorshipApproval(w http.ResponseWriter, r *http.Request) {
	session, _, installationID, _, ok := s.userForkSponsorshipScope(w, r)
	if !ok {
		return
	}
	sourceRepositoryID, ok := positivePathInt64(w, r.PathValue("sourceRepositoryID"), "invalid source repository id")
	if !ok {
		return
	}
	forkRepositoryID, ok := positivePathInt64(w, r.PathValue("forkRepositoryID"), "invalid fork repository id")
	if !ok {
		return
	}
	unlock := s.lockForkSponsorship(installationID, sourceRepositoryID)
	err := func() error {
		defer unlock()
		mutationErr := s.applyMutationWithAudit("github:"+session.Subject, "fork_sponsorship_approval.delete", "fork_sponsorship_approval", fmt.Sprintf("%d:%d", sourceRepositoryID, forkRepositoryID), nil, func(tx state.Store) error {
			return tx.DeleteForkSponsorshipApproval(installationID, sourceRepositoryID, forkRepositoryID)
		})
		return mutationErr
	}()
	if errors.Is(err, state.ErrNotFound) {
		writeError(w, http.StatusNotFound, "fork sponsorship approval not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete fork sponsorship approval")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) validateForkSponsorshipSource(r *http.Request, owner state.GitHubInstallationAccount, sourceRepositoryID int64, fullName string) (github.Repository, error) {
	if s.gh == nil {
		return github.Repository{}, errors.New("github client is not configured")
	}
	repository, err := s.gh.GetRepository(r.Context(), strings.TrimSpace(fullName))
	if err != nil {
		return github.Repository{}, fmt.Errorf("resolve source repository: %w", err)
	}
	if repository.ID != sourceRepositoryID || repository.Fork || repository.Owner.ID <= 0 || repository.Owner.ID != owner.GitHubAccountID || !strings.EqualFold(strings.TrimSpace(repository.Owner.Login), strings.TrimSpace(owner.AccountLogin)) {
		return github.Repository{}, errors.New("repository identity does not match the organization installation")
	}
	return repository, nil
}

func positivePathInt64(w http.ResponseWriter, value, message string) (int64, bool) {
	id, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, message)
		return 0, false
	}
	return id, true
}
