package server

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
	"golang.org/x/sync/singleflight"
)

const cacheCredentialsPath = "/cache/credentials"

// The encrypted capability carries no credentials. Its immutable policy is
// independent of mutable branch/PR metadata, server restarts and watch takeover.
type cacheRefreshGrant struct {
	RequestID      string   `json:"request"`
	AttemptID      string   `json:"attempt"`
	InstallationID int64    `json:"installation"`
	Repository     string   `json:"repository"`
	Configuration  string   `json:"configuration"`
	BasePrefix     string   `json:"prefix"`
	WriteScope     string   `json:"write"`
	ReadScopes     []string `json:"read"`
	ExpiresAt      int64    `json:"expires"`
}

type cachedCacheCredentials struct {
	credentials CacheSTSCredentials
	err         error
	retryAt     time.Time
	retainUntil time.Time
}

// This bounded cache only coalesces STS calls. Authorization always reads the
// database; another server can service a valid capability without this cache.
type cacheCredentialsCache struct {
	mu      sync.Mutex
	entries map[[32]byte]cachedCacheCredentials
	group   singleflight.Group
}

func (c *cacheCredentialsCache) get(key [32]byte, now time.Time) (cachedCacheCredentials, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	return entry, ok && now.Before(entry.retryAt) && now.Before(entry.retainUntil)
}

func (c *cacheCredentialsCache) put(key [32]byte, entry cachedCacheCredentials) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[[32]byte]cachedCacheCredentials)
	}
	for k, old := range c.entries {
		if time.Now().After(old.retainUntil) {
			delete(c.entries, k)
		}
	}
	if len(c.entries) >= 1024 {
		var oldest [32]byte
		var expires time.Time
		for k, old := range c.entries {
			if expires.IsZero() || old.retainUntil.Before(expires) {
				oldest, expires = k, old.retainUntil
			}
		}
		delete(c.entries, oldest)
	}
	c.entries[key] = entry
}

// Keep short-lived credentials reusable while the SDK asks for refresh below
// five minutes. Never reuse them inside the client's one-minute expiry margin.
func cacheCredentialsRetryAt(expiration time.Time) time.Time {
	return minTime(expiration.Add(-time.Minute), time.Now().Add(max(30*time.Second, time.Until(expiration)-cacheSTSHeadroom)))
}

