package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	HTTPAddr                  string
	HTTPReadTimeout           time.Duration
	HTTPWriteTimeout          time.Duration
	HTTPIdleTimeout           time.Duration
	StateDir                  string
	StateBackend              string
	StateDatabaseDSN          Secret
	OnlineMigration           OnlineMigrationConfig
	GitHubAppID               int64
	GitHubAppInstallationID   int64
	GitHubAppSlug             string
	GitHubAppPrivateKeyFile   string
	GitHubToken               Secret
	GitHubBasicAuthUsername   string
	GitHubBasicAuthPassword   Secret
	GitHubWebhookSecret       Secret
	GitHubAllowedRepositories []string
	AuthSessionSecret         Secret
	AuthEncryptionKey         Secret
	AuthSessionTTL            time.Duration
	GitHubOAuthClientID       string
	GitHubOAuthClientSecret   Secret
	GitHubOAuthRedirectURL    string
	SandboxTimeout            time.Duration
	SandboxCreateTimeout      time.Duration
	SandboxStopTimeout        time.Duration
	RecoveryTimeout           time.Duration
	WorkerLeaseTTL            time.Duration
	RunnerIdleTimeout         time.Duration
	RetryBaseDelay            time.Duration
	RetryMaxDelay             time.Duration
	RetryMaxAttempts          int
	MaxConcurrentRunners      int
	GitHubAPIBaseURL          string
	ConfigPath                string
	SandboxRegions            []SandboxRegionConfig
	CacheSTSEndpoint          string
	DeploymentTemplateID      string
	DeploymentWorkRoot        string
	DeploymentMaxConcurrent   int
	DeploymentRetention       time.Duration
}

type OnlineMigrationConfig struct {
	Directory               string `yaml:"directory"`
	PostgresDSN             Secret `yaml:"postgres_dsn"`
	Initialize              bool   `yaml:"initialize"`
	PauseBudgetMilliseconds int    `yaml:"pause_budget_milliseconds"`
}

type SandboxRegionConfig struct {
	ID            string `yaml:"id"`
	Label         string `yaml:"label"`
	SandboxAPIURL string `yaml:"sandbox_api_url"`
	S3Region      string `yaml:"s3_region"`
	S3Endpoint    string `yaml:"s3_endpoint"`
}

type fileConfig struct {
	Server struct {
		HTTPAddr        string `yaml:"http_addr"`
		ReadTimeoutSec  int    `yaml:"read_timeout_seconds"`
		WriteTimeoutSec int    `yaml:"write_timeout_seconds"`
		IdleTimeoutSec  int    `yaml:"idle_timeout_seconds"`
	} `yaml:"server"`
	Database struct {
		Backend         string                `yaml:"backend"`
		DSN             Secret                `yaml:"dsn"`
		LegacyURL       Secret                `yaml:"url"`
		OnlineMigration OnlineMigrationConfig `yaml:"online_migration"`
	} `yaml:"database"`
	Auth struct {
		EncryptionKey   Secret `yaml:"encryption_key"`
		SessionSecret   Secret `yaml:"session_secret"`
		SessionTTLHours int    `yaml:"session_ttl_hours"`
	} `yaml:"auth"`
	Sandbox struct {
		TimeoutSec       int                   `yaml:"timeout_seconds"`
		CreateTimeoutSec int                   `yaml:"create_timeout_seconds"`
		StopTimeoutSec   int                   `yaml:"stop_timeout_seconds"`
		Regions          []SandboxRegionConfig `yaml:"regions"`
	} `yaml:"sandbox"`
	GitHub struct {
		WebhookSecret       Secret   `yaml:"webhook_secret"`
		APIBaseURL          string   `yaml:"api_base_url"`
		AllowedRepositories []string `yaml:"allowed_repositories"`
		Owner               string   `yaml:"owner"`
		Repo                string   `yaml:"repo"`
		Token               Secret   `yaml:"token"`
		BasicAuth           struct {
			Username string `yaml:"username"`
			Password Secret `yaml:"password"`
		} `yaml:"basic_auth"`
		App struct {
			ID             int64  `yaml:"id"`
			InstallationID int64  `yaml:"installation_id"`
			Slug           string `yaml:"slug"`
			PrivateKeyFile string `yaml:"private_key_file"`
		} `yaml:"app"`
		OAuth struct {
			ClientID     string `yaml:"client_id"`
			ClientSecret Secret `yaml:"client_secret"`
			RedirectURL  string `yaml:"redirect_url"`
		} `yaml:"oauth"`
	} `yaml:"github"`
	Worker struct {
		MaxConcurrentRunners int `yaml:"max_concurrent_runners"`
		RunnerIdleTimeoutSec int `yaml:"runner_idle_timeout_seconds"`
		RecoveryTimeoutSec   int `yaml:"recovery_timeout_seconds"`
		LeaseTTLSec          int `yaml:"lease_ttl_seconds"`
		RetryBaseDelaySec    int `yaml:"retry_base_delay_seconds"`
		RetryMaxDelaySec     int `yaml:"retry_max_delay_seconds"`
		RetryMaxAttempts     int `yaml:"retry_max_attempts"`
	} `yaml:"worker"`
	Cache struct {
		STSEndpoint string `yaml:"sts_endpoint"`
	} `yaml:"cache"`
	Deploy struct {
		TemplateID            string `yaml:"template_id"`
		WorkRoot              string `yaml:"work_root"`
		MaxConcurrentPerScope int    `yaml:"max_concurrent_per_scope"`
		RetentionHours        int    `yaml:"retention_hours"`
	} `yaml:"deploy"`
}

