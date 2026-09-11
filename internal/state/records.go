package state

import "time"

type runnerRequestRecord struct {
	ID                      string     `gorm:"column:id;primaryKey;index:idx_runner_requests_queued_id,priority:2,sort:asc;index:idx_runner_requests_github_installation_queued_id,priority:3,sort:asc;index:idx_runner_requests_profile_queued_id,priority:3,sort:asc"`
	Source                  string     `gorm:"column:source;not null"`
	WorkflowJobID           *int64     `gorm:"column:workflow_job_id;uniqueIndex:idx_runner_requests_workflow_job_id"`
	GitHubInstallationID    int64      `gorm:"column:github_installation_id;index:idx_runner_requests_github_installation_updated;index:idx_runner_requests_github_installation_queued,priority:1;index:idx_runner_requests_github_installation_queued_id,priority:1"`
	WorkflowRunID           int64      `gorm:"column:workflow_run_id;index:idx_runner_requests_workflow_run"`
	WorkflowName            string     `gorm:"column:workflow_name"`
	WorkflowRunAttempt      int64      `gorm:"column:workflow_run_attempt"`
	HeadBranch              string     `gorm:"column:head_branch"`
	HeadSHA                 string     `gorm:"column:head_sha;index:idx_runner_requests_repository_head,priority:2"`
	GitHubJobURL            string     `gorm:"column:github_job_url"`
	PullRequestNumber       int64      `gorm:"column:pull_request_number;index:idx_runner_requests_repository_pr,priority:2"`
	GitHubContextBackfilled bool       `gorm:"column:github_context_backfilled;not null;default:false;index:idx_runner_requests_github_context_backfill"`
	RepositoryFullName      string     `gorm:"column:repository_full_name;index:idx_runner_requests_repository_pr,priority:1;index:idx_runner_requests_repository_head,priority:1"`
	RequestedLabelsJSON     string     `gorm:"column:requested_labels_json"`
	LabelsJSON              string     `gorm:"column:labels_json;not null"`
	ProfileName             string     `gorm:"column:profile_name;index:idx_runner_requests_profile_queued_id,priority:1;index:idx_runner_requests_profile_scope_status,priority:4"`
	ProfileSource           string     `gorm:"column:profile_source;index:idx_runner_requests_profile_scope_status,priority:1"`
	ProfileScopeType        string     `gorm:"column:profile_scope_type;index:idx_runner_requests_profile_scope_status,priority:2"`
	ProfileScopeID          int64      `gorm:"column:profile_scope_id;index:idx_runner_requests_profile_scope_status,priority:3"`
	RunnerGroup             string     `gorm:"column:runner_group"`
	RunnerName              string     `gorm:"column:runner_name;not null"`
	Status                  string     `gorm:"column:status;not null;index:idx_runner_requests_status_updated;index:idx_runner_requests_status_retry_queue;index:idx_runner_requests_lease_expiry"`
	FailureStage            string     `gorm:"column:failure_stage"`
	FailureReason           string     `gorm:"column:failure_reason"`
	LastErrorCode           string     `gorm:"column:last_error_code"`
	LastErrorMessage        string     `gorm:"column:last_error_message"`
	LastErrorRetryable      bool       `gorm:"column:last_error_retryable;not null;default:false"`
	RetryCount              int        `gorm:"column:retry_count;not null;default:0"`
	SandboxID               string     `gorm:"column:sandbox_id"`
	SandboxAPIURL           string     `gorm:"column:sandbox_api_url"`
	SandboxAPIKeyEncrypted  string     `gorm:"column:sandbox_api_key_encrypted;type:text"`
	SandboxConfigSource     string     `gorm:"column:sandbox_config_source"`
	ProcessPID              uint32     `gorm:"column:process_pid"`
	AssignedJobID           int64      `gorm:"column:assigned_job_id"`
	AssignedJobName         string     `gorm:"column:assigned_job_name"`
	Error                   string     `gorm:"column:error"`
	GitHubPayloadJSON       string     `gorm:"column:github_payload_json;type:text"`
	QueuedAt                time.Time  `gorm:"column:queued_at;not null;index:idx_runner_requests_status_updated;index:idx_runner_requests_status_retry_queue;index:idx_runner_requests_github_installation_queued,priority:2;index:idx_runner_requests_queued_id,priority:1,sort:desc;index:idx_runner_requests_github_installation_queued_id,priority:2,sort:desc;index:idx_runner_requests_profile_queued_id,priority:2,sort:desc"`
	LastAttemptAt           *time.Time `gorm:"column:last_attempt_at"`
	NextRetryAt             *time.Time `gorm:"column:next_retry_at;index:idx_runner_requests_status_retry_queue"`
	CreatingAt              *time.Time `gorm:"column:creating_at"`
	RunningAt               *time.Time `gorm:"column:running_at"`
	StoppingAt              *time.Time `gorm:"column:stopping_at"`
	CompletedAt             *time.Time `gorm:"column:completed_at"`
	FailedAt                *time.Time `gorm:"column:failed_at"`
	LeaseOwner              string     `gorm:"column:lease_owner"`
	LeaseExpiresAt          *time.Time `gorm:"column:lease_expires_at;index:idx_runner_requests_lease_expiry"`
	UpdatedAt               time.Time  `gorm:"column:updated_at;not null;index:idx_runner_requests_status_updated;index:idx_runner_requests_github_installation_updated"`
	Version                 int64      `gorm:"column:version;not null;default:0"`
}

