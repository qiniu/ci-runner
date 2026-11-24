package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/sandboxrunner"
	"github.com/qiniu/ci-runner/internal/state"
)

func cacheRefreshFixture(t *testing.T) (*Server, string, state.RunnerState) {
	t.Helper()
	store := state.New(t.TempDir())
	s := newTestServerWithoutLoops(t, store, "", &fakeSandbox{}, 1)
	s.cfg.SandboxTimeout = 24 * time.Hour
	s.cfg.GitHubOAuthRedirectURL = "https://runner.example.test/auth/github/callback"
	s.cfg.CacheSTSEndpoint = "https://sts.example.test"
	ak, _ := encryptSecret("owner-ak", s.cfg.AuthEncryptionKey.Value())
	sk, _ := encryptSecret("owner-sk", s.cfg.AuthEncryptionKey.Value())
	_, err := store.UpsertAccountPreferenceAndSecrets(state.AccountPreference{ScopeType: "github_installation", ScopeID: 17, Namespace: "cache", Key: "s3", ValueJSON: `{"bucket":"cache-bucket"}`},
		state.AccountSecret{ScopeType: "github_installation", ScopeID: 17, KeyType: state.AccountSecretTypeCacheAccessKeyID, EncryptedValue: ak},
		state.AccountSecret{ScopeType: "github_installation", ScopeID: 17, KeyType: state.AccountSecretTypeCacheSecretAccessKey, EncryptedValue: sk})
	if err != nil {
		t.Fatal(err)
	}
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "refresh", GitHubInstallationID: 17, RepositoryFullName: "owner/repo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.Status, st.ProvisionAttemptID, st.SandboxID, st.ProcessPID = state.StatusRunning, "attempt-a", "sandbox-a", 123
	st.SandboxAPIURL = "https://cn-yangzhou-1-sandbox.qiniuapi.com"
	st.LastAttemptAt = time.Now().Add(-23 * time.Hour)
	if err := store.WriteState(st); err != nil {
		t.Fatal(err)
	}
	st, _ = store.ReadState(st.ID)
	req, _ := store.ReadRequest(st.ID)
	storage, err := s.cacheStorageForInstallation(17, st.SandboxAPIURL)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, token, err := s.issueCacheRefresh(req, st, storage, cacheScopeDecision{ReadScopes: []string{"scopes/pr-7", "scopes/branch-main"}}, CacheSTSCredentials{})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://runner.example.test/cache/credentials" {
		t.Fatal(endpoint)
	}
	return s, token, st
}

func requestCacheCredentials(s *Server, token string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, cacheCredentialsPath, nil)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}

