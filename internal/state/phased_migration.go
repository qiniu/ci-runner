package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/google/uuid"
	"github.com/qiniu/ci-runner/internal/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

const phasedProtocol = 1

// PhasedMigrationOptions separates source-authoritative copying from PG-owned
// operation. Only cutover and resume-source require stopped source writers.
type PhasedMigrationOptions struct {
	OfflineMigrationOptions
	Mode            string
	SourceStopped   bool
	SkipEvents      bool
	MaxFinalChanges int64
	MaxFinalRows    int64
	BatchPause      time.Duration
}

// PhasedMigrationReport distinguishes availability from complete log history.
type PhasedMigrationReport struct {
	Phase                 string `json:"phase"`
	SourceSealed          bool   `json:"source_sealed"`
	RequestsCopied        int64  `json:"requests_copied"`
	PendingRequestChanges int64  `json:"pending_request_changes"`
	FinalTableRows        int64  `json:"final_table_rows"`
	PendingEventRows      int64  `json:"pending_event_rows"`
	EventsCopied          int64  `json:"events_copied"`
	EventHighWatermark    int64  `json:"event_high_watermark"`
	ReadyForCutover       bool   `json:"ready_for_cutover"`
	ReadyForPG            bool   `json:"ready_for_pg"`
	HistoryComplete       bool   `json:"history_complete"`
}

type phasedSourceRecord struct {
	ID            int    `gorm:"primaryKey;autoIncrement:false"`
	Protocol      int    `gorm:"not null"`
	Token         string `gorm:"not null"`
	TargetBinding string `gorm:"not null"`
	SchemaHash    string `gorm:"not null"`
	Phase         string `gorm:"not null"`
	RequestUpper  string `gorm:"not null"`
	EventFloor    int64  `gorm:"not null"`
}

func (phasedSourceRecord) TableName() string { return "runnerd_transfer_source" }

type phasedTargetRecord struct {
	ID             int    `gorm:"primaryKey;autoIncrement:false"`
	Protocol       int    `gorm:"not null"`
	Token          string `gorm:"not null"`
	SchemaHash     string `gorm:"not null"`
	Phase          string `gorm:"not null"`
	JournalCursor  int64  `gorm:"not null"`
	EventWatermark int64  `gorm:"not null"`
}

func (phasedTargetRecord) TableName() string { return "runnerd_transfer_target" }

type phasedCheckpoint struct {
	Name   string `gorm:"column:table_name;primaryKey"`
	Cursor string `gorm:"not null"`
	Rows   int64  `gorm:"not null"`
	Done   bool   `gorm:"not null"`
}

func (phasedCheckpoint) TableName() string { return "runnerd_transfer_progress" }

type phasedMigration struct {
	options             PhasedMigrationOptions
	source, target      *gorm.DB
	inventory           migrationInventory
	sourceState         phasedSourceRecord
	targetState         phasedTargetRecord
	schema              string
	binding, schemaHash string
	stage               string
}

func (m *phasedMigration) progress(stage string, rows int64) {
	m.stage = stage
	if m.options.Progress != nil {
		m.options.Progress(OfflineMigrationProgress{Stage: stage, Rows: rows})
	}
}

