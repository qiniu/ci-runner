package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiniu/ci-runner/internal/state"
)

const migrationSocketFilename = "control.sock"

type migrationController interface {
	Execute(context.Context, string) error
	Status(context.Context) (state.MigrationStatus, error)
}

type migrationControlServer struct {
	server *http.Server
}

func validMigrationCommand(command string) bool {
	switch command {
	case "status", "prepare", "baseline", "replay", "verify", "cutover", "rollback", "finish", "abort", "prune":
		return true
	default:
		return false
	}
}

// This socket is deliberately separate from public/admin HTTP authentication:
// only the local operator with access to the private authority directory can
// issue fixed migration commands. No request supplies a DSN or SQL fragment.
func startMigrationControl(directory string, runtime migrationController) (*migrationControlServer, error) {
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("migration control requires a private authority directory")
	}
	path := filepath.Join(directory, migrationSocketFilename)
	if stale, err := os.Lstat(path); err == nil {
		if stale.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("migration control path is not a socket")
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("remove stale migration socket failed")
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect migration socket failed")
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		return nil, fmt.Errorf("listen on migration socket failed")
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("protect migration socket failed")
	}
	busy := make(chan struct{}, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		command := strings.TrimPrefix(request.URL.Path, "/")
		if !validMigrationCommand(command) || request.Method != http.MethodPost || request.URL.RawQuery != "" {
			http.Error(w, `{"error":"unsupported migration command"}`, http.StatusBadRequest)
			return
		}
		select {
		case busy <- struct{}{}:
			defer func() { <-busy }()
		default:
			http.Error(w, `{"error":"another migration command is running"}`, http.StatusConflict)
			return
		}
		ctx, cancel := context.WithTimeout(request.Context(), 120*time.Second)
		defer cancel()
		if command != "status" {
			if err := runtime.Execute(ctx, command); err != nil {
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
				return
			}
		}
		status, err := runtime.Status(ctx)
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "migration status unavailable; inspect authority recovery"})
			return
		}
		_ = json.NewEncoder(w).Encode(status)
	})
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout: 2 * time.Second, WriteTimeout: 130 * time.Second, IdleTimeout: 5 * time.Second, MaxHeaderBytes: 4096,
	}
	go func() { _ = server.Serve(listener) }()
	return &migrationControlServer{server: server}, nil
}

func (s *migrationControlServer) Close() error {
	return s.server.Close()
}

func sendMigrationCommand(ctx context.Context, directory, command string, output io.Writer) error {
	if directory == "" || !validMigrationCommand(command) {
		return fmt.Errorf("configure online migration and select a supported command")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("migration authority directory is unavailable or not private")
	}
	path := filepath.Join(directory, migrationSocketFilename)
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, "unix", path)
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 135 * time.Second}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://localhost/"+command, nil)
	if err != nil {
		return fmt.Errorf("construct migration command failed")
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("migration control connection failed; inspect running process and status before retrying")
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(body) > 65536 {
		return fmt.Errorf("migration command response unavailable; inspect status before retrying")
	}
	if _, err := output.Write(body); err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("migration command rejected (HTTP %d)", response.StatusCode)
	}
	return nil
}
