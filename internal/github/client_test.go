package github

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestVerifyWebhookSignature(t *testing.T) {
	body := []byte(`{"ok":true}`)
	secret := "secret"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sig := "sha256=" + hex.EncodeToString(mac.Sum(nil))

	if !VerifyWebhookSignature(secret, body, sig) {
		t.Fatal("expected valid signature")
	}
	if VerifyWebhookSignature(secret, body, "sha256=deadbeef") {
		t.Fatal("expected invalid signature")
	}
	if VerifyWebhookSignature(secret, body, "") {
		t.Fatal("expected missing signature to fail")
	}
}

func TestLabelsUnmarshalAndMatch(t *testing.T) {
	var event WorkflowJobEvent
	if err := json.Unmarshal([]byte(`{"workflow_job":{"id":1,"labels":[{"name":"self-hosted"},{"name":"e2b"}]}}`), &event); err != nil {
		t.Fatal(err)
	}
	if !LabelsMatch(event.WorkflowJob.Labels, []string{"self-hosted", "e2b"}) {
		t.Fatalf("expected labels to match: %#v", event.WorkflowJob.Labels)
	}
	if !LabelsMatch([]string{"e2b"}, []string{"self-hosted", "e2b", "las-sandbox"}) {
		t.Fatal("expected runner labels to satisfy a subset of requested labels")
	}
	if LabelsMatch(event.WorkflowJob.Labels, []string{"self-hosted", "linux"}) {
		t.Fatalf("expected labels not to match")
	}
}

func TestCreateRegistrationToken(t *testing.T) {
	var requests int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/repos/o/r/actions/runners/registration-token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"token":"runner-token","expires_at":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	token, err := client.CreateRegistrationToken(t.Context(), "o/r", "")
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "runner-token" {
		t.Fatalf("unexpected token: %q", token.Token)
	}
	token, err = client.CreateRegistrationToken(t.Context(), "o/r", "")
	if err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("expected cached token to avoid second request, got %d requests", requests)
	}
}

func TestCreateRegistrationTokenFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusUnauthorized)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	if _, err := client.CreateRegistrationToken(t.Context(), "o/r", ""); err == nil {
		t.Fatal("expected error")
	}
}

func TestCreateRegistrationTokenRequiresRepository(t *testing.T) {
	client := NewClient("https://api.github.com", nil)
	if _, err := client.CreateRegistrationToken(t.Context(), "", ""); err == nil {
		t.Fatal("expected missing repository error")
	}
}

func TestCreateRegistrationTokenUsesRequestRepository(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/other/repo/actions/runners/registration-token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"token":"runner-token","expires_at":"2026-05-18T10:00:00Z"}`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	token, err := client.CreateRegistrationToken(t.Context(), "other/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "runner-token" {
		t.Fatalf("unexpected token: %q", token.Token)
	}
	if got, err := client.RunnerURL("other/repo", ""); err != nil || got != "https://github.com/other/repo" {
		t.Fatalf("unexpected runner url %q err=%v", got, err)
	}
}

func TestListRunnerApplicationsRepository(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"os":"linux","architecture":"x64","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.tar.gz","filename":"actions-runner-linux-x64-2.337.0.tar.gz","sha256_checksum":"70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613"},
			{"os":"linux","architecture":"arm64","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-arm64-2.337.0.tar.gz","filename":"actions-runner-linux-arm64-2.337.0.tar.gz","sha256_checksum":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
			{"os":"linux","architecture":"arm","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-arm-2.337.0.tar.gz","filename":"actions-runner-linux-arm-2.337.0.tar.gz","sha256_checksum":"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"},
			{"os":"win","architecture":"x64","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-win-x64-2.337.0.zip","filename":"actions-runner-win-x64-2.337.0.zip","sha256_checksum":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"}
		]`)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	applications, err := client.ListRunnerApplications(t.Context(), "o/r", "")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/repos/o/r/actions/runners/downloads" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	if len(applications) != 3 {
		t.Fatalf("expected 3 Linux applications, got %#v", applications)
	}
	want := RunnerApplication{
		Architecture:   "x64",
		Version:        "2.337.0",
		DownloadURL:    "https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.tar.gz",
		SHA256Checksum: "70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613",
	}
	if applications[0] != want {
		t.Fatalf("unexpected x64 application: %#v", applications[0])
	}
}