// MigrateSQLiteToPostgresPhased commits verified batches independently. Its
// database-owned checkpoints survive timeout, process death and retries.
func MigrateSQLiteToPostgresPhased(ctx context.Context, options PhasedMigrationOptions) (report PhasedMigrationReport, resultErr error) {
	m := &phasedMigration{options: options}
	m.progress("validate phased migration options", 0)
	defer func() {
		if resultErr == nil {
			return
		}
		switch {
		case errors.Is(resultErr, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
			resultErr = fmt.Errorf("phased migration failed at %s: %w; inspect status before switching services", m.stage, context.Canceled)
		case errors.Is(resultErr, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded):
			resultErr = fmt.Errorf("phased migration failed at %s: %w; verified batches are retained", m.stage, context.DeadlineExceeded)
		case errors.Is(resultErr, errMigrationPrecondition), errors.Is(resultErr, errMigrationInvalid), errors.Is(resultErr, errLegacyMigration):
			resultErr = fmt.Errorf("phased migration failed at %s: %w", m.stage, resultErr)
		case m.stage == "connect PostgreSQL target":
			resultErr = fmt.Errorf("phased migration failed at %s: %s", m.stage, offlinePostgresConnectionFailure(resultErr))
		default:
			resultErr = fmt.Errorf("phased migration failed at %s; inspect status before retrying or switching services", m.stage)
		}
	}()
	switch options.Mode {
	case "prepare", "sync", "status", "backfill":
	case "cutover", "resume-source":
		if !options.SourceStopped {
			return report, fmt.Errorf("%w: --source-stopped required", errMigrationPrecondition)
		}
	default:
		return report, fmt.Errorf("%w: unknown migration mode", errMigrationPrecondition)
	}
	if _, ok := ctx.Deadline(); !ok || options.PostgresDSN == "" || options.SQLitePath == "" {
		return report, fmt.Errorf("%w: deadline, SQLite path and PostgreSQL target required", errMigrationPrecondition)
	}
	if options.BatchPause < 0 {
		return report, fmt.Errorf("%w: negative batch pause", errMigrationPrecondition)
	}
	if m.options.MaxFinalRows <= 0 {
		m.options.MaxFinalRows = 100000
	}
	if m.options.MaxFinalChanges <= 0 {
		m.options.MaxFinalChanges = 10000
	}
	inventory, err := newMigrationInventory()
	if err != nil {
		return report, err
	}
	m.inventory = inventory
	encoded, err := json.Marshal(inventory)
	if err != nil {
		return report, err
	}
	hash := sha256.Sum256(encoded)
	m.schemaHash = hex.EncodeToString(hash[:])
	sourcePath, err := filepath.Abs(options.SQLitePath)
	if err != nil {
		return report, err
	}
	info, err := os.Stat(sourcePath)
	if err != nil || !info.Mode().IsRegular() {
		return report, fmt.Errorf("%w: existing SQLite file required", errMigrationPrecondition)
	}
	cfg := func() *gorm.Config {
		return &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), DisableAutomaticPing: true, DisableForeignKeyConstraintWhenMigrating: true}
	}
	uri := (&url.URL{Scheme: "file", Path: sourcePath, RawQuery: "mode=rw"}).String()
	sourcePool, err := sql.Open("sqlite", uri)
	if err != nil {
		return report, err
	}
	defer sourcePool.Close()
	sourceConn, err := sourcePool.Conn(ctx)
	if err != nil {
		return report, err
	}
	defer sourceConn.Close()
	if _, err := sourceConn.ExecContext(ctx, "PRAGMA busy_timeout = 1000"); err != nil {
		return report, err
	}
	m.source, err = gorm.Open(sqlite.Dialector{Conn: sourceConn}, cfg())
	if err != nil {
		return report, err
	}
	m.source = m.source.WithContext(ctx)
	var journalMode string
	if err := m.source.Raw("PRAGMA journal_mode").Scan(&journalMode).Error; err != nil {
		return report, err
	}
	if strings.ToLower(journalMode) != "wal" {
		return report, fmt.Errorf("%w: phased migration requires an existing WAL-mode SQLite database", errMigrationPrecondition)
	}
	m.progress("validate source schema and migration fence", 0)
	if m.source.Migrator().HasTable(&migrationControlRecord{}) {
		if err := validateStandaloneMigrationDB(m.source); err != nil {
			return report, err
		}
	}
	if _, err := inspectMigrationSchema(ctx, m.source, inventory); err != nil {
		return report, err
	}
	m.progress("connect PostgreSQL target", 0)
	targetPool, err := openPostgresPool(options.PostgresDSN)
	if err != nil {
		return report, err
	}
	defer targetPool.Close()
	targetConn, err := targetPool.Conn(ctx)
	if err != nil {
		return report, err
	}
	defer targetConn.Close()
	if err := targetConn.PingContext(ctx); err != nil {
		return report, err
	}
	m.target, err = gorm.Open(postgres.New(postgres.Config{Conn: targetConn}), cfg())
	if err != nil {
		return report, err
	}
	m.target = m.target.WithContext(ctx)
	if err := m.target.Raw("SELECT COALESCE(current_schema(), '')").Scan(&m.schema).Error; err != nil {
		return report, err
	}
	if m.schema == "" {
		return report, fmt.Errorf("%w: target schema unavailable", errMigrationPrecondition)
	}
	// Pin this dedicated connection; never resolve metadata or tables in fallback schemas.
	if err := m.target.Exec("SELECT set_config('search_path', quote_ident(?), false)", m.schema).Error; err != nil {
		return report, err
	}
	if options.Mode != "status" {
		var locked bool
		if err := m.target.Raw("SELECT pg_try_advisory_lock(724901832)").Scan(&locked).Error; err != nil {
			return report, err
		}
		if !locked {
			return report, fmt.Errorf("%w: another importer owns the target", errMigrationPrecondition)
		}
	}
	// Closing the dedicated pool also releases the session lock after cancellation.
	pgConfig, err := config.ParsePostgresDSN(config.Secret(options.PostgresDSN))
	if err != nil {
		return report, err
	}
	identity, _ := json.Marshal([]any{pgConfig.Host, pgConfig.Port, pgConfig.Database, m.schema})
	bound := sha256.Sum256(identity)
	m.binding = hex.EncodeToString(bound[:])
	m.progress("validate migration identity and checkpoints", 0)
	if options.Mode == "prepare" {
		if err := m.prepare(); err != nil {
			return report, err
		}
	} else if err := m.load(); err != nil {
		return report, err
	}
	if err := m.validateSourceTriggers(); err != nil {
		return report, err
	}
	if err := m.validateTargetGuards(); err != nil {
		return report, err
	}
	switch options.Mode {
	case "sync":
		if m.sourceState.Phase != "copying" || m.targetState.Phase != "copying" {
			return report, fmt.Errorf("%w: sync requires a writable source and an inactive PG target", errMigrationPrecondition)
		}
		if err := m.syncSmallTables(); err != nil {
			return report, err
		}
		if err := m.copyRequests(); err != nil {
			return report, err
		}
		if err := m.replayRequests(); err != nil {
			return report, err
		}
		if !options.SkipEvents {
			if err := m.copyEvents(false); err != nil {
				return report, err
			}
		}
	case "cutover":
		if err := m.cutover(); err != nil {
			return report, err
		}
	case "resume-source":
		if err := m.resumeSource(); err != nil {
			return report, err
		}
	case "backfill":
		if m.sourceState.Phase != "sealed" || (m.targetState.Phase != "ready" && m.targetState.Phase != "complete") {
			return report, fmt.Errorf("%w: backfill requires a sealed source and a ready PG target", errMigrationPrecondition)
		}
		if err := m.copyEvents(true); err != nil {
			return report, err
		}
	}
	report, err = m.report()
	if err == nil {
		m.progress("phased migration command complete", 0)
	}
	return report, err
}

