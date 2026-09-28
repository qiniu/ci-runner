package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

var (
	errSandboxServiceNotConfigured  = errors.New("sandbox service not configured")
	errForkSponsorshipAtCapacity    = errors.New("fork sponsorship is at capacity")
	errForkSponsorshipPolicyChanged = errors.New("fork sponsorship policy changed during startup")
)

func (s *Server) sandboxServiceForRunnerRequest(ctx context.Context, req state.RunnerRequest) (sandboxrunner.Service, error) {
	svc, _, err := s.sandboxServiceAndConfigForRunnerRequestContext(ctx, req)
	return svc, err
}

type sandboxServiceConfigSnapshot struct {
	APIURL                          string
	EncryptedAPIKey                 string
	Source                          string
	SponsorInstallationID           int64
	SponsorSourceRepositoryID       int64
	SponsorSourceRepositoryFullName string
	SponsorAuthorizationReason      string
	ForkRepositoryID                int64
	ForkRepositoryFullName          string
	ForkOwnerID                     int64
	ForkOwnerLogin                  string
}

const (
	sandboxConfigSourceRequestSnapshot  = "request_snapshot"
	sandboxConfigSourceInstallation     = "installation"
	sandboxConfigSourceAccount          = "account"
	sandboxConfigSourceInheritedAccount = "inherited_account"
	sandboxConfigSourceAdminDefault     = "admin_default"
	sandboxConfigSourceForkSponsorship  = "organization_sponsorship"
)

func (s *Server) sandboxServiceAndConfigForRunnerRequest(req state.RunnerRequest) (sandboxrunner.Service, sandboxServiceConfigSnapshot, error) {
	return s.sandboxServiceAndConfigForRunnerRequestContext(context.Background(), req)
}

func (s *Server) sandboxServiceAndConfigForRunnerRequestContext(ctx context.Context, req state.RunnerRequest) (sandboxrunner.Service, sandboxServiceConfigSnapshot, error) {
	scopedCustom := strings.TrimSpace(req.ProfileSource) == "scoped_custom"
	if s.sandbox != nil {
		return s.sandbox, sandboxServiceConfigSnapshot{}, nil
	}
	if strings.TrimSpace(req.SandboxAPIURL) != "" && strings.TrimSpace(req.SandboxAPIKeyEncrypted) != "" {
		source := strings.TrimSpace(req.SandboxConfigSource)
		if source == "" {
			source = sandboxConfigSourceRequestSnapshot
		}
		snapshot := sandboxServiceConfigSnapshot{
			APIURL:                          strings.TrimSpace(req.SandboxAPIURL),
			EncryptedAPIKey:                 strings.TrimSpace(req.SandboxAPIKeyEncrypted),
			Source:                          source,
			SponsorInstallationID:           req.SponsorInstallationID,
			SponsorSourceRepositoryID:       req.SponsorSourceRepositoryID,
			SponsorSourceRepositoryFullName: strings.TrimSpace(req.SponsorSourceRepositoryFullName),
			SponsorAuthorizationReason:      strings.TrimSpace(req.SponsorAuthorizationReason),
		}
		svc, err := s.sandboxServiceForConfig(snapshot)
		return svc, snapshot, err
	}
	if req.GitHubInstallationID <= 0 {
		installationID, ok, err := s.githubInstallationScopeForRepository(req.RepositoryFullName)
		if err != nil {
			return nil, sandboxServiceConfigSnapshot{}, err
		}
		if ok {
			req.GitHubInstallationID = installationID
		}
	}
	scope, err := sandboxScopeForRunnerRequest(req)
	if err != nil {
		if scopedCustom {
			return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("scoped custom runner request %s has no configured Sandbox service: %w", req.ID, errSandboxServiceNotConfigured)
		}
		svc, snapshot, defaultErr := s.sandboxServiceForAdminDefault(func() (state.GitHubInstallationAccount, error) {
			return state.GitHubInstallationAccount{}, state.ErrNotFound
		})
		if defaultErr == nil {
			return svc, snapshot, nil
		}
		if errors.Is(defaultErr, errSandboxServiceNotConfigured) {
			return nil, sandboxServiceConfigSnapshot{}, err
		}
		return nil, sandboxServiceConfigSnapshot{}, defaultErr
	}
	svc, snapshot, err := s.sandboxServiceForScope(scope)
	if err == nil {
		return svc, snapshot, nil
	}
	if !errors.Is(err, errSandboxServiceNotConfigured) {
		return nil, sandboxServiceConfigSnapshot{}, err
	}
	accountID, ok, lookupErr := s.store.AccountScopeForPersonalGitHubInstallation(req.GitHubInstallationID)
	if lookupErr != nil {
		return nil, sandboxServiceConfigSnapshot{}, lookupErr
	}
	if ok {
		accountScope := accountPreferenceScope{Type: state.AccountScopeTypeAccount, ID: accountID}
		svc, snapshot, accountErr := s.sandboxServiceForScope(accountScope)
		if accountErr == nil {
			return svc, snapshot, nil
		}
		if !errors.Is(accountErr, errSandboxServiceNotConfigured) {
			return nil, sandboxServiceConfigSnapshot{}, accountErr
		}
	}
	if scopedCustom {
		return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("sandbox service is not configured for scoped custom runner request %s: %w", req.ID, errSandboxServiceNotConfigured)
	}
	profile, profileErr := s.profileForRunnerRequest(req)
	if profileErr == nil && strings.TrimSpace(profile.ManagedBy) != "" {
		svc, snapshot, sponsorshipErr := s.sandboxServiceForForkSponsorship(ctx, req)
		if sponsorshipErr == nil {
			return svc, snapshot, nil
		}
		if !errors.Is(sponsorshipErr, state.ErrNotFound) {
			return nil, sandboxServiceConfigSnapshot{}, sponsorshipErr
		}
	}
	return s.sandboxServiceForAdminDefault(func() (state.GitHubInstallationAccount, error) {
		return s.githubInstallationOwner(ctx, req.GitHubInstallationID)
	})
}

