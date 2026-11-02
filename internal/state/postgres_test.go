package state

import (
	"context"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
)

func TestPostgresObfuscatedPasswordSQLBackends(t *testing.T) {
	for _, mode := range []string{"password", "whole DSN"} {
		t.Run(mode, func(t *testing.T) {
			dsn := offlinePostgresDSN(t)
			parsed, err := config.ParsePostgresDSN(config.Secret(dsn))
			if err != nil {
				t.Fatal(err)
			}
			if parsed.Password == "" {
				t.Skip("requires password-authenticated PostgreSQL fixture")
			}
			encoded, err := config.ObfuscateSecret([]byte(parsed.Password))
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
				u, err := url.Parse(dsn)
				if err != nil {
					t.Fatal("invalid test URL")
				}
				query := u.Query()
				query.Set("password", encoded)
				query.Set("timezone", "Asia/Shanghai")
				u.RawQuery = query.Encode()
				dsn = u.String()
			} else {
				dsn += " password=" + encoded + " TimeZone=Asia/Shanghai"
			}
			if mode == "whole DSN" {
				dsn, err = config.ObfuscateSecret([]byte(dsn))
				if err != nil {
					t.Fatal(err)
				}
			}
			source := New(t.TempDir()).(*DBStore)
			db, err := source.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestDB(t, db)
			if _, err := source.UpsertProfile(RunnerProfile{Name: "password-test", Labels: []string{"qiniu"}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			report, err := MigrateSQLiteToPostgres(ctx, OfflineMigrationOptions{
				SQLitePath: source.opts.DatabaseDSN, BackupPath: filepath.Join(offlinePrivateDir(t), "backup.db"), PostgresDSN: dsn,
			})
			if err != nil || !report.Verified {
				t.Fatalf("migration failed: %v", err)
			}
			target := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn}).(*DBStore)
			pg, err := target.open()
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestDB(t, pg)
			var profile runnerProfileRecord
			if err := pg.Where("name = ?", "password-test").First(&profile).Error; err != nil {
				t.Fatal(err)
			}
			var stamp time.Time
			if err := pg.Raw("SELECT TIMESTAMP '2026-11-02 12:00:00'").Scan(&stamp).Error; err != nil {
				t.Fatal(err)
			}
			if _, offset := stamp.Zone(); offset != 8*60*60 || stamp.Hour() != 12 {
				t.Fatalf("timestamp scan location changed: %s", stamp)
			}
		})
	}
}