const (
	defaultGitHubAPIBaseURL = "https://api.github.com"
	defaultCacheSTSEndpoint = "https://sts-ov.qiniuapi.com"
)

func validateSandboxRegions(regions []SandboxRegionConfig) error {
	if len(regions) == 0 {
		return fmt.Errorf("sandbox.regions requires at least one region")
	}
	seen := make(map[string]struct{}, len(regions))
	for i, region := range regions {
		id := strings.TrimSpace(region.ID)
		label := strings.TrimSpace(region.Label)
		apiURL := strings.TrimSpace(region.SandboxAPIURL)
		s3Region := strings.TrimSpace(region.S3Region)
		s3Endpoint := strings.TrimSpace(region.S3Endpoint)
		if id == "" || label == "" || apiURL == "" {
			return fmt.Errorf("sandbox.regions[%d] requires id, label, and sandbox_api_url", i)
		}
		if s3Region == "" && s3Endpoint != "" || s3Region != "" && s3Endpoint == "" {
			return fmt.Errorf("sandbox.regions[%d] must set both s3_region and s3_endpoint", i)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("sandbox.regions[%d] duplicates id %q", i, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func Load(path string) (Config, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		path = "runnerd.yaml"
	}
	return LoadFile(path)
}

func LoadFile(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var raw fileConfig
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parse config %q: %w", path, err)
	}
	if strings.TrimSpace(raw.GitHub.Owner) != "" || strings.TrimSpace(raw.GitHub.Repo) != "" {
		return Config{}, fmt.Errorf("invalid config github.owner/github.repo: no longer supported; pass repository_full_name on manual runner requests")
	}
	configDir := filepath.Dir(path)
	cfg := Config{
		HTTPAddr:                  defaultString(raw.Server.HTTPAddr, ":25500"),
		HTTPReadTimeout:           durationSeconds(raw.Server.ReadTimeoutSec, 15),
		HTTPWriteTimeout:          durationSeconds(raw.Server.WriteTimeoutSec, 60),
		HTTPIdleTimeout:           durationSeconds(raw.Server.IdleTimeoutSec, 120),
		StateBackend:              strings.ToLower(defaultString(raw.Database.Backend, "sqlite")),
		StateDatabaseDSN:          defaultSecret(raw.Database.DSN, raw.Database.LegacyURL),
		OnlineMigration:           raw.Database.OnlineMigration,
		GitHubAppID:               raw.GitHub.App.ID,
		GitHubAppInstallationID:   raw.GitHub.App.InstallationID,
		GitHubAppSlug:             strings.TrimSpace(raw.GitHub.App.Slug),
		GitHubAppPrivateKeyFile:   resolveConfigPath(configDir, raw.GitHub.App.PrivateKeyFile),
		GitHubToken:               raw.GitHub.Token,
		GitHubBasicAuthUsername:   raw.GitHub.BasicAuth.Username,
		GitHubBasicAuthPassword:   raw.GitHub.BasicAuth.Password,
		GitHubWebhookSecret:       raw.GitHub.WebhookSecret,
		GitHubAllowedRepositories: normalizePatterns(raw.GitHub.AllowedRepositories),
		AuthEncryptionKey:         Secret(strings.TrimSpace(raw.Auth.EncryptionKey.Value())),
		AuthSessionSecret:         Secret(strings.TrimSpace(raw.Auth.SessionSecret.Value())),
		AuthSessionTTL:            durationHours(raw.Auth.SessionTTLHours, 12),
		GitHubOAuthClientID:       strings.TrimSpace(raw.GitHub.OAuth.ClientID),
		GitHubOAuthClientSecret:   Secret(strings.TrimSpace(raw.GitHub.OAuth.ClientSecret.Value())),
		GitHubOAuthRedirectURL:    strings.TrimSpace(raw.GitHub.OAuth.RedirectURL),
		SandboxTimeout:            durationSeconds(raw.Sandbox.TimeoutSec, 3600),
		SandboxCreateTimeout:      durationSeconds(raw.Sandbox.CreateTimeoutSec, 120),
		SandboxStopTimeout:        durationSeconds(raw.Sandbox.StopTimeoutSec, 30),
		RecoveryTimeout:           durationSeconds(raw.Worker.RecoveryTimeoutSec, 120),
		WorkerLeaseTTL:            durationSeconds(raw.Worker.LeaseTTLSec, 300),
		RunnerIdleTimeout:         durationSeconds(raw.Worker.RunnerIdleTimeoutSec, 300),
		RetryBaseDelay:            durationSeconds(raw.Worker.RetryBaseDelaySec, 15),
		RetryMaxDelay:             durationSeconds(raw.Worker.RetryMaxDelaySec, 300),
		RetryMaxAttempts:          defaultInt(raw.Worker.RetryMaxAttempts, 5),
		MaxConcurrentRunners:      defaultInt(raw.Worker.MaxConcurrentRunners, 100),
		GitHubAPIBaseURL:          defaultString(raw.GitHub.APIBaseURL, defaultGitHubAPIBaseURL),
		ConfigPath:                path,
		SandboxRegions:            raw.Sandbox.Regions,
		CacheSTSEndpoint:          defaultString(raw.Cache.STSEndpoint, defaultCacheSTSEndpoint),
		DeploymentTemplateID:      strings.TrimSpace(raw.Deploy.TemplateID),
		DeploymentWorkRoot:        strings.TrimSpace(raw.Deploy.WorkRoot),
		DeploymentMaxConcurrent:   defaultInt(raw.Deploy.MaxConcurrentPerScope, 3),
		DeploymentRetention:       durationHours(raw.Deploy.RetentionHours, 168),
	}
	if cfg.StateBackend == "sqlite" {
		if strings.TrimSpace(cfg.StateDatabaseDSN.Value()) == "" {
			cfg.StateDatabaseDSN = Secret(filepath.Join(configDir, "var", "runnerd.db"))
		} else {
			cfg.StateDatabaseDSN = Secret(resolveConfigPath(configDir, cfg.StateDatabaseDSN.Value()))
		}
		cfg.StateDir = filepath.Dir(cfg.StateDatabaseDSN.Value())
	}
	if cfg.OnlineMigration.Directory != "" {
		cfg.OnlineMigration.Directory = resolveConfigPath(configDir, cfg.OnlineMigration.Directory)
	}
	if strings.TrimSpace(cfg.DeploymentWorkRoot) == "" {
		baseDir := cfg.StateDir
		if strings.TrimSpace(baseDir) == "" {
			baseDir = filepath.Join(configDir, "var")
		}
		cfg.DeploymentWorkRoot = filepath.Join(baseDir, "deployments")
		if absolute, err := filepath.Abs(cfg.DeploymentWorkRoot); err == nil {
			cfg.DeploymentWorkRoot = absolute
		}
	} else {
		cfg.DeploymentWorkRoot = resolveConfigPath(configDir, cfg.DeploymentWorkRoot)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	if err := validateSandboxRegions(cfg.SandboxRegions); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) GitHubAuthMode() string {
	if strings.TrimSpace(c.GitHubToken.Value()) != "" {
		return "token"
	}
	if strings.TrimSpace(c.GitHubBasicAuthUsername) != "" || strings.TrimSpace(c.GitHubBasicAuthPassword.Value()) != "" {
		return "basic"
	}
	if c.GitHubAppID == 0 && c.GitHubAppInstallationID == 0 && strings.TrimSpace(c.GitHubAppPrivateKeyFile) == "" {
		return "none"
	}
	return "app"
}

func (c Config) validate() error {
	var missing []string
	for path, value := range map[string]string{
		"github.webhook_secret": c.GitHubWebhookSecret.Value(),
	} {
		if strings.TrimSpace(value) == "" {
			missing = append(missing, path)
		}
	}
	authModes := 0
	if c.GitHubAppID != 0 || c.GitHubAppInstallationID != 0 || strings.TrimSpace(c.GitHubAppPrivateKeyFile) != "" {
		authModes++
		if c.GitHubAppID == 0 {
			missing = append(missing, "github.app.id")
		}
		if strings.TrimSpace(c.GitHubAppPrivateKeyFile) == "" {
			missing = append(missing, "github.app.private_key_file")
		}
	}
	if strings.TrimSpace(c.GitHubToken.Value()) != "" {
		authModes++
	}
	if strings.TrimSpace(c.GitHubBasicAuthUsername) != "" || strings.TrimSpace(c.GitHubBasicAuthPassword.Value()) != "" {
		authModes++
		if strings.TrimSpace(c.GitHubBasicAuthUsername) == "" {
			missing = append(missing, "github.basic_auth.username")
		}
		if strings.TrimSpace(c.GitHubBasicAuthPassword.Value()) == "" {
			missing = append(missing, "github.basic_auth.password")
		}
	}
	if authModes == 0 {
		missing = append(missing, "github.app or github.token or github.basic_auth")
	}
	if authModes > 1 {
		return fmt.Errorf("invalid config github auth: configure exactly one of app, token, or basic_auth")
	}
	if c.StateBackend != "sqlite" && c.StateBackend != "postgres" && c.StateBackend != "mysql" {
		return fmt.Errorf("invalid config database.backend: must be sqlite, postgres, or mysql")
	}
	migration := c.OnlineMigration
	if migration.Directory != "" || migration.PostgresDSN.Value() != "" || migration.Initialize || migration.PauseBudgetMilliseconds != 0 {
		if c.StateBackend != "sqlite" || migration.Directory == "" || strings.TrimSpace(migration.PostgresDSN.Value()) == "" {
			return fmt.Errorf("invalid config database.online_migration: requires sqlite backend, directory and postgres_dsn")
		}
		if migration.PauseBudgetMilliseconds != 0 && (migration.PauseBudgetMilliseconds < 50 || migration.PauseBudgetMilliseconds > 5000) {
			return fmt.Errorf("invalid config database.online_migration.pause_budget_milliseconds: must be 50..5000")
		}
	}
	if strings.TrimRight(c.GitHubAPIBaseURL, "/") != defaultGitHubAPIBaseURL {
		return fmt.Errorf("invalid config github.api_base_url: only %s is supported", defaultGitHubAPIBaseURL)
	}
	if strings.TrimSpace(c.StateDatabaseDSN.Value()) == "" {
		missing = append(missing, "database.dsn")
	}
	if strings.TrimSpace(c.AuthEncryptionKey.Value()) == "" {
		missing = append(missing, "auth.encryption_key")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
	}
	var oauthMissing []string
	if strings.TrimSpace(c.GitHubOAuthClientID) == "" {
		oauthMissing = append(oauthMissing, "github.oauth.client_id")
	}
	if strings.TrimSpace(c.GitHubOAuthClientSecret.Value()) == "" {
		oauthMissing = append(oauthMissing, "github.oauth.client_secret")
	}
	if strings.TrimSpace(c.AuthSessionSecret.Value()) == "" {
		oauthMissing = append(oauthMissing, "auth.session_secret")
	}
	if len(oauthMissing) > 0 {
		return fmt.Errorf("missing required config: %s", strings.Join(oauthMissing, ", "))
	}
	if c.MaxConcurrentRunners < 1 {
		return fmt.Errorf("invalid config worker.max_concurrent_runners: must be >= 1")
	}
	if c.RetryMaxAttempts < 1 {
		return fmt.Errorf("invalid config worker.retry_max_attempts: must be >= 1")
	}
	if len(c.DeploymentTemplateID) > 256 || strings.ContainsAny(c.DeploymentTemplateID, "\r\n") {
		return fmt.Errorf("invalid config deploy.template_id")
	}
	if strings.TrimSpace(c.DeploymentWorkRoot) == "" || !filepath.IsAbs(c.DeploymentWorkRoot) {
		return fmt.Errorf("invalid config deploy.work_root")
	}
	if c.DeploymentMaxConcurrent < 1 || c.DeploymentMaxConcurrent > 1000 {
		return fmt.Errorf("invalid config deploy.max_concurrent_per_scope: must be between 1 and 1000")
	}
	if c.DeploymentRetention < 24*time.Hour || c.DeploymentRetention > 365*24*time.Hour {
		return fmt.Errorf("invalid config deploy.retention_hours: must be between 24 and 8760")
	}
	return nil
}

func (c Config) GitHubOAuthEnabled() bool {
	return strings.TrimSpace(c.GitHubOAuthClientID) != "" &&
		strings.TrimSpace(c.GitHubOAuthClientSecret.Value()) != "" &&
		strings.TrimSpace(c.AuthSessionSecret.Value()) != ""
}

func (c Config) RepositoryAllowed(repository string) bool {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return false
	}
	if len(c.GitHubAllowedRepositories) == 0 {
		return true
	}
	for _, pattern := range c.GitHubAllowedRepositories {
		if repositoryMatches(pattern, repository) {
			return true
		}
	}
	return false
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func defaultSecret(value, fallback Secret) Secret {
	if strings.TrimSpace(value.Value()) == "" {
		return fallback
	}
	return value
}

func defaultInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func durationSeconds(value, fallback int) time.Duration {
	if value <= 0 {
		value = fallback
	}
	return time.Duration(value) * time.Second
}

func durationHours(value, fallback int) time.Duration {
	if value <= 0 {
		value = fallback
	}
	return time.Duration(value) * time.Hour
}

func normalizePatterns(values []string) []string {
	seen := map[string]bool{}
	var patterns []string
	for _, value := range values {
		pattern := strings.TrimSpace(value)
		if pattern == "" || seen[pattern] {
			continue
		}
		seen[pattern] = true
		patterns = append(patterns, pattern)
	}
	return patterns
}

func repositoryMatches(pattern, repository string) bool {
	pattern = strings.TrimSpace(pattern)
	repository = strings.TrimSpace(repository)
	if pattern == "" || repository == "" {
		return false
	}
	if pattern == repository {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		return strings.HasPrefix(repository, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

func resolveConfigPath(configDir, value string) string {
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(configDir, value)
}