func TestListRunnerApplicationsOrganization(t *testing.T) {
	var gotPath string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[
			{"os":"linux","architecture":"x64","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.tar.gz","filename":"actions-runner-linux-x64-2.337.0.tar.gz","sha256_checksum":"70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613"},
			{"os":"linux","architecture":"arm64","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-arm64-2.337.0.tar.gz","filename":"actions-runner-linux-arm64-2.337.0.tar.gz","sha256_checksum":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
			{"os":"linux","architecture":"arm","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-arm-2.337.0.tar.gz","filename":"actions-runner-linux-arm-2.337.0.tar.gz","sha256_checksum":"abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"}
		]`)
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	applications, err := client.ListRunnerApplications(t.Context(), "o/r", "default")
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/orgs/o/actions/runners/downloads" {
		t.Fatalf("unexpected path: %s", gotPath)
	}
	if len(applications) != 3 {
		t.Fatalf("expected 3 Linux applications, got %#v", applications)
	}
}

func TestListRunnerApplicationsRejectsInvalidResponses(t *testing.T) {
	const validChecksum = "70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613"
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "HTTP failure", status: http.StatusForbidden, body: `{"message":"forbidden"}`},
		{name: "non GitHub URL", status: http.StatusOK, body: `[{"os":"linux","architecture":"x64","download_url":"https://example.com/actions-runner-linux-x64-2.337.0.tar.gz","filename":"actions-runner-linux-x64-2.337.0.tar.gz","sha256_checksum":"` + validChecksum + `"}]`},
		{name: "bad digest", status: http.StatusOK, body: `[{"os":"linux","architecture":"x64","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.tar.gz","filename":"actions-runner-linux-x64-2.337.0.tar.gz","sha256_checksum":"not-a-digest"}]`},
		{name: "unsupported architecture", status: http.StatusOK, body: `[{"os":"linux","architecture":"riscv64","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-riscv64-2.337.0.tar.gz","filename":"actions-runner-linux-riscv64-2.337.0.tar.gz","sha256_checksum":"` + validChecksum + `"}]`},
		{name: "mismatched URL version", status: http.StatusOK, body: `[{"os":"linux","architecture":"x64","download_url":"https://github.com/actions/runner/releases/download/v2.336.0/actions-runner-linux-x64-2.336.0.tar.gz","filename":"actions-runner-linux-x64-2.337.0.tar.gz","sha256_checksum":"` + validChecksum + `"}]`},
		{name: "empty Linux set", status: http.StatusOK, body: `[{"os":"win","architecture":"x64","download_url":"https://example.invalid/runner.zip","filename":"runner.zip","sha256_checksum":"` + validChecksum + `"}]`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer ts.Close()
			client := NewClient(ts.URL, ts.Client())
			if _, err := client.ListRunnerApplications(t.Context(), "o/r", ""); err == nil {
				t.Fatal("expected invalid response to fail")
			}
		})
	}
}

func TestListRunnerApplicationsCachesValidatedResponse(t *testing.T) {
	var requests atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = io.WriteString(w, validRunnerApplicationsResponse())
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	first, err := client.ListRunnerApplications(t.Context(), "o/r", "")
	if err != nil {
		t.Fatal(err)
	}
	first[0].Version = "mutated"
	second, err := client.ListRunnerApplications(t.Context(), "o/r", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected one upstream request, got %d", got)
	}
	if second[0].Version != "2.337.0" {
		t.Fatalf("cached response was mutated: %#v", second[0])
	}
}

func TestStoreRunnerApplicationsEvictsExpiredEntries(t *testing.T) {
	client := NewClient("https://api.github.test", nil)
	client.applications["repo:expired"] = runnerApplicationsCacheEntry{
		applications: []RunnerApplication{{Architecture: "x64", Version: "2.336.0"}},
		expiresAt:    time.Now().Add(-time.Minute),
	}

	client.storeRunnerApplications("repo:active", []RunnerApplication{{Architecture: "x64", Version: "2.337.0"}})

	if _, ok := client.applications["repo:expired"]; ok {
		t.Fatal("expired Runner applications cache entry was retained")
	}
	if _, ok := client.applications["repo:active"]; !ok {
		t.Fatal("active Runner applications cache entry was not stored")
	}
}

func TestListRunnerApplicationsCoalescesConcurrentRequests(t *testing.T) {
	var requests atomic.Int32
	release := make(chan struct{})
	firstRequest := make(chan struct{})
	var signalFirst sync.Once
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		signalFirst.Do(func() { close(firstRequest) })
		<-release
		_, _ = io.WriteString(w, validRunnerApplicationsResponse())
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	start := make(chan struct{})
	errs := make(chan error, 20)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := client.ListRunnerApplications(t.Context(), "o/r", "")
			errs <- err
		}()
	}
	close(start)
	<-firstRequest
	deadline := time.Now().Add(500 * time.Millisecond)
	for requests.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("expected one coalesced upstream request, got %d", got)
	}
}

func validRunnerApplicationsResponse() string {
	return `[{"os":"linux","architecture":"x64","download_url":"https://github.com/actions/runner/releases/download/v2.337.0/actions-runner-linux-x64-2.337.0.tar.gz","filename":"actions-runner-linux-x64-2.337.0.tar.gz","sha256_checksum":"70920811a4f8ad4328818682bca5c6469c1c942fab52448868071d0063816613"}]`
}

func TestNewAppClientUsesConfiguredBaseURLForInstallationTransport(t *testing.T) {
	privateKey := testPrivateKeyFile(t)
	client, err := NewAppClient("https://github.example/api/v3", AppAuth{
		AppID:          123,
		InstallationID: 456,
		PrivateKeyFile: privateKey,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if client.appAuth == nil {
		t.Fatal("expected app authenticator")
	}
	if client.appAuth.baseURL != "https://github.example/api/v3" {
		t.Fatalf("unexpected app auth base URL: %s", client.appAuth.baseURL)
	}
	if client.appAuth.staticInstallationID != 456 {
		t.Fatalf("unexpected static installation id: %d", client.appAuth.staticInstallationID)
	}
}

func TestGetInstallationReturnsStableAccountIdentity(t *testing.T) {
	privateKey := testPrivateKeyFile(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/app/installations/456" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") == "" {
			t.Fatal("expected app JWT authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":456,"account":{"id":9001,"login":"octo-org","type":"Organization","name":"Octo Org","avatar_url":"https://avatars.example/o.png"}}`))
	}))
	defer ts.Close()

	client, err := NewAppClient(ts.URL, AppAuth{AppID: 123, PrivateKeyFile: privateKey}, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	installation, err := client.GetInstallation(t.Context(), 456)
	if err != nil {
		t.Fatal(err)
	}
	if installation.AccountID != 9001 || installation.AccountType != "organization" || installation.AccountLogin != "octo-org" {
		t.Fatalf("unexpected installation owner: %#v", installation)
	}
}