func (runnerRequestRecord) TableName() string { return "runner_requests" }

type runnerEventRecord struct {
	ID          int64     `gorm:"column:id;primaryKey;autoIncrement;index:idx_runner_events_request_id,priority:2;index:idx_runner_events_request_type_id,priority:3"`
	RequestID   string    `gorm:"column:request_id;not null;index:idx_runner_events_request_created;index:idx_runner_events_request_id,priority:1;index:idx_runner_events_request_type_id,priority:1"`
	EventType   string    `gorm:"column:event_type;not null;index:idx_runner_events_request_type_id,priority:2"`
	Stage       string    `gorm:"column:stage"`
	Message     string    `gorm:"column:message;type:text"`
	PayloadJSON string    `gorm:"column:payload_json;type:text"`
	CreatedAt   time.Time `gorm:"column:created_at;not null;index:idx_runner_events_request_created"`
}

func (runnerEventRecord) TableName() string { return "runner_events" }

type runnerProfileRecord struct {
	Name                string    `gorm:"column:name;primaryKey"`
	LabelsJSON          string    `gorm:"column:labels_json;not null"`
	RequiredLabelsJSON  *string   `gorm:"column:required_labels_json"`
	TemplateID          string    `gorm:"column:template_id;not null"`
	DefaultTemplateName string    `gorm:"column:default_template_name"`
	RunnerGroup         string    `gorm:"column:runner_group"`
	MaxConcurrency      int       `gorm:"column:max_concurrency;not null"`
	MinIdle             int       `gorm:"column:min_idle;not null;default:0"`
	Priority            int       `gorm:"column:priority;not null;default:0"`
	Enabled             bool      `gorm:"column:enabled;not null"`
	DefaultAvailable    bool      `gorm:"column:default_available;not null"`
	ManagedBy           string    `gorm:"column:managed_by"`
	CatalogRevision     int       `gorm:"column:catalog_revision;not null;default:0"`
	CreatedAt           time.Time `gorm:"column:created_at;not null"`
	UpdatedAt           time.Time `gorm:"column:updated_at;not null"`
}

func (runnerProfileRecord) TableName() string { return "runner_profiles" }

