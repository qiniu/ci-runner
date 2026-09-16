package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

type upsertGitHubInstallationRequest struct {
	InstallationID int64  `json:"installation_id"`
	SetupState     string `json:"setup_state"`
}

type userGitHubInstallationResponse struct {
	state.GitHubInstallation
	Manageable bool `json:"manageable"`
}

type accountPreferencesResponse struct {
	Sandbox accountSandboxPreference `json:"sandbox"`
	Cache   accountCachePreference   `json:"cache"`
}

type accountSandboxPreference struct {
	Mode                   string                         `json:"mode"`
	ResolvedSource         string                         `json:"resolved_source"`
	APIURL                 string                         `json:"api_url"`
	APIKey                 accountSandboxAPIKeyPreference `json:"api_key"`
	Manageable             bool                           `json:"manageable"`
	Inherited              bool                           `json:"inherited"`
	SourceAccountID        int64                          `json:"source_account_id,omitempty"`
	SourceAccountLogin     string                         `json:"source_account_login,omitempty"`
	SourceIsCurrentAccount bool                           `json:"source_is_current_account,omitempty"`
	SourceAvailable        bool                           `json:"source_available"`
}

type accountSandboxAPIKeyPreference struct {
	Configured bool   `json:"configured"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type accountCachePreference struct {
	Configured bool   `json:"configured"`
	Region     string `json:"region,omitempty"`
	Bucket     string `json:"bucket,omitempty"`
	Prefix     string `json:"prefix,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type accountCacheServicePreferenceValue struct {
	Bucket string `json:"bucket"`
	Prefix string `json:"prefix,omitempty"`
}

