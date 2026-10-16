package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/state"
)

// This opt-in probe uses an existing deployment in an already running local
// server. It may fill empty PR titles through the normal authorized endpoint,
// but never starts, redeploys, stops, or deletes a Sandbox. Session cookies stay
// in memory; this is API evidence, not browser OAuth or a second-user check.
func TestDeploymentLivePullRequestMetadata(t *testing.T) {
	if os.Getenv("RUNNERD_DEPLOY_LIVE_METADATA") != "1" {
		t.Skip("manual local deployment metadata acceptance")
	}
	origin, err := url.Parse(os.Getenv("RUNNERD_DEPLOY_LIVE_BASE_URL"))
	if err != nil || origin.Scheme != "http" || (origin.Hostname() != "127.0.0.1" && origin.Hostname() != "::1") || origin.User != nil || origin.Path != "" || origin.RawQuery != "" || origin.Fragment != "" {
		t.Fatal("live metadata acceptance requires a loopback HTTP origin")
	}
	configPath := os.Getenv("RUNNERD_DEPLOY_LIVE_CONFIG")
	if !filepath.IsAbs(configPath) {
		t.Fatal("absolute local config path required")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal("local config unavailable")
	}
	accountID, err := strconv.ParseInt(os.Getenv("RUNNERD_DEPLOY_LIVE_ACCOUNT_ID"), 10, 64)
	if err != nil || accountID <= 0 {
		t.Fatal("existing account ID required")
	}
	id := os.Getenv("RUNNERD_DEPLOY_LIVE_DETAIL_ID")
	if id == "" || strings.ContainsAny(id, "/?#%\\") {
		t.Fatal("existing deployment ID required")
	}
	evidencePath := os.Getenv("RUNNERD_DEPLOY_LIVE_EVIDENCE")
	if evidencePath != "" && !filepath.IsAbs(evidencePath) {
		t.Fatal("absolute evidence output path required")
	}
	store := state.NewWithOptions(state.Options{Backend: cfg.StateBackend, DatabaseDSN: cfg.StateDatabaseDSN.Value()})
	identity, err := store.GetOAuthIdentityForAccount(accountID, "github")
	if err != nil {
		t.Fatal("existing GitHub identity unavailable")
	}
	before, err := store.GetDeployment(id)
	if err != nil || deploymentPullRequestNumber(before.Ref) == 0 {
		t.Fatal("existing PR deployment required")
	}
	signer := &Server{cfg: cfg}
	cookie, err := signer.encodeAdminSession(adminSession{Provider: "github", Subject: identity.OAuthSubject, Login: identity.OAuthLogin, Role: "user", ExpiresAt: time.Now().Add(2 * time.Minute).Unix()})
	if err != nil {
		t.Fatal("temporary session signing failed")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("unexpected API redirect") }}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	timings := map[string]int64{}
	get := func(suffix string, authenticated bool, expectedStatus int, target any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin.String()+"/user/deployments/"+id+suffix, nil)
		if err != nil {
			t.Fatal("invalid probe request")
		}
		if authenticated {
			req.AddCookie(&http.Cookie{Name: adminSessionCookieName, Value: cookie})
		}
		started := time.Now()
		response, err := client.Do(req)
		if err != nil {
			t.Fatal("local metadata request unavailable")
		}
		defer response.Body.Close()
		label := suffix
		if !authenticated {
			label = "anonymous"
		} else if label == "" {
			label = "detail"
		}
		if _, exists := timings[label]; exists {
			label += "_reloaded"
		}
		if response.StatusCode != expectedStatus {
			t.Fatalf("%s status=%d", label, response.StatusCode)
		}
		if target != nil {
			if response.Header.Get("Cache-Control") != "no-store" {
				t.Fatal("metadata response is cacheable")
			}
			if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
				t.Fatal("invalid metadata response")
			}
		}
		timings[label] = time.Since(started).Milliseconds()
	}
	get("", false, 401, nil)
	var initial deploymentResponse
	get("", true, 200, &initial)
	var metadata struct {
		Number int64  `json:"pull_request_number"`
		Title  string `json:"pull_request_title"`
	}
	get("/pull-request", true, 200, &metadata)
	if metadata.Number != deploymentPullRequestNumber(before.Ref) || strings.TrimSpace(metadata.Title) == "" {
		t.Fatal("missing real PR title")
	}
	var detail deploymentResponse
	get("", true, 200, &detail)
	if detail.ID != id || detail.PullRequestTitle != metadata.Title || detail.PullRequestNumber != metadata.Number || detail.CommitSHA != before.CommitSHA {
		t.Fatal("detail lost PR title or immutable source")
	}
	var revisions struct {
		Items []deploymentRevisionResponse `json:"revisions"`
	}
	get("/revisions", true, 200, &revisions)
	if len(revisions.Items) == 0 {
		t.Fatal("missing retained revisions")
	}
	for _, revision := range revisions.Items {
		if revision.Ref == before.Ref && strings.TrimSpace(revision.PullRequestTitle) == "" {
			t.Fatal("matching legacy revision title missing")
		}
	}
	after, err := store.GetDeployment(id)
	if err != nil || after.Status != before.Status || after.Attempt != before.Attempt || after.Version != before.Version || after.RevisionNumber != before.RevisionNumber || !after.UpdatedAt.Equal(before.UpdatedAt) || after.SandboxID != before.SandboxID || after.PullRequestTitle != metadata.Title {
		t.Fatal("metadata read changed lifecycle or failed to retain title")
	}
	evidence := map[string]any{"recorded_at": time.Now().UTC(), "deployment_id": id, "repository": after.RepositoryFullName, "pull_request_number": metadata.Number, "pull_request_title": metadata.Title, "anonymous_status": 401, "authenticated_status": 200, "revision_count": len(revisions.Items), "lifecycle_unchanged": true, "latencies_ms": timings, "legacy_backfill_exercised": strings.TrimSpace(before.PullRequestTitle) == "", "sandbox_mutations": false, "browser_oauth_verified": false}
	if evidencePath != "" {
		data, err := json.MarshalIndent(evidence, "", "  ")
		if err != nil {
			t.Fatal("invalid evidence")
		}
		if err := os.WriteFile(evidencePath, data, 0o600); err != nil {
			t.Fatal("evidence write failed")
		}
	}
	t.Logf("real metadata acceptance: deployment=%s pr=%d title=%q revisions=%d timings_ms=%v", id, metadata.Number, metadata.Title, len(revisions.Items), timings)
}