func cacheConfigurationFingerprint(storage *cacheS3) string {
	// Include the resolved scope and credentials so deleting, editing, rotating,
	// or switching the source revokes the old capability instead of widening it.
	encoded, _ := json.Marshal([]any{
		storage.scope.Type, storage.scope.ID, storage.bucket, storage.prefix,
		storage.region, storage.endpoint, storage.accessKeyID, storage.secretKey,
	})
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func (s *Server) cacheCredentialsURL() (string, error) {
	value := s.cfg.CacheCredentialsURL
	if value == "" {
		u, err := url.Parse(s.cfg.GitHubOAuthRedirectURL)
		if err != nil || u.Host == "" {
			return "", &cacheSTSError{Reason: "refresh_endpoint_unavailable"}
		}
		u.Path, u.RawPath, u.RawQuery, u.Fragment = cacheCredentialsPath, "", "", ""
		value = u.String()
	}
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != cacheCredentialsPath {
		return "", &cacheSTSError{Reason: "refresh_endpoint_invalid"}
	}
	return u.String(), nil
}

func (s *Server) cacheRefreshKey() string {
	if s.cfg.AuthEncryptionKey.Value() == "" {
		return ""
	}
	// Domain separation prevents stored account ciphertext being used here.
	return "runnerd-cache-refresh-v1\x00" + s.cfg.AuthEncryptionKey.Value()
}

func (s *Server) issueCacheRefresh(req state.RunnerRequest, st state.RunnerState, storage *cacheS3, decision cacheScopeDecision, initial CacheSTSCredentials) (string, string, error) {
	endpoint, err := s.cacheCredentialsURL()
	if err != nil {
		return "", "", err
	}
	if st.ProvisionAttemptID == "" || req.GitHubInstallationID <= 0 {
		return "", "", errors.New("cache refresh requires an execution attempt")
	}
	lifetime := s.cfg.SandboxTimeout
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	grant := cacheRefreshGrant{
		RequestID: req.ID, AttemptID: st.ProvisionAttemptID, InstallationID: req.GitHubInstallationID,
		Repository: req.RepositoryFullName, Configuration: cacheConfigurationFingerprint(storage),
		BasePrefix: storage.prefix + "/" + req.RepositoryFullName, WriteScope: decision.WriteScope,
		ReadScopes: decision.ReadScopes, ExpiresAt: time.Now().Add(lifetime + cacheSTSHeadroom).Unix(),
	}
	encoded, err := json.Marshal(grant)
	if err != nil {
		return "", "", err
	}
	token, err := encryptSecret(string(encoded), s.cacheRefreshKey())
	if err != nil {
		return "", "", err
	}
	s.cacheCredentials.put(sha256.Sum256(encoded), cachedCacheCredentials{
		credentials: initial, retryAt: cacheCredentialsRetryAt(initial.Expiration), retainUntil: time.Unix(grant.ExpiresAt, 0),
	})
	return endpoint, token, nil
}

func (s *Server) validateCacheGrant(grant cacheRefreshGrant) (*cacheS3, error) {
	if grant.RequestID == "" || grant.AttemptID == "" || grant.InstallationID <= 0 || time.Now().Unix() >= grant.ExpiresAt {
		return nil, state.ErrConflict
	}
	st, err := s.store.ReadState(grant.RequestID)
	if err != nil {
		return nil, err
	}
	if st.Status != state.StatusRunning || st.ManualStopRequested || st.ProvisionAttemptID != grant.AttemptID || st.SandboxID == "" || st.ProcessPID == 0 {
		return nil, state.ErrConflict
	}
	req, err := s.store.ReadRequest(grant.RequestID)
	if err != nil {
		return nil, err
	}
	if req.GitHubInstallationID != grant.InstallationID || req.RepositoryFullName != grant.Repository {
		return nil, state.ErrConflict
	}
	storage, err := s.cacheStorageForInstallation(grant.InstallationID, req.SandboxAPIURL)
	if err != nil {
		return nil, err
	}
	if cacheConfigurationFingerprint(storage) != grant.Configuration || grant.BasePrefix != storage.prefix+"/"+req.RepositoryFullName {
		return nil, state.ErrConflict
	}
	return storage, nil
}

func (s *Server) handleCacheCredentials(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	// Credentials are never accepted in URLs or cookies. Refuse ambiguous
	// transports so access logs cannot accidentally persist a bearer token.
	authorization := r.Header.Get("Authorization")
	if r.URL.RawQuery != "" || len(authorization) > 16384 || !strings.HasPrefix(authorization, "Bearer ") {
		writeError(w, http.StatusUnauthorized, "invalid cache capability")
		return
	}
	token := strings.TrimPrefix(authorization, "Bearer ")
	plaintext, err := decryptSecret(token, s.cacheRefreshKey())
	var grant cacheRefreshGrant
	if err != nil || json.Unmarshal([]byte(plaintext), &grant) != nil {
		writeError(w, http.StatusUnauthorized, "invalid cache capability")
		return
	}
	storage, err := s.validateCacheGrant(grant)
	if err != nil {
		writeError(w, http.StatusForbidden, "cache capability is no longer available")
		return
	}
	// Key the authenticated payload, not its presentation: decryptSecret accepts
	// equivalent encodings, which must not bypass coalescing or failure backoff.
	key := sha256.Sum256([]byte(plaintext))
	result, err, _ := s.cacheCredentials.group.Do(fmt.Sprintf("%x", key), func() (any, error) {
		if entry, ok := s.cacheCredentials.get(key, time.Now()); ok {
			return entry.credentials, entry.err
		}
		// A disconnected caller must not cancel another caller's shared refresh.
		// Provider work remains bounded even if every caller disconnects.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
		defer cancel()
		// Refresh lifetime is independent of job lifetime, including the final
		// post-job save. The grant controls issuance, not revocation of STS
		// credentials already issued.
		duration := int(cacheSTSMaxDuration.Seconds())
		credentials, err := generateCacheSTS(ctx, cacheSTSCredentialConfig{
			Bucket: storage.bucket, AccessKeyID: storage.accessKeyID, SecretAccessKey: storage.secretKey,
		}, grant.BasePrefix, grant.WriteScope, grant.ReadScopes, s.cfg.CacheSTSEndpoint, duration, s.cacheSTS)
		entry := cachedCacheCredentials{credentials: credentials, err: err, retryAt: cacheCredentialsRetryAt(credentials.Expiration), retainUntil: time.Unix(grant.ExpiresAt, 0)}
		if err != nil {
			entry.retryAt = time.Now().Add(30 * time.Second)
			// Recheck before retaining evidence from an external call.
			if _, currentErr := s.validateCacheGrant(grant); currentErr == nil {
				s.recordCacheFailure(r.Context(), grant.RequestID, grant.AttemptID, "refresh", err)
			}
		}
		s.cacheCredentials.put(key, entry)
		return credentials, err
	})
	// Never return a credential after an intervening stop, retry or config edit.
	if _, currentErr := s.validateCacheGrant(grant); currentErr != nil {
		writeError(w, http.StatusForbidden, "cache capability is no longer available")
		return
	}
	if err != nil {
		w.Header().Set("Retry-After", "30")
		writeError(w, http.StatusServiceUnavailable, cacheFailureReason(err))
		return
	}
	writeJSON(w, http.StatusOK, result.(CacheSTSCredentials))
}
