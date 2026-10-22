package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/qiniu/ci-runner/internal/state"
)

type fakeMigrationController struct {
	mu       sync.Mutex
	commands []string
	started  chan struct{}
	release  chan struct{}
}

func (f *fakeMigrationController) Execute(ctx context.Context, command string) error {
	f.mu.Lock()
	f.commands = append(f.commands, command)
	f.mu.Unlock()
	if f.started != nil {
		close(f.started)
		select {
		case <-f.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (f *fakeMigrationController) Status(context.Context) (state.MigrationStatus, error) {
	return state.MigrationStatus{Primary: "postgres", Phase: "postgres_primary", Epoch: 2}, nil
}

func TestMigrationControlPrivateSocketAndSerialization(t *testing.T) {
	// macOS limits Unix socket path length; Go test temp roots can exceed it.
	directory, err := os.MkdirTemp("/tmp", "runnerd-control-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	fake := &fakeMigrationController{started: make(chan struct{}), release: make(chan struct{})}
	server, err := startMigrationControl(directory, fake)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	info, err := os.Stat(filepath.Join(directory, migrationSocketFilename))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("control socket is not private: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- sendMigrationCommand(context.Background(), directory, "cutover", &bytes.Buffer{}) }()
	<-fake.started
	if err := sendMigrationCommand(context.Background(), directory, "rollback", &bytes.Buffer{}); err == nil {
		t.Fatal("concurrent mutation was accepted")
	}
	close(fake.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	if err := sendMigrationCommand(context.Background(), directory, "status", &output); err != nil {
		t.Fatal(err)
	}
	var status state.MigrationStatus
	if err := json.Unmarshal(output.Bytes(), &status); err != nil || status.Primary != "postgres" {
		t.Fatalf("CLI did not read the running process's authority: %v", err)
	}
	if err := sendMigrationCommand(context.Background(), directory, "postgres://operator-input", &bytes.Buffer{}); err == nil {
		t.Fatal("arbitrary command reached migration controller")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.commands) != 1 || fake.commands[0] != "cutover" {
		t.Fatalf("CLI bypassed fixed/serialized commands: %v", fake.commands)
	}
}

func TestMigrationControlRejectsSharedDirectoryAndNonSocket(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "runnerd-control-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(directory)
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if server, err := startMigrationControl(directory, &fakeMigrationController{}); err == nil {
		_ = server.Close()
		t.Fatal("shared directory accepted")
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, migrationSocketFilename)
	if err := os.WriteFile(path, []byte("preserve me"), 0o600); err != nil {
		t.Fatal(err)
	}
	if server, err := startMigrationControl(directory, &fakeMigrationController{}); err == nil {
		_ = server.Close()
		t.Fatal("control replaced a non-socket file")
	}
	if content, err := os.ReadFile(path); err != nil || string(content) != "preserve me" {
		t.Fatal("control deleted an unrelated file")
	}
}
