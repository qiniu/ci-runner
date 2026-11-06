package sandboxrunner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	qnsandbox "github.com/qiniu/go-sdk/v7/sandbox"
)

// newTestService creates an E2BService wired to the given httptest.Server.
func newTestService(t *testing.T, ts *httptest.Server) *E2BService {
	t.Helper()
	svc, err := NewE2BService("test-api-key", ts.URL, ts.Client())
	if err != nil {
		t.Fatalf("NewE2BService: %v", err)
	}
	return svc
}

func TestCreateRunnerSandboxTagsProvisionAttempt(t *testing.T) {
	var called atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Metadata map[string]string `json:"metadata"`
		}
		if r.Method != http.MethodPost || r.Header.Get("Idempotency-Key") != "runnerd-attempt-2" || json.NewDecoder(r.Body).Decode(&body) != nil ||
			body.Metadata["app"] != "e2b-github-runner" || body.Metadata["request_id"] != "job" ||
			body.Metadata["provision_attempt_id"] != "attempt-2" {
			t.Errorf("unexpected create key %q or metadata %v", r.Header.Get("Idempotency-Key"), body.Metadata)
		}
		called.Store(true)
		http.Error(w, "fixture rejects creation", http.StatusBadRequest)
	}))
	defer api.Close()
	_, err := newTestService(t, api).StartRunner(t.Context(), StartInput{RequestID: "job", ProvisionAttemptID: "attempt-2", TemplateID: "template"})
	if !called.Load() || err == nil {
		t.Fatalf("expected fixture to receive and reject create: called=%v err=%v", called.Load(), err)
	}
}

func TestFindRunnerSandboxByProvisionAttempt(t *testing.T) {
	const legacy = `{"sandboxID":"historical","metadata":{"app":"e2b-github-runner","request_id":"job"}}`
	const current = `{"sandboxID":"current","metadata":{"app":"e2b-github-runner","request_id":"job","provision_attempt_id":"attempt-2"}}`
	for _, tc := range []struct {
		name, attempt, response, want string
		notFound, ambiguous           bool
	}{
		{"missing metadata", "", `[{"sandboxID":"unverified"}]`, "", false, true},
		{"current", "attempt-2", "[" + current + "]", "current", false, false},
		{"absent", "attempt-2", "[]", "", true, false},
		{"cannot verify", "attempt-2", "[" + legacy + "]", "", false, true},
		{"duplicate external create", "attempt-2", "[" + current + "," + current + "]", "", false, true},
		{"historical", "", "[" + current + "," + legacy + "]", "historical", false, false},
		{"historical excludes retry", "", "[" + current + "]", "", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				filter, err := url.ParseQuery(r.URL.Query().Get("metadata"))
				if err != nil || filter.Get("request_id") != "job" || filter.Get("app") != "e2b-github-runner" ||
					filter.Has("provision_attempt_id") != (tc.attempt != "") || filter.Get("provision_attempt_id") != tc.attempt {
					t.Errorf("incorrect sandbox filter: %v (%v)", filter, err)
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.response)
			}))
			defer ts.Close()
			id, err := newTestService(t, ts).findRunnerSandbox(t.Context(), "job", tc.attempt)
			if id != tc.want || errors.Is(err, ErrSandboxNotFound) != tc.notFound ||
				(err != nil && !errors.Is(err, ErrSandboxNotFound)) != tc.ambiguous {
				t.Fatalf("lookup returned id=%q err=%v", id, err)
			}
		})
	}
}

func TestFindRunnerSandboxListFailureIsNotAbsence(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer api.Close()
	id, err := newTestService(t, api).findRunnerSandbox(t.Context(), "job", "attempt")
	if id != "" || err == nil || errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("failed provider list was treated as discovery: id=%q err=%v", id, err)
	}
}