func TestListInstallationsFollowsPagination(t *testing.T) {
	privateKey := testPrivateKeyFile(t)
	var serverURL string
	var requests []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/app/installations" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") == "" {
			t.Fatal("expected app JWT authorization")
		}
		requests = append(requests, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("page") == "2" {
			_, _ = io.WriteString(w, `[{"id":457,"account":{"id":9002,"login":" octocat ","type":"User","name":" Octo Cat ","avatar_url":" https://avatars.example/u.png "}}]`)
			return
		}
		w.Header().Set("Link", `<`+serverURL+`/app/installations?per_page=100&page=2>; rel="next"`)
		_, _ = io.WriteString(w, `[
			{"id":0,"account":{"id":8999,"login":"invalid-zero","type":"User"}},
			{"id":-1,"account":{"id":8998,"login":"invalid-negative","type":"User"}},
			{"id":456,"account":{"id":9001,"login":"octo-org","type":"Organization","name":"Octo Org","avatar_url":"https://avatars.example/o.png"}}
		]`)
	}))
	serverURL = ts.URL
	defer ts.Close()

	client, err := NewAppClient(ts.URL, AppAuth{AppID: 123, PrivateKeyFile: privateKey}, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	installations, err := client.ListInstallations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []Installation{
		{ID: 456, AccountID: 9001, AccountType: "organization", AccountLogin: "octo-org", AccountName: "Octo Org", AccountAvatar: "https://avatars.example/o.png"},
		{ID: 457, AccountID: 9002, AccountType: "user", AccountLogin: "octocat", AccountName: "Octo Cat", AccountAvatar: "https://avatars.example/u.png"},
	}
	if !reflect.DeepEqual(installations, want) {
		t.Fatalf("unexpected installations: got=%#v want=%#v", installations, want)
	}
	if !reflect.DeepEqual(requests, []string{"per_page=100", "per_page=100&page=2"}) {
		t.Fatalf("unexpected installation requests: %#v", requests)
	}
}