func successfulCacheTransport(t *testing.T, calls *atomic.Int32, beforeReturn func()) *http.Client {
	t.Helper()
	return &http.Client{Transport: profileValidationTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		var request qiniuSTSRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if request.DurationSeconds > 3600 || request.DurationSeconds <= 0 {
			t.Errorf("duration=%d", request.DurationSeconds)
		}
		if strings.Contains(request.Policy, "kodo/upload") || !strings.Contains(request.Policy, "scopes/pr-7") {
			t.Errorf("read-only policy changed: %s", request.Policy)
		}
		if beforeReturn != nil {
			beforeReturn()
		}
		body := fmt.Sprintf(`{"data":{"credential":{"access_key":"sts-ak","secret_key":"sts-sk","session_token":"sts-token","expiration":%q}}}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
}

func TestCacheRefreshSupportsLongJobsAndServerRestart(t *testing.T) {
	s, token, _ := cacheRefreshFixture(t)
	var calls atomic.Int32
	s.cacheSTS = successfulCacheTransport(t, &calls, nil)
	// The grant is portable to a second process with the same config/store;
	// no original node's in-memory session or watch ownership is required.
	peer := newTestServerWithoutLoops(t, s.store, "", &fakeSandbox{}, 1)
	peer.cfg, peer.cacheSTS = s.cfg, s.cacheSTS
	for _, server := range []*Server{s, peer} {
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				w := requestCacheCredentials(server, token)
				if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" {
					t.Errorf("status=%d body=%s", w.Code, w.Body.String())
				}
			})
		}
		wg.Wait()
	}
	if calls.Load() != 2 {
		t.Fatalf("STS calls=%d, expected one per process", calls.Load())
	}
	if s.cfg.SandboxTimeout != 24*time.Hour {
		t.Fatal("Sandbox timeout shortened")
	}
}

func TestCacheRefreshRevokesStoppedRetriedExpiredOrChangedConfiguration(t *testing.T) {
	for _, kind := range []string{"stopped", "retry", "manual_stop", "expired", "tampered", "key_rotation", "inflight_stop"} {
		t.Run(kind, func(t *testing.T) {
			s, token, st := cacheRefreshFixture(t)
			var calls atomic.Int32
			s.cacheSTS = successfulCacheTransport(t, &calls, nil)
			switch kind {
			case "stopped":
				st.Status = state.StatusCompleted
			case "retry":
				st.ProvisionAttemptID = "attempt-b"
			case "manual_stop":
				st.ManualStopRequested = true
			case "expired":
				plaintext, _ := decryptSecret(token, s.cacheRefreshKey())
				var grant cacheRefreshGrant
				_ = json.Unmarshal([]byte(plaintext), &grant)
				grant.ExpiresAt = time.Now().Add(-time.Second).Unix()
				encoded, _ := json.Marshal(grant)
				token, _ = encryptSecret(string(encoded), s.cacheRefreshKey())
			case "tampered":
				token = "x" + token
			case "key_rotation":
				s.cfg.AuthEncryptionKey = "other-key"
			case "inflight_stop":
				s.cacheSTS = successfulCacheTransport(t, &calls, func() {
					current, _ := s.store.ReadState(st.ID)
					current.Status = state.StatusStopping
					if err := s.store.WriteState(current); err != nil {
						t.Error(err)
					}
				})
			}
			if kind == "stopped" || kind == "retry" || kind == "manual_stop" {
				if err := s.store.WriteState(st); err != nil {
					t.Fatal(err)
				}
			}
			w := requestCacheCredentials(s, token)
			if w.Code != 401 && w.Code != 403 {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if kind != "inflight_stop" && calls.Load() != 0 {
				t.Fatal("unauthorized STS call")
			}
			if strings.Contains(w.Body.String(), "sts-sk") {
				t.Fatal("returned revoked credential")
			}
		})
	}
}

func TestCacheRefreshConfigEditRevokesCachedCredentials(t *testing.T) {
	s, token, _ := cacheRefreshFixture(t)
	var calls atomic.Int32
	s.cacheSTS = successfulCacheTransport(t, &calls, nil)
	if w := requestCacheCredentials(s, token); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	_, err := s.store.UpsertAccountPreferenceAndSecrets(state.AccountPreference{ScopeType: "github_installation", ScopeID: 17, Namespace: "cache", Key: "s3", ValueJSON: `{"bucket":"different-bucket"}`})
	if err != nil {
		t.Fatal(err)
	}
	if w := requestCacheCredentials(s, token); w.Code != 403 {
		t.Fatalf("status=%d", w.Code)
	}
	if calls.Load() != 1 {
		t.Fatal("called provider after config edit")
	}
}

func TestCacheSTSFailureIsSafeAndRetainedOnceInTimeline(t *testing.T) {
	s, token, st := cacheRefreshFixture(t)
	var calls atomic.Int32
	s.cacheSTS = &http.Client{Transport: profileValidationTransportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader(`{"error":"duration_seconds is too long, exceed 3600; owner-sk sts-token signed-header-secret"}`))}, nil
	})}
	for range 3 {
		w := requestCacheCredentials(s, token)
		if w.Code != 503 || !strings.Contains(w.Body.String(), "duration_exceeded") {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
		for _, secret := range []string{"owner-sk", "sts-token", "signed-header-secret"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("secret in response")
			}
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("failure not backed off: %d", calls.Load())
	}
	events, _, err := s.store.ListRunnerEvents(st.ID, 0, 20)
	if err != nil || len(events) != 1 || events[0].Stage != "cache_sts" || !strings.Contains(events[0].Message, "maximum duration_seconds=3600") {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	if strings.Contains(events[0].Message, "owner-sk") {
		t.Fatal("secret in timeline")
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, adminRequest(http.MethodGet, "/runner_requests/refresh/events", nil))
	if w.Code != 200 || !bytes.Contains(w.Body.Bytes(), []byte("cache_sts")) {
		t.Fatalf("timeline API=%d %s", w.Code, w.Body.String())
	}
}

func TestCacheSTSDurationSupports24HoursWithoutExceedingProviderLimit(t *testing.T) {
	for _, lifetime := range []time.Duration{0, time.Hour, 24 * time.Hour, 48 * time.Hour} {
		if got := cacheSTSDurationSeconds(lifetime); got != 3600 {
			t.Fatalf("%s -> %d", lifetime, got)
		}
	}
}

func TestCacheRefreshAtEndOf24HourGrantAndAfterCredentialExpiry(t *testing.T) {
	s, token, _ := cacheRefreshFixture(t)
	plaintext, err := decryptSecret(token, s.cacheRefreshKey())
	if err != nil {
		t.Fatal(err)
	}
	var grant cacheRefreshGrant
	if err := json.Unmarshal([]byte(plaintext), &grant); err != nil {
		t.Fatal(err)
	}
	// Simulate the original 24-hour grant reaching its final post-job minutes.
	grant.ExpiresAt = time.Now().Add(4 * time.Minute).Unix()
	encoded, _ := json.Marshal(grant)
	token, err = encryptSecret(string(encoded), s.cacheRefreshKey())
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s.cacheSTS = successfulCacheTransport(t, &calls, nil)
	for range 2 {
		w := requestCacheCredentials(s, token)
		if w.Code != 200 {
			t.Fatalf("%d %s", w.Code, w.Body.String())
		}
		var credentials CacheSTSCredentials
		if err := json.Unmarshal(w.Body.Bytes(), &credentials); err != nil {
			t.Fatal(err)
		}
		if credentials.Expiration.After(time.Now().Add(time.Hour)) {
			t.Fatal("credential exceeds one hour")
		}
	}
	if calls.Load() != 1 {
		t.Fatal("post-job credential was not cached")
	}
	// Force the locally cached STS to expire; the still-live grant must renew it.
	s.cacheCredentials.mu.Lock()
	for key, entry := range s.cacheCredentials.entries {
		entry.retryAt = time.Now().Add(-time.Minute)
		s.cacheCredentials.entries[key] = entry
	}
	s.cacheCredentials.mu.Unlock()
	if w := requestCacheCredentials(s, token); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if calls.Load() != 2 {
		t.Fatal("expired STS was reused")
	}
}

func TestCacheRefreshEndpointConfiguration(t *testing.T) {
	s, _, _ := cacheRefreshFixture(t)
	for _, endpoint := range []string{"http://runner.example/cache/credentials", "https://user:pass@runner.example/cache/credentials", "https://runner.example/cache/credentials?token=secret", "https://runner.example/other"} {
		s.cfg.CacheCredentialsURL = endpoint
		if _, err := s.cacheCredentialsURL(); err == nil || strings.Contains(cacheFailureReason(err), endpoint) {
			t.Fatalf("unsafe endpoint: %q, %v", endpoint, err)
		}
	}
	s.cfg.CacheCredentialsURL = "https://cache.example/cache/credentials"
	if got, err := s.cacheCredentialsURL(); err != nil || got != s.cfg.CacheCredentialsURL {
		t.Fatalf("%q %v", got, err)
	}
}

func TestCacheSTSLifecycleRetainsInitialFailureAndInjectsRefresh(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint("failure=", fail), func(t *testing.T) {
			fixture, _, _ := cacheRefreshFixture(t)
			events := &lifecycleEventRecorder{}
			githubServer := newLifecycleGitHubServer(t, events)
			defer githubServer.Close()
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/repos/o/r" {
					writeJSON(w, 200, map[string]string{"full_name": "o/r", "default_branch": "main"})
					return
				}
				githubServer.Config.Handler.ServeHTTP(w, r)
			}))
			defer api.Close()
			upsertLifecycleProfile(t, fixture.store, state.RunnerProfile{Name: "custom", Labels: []string{"self-hosted", "custom"}, TemplateID: "custom-template", Enabled: true, MaxConcurrency: 10})
			sandbox := &lifecycleSandboxService{events: events, startResult: &sandboxrunner.StartResult{}}
			s := newRunnerLifecycleTestServer(t, fixture.store, api.URL, sandbox)
			s.cfg.AuthEncryptionKey = fixture.cfg.AuthEncryptionKey
			s.cfg.SandboxRegions = append(s.cfg.SandboxRegions[:0:0], fixture.cfg.SandboxRegions...)
			// The injected Sandbox fake returns an empty config snapshot. Map
			// that test-only snapshot to the same region as the saved request.
			for _, region := range fixture.cfg.SandboxRegions {
				if region.SandboxAPIURL == "https://cn-yangzhou-1-sandbox.qiniuapi.com" {
					region.SandboxAPIURL = ""
					s.cfg.SandboxRegions = append(s.cfg.SandboxRegions, region)
					break
				}
			}
			s.cfg.SandboxTimeout = 24 * time.Hour
			s.cfg.GitHubOAuthRedirectURL = fixture.cfg.GitHubOAuthRedirectURL
			s.cfg.CacheSTSEndpoint = fixture.cfg.CacheSTSEndpoint
			s.cacheSTS = &http.Client{Transport: profileValidationTransportFunc(func(r *http.Request) (*http.Response, error) {
				var request qiniuSTSRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if request.DurationSeconds != 3600 {
					t.Errorf("duration=%d", request.DurationSeconds)
				}
				status, body := 200, fmt.Sprintf(`{"data":{"credential":{"access_key":"sts-ak","secret_key":"sts-sk","session_token":"sts-token","expiration":%q}}}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
				if fail {
					status, body = 400, `{"error":"duration_seconds is too long, exceed 3600 SECRET"}`
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			createLifecycleRequestWithSandboxAPIURL(t, fixture.store, "initial-cache", "custom", 17, "https://cn-yangzhou-1-sandbox.qiniuapi.com")
			go s.startClaimedRunner(s.loopCtx, queuedRunnerForTest(t, s.store, "initial-cache", "worker-cache"))
			waitForState(t, s.store, "initial-cache", state.StatusRunning)
			inputs := sandbox.startInputs()
			if len(inputs) != 1 || inputs[0].Timeout != 24*time.Hour {
				t.Fatal("24-hour Runner did not start")
			}
			retained, _, err := s.store.ListRunnerEvents("initial-cache", 0, 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, event := range retained {
				if event.Stage == "cache_sts" {
					found = true
					if !strings.Contains(event.Message, "phase=initial_credentials") || !strings.Contains(event.Message, "duration_exceeded") || strings.Contains(event.Message, "SECRET") {
						t.Fatalf("bad diagnostic: %s", event.Message)
					}
				}
			}
			if found != fail {
				t.Fatalf("initial failure retained=%v want=%v", found, fail)
			}
			if fail {
				if inputs[0].CacheCredentialsToken != "" || inputs[0].CacheS3AccessKeyID != "" {
					t.Fatal("injected failed credentials")
				}
			} else {
				if inputs[0].CacheCredentialsToken == "" || inputs[0].CacheS3AccessKeyID != "sts-ak" {
					t.Fatal("missing cache capability or STS")
				}
				if w := requestCacheCredentials(s, inputs[0].CacheCredentialsToken); w.Code != 200 {
					t.Fatalf("injected capability unusable: %d %s", w.Code, w.Body.String())
				}
			}
		})
	}
}

func TestCacheRefreshCallerCancellationDoesNotPoisonSharedCredentials(t *testing.T) {
	s, token, _ := cacheRefreshFixture(t)
	entered := make(chan context.Context, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	s.cacheSTS = &http.Client{Transport: profileValidationTransportFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		entered <- r.Context()
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-release:
		}
		body := fmt.Sprintf(`{"data":{"credential":{"access_key":"sts-ak","secret_key":"sts-sk","session_token":"sts-token","expiration":%q}}}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, cacheCredentialsPath, nil).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+token)
	done := make(chan struct{})
	go func() { defer close(done); s.ServeHTTP(httptest.NewRecorder(), r) }()
	var providerCtx context.Context
	select {
	case providerCtx = <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("STS call did not start")
	}
	cancel()
	if providerCtx.Err() != nil {
		t.Error("caller canceled the shared STS operation")
	}
	close(release)
	<-done
	w := requestCacheCredentials(s, token)
	if w.Code != 200 || calls.Load() != 1 {
		t.Fatalf("next caller poisoned: status=%d calls=%d", w.Code, calls.Load())
	}
}

func TestCacheSTSFailureRetainedAfterCallerCancellation(t *testing.T) {
	s, _, st := cacheRefreshFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s.recordCacheFailure(ctx, st.ID, st.ProvisionAttemptID, "refresh", &cacheSTSError{Reason: "request_timed_out"})
	events, _, err := s.store.ListRunnerEvents(st.ID, 0, 20)
	if err != nil || len(events) != 1 || events[0].Stage != "cache_sts" || !strings.Contains(events[0].Message, "request_timed_out") {
		t.Fatalf("lost failure after timeout: %#v %v", events, err)
	}
}

func TestCacheRefreshEquivalentCapabilityCannotBypassFailureBackoff(t *testing.T) {
	s, token, _ := cacheRefreshFixture(t)
	var calls atomic.Int32
	s.cacheSTS = &http.Client{Transport: profileValidationTransportFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("unavailable"))}, nil
	})}
	// decryptSecret accepts outer whitespace. Equivalent authenticated grants
	// must share the same cache/retained-error budget regardless of formatting.
	for _, presentation := range []string{token, " " + token, token + " ", "  " + token} {
		w := requestCacheCredentials(s, presentation)
		if w.Code != 503 && w.Code != 401 {
			t.Fatalf("status=%d", w.Code)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("equivalent capability bypassed backoff: calls=%d", calls.Load())
	}
}