func TestStopRunnerCapsLifetimeBeforeDelete(t *testing.T) {
	for _, tc := range []struct {
		name         string
		updateStatus int // Zero simulates a stalled timeout update.
		deleteStatus int
		wantReason   string
	}{
		{"delete fails after cap", http.StatusNoContent, http.StatusForbidden, ""},
		{"cap rejected", http.StatusForbidden, http.StatusNoContent, "update_failed"},
		{"cap and delete rejected", http.StatusForbidden, http.StatusForbidden, "update_failed"},
		{"cap stalls", 0, http.StatusNoContent, "deadline_exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var logs strings.Builder
			previous := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
			t.Cleanup(func() { slog.SetDefault(previous) })
			var updateCalled, deleteCalled atomic.Bool
			mux := http.NewServeMux()
			mux.HandleFunc("POST /v2/sandboxes/exact/connect", func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"sandboxID":"exact","envdAccessToken":"fixture"}`)
			})
			mux.HandleFunc("POST /sandboxes/exact/timeout", func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Timeout int `json:"timeout"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Timeout != 30 {
					t.Errorf("cleanup timeout = %d, decode error = %v", body.Timeout, err)
				}
				updateCalled.Store(true)
				if tc.updateStatus == 0 {
					<-r.Context().Done()
					return
				}
				w.WriteHeader(tc.updateStatus)
				if tc.updateStatus >= 400 {
					fmt.Fprint(w, `{"message":"provider-private-detail"}`)
				}
			})
			mux.HandleFunc("DELETE /sandboxes/exact", func(w http.ResponseWriter, _ *http.Request) {
				if !updateCalled.Load() {
					t.Error("Sandbox deleted before timeout update")
				}
				deleteCalled.Store(true)
				w.WriteHeader(tc.deleteStatus)
			})
			api := httptest.NewServer(mux)
			defer api.Close()
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := newTestService(t, api).StopRunner(ctx, "exact", 0)
			if !deleteCalled.Load() || (err != nil) != (tc.deleteStatus != http.StatusNoContent) {
				t.Fatalf("delete called = %t, StopRunner error = %v", deleteCalled.Load(), err)
			}
			if tc.wantReason == "" {
				if logs.Len() != 0 {
					t.Errorf("unexpected lifetime warning: %s", &logs)
				}
			} else if !strings.Contains(logs.String(), "reason="+tc.wantReason) || strings.Contains(logs.String(), "provider-private-detail") {
				t.Errorf("expected sanitized lifetime warning with reason %q, got %s", tc.wantReason, &logs)
			}
		})
	}
}

// templateJSON returns a minimal TemplateWithBuilds JSON payload with the given build status entries.
// Pass no statuses to return an empty builds list.
func templateJSON(id string, statuses ...string) string {
	builds := make([]string, 0, len(statuses))
	for i, s := range statuses {
		builds = append(builds, `{"buildID":"00000000-0000-0000-0000-`+
			strings.Repeat("0", 11)+string(rune('0'+i))+`","cpuCount":2,"memoryMB":1024,`+
			`"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:00:00Z",`+
			`"status":"`+s+`"}`)
	}
	body := `{"templateID":"` + id + `","aliases":[],"public":false,"spawnCount":0,` +
		`"createdAt":"2024-01-01T00:00:00Z","updatedAt":"2024-01-01T00:00:00Z",` +
		`"lastSpawnedAt":null,"builds":[` + strings.Join(builds, ",") + `]}`
	return body
}

// serveTemplate returns an http.Handler that serves the given status code and body for all requests.
func serveTemplate(code int, body string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	})
}

func TestValidateTemplate_EmptyID(t *testing.T) {
	// No HTTP call is made; the function short-circuits before calling the API.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected HTTP call for empty template ID")
	}))
	defer ts.Close()

	svc := newTestService(t, ts)
	err := svc.ValidateTemplate(context.Background(), "")
	if !errors.Is(err, ErrTemplateRequired) {
		t.Fatalf("expected ErrTemplateRequired, got %v", err)
	}
}

func TestValidateTemplate_WhitespaceOnlyID(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("unexpected HTTP call for whitespace template ID")
	}))
	defer ts.Close()

	svc := newTestService(t, ts)
	err := svc.ValidateTemplate(context.Background(), "   ")
	if !errors.Is(err, ErrTemplateRequired) {
		t.Fatalf("expected ErrTemplateRequired, got %v", err)
	}
}

func TestValidateTemplate_NotFound(t *testing.T) {
	ts := httptest.NewServer(serveTemplate(http.StatusNotFound, `{"code":"not_found","message":"template not found"}`))
	defer ts.Close()

	svc := newTestService(t, ts)
	err := svc.ValidateTemplate(context.Background(), "missing-tpl")
	if !errors.Is(err, ErrTemplateNotFound) {
		t.Fatalf("expected ErrTemplateNotFound, got %v", err)
	}
}

