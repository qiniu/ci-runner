package state

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestOfflineMigrationConnectionFailureRedaction(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{pgconn.NewParseConfigError("private-dsn", "private-password", nil), "invalid DSN"},
		{&pgconn.PgError{Code: "28P01", Message: "private-password"}, "authentication rejected"},
		{&pgconn.PgError{Code: "3D000", Message: "private-db"}, "database does not exist"},
		{&pgconn.PgError{Code: "private-code", Message: "private-message"}, "connection rejected by server"},
		{&net.DNSError{Name: "private-host", Err: "private-error"}, "DNS lookup failed"},
		{&tls.CertificateVerificationError{Err: errors.New("private-certificate")}, "TLS certificate verification failed"},
		{errors.New("private-unknown"), "connection initialization failed"},
	} {
		got := offlinePostgresConnectionFailure(fmt.Errorf("private-wrapper: %w", tc.err))
		if !strings.Contains(got, tc.want) || strings.Contains(got, "private-") {
			t.Fatalf("classification = %q, want %q without private data", got, tc.want)
		}
	}
}

func TestOfflineMigrationConnectionPreflightBeforeBackup(t *testing.T) {
	source := New(t.TempDir()).(*DBStore)
	db, err := source.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	backup := filepath.Join(offlinePrivateDir(t), "backup.db")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var events []OfflineMigrationProgress
	_, err = MigrateSQLiteToPostgres(ctx, OfflineMigrationOptions{
		SQLitePath: source.opts.DatabaseDSN, BackupPath: backup, PostgresDSN: "private-invalid-dsn",
		Progress: func(p OfflineMigrationProgress) { events = append(events, p) },
	})
	if err == nil || !strings.Contains(err.Error(), "connect PostgreSQL target: invalid DSN") || strings.Contains(err.Error(), "private-") {
		t.Fatalf("unexpected preflight error: %v", err)
	}
	if _, err := os.Stat(backup); !os.IsNotExist(err) {
		t.Fatalf("backup was created before target connection succeeded: %v", err)
	}
	if len(events) == 0 || events[len(events)-1].Stage != "connect PostgreSQL target" {
		t.Fatalf("unexpected stages: %+v", events)
	}
}

func TestOfflineMigrationProgressCancellationRollsBackSQLBackends(t *testing.T) {
	dsn := offlinePostgresDSN(t)
	source := New(t.TempDir()).(*DBStore)
	db, err := source.dbOrEnsure()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, db)
	for i := 0; i < 520; i++ {
		if err := db.Create(&runnerEventRecord{RequestID: "private-request", Message: "private-message", CreatedAt: time.Now()}).Error; err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var events []OfflineMigrationProgress
	report, err := MigrateSQLiteToPostgres(ctx, OfflineMigrationOptions{
		SQLitePath: source.opts.DatabaseDSN, BackupPath: filepath.Join(offlinePrivateDir(t), "backup.db"), PostgresDSN: dsn,
		Progress: func(p OfflineMigrationProgress) {
			events = append(events, p)
			if p.Stage == "verify table runner_events" && p.Rows == 256 {
				cancel()
			}
		},
	})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "verify table runner_events") || report.Verified {
		t.Fatalf("cancellation result: verified=%t error=%v", report.Verified, err)
	}
	var copied, verified int64
	for _, event := range events {
		if event.Stage == "copy table runner_events" {
			copied = event.Rows
		}
		if event.Stage == "verify table runner_events" {
			verified = event.Rows
		}
		if event.Stage == "migration complete" || strings.Contains(fmt.Sprint(event), "private-") {
			t.Fatalf("invalid progress event: %+v", event)
		}
	}
	if copied != 520 || verified != 256 {
		t.Fatalf("copied=%d verified=%d", copied, verified)
	}
	target := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn}).(*DBStore)
	pg, err := target.open()
	if err != nil {
		t.Fatal(err)
	}
	defer closeTestDB(t, pg)
	tables, err := pg.Migrator().GetTables()
	if err != nil || len(tables) != 0 {
		t.Fatalf("cancelled import left tables: %v %v", tables, err)
	}
}