func (s *Server) sandboxServiceForForkSponsorship(ctx context.Context, req state.RunnerRequest) (sandboxrunner.Service, sandboxServiceConfigSnapshot, error) {
	if s.gh == nil || strings.TrimSpace(req.RepositoryFullName) == "" {
		return nil, sandboxServiceConfigSnapshot{}, state.ErrNotFound
	}
	fork, err := s.gh.GetRepository(ctx, req.RepositoryFullName)
	if err != nil {
		return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("resolve fork repository for sponsorship: %w", err)
	}
	if !fork.Fork || fork.ID <= 0 || fork.Owner.ID <= 0 || strings.TrimSpace(fork.Owner.Login) == "" || fork.Source == nil || fork.Source.ID <= 0 || strings.TrimSpace(fork.Source.FullName) == "" {
		return nil, sandboxServiceConfigSnapshot{}, state.ErrNotFound
	}
	if !strings.EqualFold(strings.TrimSpace(fork.FullName), strings.TrimSpace(req.RepositoryFullName)) {
		return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("fork repository identity changed: %w", state.ErrNotFound)
	}
	source := *fork.Source
	policy, err := s.store.GetForkSponsorshipPolicyBySourceRepositoryID(source.ID)
	if err != nil {
		return nil, sandboxServiceConfigSnapshot{}, err
	}
	if !policy.Enabled || !strings.EqualFold(strings.TrimSpace(policy.SourceRepositoryFullName), strings.TrimSpace(source.FullName)) {
		return nil, sandboxServiceConfigSnapshot{}, state.ErrNotFound
	}
	sponsor, err := s.githubInstallationOwner(ctx, policy.SponsorInstallationID)
	if err != nil {
		return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("resolve fork sponsor installation: %w", err)
	}
	if !strings.EqualFold(strings.TrimSpace(sponsor.AccountType), "organization") || source.Owner.ID <= 0 || source.Owner.ID != sponsor.GitHubAccountID || !strings.EqualFold(strings.TrimSpace(source.Owner.Login), strings.TrimSpace(sponsor.AccountLogin)) {
		return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("fork sponsorship source owner no longer matches sponsor: %w", state.ErrNotFound)
	}
	reason, err := s.authorizeForkSponsorship(ctx, policy, fork, source)
	if err != nil {
		return nil, sandboxServiceConfigSnapshot{}, err
	}
	svc, snapshot, err := s.sandboxServiceForScope(accountPreferenceScope{Type: state.AccountScopeTypeGitHubInstall, ID: policy.SponsorInstallationID})
	if err != nil {
		return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("resolve sponsor sandbox service: %w", err)
	}
	snapshot.Source = sandboxConfigSourceForkSponsorship
	snapshot.SponsorInstallationID = policy.SponsorInstallationID
	snapshot.SponsorSourceRepositoryID = policy.SourceRepositoryID
	snapshot.SponsorSourceRepositoryFullName = policy.SourceRepositoryFullName
	snapshot.SponsorAuthorizationReason = reason
	snapshot.ForkRepositoryID = fork.ID
	snapshot.ForkRepositoryFullName = fork.FullName
	snapshot.ForkOwnerID = fork.Owner.ID
	snapshot.ForkOwnerLogin = fork.Owner.Login
	return svc, snapshot, nil
}

