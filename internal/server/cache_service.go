package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/qiniu/ci-runner/internal/state"
)

// cacheS3 holds the S3 configuration and raw long-term credentials used to
// mint short-lived STS tokens for sandbox runners. The S3 endpoint may be
// reachable only from the Sandbox, so runnerd does not create an S3 client or
// perform network validation against it.
type cacheS3 struct {
	scope          accountPreferenceScope
	bucket, prefix string
	region         string
	endpoint       string
	accessKeyID    string
	secretKey      string
}

type cacheS3Config struct {
	Region          string
	Bucket          string
	Prefix          string
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
}

func newCacheS3(config cacheS3Config) (*cacheS3, error) {
	config.Region = strings.TrimSpace(config.Region)
	config.Bucket = strings.TrimSpace(config.Bucket)
	if config.Region == "" || config.Bucket == "" || strings.TrimSpace(config.AccessKeyID) == "" || strings.TrimSpace(config.SecretAccessKey) == "" {
		return nil, errors.New("cache S3 region, bucket, access key, and secret key are required")
	}
	prefix := strings.Trim(strings.TrimSpace(config.Prefix), "/")
	if prefix == "" {
		prefix = "gh-actions-cache"
	}
	return &cacheS3{bucket: config.Bucket, prefix: prefix, region: config.Region, endpoint: config.Endpoint, accessKeyID: config.AccessKeyID, secretKey: config.SecretAccessKey}, nil
}

// cacheStorageForInstallation resolves the cache S3 configuration for a
// GitHub installation ID. Installation-scoped configuration wins; a personal
// installation without its own configuration falls back to the account scope.
func (s *Server) cacheStorageForInstallation(installationID int64, sandboxAPIURL string) (*cacheS3, error) {
	if installationID <= 0 {
		return nil, fmt.Errorf("invalid cache installation id")
	}
	installationScope := accountPreferenceScope{Type: state.AccountScopeTypeGitHubInstall, ID: installationID}
	storage, err := s.cacheStorageForScope(installationScope, sandboxAPIURL)
	if err == nil {
		return storage, nil
	}
	if !errors.Is(err, state.ErrCacheServiceNotConfigured) {
		return nil, err
	}
	accountID, ok, err := s.store.AccountScopeForPersonalGitHubInstallation(installationID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, state.ErrCacheServiceNotConfigured
	}
	return s.cacheStorageForScope(accountPreferenceScope{Type: state.AccountScopeTypeAccount, ID: accountID}, sandboxAPIURL)
}

func (s *Server) cacheStorageForScope(scope accountPreferenceScope, sandboxAPIURL string) (*cacheS3, error) {
	preference, err := s.store.GetAccountPreference(scope.Type, scope.ID, accountPreferenceNamespaceCache, accountPreferenceKeyCacheS3)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, state.ErrCacheServiceNotConfigured
		}
		return nil, err
	}
	var value accountCacheServicePreferenceValue
	if err := json.Unmarshal([]byte(preference.ValueJSON), &value); err != nil {
		return nil, err
	}
	// Region and endpoint are operator-owned mappings. Derive them from the
	// Sandbox configuration already resolved for this runner so preferences only
	// contain the user's bucket and prefix.
	mappedRegion, mappedEndpoint, mapped := s.cacheS3SettingsForSandboxAPIURL(sandboxAPIURL)
	if !mapped || mappedRegion == "" || mappedEndpoint == "" {
		return nil, state.ErrCacheServiceNotConfigured
	}
	accessKey, err := s.store.GetAccountSecret(scope.Type, scope.ID, state.AccountSecretTypeCacheAccessKeyID)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, state.ErrCacheServiceNotConfigured
		}
		return nil, err
	}
	secretKey, err := s.store.GetAccountSecret(scope.Type, scope.ID, state.AccountSecretTypeCacheSecretAccessKey)
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return nil, state.ErrCacheServiceNotConfigured
		}
		return nil, err
	}
	accessKeyValue, err := decryptSecret(accessKey.EncryptedValue, s.cfg.AuthEncryptionKey.Value())
	if err != nil {
		return nil, err
	}
	secretKeyValue, err := decryptSecret(secretKey.EncryptedValue, s.cfg.AuthEncryptionKey.Value())
	if err != nil {
		return nil, err
	}
	storage, err := newCacheS3(cacheS3Config{
		Region: mappedRegion, Bucket: value.Bucket, Prefix: value.Prefix, Endpoint: mappedEndpoint,
		AccessKeyID: accessKeyValue, SecretAccessKey: secretKeyValue,
	})
	if storage != nil {
		storage.scope = scope
	}
	return storage, err
}

func (s *Server) cacheS3SettingsForSandboxAPIURL(apiURL string) (string, string, bool) {
	normalized := strings.TrimRight(strings.TrimSpace(apiURL), "/")
	for _, region := range s.cfg.SandboxRegions {
		if !strings.EqualFold(strings.TrimRight(strings.TrimSpace(region.SandboxAPIURL), "/"), normalized) {
			continue
		}
		endpoint := strings.TrimSpace(region.S3Endpoint)
		if endpoint != "" && !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			endpoint = "https://" + endpoint
		}
		return strings.TrimSpace(region.S3Region), endpoint, true
	}
	return "", "", false
}

// handleSandboxRegions returns the public Sandbox region catalog configured in
// runnerd.yaml. S3 mappings remain server-only because endpoints can be private
// to the Sandbox network.
func (s *Server) handleSandboxRegions(w http.ResponseWriter, r *http.Request) {
	type regionEntry struct {
		ID            string `json:"id"`
		Label         string `json:"label"`
		SandboxAPIURL string `json:"api_url"`
	}
	entries := make([]regionEntry, 0, len(s.cfg.SandboxRegions))
	for _, region := range s.cfg.SandboxRegions {
		id := strings.TrimSpace(region.ID)
		if id == "" {
			continue
		}
		entries = append(entries, regionEntry{
			ID:            id,
			Label:         strings.TrimSpace(region.Label),
			SandboxAPIURL: strings.TrimSpace(region.SandboxAPIURL),
		})
	}
	writeJSON(w, http.StatusOK, entries)
}
