package github

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
				if _, err := client.CreateRunnerJITConfig(t.Context(), "o/r", group, "dedicated", []string{"windows", "one-job"}); err != nil {
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
		_, err := NewClient(ts.URL, ts.Client()).CreateRunnerJITConfig(t.Context(), "o/r", "", "dedicated", []string{"windows"})
		ts.Close()
		if err == nil || strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("invalid or unredacted error: %v", err)
		}
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "secret-value", http.StatusForbidden) }))
	defer ts.Close()
	_, err := NewClient(ts.URL, ts.Client()).CreateRunnerJITConfig(t.Context(), "o/r", "", "dedicated", []string{"windows"})
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
	_, err := NewClient(ts.URL, ts.Client()).CreateRunnerJITConfig(t.Context(), "o/r", "", "dedicated", []string{"windows"})
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
