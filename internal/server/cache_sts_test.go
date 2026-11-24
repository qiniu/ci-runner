package server

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type cacheSTSRecordingClient struct {
	request *http.Request
}

func (c *cacheSTSRecordingClient) Do(request *http.Request) (*http.Response, error) {
	c.request = request
	return &http.Response{
		StatusCode: http.StatusOK,
		Status:     "200 OK",
		Body:       io.NopCloser(strings.NewReader(`{"data":{"credential":{"access_key":"STS-test","secret_key":"secret","session_token":"token","expiration":"2030-01-01T00:00:00Z"}}}`)),
	}, nil
}

func TestCacheSTSDurationSecondsAddsHeadroom(t *testing.T) {
	if got, want := cacheSTSDurationSeconds(time.Hour), 3600; got != want {
		t.Fatalf("cacheSTSDurationSeconds(1h) = %d, want %d", got, want)
	}
	if got, want := cacheSTSDurationSeconds(30*time.Minute), 2100; got != want {
		t.Fatalf("cacheSTSDurationSeconds(30m) = %d, want %d", got, want)
	}
}

func TestGenerateCacheSTSUsesRequestedDuration(t *testing.T) {
	client := new(cacheSTSRecordingClient)
	ownScope := scopeForBranch("main")
	_, err := generateCacheSTSWithClient(t.Context(), cacheSTSCredentialConfig{
		Bucket:          "cache-bucket",
		AccessKeyID:     "access-key",
		SecretAccessKey: "secret-key",
	}, "gh-actions-cache/example/repo", ownScope, []string{ownScope}, "https://sts.example.test", 3600, client)
	if err != nil {
		t.Fatal(err)
	}
	if client.request == nil {
		t.Fatal("STS request was not captured")
	}
	body, err := io.ReadAll(client.request.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"duration_seconds":3600`) {
		t.Fatalf("STS request does not contain requested duration: %s", body)
	}
}

func TestValidateCachePrefixRejectsInvalidComponents(t *testing.T) {
	for _, prefix := range []string{"", "cache/../repo", "cache//repo", "cache/./repo"} {
		t.Run(prefix, func(t *testing.T) {
			if err := validateCachePrefix(prefix); err == nil {
				t.Fatalf("validateCachePrefix(%q) should reject invalid prefix", prefix)
			}
		})
	}
}

func TestValidateCacheBucketRejectsResourceComponents(t *testing.T) {
	for _, bucket := range []string{"", "*", "name/other", "name/..", "name?"} {
		t.Run(bucket, func(t *testing.T) {
			if err := validateCacheBucket(bucket); err == nil {
				t.Fatalf("validateCacheBucket(%q) should reject invalid bucket", bucket)
			}
		})
	}
}

func TestGenerateCacheSTSRejectsResourceWildcards(t *testing.T) {
	for _, prefix := range []string{"gh-actions/*/repo", "gh-actions/?/repo"} {
		t.Run(prefix, func(t *testing.T) {
			_, err := generateCacheSTSWithClient(t.Context(), cacheSTSCredentialConfig{Bucket: "cache-bucket"}, prefix, "scopes/branch-main", []string{"scopes/branch-main"}, "", 3600, &cacheSTSRecordingClient{})
			if err == nil || !strings.Contains(err.Error(), "resource wildcard") {
				t.Fatalf("expected resource wildcard error, got %v", err)
			}
		})
	}
}

func TestGenerateCacheSTSScopedBranch(t *testing.T) {
	client := new(cacheSTSRecordingClient)
	ownScope := scopeForBranch("feature-a")
	defaultScope := scopeForBranch("main")

	_, err := generateCacheSTSWithClient(t.Context(), cacheSTSCredentialConfig{
		Bucket:          "my-bucket",
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
	}, "gh-cache/org/repo", ownScope, []string{ownScope, defaultScope}, "https://sts.example.test", 1800, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body, _ := io.ReadAll(client.request.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, ownScope) {
		t.Fatalf("expected policy to contain ownScope %s, got: %s", ownScope, bodyStr)
	}
	if !strings.Contains(bodyStr, defaultScope) {
		t.Fatalf("expected policy to contain defaultScope %s, got: %s", defaultScope, bodyStr)
	}
	if !strings.Contains(bodyStr, "kodo/upload") {
		t.Fatalf("expected branch policy to allow write actions, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "kodo:prefix") {
		t.Fatalf("expected policy to scope list via kodo:prefix condition, got: %s", bodyStr)
	}
}

func TestGenerateCacheSTSReadOnlyForkPR(t *testing.T) {
	client := new(cacheSTSRecordingClient)
	defaultScope := scopeForBranch("main")

	// ownScope is empty for Fork PR
	_, err := generateCacheSTSWithClient(t.Context(), cacheSTSCredentialConfig{
		Bucket:          "my-bucket",
		AccessKeyID:     "ak",
		SecretAccessKey: "sk",
	}, "gh-cache/org/repo", "", []string{defaultScope}, "https://sts.example.test", 1800, client)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	body, _ := io.ReadAll(client.request.Body)
	bodyStr := string(body)
	if strings.Contains(bodyStr, "kodo/upload") || strings.Contains(bodyStr, "kodo/delete") {
		t.Fatalf("fork PR policy must NOT contain write actions, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "kodo/get") {
		t.Fatalf("fork PR policy should allow read action kodo/get, got: %s", bodyStr)
	}
}

func TestGenerateCacheSTSRequiresReadScope(t *testing.T) {
	_, err := generateCacheSTSWithClient(t.Context(), cacheSTSCredentialConfig{Bucket: "my-bucket"}, "gh-cache/org/repo", "", nil, "https://sts.example.test", 1800, &cacheSTSRecordingClient{})
	if err == nil || !strings.Contains(err.Error(), "read scope") {
		t.Fatalf("expected missing read scope error, got %v", err)
	}
}

func TestCacheSTSFailuresRedactProviderDetails(t *testing.T) {
	for _, tc := range []struct {
		name         string
		status       int
		body, reason string
	}{
		{"duration", 400, `{"error":"duration_seconds is too long, exceed 3600 SECRET"}`, "duration_exceeded"},
		{"denied", 403, "SECRET", "authorization_rejected"},
		{"throttled", 429, "SECRET", "rate_limited"},
		{"unavailable", 503, "SECRET", "service_unavailable"},
		{"invalid-json", 200, "SECRET", "invalid_response"},
		{"incomplete", 200, `{"data":{"credential":{"access_key":"SECRET"}}}`, "incomplete_credentials"},
		{"expired", 200, `{"data":{"credential":{"access_key":"SECRET","secret_key":"SECRET","session_token":"SECRET","expiration":"2000-01-01T00:00:00Z"}}}`, "credentials_expired_or_expiring"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: profileValidationTransportFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			_, err := generateCacheSTS(t.Context(), cacheSTSCredentialConfig{Bucket: "bucket", AccessKeyID: "ak", SecretAccessKey: "SECRET"}, "cache/o/r", "scopes/branch-main", []string{"scopes/branch-main"}, "https://sts.example", 3600, client)
			if err == nil || !strings.Contains(err.Error(), tc.reason) || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("unsafe or missing failure: %v", err)
			}
		})
	}
}
