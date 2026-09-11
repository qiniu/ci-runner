package state

import (
	"errors"
	"time"
)

const (
	StatusQueued    = "queued"
	StatusCreating  = "creating"
	StatusRunning   = "running"
	StatusStopping  = "stopping"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)

const (
	BackendSQLite   = "sqlite"
	BackendPostgres = "postgres"
	BackendMySQL    = "mysql"
)

var (
	ErrConflict                            = errors.New("state conflict")
	ErrNotFound                            = errors.New("state record not found")
	ErrRetryNotAllowed                     = errors.New("retry not allowed for current state")
	ErrSandboxServiceDefaultAPIKeyRequired = errors.New("sandbox service default api key is required")
	ErrCacheServiceNotConfigured           = errors.New("cache service not configured")
	ErrAuditEventPersistence               = errors.New("audit event persistence failed")
	ErrRunnerProfileNameConflict           = errors.New("runner profile name conflicts with enabled global profile")
	ErrRunnerProfileLabelsConflict         = errors.New("runner profile labels conflict")
)

type RunnerRequest struct {
	ID                     string    `json:"id"`
	Source                 string    `json:"source"`
	JobID                  int64     `json:"job_id,omitempty"`
	PullRequestNumber      int64     `json:"pull_request_number,omitempty"`
	GitHubInstallationID   int64     `json:"github_installation_id,omitempty"`
	RepositoryFullName     string    `json:"repository_full_name,omitempty"`
	RequestedLabels        []string  `json:"requested_labels,omitempty"`
	Labels                 []string  `json:"labels"`
	ProfileName            string    `json:"runner_spec_name,omitempty"`
	ProfileSource          string    `json:"runner_spec_source,omitempty"`
	ProfileScopeType       string    `json:"runner_spec_scope_type,omitempty"`
	ProfileScopeID         int64     `json:"runner_spec_scope_id,omitempty"`
	RunnerGroup            string    `json:"runner_group,omitempty"`
	RunnerName             string    `json:"runner_name"`
	SandboxAPIURL          string    `json:"-"`
	SandboxAPIKeyEncrypted string    `json:"-"`
	SandboxConfigSource    string    `json:"sandbox_config_source,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
}

type RunnerState struct {
	ID                     string    `json:"id"`
	Status                 string    `json:"status"`
	GitHubInstallationID   int64     `json:"github_installation_id,omitempty"`
	RepositoryFullName     string    `json:"repository_full_name,omitempty"`
	RequestedLabels        []string  `json:"requested_labels,omitempty"`
	ProfileName            string    `json:"runner_spec_name,omitempty"`
	ProfileSource          string    `json:"runner_spec_source,omitempty"`
	ProfileScopeType       string    `json:"runner_spec_scope_type,omitempty"`
	ProfileScopeID         int64     `json:"runner_spec_scope_id,omitempty"`
	RunnerGroup            string    `json:"runner_group,omitempty"`
	RunnerName             string    `json:"runner_name"`
	SandboxID              string    `json:"sandbox_id,omitempty"`
	SandboxAPIURL          string    `json:"-"`
	SandboxAPIKeyEncrypted string    `json:"-"`
	SandboxConfigSource    string    `json:"sandbox_config_source,omitempty"`
	ProcessPID             uint32    `json:"process_pid,omitempty"`
	WorkflowJobID          int64     `json:"workflow_job_id,omitempty"`
	WorkflowRunID          int64     `json:"workflow_run_id,omitempty"`
	WorkflowName           string    `json:"workflow_name,omitempty"`
	WorkflowRunAttempt     int64     `json:"workflow_run_attempt,omitempty"`
	HeadBranch             string    `json:"head_branch,omitempty"`
	HeadSHA                string    `json:"head_sha,omitempty"`
	GitHubJobURL           string    `json:"github_job_url,omitempty"`
	PullRequestNumber      int64     `json:"pull_request_number,omitempty"`
	AssignedJobID          int64     `json:"assigned_job_id,omitempty"`
	AssignedJobName        string    `json:"assigned_job_name,omitempty"`
	Error                  string    `json:"error,omitempty"`
	FailureStage           string    `json:"failure_stage,omitempty"`
	FailureReason          string    `json:"failure_reason,omitempty"`
	LastErrorCode          string    `json:"last_error_code,omitempty"`
	LastErrorMessage       string    `json:"last_error_message,omitempty"`
	LastErrorRetryable     bool      `json:"last_error_retryable,omitempty"`
	RetryCount             int       `json:"retry_count,omitempty"`
	UpdatedAt              time.Time `json:"updated_at"`
	CreatedAt              time.Time `json:"created_at"`
	LastAttemptAt          time.Time `json:"last_attempt_at,omitempty"`
	NextRetryAt            time.Time `json:"next_retry_at,omitempty"`
	CreatingAt             time.Time `json:"creating_at,omitempty"`
	RunningAt              time.Time `json:"running_at,omitempty"`
	StoppingAt             time.Time `json:"stopping_at,omitempty"`
	CompletedAt            time.Time `json:"completed_at,omitempty"`
	FailedAt               time.Time `json:"failed_at,omitempty"`
	LeaseOwner             string    `json:"lease_owner,omitempty"`
	LeaseExpiresAt         time.Time `json:"lease_expires_at,omitempty"`
	Version                int64     `json:"version"`
}

// RunnerEvent is a bounded, read-only lifecycle or process log event attached
// to a runner request. PayloadJSON stays private because diagnostics only need
// the operator-safe event metadata and message.
type RunnerEvent struct {
	ID        int64     `json:"id"`
	EventType string    `json:"event_type"`
	Stage     string    `json:"stage,omitempty"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type RunnerProfile struct {
	Name                string    `json:"name"`
	Labels              []string  `json:"labels"`
	RequiredLabels      []string  `json:"required_labels"`
	TemplateID          string    `json:"template_id"`
	DefaultTemplateName string    `json:"default_template_name,omitempty"`
	RunnerGroup         string    `json:"runner_group,omitempty"`
	MaxConcurrency      int       `json:"max_concurrency"`
	MinIdle             int       `json:"min_idle"`
	Priority            int       `json:"priority"`
	Enabled             bool      `json:"enabled"`
	ManagedBy           string    `json:"managed_by,omitempty"`
	CatalogRevision     int       `json:"catalog_revision,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// ManagedProfileConflict reports a catalog name already owned by another source.
type ManagedProfileConflict struct {
	Name              string
	ExistingManagedBy string
}

type ProfileMatch struct {
	RepositoryFullName string         `json:"repository_full_name"`
	Labels             []string       `json:"labels"`
	Profile            *RunnerProfile `json:"runner_spec,omitempty"`
	Source             string         `json:"runner_spec_source,omitempty"`
	ScopeType          string         `json:"runner_spec_scope_type,omitempty"`
	ScopeID            int64          `json:"runner_spec_scope_id,omitempty"`
	Reason             string         `json:"reason,omitempty"`
}

type RunnerProfileScope struct {
	Type string `json:"scope_type"`
	ID   int64  `json:"scope_id"`
}

type ScopedRunnerProfile struct {
	ScopeType      string    `json:"scope_type"`
	ScopeID        int64     `json:"scope_id"`
	Name           string    `json:"name"`
	WorkflowLabels []string  `json:"workflow_labels"`
	LabelKey       string    `json:"-"`
	TemplateID     string    `json:"template_id,omitempty"`
	RunnerGroup    string    `json:"runner_group,omitempty"`
	MaxConcurrency int       `json:"max_concurrency"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

type EffectiveRunnerProfile struct {
	Source          string        `json:"source"`
	ScopeType       string        `json:"scope_type,omitempty"`
	ScopeID         int64         `json:"scope_id,omitempty"`
	Profile         RunnerProfile `json:"-"`
	WorkflowLabels  []string      `json:"workflow_labels"`
	OverridesGlobal bool          `json:"overrides_global"`
}

type AuditEvent struct {
	ID           int64     `json:"id"`
	Actor        string    `json:"actor"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
	PayloadJSON  string    `json:"payload_json,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

// Account represents a local user account.
type Account struct {
	ID        int64     `json:"id"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// AccountListOptions controls account search, role filtering, and pagination.
type AccountListOptions struct {
	Query  string
	Role   string
	Limit  int
	Offset int
}

// AccountListItem combines a local account with all linked OAuth identities.
type AccountListItem struct {
	Account
	OAuthIdentities []OAuthIdentity `json:"oauth_identities"`
}

// AccountStats summarizes local accounts, roles, and linked OAuth identities.
type AccountStats struct {
	TotalAccounts   int64 `json:"total_accounts"`
	AdminAccounts   int64 `json:"admin_accounts"`
	UserAccounts    int64 `json:"user_accounts"`
	OAuthIdentities int64 `json:"oauth_identities"`
}

// AccountRoleUpdate describes an audited administrator-initiated role change.
type AccountRoleUpdate struct {
	ActorAccountID int64
	AccountID      int64
	Role           string
	AuditActor     string
}

// OAuthIdentity represents an external OAuth identity linked to an account.
type OAuthIdentity struct {
	ID            int64     `json:"id"`
	AccountID     int64     `json:"account_id"`
	OAuthProvider string    `json:"oauth_provider"`
	OAuthSubject  string    `json:"oauth_subject"`
	OAuthLogin    string    `json:"oauth_login"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// GitHubInstallation is a per-account GitHub App installation link.
// Repositories contains repositories observed by runnerd for that installation,
// not the full GitHub App authorization scope.
type GitHubInstallation struct {
	ID              int64     `json:"id"`
	AccountID       int64     `json:"account_id"`
	InstallationID  int64     `json:"installation_id"`
	GitHubAccountID int64     `json:"github_account_id,omitempty"`
	AccountType     string    `json:"account_type,omitempty"`
	AccountLogin    string    `json:"account_login,omitempty"`
	AccountName     string    `json:"account_name,omitempty"`
	AccountAvatar   string    `json:"account_avatar,omitempty"`
	Repositories    []string  `json:"repositories"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type GitHubInstallationAccount struct {
	GitHubAccountID int64  `json:"github_account_id"`
	AccountType     string `json:"account_type"`
	AccountLogin    string `json:"account_login"`
	AccountName     string `json:"account_name,omitempty"`
	AccountAvatar   string `json:"account_avatar,omitempty"`
}

// GitHubInstallationRepositoryAccess preserves repositories within one installation authorization scope.
type GitHubInstallationRepositoryAccess struct {
	InstallationID int64
	Repositories   []string
}

const (
	AccountSecretTypeSandboxAPIKey        = "sandbox_api_key"
	AccountSecretTypeGitHubOAuthToken     = "github_oauth_token"
	AccountSecretTypeCacheAccessKeyID     = "cache_access_key_id"
	AccountSecretTypeCacheSecretAccessKey = "cache_secret_access_key"
	AccountScopeTypeAccount               = "account"
	AccountScopeTypeGitHubInstall         = "github_installation"
)

// AccountSecret stores an encrypted named secret for one account.
type AccountSecret struct {
	ID             int64     `json:"id"`
	ScopeType      string    `json:"scope_type"`
	ScopeID        int64     `json:"scope_id"`
	KeyType        string    `json:"key_type"`
	EncryptedValue string    `json:"-"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// AccountPreference stores a non-secret structured preference for one account.
type AccountPreference struct {
	ID        int64     `json:"id"`
	ScopeType string    `json:"scope_type"`
	ScopeID   int64     `json:"scope_id"`
	Namespace string    `json:"namespace"`
	Key       string    `json:"key"`
	ValueJSON string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SandboxServiceDefault struct {
	ID              int64      `json:"id"`
	Enabled         bool       `json:"enabled"`
	AudienceMode    string     `json:"audience_mode"`
	APIURL          string     `json:"api_url"`
	APIKeyEncrypted string     `json:"-"`
	APIKeyUpdatedAt *time.Time `json:"api_key_updated_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

const (
	SandboxServiceDefaultAudienceModeAll      = "all"
	SandboxServiceDefaultAudienceModeSelected = "selected"
)

type SandboxServiceDefaultAudience struct {
	ID              int64     `json:"id"`
	GitHubAccountID int64     `json:"github_account_id"`
	AccountType     string    `json:"account_type"`
	AccountLogin    string    `json:"account_login"`
	AccountName     string    `json:"account_name,omitempty"`
	AccountAvatar   string    `json:"account_avatar,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type RunnerRequestStore interface {
	Ensure() error
	CreateRequest(req RunnerRequest, payload []byte) (bool, RunnerState, error)
	CreateRejectedRequest(req RunnerRequest, payload []byte, reason string) (bool, RunnerState, error)
	ReadRequest(id string) (RunnerRequest, error)
	ReadState(id string) (RunnerState, error)
	WriteState(st RunnerState) error
	ListStates() ([]RunnerState, error)
	ListActiveStates() ([]RunnerState, error)
	ListMismatchedCompletedStates(limit int) ([]RunnerState, error)
	ListFailedWorkflowJobStates(limit int) ([]RunnerState, error)
	ListStatesPage(limit, offset int) ([]RunnerState, int64, error)
	ListStatesForRepositories(repositories []string, limit int) ([]RunnerState, error)
	ListStatesForGitHubInstallations(installationIDs []int64, limit int) ([]RunnerState, error)
	ListStatesForGitHubInstallationRepositories(access []GitHubInstallationRepositoryAccess, limit, offset int) ([]RunnerState, int64, error)
	ActiveCount() (int, error)
	InFlightCount() (int, error)
	ActiveCountForProfile(name string) (int, error)
	InFlightCountForProfile(name string) (int, error)
	ActiveCountForProfileScope(source string, scope RunnerProfileScope, name string) (int, error)
	InFlightCountForProfileScope(source string, scope RunnerProfileScope, name string) (int, error)
	ClaimNextRunnable(workerID string, now time.Time, leaseTTL time.Duration) (RunnerRequest, RunnerState, bool, error)
	ReleaseLease(id, workerID string) error
	RetryRequest(id string, now time.Time) (RunnerState, error)
	AppendLog(id, name string, data []byte)
	ReadLog(id, name string, maxBytes int64) ([]byte, error)
	ListRunnerEvents(id string, beforeID int64, limit int, eventTypes ...string) ([]RunnerEvent, bool, error)
	ListRunnerEventsAfter(id string, afterID int64, limit int, eventTypes ...string) ([]RunnerEvent, bool, error)
}

type RunnerCatalogStore interface {
	ListProfiles() ([]RunnerProfile, error)
	GetProfile(name string) (RunnerProfile, error)
	UpsertProfile(profile RunnerProfile) (RunnerProfile, error)
	UpsertProfileIfUnchanged(profile RunnerProfile, expectedUpdatedAt *time.Time) (RunnerProfile, error)
	ReconcileManagedProfiles(profiles []RunnerProfile) ([]ManagedProfileConflict, error)
	DeleteProfile(name string) error
	MatchProfile(repositoryFullName string, labels []string) (ProfileMatch, error)
	ListEffectiveProfiles(scope RunnerProfileScope) ([]EffectiveRunnerProfile, error)
	MatchProfileForScope(scope RunnerProfileScope, repositoryFullName string, labels []string) (ProfileMatch, error)
	ListScopedProfiles(scope RunnerProfileScope) ([]ScopedRunnerProfile, error)
	GetScopedProfile(scope RunnerProfileScope, name string) (ScopedRunnerProfile, error)
	UpsertScopedProfileIfUnchanged(profile ScopedRunnerProfile, expectedUpdatedAt *time.Time) (ScopedRunnerProfile, error)
	DeleteScopedProfileIfUnchanged(scope RunnerProfileScope, name string, expectedUpdatedAt *time.Time) error
}

type IdentityStore interface {
	GetAccount(accountID int64) (Account, error)
	GetAccountByOAuthIdentity(provider, subject string) (Account, OAuthIdentity, error)
	GetOAuthIdentityForAccount(accountID int64, provider string) (OAuthIdentity, error)
	ListAccounts(options AccountListOptions) ([]AccountListItem, int64, error)
	GetAccountStats() (AccountStats, error)
	UpdateAccountRoleWithAudit(update AccountRoleUpdate) (Account, error)
	EnsureAccountForOAuthIdentity(identity OAuthIdentity, role string) (Account, OAuthIdentity, error)
	UpsertAccountForOAuthIdentity(identity OAuthIdentity, role string) (Account, OAuthIdentity, error)
	LinkOAuthIdentityToAccount(accountID int64, identity OAuthIdentity) (Account, OAuthIdentity, error)
}

type GitHubInstallationStore interface {
	ListGitHubInstallations(accountID int64) ([]GitHubInstallation, error)
	ListGitHubInstallationAccounts() ([]GitHubInstallationAccount, error)
	GitHubInstallationAccountForInstallation(installationID int64) (GitHubInstallationAccount, error)
	GitHubInstallationAccountForLogin(accountLogin string) (GitHubInstallationAccount, error)
	GetGitHubInstallationOwner(installationID int64) (GitHubInstallationAccount, error)
	UpsertGitHubInstallationOwner(installationID int64, owner GitHubInstallationAccount) (GitHubInstallationAccount, error)
	GitHubInstallationScopeForAccountLogin(accountLogin string) (int64, bool, error)
	AccountScopeForPersonalGitHubInstallation(installationID int64) (int64, bool, error)
	UpsertGitHubInstallation(installation GitHubInstallation) (GitHubInstallation, error)
	DeleteGitHubInstallation(accountID, id int64) error
}

type AccountSecretStore interface {
	GetAccountSecret(scopeType string, scopeID int64, keyType string) (AccountSecret, error)
	UpsertAccountSecret(secret AccountSecret) (AccountSecret, error)
	DeleteAccountSecret(scopeType string, scopeID int64, keyType string) error
}

type AccountPreferenceStore interface {
	GetAccountPreference(scopeType string, scopeID int64, namespace, key string) (AccountPreference, error)
	DeleteAccountPreference(scopeType string, scopeID int64, namespace, key string) error
	UpsertAccountPreference(preference AccountPreference) (AccountPreference, error)
	UpsertAccountPreferenceAndSecret(preference AccountPreference, secret *AccountSecret) (AccountPreference, *AccountSecret, error)
	UpsertAccountPreferenceAndSecrets(preference AccountPreference, secrets ...AccountSecret) (AccountPreference, error)
	UpsertAccountPreferenceAndDeleteSecret(preference AccountPreference, secret AccountSecret) (AccountPreference, error)
}

type SandboxServiceDefaultStore interface {
	GetSandboxServiceDefault() (SandboxServiceDefault, error)
	UpsertSandboxServiceDefault(defaultConfig SandboxServiceDefault) (SandboxServiceDefault, error)
	UpdateSandboxServiceDefaultPreservingAPIKey(defaultConfig SandboxServiceDefault) (SandboxServiceDefault, error)
	DeleteSandboxServiceDefaultAPIKey() error
	ListSandboxServiceDefaultAudiences() ([]SandboxServiceDefaultAudience, error)
	UpsertSandboxServiceDefaultAudience(audience SandboxServiceDefaultAudience) (SandboxServiceDefaultAudience, error)
	DeleteSandboxServiceDefaultAudience(id int64) error
	SandboxServiceDefaultAudienceContains(githubAccountID int64, accountType string) (bool, error)
}

type AuditStore interface {
	AppendAuditEvent(event AuditEvent) (AuditEvent, error)
	ApplyMutationWithAudit(event AuditEvent, mutation func(Store) error) (AuditEvent, error)
	ListAuditEvents(limit int) ([]AuditEvent, error)
}

type Store interface {
	RunnerRequestStore
	RunnerCatalogStore
	IdentityStore
	GitHubInstallationStore
	AccountSecretStore
	AccountPreferenceStore
	SandboxServiceDefaultStore
	AuditStore
}

type Options struct {
	Backend        string
	DatabaseDSN    string
	MigrateOnStart bool
}
