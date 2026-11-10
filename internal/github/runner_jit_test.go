package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunnerJITConfigScopeAndNoCache(t *testing.T) {
	for _, group := range []string{"", "Build"} {
		t.Run("group="+group, func(t *testing.T) {
			posts := 0
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.Method == http.MethodGet && r.URL.Path == "/orgs/o/actions/runner-groups" {
					if r.URL.Query().Get("visible_to_repository") != "r" {
						t.Error("missing exact repository filter")
					}
					_, _ = w.Write([]byte(`{"total_count":1,"runner_groups":[{"id":7,"name":"Build"}]}`))
					return
				}
				want := "/repos/o/r/actions/runners/generate-jitconfig"
				wantID := float64(1)
				if group != "" {
					want = "/orgs/o/actions/runners/generate-jitconfig"
					wantID = 7
				}
				if r.Method != http.MethodPost || r.URL.Path != want {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
					return
				}
				var in map[string]any
				if json.NewDecoder(r.Body).Decode(&in) != nil {
					t.Error("invalid body")
				}
				if in["name"] != "dedicated" || in["runner_group_id"] != wantID || in["work_folder"] != `..\work` {
					t.Errorf("unexpected JIT request: %#v", in)
				}
				posts++
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"runner":{"id":42,"name":"dedicated"},"encoded_jit_config":"` + jitTestConfig(t, "dedicated", "42") + `"}`))
			}))
			defer ts.Close()
			client := NewClient(ts.URL, ts.Client())
			for range 2 {
				if _, err := client.CreateRunnerJITConfig(t.Context(), "o/r", group, "dedicated", []string{"windows", "one-job"}, nil); err != nil {
					t.Fatal(err)
				}
			}
			if posts != 2 {
				t.Fatal("one-time JIT config must not be cached")
			}
		})
	}
}

func TestRunnerJITConfigRejectsMalformedAndRedactsErrors(t *testing.T) {
	for _, body := range []string{`secret-value`, `{"runner":{"id":42,"name":"other"},"encoded_jit_config":"` + jitTestConfig(t, "other", "42") + `"}`, `{"runner":{"id":42,"name":"dedicated"},"encoded_jit_config":"secret-value"}`} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(201); _, _ = w.Write([]byte(body)) }))
		_, err := NewClient(ts.URL, ts.Client()).CreateRunnerJITConfig(t.Context(), "o/r", "", "dedicated", []string{"windows"}, nil)
		ts.Close()
		if err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid or unredacted error: %v", err)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret-value", http.StatusForbidden) }))
	defer ts.Close()
	_, err := NewClient(ts.URL, ts.Client()).CreateRunnerJITConfig(t.Context(), "o/r", "", "dedicated", []string{"windows"}, nil)
	if err == nil || strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("unredacted status error: %v", err)
	}
}

func TestJITUpdateKeepsIdentityAndCredentials(t *testing.T) {
	settings, _ := json.Marshal(map[string]string{"AgentName": "dedicated", "Ephemeral": "True", "DisableUpdate": "True", "AgentId": "42", "WorkFolder": `..\work`})
	files := map[string]string{".runner": base64.StdEncoding.EncodeToString(settings), ".credentials": "cHJpdmF0ZQ==", ".credentials_rsaparams": "cHJpdmF0ZQ=="}
	raw, _ := json.Marshal(files)
	result, err := enableJITRunnerUpdate(base64.StdEncoding.EncodeToString(raw), "dedicated", 42)
	if err != nil {
		t.Fatal(err)
	}
	decoded, _ := base64.StdEncoding.DecodeString(result)
	var updated map[string]string
	_ = json.Unmarshal(decoded, &updated)
	if updated[".credentials"] != files[".credentials"] {
		t.Fatal("JIT credentials changed")
	}
	raw, _ = base64.StdEncoding.DecodeString(updated[".runner"])
	var got map[string]string
	_ = json.Unmarshal(raw, &got)
	if got["DisableUpdate"] != "False" || got["Ephemeral"] != "True" || got["AgentId"] != "42" || got["AgentName"] != "dedicated" {
		t.Fatal("Runner identity or update policy changed incorrectly")
	}
}

func TestJITUpdateRejectsMismatchedIdentityAndUnsafeFiles(t *testing.T) {
	for _, field := range []string{"AgentId", "AgentName", "Ephemeral", "WorkFolder", "file"} {
		t.Run(field, func(t *testing.T) {
			settings := map[string]string{"AgentId": "42", "AgentName": "dedicated", "Ephemeral": "True", "WorkFolder": `..\work`}
			if field != "file" {
				settings[field] = "invalid"
			}
			raw, _ := json.Marshal(settings)
			files := map[string]string{".runner": base64.StdEncoding.EncodeToString(raw), ".credentials": "e30=", ".credentials_rsaparams": "e30="}
			if field == "file" {
				files[`..\escape`] = "e30="
			}
			raw, _ = json.Marshal(files)
			if _, err := enableJITRunnerUpdate(base64.StdEncoding.EncodeToString(raw), "dedicated", 42); err == nil {
				t.Fatal("invalid JIT configuration accepted")
			}
		})
	}
}

func TestRunnerJITConfigRetainsRateLimitClassification(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		http.Error(w, "private body", http.StatusForbidden)
	}))
	defer ts.Close()
	_, err := NewClient(ts.URL, ts.Client()).CreateRunnerJITConfig(t.Context(), "o/r", "", "dedicated", []string{"windows"}, nil)
	if !IsRateLimitError(err) || strings.Contains(err.Error(), "private body") {
		t.Fatal("rate limit classification lost or body leaked")
	}
}

func jitTestConfig(t *testing.T, name, id string) string {
	t.Helper()
	settings, err := json.Marshal(map[string]string{"AgentId": id, "AgentName": name, "Ephemeral": "True", "DisableUpdate": "True", "WorkFolder": `..\work`})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]string{".runner": base64.StdEncoding.EncodeToString(settings), ".credentials": "e30=", ".credentials_rsaparams": "e30="})
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(raw)
}

func TestJITDispatchBoundary(t *testing.T) {
	labels := make([]string, 100)
	for i := range labels {
		labels[i] = strconv.Itoa(i)
	}
	withAttempt := JITAttemptLabels(labels[:99], "ABC")
	if len(withAttempt) != 100 || withAttempt[99] != "runnerd-jit-abc" || labels[99] != "99" {
		t.Fatal("attempt label changed input labels")
	}
	if got := JITAttemptLabels(labels, "ABC"); !slices.Equal(got, labels) {
		t.Fatal("full label set changed")
	}
	var posts atomic.Int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posts.Add(1)
		if r.URL.Path == "/redirected" {
			t.Error("redirect replayed JIT POST")
		}
		http.Redirect(w, r, "/redirected", http.StatusTemporaryRedirect)
	}))
	defer ts.Close()
	c := NewClient(ts.URL, ts.Client())
	lost := errors.New("lease lost")
	jit, err := c.CreateRunnerJITConfig(t.Context(), "o/r", "", "runner", labels, func() error { return lost })
	if !errors.Is(err, lost) || jit.CleanupRequired || posts.Load() != 0 {
		t.Fatal("POST escaped the dispatch guard", err)
	}
	jit, err = c.CreateRunnerJITConfig(t.Context(), "o/r", "", "runner", labels, nil)
	if err == nil || !jit.CleanupRequired || posts.Load() != 1 {
		t.Fatal("redirect did not retain unknown outcome", err)
	}
}

// Installation token acquisition must finish before recording JIT dispatch.
func TestJITAppAuthenticationPrecedesDispatch(t *testing.T) {
	for _, mode := range []string{"rejected", "rate limited", "secondary limit", "timeout", "client timeout", "success", "response lost"} {
		t.Run(mode, func(t *testing.T) {
			var tokenCalls, posts, dispatches atomic.Int32
			release := make(chan struct{})
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/app/installations/456/access_tokens":
					tokenCalls.Add(1)
					if dispatches.Load() != 0 {
						t.Error("dispatch recorded before authentication")
					}
					switch mode {
					case "rejected":
						http.Error(w, "private authentication error", http.StatusForbidden)
						return
					case "rate limited":
						w.Header().Set("Retry-After", "3")
						http.Error(w, "private authentication error", http.StatusTooManyRequests)
						return
					case "secondary limit":
						http.Error(w, "private authentication error: secondary rate limit", http.StatusForbidden)
						return
					case "timeout", "client timeout":
						<-release
						return
					}
					// Already inside ghinstallation's refresh window: dispatch must use
					// the prepared token without another lazy auth request.
					fmt.Fprintf(w, `{"token":"installation-secret","expires_at":%q}`, time.Now().Add(30*time.Second).Format(time.RFC3339))
				case "/repos/o/r/actions/runners/generate-jitconfig":
					posts.Add(1)
					if dispatches.Load() != 1 || r.Header.Get("Authorization") != "Bearer installation-secret" {
						t.Error("registration requires one dispatch and the prepared token")
					}
					if mode == "response lost" {
						conn, _, err := w.(http.Hijacker).Hijack()
						if err != nil {
							t.Error(err)
							return
						}
						conn.Close()
						return
					}
					w.WriteHeader(http.StatusCreated)
					fmt.Fprintf(w, `{"runner":{"id":42,"name":"dedicated"},"encoded_jit_config":%q}`, jitTestConfig(t, "dedicated", "42"))
				default:
					t.Errorf("unexpected request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			defer close(release)
			httpClient := ts.Client()
			if mode == "client timeout" {
				httpClient.Timeout = 200 * time.Millisecond
			}
			client, err := NewAppClient(ts.URL, AppAuth{AppID: 123, InstallationID: 456, PrivateKeyFile: testPrivateKeyFile(t)}, httpClient)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			out, err := client.CreateRunnerJITConfig(ctx, "o/r", "", "dedicated", []string{"windows"}, func() error { dispatches.Add(1); return nil })
			if mode == "success" || mode == "response lost" {
				if posts.Load() != 1 || dispatches.Load() != 1 || tokenCalls.Load() != 1 || !out.CleanupRequired {
					t.Fatalf("dispatch or cleanup evidence: posts=%d dispatches=%d token=%d cleanup=%t", posts.Load(), dispatches.Load(), tokenCalls.Load(), out.CleanupRequired)
				}
				if (err == nil) != (mode == "success") {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || out.CleanupRequired || dispatches.Load() != 0 || posts.Load() != 0 || strings.Contains(err.Error(), "private authentication error") {
				t.Fatalf("unsent registration retained: cleanup=%t dispatches=%d posts=%d err=%v", out.CleanupRequired, dispatches.Load(), posts.Load(), err)
			}
			if status, ok := ErrorStatus(err); mode == "rejected" && (!ok || status != http.StatusForbidden) {
				t.Fatalf("lost auth rejection: %v", err)
			}
			if mode == "rate limited" || mode == "secondary limit" {
				status, ok := ErrorStatus(err)
				wantStatus := http.StatusTooManyRequests
				if mode == "secondary limit" {
					wantStatus = http.StatusForbidden
				}
				if !ok || status != wantStatus || !IsRateLimitError(err) {
					t.Fatalf("lost auth rate limit: %v", err)
				}
			}
			if retryAfter, ok := RateLimitRetryAfter(err); mode == "rate limited" && (!ok || retryAfter != "3") {
				t.Fatalf("lost auth retry delay: %v", err)
			}
			if (mode == "timeout" || mode == "client timeout") && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("lost timeout: %v", err)
			}
		})
	}
}
