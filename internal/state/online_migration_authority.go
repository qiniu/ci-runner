package state

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

type migrationPreparedAuthority struct {
	From             string `json:"from"`
	To               string `json:"to"`
	NextEpoch        int64  `json:"next_epoch"`
	SourcePosition   int64  `json:"source_position"`
	VerifiedPosition int64  `json:"verified_position"`
}

// Connection configuration belongs in protected config, never the manifest.
// The primary/epoch in this file is the sole durable authority commit point;
// database role records are recoverable fences, not competing authorities.
type onlineMigrationManifest struct {
	ProtocolVersion    int                         `json:"protocol_version"`
	Revision           uint64                      `json:"revision"`
	MigrationID        string                      `json:"migration_id"`
	SchemaFingerprint  string                      `json:"schema_fingerprint"`
	SQLiteInstanceID   string                      `json:"sqlite_instance_id"`
	PostgresInstanceID string                      `json:"postgres_instance_id"`
	Primary            string                      `json:"primary"`
	Epoch              int64                       `json:"epoch"`
	Phase              onlineMigrationPhase        `json:"phase"`
	SnapshotFile       string                      `json:"snapshot_file,omitempty"`
	SnapshotDigest     string                      `json:"snapshot_digest,omitempty"`
	Prepared           *migrationPreparedAuthority `json:"prepared,omitempty"`
}

func (m onlineMigrationManifest) validate() error {
	invalid := fmt.Errorf("%w: invalid authority manifest", errOnlineMigrationNotReady)
	fingerprint, err := hex.DecodeString(m.SchemaFingerprint)
	if err != nil || len(fingerprint) != sha256.Size || m.ProtocolVersion != onlineMigrationProtocolVersion ||
		m.Revision == 0 || m.Epoch <= 0 || m.Epoch == math.MaxInt64 || !validMigrationIdentity(m.MigrationID) ||
		!validMigrationIdentity(m.SQLiteInstanceID) || !validMigrationIdentity(m.PostgresInstanceID) || m.SQLiteInstanceID == m.PostgresInstanceID {
		return invalid
	}
	if (m.SnapshotFile == "") != (m.SnapshotDigest == "") {
		return invalid
	}
	if m.SnapshotFile != "" {
		digest, err := hex.DecodeString(m.SnapshotDigest)
		if err != nil || len(digest) != sha256.Size || filepath.Base(m.SnapshotFile) != m.SnapshotFile ||
			m.SnapshotFile == "." || m.SnapshotFile == ".." || len(m.SnapshotFile) > 128 {
			return invalid
		}
	}
	switch m.Primary {
	case BackendSQLite:
		switch m.Phase {
		case onlineMigrationPlanned, onlineMigrationCapturing, onlineMigrationBaseline, onlineMigrationCatchingUp,
			onlineMigrationReady, onlineMigrationCutoverPrepared, onlineMigrationSQLiteRestored, onlineMigrationCompleted, onlineMigrationAborted:
		default:
			return invalid
		}
	case BackendPostgres:
		if m.Phase != onlineMigrationPostgresPrimary && m.Phase != onlineMigrationRollbackPrepared && m.Phase != onlineMigrationCompleted {
			return invalid
		}
	default:
		return invalid
	}
	prepared := m.Phase == onlineMigrationCutoverPrepared || m.Phase == onlineMigrationRollbackPrepared
	if prepared != (m.Prepared != nil) {
		return invalid
	}
	if prepared {
		p := m.Prepared
		if p.From != m.Primary || p.To == m.Primary || (p.To != BackendSQLite && p.To != BackendPostgres) ||
			p.NextEpoch != m.Epoch+1 || p.SourcePosition < 0 || p.VerifiedPosition != p.SourcePosition {
			return invalid
		}
	}
	return nil
}

func (m onlineMigrationManifest) follows(previous onlineMigrationManifest) bool {
	if previous.Revision == math.MaxUint64 || m.Revision != previous.Revision+1 ||
		m.ProtocolVersion != previous.ProtocolVersion || m.MigrationID != previous.MigrationID ||
		m.SchemaFingerprint != previous.SchemaFingerprint || m.SQLiteInstanceID != previous.SQLiteInstanceID ||
		m.PostgresInstanceID != previous.PostgresInstanceID ||
		(m.Phase != previous.Phase && !previous.Phase.permits(m.Phase)) {
		return false
	}
	if m.Primary == previous.Primary {
		return m.Epoch == previous.Epoch
	}
	return previous.Prepared != nil && m.Prepared == nil && m.Primary == previous.Prepared.To &&
		m.Epoch == previous.Prepared.NextEpoch &&
		((previous.Phase == onlineMigrationCutoverPrepared && m.Phase == onlineMigrationPostgresPrimary) ||
			(previous.Phase == onlineMigrationRollbackPrepared && m.Phase == onlineMigrationSQLiteRestored))
}

type migrationManifestEnvelope struct {
	Manifest onlineMigrationManifest `json:"manifest"`
	SHA256   string                  `json:"sha256"`
}

func encodeMigrationManifest(manifest onlineMigrationManifest) ([]byte, error) {
	if err := manifest.validate(); err != nil {
		return nil, err
	}
	content, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(content)
	encoded, err := json.MarshalIndent(migrationManifestEnvelope{Manifest: manifest, SHA256: hex.EncodeToString(digest[:])}, "", "  ")
	return append(encoded, '\n'), err
}

