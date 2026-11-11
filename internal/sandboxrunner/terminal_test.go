package sandboxrunner

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	qnsandbox "github.com/qiniu/go-sdk/v7/sandbox"
)

func TestDeploymentTerminalConnectUsesRemainingLifetime(t *testing.T) {
	calls, timeout := 0, int32(0)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != "/v2/sandboxes/deployment-sandbox/connect" {
			t.Errorf("unexpected terminal request: %s %s", r.Method, r.URL.Path)
		}
		var input struct {
			Timeout int32 `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			t.Error(err)
		}
		timeout = input.Timeout
		w.WriteHeader(http.StatusBadRequest) // Capture Connect without creating a PTY.
	}))
	defer api.Close()
	svc, err := NewE2BService("test-key", api.URL, api.Client())
	if err != nil {
		t.Fatal(err)
	}
	_, err = svc.StartTerminalUntil(context.Background(), "deployment-sandbox", PtySize{Cols: 100, Rows: 28}, time.Now().Add(20*time.Minute), nil)
	if err == nil || calls != 1 || timeout < 1190 || timeout > 1200 {
		t.Fatalf("connect calls=%d timeout=%d err=%v", calls, timeout, err)
	}
	_, err = svc.StartTerminalUntil(context.Background(), "deployment-sandbox", PtySize{}, time.Now().Add(-time.Second), nil)
	if err == nil || calls != 1 {
		t.Fatal("expired terminal contacted provider")
	}
}

// The envd proxy returns an ordinary HTTP 404 once the Sandbox is deleted.
// Exercise the SDK's actual Kill transport instead of substituting a sentinel.
func TestTerminalCloseAfterSandboxRemoval(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		killStatus, infoStatus int
		wantError              bool
	}{
		{"live PTY closes normally", 200, 0, false},
		{"removed Sandbox", 404, 404, false},
		{"envd missing but Sandbox exists", 404, 200, true},
		{"cannot verify permission", 404, 403, true},
		{"cannot verify provider availability", 503, 503, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			killCalls, infoCalls := 0, 0
			transport := terminalCloseTransport(func(r *http.Request) (*http.Response, error) {
				status, contentType, body := http.StatusOK, "application/json", "{}"
				switch r.Method + " " + r.URL.Path {
				case "POST /v2/sandboxes/terminal-sandbox/connect":
					body = `{"sandboxID":"terminal-sandbox","templateID":"base","envdAccessToken":"fixture","domain":"sandbox.invalid"}`
				case "POST /process.Process/SendSignal":
					killCalls++
					status = tc.killStatus
					contentType, body = "application/proto", ""
					if status != http.StatusOK {
						contentType, body = "text/html", "<h1>Sandbox Not Found</h1>"
					}
				case "GET /sandboxes/terminal-sandbox":
					infoCalls++
					if r.URL.Host != "api.invalid" || r.Header.Get("X-API-Key") != "fixture-key" {
						t.Error("absence lookup did not use the original Sandbox endpoint and credential")
					}
					status = tc.infoStatus
					body = `{"sandboxID":"terminal-sandbox","templateID":"base"}`
				default:
					return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})
			svc, err := NewE2BService("fixture-key", "https://api.invalid", &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			sb, err := svc.client.Connect(context.Background(), "terminal-sandbox", qnsandbox.ConnectParams{})
			if err != nil {
				t.Fatal(err)
			}
			session := &e2bTerminalSession{sandbox: sb, pty: sb.Pty(), pid: 612}
			err = session.Close(context.Background())
			if (err != nil) != tc.wantError {
				t.Fatalf("Close()=%v, wantError=%v", err, tc.wantError)
			}
			if tc.wantError && !strings.HasPrefix(err.Error(), "kill process:") {
				t.Fatalf("lost original PTY failure: %v", err)
			}
			wantInfoCalls := 1
			if tc.killStatus == http.StatusOK {
				wantInfoCalls = 0
			}
			if killCalls != 1 || infoCalls != wantInfoCalls {
				t.Fatalf("Kill calls=%d, info calls=%d, want 1/%d", killCalls, infoCalls, wantInfoCalls)
			}
		})
	}
}

type terminalCloseTransport func(*http.Request) (*http.Response, error)

func (f terminalCloseTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
