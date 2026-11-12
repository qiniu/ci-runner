package sandboxrunner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
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
	_, err = svc.StartTerminal(context.Background(), TerminalStart{ID: strings.Repeat("a", 32), SandboxID: "deployment-sandbox", Size: PtySize{Cols: 100, Rows: 28}, ExpiresAt: time.Now().Add(20 * time.Minute)}, nil)
	if err == nil || calls != 1 || timeout < 1190 || timeout > 1200 {
		t.Fatalf("connect calls=%d timeout=%d err=%v", calls, timeout, err)
	}
	_, err = svc.StartTerminal(context.Background(), TerminalStart{ID: strings.Repeat("a", 32), SandboxID: "deployment-sandbox", ExpiresAt: time.Now().Add(-time.Second)}, nil)
	if err == nil || calls != 1 {
		t.Fatal("expired terminal contacted provider")
	}
}

// Opt-in transport integration against an existing, unmodified local envd.
// Only the control-plane Connect response is a fixture; files, command streams,
// private-socket operations and the Linux PTY use the real SDK/API adapter.
func TestTerminalExistingAPIIntegration(t *testing.T) {
	endpoint := os.Getenv("RUNNERD_TERMINAL_ENVD_TEST_URL")
	if endpoint == "" {
		t.Skip("set RUNNERD_TERMINAL_ENVD_TEST_URL to disposable loopback envd")
	}
	base, err := url.Parse(endpoint)
	if err != nil || base.Hostname() != "127.0.0.1" || base.Scheme != "http" {
		t.Fatal("loopback HTTP fixture required")
	}
	client := &http.Client{Transport: terminalAdapterTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v2/sandboxes/terminal-fixture/connect" {
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"sandboxID":"terminal-fixture","templateID":"fixture","domain":"fixture.invalid","envdAccessToken":""}`)), Request: r}, nil
		}
		if strings.Contains(r.URL.Path, "SendSignal") || strings.Contains(r.URL.Path, "SendInput") || strings.Contains(r.URL.Path, "Update") {
			t.Errorf("unsafe PID operation %s", r.URL.Path)
		}
		clone := r.Clone(r.Context())
		u := *r.URL
		u.Scheme, u.Host = base.Scheme, base.Host
		clone.URL = &u
		clone.Host = base.Host
		return http.DefaultTransport.RoundTrip(clone)
	})}
	svc, err := NewE2BService("fixture", "http://control.invalid", client)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var nonce [16]byte
	_, _ = rand.Read(nonce[:])
	var mu sync.Mutex
	var output strings.Builder
	term, err := svc.StartTerminal(ctx, TerminalStart{ID: hex.EncodeToString(nonce[:]), SandboxID: "terminal-fixture", Size: PtySize{Cols: 100, Rows: 28}}, func(data []byte) { mu.Lock(); output.Write(data); mu.Unlock() })
	if err != nil {
		t.Fatal(err)
	}
	defer term.Close(context.Background())
	if err = term.Resize(ctx, PtySize{Cols: 121, Rows: 41}); err != nil {
		t.Fatal(err)
	}
	if err = term.SendInput(ctx, []byte("stty size; printf '__SDK_%s__\\n' adapter\n")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	found := false
	for time.Now().Before(deadline) {
		mu.Lock()
		found = strings.Contains(output.String(), "41 121") && strings.Contains(output.String(), "__SDK_adapter__")
		mu.Unlock()
		if found {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		mu.Lock()
		t.Errorf("missing real PTY output %q", output.String())
		mu.Unlock()
	}
	if err = term.KeepAlive(ctx); err != nil {
		t.Fatal(err)
	}
	// Natural shell completion must deliver its final output through the actual
	// SDK stream before Done closes, not just through a fake terminal callback.
	if err = term.SendInput(ctx, []byte("python3 -c 'import os; os.write(1, bytes([122])*80000)'; printf '__TAIL_%s__\\n' complete; exit\n")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-term.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("helper did not exit")
	}
	mu.Lock()
	complete := strings.Contains(output.String(), strings.Repeat("z", 80000)) && strings.Contains(output.String(), "__TAIL_complete__")
	mu.Unlock()
	if !complete {
		t.Fatal("natural completion lost final output")
	}
	if err = term.Close(ctx); err != nil {
		t.Fatal(err)
	}
}

type terminalAdapterTransport func(*http.Request) (*http.Response, error)

func (f terminalAdapterTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTerminalCloseRequiresConfirmedAbsence(t *testing.T) {
	for _, status := range []int{200, 403, 404, 503} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client := &http.Client{Transport: terminalAdapterTransport(func(r *http.Request) (*http.Response, error) {
				code, body := 403, `{"message":"unavailable"}`
				switch r.URL.Path {
				case "/v2/sandboxes/fixture/connect":
					code, body = 200, `{"sandboxID":"fixture","templateID":"fixture","domain":"fixture.invalid","envdAccessToken":""}`
				case "/sandboxes/fixture":
					code, body = status, `{"sandboxID":"fixture","templateID":"fixture"}`
				}
				if strings.Contains(r.URL.Path, "SendSignal") {
					t.Error("close used PID")
				}
				return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
			})}
			api, err := qnsandbox.NewClient(&qnsandbox.Config{APIKey: "fixture", Endpoint: "http://control.invalid", HTTPClient: client, RetryMax: new(int)})
			if err != nil {
				t.Fatal(err)
			}
			sb, err := api.Connect(context.Background(), "fixture", qnsandbox.ConnectParams{})
			if err != nil {
				t.Fatal(err)
			}
			term := &helperTerminal{sandbox: sb, directory: "/tmp/fixture", done: make(chan struct{})}
			err = term.Close(context.Background())
			if (err == nil) != (status == 404) {
				t.Fatalf("status %d close %v", status, err)
			}
		})
	}
}

func TestTerminalHelperLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux supervisor; run disposable Linux fixture for PTY/PID validation")
	}
	cmd := exec.CommandContext(t.Context(), "python3", "-B", "terminal/helper_test.py")
	// Never force kernel PID reuse in the host test namespace.
	cmd.Env = append(os.Environ(), "RUNNERD_TERMINAL_PID_REUSE_TEST=0")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper: %v\n%s", err, output)
	}
}

func TestTerminalProtocolIgnoresShellStartupOutput(t *testing.T) {
	// Both login profiles and BASH_ENV can print arbitrary text or even JSON,
	// including an unterminated banner immediately before the first frame.
	stream := "welcome\n{\"ready\":999}\nBASH_ENV banner" + terminalProtocolPrefix + `{"ready":42}` + "\n" + terminalProtocolPrefix + `{"data":"dGFpbA=="}` + "\n"
	for _, chunkSize := range []int{1, 7, len(stream)} {
		var reader terminalOutputReader
		var frames []terminalOutputFrame
		for offset := 0; offset < len(stream); offset += chunkSize {
			if err := reader.feed([]byte(stream[offset:min(offset+chunkSize, len(stream))]), func(frame terminalOutputFrame) { frames = append(frames, frame) }); err != nil {
				t.Fatal(err)
			}
		}
		if len(frames) != 2 || frames[0].Ready != 42 || string(frames[1].Data) != "tail" {
			t.Fatalf("chunk size %d: %+v", chunkSize, frames)
		}
	}
	for _, stream := range []string{strings.Repeat("x", 16385), terminalProtocolPrefix + "invalid\n", terminalProtocolPrefix + strings.Repeat("x", 8193), terminalProtocolPrefix + "{}\nmissing prefix\n"} {
		var reader terminalOutputReader
		if err := reader.feed([]byte(stream), func(terminalOutputFrame) {}); err == nil {
			t.Fatal("unbounded or malformed protocol accepted")
		}
	}
	var boundary terminalOutputReader
	for _, chunk := range []string{strings.Repeat("x", 16384) + terminalProtocolPrefix[:1], terminalProtocolPrefix[1:] + "{}\n"} {
		if err := boundary.feed([]byte(chunk), func(terminalOutputFrame) {}); err != nil {
			t.Fatalf("split marker counted as banner: %v", err)
		}
	}
	const nonce = "request-nonce"
	ack := terminalProtocolPrefix + nonce + ":ok\n"
	for _, banner := range []string{"", "welcome\n", "unterminated BASH_ENV output"} {
		if !terminalCallConfirmed(banner+ack, nonce) {
			t.Fatalf("banner rejected: %q", banner)
		}
	}
	for _, stdout := range []string{"ok\n", ack, ack + "extra", strings.Repeat("x", 16385) + ack} {
		if terminalCallConfirmed(stdout, "different-request") {
			t.Fatal("unrelated acknowledgement accepted")
		}
	}
	if terminalCallConfirmed(ack+"extra", nonce) || terminalCallConfirmed(strings.Repeat("x", 16385)+ack, nonce) {
		t.Fatal("unbounded or malformed acknowledgement accepted")
	}
}
