package github

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/qiniu/ci-runner/internal/labelutil"
	"github.com/qiniu/ci-runner/internal/metrics"
	"github.com/qiniu/ci-runner/internal/runnerapplication"
	"golang.org/x/sync/singleflight"
)

type Client struct {
	baseURL           string
	http              *http.Client
	userHTTP          *http.Client
	downloadHTTP      *http.Client
	appAuth           *appAuthenticator
	tokensMu          sync.Mutex
	regTokens         map[string]RegistrationToken
	applicationsMu    sync.Mutex
	applications      map[string]runnerApplicationsCacheEntry
	applicationsGroup singleflight.Group
}

type runnerApplicationsCacheEntry struct {
	applications []RunnerApplication
	expiresAt    time.Time
}

type AppAuth struct {
	AppID          int64
	InstallationID int64
	PrivateKeyFile string
}

type apiError struct {
	Operation          string
	StatusCode         int
	Body               string
	RetryAfter         string
	RateLimitRemaining string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("%s: status %d: %s", e.Operation, e.StatusCode, e.Body)
}

func asAPIError(err error) (*apiError, bool) {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return nil, false
	}
	return apiErr, true
}

// ErrorStatus returns the HTTP status carried by a GitHub API error.
func ErrorStatus(err error) (int, bool) {
	apiErr, ok := asAPIError(err)
	if !ok {
		return 0, false
	}
	return apiErr.StatusCode, true
}

// IsRateLimitError reports whether GitHub rejected the request because of a primary or secondary rate limit.
func IsRateLimitError(err error) bool {
	apiErr, ok := asAPIError(err)
	if !ok {
		return false
	}
	return isRateLimitAPIError(apiErr)
}

func isRateLimitAPIError(apiErr *apiError) bool {
	if apiErr.StatusCode != http.StatusForbidden && apiErr.StatusCode != http.StatusTooManyRequests {
		return false
	}
	return strings.TrimSpace(apiErr.RetryAfter) != "" ||
		strings.TrimSpace(apiErr.RateLimitRemaining) == "0" ||
		strings.Contains(strings.ToLower(apiErr.Body), "rate limit")
}

// RateLimitRetryAfter returns GitHub's Retry-After header for a classified rate-limit response.
func RateLimitRetryAfter(err error) (string, bool) {
	apiErr, ok := asAPIError(err)
	if !ok || !isRateLimitAPIError(apiErr) {
		return "", false
	}
	retryAfter := strings.TrimSpace(apiErr.RetryAfter)
	return retryAfter, retryAfter != ""
}

type appAuthenticator struct {
	baseURL              string
	staticInstallationID int64
	httpClient           *http.Client
	appHTTP              *http.Client
	appTransport         *ghinstallation.AppsTransport
	mu                   sync.Mutex
	installationByRepo   map[string]int64
	transports           map[int64]*ghinstallation.Transport
}

type RegistrationToken struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

type RunnerApplication = runnerapplication.Application

type OrganizationMembership struct {
	OrganizationID    int64
	OrganizationLogin string
	State             string
	Role              string
}

type Runner struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
	Busy   bool   `json:"busy"`
}

type Installation struct {
	ID            int64    `json:"id"`
	AccountID     int64    `json:"account_id,omitempty"`
	AccountType   string   `json:"account_type,omitempty"`
	AccountLogin  string   `json:"account_login,omitempty"`
	AccountName   string   `json:"account_name,omitempty"`
	AccountAvatar string   `json:"account_avatar,omitempty"`
	Repositories  []string `json:"repositories"`
}

type Account struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Login     string `json:"login"`
	Name      string `json:"name,omitempty"`
	AvatarURL string `json:"avatar_url,omitempty"`
}

var (
	ErrAccountNotFound        = errors.New("github account not found")
	ErrUnsupportedAccountType = errors.New("unsupported github account type")
)

type runnerTarget struct {
	repositoryFullName string
	apiPath            string
	webURL             string
	cacheKey           string
}

