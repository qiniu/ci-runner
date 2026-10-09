package sandboxrunner

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

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