type scopedRunnerProfileRecord struct {
	ScopeType          string    `gorm:"column:scope_type;primaryKey;index:idx_scoped_runner_profiles_scope,priority:1;uniqueIndex:idx_scoped_runner_profiles_scope_labels,priority:1"`
	ScopeID            int64     `gorm:"column:scope_id;primaryKey;index:idx_scoped_runner_profiles_scope,priority:2;uniqueIndex:idx_scoped_runner_profiles_scope_labels,priority:2"`
	Name               string    `gorm:"column:name;primaryKey"`
	WorkflowLabelsJSON string    `gorm:"column:workflow_labels_json;type:text;not null"`
	LabelKey           string    `gorm:"column:label_key;size:64;not null;uniqueIndex:idx_scoped_runner_profiles_scope_labels,priority:3"`
	TemplateID         string    `gorm:"column:template_id;not null"`
	RunnerGroup        string    `gorm:"column:runner_group"`
	MaxConcurrency     int       `gorm:"column:max_concurrency;not null"`
	Enabled            bool      `gorm:"column:enabled;not null"`
	CreatedAt          time.Time `gorm:"column:created_at;not null"`
	UpdatedAt          time.Time `gorm:"column:updated_at;not null"`
}

func (scopedRunnerProfileRecord) TableName() string { return "scoped_runner_profiles" }

type legacyRunnerProfileDefaultAvailableColumn struct {
	DefaultAvailable bool `gorm:"column:default_available;not null;default:true"`
}

func (legacyRunnerProfileDefaultAvailableColumn) TableName() string { return "runner_profiles" }

type auditEventRecord struct {
	ID           int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Actor        string    `gorm:"column:actor;not null"`
	Action       string    `gorm:"column:action;not null"`
	ResourceType string    `gorm:"column:resource_type;not null"`
	ResourceID   string    `gorm:"column:resource_id;not null"`
	PayloadJSON  string    `gorm:"column:payload_json;type:text"`
	CreatedAt    time.Time `gorm:"column:created_at;not null;index:idx_audit_events_created"`
}

func (auditEventRecord) TableName() string { return "audit_events" }

type accountRecord struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	Role      string    `gorm:"column:role;not null"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null"`
}

func (accountRecord) TableName() string { return "accounts" }

type oauthIdentityRecord struct {
	ID            int64         `gorm:"column:id;primaryKey;autoIncrement"`
	AccountID     int64         `gorm:"column:account_id;not null;index:idx_oauth_identities_account"`
	Account       accountRecord `gorm:"foreignKey:AccountID"`
	OAuthProvider string        `gorm:"column:oauth_provider;not null;uniqueIndex:idx_oauth_identities_provider_subject,length:191"`
	OAuthSubject  string        `gorm:"column:oauth_subject;not null;uniqueIndex:idx_oauth_identities_provider_subject,length:191"`
	OAuthLogin    string        `gorm:"column:oauth_login;not null"`
	CreatedAt     time.Time     `gorm:"column:created_at;not null"`
	UpdatedAt     time.Time     `gorm:"column:updated_at;not null"`
}

func (oauthIdentityRecord) TableName() string { return "oauth_identities" }

type githubInstallationRecord struct {
	ID              int64         `gorm:"column:id;primaryKey;autoIncrement"`
	AccountID       int64         `gorm:"column:account_id;not null;uniqueIndex:idx_github_installations_account_installation;index:idx_github_installations_account"`
	Account         accountRecord `gorm:"foreignKey:AccountID"`
	InstallationID  int64         `gorm:"column:installation_id;not null;uniqueIndex:idx_github_installations_account_installation;index:idx_github_installations_installation"`
	GitHubAccountID int64         `gorm:"column:github_account_id;index:idx_github_installations_github_account"`
	AccountType     string        `gorm:"column:account_type"`
	AccountLogin    string        `gorm:"column:account_login;not null"`
	AccountName     string        `gorm:"column:account_name"`
	AccountAvatar   string        `gorm:"column:account_avatar"`
	CreatedAt       time.Time     `gorm:"column:created_at;not null"`
	UpdatedAt       time.Time     `gorm:"column:updated_at;not null"`
}

func (githubInstallationRecord) TableName() string { return "github_installations" }

