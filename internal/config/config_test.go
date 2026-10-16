package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadFileUsesRunnerdYAML(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
server:
  http_addr: ":28000"
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
admin:
  token: admin-token
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  allowed_repositories:
    - octo/*
  app:
    id: 123
    installation_id: 456
    private_key_file: ./secrets/app.pem
  oauth:
    client_id: Iv1.test
    client_secret: secret
worker:
  max_concurrent_runners: 5
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":28000" {
		t.Fatalf("unexpected HTTP address: %s", cfg.HTTPAddr)
	}
	if cfg.StateBackend != "sqlite" {
		t.Fatalf("unexpected state backend: %s", cfg.StateBackend)
	}
	if cfg.StateDatabaseDSN.Value() != filepath.Join(dir, "runnerd.db") {
		t.Fatalf("unexpected database dsn: %s", cfg.StateDatabaseDSN)
	}
	if cfg.GitHubAuthMode() != "app" {
		t.Fatalf("unexpected auth mode: %s", cfg.GitHubAuthMode())
	}
	if cfg.GitHubAPIBaseURL != defaultGitHubAPIBaseURL {
		t.Fatalf("unexpected GitHub API base URL: %s", cfg.GitHubAPIBaseURL)
	}
	if cfg.GitHubAppPrivateKeyFile != filepath.Join(dir, "secrets", "app.pem") {
		t.Fatalf("unexpected private key path: %s", cfg.GitHubAppPrivateKeyFile)
	}
	if cfg.AuthSessionSecret != "test-session-secret" || cfg.AuthEncryptionKey != "test-encryption-key" {
		t.Fatalf("unexpected auth secrets: session=%q encryption=%q", cfg.AuthSessionSecret, cfg.AuthEncryptionKey)
	}
	if !cfg.RepositoryAllowed("octo/repo") || cfg.RepositoryAllowed("other/repo") {
		t.Fatalf("unexpected repository allowlist behavior: %#v", cfg.GitHubAllowedRepositories)
	}
	if cfg.DeploymentMaxConcurrent != 3 || cfg.DeploymentRetention.Hours() != 168 {
		t.Fatalf("unexpected deployment limits: max=%d retention=%s", cfg.DeploymentMaxConcurrent, cfg.DeploymentRetention)
	}
}

func TestLoadFileSupportsDynamicGitHubAppInstallation(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
admin:
  token: admin-token
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  app:
    id: 123
    private_key_file: ./secrets/app.pem
  oauth:
    client_id: Iv1.test
    client_secret: secret
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubAuthMode() != "app" || cfg.GitHubAppInstallationID != 0 {
		t.Fatalf("unexpected app config: mode=%s installation_id=%d", cfg.GitHubAuthMode(), cfg.GitHubAppInstallationID)
	}
	if !cfg.RepositoryAllowed("any/repo") {
		t.Fatal("empty allowed_repositories should allow all repositories")
	}
}

func TestLoadFileSupportsGitHubOAuthLogin(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
admin:
  token: admin-token
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  app:
    id: 123
    private_key_file: ./secrets/app.pem
  oauth:
    client_id: Iv1.test
    client_secret: secret
    redirect_url: https://runner.example.com/auth/github/callback
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.GitHubOAuthEnabled() {
		t.Fatal("expected github oauth to be enabled")
	}
	if cfg.GitHubOAuthRedirectURL != "https://runner.example.com/auth/github/callback" {
		t.Fatalf("unexpected redirect url: %s", cfg.GitHubOAuthRedirectURL)
	}
}

func TestLoadFileRejectsPartialGitHubOAuthLogin(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
admin:
  token: admin-token
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  app:
    id: 123
    private_key_file: ./secrets/app.pem
  oauth:
    client_id: Iv1.test
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFile(configPath)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "github.oauth.client_secret") {
		t.Fatalf("expected oauth missing error, got %v", err)
	}
}

func TestLoadFileRejectsMissingGitHubOAuthLogin(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  app:
    id: 123
    private_key_file: ./secrets/app.pem
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFile(configPath)
	if err == nil {
		t.Fatal("expected validation error")
	}
	for _, want := range []string{"github.oauth.client_id", "github.oauth.client_secret"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected oauth missing error to mention %s, got %v", want, err)
		}
	}
}

func TestLoadFileRejectsMissingEncryptionKey(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  token: ghp_test
  oauth:
    client_id: Iv1.test
    client_secret: secret
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFile(configPath)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "auth.encryption_key") {
		t.Fatalf("expected missing encryption key error, got %v", err)
	}
}

func TestLoadFileRejectsDeprecatedGitHubDefaultRepository(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
admin:
  token: admin-token
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  owner: o
  repo: r
  app:
    id: 123
    private_key_file: ./secrets/app.pem
  oauth:
    client_id: Iv1.test
    client_secret: secret
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFile(configPath)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "github.owner/github.repo") {
		t.Fatalf("expected deprecated owner/repo error, got %v", err)
	}
}

func TestLoadFileAllowsEmptyDeprecatedGitHubDefaultRepository(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
admin:
  token: admin-token
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  owner: ""
  repo: ""
  app:
    id: 123
    private_key_file: ./secrets/app.pem
  oauth:
    client_id: Iv1.test
    client_secret: secret
`), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadFile(configPath); err != nil {
		t.Fatal(err)
	}
}

func TestLoadFileRejectsUnsupportedGitHubAPIBaseURL(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
admin:
  token: admin-token
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  api_base_url: https://github.example/api/v3
  app:
    id: 123
    installation_id: 456
    private_key_file: ./secrets/app.pem
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFile(configPath)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "github.api_base_url") {
		t.Fatalf("expected error to mention github.api_base_url, got %v", err)
	}
}

func TestLoadFileSupportsGitHubTokenAuth(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
admin:
  token: admin-token
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  token: ghp_test
  oauth:
    client_id: Iv1.test
    client_secret: secret
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubAuthMode() != "token" || cfg.GitHubToken != "ghp_test" {
		t.Fatalf("unexpected token auth config: mode=%s token=%q", cfg.GitHubAuthMode(), cfg.GitHubToken)
	}
}

func TestLoadFileSupportsGitHubBasicAuth(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
admin:
  token: admin-token
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  basic_auth:
    username: octo
    password: secret
  oauth:
    client_id: Iv1.test
    client_secret: secret
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubAuthMode() != "basic" || cfg.GitHubBasicAuthUsername != "octo" || cfg.GitHubBasicAuthPassword != "secret" {
		t.Fatalf("unexpected basic auth config: %#v", cfg)
	}
}

func TestLoadFileSupportsMySQLDatabaseBackend(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	const mysqlDSN = "runner:secret@tcp(mysql.example:3306)/runnerd?parseTime=true"
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: mysql
  dsn: `+mysqlDSN+`
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  token: ghp_test
  oauth:
    client_id: Iv1.test
    client_secret: secret
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StateBackend != "mysql" {
		t.Fatalf("unexpected state backend: %s", cfg.StateBackend)
	}
	if cfg.StateDatabaseDSN != mysqlDSN {
		t.Fatalf("unexpected database dsn: %s", cfg.StateDatabaseDSN)
	}
	if cfg.StateDir != "" {
		t.Fatalf("mysql should not set sqlite state dir, got %q", cfg.StateDir)
	}
}

func TestLoadFileSupportsLegacyDatabaseURLAlias(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  url: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  token: ghp_test
  oauth:
    client_id: Iv1.test
    client_secret: secret
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(dir, "runnerd.db")
	if cfg.StateDatabaseDSN.Value() != want {
		t.Fatalf("unexpected database dsn: %s, want %s", cfg.StateDatabaseDSN, want)
	}
}

func TestGitHubAuthModeReturnsNoneWithoutAuthConfig(t *testing.T) {
	if mode := (Config{}).GitHubAuthMode(); mode != "none" {
		t.Fatalf("unexpected auth mode: %s", mode)
	}
}

func TestLoadFileRejectsMissingGitHubAuth(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: `+filepath.Join(dir, "runnerd.db")+`
admin:
  token: admin-token
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFile(configPath)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "github.app or github.token or github.basic_auth") {
		t.Fatalf("expected missing auth error, got %v", err)
	}
}

func TestLoadFileRejectsMultipleGitHubAuthModes(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "runnerd.yaml")
	if err := os.WriteFile(configPath, []byte(`
database:
  backend: sqlite
  dsn: ./runnerd.db
auth:
  session_secret: test-session-secret
  encryption_key: test-encryption-key
admin:
  token: admin-token
sandbox:
  regions:
    - id: test-region
      label: Test Region
      sandbox_api_url: https://sandbox.example.test

github:
  webhook_secret: webhook-secret
  token: ghp_test
  app:
    id: 123
    installation_id: 456
    private_key_file: ./secrets/app.pem
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadFile(configPath)
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("expected multiple auth error, got %v", err)
	}
}