func TestListInstallationsRequiresAppAuth(t *testing.T) {
	_, err := NewClient("https://api.github.test", nil).ListInstallations(t.Context())
	if err == nil || !strings.Contains(err.Error(), "github app auth is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListInstallationsRejectsGitHubFailure(t *testing.T) {
	privateKey := testPrivateKeyFile(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer ts.Close()

	client, err := NewAppClient(ts.URL, AppAuth{AppID: 123, PrivateKeyFile: privateKey}, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.ListInstallations(t.Context())
	if err == nil || !strings.Contains(err.Error(), "status 403") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestListInstallationRepositoriesFollowsPagination(t *testing.T) {
	privateKey := testPrivateKeyFile(t)
	var serverURL string
	var repositoryRequests []string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/987/access_tokens":
			if r.Header.Get("Authorization") == "" {
				t.Fatal("expected app JWT authorization for installation token")
			}
			_, _ = io.WriteString(w, `{"token":"installation-token","expires_at":"`+time.Now().Add(time.Hour).Format(time.RFC3339)+`"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/installation/repositories":
			if got := r.Header.Get("Authorization"); got != "token installation-token" {
				t.Fatalf("unexpected installation authorization: %q", got)
			}
			repositoryRequests = append(repositoryRequests, r.URL.RawQuery)
			if r.URL.Query().Get("page") == "2" {
				_, _ = io.WriteString(w, `{"repositories":[{"full_name":"octo-org/api"}]}`)
				return
			}
			w.Header().Set("Link", `<`+serverURL+`/installation/repositories?per_page=100&page=2>; rel="next"`)
			_, _ = io.WriteString(w, `{"repositories":[{"full_name":"octo-org/runner"}]}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	serverURL = ts.URL
	defer ts.Close()

	client, err := NewAppClient(ts.URL, AppAuth{AppID: 123, PrivateKeyFile: privateKey}, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	repositories, err := client.ListInstallationRepositories(t.Context(), 987)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"octo-org/runner", "octo-org/api"}; !reflect.DeepEqual(repositories, want) {
		t.Fatalf("unexpected repositories: got=%#v want=%#v", repositories, want)
	}
	if want := []string{"per_page=100", "per_page=100&page=2"}; !reflect.DeepEqual(repositoryRequests, want) {
		t.Fatalf("unexpected repository requests: got=%#v want=%#v", repositoryRequests, want)
	}
}

func TestGetAccountReturnsCanonicalStableIdentity(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/users/OCTO-ORG" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":9001,"login":"octo-org","type":"Organization","name":"Octo Org","avatar_url":"https://avatars.example/o.png"}`))
	}))
	defer ts.Close()

	account, err := NewClient(ts.URL, ts.Client()).GetAccount(t.Context(), "OCTO-ORG")
	if err != nil {
		t.Fatal(err)
	}
	if account.ID != 9001 || account.Type != "organization" || account.Login != "octo-org" || account.Name != "Octo Org" {
		t.Fatalf("unexpected account: %#v", account)
	}
}

func TestGetAccountRejectsUnsupportedAccountType(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":9001,"login":"release-bot","type":"Bot"}`))
	}))
	defer ts.Close()

	_, err := NewClient(ts.URL, ts.Client()).GetAccount(t.Context(), "release-bot")
	if !errors.Is(err, ErrUnsupportedAccountType) {
		t.Fatalf("GetAccount() error = %v, want ErrUnsupportedAccountType", err)
	}
}

