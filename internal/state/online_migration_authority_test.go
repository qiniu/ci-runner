package state

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testMigrationManifest(t *testing.T) onlineMigrationManifest {
	t.Helper()
	inventory, err := newOnlineMigrationInventory()
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := inventory.fingerprint()
	if err != nil {
		t.Fatal(err)
	}
	return onlineMigrationManifest{
		ProtocolVersion: onlineMigrationProtocolVersion, Revision: 1, MigrationID: "test-migration",
		SchemaFingerprint: fingerprint, SQLiteInstanceID: "sqlite-source", PostgresInstanceID: "postgres-target",
		Primary: BackendSQLite, Epoch: 1, Phase: onlineMigrationPlanned,
	}
}

func commitTestManifest(t *testing.T, authority *onlineMigrationAuthority, next onlineMigrationManifest) {
	t.Helper()
	if published, err := authority.commit(context.Background(), next); err != nil || !published {
		t.Fatalf("manifest commit published=%v err=%v", published, err)
	}
}

func TestOnlineMigrationAuthorityCommitAndControlledRollback(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	authority, err := openOnlineMigrationAuthority(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if _, exists, err := authority.read(); err != nil || exists {
		t.Fatalf("fresh authority exists=%v err=%v", exists, err)
	}
	manifest := testMigrationManifest(t)
	commitTestManifest(t, authority, manifest)
	if second, err := openOnlineMigrationAuthority(directory); err == nil {
		_ = second.Close()
		t.Fatal("another lock owner was accepted")
	}
	invalid := manifest
	invalid.Revision++
	invalid.Primary, invalid.Phase, invalid.Epoch = BackendPostgres, onlineMigrationPostgresPrimary, 2
	if published, err := authority.commit(context.Background(), invalid); published || !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("authority skipped preparation: published=%v err=%v", published, err)
	}
	for _, phase := range []onlineMigrationPhase{onlineMigrationCapturing, onlineMigrationBaseline, onlineMigrationCatchingUp, onlineMigrationReady, onlineMigrationCutoverPrepared} {
		manifest.Revision++
		manifest.Phase = phase
		if phase == onlineMigrationCutoverPrepared {
			manifest.Prepared = &migrationPreparedAuthority{From: BackendSQLite, To: BackendPostgres, NextEpoch: 2, SourcePosition: 17, VerifiedPosition: 17}
		}
		commitTestManifest(t, authority, manifest)
	}
	// The durable prepared record still selects SQLite. A mutated caller copy
	// must not mutate the authority retained inside the process.
	copy, _, err := authority.read()
	if err != nil || copy.Primary != BackendSQLite || copy.Epoch != 1 {
		t.Fatalf("preparation changed primary: %v", err)
	}
	copy.Prepared.NextEpoch = 99
	copy, _, _ = authority.read()
	if copy.Prepared.NextEpoch != 2 {
		t.Fatal("authority retained a caller-owned pointer")
	}
	manifest.Revision++
	manifest.Primary, manifest.Epoch, manifest.Phase, manifest.Prepared = BackendPostgres, 2, onlineMigrationPostgresPrimary, nil
	commitTestManifest(t, authority, manifest)
	invalid = manifest
	invalid.Revision++
	invalid.Primary, invalid.Epoch, invalid.Phase = BackendSQLite, 1, onlineMigrationSQLiteRestored
	if published, err := authority.commit(context.Background(), invalid); published || !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("authority silently returned to old epoch: published=%v err=%v", published, err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	authority, err = openOnlineMigrationAuthority(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	recovered, exists, err := authority.read()
	if err != nil || !exists || recovered.Primary != BackendPostgres || recovered.Epoch != 2 {
		t.Fatalf("reopen lost durable authority: %v", err)
	}
	manifest = recovered
	manifest.Revision++
	manifest.Phase = onlineMigrationRollbackPrepared
	manifest.Prepared = &migrationPreparedAuthority{From: BackendPostgres, To: BackendSQLite, NextEpoch: 3, SourcePosition: 8, VerifiedPosition: 8}
	commitTestManifest(t, authority, manifest)
	manifest.Revision++
	manifest.Primary, manifest.Epoch, manifest.Phase, manifest.Prepared = BackendSQLite, 3, onlineMigrationSQLiteRestored, nil
	commitTestManifest(t, authority, manifest)
	if published, err := authority.commit(context.Background(), manifest); published || !errors.Is(err, errOnlineMigrationNotReady) {
		t.Fatalf("stale manifest revision was accepted: %v", err)
	}
}

func TestOnlineMigrationAuthorityIOFailurePublicationBoundary(t *testing.T) {
	for _, stage := range []string{"file sync", "rename", "directory sync", "cancelled"} {
		t.Run(stage, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "private")
			authority, err := openOnlineMigrationAuthority(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()
			manifest := testMigrationManifest(t)
			commitTestManifest(t, authority, manifest)
			next := manifest
			next.Revision++
			next.Phase = onlineMigrationCapturing
			fault := errors.New("injected disk failure")
			ctx := context.Background()
			switch stage {
			case "file sync":
				authority.syncFile = func(*os.File) error { return fault }
			case "rename":
				authority.rename = func(string, string) error { return fault }
			case "directory sync":
				authority.syncDirectory = func(string) error { return fault }
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			published, err := authority.commit(ctx, next)
			if err == nil || published != (stage == "directory sync") {
				t.Fatalf("wrong publication boundary: published=%v err=%v", published, err)
			}
			if stage == "directory sync" {
				if _, _, err := authority.read(); !errors.Is(err, errOnlineMigrationNotReady) {
					t.Fatalf("uncertain publication remained usable: %v", err)
				}
				if _, err := authority.commit(context.Background(), next); !errors.Is(err, errOnlineMigrationNotReady) {
					t.Fatalf("uncertain publication accepted a second commit: %v", err)
				}
			}
			if err := authority.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := openOnlineMigrationAuthority(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			current, _, err := reopened.read()
			if err != nil {
				t.Fatal(err)
			}
			want := manifest.Revision
			if published {
				want = next.Revision
			}
			if current.Revision != want {
				t.Fatalf("recovery chose revision %d instead of %d", current.Revision, want)
			}
			entries, err := os.ReadDir(directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".authority-") {
					t.Fatal("commit failure retained its temporary file")
				}
			}
		})
	}
}

func TestOnlineMigrationAuthorityRejectsCorruptOrUnsafeManifest(t *testing.T) {
	encoded, err := encodeMigrationManifest(testMigrationManifest(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, corrupt := range [][]byte{
		encoded[:len(encoded)/2], append(append([]byte{}, encoded...), []byte("{}")...),
		bytes.Replace(encoded, []byte(`"epoch": 1`), []byte(`"epoch": 2`), 1),
		bytes.Replace(encoded, []byte(`"epoch": 1`), []byte(`"epoch": 1, "epoch": 1`), 1),
	} {
		if _, err := decodeMigrationManifest(corrupt); !errors.Is(err, errOnlineMigrationNotReady) {
			t.Fatalf("corrupt manifest accepted: %v", err)
		}
	}
	directory := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, migrationManifestFilename)
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if authority, err := openOnlineMigrationAuthority(directory); err == nil {
		_ = authority.Close()
		t.Fatal("publicly readable manifest was accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(directory, "missing"), path); err != nil {
		t.Fatal(err)
	}
	if authority, err := openOnlineMigrationAuthority(directory); err == nil {
		_ = authority.Close()
		t.Fatal("symlinked manifest was accepted as missing")
	}
}

func TestOnlineMigrationAuthorityCancellationAfterRenameFinishesDurability(t *testing.T) {
	authority, err := openOnlineMigrationAuthority(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	manifest := testMigrationManifest(t)
	commitTestManifest(t, authority, manifest)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	authority.rename = func(from, to string) error {
		err := os.Rename(from, to)
		cancel()
		return err
	}
	manifest.Revision++
	manifest.Phase = onlineMigrationCapturing
	if published, err := authority.commit(ctx, manifest); !published || err != nil {
		t.Fatalf("cancellation interrupted publication durability: published=%v err=%v", published, err)
	}
	current, _, err := authority.read()
	if err != nil || current.Revision != manifest.Revision {
		t.Fatalf("completed publication was not retained: %v", err)
	}
}

func TestOnlineMigrationAuthorityProcessExitAtCommitBoundaries(t *testing.T) {
	for _, stage := range []string{"file sync", "rename", "directory sync"} {
		t.Run(stage, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "private")
			authority, err := openOnlineMigrationAuthority(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer authority.Close()
			manifest := testMigrationManifest(t)
			commitTestManifest(t, authority, manifest)
			for _, phase := range []onlineMigrationPhase{onlineMigrationCapturing, onlineMigrationBaseline, onlineMigrationCatchingUp, onlineMigrationReady, onlineMigrationCutoverPrepared} {
				manifest.Revision++
				manifest.Phase = phase
				if phase == onlineMigrationCutoverPrepared {
					manifest.Prepared = &migrationPreparedAuthority{From: BackendSQLite, To: BackendPostgres, NextEpoch: 2}
				}
				commitTestManifest(t, authority, manifest)
			}
			// A second process cannot own the authority while this one runs.
			command := exec.Command(os.Args[0], "-test.run=^TestOnlineMigrationAuthorityProcessHelper$")
			command.Env = append(os.Environ(), "RUNNERD_TEST_AUTHORITY_DIRECTORY="+directory, "RUNNERD_TEST_AUTHORITY_STAGE=lock")
			if output, err := command.CombinedOutput(); err != nil {
				t.Fatalf("exclusive process lock failed: %v %s", err, output)
			}
			if err := authority.Close(); err != nil {
				t.Fatal(err)
			}
			command = exec.Command(os.Args[0], "-test.run=^TestOnlineMigrationAuthorityProcessHelper$")
			command.Env = append(os.Environ(), "RUNNERD_TEST_AUTHORITY_DIRECTORY="+directory, "RUNNERD_TEST_AUTHORITY_STAGE="+stage)
			if output, err := command.CombinedOutput(); err == nil {
				t.Fatal("fault child did not exit at the commit boundary")
			} else {
				var exited *exec.ExitError
				if !errors.As(err, &exited) || exited.ExitCode() != 23 {
					t.Fatalf("unexpected child error: %v %s", err, output)
				}
			}
			recovered, err := openOnlineMigrationAuthority(directory)
			if err != nil {
				t.Fatal(err)
			}
			defer recovered.Close()
			current, _, err := recovered.read()
			if err != nil {
				t.Fatal(err)
			}
			wantPrimary, wantEpoch := BackendPostgres, int64(2)
			if stage == "file sync" {
				wantPrimary, wantEpoch = BackendSQLite, 1
			}
			if current.Primary != wantPrimary || current.Epoch != wantEpoch {
				t.Fatalf("crash selected primary=%s epoch=%d, want %s/%d", current.Primary, current.Epoch, wantPrimary, wantEpoch)
			}
		})
	}
}

func TestOnlineMigrationAuthorityProcessHelper(t *testing.T) {
	directory := os.Getenv("RUNNERD_TEST_AUTHORITY_DIRECTORY")
	if directory == "" {
		t.Skip("authority fault subprocess only")
	}
	stage := os.Getenv("RUNNERD_TEST_AUTHORITY_STAGE")
	authority, err := openOnlineMigrationAuthority(directory)
	if stage == "lock" {
		if err == nil {
			_ = authority.Close()
			t.Fatal("child acquired another process's authority lock")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	switch stage {
	case "file sync":
		authority.syncFile = func(file *os.File) error {
			if err := file.Sync(); err != nil {
				return err
			}
			os.Exit(23)
			return nil
		}
	case "rename":
		authority.rename = func(from, to string) error {
			if err := os.Rename(from, to); err != nil {
				return err
			}
			os.Exit(23)
			return nil
		}
	case "directory sync":
		authority.syncDirectory = func(path string) error {
			if err := syncMigrationDirectory(path); err != nil {
				return err
			}
			os.Exit(23)
			return nil
		}
	default:
		t.Fatal("unknown authority fault stage")
	}
	manifest, _, err := authority.read()
	if err != nil {
		t.Fatal(err)
	}
	manifest.Revision++
	manifest.Primary, manifest.Epoch, manifest.Phase, manifest.Prepared = BackendPostgres, 2, onlineMigrationPostgresPrimary, nil
	if _, err := authority.commit(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	t.Fatal("fault child completed without exiting")
}