func (c *Client) GetInstallation(ctx context.Context, installationID int64) (Installation, error) {
	if c.appAuth == nil {
		return Installation{}, fmt.Errorf("github app auth is required")
	}
	if installationID <= 0 {
		return Installation{}, fmt.Errorf("installation id is required")
	}
	url := fmt.Sprintf("%s/app/installations/%d", c.baseURL, installationID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Installation{}, err
	}
	setGitHubHeaders(req)
	resp, err := c.appAuth.appHTTP.Do(req)
	if err != nil {
		return Installation{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Installation{}, fmt.Errorf("read github installation response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Installation{}, fmt.Errorf("github installation: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		ID      int64 `json:"id"`
		Account struct {
			ID        int64  `json:"id"`
			Type      string `json:"type"`
			Login     string `json:"login"`
			Name      string `json:"name"`
			AvatarURL string `json:"avatar_url"`
		} `json:"account"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return Installation{}, err
	}
	if out.ID == 0 {
		return Installation{}, fmt.Errorf("github installation response missing id")
	}
	return Installation{
		ID:            out.ID,
		AccountID:     out.Account.ID,
		AccountType:   normalizeGitHubAccountType(out.Account.Type),
		AccountLogin:  out.Account.Login,
		AccountName:   out.Account.Name,
		AccountAvatar: out.Account.AvatarURL,
	}, nil
}

func (c *Client) GetAccount(ctx context.Context, login string) (Account, error) {
	login = strings.TrimSpace(login)
	if login == "" {
		return Account{}, fmt.Errorf("github account login is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/users/%s", c.baseURL, url.PathEscape(login)), nil)
	if err != nil {
		return Account{}, err
	}
	setGitHubHeaders(req)
	resp, err := c.http.Do(req)
	if err != nil {
		return Account{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Account{}, fmt.Errorf("read github account response: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		return Account{}, fmt.Errorf("%w: %s", ErrAccountNotFound, login)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Account{}, fmt.Errorf("github account: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var account Account
	if err := json.Unmarshal(body, &account); err != nil {
		return Account{}, err
	}
	rawAccountType := account.Type
	account.Type = normalizeGitHubAccountType(rawAccountType)
	account.Login = strings.TrimSpace(account.Login)
	account.Name = strings.TrimSpace(account.Name)
	account.AvatarURL = strings.TrimSpace(account.AvatarURL)
	if account.Type == "" {
		return Account{}, fmt.Errorf("%w: %s", ErrUnsupportedAccountType, rawAccountType)
	}
	if account.ID <= 0 || account.Login == "" {
		return Account{}, fmt.Errorf("github account response has no supported stable identity")
	}
	return account, nil
}

func (c *Client) ListInstallationRepositories(ctx context.Context, installationID int64) ([]string, error) {
	if c.appAuth == nil {
		return nil, fmt.Errorf("github app auth is required")
	}
	if installationID <= 0 {
		return nil, fmt.Errorf("installation id is required")
	}
	client, err := c.appAuth.clientForInstallation(ctx, installationID)
	if err != nil {
		return nil, err
	}
	nextURL := fmt.Sprintf("%s/installation/repositories?per_page=100", c.baseURL)
	var repositories []string
	for nextURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		setGitHubHeaders(req)
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read github installation repositories response: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("github installation repositories: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var out struct {
			Repositories []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		for _, repo := range out.Repositories {
			if fullName := strings.TrimSpace(repo.FullName); fullName != "" {
				repositories = append(repositories, fullName)
			}
		}
		nextURL = nextLink(resp.Header.Get("Link"))
	}
	return repositories, nil
}

// ListUserInstallationRepositories lists repositories accessible to both the user token and installation.
func (c *Client) ListUserInstallationRepositories(ctx context.Context, token string, installationID int64) ([]string, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("github oauth token is required")
	}
	if installationID <= 0 {
		return nil, fmt.Errorf("installation id is required")
	}
	nextURL := fmt.Sprintf("%s/user/installations/%d/repositories?per_page=100", c.baseURL, installationID)
	var repositories []string
	for nextURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		setGitHubHeaders(req)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.userHTTP.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read github user installation repositories response: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, &apiError{
				Operation:          "github user installation repositories",
				StatusCode:         resp.StatusCode,
				Body:               strings.TrimSpace(string(body)),
				RetryAfter:         resp.Header.Get("Retry-After"),
				RateLimitRemaining: resp.Header.Get("X-RateLimit-Remaining"),
			}
		}
		var out struct {
			Repositories []struct {
				FullName string `json:"full_name"`
			} `json:"repositories"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		for _, repo := range out.Repositories {
			if fullName := strings.TrimSpace(repo.FullName); fullName != "" {
				repositories = append(repositories, fullName)
			}
		}
		nextURL = nextLink(resp.Header.Get("Link"))
	}
	return repositories, nil
}

func (c *Client) ListUserInstallations(ctx context.Context, token string) ([]Installation, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("github oauth token is required")
	}
	nextURL := fmt.Sprintf("%s/user/installations?per_page=100", c.baseURL)
	var installations []Installation
	for nextURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		setGitHubHeaders(req)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.userHTTP.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read github user installations response: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, &apiError{
				Operation:          "github user installations",
				StatusCode:         resp.StatusCode,
				Body:               strings.TrimSpace(string(body)),
				RetryAfter:         resp.Header.Get("Retry-After"),
				RateLimitRemaining: resp.Header.Get("X-RateLimit-Remaining"),
			}
		}
		var out struct {
			Installations []struct {
				ID      int64 `json:"id"`
				Account struct {
					ID        int64  `json:"id"`
					Type      string `json:"type"`
					Login     string `json:"login"`
					Name      string `json:"name"`
					AvatarURL string `json:"avatar_url"`
				} `json:"account"`
			} `json:"installations"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		for _, item := range out.Installations {
			if item.ID <= 0 {
				continue
			}
			installations = append(installations, Installation{
				ID:            item.ID,
				AccountID:     item.Account.ID,
				AccountType:   normalizeGitHubAccountType(item.Account.Type),
				AccountLogin:  item.Account.Login,
				AccountName:   item.Account.Name,
				AccountAvatar: item.Account.AvatarURL,
			})
		}
		nextURL = nextLink(resp.Header.Get("Link"))
	}
	return installations, nil
}

func (c *Client) ListUserOrganizationMemberships(ctx context.Context, token string) ([]OrganizationMembership, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, fmt.Errorf("github oauth token is required")
	}
	nextURL := fmt.Sprintf("%s/user/memberships/orgs?state=active&per_page=100", c.baseURL)
	var memberships []OrganizationMembership
	for nextURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		setGitHubHeaders(req)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := c.userHTTP.Do(req)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read github user organization memberships response: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, &apiError{
				Operation:          "github user organization memberships",
				StatusCode:         resp.StatusCode,
				Body:               strings.TrimSpace(string(body)),
				RetryAfter:         resp.Header.Get("Retry-After"),
				RateLimitRemaining: resp.Header.Get("X-RateLimit-Remaining"),
			}
		}
		var out []struct {
			State        string `json:"state"`
			Role         string `json:"role"`
			Organization struct {
				ID    int64  `json:"id"`
				Login string `json:"login"`
			} `json:"organization"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		for _, item := range out {
			state := strings.ToLower(strings.TrimSpace(item.State))
			login := strings.TrimSpace(item.Organization.Login)
			if state != "active" || item.Organization.ID <= 0 || login == "" {
				continue
			}
			memberships = append(memberships, OrganizationMembership{
				OrganizationID:    item.Organization.ID,
				OrganizationLogin: login,
				State:             state,
				Role:              strings.ToLower(strings.TrimSpace(item.Role)),
			})
		}
		nextURL = nextLink(resp.Header.Get("Link"))
	}
	return memberships, nil
}