func TestAppClientResolvesDynamicRepositoryInstallation(t *testing.T) {
	privateKey := testPrivateKeyFile(t)
	var installationLookups int
	var tokenRequests int
	var registrationAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/installation":
			installationLookups++
			if r.Header.Get("Authorization") == "" {
				t.Fatal("expected app JWT authorization for installation lookup")
			}
			w.Write([]byte(`{"id":456}`))
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/456/access_tokens":
			tokenRequests++
			if r.Header.Get("Authorization") == "" {
				t.Fatal("expected app JWT authorization for installation token")
			}
			w.Write([]byte(`{"token":"installation-token","expires_at":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/repos/o/r/actions/runners/registration-token":
			registrationAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"token":"runner-token","expires_at":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	client, err := NewAppClient(ts.URL, AppAuth{
		AppID:          123,
		PrivateKeyFile: privateKey,
	}, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.CreateRegistrationToken(t.Context(), "o/r", "")
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "runner-token" {
		t.Fatalf("unexpected token: %q", token.Token)
	}
	if registrationAuth != "token installation-token" {
		t.Fatalf("unexpected installation auth header: %q", registrationAuth)
	}
	if installationLookups != 1 || tokenRequests != 1 {
		t.Fatalf("expected cached dynamic installation, lookups=%d tokenRequests=%d", installationLookups, tokenRequests)
	}
}

func TestAppClientUsesRepositoryInstallationForOrgRunnerTarget(t *testing.T) {
	privateKey := testPrivateKeyFile(t)
	var registrationAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/installation":
			w.Write([]byte(`{"id":456}`))
		case r.Method == http.MethodPost && r.URL.Path == "/app/installations/456/access_tokens":
			w.Write([]byte(`{"token":"installation-token","expires_at":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/orgs/o/actions/runners/registration-token":
			registrationAuth = r.Header.Get("Authorization")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"token":"runner-token","expires_at":"` + time.Now().Add(time.Hour).Format(time.RFC3339) + `"}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	client, err := NewAppClient(ts.URL, AppAuth{
		AppID:          123,
		PrivateKeyFile: privateKey,
	}, ts.Client())
	if err != nil {
		t.Fatal(err)
	}
	token, err := client.CreateRegistrationToken(t.Context(), "o/r", "default")
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "runner-token" || registrationAuth != "token installation-token" {
		t.Fatalf("unexpected token=%q auth=%q", token.Token, registrationAuth)
	}
	if got, err := client.RunnerURL("o/r", "default"); err != nil || got != "https://github.com/o" {
		t.Fatalf("unexpected runner url %q err=%v", got, err)
	}
}

func TestCreateOrgRegistrationToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/orgs/my-org/actions/runners/registration-token" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"token":"runner-token","expires_at":"2026-05-18T10:00:00Z"}`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	token, err := client.CreateRegistrationToken(t.Context(), "my-org/repo", "default")
	if err != nil {
		t.Fatal(err)
	}
	if token.Token != "runner-token" {
		t.Fatalf("unexpected token: %q", token.Token)
	}
	if got, err := client.RunnerURL("my-org/repo", "default"); err != nil || got != "https://github.com/my-org" {
		t.Fatalf("unexpected runner url: %s", got)
	}
}

func TestTokenAndBasicAuthClientsSetAuthorizationHeader(t *testing.T) {
	var gotTokenAuth string
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTokenAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"token":"runner-token","expires_at":"2026-05-18T10:00:00Z"}`))
	}))
	defer tokenServer.Close()

	tokenClient := NewTokenClient(tokenServer.URL, "ghp_test", tokenServer.Client())
	if _, err := tokenClient.CreateRegistrationToken(t.Context(), "o/r", ""); err != nil {
		t.Fatal(err)
	}
	if gotTokenAuth != "Bearer ghp_test" {
		t.Fatalf("unexpected token auth header: %q", gotTokenAuth)
	}

	var gotBasicAuth string
	basicServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBasicAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"token":"runner-token","expires_at":"2026-05-18T10:00:00Z"}`))
	}))
	defer basicServer.Close()

	basicClient := NewBasicAuthClient(basicServer.URL, "octo", "secret", basicServer.Client())
	if _, err := basicClient.CreateRegistrationToken(t.Context(), "o/r", ""); err != nil {
		t.Fatal(err)
	}
	wantBasic := "Basic " + base64.StdEncoding.EncodeToString([]byte("octo:secret"))
	if gotBasicAuth != wantBasic {
		t.Fatalf("unexpected basic auth header: %q", gotBasicAuth)
	}
}