type githubInstallationOwnerRecord struct {
	InstallationID  int64     `gorm:"column:installation_id;primaryKey;autoIncrement:false"`
	GitHubAccountID int64     `gorm:"column:github_account_id;not null;index:idx_github_installation_owners_account"`
	AccountType     string    `gorm:"column:account_type;not null;index:idx_github_installation_owners_account"`
	AccountLogin    string    `gorm:"column:account_login;not null"`
	AccountName     string    `gorm:"column:account_name"`
	AccountAvatar   string    `gorm:"column:account_avatar"`
	CreatedAt       time.Time `gorm:"column:created_at;not null"`
	UpdatedAt       time.Time `gorm:"column:updated_at;not null"`
}

func (githubInstallationOwnerRecord) TableName() string { return "github_installation_owners" }

type accountSecretRecord struct {
	ID             int64     `gorm:"column:id;primaryKey;autoIncrement"`
	ScopeType      string    `gorm:"column:scope_type;not null;uniqueIndex:idx_account_secrets_scope_type,priority:1,length:191;index:idx_account_secrets_scope"`
	ScopeID        int64     `gorm:"column:scope_id;not null;uniqueIndex:idx_account_secrets_scope_type,priority:2;index:idx_account_secrets_scope"`
	KeyType        string    `gorm:"column:key_type;not null;uniqueIndex:idx_account_secrets_scope_type,priority:3,length:191"`
	EncryptedValue string    `gorm:"column:encrypted_value;type:text;not null"`
	CreatedAt      time.Time `gorm:"column:created_at;not null"`
	UpdatedAt      time.Time `gorm:"column:updated_at;not null"`
}

func (accountSecretRecord) TableName() string { return "account_secrets" }

type accountPreferenceRecord struct {
	ID        int64     `gorm:"column:id;primaryKey;autoIncrement"`
	ScopeType string    `gorm:"column:scope_type;not null;uniqueIndex:idx_account_preferences_scope_key,priority:1,length:191;index:idx_account_preferences_scope"`
	ScopeID   int64     `gorm:"column:scope_id;not null;uniqueIndex:idx_account_preferences_scope_key,priority:2;index:idx_account_preferences_scope"`
	Namespace string    `gorm:"column:namespace;not null;uniqueIndex:idx_account_preferences_scope_key,priority:3,length:191"`
	Key       string    `gorm:"column:key;not null;uniqueIndex:idx_account_preferences_scope_key,priority:4,length:191"`
	ValueJSON string    `gorm:"column:value_json;type:text;not null"`
	CreatedAt time.Time `gorm:"column:created_at;not null"`
	UpdatedAt time.Time `gorm:"column:updated_at;not null"`
}

func (accountPreferenceRecord) TableName() string { return "account_preferences" }

type sandboxServiceDefaultRecord struct {
	ID              int64      `gorm:"column:id;primaryKey"`
	Enabled         bool       `gorm:"column:enabled;not null"`
	AudienceMode    string     `gorm:"column:audience_mode"`
	APIURL          string     `gorm:"column:api_url"`
	APIKeyEncrypted string     `gorm:"column:api_key_encrypted;type:text"`
	APIKeyUpdatedAt *time.Time `gorm:"column:api_key_updated_at"`
	CreatedAt       time.Time  `gorm:"column:created_at;not null"`
	UpdatedAt       time.Time  `gorm:"column:updated_at;not null"`
}

func (sandboxServiceDefaultRecord) TableName() string { return "sandbox_service_defaults" }

type sandboxServiceDefaultAudienceRecord struct {
	ID              int64     `gorm:"column:id;primaryKey;autoIncrement"`
	GitHubAccountID int64     `gorm:"column:github_account_id;not null;uniqueIndex:idx_sandbox_default_audience_identity,priority:2"`
	AccountType     string    `gorm:"column:account_type;not null;uniqueIndex:idx_sandbox_default_audience_identity,priority:1,length:191"`
	AccountLogin    string    `gorm:"column:account_login;not null"`
	AccountName     string    `gorm:"column:account_name"`
	AccountAvatar   string    `gorm:"column:account_avatar"`
	CreatedAt       time.Time `gorm:"column:created_at;not null"`
	UpdatedAt       time.Time `gorm:"column:updated_at;not null"`
}

func (sandboxServiceDefaultAudienceRecord) TableName() string {
	return "sandbox_service_default_audiences"
}
