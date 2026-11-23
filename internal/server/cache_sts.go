package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/qiniu/go-sdk/v7/auth"
)

const (
	cacheSTSHeadroom    = 5 * time.Minute
	cacheSTSMaxDuration = time.Hour
)

// Never retain provider bodies or transport errors: they may echo credentials,
// signed headers, private endpoints, or arbitrary upstream text.
type cacheSTSError struct {
	Reason        string
	HTTPStatus    int
	DurationLimit int
}

func (e *cacheSTSError) Error() string {
	message := "cache STS " + e.Reason
	if e.HTTPStatus != 0 {
		message += fmt.Sprintf(" (HTTP %d)", e.HTTPStatus)
	}
	if e.DurationLimit > 0 {
		message += fmt.Sprintf("; maximum duration_seconds=%d", e.DurationLimit)
	}
	return message
}

var cacheSTSDurationLimitPattern = regexp.MustCompile(`duration_seconds is too long, exceed ([0-9]{1,6})\b`)

func cacheSTSProviderError(status int, body []byte) error {
	reason := "provider_rejected"
	limit := 0
	if match := cacheSTSDurationLimitPattern.FindSubmatch(body); len(match) == 2 {
		limit, _ = strconv.Atoi(string(match[1]))
		reason = "duration_exceeded"
	} else if status == http.StatusUnauthorized || status == http.StatusForbidden {
		reason = "authorization_rejected"
	} else if status == http.StatusTooManyRequests {
		reason = "rate_limited"
	} else if status >= 500 {
		reason = "service_unavailable"
	}
	return &cacheSTSError{Reason: reason, HTTPStatus: status, DurationLimit: limit}
}

// CacheSTSCredentials represents temporary S3 credentials generated for a sandbox.
type CacheSTSCredentials struct {
	AccessKeyID     string    `json:"access_key_id"`
	SecretAccessKey string    `json:"secret_access_key"`
	SessionToken    string    `json:"session_token"`
	Expiration      time.Time `json:"expiration"`
}

// qiniuSTSRequest mirrors the Qiniu IAM STS request body. The Policy field is
// a JSON-encoded string (double encoding).
type qiniuSTSRequest struct {
	Name            string `json:"name"`
	DurationSeconds int    `json:"duration_seconds"`
	Policy          string `json:"policy"`
}

type qiniuSTSResponse struct {
	Code    int           `json:"code,omitempty"`
	Error   string        `json:"error,omitempty"`
	Message string        `json:"message,omitempty"`
	Data    *qiniuSTSData `json:"data"`
}

type qiniuSTSData struct {
	Credential qiniuSTSCredential `json:"credential"`
}

type qiniuSTSCredential struct {
	AccessKey    string `json:"access_key"`
	SecretKey    string `json:"secret_key"`
	SessionToken string `json:"session_token"`
	Expiration   string `json:"expiration"`
}

type cacheSTSCredentialConfig struct {
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
}

// cacheSTSClient performs the Qiniu-signed HTTP call so the endpoint can be
// swapped in tests.
type cacheSTSClient interface {
	Do(req *http.Request) (*http.Response, error)
}

func scopeForBranch(branch string) string {
	branch = strings.TrimSpace(branch)
	branch = strings.TrimPrefix(branch, "refs/heads/")
	if branch == "" {
		branch = "default"
	}
	h := sha256.Sum256([]byte(branch))
	return fmt.Sprintf("scopes/branch-%x", h[:16])
}

func scopeForPR(prNumber int64) string {
	if prNumber <= 0 {
		return "scopes/pr-unknown"
	}
	return fmt.Sprintf("scopes/pr-%d", prNumber)
}

func validateCacheBucket(bucket string) error {
	bucket = strings.TrimSpace(bucket)
	if bucket == "" || bucket == "." || bucket == ".." || strings.ContainsAny(bucket, "/\\*?") {
		return fmt.Errorf("cache S3 bucket contains an invalid resource component")
	}
	return nil
}