type upsertCacheConfigRequest struct {
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

type accountSandboxServicePreferenceValue struct {
	Mode            string `json:"mode,omitempty"`
	APIURL          string `json:"api_url,omitempty"`
	SourceAccountID int64  `json:"source_account_id,omitempty"`
}

type upsertSandboxConfigRequest struct {
	APIURL                 string `json:"api_url"`
	APIKey                 string `json:"api_key"`
	Mode                   string `json:"mode"`
	ReplaceInheritedSource bool   `json:"replace_inherited_source"`
}

type accountPreferenceScope struct {
	Type string
	ID   int64
}

const (
	accountPreferenceNamespaceSandbox  = "sandbox"
	accountPreferenceKeySandboxService = "service"
	accountPreferenceNamespaceCache    = "cache"
	accountPreferenceKeyCacheS3        = "s3"
)

func (s *Server) handleGitHubAppInstallRedirect(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireUserSession(w, r); !ok {
		return
	}
	if strings.TrimSpace(s.cfg.GitHubAppSlug) == "" {
		writeError(w, http.StatusBadRequest, "github app slug is not configured")
		return
	}
	setupState := newID()
	http.SetCookie(w, &http.Cookie{
		Name:     githubAppSetupStateCookieName,
		Value:    setupState,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   requestIsSecure(r),
		MaxAge:   int((10 * time.Minute).Seconds()),
	})
	http.Redirect(w, r, s.githubAppInstallationURL(setupState), http.StatusFound)
}

func (s *Server) handleGitHubAppSetupRedirect(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := s.requireUserSession(w, r); !ok {
		return
	}
	values := url.Values{}
	for _, key := range []string{"installation_id", "setup_action", "state"} {
		if value := strings.TrimSpace(r.URL.Query().Get(key)); value != "" {
			values.Set(key, value)
		}
	}
	installationID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("installation_id")), 10, 64)
	if err == nil && installationID > 0 {
		if !s.validGitHubAppSetupState(r, r.URL.Query().Get("state")) {
			values.Set("setup_error", "invalid_state")
		}
	}
	target := "/repositories"
	if encoded := values.Encode(); encoded != "" {
		target += "?" + encoded
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func (s *Server) handleUserGitHubApp(w http.ResponseWriter, r *http.Request) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	installations, err := s.store.ListGitHubInstallations(account.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	responseInstallations := any(installations)
	settingsManageability := r.URL.Query().Get("include") == "settings"
	if settingsManageability {
		owners := make([]state.GitHubInstallationAccount, 0, len(installations))
		for _, installation := range installations {
			owners = append(owners, state.GitHubInstallationAccount{
				GitHubAccountID: installation.GitHubAccountID,
				AccountType:     installation.AccountType,
				AccountLogin:    installation.AccountLogin,
				AccountName:     installation.AccountName,
				AccountAvatar:   installation.AccountAvatar,
			})
		}
		manageable, manageableErr := s.githubInstallationAccountsManageable(r.Context(), account.ID, owners)
		if manageableErr != nil {
			s.writeUserRepositoryAuthorizationError(w, manageableErr)
			return
		}
		responses := make([]userGitHubInstallationResponse, 0, len(installations))
		for i, installation := range installations {
			responses = append(responses, userGitHubInstallationResponse{
				GitHubInstallation: installation,
				Manageable:         manageable[i],
			})
		}
		responseInstallations = responses
	}
	response := map[string]any{
		"app_slug":      strings.TrimSpace(s.cfg.GitHubAppSlug),
		"install_url":   s.githubAppInstallURL(),
		"setup_url":     "/github-app/setup",
		"installations": responseInstallations,
	}
	if settingsManageability {
		response["settings_manageability"] = true
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleUserSaveGitHubInstallation(w http.ResponseWriter, r *http.Request) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	var input upsertGitHubInstallationRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid github installation payload")
		return
	}
	if input.InstallationID <= 0 {
		writeError(w, http.StatusBadRequest, "installation_id is required")
		return
	}
	if !s.validGitHubAppSetupState(r, input.SetupState) {
		clearCookie(w, githubAppSetupStateCookieName, "/", requestIsSecure(r))
		writeError(w, http.StatusForbidden, "invalid github app setup state")
		return
	}
	installation, err := s.syncGitHubInstallation(r.Context(), account.ID, input.InstallationID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.invalidateUserRepositoryAccess(account.ID)
	clearCookie(w, githubAppSetupStateCookieName, "/", requestIsSecure(r))
	s.recordAudit("github:"+session.Subject, "github_app.configure", "github_installation", strconv.FormatInt(installation.InstallationID, 10), map[string]any{
		"account_login": installation.AccountLogin,
	})
	writeJSON(w, http.StatusOK, installation)
}

func (s *Server) handleUserDeleteGitHubInstallation(w http.ResponseWriter, r *http.Request) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid github installation id")
		return
	}
	if err := s.store.DeleteGitHubInstallation(account.ID, id); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			writeError(w, http.StatusNotFound, "github installation not found")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.invalidateUserRepositoryAccess(account.ID)
	s.recordAudit("github:"+session.Subject, "github_app.delete", "github_installation", strconv.FormatInt(id, 10), nil)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleUserSyncGitHubInstallations(w http.ResponseWriter, r *http.Request) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	if s.gh == nil {
		writeError(w, http.StatusInternalServerError, "github client is not configured")
		return
	}
	token, err := s.githubUserAccessToken(account.ID)
	if err != nil {
		if errors.Is(err, errGitHubUserAccessTokenRequired) {
			writeErrorCode(w, http.StatusBadRequest, "REAUTH_REQUIRED", "sign in with GitHub again before syncing installations")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	remoteInstallations, err := s.gh.ListUserInstallations(r.Context(), token)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	existingInstallations, err := s.store.ListGitHubInstallations(account.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer s.invalidateUserRepositoryAccess(account.ID)
	remoteIDs := make(map[int64]struct{}, len(remoteInstallations))
	synced := make([]state.GitHubInstallation, 0, len(remoteInstallations))
	for _, remote := range remoteInstallations {
		remoteIDs[remote.ID] = struct{}{}
		installation, err := s.store.UpsertGitHubInstallation(state.GitHubInstallation{
			AccountID:       account.ID,
			InstallationID:  remote.ID,
			GitHubAccountID: remote.AccountID,
			AccountType:     remote.AccountType,
			AccountLogin:    remote.AccountLogin,
			AccountName:     remote.AccountName,
			AccountAvatar:   remote.AccountAvatar,
		})
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		synced = append(synced, installation)
	}
	removed := 0
	for _, existing := range existingInstallations {
		if _, ok := remoteIDs[existing.InstallationID]; ok {
			continue
		}
		if err := s.store.DeleteGitHubInstallation(account.ID, existing.ID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		removed++
	}
	s.recordAudit("github:"+session.Subject, "github_app.sync", "account", strconv.FormatInt(account.ID, 10), map[string]any{
		"count":   len(synced),
		"removed": removed,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"installations": synced,
	})
}

func (s *Server) handleUserListGitHubInstallationRepositories(w http.ResponseWriter, r *http.Request) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "invalid github installation id")
		return
	}
	installation, ok, err := s.githubInstallationForAccount(account.ID, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "github installation not found")
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
	repositories, err := s.gh.ListUserInstallationRepositories(r.Context(), token, installation.InstallationID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"installation_id": installation.InstallationID,
		"repositories":    repositories,
	})
}