func (s *Server) persistSandboxServiceSnapshot(id string, snapshot sandboxServiceConfigSnapshot) error {
	if snapshot.APIURL == "" && snapshot.EncryptedAPIKey == "" {
		return nil
	}
	if snapshot.Source == sandboxConfigSourceForkSponsorship {
		unlock := s.lockForkSponsorship(snapshot.SponsorInstallationID, snapshot.SponsorSourceRepositoryID)
		defer unlock()

		policy, err := s.store.GetForkSponsorshipPolicy(snapshot.SponsorInstallationID, snapshot.SponsorSourceRepositoryID)
		if err != nil || !policy.Enabled || policy.Mode != snapshot.SponsorAuthorizationReason || !strings.EqualFold(policy.SourceRepositoryFullName, snapshot.SponsorSourceRepositoryFullName) {
			return errForkSponsorshipPolicyChanged
		}
		if policy.Mode == state.ForkSponsorshipModeApprovalRequired {
			approval, approvalErr := s.store.GetForkSponsorshipApproval(policy.SourceRepositoryID, snapshot.ForkRepositoryID)
			if approvalErr != nil || approval.SponsorInstallationID != policy.SponsorInstallationID || approval.ForkOwnerID != snapshot.ForkOwnerID || !strings.EqualFold(approval.ForkRepositoryFullName, snapshot.ForkRepositoryFullName) || !strings.EqualFold(approval.ForkOwnerLogin, snapshot.ForkOwnerLogin) {
				return errForkSponsorshipPolicyChanged
			}
		}
		inFlight, err := s.store.InFlightCountForForkSponsorship(policy.SponsorInstallationID, policy.SourceRepositoryID)
		if err != nil {
			return err
		}
		if inFlight >= policy.MaxConcurrency {
			return errForkSponsorshipAtCapacity
		}
	}

	current, err := s.store.ReadState(id)
	if err != nil {
		return fmt.Errorf("read state for sandbox config snapshot: %w", err)
	}
	current.SandboxAPIURL = snapshot.APIURL
	current.SandboxAPIKeyEncrypted = snapshot.EncryptedAPIKey
	current.SandboxConfigSource = snapshot.Source
	current.SponsorInstallationID = snapshot.SponsorInstallationID
	current.SponsorSourceRepositoryID = snapshot.SponsorSourceRepositoryID
	current.SponsorSourceRepositoryFullName = snapshot.SponsorSourceRepositoryFullName
	current.SponsorAuthorizationReason = snapshot.SponsorAuthorizationReason
	if err := s.store.WriteState(current); err != nil {
		return fmt.Errorf("write sandbox config snapshot: %w", err)
	}
	return nil
}

