package deployruntime

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	deploy "github.com/miclle/deployd"
)

func TestOutputLimiterBoundsAndDropsLaterChunks(t *testing.T) {
	var got strings.Builder
	limiter := newOutputLimiter(func(_ deploy.Stream, data []byte) { got.Write(data) }, nil)
	limiter.write(deploy.Stdout, []byte(strings.Repeat("x", maxCommandOutput-1)))
	limiter.write(deploy.Stderr, []byte("overflow"))
	if !limiter.Exceeded() || got.Len() != maxCommandOutput {
		t.Fatalf("limiter exceeded=%v bytes=%d", limiter.Exceeded(), got.Len())
	}
}

type fixtureRoutingTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t fixtureRoutingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL.Scheme, clone.URL.Host, clone.Host = t.target.Scheme, t.target.Host, t.target.Host
	return t.base.RoundTrip(clone)
}

func newStartProtocolFixture(t *testing.T, mode string) (*Handle, <-chan string) {
	t.Helper()
	deadlineHeader := make(chan string, 1)
	running := &atomic.Bool{}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/process.Process/") {
			_, _ = io.Copy(io.Discard, r.Body)
		}
		switch r.URL.Path {
		case "/sandboxes/exact/connect":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"sandboxID":"exact","templateID":"template-a","envdAccessToken":"fixture","envdVersion":"0.1","clientID":"fixture","domain":"example.test"}`)
		case "/process.Process/Start":
			deadlineHeader <- r.Header.Get("Connect-Timeout-Ms")
			if mode == "blocked_headers" {
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "application/connect+proto")
			w.WriteHeader(http.StatusOK)
			if mode == "confirmed" {
				running.Store(true)
				// StartResponse{event: ProcessEvent{start: {pid: 41}}}.
				payload := []byte{0x0a, 4, 0x0a, 2, 0x08, 41}
				var header [5]byte
				binary.BigEndian.PutUint32(header[1:], uint32(len(payload)))
				_, _ = w.Write(header[:])
				_, _ = w.Write(payload)
			}
			w.(http.Flusher).Flush()
			<-r.Context().Done()
		case "/process.Process/List":
			w.Header().Set("Content-Type", "application/proto")
			if running.Load() {
				// ListResponse{processes: [{pid: 41, tag: "attempt"}]}.
				_, _ = w.Write(append([]byte{0x0a, 11, 0x10, 41, 0x1a, 7}, []byte("attempt")...))
			}
		default:
			t.Errorf("unexpected process fixture request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	target, _ := url.Parse(api.URL)
	client := &http.Client{Transport: fixtureRoutingTransport{base: api.Client().Transport, target: target}}
	factory, err := NewFactory("fixture-key", api.URL, client)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := factory.Connect(context.Background(), "exact", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return handle, deadlineHeader
}

func TestSandboxStartKeepsStartupDeadlineOutOfRemoteLifetime(t *testing.T) {
	handle, deadlineHeader := newStartProtocolFixture(t, "confirmed")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	ref, err := handle.Start(ctx, deploy.Command{Script: "npm start", Directory: "/workspace", Tag: "attempt"}, nil)
	if err != nil || ref.ID != "41" {
		t.Fatalf("start: ref=%#v err=%v", ref, err)
	}
	if header := <-deadlineHeader; header != "" {
		t.Fatalf("startup deadline became remote workload lifetime: %s", header)
	}
	cancel()
	state, err := handle.Inspect(context.Background(), ref)
	if err != nil || !state.Running {
		t.Fatalf("confirmed service was lost on startup cancellation: %#v %v", state, err)
	}
}

func TestSandboxStartRejectsAlreadyCancelledContext(t *testing.T) {
	handle, deadlineHeader := newStartProtocolFixture(t, "confirmed")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ref, err := handle.Start(ctx, deploy.Command{Script: "npm start", Directory: "/workspace", Tag: "attempt"}, nil)
	if !errors.Is(err, context.Canceled) || ref.ID != "" || len(deadlineHeader) != 0 {
		t.Fatalf("cancelled startup reached provider: ref=%#v err=%v", ref, err)
	}
}

func TestSandboxStartStillBoundsConfirmation(t *testing.T) {
	for _, mode := range []string{"missing_pid", "blocked_headers"} {
		t.Run(mode, func(t *testing.T) {
			handle, deadlineHeader := newStartProtocolFixture(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			started := time.Now()
			ref, err := handle.Start(ctx, deploy.Command{Script: "npm start", Directory: "/workspace", Tag: "attempt"}, nil)
			if !errors.Is(err, context.DeadlineExceeded) || ref.ID != "" || time.Since(started) > time.Second {
				t.Fatalf("startup confirmation was unbounded: ref=%#v err=%v duration=%s", ref, err, time.Since(started))
			}
			if header := <-deadlineHeader; header != "" {
				t.Fatalf("startup deadline leaked into process RPC: %s", header)
			}
		})
	}
}

func TestValidateCommandRejectsUnsafeExecutionDirectories(t *testing.T) {
	for _, command := range []deploy.Command{
		{Script: "true", Directory: "relative"},
		{Script: "true", Directory: "/tmp\nunsafe"},
		{Script: "true", Directory: "/tmp"},
		{Script: "true"},
	} {
		err := validateCommand(command, false)
		if command.Directory == "/tmp" || command.Directory == "" {
			if err != nil {
				t.Fatalf("valid command rejected: %v", err)
			}
		} else if err == nil {
			t.Fatalf("unsafe command accepted: %#v", command)
		}
	}
	if err := validateCommand(deploy.Command{Script: "true", Directory: "/tmp"}, true); err == nil {
		t.Fatal("missing process tag accepted")
	}
}

func TestSanitizeProviderErrorNeverReturnsProviderText(t *testing.T) {
	err := sanitizeProviderError(context.Background(), errors.New("secret-provider-body"))
	if strings.Contains(err.Error(), "secret-provider-body") {
		t.Fatalf("provider text leaked: %v", err)
	}
}

func TestConnectPreservesSavedLifetimeAndRecognizesMissingSandbox(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/sandboxes/missing/connect" {
			w.WriteHeader(http.StatusNotFound)
			fmt.Fprint(w, `{"message":"gone"}`)
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/sandboxes/exact/connect" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var body struct {
			Timeout int `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Timeout != 1801 {
			t.Errorf("connect timeout=%d, want remaining saved lifetime", body.Timeout)
		}
		fmt.Fprint(w, `{"sandboxID":"exact","templateID":"template-a","envdAccessToken":"fixture","envdVersion":"0.1","clientID":"fixture","domain":"example.test"}`)
	}))
	defer api.Close()
	factory, err := NewFactory("fixture-key", api.URL, api.Client())
	if err != nil {
		t.Fatal(err)
	}
	handle, err := factory.Connect(context.Background(), "exact", 1800*time.Second+time.Millisecond)
	if err != nil || handle == nil || handle.SandboxID != "exact" {
		t.Fatalf("connect=%#v %v", handle, err)
	}
	if endpoint, err := handle.Endpoint(context.Background(), 4173); err != nil || endpoint != "https://4173-exact.example.test" {
		t.Fatalf("SDK hostname endpoint=%q %v", endpoint, err)
	}
	if _, err := factory.Connect(context.Background(), "missing", time.Minute); !errors.Is(err, ErrSandboxNotFound) {
		t.Fatalf("missing sandbox=%v", err)
	}
}

func TestReconcileCreateDoesNotCreateOrHideProviderFailure(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v2/sandboxes" {
					t.Errorf("reconciliation created/changed a resource: %s %s", r.Method, r.URL.Path)
				}
				if r.URL.Query().Get("metadata") != "app=qiniu-deployd&deployment_id=deployment-1-2" {
					t.Errorf("operation metadata=%s", r.URL.RawQuery)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				if status == http.StatusOK {
					fmt.Fprint(w, `[]`)
				} else {
					fmt.Fprint(w, `{"message":"sensitive-provider-details"}`)
				}
			}))
			defer api.Close()
			factory, err := NewFactory("fixture-key", api.URL, api.Client())
			if err != nil {
				t.Fatal(err)
			}
			handle, err := factory.ReconcileCreate(context.Background(), "template-a", "deployment-1-2", time.Hour)
			if handle != nil {
				t.Fatalf("unexpected recovered handle=%#v", handle)
			}
			if status == http.StatusOK && err != nil {
				t.Fatal(err)
			}
			if status != http.StatusOK && !errors.Is(err, ErrCreatePending) {
				t.Fatalf("provider outage declared empty=%v", err)
			}
		})
	}
}