func (m *phasedMigration) load() error {
	if !m.source.Migrator().HasTable(&phasedSourceRecord{}) || !m.target.Migrator().HasTable(&phasedTargetRecord{}) {
		return fmt.Errorf("%w: run prepare for this source and target first", errMigrationPrecondition)
	}
	if err := m.source.First(&m.sourceState, 1).Error; err != nil {
		return err
	}
	if err := m.target.First(&m.targetState, 1).Error; err != nil {
		return err
	}
	if m.sourceState.Protocol != phasedProtocol || m.targetState.Protocol != phasedProtocol || m.sourceState.SchemaHash != m.schemaHash || m.targetState.SchemaHash != m.schemaHash || m.sourceState.TargetBinding != m.binding || m.sourceState.Token != m.targetState.Token || !validMigrationIdentity(m.sourceState.Token) {
		return fmt.Errorf("%w: migration identity, target binding or model schema differs", errMigrationPrecondition)
	}
	if (m.sourceState.Phase != "copying" && m.sourceState.Phase != "sealed") || (m.targetState.Phase != "copying" && m.targetState.Phase != "ready" && m.targetState.Phase != "complete") || (m.targetState.Phase != "copying" && m.sourceState.Phase != "sealed") {
		return fmt.Errorf("%w: inconsistent migration phase", errMigrationPrecondition)
	}
	var high int64
	if err := m.source.Raw("SELECT COALESCE(MAX(seq), 0) FROM runnerd_transfer_changes").Scan(&high).Error; err != nil {
		return err
	}
	if high < m.targetState.JournalCursor {
		return fmt.Errorf("%w: source journal moved backwards", errMigrationPrecondition)
	}
	return nil
}