func (s *Server) authorizeForkSponsorship(ctx context.Context, policy state.ForkSponsorshipPolicy, fork, source github.Repository) (string, error) {
	switch policy.Mode {
	case state.ForkSponsorshipModeApprovalRequired:
		approval, err := s.store.GetForkSponsorshipApproval(policy.SourceRepositoryID, fork.ID)
		if err != nil {
			return "", err
		}
		if approval.SponsorInstallationID != policy.SponsorInstallationID || approval.ForkOwnerID != fork.Owner.ID || !strings.EqualFold(approval.ForkRepositoryFullName, fork.FullName) || !strings.EqualFold(approval.ForkOwnerLogin, fork.Owner.Login) {
			return "", state.ErrNotFound
		}
		return state.ForkSponsorshipModeApprovalRequired, nil
	case state.ForkSponsorshipModeWritePermission:
		if !strings.EqualFold(strings.TrimSpace(fork.Owner.Type), "user") {
			return "", state.ErrNotFound
		}
		permission, err := s.gh.GetRepositoryCollaboratorPermission(ctx, source.FullName, fork.Owner.Login)
		if err != nil {
			return "", fmt.Errorf("check fork owner repository permission: %w", err)
		}
		if permission.UserID != fork.Owner.ID || !strings.EqualFold(permission.UserLogin, fork.Owner.Login) || (permission.Permission != "write" && permission.Permission != "maintain" && permission.Permission != "admin") {
			return "", state.ErrNotFound
		}
		return state.ForkSponsorshipModeWritePermission, nil
	case state.ForkSponsorshipModeOrganizationMember:
		if !strings.EqualFold(strings.TrimSpace(fork.Owner.Type), "user") {
			return "", state.ErrNotFound
		}
		membership, err := s.gh.GetOrganizationMembership(ctx, source.FullName, source.Owner.Login, fork.Owner.Login)
		if err != nil {
			return "", fmt.Errorf("check fork owner organization membership: %w", err)
		}
		if membership.State != "active" || membership.UserID != fork.Owner.ID || !strings.EqualFold(membership.UserLogin, fork.Owner.Login) {
			return "", state.ErrNotFound
		}
		return state.ForkSponsorshipModeOrganizationMember, nil
	default:
		return "", state.ErrNotFound
	}
}

func (s *Server) githubInstallationScopeForRepository(repositoryFullName string) (int64, bool, error) {
	owner, _, ok := strings.Cut(strings.TrimSpace(repositoryFullName), "/")
	if !ok {
		return 0, false, nil
	}
	return s.store.GitHubInstallationScopeForAccountLogin(owner)
}

func (s *Server) sandboxServiceForScope(scope accountPreferenceScope) (sandboxrunner.Service, sandboxServiceConfigSnapshot, error) {
	preference, err := s.store.GetAccountPreference(scope.Type, scope.ID, accountPreferenceNamespaceSandbox, accountPreferenceKeySandboxService)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("sandbox service is not configured for %s:%d: %w", scope.Type, scope.ID, errSandboxServiceNotConfigured)
		}
		return nil, sandboxServiceConfigSnapshot{}, err
	}
	var value accountSandboxServicePreferenceValue
	if err := json.Unmarshal([]byte(preference.ValueJSON), &value); err != nil {
		return nil, sandboxServiceConfigSnapshot{}, err
	}
	if normalizeSandboxPreferenceMode(value.Mode, scope) == sandboxPreferenceModeInherit {
		if value.SourceAccountID <= 0 {
			return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("sandbox service inherited account is not configured for %s:%d: %w", scope.Type, scope.ID, errSandboxServiceNotConfigured)
		}
		ok, err := s.githubInstallationLinkedToAccount(value.SourceAccountID, scope.ID)
		if err != nil {
			return nil, sandboxServiceConfigSnapshot{}, err
		}
		if !ok {
			return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("sandbox service inherited account no longer has github installation %d: %w", scope.ID, errSandboxServiceNotConfigured)
		}
		svc, snapshot, err := s.sandboxServiceForScope(accountPreferenceScope{Type: state.AccountScopeTypeAccount, ID: value.SourceAccountID})
		if err == nil {
			snapshot.Source = sandboxConfigSourceInheritedAccount
		}
		return svc, snapshot, err
	}
	apiURL := strings.TrimSpace(value.APIURL)
	if apiURL == "" {
		return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("sandbox service api_url is not configured for %s:%d: %w", scope.Type, scope.ID, errSandboxServiceNotConfigured)
	}
	secret, err := s.store.GetAccountSecret(scope.Type, scope.ID, state.AccountSecretTypeSandboxAPIKey)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("sandbox service api key is not configured for %s:%d: %w", scope.Type, scope.ID, errSandboxServiceNotConfigured)
		}
		return nil, sandboxServiceConfigSnapshot{}, err
	}
	snapshot := sandboxServiceConfigSnapshot{
		APIURL:          apiURL,
		EncryptedAPIKey: secret.EncryptedValue,
		Source:          sandboxConfigSourceForScope(scope),
	}
	svc, err := s.sandboxServiceForConfig(snapshot)
	return svc, snapshot, err
}