func TestValidateTemplateEffectiveDefaultBuild(t *testing.T) {
	const usableID = "00000000-0000-0000-0000-000000000001"
	for _, tt := range []struct {
		name           string
		owner          bool
		public         bool
		buildID        string
		status         string
		history        []string
		missingCatalog bool
		wantErr        error
		wantUnknown    bool
	}{
		{name: "uploaded default", owner: true, buildID: usableID, status: "uploaded"},
		{name: "ready default", owner: true, buildID: usableID, status: "ready"},
		{name: "rebuilding keeps uploaded default", owner: true, buildID: usableID, status: "building", history: []string{"building"}},
		{name: "failed provider rebuild keeps uploaded default", owner: true, buildID: usableID, status: "failed"},
		{name: "failed rebuild keeps uploaded default", owner: true, buildID: usableID, status: "error"},
		{name: "no builds", owner: true, buildID: "00000000-0000-0000-0000-000000000000", status: "waiting", wantErr: ErrTemplateNotReady},
		{name: "other tag is ready but default is not", owner: true, status: "building", history: []string{"ready", "uploaded"}, wantErr: ErrTemplateNotReady},
		{name: "non-owner public default hides builds", public: true, buildID: usableID, status: "uploaded"},
		{name: "public default without a usable build", public: true, status: "waiting", wantErr: ErrTemplateNotReady},
		{name: "public outside default catalog", public: true, missingCatalog: true, wantUnknown: true},
		{name: "owner template removed from catalog", owner: true, missingCatalog: true, wantUnknown: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/templates/tpl":
					body := templateJSON("tpl", tt.history...)
					body = strings.Replace(body, `"public":false`, fmt.Sprintf(`"public":%t,"isOwner":%t`, tt.public, tt.owner), 1)
					_, _ = w.Write([]byte(body))
				case "/templates", "/default-templates":
					wantPath := "/default-templates"
					if tt.owner {
						wantPath = "/templates"
					}
					if r.URL.Path != wantPath {
						t.Errorf("catalog path = %s, want %s", r.URL.Path, wantPath)
					}
					if tt.missingCatalog {
						_, _ = w.Write([]byte(`[]`))
						return
					}
					_, _ = fmt.Fprintf(w, `[{"templateID":"tpl","buildID":%q,"buildStatus":%q}]`, tt.buildID, tt.status)
				default:
					t.Errorf("unexpected provider request: %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer ts.Close()
			err := newTestService(t, ts).ValidateTemplate(context.Background(), " tpl ")
			if tt.wantUnknown {
				if err == nil || !strings.Contains(err.Error(), "cannot be confirmed") {
					t.Fatalf("error = %v, want unknown build state", err)
				}
			} else if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
			if calls.Load() != 2 {
				t.Fatalf("provider requests = %d, want detail and effective catalog", calls.Load())
			}
		})
	}
}

func TestValidateTemplateProviderFailures(t *testing.T) {
	for _, stage := range []string{"detail", "catalog"} {
		for _, code := range []int{401, 403, 429, 500} {
			t.Run(fmt.Sprintf("%s/%d", stage, code), func(t *testing.T) {
				ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					if stage == "catalog" && r.URL.Path == "/templates/tpl" {
						_, _ = w.Write([]byte(`{"templateID":"tpl","isOwner":true,"builds":[]}`))
						return
					}
					w.WriteHeader(code)
					_, _ = w.Write([]byte(`{"message":"provider failed"}`))
				}))
				defer ts.Close()
				err := newTestService(t, ts).ValidateTemplate(context.Background(), "tpl")
				var apiErr *qnsandbox.APIError
				if !errors.As(err, &apiErr) || apiErr.StatusCode != code {
					t.Fatalf("error = %v, want provider status %d", err, code)
				}
			})
		}
	}
}

func TestValidateTemplateCanceled(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("canceled validation reached provider") }))
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := newTestService(t, ts).ValidateTemplate(ctx, "tpl"); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want canceled", err)
	}
}

