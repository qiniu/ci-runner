package state

import (
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qiniu/ci-runner/internal/config"
)

func TestPostgresPoolBudgetSQLBackends(t *testing.T) {
	dsn := isolatedPostgresDSN(t)
	for _, limit := range []int{0, 1, 4} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			store := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn, PostgresMaxOpenConns: limit}).(*DBStore)
			db, err := store.open()
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestDB(t, db)
			pool, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			if got := pool.Stats().MaxOpenConnections; got != limit {
				t.Fatalf("got %d, want %d", got, limit)
			}
		})
	}
}

func TestPostgresObfuscatedPasswordSQLBackends(t *testing.T) {
	for _, mode := range []string{"password", "whole DSN"} {
		t.Run(mode, func(t *testing.T) {
			dsn := isolatedPostgresDSN(t)
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
			target := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn, MigrateOnStart: true}).(*DBStore)
			pg, err := target.dbOrEnsure()
			if err != nil {
				t.Fatal(err)
			}
			defer closeTestDB(t, pg)
			if _, err := target.UpsertProfile(RunnerProfile{Name: "password-test", Labels: []string{"qiniu"}}); err != nil {
				t.Fatal(err)
			}
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

func isolatedPostgresDSN(t *testing.T) string {
	t.Helper()
	if os.Getenv("RUNNERD_CATALOG_BACKEND_TESTS") != "1" {
		t.Skip("requires dedicated PostgreSQL test database")
	}
	dsn := os.Getenv("RUNNERD_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Fatal("missing PostgreSQL test DSN")
	}
	store := NewWithOptions(Options{Backend: BackendPostgres, DatabaseDSN: dsn}).(*DBStore)
	db, err := store.open()
	if err != nil {
		t.Fatal(err)
	}
	requireCatalogMatcherTestDatabase(t, db, BackendPostgres)
	schema := fmt.Sprintf("state_%d", time.Now().UnixNano())
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Exec("DROP SCHEMA " + schema + " CASCADE"); closeTestDB(t, db) })
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		parsed, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return dsn + " search_path=" + schema
}