func (s *Server) sandboxServiceForScopeWithDefault(scope accountPreferenceScope) (sandboxrunner.Service, sandboxServiceConfigSnapshot, error) {
	return s.sandboxServiceForScopeWithDefaultContext(context.Background(), scope)
}

func (s *Server) sandboxServiceForScopeWithDefaultContext(ctx context.Context, scope accountPreferenceScope) (sandboxrunner.Service, sandboxServiceConfigSnapshot, error) {
	svc, snapshot, err := s.sandboxServiceForScope(scope)
	if err == nil {
		return svc, snapshot, nil
	}
	if !errors.Is(err, errSandboxServiceNotConfigured) {
		return nil, sandboxServiceConfigSnapshot{}, err
	}
	return s.sandboxServiceForAdminDefault(func() (state.GitHubInstallationAccount, error) {
		return s.sandboxDefaultAudienceOwnerForScope(ctx, scope)
	})
}

func sandboxConfigSourceForScope(scope accountPreferenceScope) string {
	if scope.Type == state.AccountScopeTypeAccount {
		return sandboxConfigSourceAccount
	}
	return sandboxConfigSourceInstallation
}

func (s *Server) sandboxServiceForAdminDefault(resolveOwner func() (state.GitHubInstallationAccount, error)) (sandboxrunner.Service, sandboxServiceConfigSnapshot, error) {
	defaultConfig, err := s.store.GetSandboxServiceDefault()
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("admin sandbox service default is not configured: %w", errSandboxServiceNotConfigured)
		}
		return nil, sandboxServiceConfigSnapshot{}, err
	}
	if !defaultConfig.Enabled {
		return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("admin sandbox service default is disabled: %w", errSandboxServiceNotConfigured)
	}
	if defaultConfig.AudienceMode == state.SandboxServiceDefaultAudienceModeSelected {
		owner, err := resolveOwner()
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("github repository owner is not known for admin sandbox service default: %w", errSandboxServiceNotConfigured)
			}
			return nil, sandboxServiceConfigSnapshot{}, err
		}
		allowed, err := s.store.SandboxServiceDefaultAudienceContains(owner.GitHubAccountID, owner.AccountType)
		if err != nil {
			return nil, sandboxServiceConfigSnapshot{}, err
		}
		if !allowed {
			return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("github repository owner is not selected for admin sandbox service default: %w", errSandboxServiceNotConfigured)
		}
	}
	apiURL := strings.TrimSpace(defaultConfig.APIURL)
	if apiURL == "" {
		return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("admin sandbox service default api_url is not configured: %w", errSandboxServiceNotConfigured)
	}
	encryptedAPIKey := strings.TrimSpace(defaultConfig.APIKeyEncrypted)
	if encryptedAPIKey == "" {
		return nil, sandboxServiceConfigSnapshot{}, fmt.Errorf("admin sandbox service default api key is not configured: %w", errSandboxServiceNotConfigured)
	}
	snapshot := sandboxServiceConfigSnapshot{
		APIURL:          apiURL,
		EncryptedAPIKey: encryptedAPIKey,
		Source:          sandboxConfigSourceAdminDefault,
	}
	svc, err := s.sandboxServiceForConfig(snapshot)
	return svc, snapshot, err
}

