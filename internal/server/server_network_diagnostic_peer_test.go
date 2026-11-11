package server

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func TestNetworkDiagnosticPeer(t *testing.T) {
	checkDiagnosticPeer(t, state.BackendSQLite, filepath.Join(t.TempDir(), "runner.db"))
}

func TestNetworkDiagnosticPeerPostgresSQLBackends(t *testing.T) {
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL test database")
	}
	checkDiagnosticPeer(t, state.BackendPostgres, catalogServerTestDSN(t))
}

func peerDiagnosticStatus(s *Server) int {
	req := adminRequest(http.MethodPost, "/runner_requests/peer-probe/network-diagnostics", strings.NewReader(`{"target":"github_api"}`))
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	return rec.Code
}

func TestNetworkDiagnosticOwnerProcess(t *testing.T) {
	dsn := os.Getenv("RUNNERD_DIAGNOSTIC_TEST_DSN")
	if dsn == "" {
		t.Skip("child of diagnostic peer test")
	}
	store := state.NewWithOptions(state.Options{Backend: os.Getenv("RUNNERD_DIAGNOSTIC_TEST_BACKEND"), DatabaseDSN: dsn})
	block, started := make(chan struct{}), make(chan struct{}, 1)
	srv := newTestServerWithoutLoops(t, store, "", &fakeSandbox{diagnosticStarted: started, diagnosticBlock: block}, 1)
	go func() {
		<-started
		fmt.Println("probe entered")
		var one [1]byte
		_, _ = os.Stdin.Read(one[:])
		close(block)
	}()
	if code := peerDiagnosticStatus(srv); code != http.StatusOK {
		t.Fatalf("owner returned %d", code)
	}
}

func checkDiagnosticPeer(t *testing.T, backend, dsn string) {
	t.Helper()
	store := state.NewWithOptions(state.Options{Backend: backend, DatabaseDSN: dsn, MigrateOnStart: true})
	srv := newTestServerWithoutLoops(t, store, "", &fakeSandbox{}, 1)
	_, st, err := store.CreateRequest(state.RunnerRequest{ID: "peer-probe", RunnerName: "test"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	st.SandboxID, st.ProcessPID, st.Status = "sandbox-1", 42, state.StatusRunning
	for _, action := range []string{"finish", "kill"} {
		st.ProvisionAttemptID = action
		if err := store.WriteState(st); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestNetworkDiagnosticOwnerProcess$")
		cmd.Env = append(os.Environ(), "RUNNERD_DIAGNOSTIC_TEST_DSN="+dsn, "RUNNERD_DIAGNOSTIC_TEST_BACKEND="+backend)
		cmd.Stderr = os.Stderr
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
			}
		})
		ready := bufio.NewScanner(out)
		for ready.Scan() && ready.Text() != "probe entered" {
		}
		if ready.Text() != "probe entered" {
			t.Fatal("owner never entered provider call", ready.Err())
		}
		if code := peerDiagnosticStatus(srv); code != 429 {
			t.Fatalf("%s: second node during probe got %d", action, code)
		}
		if action == "kill" {
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
		} else if _, err := in.Write([]byte{1}); err != nil {
			t.Fatal(err)
		}
		_ = in.Close()
		for ready.Scan() {
		}
		err = cmd.Wait()
		if (err == nil) != (action == "finish") {
			t.Fatal("owner exit", action, err)
		}
		if code := peerDiagnosticStatus(srv); code != 429 {
			t.Fatalf("%s: second node after owner exit got %d", action, code)
		}
		st, err = store.ReadState(st.ID)
		if err != nil {
			t.Fatal(err)
		}
	}
	events, _, err := store.ListRunnerEvents(st.ID, 0, 10, "control_log")
	if err != nil || len(events) != 1 || events[0].Stage != "network_diagnostic" {
		t.Fatal("unexpected diagnostic evidence", len(events), err)
	}

	if backend == state.BackendPostgres {
		// Exercise the configured ten-second command budget and thirty-second
		// cooldown with the real DB clock after a SIGKILL. The fast state
		// tests independently verify the exact expiry boundary on all dialects.
		started := time.Now()
		timer := time.NewTimer(runnerNetworkDiagnosticTimeout + runnerNetworkDiagnosticCooldown)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-t.Context().Done():
			t.Fatal(t.Context().Err())
		}
		if code := peerDiagnosticStatus(srv); code != http.StatusOK {
			t.Fatalf("after abandoned-claim expiry: got %d", code)
		}
		t.Logf("surviving node successfully probed the same attempt at the %.2fs post-kill check", time.Since(started).Seconds())
	}
}