func TestListAndRemoveRunnerByName(t *testing.T) {
	var deleted bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/o/r/actions/runners":
			w.Write([]byte(`{"runners":[{"id":42,"name":"e2b-1001","status":"offline","busy":false}]}`))
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/o/r/actions/runners/42":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	removed, err := client.RemoveRunnerByName(t.Context(), "o/r", "", "e2b-1001")
	if err != nil {
		t.Fatal(err)
	}
	if !removed || !deleted {
		t.Fatalf("expected runner to be removed, removed=%v deleted=%v", removed, deleted)
	}
}

func TestListWorkflowRunJobs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/runs/123/jobs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jobs":[{"id":1001,"name":"test","status":"queued","labels":["self-hosted","e2b"]}]}`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	jobs, err := client.ListWorkflowRunJobs(t.Context(), "o/r", 123)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ID != 1001 || jobs[0].Labels[1] != "e2b" {
		t.Fatalf("unexpected jobs: %#v", jobs)
	}
}

func TestListWorkflowRunJobsFollowsPagination(t *testing.T) {
	var requests int
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/repos/o/r/actions/runs/123/jobs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", `<`+ts.URL+`/repos/o/r/actions/runs/123/jobs?per_page=100&page=2>; rel="next"`)
			w.Write([]byte(`{"jobs":[{"id":1001,"name":"first","status":"queued","labels":["self-hosted"]}]}`))
		case "2":
			w.Write([]byte(`{"jobs":[{"id":1002,"name":"second","status":"queued","labels":["e2b"]}]}`))
		default:
			t.Fatalf("unexpected page: %s", r.URL.Query().Get("page"))
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	jobs, err := client.ListWorkflowRunJobs(t.Context(), "o/r", 123)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("expected 2 requests, got %d", requests)
	}
	if len(jobs) != 2 || jobs[0].ID != 1001 || jobs[1].ID != 1002 {
		t.Fatalf("unexpected jobs: %#v", jobs)
	}
}

func TestGetWorkflowRun(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/runs/123" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":123,"name":"CI","event":"pull_request","head_branch":"feature","head_repository":{"full_name":"fork/r"},"repository":{"full_name":"o/r"},"pull_requests":[{"number":7}]}`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	run, err := client.GetWorkflowRun(t.Context(), "o/r", 123)
	if err != nil {
		t.Fatal(err)
	}
	if run.ID != 123 || run.Event != "pull_request" || run.HeadRepository.FullName != "fork/r" || len(run.PullRequests) != 1 || run.PullRequests[0].Number != 7 {
		t.Fatalf("unexpected workflow run: %#v", run)
	}
}

func TestGetRepository(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":123,"full_name":"o/r","default_branch":"master"}`))
	}))
	defer ts.Close()

	repository, err := NewClient(ts.URL, ts.Client()).GetRepository(t.Context(), "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if repository.ID != 123 || repository.FullName != "o/r" || repository.DefaultBranch != "master" {
		t.Fatalf("unexpected repository: %#v", repository)
	}
}

func TestGetPullRequestIncludesTrustedRefs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/pulls/7" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"number":7,"head":{"ref":"feature","repo":{"full_name":"fork/r"}},"base":{"ref":"release","repo":{"full_name":"o/r"}}}`))
	}))
	defer ts.Close()

	pull, err := NewClient(ts.URL, ts.Client()).GetPullRequest(t.Context(), "o/r", 7)
	if err != nil {
		t.Fatal(err)
	}
	if pull.Number != 7 || pull.Head.Ref != "feature" || pull.Head.Repository.FullName != "fork/r" || pull.Base.Ref != "release" || pull.Base.Repository.FullName != "o/r" {
		t.Fatalf("unexpected pull request: %#v", pull)
	}
}

func TestGetWorkflowJob(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/jobs/1001" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1001,"name":"test","status":"completed","conclusion":"cancelled","labels":["self-hosted","e2b"]}`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	job, err := client.GetWorkflowJob(t.Context(), "o/r", 1001)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != 1001 || job.Status != "completed" || job.Conclusion != "cancelled" {
		t.Fatalf("unexpected job: %#v", job)
	}
}

