package deployconfig

import (
	"errors"
	"strings"
	"testing"

	deploy "github.com/miclle/deployd"
)

func TestParseStrictConfiguration(t *testing.T) {
	data := []byte(`version: 1
working_directory: web
install_command: npm ci
start_command: npm run start -- --host 0.0.0.0 --port 3000
port: 3000
healthcheck:
  path: /health
  timeout_seconds: 45
`)

	spec, err := Parse(data)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	want := deploy.Spec{
		WorkingDirectory: "web",
		InstallCommand:   "npm ci",
		StartCommand:     "npm run start -- --host 0.0.0.0 --port 3000",
		Port:             3000,
		Healthcheck:      deploy.Healthcheck{Path: "/health", TimeoutSeconds: 45},
	}
	if spec != want {
		t.Fatalf("Parse() = %#v, want %#v", spec, want)
	}
}

func TestParseRejectsUnknownFieldsAndMultipleDocuments(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{name: "unknown field", data: "version: 1\ninstall_command: make\nstart_command: ./server\nport: 8080\nextra: true\n"},
		{name: "duplicate field", data: "version: 1\ninstall_command: make\ninstall_command: make\nstart_command: ./server\nport: 8080\n"},
		{name: "multiple documents", data: "version: 1\ninstall_command: make\nstart_command: ./server\nport: 8080\n---\nversion: 1\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.data))
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("Parse() error = %v, want ErrInvalidConfig", err)
			}
		})
	}
}

func TestParseRejectsUnsupportedVersionAndInvalidExecutionParameters(t *testing.T) {
	tests := []string{
		"version: 2\ninstall_command: make\nstart_command: ./server\nport: 8080\n",
		"version: 1\ninstall_command: make\nstart_command: ./server\nport: 8080\nworking_directory: ../outside\n",
		"version: 1\ninstall_command: make\nstart_command: ./server\nport: 8080\nhealthcheck:\n  path: https://example.com\n",
	}
	for _, data := range tests {
		_, err := Parse([]byte(data))
		if !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("Parse(%q) error = %v, want ErrInvalidConfig", data, err)
		}
	}
}

func TestParseRejectsEmptyAndOversizedDocuments(t *testing.T) {
	if _, err := Parse(nil); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Parse(empty) error = %v, want ErrInvalidConfig", err)
	}
	if _, err := Parse([]byte(strings.Repeat("x", MaxBytes+1))); !errors.Is(err, ErrConfigTooLarge) {
		t.Fatalf("Parse(oversized) error = %v, want ErrConfigTooLarge", err)
	}
}

func TestParseAppliesExecutionDefaults(t *testing.T) {
	spec, err := Parse([]byte("version: 1\ninstall_command: make\nstart_command: ./server\nport: 8080\n"))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if spec.WorkingDirectory != "." || spec.Healthcheck.Path != "/" || spec.Healthcheck.TimeoutSeconds != 60 {
		t.Fatalf("Parse() defaults = %#v", spec)
	}
}

func TestParseExistingCamelCaseRepositoryConfiguration(t *testing.T) {
	spec, err := Parse([]byte("version: 1\nworkingDirectory: .\ninstallCommand: npm ci && npm run build\nstartCommand: npm run start\nport: 4173\nhealthcheck:\n  path: /\n  timeoutSeconds: 60\n"))
	if err != nil || spec.Port != 4173 || spec.InstallCommand != "npm ci && npm run build" || spec.Healthcheck.TimeoutSeconds != 60 {
		t.Fatalf("existing repository contract=%#v %v", spec, err)
	}
	for _, data := range []string{
		"version: 1\nstartCommand: true\nstart_command: true\nport: 4173\n",
		"version: 1\nstartCommand: true\nport: 4173\nhealthcheck:\n  timeoutSeconds: 60\n  timeout_seconds: 60\n",
	} {
		if _, err := Parse([]byte(data)); err == nil {
			t.Fatal("ambiguous field aliases accepted")
		}
	}
}
