package sandboxrunner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDeploymentTerminalConnectUsesRemainingLifetime(t *testing.T) {
	calls, timeout := 0, int32(0)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
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