func TestDownloadWorkflowJobLogs(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/actions/jobs/1001/logs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("github log\n"))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	body, contentType, err := client.DownloadWorkflowJobLogs(t.Context(), "o/r", 1001)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "github log\n" || !strings.HasPrefix(contentType, "text/plain") {
		t.Fatalf("unexpected logs body=%q contentType=%q", string(body), contentType)
	}
}

func TestDownloadWorkflowJobLogsFollowsRedirectWithoutGitHubAuth(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/actions/jobs/1001/logs":
			if r.Header.Get("Authorization") != "Bearer github-token" {
				t.Fatalf("expected github authorization on api request, got %q", r.Header.Get("Authorization"))
			}
			http.Redirect(w, r, ts.URL+"/download/logs.zip?sig=temporary", http.StatusFound)
		case "/download/logs.zip":
			if r.Header.Get("Authorization") != "" {
				t.Fatalf("expected redirect download without github authorization, got %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/zip")
			w.Write([]byte("zip bytes\n"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer ts.Close()

	client := NewTokenClient(ts.URL, "github-token", ts.Client())
	body, contentType, err := client.DownloadWorkflowJobLogs(t.Context(), "o/r", 1001)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "zip bytes\n" || !strings.HasPrefix(contentType, "application/zip") {
		t.Fatalf("unexpected logs body=%q contentType=%q", string(body), contentType)
	}
}

func TestListUserInstallationsUsesOAuthToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/installations" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer user-token" {
			t.Fatalf("expected bearer user token, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"installations":[{"id":987,"account":{"id":9001,"login":"octo-org","type":"Organization","name":"Octo Org","avatar_url":"https://avatars.example/o.png"}}]}`))
	}))
	defer ts.Close()

	client := NewTokenClient(ts.URL, "configured-runner-token", ts.Client())
	installations, err := client.ListUserInstallations(t.Context(), "user-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(installations) != 1 || installations[0].ID != 987 || installations[0].AccountID != 9001 || installations[0].AccountType != "organization" || installations[0].AccountLogin != "octo-org" {
		t.Fatalf("unexpected installations: %#v", installations)
	}
}

func TestListUserInstallationsReturnsStructuredAPIError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/installations" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"message":"Bad credentials"}`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	_, err := client.ListUserInstallations(t.Context(), "expired-user-token")
	if err == nil {
		t.Fatal("expected GitHub API error")
	}
	if status, ok := ErrorStatus(err); !ok || status != http.StatusUnauthorized {
		t.Fatalf("expected structured unauthorized error, got status=%d ok=%v err=%v", status, ok, err)
	}
}

func TestListUserOrganizationMembershipsUsesOAuthToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/memberships/orgs" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if r.URL.Query().Get("state") != "active" || r.URL.Query().Get("per_page") != "100" {
			t.Fatalf("unexpected membership query: %q", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer user-token" {
			t.Fatalf("expected bearer user token, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[
			{"state":"active","role":"member","organization":{"id":9001,"login":"octo-org"}},
			{"state":"pending","role":"member","organization":{"id":9002,"login":"pending-org"}}
		]`))
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	memberships, err := client.ListUserOrganizationMemberships(t.Context(), "user-token")
	if err != nil {
		t.Fatal(err)
	}
	if len(memberships) != 1 || memberships[0].OrganizationID != 9001 || memberships[0].OrganizationLogin != "octo-org" || memberships[0].Role != "member" {
		t.Fatalf("unexpected active memberships: %#v", memberships)
	}
}

func TestListUserInstallationRepositoriesUsesOAuthToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/installations/987/repositories" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if r.URL.Query().Get("per_page") != "100" {
			t.Fatalf("expected per_page=100, got %q", r.URL.RawQuery)
		}
		if r.Header.Get("Authorization") != "Bearer user-token" {
			t.Fatalf("expected bearer user token, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"repositories":[{"full_name":"o/visible"},{"full_name":"o/another"}]}`))
	}))
	defer ts.Close()

	client := NewTokenClient(ts.URL, "configured-runner-token", ts.Client())
	repositories, err := client.ListUserInstallationRepositories(t.Context(), "user-token", 987)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 2 || repositories[0] != "o/visible" || repositories[1] != "o/another" {
		t.Fatalf("unexpected repositories: %#v", repositories)
	}
}

