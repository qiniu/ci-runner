package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"

	"gorm.io/gorm/schema"
)

const onlineMigrationProtocolVersion = 1

var errOnlineMigrationNotReady = errors.New("online migration is not ready")

// The inventory is deliberately explicit: retired tables and arbitrary tables
// found in an operator's database must not become active migration inputs.
func onlineMigrationModels() []any {
	return []any{
		&runnerRequestRecord{}, &runnerEventRecord{}, &runnerProfileRecord{},
		&scopedRunnerProfileRecord{}, &deploymentRecord{}, &deploymentRevisionRecord{},
		&deploymentEventRecord{}, &auditEventRecord{}, &accountRecord{}, &oauthIdentityRecord{},
		&githubInstallationRecord{}, &githubInstallationOwnerRecord{},
		&forkSponsorshipPolicyRecord{}, &forkSponsorshipApprovalRecord{},
		&accountSecretRecord{}, &accountPreferenceRecord{},
		&sandboxServiceDefaultRecord{}, &sandboxServiceDefaultAudienceRecord{},
	}
}

type onlineMigrationColumn struct {
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Size          int    `json:"size"`
	Precision     int    `json:"precision"`
	Scale         int    `json:"scale"`
	PrimaryKey    bool   `json:"primary_key"`
	AutoIncrement bool   `json:"auto_increment"`
	NotNull       bool   `json:"not_null"`
	Declaration   string `json:"declaration"`
}

type onlineMigrationTable struct {
	Name    string                  `json:"name"`
	Columns []onlineMigrationColumn `json:"columns"`
}

type onlineMigrationInventory struct {
	ProtocolVersion int                    `json:"protocol_version"`
	Tables          []onlineMigrationTable `json:"tables"`
}

func newOnlineMigrationInventory() (onlineMigrationInventory, error) {
	return describeOnlineMigrationModels(onlineMigrationModels())
}

func describeOnlineMigrationModels(models []any) (onlineMigrationInventory, error) {
	inventory := onlineMigrationInventory{ProtocolVersion: onlineMigrationProtocolVersion}
	seen := make(map[string]bool, len(models))
	var cache sync.Map
	for _, model := range models {
		s, err := schema.Parse(model, &cache, schema.NamingStrategy{})
		if err != nil {
			return onlineMigrationInventory{}, fmt.Errorf("parse migration model: %w", err)
		}
		if !validMigrationIdentifier(s.Table) || seen[s.Table] {
			return onlineMigrationInventory{}, errors.New("invalid or duplicate migration table")
		}
		seen[s.Table] = true
		table := onlineMigrationTable{Name: s.Table}
		for _, field := range s.Fields {
			// Associations have no physical DB column. Explicitly ignored model
			// fields must not leak into the transfer schema either.
			if field.DBName == "" || field.IgnoreMigration {
				continue
			}
			if !validMigrationIdentifier(field.DBName) {
				return onlineMigrationInventory{}, errors.New("invalid migration column")
			}
			switch field.GORMDataType {
			case schema.Bool, schema.Int, schema.Uint, schema.String, schema.Time, schema.Bytes, schema.Float:
			default:
				return onlineMigrationInventory{}, fmt.Errorf("unsupported migration type for %s.%s", s.Table, field.DBName)
			}
			table.Columns = append(table.Columns, onlineMigrationColumn{
				Name: field.DBName, Kind: string(field.GORMDataType),
				Size: field.Size, Precision: field.Precision, Scale: field.Scale,
				PrimaryKey: field.PrimaryKey, AutoIncrement: field.AutoIncrement,
				NotNull: field.NotNull, Declaration: field.Tag.Get("gorm"),
			})
		}
		if len(s.PrimaryFields) == 0 || len(table.Columns) == 0 {
			return onlineMigrationInventory{}, errors.New("migration table requires columns and a primary key")
		}
		sort.Slice(table.Columns, func(i, j int) bool { return table.Columns[i].Name < table.Columns[j].Name })
		inventory.Tables = append(inventory.Tables, table)
	}
	if len(inventory.Tables) == 0 {
		return onlineMigrationInventory{}, errors.New("migration inventory is empty")
	}
	sort.Slice(inventory.Tables, func(i, j int) bool { return inventory.Tables[i].Name < inventory.Tables[j].Name })
	return inventory, nil
}