func (s *Server) handleUserListRunners(w http.ResponseWriter, r *http.Request) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	limit, offset, err := parsePagination(r, defaultRunnerRequestListLimit, maxRunnerRequestListLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if offset > maxUserRunnerHistoryWindow-limit {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("requested page exceeds the %d-runner history window", maxUserRunnerHistoryWindow))
		return
	}
	access, err := s.userAuthorizedRepositoryAccess(r.Context(), account.ID)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	states, total, err := s.store.ListStatesForGitHubInstallationRepositories(access, limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writePaginationHeadersWithinWindow(w, r, total, limit, offset, maxUserRunnerHistoryWindow)
	writeJSON(w, http.StatusOK, states)
}

func (s *Server) handleUserPreferences(w http.ResponseWriter, r *http.Request) {
	_, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	scope, err := s.accountPreferenceScopeFromRequest(account.ID, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	manageable, err := s.accountPreferenceScopeManageable(r.Context(), account.ID, scope)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	if !manageable {
		response, err := s.accountReadinessResponse(scope)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	response, err := s.accountPreferencesResponse(scope, account.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.Sandbox.Manageable = true
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleUserSaveCacheConfig(w http.ResponseWriter, r *http.Request) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	scope, err := s.accountPreferenceScopeFromRequest(account.ID, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	manageable, err := s.accountPreferenceScopeManageable(r.Context(), account.ID, scope)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	if !manageable {
		writeError(w, http.StatusForbidden, "Cache service for this GitHub account is managed by its owner")
		return
	}
	var input upsertCacheConfigRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid cache config payload")
		return
	}
	// Region and endpoint are server-owned. Resolve them from the effective
	// Sandbox service region instead of trusting values supplied by the browser
	// or reading only the scope's local preference (which may be empty when the
	// service comes from inheritance or the admin default).
	_, sandboxSnapshot, sandboxErr := s.sandboxServiceForScopeWithDefaultContext(r.Context(), scope)
	if sandboxErr != nil {
		if errors.Is(sandboxErr, errSandboxServiceNotConfigured) {
			writeError(w, http.StatusBadRequest, "configure a Sandbox service before configuring Cache S3")
			return
		}
		writeError(w, http.StatusInternalServerError, sandboxErr.Error())
		return
	}
	region, endpoint, ok := s.cacheS3SettingsForSandboxAPIURL(sandboxSnapshot.APIURL)
	if !ok || region == "" || endpoint == "" {
		writeError(w, http.StatusBadRequest, "selected Sandbox service region has no cache S3 mapping")
		return
	}
	value := accountCacheServicePreferenceValue{
		Bucket: strings.TrimSpace(input.Bucket),
		Prefix: strings.Trim(strings.TrimSpace(input.Prefix), "/"),
	}
	if value.Prefix != "" {
		if err := validateCachePrefix(value.Prefix); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if err := validateCacheBucket(value.Bucket); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	accessKeyID := strings.TrimSpace(input.AccessKeyID)
	secretAccessKey := strings.TrimSpace(input.SecretAccessKey)
	if accessKeyID == "" || secretAccessKey == "" {
		if _, err := s.store.GetAccountPreference(scope.Type, scope.ID, accountPreferenceNamespaceCache, accountPreferenceKeyCacheS3); err != nil {
			writeError(w, http.StatusBadRequest, "access_key_id and secret_access_key are required")
			return
		}
		if accessKeyID == "" {
			secret, secretErr := s.store.GetAccountSecret(scope.Type, scope.ID, state.AccountSecretTypeCacheAccessKeyID)
			if secretErr != nil {
				writeError(w, http.StatusBadRequest, "access_key_id is required")
				return
			}
			accessKeyID, err = decryptSecret(secret.EncryptedValue, s.cfg.AuthEncryptionKey.Value())
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
		if secretAccessKey == "" {
			secret, secretErr := s.store.GetAccountSecret(scope.Type, scope.ID, state.AccountSecretTypeCacheSecretAccessKey)
			if secretErr != nil {
				writeError(w, http.StatusBadRequest, "secret_access_key is required")
				return
			}
			secretAccessKey, err = decryptSecret(secret.EncryptedValue, s.cfg.AuthEncryptionKey.Value())
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
		}
	}
	if _, err := newCacheS3(cacheS3Config{Region: region, Bucket: value.Bucket, Prefix: value.Prefix, Endpoint: endpoint, AccessKeyID: accessKeyID, SecretAccessKey: secretAccessKey}); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	valueJSON, err := json.Marshal(value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	encryptedAccessKey, err := encryptSecret(accessKeyID, s.cfg.AuthEncryptionKey.Value())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	encryptedSecretKey, err := encryptSecret(secretAccessKey, s.cfg.AuthEncryptionKey.Value())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := s.store.UpsertAccountPreferenceAndSecrets(state.AccountPreference{ScopeType: scope.Type, ScopeID: scope.ID, Namespace: accountPreferenceNamespaceCache, Key: accountPreferenceKeyCacheS3, ValueJSON: string(valueJSON)},
		state.AccountSecret{ScopeType: scope.Type, ScopeID: scope.ID, KeyType: state.AccountSecretTypeCacheAccessKeyID, EncryptedValue: encryptedAccessKey},
		state.AccountSecret{ScopeType: scope.Type, ScopeID: scope.ID, KeyType: state.AccountSecretTypeCacheSecretAccessKey, EncryptedValue: encryptedSecretKey}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.recordAudit("github:"+session.Subject, "cache.configure", scope.Type, strconv.FormatInt(scope.ID, 10), map[string]any{"region": region, "bucket": value.Bucket, "endpoint": endpoint})
	response, err := s.accountPreferencesResponse(scope, account.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.Sandbox.Manageable = true
	response.Cache.Configured = true
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleUserDeleteCacheConfig(w http.ResponseWriter, r *http.Request) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	scope, err := s.accountPreferenceScopeFromRequest(account.ID, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	manageable, err := s.accountPreferenceScopeManageable(r.Context(), account.ID, scope)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	if !manageable {
		writeError(w, http.StatusForbidden, "Cache service for this GitHub account is managed by its owner")
		return
	}
	err = s.applyMutationWithAudit("github:"+session.Subject, "cache.delete", scope.Type, strconv.FormatInt(scope.ID, 10), nil, func(tx state.Store) error {
		for _, key := range []string{state.AccountSecretTypeCacheAccessKeyID, state.AccountSecretTypeCacheSecretAccessKey} {
			if deleteErr := tx.DeleteAccountSecret(scope.Type, scope.ID, key); deleteErr != nil && !errors.Is(deleteErr, state.ErrNotFound) {
				return deleteErr
			}
		}
		if deleteErr := tx.DeleteAccountPreference(scope.Type, scope.ID, accountPreferenceNamespaceCache, accountPreferenceKeyCacheS3); deleteErr != nil && !errors.Is(deleteErr, state.ErrNotFound) {
			return deleteErr
		}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	response, err := s.accountPreferencesResponse(scope, account.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.Sandbox.Manageable = true
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleUserSaveSandboxConfig(w http.ResponseWriter, r *http.Request) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	scope, err := s.accountPreferenceScopeFromRequest(account.ID, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	manageable, err := s.accountPreferenceScopeManageable(r.Context(), account.ID, scope)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	if !manageable {
		writeError(w, http.StatusForbidden, "Sandbox service for this GitHub account is managed by its owner")
		return
	}
	var input upsertSandboxConfigRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid sandbox config payload")
		return
	}
	mode := normalizeSandboxPreferenceMode(input.Mode, scope)
	apiKey := strings.TrimSpace(input.APIKey)
	var value accountSandboxServicePreferenceValue
	var secret *state.AccountSecret
	if mode == sandboxPreferenceModeInherit {
		if scope.Type != state.AccountScopeTypeGitHubInstall {
			writeError(w, http.StatusBadRequest, "inherit mode is only supported for GitHub installation preferences")
			return
		}
		sourceAccountID, err := s.inheritedSandboxSourceAccountID(scope, account.ID, input.ReplaceInheritedSource)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if sourceAccountID == account.ID {
			configured, err := s.sandboxSourceAccountConfigured(account.ID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if !configured {
				writeError(w, http.StatusBadRequest, "configure your account Sandbox service first")
				return
			}
		}
		value = accountSandboxServicePreferenceValue{Mode: sandboxPreferenceModeInherit, SourceAccountID: sourceAccountID}
	} else {
		apiURL, err := normalizeHTTPURL(input.APIURL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		apiURL, supported := s.supportedSandboxRegionEndpoint(apiURL)
		if !supported {
			writeError(w, http.StatusBadRequest, "unsupported sandbox region")
			return
		}
		_, currentKeyErr := s.store.GetAccountSecret(scope.Type, scope.ID, state.AccountSecretTypeSandboxAPIKey)
		if apiKey == "" && errors.Is(currentKeyErr, state.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "api_key is required")
			return
		}
		if currentKeyErr != nil && !errors.Is(currentKeyErr, state.ErrNotFound) {
			writeError(w, http.StatusInternalServerError, currentKeyErr.Error())
			return
		}
		value = accountSandboxServicePreferenceValue{APIURL: apiURL}
		if apiKey != "" {
			encryptedAPIKey, err := encryptSecret(apiKey, s.cfg.AuthEncryptionKey.Value())
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			secret = &state.AccountSecret{
				ScopeType:      scope.Type,
				ScopeID:        scope.ID,
				KeyType:        state.AccountSecretTypeSandboxAPIKey,
				EncryptedValue: encryptedAPIKey,
			}
		}
	}
	valueJSON, err := json.Marshal(value)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	preference := state.AccountPreference{
		ScopeType: scope.Type,
		ScopeID:   scope.ID,
		Namespace: accountPreferenceNamespaceSandbox,
		Key:       accountPreferenceKeySandboxService,
		ValueJSON: string(valueJSON),
	}
	auditPayload := map[string]any{
		"mode":           mode,
		"api_url":        value.APIURL,
		"api_key_update": apiKey != "",
	}
	if mode == sandboxPreferenceModeInherit {
		auditPayload["source_account_id"] = value.SourceAccountID
	}
	err = s.applyMutationWithAudit("github:"+session.Subject, "sandbox.configure", scope.Type, strconv.FormatInt(scope.ID, 10), auditPayload, func(tx state.Store) error {
		if mode == sandboxPreferenceModeInherit {
			_, err = tx.UpsertAccountPreferenceAndDeleteSecret(preference, state.AccountSecret{
				ScopeType: scope.Type,
				ScopeID:   scope.ID,
				KeyType:   state.AccountSecretTypeSandboxAPIKey,
			})
		} else {
			_, _, err = tx.UpsertAccountPreferenceAndSecret(preference, secret)
		}
		return err
	})
	if err != nil {
		if writeMutationAuditError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	response, err := s.accountPreferencesResponse(scope, account.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.Sandbox.Manageable = true
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleUserDeleteSandboxAPIKey(w http.ResponseWriter, r *http.Request) {
	session, account, ok := s.requireUserSession(w, r)
	if !ok {
		return
	}
	scope, err := s.accountPreferenceScopeFromRequest(account.ID, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	manageable, err := s.accountPreferenceScopeManageable(r.Context(), account.ID, scope)
	if err != nil {
		s.writeUserRepositoryAuthorizationError(w, err)
		return
	}
	if !manageable {
		writeError(w, http.StatusForbidden, "Sandbox service for this GitHub account is managed by its owner")
		return
	}
	err = s.applyMutationWithAudit("github:"+session.Subject, "sandbox_api_key.delete", scope.Type, strconv.FormatInt(scope.ID, 10), nil, func(tx state.Store) error {
		mutationErr := tx.DeleteAccountSecret(scope.Type, scope.ID, state.AccountSecretTypeSandboxAPIKey)
		if errors.Is(mutationErr, state.ErrNotFound) {
			return nil
		}
		return mutationErr
	})
	if err != nil {
		if writeMutationAuditError(w, err) {
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	response, err := s.accountPreferencesResponse(scope, account.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	response.Sandbox.Manageable = true
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) accountPreferencesResponse(scope accountPreferenceScope, viewerAccountID int64) (accountPreferencesResponse, error) {
	response, err := s.accountPreferencesResponseBase(scope, viewerAccountID)
	if err != nil {
		return accountPreferencesResponse{}, err
	}
	return s.fillCachePreferenceResponse(response, scope)
}

func (s *Server) accountReadinessResponse(scope accountPreferenceScope) (accountPreferencesResponse, error) {
	response := accountPreferencesResponse{}
	response.Sandbox.Mode = sandboxPreferenceModeCustom
	return s.fillSandboxResolvedSource(response, scope)
}

func (s *Server) accountPreferencesResponseBase(scope accountPreferenceScope, viewerAccountID int64) (accountPreferencesResponse, error) {
	var response accountPreferencesResponse
	preference, err := s.store.GetAccountPreference(scope.Type, scope.ID, accountPreferenceNamespaceSandbox, accountPreferenceKeySandboxService)
	if err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			return accountPreferencesResponse{}, err
		}
		response.Sandbox.Mode = sandboxPreferenceModeCustom
		return s.fillSandboxResolvedSource(response, scope)
	} else {
		var value accountSandboxServicePreferenceValue
		if err := json.Unmarshal([]byte(preference.ValueJSON), &value); err != nil {
			return accountPreferencesResponse{}, err
		}
		mode := normalizeSandboxPreferenceMode(value.Mode, scope)
		response.Sandbox.Mode = mode
		if mode == sandboxPreferenceModeInherit {
			response.Sandbox.Inherited = true
			response.Sandbox.SourceAccountID = value.SourceAccountID
			response.Sandbox.SourceIsCurrentAccount = value.SourceAccountID == viewerAccountID
			identity, err := s.store.GetOAuthIdentityForAccount(value.SourceAccountID, "github")
			if err != nil && !errors.Is(err, state.ErrNotFound) {
				return accountPreferencesResponse{}, err
			}
			response.Sandbox.SourceAccountLogin = identity.OAuthLogin
			available, err := s.githubInstallationLinkedToAccount(value.SourceAccountID, scope.ID)
			if err != nil {
				return accountPreferencesResponse{}, err
			}
			response.Sandbox.SourceAvailable = available
			if !available {
				return s.fillSandboxResolvedSource(response, scope)
			}
			response, err = s.fillSandboxResponseFromScope(response, accountPreferenceScope{Type: state.AccountScopeTypeAccount, ID: value.SourceAccountID})
			if err != nil {
				return accountPreferencesResponse{}, err
			}
			return s.fillSandboxResolvedSource(response, scope)
		}
		response.Sandbox.APIURL = value.APIURL
	}
	response, err = s.fillSandboxResponseFromScope(response, scope)
	if err != nil {
		return accountPreferencesResponse{}, err
	}
	return s.fillSandboxResolvedSource(response, scope)
}

func (s *Server) fillCachePreferenceResponse(response accountPreferencesResponse, scope accountPreferenceScope) (accountPreferencesResponse, error) {
	// Always expose the effective operator-owned mapping, even before a cache
	// preference exists. This lets the UI configure Cache S3 when Sandbox is
	// supplied by inheritance or the admin default.
	region, endpoint := "", ""
	if _, snapshot, snapshotErr := s.sandboxServiceForScopeWithDefault(scope); snapshotErr == nil {
		region, endpoint, _ = s.cacheS3SettingsForSandboxAPIURL(snapshot.APIURL)
	}

	preference, err := s.store.GetAccountPreference(scope.Type, scope.ID, accountPreferenceNamespaceCache, accountPreferenceKeyCacheS3)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			response.Cache.Region = region
			response.Cache.Endpoint = endpoint
			return response, nil
		}
		return accountPreferencesResponse{}, err
	}
	var value accountCacheServicePreferenceValue
	if err := json.Unmarshal([]byte(preference.ValueJSON), &value); err != nil {
		return accountPreferencesResponse{}, err
	}
	accessKey, accessErr := s.store.GetAccountSecret(scope.Type, scope.ID, state.AccountSecretTypeCacheAccessKeyID)
	_, secretErr := s.store.GetAccountSecret(scope.Type, scope.ID, state.AccountSecretTypeCacheSecretAccessKey)
	if errors.Is(accessErr, state.ErrNotFound) || errors.Is(secretErr, state.ErrNotFound) {
		response.Cache.Region = region
		response.Cache.Endpoint = endpoint
		return response, nil
	}
	if accessErr != nil {
		return accountPreferencesResponse{}, accessErr
	}
	if secretErr != nil {
		return accountPreferencesResponse{}, secretErr
	}
	response.Cache = accountCachePreference{
		Configured: true,
		Region:     region,
		Bucket:     value.Bucket,
		Prefix:     value.Prefix,
		Endpoint:   endpoint,
		UpdatedAt:  accessKey.UpdatedAt.Format(time.RFC3339),
	}
	return response, nil
}

func (s *Server) fillSandboxResolvedSource(response accountPreferencesResponse, scope accountPreferenceScope) (accountPreferencesResponse, error) {
	_, snapshot, err := s.sandboxServiceForScopeWithDefault(scope)
	if err != nil {
		if errors.Is(err, errSandboxServiceNotConfigured) {
			response.Sandbox.ResolvedSource = "none"
			return response, nil
		}
		return accountPreferencesResponse{}, err
	}
	switch snapshot.Source {
	case sandboxConfigSourceInheritedAccount:
		response.Sandbox.ResolvedSource = "inherited"
	case sandboxConfigSourceAdminDefault:
		response.Sandbox.ResolvedSource = sandboxConfigSourceAdminDefault
	default:
		response.Sandbox.ResolvedSource = "custom"
	}
	return response, nil
}

func (s *Server) inheritedSandboxSourceAccountID(scope accountPreferenceScope, currentAccountID int64, replace bool) (int64, error) {
	if replace {
		return currentAccountID, nil
	}
	preference, err := s.store.GetAccountPreference(scope.Type, scope.ID, accountPreferenceNamespaceSandbox, accountPreferenceKeySandboxService)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return currentAccountID, nil
		}
		return 0, err
	}
	var value accountSandboxServicePreferenceValue
	if err := json.Unmarshal([]byte(preference.ValueJSON), &value); err != nil {
		return 0, err
	}
	if normalizeSandboxPreferenceMode(value.Mode, scope) == sandboxPreferenceModeInherit && value.SourceAccountID > 0 {
		return value.SourceAccountID, nil
	}
	return currentAccountID, nil
}

func (s *Server) sandboxSourceAccountConfigured(accountID int64) (bool, error) {
	preference, err := s.store.GetAccountPreference(state.AccountScopeTypeAccount, accountID, accountPreferenceNamespaceSandbox, accountPreferenceKeySandboxService)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	var value accountSandboxServicePreferenceValue
	if err := json.Unmarshal([]byte(preference.ValueJSON), &value); err != nil {
		return false, err
	}
	if strings.TrimSpace(value.APIURL) == "" {
		return false, nil
	}
	if _, err := s.store.GetAccountSecret(state.AccountScopeTypeAccount, accountID, state.AccountSecretTypeSandboxAPIKey); err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *Server) fillSandboxResponseFromScope(response accountPreferencesResponse, scope accountPreferenceScope) (accountPreferencesResponse, error) {
	if strings.TrimSpace(response.Sandbox.APIURL) == "" {
		preference, err := s.store.GetAccountPreference(scope.Type, scope.ID, accountPreferenceNamespaceSandbox, accountPreferenceKeySandboxService)
		if err != nil {
			if errors.Is(err, state.ErrNotFound) {
				return response, nil
			}
			return accountPreferencesResponse{}, err
		}
		var value accountSandboxServicePreferenceValue
		if err := json.Unmarshal([]byte(preference.ValueJSON), &value); err != nil {
			return accountPreferencesResponse{}, err
		}
		response.Sandbox.APIURL = value.APIURL
	}
	key, err := s.store.GetAccountSecret(scope.Type, scope.ID, state.AccountSecretTypeSandboxAPIKey)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return response, nil
		}
		return accountPreferencesResponse{}, err
	}
	response.Sandbox.APIKey.Configured = true
	response.Sandbox.APIKey.UpdatedAt = key.UpdatedAt.Format(time.RFC3339)
	return response, nil
}

const (
	sandboxPreferenceModeCustom  = "custom"
	sandboxPreferenceModeInherit = "inherit"
)

func normalizeSandboxPreferenceMode(mode string, scope accountPreferenceScope) string {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode == sandboxPreferenceModeInherit && scope.Type == state.AccountScopeTypeGitHubInstall {
		return sandboxPreferenceModeInherit
	}
	return sandboxPreferenceModeCustom
}

func (s *Server) accountPreferenceScopeFromRequest(accountID int64, r *http.Request) (accountPreferenceScope, error) {
	installationIDText := strings.TrimSpace(r.URL.Query().Get("installation_id"))
	if installationIDText == "" {
		return accountPreferenceScope{Type: state.AccountScopeTypeAccount, ID: accountID}, nil
	}
	installationID, err := strconv.ParseInt(installationIDText, 10, 64)
	if err != nil || installationID <= 0 {
		return accountPreferenceScope{}, errors.New("invalid installation_id")
	}
	installation, ok, err := s.githubInstallationForAccount(accountID, installationID)
	if err != nil {
		return accountPreferenceScope{}, err
	} else if !ok {
		return accountPreferenceScope{}, errors.New("github installation not found")
	}
	return accountPreferenceScope{Type: state.AccountScopeTypeGitHubInstall, ID: installation.InstallationID}, nil
}

func (s *Server) accountPreferenceScopeManageable(ctx context.Context, viewerAccountID int64, scope accountPreferenceScope) (bool, error) {
	switch scope.Type {
	case state.AccountScopeTypeAccount:
		return scope.ID == viewerAccountID, nil
	case state.AccountScopeTypeGitHubInstall:
	default:
		return false, nil
	}

	owner, err := s.store.GitHubInstallationAccountForInstallation(scope.ID)
	if err != nil {
		if !errors.Is(err, state.ErrNotFound) {
			return false, err
		}
		installations, listErr := s.store.ListGitHubInstallations(viewerAccountID)
		if listErr != nil {
			return false, listErr
		}
		for _, installation := range installations {
			if installation.InstallationID == scope.ID {
				owner = state.GitHubInstallationAccount{
					GitHubAccountID: installation.GitHubAccountID,
					AccountType:     installation.AccountType,
					AccountLogin:    installation.AccountLogin,
				}
				break
			}
		}
	}

	manageable, err := s.githubInstallationAccountsManageable(
		ctx,
		viewerAccountID,
		[]state.GitHubInstallationAccount{owner},
	)
	if err != nil {
		return false, err
	}
	return manageable[0], nil
}

const githubOrganizationOwnerRole = "admin"

func (s *Server) githubInstallationAccountsManageable(
	ctx context.Context,
	viewerAccountID int64,
	owners []state.GitHubInstallationAccount,
) ([]bool, error) {
	manageable := make([]bool, len(owners))
	var needsUserIdentity bool
	var needsOrganizationMemberships bool
	for _, owner := range owners {
		switch strings.ToLower(strings.TrimSpace(owner.AccountType)) {
		case "user":
			needsUserIdentity = true
		case "organization":
			needsOrganizationMemberships = true
		}
	}

	if needsUserIdentity {
		identity, err := s.store.GetOAuthIdentityForAccount(viewerAccountID, "github")
		if err != nil {
			return manageable, err
		}
		subject := strings.TrimSpace(identity.OAuthSubject)
		login := strings.TrimSpace(identity.OAuthLogin)
		for i, owner := range owners {
			if !strings.EqualFold(strings.TrimSpace(owner.AccountType), "user") {
				continue
			}
			if owner.GitHubAccountID > 0 {
				manageable[i] = subject == strconv.FormatInt(owner.GitHubAccountID, 10)
			} else {
				manageable[i] = strings.EqualFold(login, strings.TrimSpace(owner.AccountLogin))
			}
		}
	}

	if !needsOrganizationMemberships {
		return manageable, nil
	}
	if s.gh == nil {
		return manageable, errors.New("github client is not configured")
	}
	token, err := s.githubUserAccessToken(viewerAccountID)
	if err != nil {
		return manageable, err
	}
	memberships, err := s.gh.ListUserOrganizationMemberships(ctx, token)
	if err != nil {
		return manageable, err
	}
	organizationIDs := make(map[int64]struct{}, len(memberships))
	organizationLogins := make(map[string]struct{}, len(memberships))
	for _, membership := range memberships {
		if !strings.EqualFold(strings.TrimSpace(membership.Role), githubOrganizationOwnerRole) {
			continue
		}
		organizationIDs[membership.OrganizationID] = struct{}{}
		organizationLogins[strings.ToLower(strings.TrimSpace(membership.OrganizationLogin))] = struct{}{}
	}
	for i, owner := range owners {
		if !strings.EqualFold(strings.TrimSpace(owner.AccountType), "organization") {
			continue
		}
		_, matchesID := organizationIDs[owner.GitHubAccountID]
		_, matchesLogin := organizationLogins[strings.ToLower(strings.TrimSpace(owner.AccountLogin))]
		if owner.GitHubAccountID > 0 {
			manageable[i] = matchesID
		} else {
			manageable[i] = matchesLogin
		}
	}
	return manageable, nil
}

func normalizeHTTPURL(rawURL string) (string, error) {
	value := strings.TrimSpace(rawURL)
	if value == "" {
		return "", errors.New("api_url is required")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", errors.New("api_url must be an absolute URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("api_url must use http or https")
	}
	return parsed.String(), nil
}

func (s *Server) githubAppInstallURL() string {
	slug := strings.TrimSpace(s.cfg.GitHubAppSlug)
	if slug == "" {
		return ""
	}
	return "/github-app/install"
}

func (s *Server) githubAppInstallationURL(setupState string) string {
	slug := strings.TrimSpace(s.cfg.GitHubAppSlug)
	if slug == "" {
		return ""
	}
	target := "https://github.com/apps/" + url.PathEscape(slug) + "/installations/new"
	if strings.TrimSpace(setupState) == "" {
		return target
	}
	values := url.Values{"state": {strings.TrimSpace(setupState)}}
	return target + "?" + values.Encode()
}

func (s *Server) githubInstallationForAccount(accountID, id int64) (state.GitHubInstallation, bool, error) {
	installations, err := s.store.ListGitHubInstallations(accountID)
	if err != nil {
		return state.GitHubInstallation{}, false, err
	}
	for _, installation := range installations {
		if installation.ID == id {
			return installation, true, nil
		}
	}
	return state.GitHubInstallation{}, false, nil
}

func (s *Server) syncGitHubInstallation(ctx context.Context, accountID, installationID int64) (state.GitHubInstallation, error) {
	if s.gh == nil {
		return state.GitHubInstallation{}, errors.New("github client is not configured")
	}
	installation, err := s.gh.GetInstallation(ctx, installationID)
	if err != nil {
		return state.GitHubInstallation{}, err
	}
	return s.store.UpsertGitHubInstallation(state.GitHubInstallation{
		AccountID:       accountID,
		InstallationID:  installation.ID,
		GitHubAccountID: installation.AccountID,
		AccountType:     installation.AccountType,
		AccountLogin:    installation.AccountLogin,
		AccountName:     installation.AccountName,
		AccountAvatar:   installation.AccountAvatar,
	})
}

func (s *Server) validGitHubAppSetupState(r *http.Request, setupState string) bool {
	setupState = strings.TrimSpace(setupState)
	cookie, err := r.Cookie(githubAppSetupStateCookieName)
	if err != nil || setupState == "" || strings.TrimSpace(cookie.Value) == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(setupState), []byte(cookie.Value)) == 1
}
