package server

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

func terminalProcessStore(t *testing.T) (state.Store, string) {
	t.Helper()
	dsn := os.Getenv("RUNNERD_TERMINAL_PROCESS_DSN")
	if dsn == "" {
		dsn = catalogServerTestDSN(t)
	}
	return state.NewWithOptions(state.Options{Backend: state.BackendPostgres, DatabaseDSN: dsn, MigrateOnStart: true}), dsn
}

func TestTerminalSocketProcess(t *testing.T) {
	if ready := os.Getenv("RUNNERD_TERMINAL_PROCESS_READY"); ready != "" {
		store, _ := terminalProcessStore(t)
		f := newTerminalFixtureStore(t, "runner", store)
		if err := os.WriteFile(ready, []byte(f.http.URL), 0o600); err != nil {
			t.Fatal(err)
		}
		<-t.Context().Done()
		return
	}
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("opt-in PostgreSQL process test")
	}
	store, dsn := terminalProcessStore(t)
	f := newTerminalFixtureStore(t, "runner", store)
	ready := filepath.Join(t.TempDir(), "ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestTerminalSocketProcess$", "-test.timeout=90s")
	cmd.Env = append(os.Environ(), "RUNNERD_TERMINAL_PROCESS_READY="+ready, "RUNNERD_TERMINAL_PROCESS_DSN="+dsn)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	var address []byte
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		address, _ = os.ReadFile(ready)
		if len(address) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(address) == 0 {
		t.Fatal("child node did not start")
	}
	remote := f
	remote.http = &httptest.Server{URL: string(address)}
	a := remote.dial(t)
	aID := readTerminalReady(t, a)
	b := f.dial(t)
	bID := readTerminalReady(t, b)
	_ = b.SetReadDeadline(time.Time{})
	// Continue reading to answer protocol pings on node B while A is dead.
	go func() {
		for {
			if _, _, err := b.ReadMessage(); err != nil {
				return
			}
		}
	}()
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	a.Close()
	var lost state.TerminalSession
	for deadline := time.Now().Add(55 * time.Second); time.Now().Before(deadline); {
		if err := store.ExpireTerminalSessions(context.Background()); err != nil {
			t.Fatal(err)
		}
		lost, _ = store.GetTerminalSession(context.Background(), aID)
		if lost.Status == "closed" {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lost.Status != "closed" || lost.Cleanup != "unknown" || lost.CloseReason != "connection_owner_lost" {
		t.Fatalf("lost node record %+v", lost)
	}
	active, _ := store.GetTerminalSession(context.Background(), bID)
	if active.Status != "active" {
		t.Fatalf("other node session affected %+v", active)
	}
	next := f.dial(t)
	nextID := readTerminalReady(t, next)
	if nextID == aID || nextID == bID {
		t.Fatal("session identity reused")
	}
	next.Close()
	waitTerminalClosed(t, f, nextID)
	b.Close()
	waitTerminalClosed(t, f, bID)
	st, _ := store.ReadState("terminal-runner")
	if st.Status != state.StatusRunning {
		t.Fatal("console node failure changed job")
	}
}