func TestListUserInstallationRepositoriesClassifiesRateLimitResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/installations/987/repositories" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("Retry-After", "60")
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
		w.Write([]byte(`{"message":"API rate limit exceeded"}`))
	}))
	defer ts.Close()

	client := NewTokenClient(ts.URL, "configured-runner-token", ts.Client())
	_, err := client.ListUserInstallationRepositories(t.Context(), "user-token", 987)
	if err == nil {
		t.Fatal("expected rate limit error")
	}
	if !IsRateLimitError(err) {
		t.Fatalf("expected rate limit classification, got %v", err)
	}
	if retryAfter, ok := RateLimitRetryAfter(err); !ok || retryAfter != "60" {
		t.Fatalf("expected retry-after metadata, got %q ok=%v", retryAfter, ok)
	}
}

func TestRateLimitHelpersHandleWrappedAPIError(t *testing.T) {
	err := errors.Join(errors.New("request failed"), &apiError{
		StatusCode:         http.StatusForbidden,
		Body:               `{"message":"API rate limit exceeded"}`,
		RetryAfter:         " 60 ",
		RateLimitRemaining: "0",
	})
	if !IsRateLimitError(err) {
		t.Fatalf("expected wrapped API error to be classified, got %v", err)
	}
	if retryAfter, ok := RateLimitRetryAfter(err); !ok || retryAfter != "60" {
		t.Fatalf("expected wrapped retry-after metadata, got %q ok=%v", retryAfter, ok)
	}
}

func TestListUserInstallationRepositoriesFollowsPagination(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/user/installations/987/repositories" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer user-token" {
			t.Fatalf("expected bearer user token, got %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Query().Get("page") {
		case "":
			w.Header().Set("Link", `<`+ts.URL+`/user/installations/987/repositories?per_page=100&page=2>; rel="next"`)
			w.Write([]byte(`{"repositories":[{"full_name":"o/first"}]}`))
		case "2":
			w.Write([]byte(`{"repositories":[{"full_name":"o/second"}]}`))
		default:
			t.Fatalf("unexpected page: %q", r.URL.RawQuery)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL, ts.Client())
	repositories, err := client.ListUserInstallationRepositories(t.Context(), "user-token", 987)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 2 || repositories[0] != "o/first" || repositories[1] != "o/second" {
		t.Fatalf("unexpected repositories: %#v", repositories)
	}
}

func TestDownloadWorkflowJobLogsRedirectUsesConfiguredTransport(t *testing.T) {
	client := NewTokenClient("https://api.github.test", "github-token", &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch r.URL.String() {
			case "https://api.github.test/repos/o/r/actions/jobs/1001/logs":
				if r.Header.Get("Authorization") != "Bearer github-token" {
					t.Fatalf("expected github authorization on api request, got %q", r.Header.Get("Authorization"))
				}
				return textResponse(http.StatusFound, "", map[string]string{
					"Location": "https://actions-results.test/download/logs.zip?sig=temporary",
				}), nil
			case "https://actions-results.test/download/logs.zip?sig=temporary":
				if r.Header.Get("Authorization") != "" {
					t.Fatalf("expected redirect download without github authorization, got %q", r.Header.Get("Authorization"))
				}
				return textResponse(http.StatusOK, "zip bytes\n", map[string]string{
					"Content-Type": "application/zip",
				}), nil
			default:
				t.Fatalf("unexpected request: %s", r.URL.String())
				return nil, nil
			}
		}),
		Timeout: time.Second,
	})
	body, contentType, err := client.DownloadWorkflowJobLogs(t.Context(), "o/r", 1001)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "zip bytes\n" || !strings.HasPrefix(contentType, "application/zip") {
		t.Fatalf("unexpected logs body=%q contentType=%q", string(body), contentType)
	}
}

func textResponse(status int, body string, headers map[string]string) *http.Response {
	resp := &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
	for key, value := range headers {
		resp.Header.Set(key, value)
	}
	return resp
}

func TestReadActionsLogBodyRejectsOversizedLog(t *testing.T) {
	_, err := readActionsLogBody(strings.NewReader("123456"), 5)
	if err == nil {
		t.Fatal("expected oversized log error")
	}
	if !strings.Contains(err.Error(), "exceeds 5 bytes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func testPrivateKeyFile(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	data := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	path := filepath.Join(t.TempDir(), "github-app.pem")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