func (s *Server) sandboxDefaultAudienceOwnerForScope(ctx context.Context, scope accountPreferenceScope) (state.GitHubInstallationAccount, error) {
	switch scope.Type {
	case state.AccountScopeTypeGitHubInstall:
		return s.githubInstallationOwner(ctx, scope.ID)
	case state.AccountScopeTypeAccount:
		identity, err := s.store.GetOAuthIdentityForAccount(scope.ID, "github")
		if err != nil {
			return state.GitHubInstallationAccount{}, err
		}
		githubAccountID, err := strconv.ParseInt(strings.TrimSpace(identity.OAuthSubject), 10, 64)
		if err != nil || githubAccountID <= 0 {
			return state.GitHubInstallationAccount{}, state.ErrNotFound
		}
		return state.GitHubInstallationAccount{
			GitHubAccountID: githubAccountID,
			AccountType:     "user",
			AccountLogin:    identity.OAuthLogin,
		}, nil
	default:
		return state.GitHubInstallationAccount{}, state.ErrNotFound
	}
}

func (s *Server) githubInstallationOwner(ctx context.Context, installationID int64) (state.GitHubInstallationAccount, error) {
	if installationID <= 0 {
		return state.GitHubInstallationAccount{}, state.ErrNotFound
	}
	owner, err := s.store.GitHubInstallationAccountForInstallation(installationID)
	if err == nil {
		return owner, nil
	}
	if !errors.Is(err, state.ErrNotFound) {
		return state.GitHubInstallationAccount{}, err
	}
	owner, err = s.store.GetGitHubInstallationOwner(installationID)
	if err == nil {
		return owner, nil
	}
	if !errors.Is(err, state.ErrNotFound) {
		return state.GitHubInstallationAccount{}, err
	}
	if s.gh == nil {
		return state.GitHubInstallationAccount{}, state.ErrNotFound
	}
	installation, err := s.gh.GetInstallation(ctx, installationID)
	if err != nil {
		return state.GitHubInstallationAccount{}, err
	}
	return s.store.UpsertGitHubInstallationOwner(installationID, state.GitHubInstallationAccount{
		GitHubAccountID: installation.AccountID,
		AccountType:     installation.AccountType,
		AccountLogin:    installation.AccountLogin,
		AccountName:     installation.AccountName,
		AccountAvatar:   installation.AccountAvatar,
	})
}

func (s *Server) githubInstallationLinkedToAccount(accountID, installationID int64) (bool, error) {
	installations, err := s.store.ListGitHubInstallations(accountID)
	if err != nil {
		return false, err
	}
	for _, installation := range installations {
		if installation.InstallationID == installationID {
			return true, nil
		}
	}
	return false, nil
}

func (s *Server) sandboxServiceForConfig(snapshot sandboxServiceConfigSnapshot) (sandboxrunner.Service, error) {
	apiKey, err := decryptSecret(snapshot.EncryptedAPIKey, s.cfg.AuthEncryptionKey.Value())
	if err != nil {
		return nil, err
	}
	return sandboxrunner.NewE2BService(apiKey, strings.TrimSpace(snapshot.APIURL), s.sandboxHTTP)
}

func sandboxScopeForRunnerRequest(req state.RunnerRequest) (accountPreferenceScope, error) {
	if req.GitHubInstallationID <= 0 {
		return accountPreferenceScope{}, fmt.Errorf("runner request %s has no github installation scope", req.ID)
	}
	return accountPreferenceScope{
		Type: state.AccountScopeTypeGitHubInstall,
		ID:   req.GitHubInstallationID,
	}, nil
}