// generateCacheSTS generates a temporary S3-compatible credential via the
// Qiniu IAM federation token API. The session policy uses resource-level
// scope isolation: bucket-level actions (list) are scoped to the bucket,
// object read actions (stat/get) cover all specified readScopes, and object
// write actions (upload/delete/etc.) are strictly restricted to ownScope.
// If ownScope is empty, write permissions are omitted (read-only mode).
func generateCacheSTS(ctx context.Context, config cacheSTSCredentialConfig, cachePrefix, ownScope string, readScopes []string, endpoint string, durationSeconds int, client cacheSTSClient) (CacheSTSCredentials, error) {
	if client == nil {
		return CacheSTSCredentials{}, fmt.Errorf("cache STS HTTP client is required")
	}
	return generateCacheSTSWithClient(ctx, config, cachePrefix, ownScope, readScopes, endpoint, durationSeconds, client)
}

func generateCacheSTSWithClient(ctx context.Context, config cacheSTSCredentialConfig, cachePrefix, ownScope string, readScopes []string, endpoint string, durationSeconds int, client cacheSTSClient) (CacheSTSCredentials, error) {
	bucket := strings.TrimSpace(config.Bucket)
	if err := validateCacheBucket(bucket); err != nil {
		return CacheSTSCredentials{}, err
	}
	cachePrefix = strings.TrimPrefix(strings.TrimSpace(cachePrefix), "/")
	cachePrefix = strings.TrimSuffix(cachePrefix, "/")
	if err := validateCachePrefix(cachePrefix); err != nil {
		return CacheSTSCredentials{}, err
	}

	bucketResource := fmt.Sprintf("qrn:kodo:::bucket/%s", bucket)
	bucketActions := []string{"kodo/list", "kodo/listMultipartUploads"}

	// Collect validated read scopes and derive both the object-level read
	// resources and the kodo:prefix condition values for list scoping.
	readActions := []string{"kodo/stat", "kodo/get"}
	var readResources []string
	var listPrefixValues []string
	seenRead := make(map[string]bool)
	for _, scope := range readScopes {
		scope = strings.Trim(strings.TrimSpace(scope), "/")
		if scope == "" {
			continue
		}
		if err := validateCachePrefix(scope); err != nil {
			return CacheSTSCredentials{}, fmt.Errorf("invalid read scope %q: %w", scope, err)
		}
		res := fmt.Sprintf("qrn:kodo:::bucket/%s/%s/%s/*", bucket, cachePrefix, scope)
		if !seenRead[res] {
			seenRead[res] = true
			readResources = append(readResources, res)
			listPrefixValues = append(listPrefixValues, fmt.Sprintf("%s/%s/*", cachePrefix, scope))
		}
	}
	if len(readResources) == 0 {
		return CacheSTSCredentials{}, fmt.Errorf("at least one cache read scope is required")
	}

	statements := []map[string]any{
		// List operations are scoped to authorized prefixes via kodo:prefix
		// condition so cache key names outside the granted scopes are not
		// enumerable.
		{
			"effect":   "Allow",
			"action":   bucketActions,
			"resource": []string{bucketResource},
			"condition": map[string]any{
				"StringLike": map[string]any{
					"kodo:prefix": listPrefixValues,
				},
			},
		},
		{
			"effect":   "Allow",
			"action":   readActions,
			"resource": readResources,
		},
	}

	ownScope = strings.Trim(strings.TrimSpace(ownScope), "/")
	if ownScope != "" {
		if err := validateCachePrefix(ownScope); err != nil {
			return CacheSTSCredentials{}, fmt.Errorf("invalid own scope %q: %w", ownScope, err)
		}
		writeActions := []string{
			"kodo/upload",
			"kodo/mkfile",
			"kodo/listParts",
			"kodo/abortMultipartUpload",
			"kodo/delete",
		}
		writeResource := fmt.Sprintf("qrn:kodo:::bucket/%s/%s/%s/*", bucket, cachePrefix, ownScope)
		statements = append(statements, map[string]any{
			"effect":   "Allow",
			"action":   writeActions,
			"resource": []string{writeResource},
		})
	}

	policy := map[string]any{
		"statement": statements,
	}
	policyJSON, err := json.Marshal(policy)
	if err != nil {
		return CacheSTSCredentials{}, fmt.Errorf("marshal STS policy: %w", err)
	}
	if durationSeconds <= 0 || durationSeconds > int(cacheSTSMaxDuration.Seconds()) {
		return CacheSTSCredentials{}, &cacheSTSError{Reason: "invalid_requested_duration", DurationLimit: int(cacheSTSMaxDuration.Seconds())}
	}
	body, err := json.Marshal(qiniuSTSRequest{
		Name:            "runnerd-cache",
		DurationSeconds: durationSeconds,
		Policy:          string(policyJSON),
	})
	if err != nil {
		return CacheSTSCredentials{}, fmt.Errorf("marshal STS request: %w", err)
	}

	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return CacheSTSCredentials{}, fmt.Errorf("cache STS endpoint is required")
	}
	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	stsURL := endpoint + "/sts/federation/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, stsURL, bytes.NewReader(body))
	if err != nil {
		return CacheSTSCredentials{}, &cacheSTSError{Reason: "invalid_endpoint"}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Host = req.URL.Host
	cred := auth.New(strings.TrimSpace(config.AccessKeyID), strings.TrimSpace(config.SecretAccessKey))
	authToken, err := cred.SignRequestV2(req)
	if err != nil {
		return CacheSTSCredentials{}, &cacheSTSError{Reason: "request_signing_failed"}
	}
	req.Header.Set("Authorization", "Qiniu "+authToken)

	resp, err := client.Do(req)
	if err != nil {
		reason := "transport_failed"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "request_timed_out"
		} else if errors.Is(err, context.Canceled) {
			reason = "request_canceled"
		}
		return CacheSTSCredentials{}, &cacheSTSError{Reason: reason}
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return CacheSTSCredentials{}, &cacheSTSError{Reason: "response_read_failed"}
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return CacheSTSCredentials{}, cacheSTSProviderError(resp.StatusCode, data)
	}
	var stResp qiniuSTSResponse
	if err := json.Unmarshal(data, &stResp); err != nil {
		return CacheSTSCredentials{}, &cacheSTSError{Reason: "invalid_response"}
	}
	if stResp.Code != 0 || stResp.Error != "" {
		return CacheSTSCredentials{}, cacheSTSProviderError(resp.StatusCode, data)
	}
	if stResp.Data == nil || strings.TrimSpace(stResp.Data.Credential.AccessKey) == "" || strings.TrimSpace(stResp.Data.Credential.SecretKey) == "" || strings.TrimSpace(stResp.Data.Credential.SessionToken) == "" {
		return CacheSTSCredentials{}, &cacheSTSError{Reason: "incomplete_credentials"}
	}
	creds := stResp.Data.Credential
	expiration, err := time.Parse(time.RFC3339, creds.Expiration)
	if err != nil {
		return CacheSTSCredentials{}, &cacheSTSError{Reason: "invalid_expiration"}
	}
	if !expiration.After(time.Now().Add(time.Minute)) {
		return CacheSTSCredentials{}, &cacheSTSError{Reason: "credentials_expired_or_expiring"}
	}
	return CacheSTSCredentials{
		AccessKeyID:     creds.AccessKey,
		SecretAccessKey: creds.SecretKey,
		SessionToken:    creds.SessionToken,
		Expiration:      minTime(expiration, time.Now().Add(time.Duration(durationSeconds)*time.Second)),
	}, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

// Long jobs refresh credentials independently of the Sandbox lifetime.
func cacheSTSDurationSeconds(sandboxTimeout time.Duration) int {
	if sandboxTimeout <= 0 {
		sandboxTimeout = time.Hour
	}
	if sandboxTimeout >= cacheSTSMaxDuration-cacheSTSHeadroom {
		return int(cacheSTSMaxDuration.Seconds())
	}
	return int((sandboxTimeout + cacheSTSHeadroom).Seconds())
}

func validateCachePrefix(prefix string) error {
	if prefix == "" {
		return fmt.Errorf("cache prefix is required for STS prefix isolation")
	}
	for _, component := range strings.Split(prefix, "/") {
		if component == "" || component == "." || component == ".." {
			return fmt.Errorf("cache prefix contains an invalid path component")
		}
		if strings.ContainsAny(component, "*?") {
			return fmt.Errorf("cache prefix contains a resource wildcard")
		}
	}
	return nil
}