func decodeMigrationManifest(encoded []byte) (onlineMigrationManifest, error) {
	var envelope migrationManifestEnvelope
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return onlineMigrationManifest{}, fmt.Errorf("%w: unreadable authority manifest", errOnlineMigrationNotReady)
	}
	canonical, err := encodeMigrationManifest(envelope.Manifest)
	// Exact canonical encoding also rejects duplicate keys, trailing values,
	// checksum damage and hand-edited ambiguity without printing any contents.
	if err != nil || !bytes.Equal(canonical, encoded) {
		return onlineMigrationManifest{}, fmt.Errorf("%w: corrupt authority manifest", errOnlineMigrationNotReady)
	}
	return envelope.Manifest, nil
}

type onlineMigrationAuthority struct {
	mu        sync.Mutex
	directory string
	lock      *os.File
	current   *onlineMigrationManifest
	uncertain bool

	syncFile      func(*os.File) error
	rename        func(string, string) error
	syncDirectory func(string) error
}

const migrationManifestFilename = "authority.json"

func syncMigrationDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	return errors.Join(directory.Sync(), directory.Close())
}

// The immediate parent must already exist. Syncing it when creating the private
// directory makes the directory entry durable before it can hold authority.
func openOnlineMigrationAuthority(directory string) (_ *onlineMigrationAuthority, resultErr error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(absolute, 0o700); err == nil {
		if err := syncMigrationDirectory(filepath.Dir(absolute)); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: authority directory must be private", errOnlineMigrationNotReady)
	}
	lock, err := os.OpenFile(filepath.Join(absolute, "authority.lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	defer func() {
		if resultErr != nil {
			_ = lock.Close()
		}
	}()
	info, err = lock.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: authority lock must be a private regular file", errOnlineMigrationNotReady)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, fmt.Errorf("%w: authority directory is already locked", errOnlineMigrationNotReady)
	}
	authority := &onlineMigrationAuthority{
		directory: absolute, lock: lock, syncFile: (*os.File).Sync, rename: os.Rename, syncDirectory: syncMigrationDirectory,
	}
	manifest, err := readMigrationManifestFile(filepath.Join(absolute, migrationManifestFilename))
	if errors.Is(err, os.ErrNotExist) {
		return authority, nil
	}
	if err != nil {
		return nil, err
	}
	// A prior process might have stopped after rename but before directory
	// fsync. Confirm the currently observed file before trusting it at startup.
	if err := syncMigrationSnapshot(filepath.Join(absolute, migrationManifestFilename), absolute); err != nil {
		return nil, err
	}
	authority.current = &manifest
	return authority, nil
}

func readMigrationManifestFile(path string) (onlineMigrationManifest, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return onlineMigrationManifest{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return onlineMigrationManifest{}, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || info.Size() > 65536 {
		return onlineMigrationManifest{}, fmt.Errorf("%w: authority manifest must be a bounded private regular file", errOnlineMigrationNotReady)
	}
	encoded, err := io.ReadAll(io.LimitReader(file, 65537))
	if err != nil {
		return onlineMigrationManifest{}, err
	}
	return decodeMigrationManifest(encoded)
}

func (a *onlineMigrationAuthority) read() (onlineMigrationManifest, bool, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lock == nil || a.uncertain {
		return onlineMigrationManifest{}, false, fmt.Errorf("%w: authority is closed or durability is uncertain", errOnlineMigrationNotReady)
	}
	if a.current == nil {
		return onlineMigrationManifest{}, false, nil
	}
	copy := *a.current
	if copy.Prepared != nil {
		prepared := *copy.Prepared
		copy.Prepared = &prepared
	}
	return copy, true, nil
}

// published reports whether rename happened, even if its directory fsync failed.
// Once true, the caller MUST NOT restore the old authority on an error. Keep both
// databases fenced until recovery can confirm the observed durable manifest.
// Cancellation is checked before rename; publication always finishes its fsync.
func (a *onlineMigrationAuthority) commit(ctx context.Context, next onlineMigrationManifest) (published bool, resultErr error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lock == nil || a.uncertain {
		return false, fmt.Errorf("%w: authority is closed or durability is uncertain", errOnlineMigrationNotReady)
	}
	if a.current == nil {
		if next.Revision != 1 || next.Primary != BackendSQLite || next.Epoch != 1 || next.Phase != onlineMigrationPlanned {
			return false, fmt.Errorf("%w: invalid initial authority", errOnlineMigrationNotReady)
		}
	} else if !next.follows(*a.current) {
		return false, fmt.Errorf("%w: stale or invalid authority transition", errOnlineMigrationNotReady)
	}
	encoded, err := encodeMigrationManifest(next)
	if err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	file, err := os.CreateTemp(a.directory, ".authority-*")
	if err != nil {
		return false, err
	}
	defer func() {
		_ = file.Close()
		_ = os.Remove(file.Name()) // Only this call's temporary file, never authority.json.
	}()
	if _, err := file.Write(encoded); err != nil {
		return false, err
	}
	if err := a.syncFile(file); err != nil {
		return false, err
	}
	if err := file.Close(); err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if err := a.rename(file.Name(), filepath.Join(a.directory, migrationManifestFilename)); err != nil {
		return false, err
	}
	published = true
	if err := a.syncDirectory(a.directory); err != nil {
		a.uncertain = true
		return true, err
	}
	// Avoid retaining a caller-owned Prepared pointer after a durable commit.
	committed, err := decodeMigrationManifest(encoded)
	if err != nil {
		a.uncertain = true
		return true, err
	}
	a.current = &committed
	return true, nil
}

func (a *onlineMigrationAuthority) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lock == nil {
		return nil
	}
	err := a.lock.Close() // Closing the descriptor releases its exclusive flock.
	a.lock = nil
	return err
}
