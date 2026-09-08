package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/glebarez/sqlite"
	mysqldriver "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

const runnerRequestGitHubContextBackfillBatchSize = 200

type DBStore struct {
	opts     Options
	mu       sync.Mutex
	db       *gorm.DB
	migrated bool
}

func New(dir string) Store {
	return NewWithOptions(Options{
		Backend:        BackendSQLite,
		DatabaseDSN:    filepath.Join(dir, "runnerd.db"),
		MigrateOnStart: true,
	})
}

func NewWithOptions(opts Options) Store {
	backend := strings.ToLower(strings.TrimSpace(opts.Backend))
	if backend == "" {
		backend = BackendSQLite
	}
	if opts.DatabaseDSN == "" && backend == BackendSQLite {
		opts.DatabaseDSN = filepath.Join(".", "var", "runnerd.db")
	}
	opts.Backend = backend
	return &DBStore{opts: opts}
}

func (s *DBStore) Ensure() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return nil
	}

	db, err := s.open()
	if err != nil {
		return err
	}
	if s.opts.MigrateOnStart && !s.migrated {
		if err := s.migrate(db); err != nil {
			return err
		}
		s.migrated = true
	}
	if s.opts.Backend == BackendSQLite {
		if err := enableSQLiteForeignKeys(db); err != nil {
			return err
		}
	}
	s.db = db
	return nil
}

func (s *DBStore) dbOrEnsure() (*gorm.DB, error) {
	if err := s.Ensure(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.db, nil
}

func (s *DBStore) open() (*gorm.DB, error) {
	cfg := &gorm.Config{
		Logger:                                   gormlogger.Default.LogMode(gormlogger.Silent),
		DisableForeignKeyConstraintWhenMigrating: true,
	}
	switch s.opts.Backend {
	case BackendSQLite:
		dir := filepath.Dir(s.opts.DatabaseDSN)
		if dir != "." && dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return nil, err
			}
		}
		db, err := gorm.Open(sqlite.Open(s.opts.DatabaseDSN), cfg)
		if err != nil {
			return nil, err
		}
		if err := configureSQLite(db); err != nil {
			return nil, err
		}
		return db, nil
	case BackendPostgres:
		return gorm.Open(postgres.Open(s.opts.DatabaseDSN), cfg)
	case BackendMySQL:
		dsn, err := mysqlDSNWithParseTime(s.opts.DatabaseDSN)
		if err != nil {
			return nil, err
		}
		return gorm.Open(gormmysql.Open(dsn), cfg)
	default:
		return nil, fmt.Errorf("unsupported state backend: %s", s.opts.Backend)
	}
}

func configureSQLite(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	sqlDB.SetMaxOpenConns(1)
	sqlDB.SetMaxIdleConns(1)
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA busy_timeout = 15000",
	}
	for _, pragma := range pragmas {
		if err := db.Exec(pragma).Error; err != nil {
			return fmt.Errorf("%s: %w", pragma, err)
		}
	}
	return nil
}

func enableSQLiteForeignKeys(db *gorm.DB) error {
	if err := db.Exec("PRAGMA foreign_keys = ON").Error; err != nil {
		return fmt.Errorf("PRAGMA foreign_keys = ON: %w", err)
	}
	return nil
}

func mysqlDSNWithParseTime(dsn string) (string, error) {
	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		return "", err
	}
	cfg.ParseTime = true
	return cfg.FormatDSN(), nil
}

func (s *DBStore) migrate(db *gorm.DB) error {
	if err := migrateLegacySchemaColumns(db); err != nil {
		return err
	}
	existingSQLiteRunnerProfiles := s.opts.Backend == BackendSQLite &&
		db.Migrator().HasTable(&runnerProfileRecord{})
	if existingSQLiteRunnerProfiles {
		if err := migrateSQLiteRunnerProfileSchema(db); err != nil {
			return err
		}
	}
	existingSQLiteRunnerRequests := s.opts.Backend == BackendSQLite &&
		db.Migrator().HasTable(&runnerRequestRecord{})
	models := []any{
		&runnerEventRecord{},
	}
	if !existingSQLiteRunnerProfiles {
		models = append(models, &runnerProfileRecord{})
	}
	models = append(
		models,
		&auditEventRecord{},
		&accountRecord{},
		&oauthIdentityRecord{},
		&githubInstallationRecord{},
		&githubInstallationOwnerRecord{},
		&accountSecretRecord{},
		&accountPreferenceRecord{},
		&sandboxServiceDefaultRecord{},
		&sandboxServiceDefaultAudienceRecord{},
		&scopedRunnerProfileRecord{},
	)
	if existingSQLiteRunnerRequests {
		if err := migrateSQLiteRunnerRequestSchema(db); err != nil {
			return err
		}
	} else {
		models = append([]any{&runnerRequestRecord{}}, models...)
	}
	if err := db.AutoMigrate(models...); err != nil {
		return err
	}
	if err := backfillRunnerRequestGitHubContext(db); err != nil {
		return err
	}
	return nil
}

func migrateSQLiteRunnerProfileSchema(db *gorm.DB) error {
	return migrateSQLiteSchemaAdditively(db, &runnerProfileRecord{}, "runner_profiles")
}