func (m *phasedMigration) prepare() error {
	hasSource := m.source.Migrator().HasTable(&phasedSourceRecord{})
	hasTarget := m.target.Migrator().HasTable(&phasedTargetRecord{})
	if hasSource && hasTarget {
		return m.load()
	}
	if hasTarget {
		return fmt.Errorf("%w: target belongs to another source", errMigrationPrecondition)
	}
	var objects int64
	if err := m.target.Raw("SELECT count(*) FROM pg_class WHERE relnamespace = (SELECT oid FROM pg_namespace WHERE nspname = current_schema())").Scan(&objects).Error; err != nil {
		return err
	}
	if objects != 0 {
		return fmt.Errorf("%w: prepare requires an empty target schema", errMigrationPrecondition)
	}
	if !hasSource {
		if err := m.onlineBackup(); err != nil {
			return err
		}
		m.progress("install source change capture", 0)
		if err := m.sourceImmediate(func(tx *gorm.DB) error {
			if tx.Migrator().HasTable(&phasedSourceRecord{}) {
				return fmt.Errorf("%w: concurrent source preparation", errMigrationPrecondition)
			}
			if err := tx.AutoMigrate(&phasedSourceRecord{}); err != nil {
				return err
			}
			if err := tx.Exec("CREATE TABLE runnerd_transfer_changes (seq INTEGER PRIMARY KEY AUTOINCREMENT, request_id TEXT NOT NULL)").Error; err != nil {
				return err
			}
			var upper string
			if err := tx.Raw("SELECT COALESCE(MAX(id COLLATE BINARY), '') FROM runner_requests").Scan(&upper).Error; err != nil {
				return err
			}
			table, _ := migrationTableByName(m.inventory, "runner_events")
			floor, err := migrationSequenceWatermark(tx, table, migrationColumn{Name: "id"})
			if err != nil {
				return err
			}
			m.sourceState = phasedSourceRecord{ID: 1, Protocol: phasedProtocol, Token: uuid.NewString(), TargetBinding: m.binding, SchemaHash: m.schemaHash, Phase: "copying", RequestUpper: upper, EventFloor: floor}
			if err := tx.Create(&m.sourceState).Error; err != nil {
				return err
			}
			for _, trigger := range m.sourceTriggers() {
				if err := tx.Exec(trigger.sql).Error; err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return err
		}
	} else {
		if err := m.source.First(&m.sourceState, 1).Error; err != nil {
			return err
		}
		if m.sourceState.Phase != "copying" || m.sourceState.Protocol != phasedProtocol || m.sourceState.TargetBinding != m.binding || m.sourceState.SchemaHash != m.schemaHash || !validMigrationIdentity(m.sourceState.Token) {
			return fmt.Errorf("%w: interrupted preparation belongs to another target or version", errMigrationPrecondition)
		}
	}
	if err := m.validateSourceTriggers(); err != nil {
		return err
	}
	m.progress("prepare fenced PostgreSQL schema", 0)
	if err := m.target.Transaction(func(tx *gorm.DB) error {
		if err := tx.AutoMigrate(append(migrationModels(), &phasedTargetRecord{}, &phasedCheckpoint{})...); err != nil {
			return err
		}
		m.targetState = phasedTargetRecord{ID: 1, Protocol: phasedProtocol, Token: m.sourceState.Token, SchemaHash: m.schemaHash, Phase: "copying"}
		if err := tx.Create(&m.targetState).Error; err != nil {
			return err
		}
		for _, name := range []string{"runner_requests", "runner_events"} {
			if err := tx.Create(&phasedCheckpoint{Name: name}).Error; err != nil {
				return err
			}
		}
		return m.installTargetGuards(tx)
	}); err != nil {
		return err
	}
	return m.load()
}