func (i onlineMigrationInventory) fingerprint() (string, error) {
	encoded, err := json.Marshal(i)
	if err != nil {
		return "", fmt.Errorf("encode migration inventory: %w", err)
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func validMigrationIdentifier(value string) bool {
	if value == "" {
		return false
	}
	for i, r := range value {
		if (r < 'a' || r > 'z') && r != '_' && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

type onlineMigrationPhase string

const (
	onlineMigrationPlanned          onlineMigrationPhase = "planned"
	onlineMigrationCapturing        onlineMigrationPhase = "capturing"
	onlineMigrationBaseline         onlineMigrationPhase = "baseline"
	onlineMigrationCatchingUp       onlineMigrationPhase = "catching_up"
	onlineMigrationReady            onlineMigrationPhase = "ready"
	onlineMigrationCutoverPrepared  onlineMigrationPhase = "cutover_prepared"
	onlineMigrationPostgresPrimary  onlineMigrationPhase = "postgres_primary"
	onlineMigrationRollbackPrepared onlineMigrationPhase = "rollback_prepared"
	onlineMigrationSQLiteRestored   onlineMigrationPhase = "sqlite_restored"
	onlineMigrationCompleted        onlineMigrationPhase = "completed"
	onlineMigrationAborted          onlineMigrationPhase = "aborted"
)

// permits only describes protocol edges. It is not authority to switch a Store:
// prepared -> primary additionally requires a durable authority commit.
func (p onlineMigrationPhase) permits(next onlineMigrationPhase) bool {
	switch p {
	case onlineMigrationPlanned:
		return next == onlineMigrationCapturing || next == onlineMigrationAborted
	case onlineMigrationCapturing:
		return next == onlineMigrationBaseline || next == onlineMigrationAborted
	case onlineMigrationBaseline:
		return next == onlineMigrationCatchingUp || next == onlineMigrationAborted
	case onlineMigrationCatchingUp:
		return next == onlineMigrationReady || next == onlineMigrationAborted
	case onlineMigrationReady:
		return next == onlineMigrationCatchingUp || next == onlineMigrationCutoverPrepared || next == onlineMigrationAborted
	case onlineMigrationCutoverPrepared:
		return next == onlineMigrationPostgresPrimary || next == onlineMigrationCatchingUp
	case onlineMigrationPostgresPrimary:
		return next == onlineMigrationRollbackPrepared || next == onlineMigrationCompleted
	case onlineMigrationRollbackPrepared:
		return next == onlineMigrationSQLiteRestored || next == onlineMigrationPostgresPrimary
	case onlineMigrationSQLiteRestored:
		return next == onlineMigrationCompleted
	default:
		return false
	}
}

// Evidence belongs to one drained primary epoch and final committed checkpoint.
// A caller must build it from the real barrier/replicator/verifier, not from
// operator-supplied booleans or an earlier "replication lag = 0" observation.
type onlineMigrationCutoverEvidence struct {
	ProtocolVersion     int
	Epoch               uint64
	SchemaFingerprint   string
	SourceCheckpoint    uint64
	TargetCheckpoint    uint64
	VerifiedCheckpoint  uint64
	BaselineComplete    bool
	CaptureComplete     bool
	SourceDrained       bool
	TargetHealthy       bool
	ReverseCaptureReady bool
	SequencesReady      bool
}

func (e onlineMigrationCutoverEvidence) validate(epoch uint64, fingerprint string) error {
	if e.ProtocolVersion != onlineMigrationProtocolVersion {
		return fmt.Errorf("%w: incompatible protocol", errOnlineMigrationNotReady)
	}
	if epoch == 0 || e.Epoch != epoch {
		return fmt.Errorf("%w: stale primary epoch", errOnlineMigrationNotReady)
	}
	digest, err := hex.DecodeString(fingerprint)
	if err != nil || len(digest) != sha256.Size || e.SchemaFingerprint != fingerprint {
		return fmt.Errorf("%w: incompatible schema", errOnlineMigrationNotReady)
	}
	if !e.BaselineComplete || !e.CaptureComplete || !e.SourceDrained || !e.TargetHealthy || !e.ReverseCaptureReady || !e.SequencesReady {
		return fmt.Errorf("%w: incomplete cutover preparation", errOnlineMigrationNotReady)
	}
	// Zero is valid for an empty source, but only after all preparation above.
	if e.SourceCheckpoint != e.TargetCheckpoint || e.SourceCheckpoint != e.VerifiedCheckpoint {
		return fmt.Errorf("%w: replication or verification checkpoint differs", errOnlineMigrationNotReady)
	}
	return nil
}
