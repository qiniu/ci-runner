package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	_ "github.com/jimmicro/pprof"
	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/github"
	"github.com/qiniu/ci-runner/internal/redact"
	"github.com/qiniu/ci-runner/internal/server"
	"github.com/qiniu/ci-runner/internal/state"
)

type startupStateStore interface {
	Ensure() error
}

func main() {
	configPath := flag.String("config", "runnerd.yaml", "path to runnerd config file")
	bootstrapAdmin := flag.String("bootstrap-admin", "", "set an admin account as provider:subject (for example github:12345678) and exit without starting the server")
	obfuscateConfigValue := flag.Bool("obfuscate-config-value", false, "read a secret from stdin and print a RUNNERD_ENC value")
	flag.Parse()
	if *obfuscateConfigValue {
		if !runObfuscateConfigValue(os.Stdin, os.Stdout, os.Stderr) {
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("load config", "error", err)
		os.Exit(1)
	}
	store := state.NewWithOptions(state.Options{
		Backend:        cfg.StateBackend,
		DatabaseDSN:    cfg.StateDatabaseDSN.Value(),
		MigrateOnStart: true,
	})
	if err := initializeStateStore(store, logger); err != nil {
		logger.Error("initialize state store", "error", err)
		os.Exit(1)
	}
	if *bootstrapAdmin != "" {
		if err := bootstrapAdminAccount(store, *bootstrapAdmin); err != nil {
			logger.Error("bootstrap admin account", "error", err)
			os.Exit(1)
		}
		logger.Info("bootstrap admin account done, exiting", "identity", *bootstrapAdmin)
		return
	}
	githubHTTPClient := &http.Client{Timeout: 30 * time.Second}
	var gh *github.Client
	switch cfg.GitHubAuthMode() {
	case "app":
		gh, err = github.NewAppClient(cfg.GitHubAPIBaseURL, github.AppAuth{
			AppID:          cfg.GitHubAppID,
			InstallationID: cfg.GitHubAppInstallationID,
			PrivateKeyFile: cfg.GitHubAppPrivateKeyFile,
		}, githubHTTPClient)
		if err != nil {
			logger.Error("create github app client", "error", err)
			os.Exit(1)
		}
	case "token":
		gh = github.NewTokenClient(cfg.GitHubAPIBaseURL, cfg.GitHubToken.Value(), githubHTTPClient)
	case "basic":
		gh = github.NewBasicAuthClient(cfg.GitHubAPIBaseURL, cfg.GitHubBasicAuthUsername, cfg.GitHubBasicAuthPassword.Value(), githubHTTPClient)
	default:
		logger.Error("unsupported github auth mode", "mode", cfg.GitHubAuthMode())
		os.Exit(1)
	}
	handler := server.New(cfg, store, gh, nil, logger)
	gate := &recoveryGate{next: handler}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           gate,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       cfg.HTTPIdleTimeout,
		MaxHeaderBytes:    1 << 20,
	}
	recoveryCtx, cancelRecovery := context.WithTimeout(context.Background(), cfg.RecoveryTimeout)
	listener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		cancelRecovery()
		logger.Error("listen server", "addr", cfg.HTTPAddr, "error", err)
		os.Exit(1)
	}
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- srv.Serve(listener)
		// A premature Serve return makes recovery useless; abort it so the
		// startup path can surface the server error without waiting for timeout.
		cancelRecovery()
	}()
	logger.Info("starting server", "addr", cfg.HTTPAddr, "state_backend", cfg.StateBackend, "state_database_dsn", redact.DatabaseDSN(cfg.StateDatabaseDSN.Value()))
	if err := handler.Recover(recoveryCtx); err != nil {
		logger.Error("recover runner state", "error", err)
	}
	cancelRecovery()
	handler.Start()
	gate.ready.Store(true)
	if err := <-serverErr; err != nil && err != http.ErrServerClosed {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

type recoveryGate struct {
	next  http.Handler
	ready atomic.Bool
}

func (g *recoveryGate) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !g.ready.Load() && r.URL.Path != "/healthz" {
		w.Header().Set("Retry-After", "1")
		http.Error(w, "runnerd is recovering", http.StatusServiceUnavailable)
		return
	}
	g.next.ServeHTTP(w, r)
}

func initializeStateStore(store startupStateStore, _ *slog.Logger) error {
	if err := store.Ensure(); err != nil {
		return fmt.Errorf("ensure state store: %w", err)
	}

	return nil
}

func runObfuscateConfigValue(input io.Reader, output, errorOutput io.Writer) bool {
	if err := writeObfuscatedConfigValue(input, output); err != nil {
		_, _ = fmt.Fprintln(errorOutput, "obfuscate config value:", err)
		return false
	}
	return true
}

func writeObfuscatedConfigValue(input io.Reader, output io.Writer) error {
	const maxSecretBytes = 1 << 20
	value, err := io.ReadAll(io.LimitReader(input, maxSecretBytes+1))
	if err != nil {
		return fmt.Errorf("read secret from stdin: %w", err)
	}
	if len(value) > maxSecretBytes {
		return fmt.Errorf("secret input exceeds %d bytes", maxSecretBytes)
	}
	secretBuffer := value
	defer func() {
		for i := range secretBuffer {
			secretBuffer[i] = 0
		}
	}()
	for len(value) > 0 && (value[len(value)-1] == '\r' || value[len(value)-1] == '\n') {
		value = value[:len(value)-1]
	}
	if len(value) == 0 {
		return fmt.Errorf("secret input is empty")
	}
	encoded, err := config.ObfuscateSecret(value)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, encoded)
	return err
}

func bootstrapAdminAccount(store state.Store, identity string) error {
	identity = strings.TrimSpace(identity)
	if identity == "" {
		return nil
	}
	provider, subject, ok := strings.Cut(identity, ":")
	if !ok {
		provider = "github"
		subject = identity
	}
	provider = strings.TrimSpace(provider)
	subject = strings.TrimSpace(subject)
	if provider == "" || subject == "" {
		return fmt.Errorf("expected provider:subject")
	}
	_, _, err := store.UpsertAccountForOAuthIdentity(state.OAuthIdentity{
		OAuthProvider: provider,
		OAuthSubject:  subject,
		OAuthLogin:    subject,
	}, "admin")
	return err
}