func TestNewE2BService_ValidConfig(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	svc, err := NewE2BService("my-api-key", ts.URL, ts.Client())
	if err != nil {
		t.Fatalf("NewE2BService: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

func TestNewE2BService_EmptyAPIKey(t *testing.T) {
	// Empty API key is allowed — the SDK doesn't validate it at construction time.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer ts.Close()

	svc, err := NewE2BService("", ts.URL, ts.Client())
	if err != nil {
		t.Fatalf("NewE2BService with empty key: %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil service")
	}
}

func TestE2BServiceListDefaultTemplates(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET request, got %s", r.Method)
		}
		if r.URL.Path != "/default-templates" {
			t.Fatalf("request path = %q, want %q", r.URL.Path, "/default-templates")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"templateID":"tpl-public-runner","buildID":"00000000-0000-0000-0000-000000000001","names":["github-runner-ubuntu-24-04"],"buildStatus":"ready","cpuCount":2,"memoryMB":4096,"diskSizeMB":20480,"public":true,"spawnCount":3,"updatedAt":"2024-01-01T00:00:00Z"}]`))
	}))
	defer ts.Close()

	svc := newTestService(t, ts)
	items, err := svc.ListDefaultTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListDefaultTemplates: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected one template, got %d", len(items))
	}
	got := items[0]
	if got.TemplateID != "tpl-public-runner" {
		t.Errorf("TemplateID = %q, want %q", got.TemplateID, "tpl-public-runner")
	}
	if len(got.Names) != 1 || got.Names[0] != "github-runner-ubuntu-24-04" {
		t.Errorf("Names = %#v, want [github-runner-ubuntu-24-04]", got.Names)
	}
	if !got.Runnable() || got.BuildID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("effective default lost: %#v", got)
	}
	if got.BuildStatus != "ready" {
		t.Errorf("BuildStatus = %q, want %q", got.BuildStatus, "ready")
	}
	if !got.Public {
		t.Error("Public = false, want true")
	}
}

func TestE2BServiceInspectDeploymentSandbox(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/sandboxes/deploy-1" || r.Method != http.MethodGet || r.URL.RawQuery != "" {
			t.Fatalf("expected a read-only exact Sandbox lookup, got %s %s", r.Method, r.URL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"sandboxID":"deploy-1","templateID":"tpl","state":"running","cpuCount":4,"memoryMB":8192,"diskSizeMB":40960,"startedAt":"2026-10-13T00:00:00Z","endAt":"2026-10-13T01:00:00Z"}`))
	}))
	defer ts.Close()

	got, err := newTestService(t, ts).InspectDeploymentSandbox(context.Background(), " deploy-1 ")
	if err != nil {
		t.Fatal(err)
	}
	if got.SandboxID != "deploy-1" || got.TemplateID != "tpl" || got.CPUCount != 4 || got.MemoryMB != 8192 || got.DiskSizeMB != 40960 {
		t.Fatalf("unexpected sandbox resource snapshot: %#v", got)
	}
}

func TestInspectDeploymentSandboxBoundsAndSanitizesResponse(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{{"missing", 404, "provider-secret"}, {"denied", 403, "provider-secret"}, {"identity mismatch", 200, `{"sandboxID":"other"}`}, {"oversized", 200, strings.Repeat("x", (64<<10)+1)}} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Method != "GET" || r.URL.Path != "/sandboxes/exact" {
					t.Errorf("unexpected provider mutation/list: %s %s", r.Method, r.URL)
				}
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			defer api.Close()
			_, err := newTestService(t, api).InspectDeploymentSandbox(context.Background(), "exact")
			if err == nil || strings.Contains(err.Error(), "provider-secret") || calls != 1 {
				t.Fatalf("calls=%d error=%v", calls, err)
			}
			if test.status == 404 && !errors.Is(err, ErrSandboxNotFound) {
				t.Fatalf("missing Sandbox error=%v", err)
			}
		})
	}
}

func TestInspectTemplateMetadataFallsBackToEffectiveCatalog(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/templates/tpl":
			w.Write([]byte(`{"templateID":"tpl","isOwner":true,"public":true}`))
		case "/templates":
			w.Write([]byte(`[{"templateID":"tpl","names":["team/public"],"cpuCount":16,"memoryMB":32768,"diskSizeMB":81920,"envdVersion":"0.5.1","createdAt":"2026-01-02T03:04:05Z","updatedAt":"2026-10-09T06:00:00Z","buildID":"00000000-0000-0000-0000-000000000001"}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()
	info, err := newTestService(t, ts).InspectTemplate(context.Background(), "tpl")
	if err != nil {
		t.Fatal(err)
	}
	if !info.Public || !info.Runnable || info.Template == nil {
		t.Fatalf("inspection: %#v", info)
	}
	details := info.Template
	if details.TemplateID != "tpl" || len(details.Names) != 1 || details.Names[0] != "team/public" || details.CPUCount != 16 || details.MemoryMB != 32768 || details.DiskSizeMB != 81920 || details.EnvdVersion != "0.5.1" || details.CreatedAt != "2026-01-02T03:04:05Z" || details.UpdatedAt != "2026-10-09T06:00:00Z" {
		t.Fatalf("provider metadata: %#v", details)
	}
}