func normalizeGitHubAccountType(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "user":
		return "user"
	case "organization", "org":
		return "organization"
	default:
		return ""
	}
}

func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return newClient(baseURL, httpClient, httpClient)
}

func newClient(baseURL string, httpClient, downloadHTTP *http.Client) *Client {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if downloadHTTP == nil {
		downloadHTTP = httpClient
	}
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		http:         httpClient,
		userHTTP:     httpClient,
		downloadHTTP: downloadHTTP,
		regTokens:    map[string]RegistrationToken{},
		applications: map[string]runnerApplicationsCacheEntry{},
	}
}

func NewAppClient(baseURL string, auth AppAuth, httpClient *http.Client) (*Client, error) {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	privateKey, err := os.ReadFile(auth.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	baseTransport := httpClient.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	appTransport, err := ghinstallation.NewAppsTransport(baseTransport, auth.AppID, privateKey)
	if err != nil {
		return nil, err
	}
	appTransport.BaseURL = strings.TrimRight(baseURL, "/")
	appHTTP := *httpClient
	appHTTP.Transport = appTransport
	cloned := *httpClient
	cloned.Transport = baseTransport
	client := newClient(baseURL, &cloned, &cloned)
	client.appAuth = &appAuthenticator{
		baseURL:              strings.TrimRight(baseURL, "/"),
		staticInstallationID: auth.InstallationID,
		httpClient:           &cloned,
		appHTTP:              &appHTTP,
		appTransport:         appTransport,
		installationByRepo:   map[string]int64{},
		transports:           map[int64]*ghinstallation.Transport{},
	}
	return client, nil
}

func NewTokenClient(baseURL, token string, httpClient *http.Client) *Client {
	downloadHTTP := cloneHTTPClient(httpClient)
	client := newClient(baseURL, clientWithTransport(downloadHTTP, bearerTransport{
		token: strings.TrimSpace(token),
	}), downloadHTTP)
	client.userHTTP = downloadHTTP
	return client
}

func NewBasicAuthClient(baseURL, username, password string, httpClient *http.Client) *Client {
	downloadHTTP := cloneHTTPClient(httpClient)
	client := newClient(baseURL, clientWithTransport(downloadHTTP, basicAuthTransport{
		username: username,
		password: password,
	}), downloadHTTP)
	client.userHTTP = downloadHTTP
	return client
}

func (c *Client) RunnerURL(repositoryFullName, runnerGroup string) (string, error) {
	target, err := c.runnerTarget(repositoryFullName, runnerGroup)
	if err != nil {
		return "", err
	}
	return target.webURL, nil
}

func (c *Client) CreateRegistrationToken(ctx context.Context, repositoryFullName, runnerGroup string) (RegistrationToken, error) {
	startedAt := time.Now()
	result := "error"
	defer func() { metrics.RecordGitHubAPI("create_registration_token", result, time.Since(startedAt)) }()
	target, err := c.runnerTarget(repositoryFullName, runnerGroup)
	if err != nil {
		return RegistrationToken{}, err
	}
	if token, ok := c.cachedRegistrationToken(target.cacheKey); ok {
		result = "cache_hit"
		return token, nil
	}
	url := fmt.Sprintf("%s/%s/actions/runners/registration-token", c.baseURL, target.apiPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(nil))
	if err != nil {
		return RegistrationToken{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := c.do(req, target.repositoryFullName)
	if err != nil {
		return RegistrationToken{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return RegistrationToken{}, fmt.Errorf("read github registration token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return RegistrationToken{}, fmt.Errorf("github registration token: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var token RegistrationToken
	if err := json.Unmarshal(body, &token); err != nil {
		return RegistrationToken{}, err
	}
	if token.Token == "" {
		return RegistrationToken{}, fmt.Errorf("github registration token response missing token")
	}
	c.storeRegistrationToken(target.cacheKey, token)
	result = "success"
	return token, nil
}

func (c *Client) ListRunnerApplications(ctx context.Context, repositoryFullName, runnerGroup string) ([]RunnerApplication, error) {
	startedAt := time.Now()
	result := "error"
	defer func() { metrics.RecordGitHubAPI("list_runner_applications", result, time.Since(startedAt)) }()
	target, err := c.runnerTarget(repositoryFullName, runnerGroup)
	if err != nil {
		return nil, err
	}
	if applications, ok := c.cachedRunnerApplications(target.cacheKey); ok {
		result = "cache_hit"
		return applications, nil
	}
	value, err, _ := c.applicationsGroup.Do(target.cacheKey, func() (any, error) {
		if applications, ok := c.cachedRunnerApplications(target.cacheKey); ok {
			return applications, nil
		}
		applications, err := c.fetchRunnerApplications(ctx, target)
		if err != nil {
			return nil, err
		}
		c.storeRunnerApplications(target.cacheKey, applications)
		return cloneRunnerApplications(applications), nil
	})
	if err != nil {
		return nil, err
	}
	applications, ok := value.([]RunnerApplication)
	if !ok {
		return nil, fmt.Errorf("github runner applications cache returned an unexpected value")
	}
	result = "success"
	return cloneRunnerApplications(applications), nil
}

func (c *Client) fetchRunnerApplications(ctx context.Context, target runnerTarget) ([]RunnerApplication, error) {
	requestURL := fmt.Sprintf("%s/%s/actions/runners/downloads", c.baseURL, target.apiPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	setGitHubHeaders(req)
	resp, err := c.do(req, target.repositoryFullName)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	const maxResponseBytes = 1 << 20
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read github runner applications response: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("github runner applications response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("github runner applications: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var descriptors []struct {
		OS             string `json:"os"`
		Architecture   string `json:"architecture"`
		DownloadURL    string `json:"download_url"`
		Filename       string `json:"filename"`
		SHA256Checksum string `json:"sha256_checksum"`
	}
	if err := json.Unmarshal(body, &descriptors); err != nil {
		return nil, fmt.Errorf("decode github runner applications response: %w", err)
	}
	applications := make([]RunnerApplication, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if descriptor.OS != "linux" {
			continue
		}
		application, err := runnerapplication.FromDescriptor(
			descriptor.Architecture,
			descriptor.Filename,
			descriptor.DownloadURL,
			descriptor.SHA256Checksum,
		)
		if err != nil {
			return nil, err
		}
		applications = append(applications, application)
	}
	if len(applications) == 0 {
		return nil, fmt.Errorf("github runner applications response contains no supported Linux application")
	}
	return applications, nil
}

func (c *Client) cachedRunnerApplications(key string) ([]RunnerApplication, bool) {
	c.applicationsMu.Lock()
	defer c.applicationsMu.Unlock()
	entry, ok := c.applications[key]
	if !ok || !time.Now().Before(entry.expiresAt) {
		delete(c.applications, key)
		return nil, false
	}
	return cloneRunnerApplications(entry.applications), true
}

func (c *Client) storeRunnerApplications(key string, applications []RunnerApplication) {
	c.applicationsMu.Lock()
	defer c.applicationsMu.Unlock()
	now := time.Now()
	c.applications[key] = runnerApplicationsCacheEntry{
		applications: cloneRunnerApplications(applications),
		expiresAt:    now.Add(10 * time.Minute),
	}
	for cachedKey, cached := range c.applications {
		if !now.Before(cached.expiresAt) {
			delete(c.applications, cachedKey)
		}
	}
}

func cloneRunnerApplications(applications []RunnerApplication) []RunnerApplication {
	return append([]RunnerApplication(nil), applications...)
}

func (c *Client) ListRunners(ctx context.Context, repositoryFullName, runnerGroup string) ([]Runner, error) {
	startedAt := time.Now()
	result := "error"
	defer func() { metrics.RecordGitHubAPI("list_runners", result, time.Since(startedAt)) }()
	target, err := c.runnerTarget(repositoryFullName, runnerGroup)
	if err != nil {
		return nil, err
	}
	nextURL := fmt.Sprintf("%s/%s/actions/runners?per_page=100", c.baseURL, target.apiPath)
	var runners []Runner
	for nextURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		setGitHubHeaders(req)
		resp, err := c.do(req, target.repositoryFullName)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read github list runners response: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("github list runners: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var out struct {
			Runners []Runner `json:"runners"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		runners = append(runners, out.Runners...)
		nextURL = nextLink(resp.Header.Get("Link"))
	}
	result = "success"
	return runners, nil
}

func (c *Client) RemoveRunner(ctx context.Context, repositoryFullName, runnerGroup string, runnerID int64) error {
	startedAt := time.Now()
	result := "error"
	defer func() { metrics.RecordGitHubAPI("remove_runner", result, time.Since(startedAt)) }()
	if runnerID == 0 {
		return fmt.Errorf("runner id is required")
	}
	target, err := c.runnerTarget(repositoryFullName, runnerGroup)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("%s/%s/actions/runners/%d", c.baseURL, target.apiPath, runnerID)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	setGitHubHeaders(req)
	resp, err := c.do(req, target.repositoryFullName)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		result = "not_found"
		return nil
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read github remove runner response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github remove runner: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	result = "success"
	return nil
}

func (c *Client) RemoveRunnerByName(ctx context.Context, repositoryFullName, runnerGroup, runnerName string) (bool, error) {
	runnerName = strings.TrimSpace(runnerName)
	if runnerName == "" {
		return false, nil
	}
	runners, err := c.ListRunners(ctx, repositoryFullName, runnerGroup)
	if err != nil {
		return false, err
	}
	for _, runner := range runners {
		if runner.Name == runnerName {
			return true, c.RemoveRunner(ctx, repositoryFullName, runnerGroup, runner.ID)
		}
	}
	return false, nil
}

func (c *Client) ListWorkflowRunJobs(ctx context.Context, repositoryFullName string, runID int64) ([]WorkflowJob, error) {
	startedAt := time.Now()
	result := "error"
	defer func() { metrics.RecordGitHubAPI("list_workflow_run_jobs", result, time.Since(startedAt)) }()
	repositoryFullName = c.repositoryFullName(repositoryFullName)
	if repositoryFullName == "" {
		return nil, fmt.Errorf("repository full name is required")
	}
	nextURL := fmt.Sprintf("%s/repos/%s/actions/runs/%d/jobs?per_page=100", c.baseURL, repositoryFullName, runID)
	var jobs []WorkflowJob
	for nextURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, nextURL, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

		resp, err := c.do(req, repositoryFullName)
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read github workflow run jobs response: %w", readErr)
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("github workflow run jobs: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		var out struct {
			Jobs []WorkflowJob `json:"jobs"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		jobs = append(jobs, out.Jobs...)
		nextURL = nextLink(resp.Header.Get("Link"))
	}
	result = "success"
	return jobs, nil
}

func (c *Client) GetWorkflowRun(ctx context.Context, repositoryFullName string, runID int64) (WorkflowRun, error) {
	startedAt := time.Now()
	result := "error"
	defer func() { metrics.RecordGitHubAPI("get_workflow_run", result, time.Since(startedAt)) }()
	repositoryFullName = c.repositoryFullName(repositoryFullName)
	if repositoryFullName == "" {
		return WorkflowRun{}, fmt.Errorf("repository full name is required")
	}
	if runID <= 0 {
		return WorkflowRun{}, fmt.Errorf("workflow run id is required")
	}
	url := fmt.Sprintf("%s/repos/%s/actions/runs/%d", c.baseURL, repositoryFullName, runID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return WorkflowRun{}, err
	}
	setGitHubHeaders(req)
	resp, err := c.do(req, repositoryFullName)
	if err != nil {
		return WorkflowRun{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return WorkflowRun{}, fmt.Errorf("read github workflow run response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WorkflowRun{}, fmt.Errorf("github workflow run: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var run WorkflowRun
	if err := json.Unmarshal(body, &run); err != nil {
		return WorkflowRun{}, err
	}
	result = "success"
	return run, nil
}

func (c *Client) GetWorkflowJob(ctx context.Context, repositoryFullName string, jobID int64) (WorkflowJob, error) {
	startedAt := time.Now()
	result := "error"
	defer func() { metrics.RecordGitHubAPI("get_workflow_job", result, time.Since(startedAt)) }()
	repositoryFullName = c.repositoryFullName(repositoryFullName)
	if repositoryFullName == "" {
		return WorkflowJob{}, fmt.Errorf("repository full name is required")
	}
	url := fmt.Sprintf("%s/repos/%s/actions/jobs/%d", c.baseURL, repositoryFullName, jobID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return WorkflowJob{}, err
	}
	setGitHubHeaders(req)

	resp, err := c.do(req, repositoryFullName)
	if err != nil {
		return WorkflowJob{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return WorkflowJob{}, fmt.Errorf("read github workflow job response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return WorkflowJob{}, fmt.Errorf("github workflow job: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var job WorkflowJob
	if err := json.Unmarshal(body, &job); err != nil {
		return WorkflowJob{}, err
	}
	result = "success"
	return job, nil
}

func (c *Client) GetRepository(ctx context.Context, repositoryFullName string) (Repository, error) {
	startedAt := time.Now()
	result := "error"
	defer func() { metrics.RecordGitHubAPI("get_repository", result, time.Since(startedAt)) }()
	repositoryFullName = c.repositoryFullName(repositoryFullName)
	if repositoryFullName == "" {
		return Repository{}, fmt.Errorf("repository full name is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s", c.baseURL, repositoryFullName), nil)
	if err != nil {
		return Repository{}, err
	}
	setGitHubHeaders(req)
	resp, err := c.do(req, repositoryFullName)
	if err != nil {
		return Repository{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Repository{}, fmt.Errorf("read github repository response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Repository{}, fmt.Errorf("github repository: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var repository Repository
	if err := json.Unmarshal(body, &repository); err != nil {
		return Repository{}, err
	}
	if strings.TrimSpace(repository.FullName) == "" {
		return Repository{}, fmt.Errorf("github repository response missing full name")
	}
	result = "success"
	return repository, nil
}

func (c *Client) GetPullRequest(ctx context.Context, repositoryFullName string, number int64) (PullRequest, error) {
	startedAt := time.Now()
	result := "error"
	defer func() { metrics.RecordGitHubAPI("get_pull_request", result, time.Since(startedAt)) }()
	repositoryFullName = c.repositoryFullName(repositoryFullName)
	if repositoryFullName == "" {
		return PullRequest{}, fmt.Errorf("repository full name is required")
	}
	if number <= 0 {
		return PullRequest{}, fmt.Errorf("pull request number is required")
	}
	url := fmt.Sprintf("%s/repos/%s/pulls/%d", c.baseURL, repositoryFullName, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return PullRequest{}, err
	}
	setGitHubHeaders(req)

	resp, err := c.do(req, repositoryFullName)
	if err != nil {
		return PullRequest{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return PullRequest{}, fmt.Errorf("read github pull request response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return PullRequest{}, fmt.Errorf("github pull request: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var pull PullRequest
	if err := json.Unmarshal(body, &pull); err != nil {
		return PullRequest{}, err
	}
	result = "success"
	return pull, nil
}

func (c *Client) GetIssue(ctx context.Context, repositoryFullName string, number int64) (Issue, error) {
	startedAt := time.Now()
	result := "error"
	defer func() { metrics.RecordGitHubAPI("get_issue", result, time.Since(startedAt)) }()
	repositoryFullName = c.repositoryFullName(repositoryFullName)
	if repositoryFullName == "" {
		return Issue{}, fmt.Errorf("repository full name is required")
	}
	if number <= 0 {
		return Issue{}, fmt.Errorf("issue number is required")
	}
	url := fmt.Sprintf("%s/repos/%s/issues/%d", c.baseURL, repositoryFullName, number)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Issue{}, err
	}
	setGitHubHeaders(req)

	resp, err := c.do(req, repositoryFullName)
	if err != nil {
		return Issue{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return Issue{}, fmt.Errorf("read github issue response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Issue{}, fmt.Errorf("github issue: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var issue Issue
	if err := json.Unmarshal(body, &issue); err != nil {
		return Issue{}, err
	}
	result = "success"
	return issue, nil
}

func (c *Client) DownloadWorkflowJobLogs(ctx context.Context, repositoryFullName string, jobID int64) ([]byte, string, error) {
	if jobID == 0 {
		return nil, "", fmt.Errorf("workflow job id is required")
	}
	return c.downloadActionsLog(ctx, repositoryFullName, fmt.Sprintf("actions/jobs/%d/logs", jobID), "download_workflow_job_logs", "github workflow job logs")
}

func (c *Client) DownloadWorkflowRunLogs(ctx context.Context, repositoryFullName string, runID int64) ([]byte, string, error) {
	if runID == 0 {
		return nil, "", fmt.Errorf("workflow run id is required")
	}
	return c.downloadActionsLog(ctx, repositoryFullName, fmt.Sprintf("actions/runs/%d/logs", runID), "download_workflow_run_logs", "github workflow run logs")
}

func (c *Client) downloadActionsLog(ctx context.Context, repositoryFullName, apiPath, metricName, errorLabel string) ([]byte, string, error) {
	startedAt := time.Now()
	result := "error"
	defer func() { metrics.RecordGitHubAPI(metricName, result, time.Since(startedAt)) }()
	repositoryFullName = c.repositoryFullName(repositoryFullName)
	if repositoryFullName == "" {
		return nil, "", fmt.Errorf("repository full name is required")
	}
	url := fmt.Sprintf("%s/repos/%s/%s", c.baseURL, repositoryFullName, apiPath)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}
	setGitHubHeaders(req)

	resp, err := c.doNoRedirect(req, repositoryFullName)
	if err != nil {
		return nil, "", err
	}
	if resp.StatusCode == http.StatusMovedPermanently ||
		resp.StatusCode == http.StatusFound ||
		resp.StatusCode == http.StatusSeeOther ||
		resp.StatusCode == http.StatusTemporaryRedirect ||
		resp.StatusCode == http.StatusPermanentRedirect {
		location := strings.TrimSpace(resp.Header.Get("Location"))
		resp.Body.Close()
		if location == "" {
			return nil, "", fmt.Errorf("%s: redirect missing location", errorLabel)
		}
		resp, err = c.downloadRedirect(ctx, location)
		if err != nil {
			return nil, "", err
		}
	}
	defer resp.Body.Close()
	body, readErr := readActionsLogBody(resp.Body, 32<<20)
	if readErr != nil {
		return nil, "", fmt.Errorf("%s: %w", errorLabel, readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("%s: status %d: %s", errorLabel, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	result = "success"
	return body, resp.Header.Get("Content-Type"), nil
}

func readActionsLogBody(r io.Reader, maxBytes int64) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, fmt.Errorf("log size limit must be positive")
	}
	body, err := io.ReadAll(io.LimitReader(r, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("log archive exceeds %d bytes", maxBytes)
	}
	return body, nil
}

func (c *Client) downloadRedirect(ctx context.Context, location string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, err
	}
	client := *c.downloadHTTP
	client.CheckRedirect = nil
	return client.Do(req)
}

func (c *Client) do(req *http.Request, repositoryFullName string) (*http.Response, error) {
	if c.appAuth == nil {
		return c.http.Do(req)
	}
	client, err := c.appAuth.clientForRepository(req.Context(), c.repositoryFullName(repositoryFullName))
	if err != nil {
		return nil, err
	}
	return client.Do(req)
}

func (c *Client) doNoRedirect(req *http.Request, repositoryFullName string) (*http.Response, error) {
	client := c.http
	if c.appAuth != nil {
		var err error
		client, err = c.appAuth.clientForRepository(req.Context(), c.repositoryFullName(repositoryFullName))
		if err != nil {
			return nil, err
		}
	}
	cloned := *client
	cloned.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return cloned.Do(req)
}

func (a *appAuthenticator) clientForRepository(ctx context.Context, repositoryFullName string) (*http.Client, error) {
	installationID, err := a.installationID(ctx, repositoryFullName)
	if err != nil {
		return nil, err
	}
	return a.clientForInstallation(ctx, installationID)
}

func (a *appAuthenticator) clientForInstallation(_ context.Context, installationID int64) (*http.Client, error) {
	if installationID <= 0 {
		return nil, fmt.Errorf("installation id is required")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	transport, ok := a.transports[installationID]
	if !ok {
		transport = ghinstallation.NewFromAppsTransport(a.appTransport, installationID)
		transport.BaseURL = a.baseURL
		a.transports[installationID] = transport
	}
	client := *a.httpClient
	client.Transport = transport
	return &client, nil
}

func (a *appAuthenticator) installationID(ctx context.Context, repositoryFullName string) (int64, error) {
	if a.staticInstallationID != 0 {
		return a.staticInstallationID, nil
	}
	repositoryFullName = strings.TrimSpace(repositoryFullName)
	if repositoryFullName == "" {
		return 0, fmt.Errorf("repository full name is required for dynamic GitHub App installation")
	}
	a.mu.Lock()
	if installationID := a.installationByRepo[repositoryFullName]; installationID != 0 {
		a.mu.Unlock()
		return installationID, nil
	}
	a.mu.Unlock()

	url := fmt.Sprintf("%s/repos/%s/installation", a.baseURL, repositoryFullName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}
	setGitHubHeaders(req)
	resp, err := a.appHTTP.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, fmt.Errorf("read github repository installation response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("github repository installation: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return 0, err
	}
	if out.ID == 0 {
		return 0, fmt.Errorf("github repository installation response missing id")
	}
	a.mu.Lock()
	a.installationByRepo[repositoryFullName] = out.ID
	a.mu.Unlock()
	return out.ID, nil
}

func (c *Client) cachedRegistrationToken(key string) (RegistrationToken, bool) {
	c.tokensMu.Lock()
	defer c.tokensMu.Unlock()
	token, ok := c.regTokens[key]
	if !ok {
		return RegistrationToken{}, false
	}
	if token.ExpiresAt.After(time.Now().Add(30 * time.Minute)) {
		return token, true
	}
	delete(c.regTokens, key)
	return RegistrationToken{}, false
}

func (c *Client) storeRegistrationToken(key string, token RegistrationToken) {
	c.tokensMu.Lock()
	defer c.tokensMu.Unlock()
	c.regTokens[key] = token
	for cachedKey, cached := range c.regTokens {
		if cached.ExpiresAt.Before(time.Now()) {
			delete(c.regTokens, cachedKey)
		}
	}
}

func (c *Client) repositoryFullName(repositoryFullName string) string {
	return strings.TrimSpace(repositoryFullName)
}

func (c *Client) runnerTarget(repositoryFullName, runnerGroup string) (runnerTarget, error) {
	repositoryFullName = c.repositoryFullName(repositoryFullName)
	if repositoryFullName == "" {
		return runnerTarget{}, fmt.Errorf("repository full name is required")
	}
	owner, _, ok := strings.Cut(repositoryFullName, "/")
	if !ok || owner == "" {
		return runnerTarget{}, fmt.Errorf("repository full name must be owner/repo")
	}
	if strings.TrimSpace(runnerGroup) != "" {
		return runnerTarget{
			repositoryFullName: repositoryFullName,
			apiPath:            "orgs/" + owner,
			webURL:             "https://github.com/" + owner,
			cacheKey:           "org:" + owner,
		}, nil
	}
	return runnerTarget{
		repositoryFullName: repositoryFullName,
		apiPath:            "repos/" + repositoryFullName,
		webURL:             "https://github.com/" + repositoryFullName,
		cacheKey:           "repo:" + repositoryFullName,
	}, nil
}

func nextLink(header string) string {
	for _, part := range strings.Split(header, ",") {
		sections := strings.Split(part, ";")
		if len(sections) < 2 {
			continue
		}
		link := strings.TrimSpace(sections[0])
		if !strings.HasPrefix(link, "<") || !strings.HasSuffix(link, ">") {
			continue
		}
		for _, section := range sections[1:] {
			if strings.TrimSpace(section) == `rel="next"` {
				return strings.TrimSuffix(strings.TrimPrefix(link, "<"), ">")
			}
		}
	}
	return ""
}

func setGitHubHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
}

type authTransport interface {
	RoundTripper(http.RoundTripper) http.RoundTripper
}

func clientWithTransport(httpClient *http.Client, auth authTransport) *http.Client {
	httpClient = cloneHTTPClient(httpClient)
	baseTransport := httpClient.Transport
	if baseTransport == nil {
		baseTransport = http.DefaultTransport
	}
	cloned := *httpClient
	cloned.Transport = auth.RoundTripper(baseTransport)
	return &cloned
}

func cloneHTTPClient(httpClient *http.Client) *http.Client {
	if httpClient == nil {
		return &http.Client{Timeout: 30 * time.Second}
	}
	cloned := *httpClient
	return &cloned
}

type bearerTransport struct {
	token string
}

func (t bearerTransport) RoundTripper(base http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = cloneRequest(req)
		req.Header.Set("Authorization", "Bearer "+t.token)
		return base.RoundTrip(req)
	})
}

type basicAuthTransport struct {
	username string
	password string
}

func (t basicAuthTransport) RoundTripper(base http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req = cloneRequest(req)
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(t.username+":"+t.password)))
		return base.RoundTrip(req)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func cloneRequest(req *http.Request) *http.Request {
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	return cloned
}

func VerifyWebhookSignature(secret string, body []byte, signature string) bool {
	if secret == "" || signature == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	got, err := hex.DecodeString(strings.TrimPrefix(signature, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	want := mac.Sum(nil)
	return hmac.Equal(got, want)
}

type WorkflowJobEvent struct {
	Action       string          `json:"action"`
	WorkflowJob  WorkflowJob     `json:"workflow_job"`
	Repository   Repository      `json:"repository"`
	WorkflowRun  WorkflowRun     `json:"workflow_run"`
	Installation InstallationRef `json:"installation"`
}

type WorkflowRunEvent struct {
	Action       string          `json:"action"`
	WorkflowRun  WorkflowRun     `json:"workflow_run"`
	Repository   Repository      `json:"repository"`
	Installation InstallationRef `json:"installation"`
}

type WorkflowJob struct {
	ID           int64         `json:"id"`
	Name         string        `json:"name"`
	Status       string        `json:"status"`
	Conclusion   string        `json:"conclusion"`
	RunnerName   string        `json:"runner_name"`
	WorkflowName string        `json:"workflow_name"`
	HeadBranch   string        `json:"head_branch"`
	PullRequests []PullRequest `json:"pull_requests"`
	Labels       Labels        `json:"labels"`
}

type PullRequest struct {
	Number int64          `json:"number"`
	Title  string         `json:"title"`
	Head   PullRequestRef `json:"head"`
	Base   PullRequestRef `json:"base"`
}

type PullRequestRef struct {
	Ref        string     `json:"ref"`
	Repository Repository `json:"repo"`
}

type Issue struct {
	Number int64  `json:"number"`
	Title  string `json:"title"`
}

type Repository struct {
	ID            int64  `json:"id"`
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	DefaultBranch string `json:"default_branch"`
}

type WorkflowRun struct {
	ID             int64         `json:"id"`
	Name           string        `json:"name"`
	Event          string        `json:"event"`
	HeadBranch     string        `json:"head_branch"`
	HeadRepository Repository    `json:"head_repository"`
	Repository     Repository    `json:"repository"`
	PullRequests   []PullRequest `json:"pull_requests"`
}

type InstallationRef struct {
	ID int64 `json:"id"`
}

type Labels []string

func (l *Labels) UnmarshalJSON(data []byte) error {
	var stringsOnly []string
	if err := json.Unmarshal(data, &stringsOnly); err == nil {
		*l = stringsOnly
		return nil
	}
	var objects []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &objects); err != nil {
		return err
	}
	out := make([]string, 0, len(objects))
	for _, obj := range objects {
		if obj.Name != "" {
			out = append(out, obj.Name)
		}
	}
	*l = out
	return nil
}

func LabelsMatch(jobLabels, required []string) bool {
	return labelutil.Match(jobLabels, required)
}