func migrateSQLiteRunnerRequestSchema(db *gorm.DB) error {
	// SQLite records columns added through ALTER TABLE outside the original
	// CREATE TABLE body. Recreating that table through the current GORM SQLite
	// migrator can omit those columns from the copy and then add them back empty.
	return migrateSQLiteSchemaAdditively(db, &runnerRequestRecord{}, "runner_requests")
}

func migrateSQLiteSchemaAdditively(db *gorm.DB, model any, tableName string) error {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(model); err != nil {
		return err
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, field := range stmt.Schema.Fields {
			if field.IgnoreMigration || field.DBName == "" || tx.Migrator().HasColumn(model, field.Name) {
				continue
			}
			if err := tx.Migrator().AddColumn(model, field.Name); err != nil {
				return fmt.Errorf("add %s.%s: %w", tableName, field.DBName, err)
			}
		}
		for _, index := range stmt.Schema.ParseIndexes() {
			if tx.Migrator().HasIndex(model, index.Name) {
				continue
			}
			if err := tx.Migrator().CreateIndex(model, index.Name); err != nil {
				return fmt.Errorf("create %s index %s: %w", tableName, index.Name, err)
			}
		}
		return nil
	})
}

func migrateLegacySchemaColumns(db *gorm.DB) error {
	migrator := db.Migrator()
	if migrator.HasTable(&oauthIdentityRecord{}) {
		for _, constraint := range []string{"Account", "fk_oauth_identities_account", "oauth_identities_account_id_fkey"} {
			if migrator.HasConstraint(&oauthIdentityRecord{}, constraint) {
				if err := migrator.DropConstraint(&oauthIdentityRecord{}, constraint); err != nil {
					return err
				}
			}
		}
	}
	if migrator.HasTable(&runnerProfileRecord{}) && !migrator.HasColumn(&runnerProfileRecord{}, "default_available") {
		if err := migrator.AddColumn(&legacyRunnerProfileDefaultAvailableColumn{}, "DefaultAvailable"); err != nil {
			return err
		}
	}
	if migrator.HasTable(&accountSecretRecord{}) &&
		(!migrator.HasColumn(&accountSecretRecord{}, "scope_type") || !migrator.HasColumn(&accountSecretRecord{}, "scope_id")) {
		if err := migrator.DropTable(&accountSecretRecord{}); err != nil {
			return err
		}
	}
	if migrator.HasTable(&accountPreferenceRecord{}) &&
		(!migrator.HasColumn(&accountPreferenceRecord{}, "scope_type") || !migrator.HasColumn(&accountPreferenceRecord{}, "scope_id")) {
		if err := migrator.DropTable(&accountPreferenceRecord{}); err != nil {
			return err
		}
	}
	return nil
}

func backfillRunnerRequestGitHubContext(db *gorm.DB) error {
	var records []runnerRequestRecord
	return db.
		Where("github_payload_json <> ''").
		Where(
			"(github_context_backfilled IS NULL OR github_context_backfilled = ?) OR "+
				"((github_installation_id IS NULL OR github_installation_id = 0) AND github_payload_json LIKE ?)",
			false,
			`%"installation"%`,
		).
		FindInBatches(&records, runnerRequestGitHubContextBackfillBatchSize, func(tx *gorm.DB, _ int) error {
			return tx.Transaction(func(batchTx *gorm.DB) error {
				for _, record := range records {
					updates := runnerRequestGitHubContextBackfill(record)
					if len(updates) == 0 {
						continue
					}
					if err := batchTx.Model(&runnerRequestRecord{}).Where("id = ?", record.ID).UpdateColumns(updates).Error; err != nil {
						return err
					}
				}
				return nil
			})
		}).Error
}

func runnerRequestGitHubContextBackfill(record runnerRequestRecord) map[string]any {
	links := githubLinksFromPayload(record)
	updates := make(map[string]any)
	if !record.GitHubContextBackfilled {
		updates["github_context_backfilled"] = true
	}
	if record.GitHubInstallationID == 0 {
		if installationID := githubInstallationIDFromPayload(record.GitHubPayloadJSON); installationID > 0 {
			updates["github_installation_id"] = installationID
		}
	}
	if record.WorkflowRunID == 0 && links.workflowRunID != 0 {
		updates["workflow_run_id"] = links.workflowRunID
	}
	if record.WorkflowName == "" && links.workflowName != "" {
		updates["workflow_name"] = links.workflowName
	}
	if record.WorkflowRunAttempt == 0 && links.workflowRunAttempt != 0 {
		updates["workflow_run_attempt"] = links.workflowRunAttempt
	}
	if record.HeadBranch == "" && links.headBranch != "" {
		updates["head_branch"] = links.headBranch
	}
	if record.HeadSHA == "" && links.headSHA != "" {
		updates["head_sha"] = links.headSHA
	}
	if record.GitHubJobURL == "" && links.jobURL != "" {
		updates["github_job_url"] = links.jobURL
	}
	if record.PullRequestNumber == 0 && links.pullRequestNumber != 0 {
		updates["pull_request_number"] = links.pullRequestNumber
	}
	return updates
}
