package main

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/qiniu/ci-runner/internal/config"
	"github.com/qiniu/ci-runner/internal/state"
	"gopkg.in/yaml.v3"
)

func TestRecoveryGateAllowsOnlyHealthUntilReady(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	gate := &recoveryGate{next: next}

	health := httptest.NewRecorder()
	gate.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, want %d", health.Code, http.StatusOK)
	}

	blocked := httptest.NewRecorder()
	gate.ServeHTTP(blocked, httptest.NewRequest(http.MethodPost, "/webhooks/github", nil))
	if blocked.Code != http.StatusServiceUnavailable || blocked.Header().Get("Retry-After") != "1" {
		t.Fatalf("recovery response = %d Retry-After=%q", blocked.Code, blocked.Header().Get("Retry-After"))
	}

	gate.ready.Store(true)
	ready := httptest.NewRecorder()
	gate.ServeHTTP(ready, httptest.NewRequest(http.MethodPost, "/webhooks/github", nil))
	if ready.Code != http.StatusNoContent {
		t.Fatalf("ready status = %d, want %d", ready.Code, http.StatusNoContent)
	}
}

type startupStoreStub struct {
	ensureErr error
	calls     []string
}

func (s *startupStoreStub) Ensure() error {
	s.calls = append(s.calls, "ensure")
	return s.ensureErr
}

func TestInitializeStateStoreOnlyEnsuresDatabase(t *testing.T) {
	store := &startupStoreStub{}
	if err := initializeStateStore(store, slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(store.calls, []string{"ensure"}) {
		t.Fatalf("startup must not recreate profiles: %#v", store.calls)
	}
	store.ensureErr = errors.New("open database")
	if err := initializeStateStore(store, slog.Default()); err == nil {
		t.Fatal("missing Ensure error")
	}
}

func TestWriteObfuscatedConfigValueReadsSecretFromStdin(t *testing.T) {
	const plaintext = "secret-from-stdin"
	var output bytes.Buffer
	if err := writeObfuscatedConfigValue(strings.NewReader(plaintext+"\n"), &output); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), plaintext) {
		t.Fatalf("output exposed plaintext: %q", output.String())
	}
	var decoded struct {
		Value config.Secret `yaml:"value"`
	}
	if err := yaml.Unmarshal([]byte("value: "+strings.TrimSpace(output.String())+"\n"), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Value.Value() != plaintext {
		t.Fatalf("decoded value = %q", decoded.Value.Value())
	}
}

func TestWriteObfuscatedConfigValueTrimsTrailingLineEndings(t *testing.T) {
	const plaintext = "secret-from-stdin"
	var output bytes.Buffer
	if err := writeObfuscatedConfigValue(strings.NewReader(plaintext+"\r\n\r\n"), &output); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Value config.Secret `yaml:"value"`
	}
	if err := yaml.Unmarshal([]byte("value: "+strings.TrimSpace(output.String())+"\n"), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Value.Value() != plaintext {
		t.Fatalf("decoded value = %q", decoded.Value.Value())
	}
}

func TestRunObfuscateConfigValueWritesErrorsToStderr(t *testing.T) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if runObfuscateConfigValue(strings.NewReader(""), &stdout, &stderr) {
		t.Fatal("expected obfuscation to fail")
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
	if !strings.Contains(stderr.String(), "secret input is empty") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestBootstrapAdminAccount(t *testing.T) {
	store := state.New(t.TempDir())
	if err := bootstrapAdminAccount(store, "github:12345"); err != nil {
		t.Fatal(err)
	}
	account, _, err := store.GetAccountByOAuthIdentity("github", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if account.Role != "admin" {
		t.Fatalf("expected admin role, got %#v", account)
	}
}

func TestBootstrapAdminAccountDefaultsToGitHub(t *testing.T) {
	store := state.New(t.TempDir())
	if err := bootstrapAdminAccount(store, "12345"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetAccountByOAuthIdentity("github", "12345"); err != nil {
		t.Fatal(err)
	}
}
